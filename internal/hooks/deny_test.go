package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func denySet(t *testing.T, script string, ev Event) (*Set, *[]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts here are POSIX sh")
	}
	var logs []string
	s := &Set{
		hooks:  map[Event][]string{ev: {script}},
		logger: func(m string) { logs = append(logs, m) },
	}
	return s, &logs
}

// A pre_tool hook exiting 77 must BLOCK the call. Without this, a hook can observe a
// dangerous tool call but not stop it, which makes hooks a logging side-channel rather
// than a security control.
func TestPreToolHookDenies(t *testing.T) {
	s, logs := denySet(t, `echo "writes outside the repo are not allowed"; exit 77`, PreTool)
	err := s.Run(context.Background(), PreTool, Context{ToolName: "write_file", WorkingDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected the call to be blocked")
	}
	var deny *DenyError
	if !errors.As(err, &deny) {
		t.Fatalf("expected a *DenyError, got %T: %v", err, err)
	}
	if deny.Tool != "write_file" {
		t.Errorf("Tool = %q, want write_file", deny.Tool)
	}
	// The hook's own output is the reason shown to the model, so the policy can
	// explain itself instead of the model seeing a bare "blocked".
	if !strings.Contains(deny.Error(), "writes outside the repo are not allowed") {
		t.Errorf("the hook's message must reach the caller: %q", deny.Error())
	}
	// A deny is an intended outcome; logging it as a failure would train users to
	// ignore hook errors.
	for _, l := range *logs {
		if strings.Contains(l, "hook failed") {
			t.Errorf("a deny must not be logged as a failure: %q", l)
		}
	}
}

// The crucial distinction: a BROKEN hook must stay advisory. Treating any non-zero
// exit as a deny would let a typo silently wedge every tool call.
func TestBrokenPreToolHookDoesNotDeny(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"syntax error (sh exits 2)", `if then fi`},
		{"command not found (127)", `definitely-not-a-real-command-xyz`},
		{"generic failure (1)", `exit 1`},
		{"other explicit code", `exit 42`},
		// 78 is adjacent to the deny code: an off-by-one must not block.
		{"near-miss code", `exit 78`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := denySet(t, tc.script, PreTool)
			if err := s.Run(context.Background(), PreTool, Context{ToolName: "bash", WorkingDir: t.TempDir()}); err != nil {
				t.Errorf("a broken hook must not block the agent, got %v", err)
			}
		})
	}
}

// A hook that hangs must not block tools either: it reports -1, never the deny code.
func TestHangingPreToolHookDoesNotDeny(t *testing.T) {
	s, _ := denySet(t, `sleep 30`, PreTool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately rather than waiting out the 10s timeout
	if err := s.Run(ctx, PreTool, Context{ToolName: "bash", WorkingDir: t.TempDir()}); err != nil {
		t.Errorf("a cancelled/hanging hook must not block, got %v", err)
	}
}

// Only pre_tool can deny. Denying post_tool is meaningless — the tool already ran —
// and pre_turn/post_turn have no call to block.
func TestOnlyPreToolCanDeny(t *testing.T) {
	for _, ev := range []Event{PostTool, PreTurn, PostTurn} {
		s, _ := denySet(t, `exit 77`, ev)
		if err := s.Run(context.Background(), ev, Context{ToolName: "bash", WorkingDir: t.TempDir()}); err != nil {
			t.Errorf("%s must not be deniable, got %v", ev, err)
		}
	}
}

// A zero-exit pre_tool hook must let the call through, or hooks would break everything
// the moment one is configured.
func TestPassingPreToolHookAllows(t *testing.T) {
	s, _ := denySet(t, `echo fine; exit 0`, PreTool)
	if err := s.Run(context.Background(), PreTool, Context{ToolName: "read_file", WorkingDir: t.TempDir()}); err != nil {
		t.Errorf("a passing hook must allow the call, got %v", err)
	}
}

// Run must stop at the first deny: later hooks shouldn't observe a call that is
// already refused, and one of them returning 0 must not overturn the refusal.
func TestDenyStopsLaterHooks(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran-second")
	s := &Set{hooks: map[Event][]string{PreTool: {
		`exit 77`,
		`touch ` + marker,
	}}}
	if err := s.Run(context.Background(), PreTool, Context{ToolName: "bash", WorkingDir: dir}); err == nil {
		t.Fatal("expected a deny")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a later hook ran after the call was already denied")
	}
}

// The hook must be able to make its decision from the tool name and input, so those
// have to be in the environment before the deny is evaluated.
func TestDenyCanInspectToolNameAndInput(t *testing.T) {
	// Deny only write_file to /etc, allow everything else.
	script := `case "$MN_TOOL_NAME" in
	  write_file) echo "$MN_TOOL_INPUT" | grep -q '/etc/' && { echo "no writes to /etc"; exit 77; } ;;
	esac
	exit 0`
	s := &Set{hooks: map[Event][]string{PreTool: {script}}}

	err := s.Run(context.Background(), PreTool, Context{
		ToolName: "write_file", ToolInput: `{"path":"/etc/passwd"}`, WorkingDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected the /etc write to be denied")
	}
	if !strings.Contains(err.Error(), "no writes to /etc") {
		t.Errorf("unexpected reason: %v", err)
	}
	// Same hook, harmless input: must pass.
	if err := s.Run(context.Background(), PreTool, Context{
		ToolName: "write_file", ToolInput: `{"path":"internal/x.go"}`, WorkingDir: t.TempDir(),
	}); err != nil {
		t.Errorf("an in-repo write must be allowed, got %v", err)
	}
	// A different tool entirely: must pass.
	if err := s.Run(context.Background(), PreTool, Context{
		ToolName: "read_file", ToolInput: `{"path":"/etc/passwd"}`, WorkingDir: t.TempDir(),
	}); err != nil {
		t.Errorf("read_file is not gated by this hook, got %v", err)
	}
}

func TestDenyErrorMessage(t *testing.T) {
	// With output, the reason is included.
	e := &DenyError{Tool: "bash", Output: "  dangerous  \n"}
	if got := e.Error(); got != "tool bash blocked by a pre_tool hook: dangerous" {
		t.Errorf("got %q", got)
	}
	// Without output, it still reads sensibly rather than trailing a colon.
	bare := &DenyError{Tool: "bash"}
	if got := bare.Error(); got != "tool bash blocked by a pre_tool hook" {
		t.Errorf("got %q", got)
	}
	if got := (&DenyError{}).Error(); got != "blocked by a pre_tool hook" {
		t.Errorf("got %q", got)
	}
}

// An empty set must not pay for the deny path.
func TestEmptySetNeverDenies(t *testing.T) {
	var s *Set
	if err := s.Run(context.Background(), PreTool, Context{ToolName: "bash"}); err != nil {
		t.Errorf("a nil set must be a no-op, got %v", err)
	}
	if err := (&Set{}).Run(context.Background(), PreTool, Context{ToolName: "bash"}); err != nil {
		t.Errorf("an empty set must be a no-op, got %v", err)
	}
}

// 77 is chosen precisely because the shell reserves the low codes; pin it so nobody
// "tidies" it into 1 or 2 and makes broken hooks indistinguishable from policy.
func TestDenyExitCodeIsOutsideShellReservedRange(t *testing.T) {
	if DenyExitCode != 77 {
		t.Errorf("DenyExitCode = %d, want 77", DenyExitCode)
	}
	for _, reserved := range []int{0, 1, 2, 126, 127, 128, 130} {
		if DenyExitCode == reserved {
			t.Errorf("DenyExitCode collides with the shell-reserved %d", reserved)
		}
	}
}
