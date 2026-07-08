package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/tools"
)

// failingTool always returns an error, to exercise the loop's error path.
type failingTool struct {
	name    string
	mutates bool
	calls   int
}

func (f *failingTool) Name() string            { return f.name }
func (f *failingTool) Description() string     { return f.name }
func (f *failingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *failingTool) Mutates() bool           { return f.mutates }
func (f *failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	f.calls++
	return "", fmt.Errorf("boom")
}

// erroringStreamer always fails its stream call, to exercise the loop's fatal
// error path.
type erroringStreamer struct{ err error }

func (e *erroringStreamer) StreamMessages(context.Context, string, []llm.Message, []llm.Tool, llm.Handler) (*llm.Response, error) {
	return nil, e.err
}
func (e *erroringStreamer) ListModels(context.Context) ([]config.ModelInfo, error) { return nil, nil }
func (e *erroringStreamer) Usage(context.Context) (*llm.UsageResponse, error)      { return nil, nil }
func (e *erroringStreamer) CheckAuth(context.Context) error                        { return nil }

// TestRunFeedsToolErrorBackAndContinues verifies that when a tool fails, the
// loop reports an error tool_result to the model and keeps going to end_turn
// rather than aborting the whole Run.
func TestRunFeedsToolErrorBackAndContinues(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	ft := &failingTool{name: "boomer"}
	reg.Register(ft)

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "boomer", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("handled it")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	var endErr bool
	var runErr = ag.Run(context.Background(), "go", func(ev Event) {
		if e, ok := ev.(ToolEnd); ok && e.IsError {
			endErr = true
		}
	})
	if runErr != nil {
		t.Fatalf("Run should not return a tool failure as a fatal error: %v", runErr)
	}
	if ft.calls != 1 {
		t.Errorf("tool executed %d times, want 1", ft.calls)
	}
	if !endErr {
		t.Error("expected a ToolEnd event marked IsError")
	}
	// The model's 2nd call must have received an error tool_result.
	if len(fake.sent) != 2 {
		t.Fatalf("model calls = %d, want 2", len(fake.sent))
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "boom") {
		t.Errorf("expected an error tool_result containing 'boom', got %+v", res)
	}
}

// TestRunDeniedToolFeedsDenialAndContinues drives the approval gate to a denial
// under the prompt policy and verifies the loop reports a denial tool_result
// and the tool never executes.
func TestRunDeniedToolFeedsDenialAndContinues(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	mut := &recordingTool{name: "writer", mutates: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail}
	reg.Register(mut)

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "writer", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok, asking you")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.ApprovalPolicy = config.ApprovalPrompt // force a prompt

	var approvalAsked, denied bool
	err := ag.Run(context.Background(), "write something", func(ev Event) {
		switch e := ev.(type) {
		case ApprovalRequest:
			approvalAsked = true
			e.Reply <- Deny
		case ToolEnd:
			if e.Denied {
				denied = true
			}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !approvalAsked {
		t.Error("expected an approval prompt for the mutating tool")
	}
	if !denied {
		t.Error("expected a Denied ToolEnd event")
	}
	if maxSeen != 0 {
		t.Errorf("denied tool should never execute, ran %d times", maxSeen)
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !res[0].IsError {
		t.Errorf("expected an error (denial) tool_result, got %+v", res)
	}
}

// TestRunApproveAlwaysSkipsSecondPrompt verifies ApproveAlways records the tool
// in alwaysAllow so a later call of the same tool runs without a prompt.
func TestRunApproveAlwaysSkipsSecondPrompt(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "writer", mutates: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	tu := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "writer", `{}`)}}
	fake := &scriptedStreamer{turns: []llm.Response{
		tu, tu,
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.ApprovalPolicy = config.ApprovalPrompt

	prompts := 0
	err := ag.Run(context.Background(), "write twice", func(ev Event) {
		if e, ok := ev.(ApprovalRequest); ok {
			prompts++
			e.Reply <- ApproveAlways
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prompts != 1 {
		t.Errorf("expected exactly 1 prompt (ApproveAlways), got %d", prompts)
	}
	if maxSeen < 1 {
		t.Error("tool never executed after approval")
	}
	if !ag.alwaysAllow["writer"] {
		t.Error("expected writer to be remembered in alwaysAllow")
	}
}

// TestRunApproveAlwaysPersistsPermission verifies ApproveAlways writes a
// project-scoped allow rule, so the choice survives a restart (a new Agent that
// loads the same permissions sees the tool pre-approved).
func TestRunApproveAlwaysPersistsPermission(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "writer", mutates: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	tu := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "writer", `{}`)}}
	fake := &scriptedStreamer{turns: []llm.Response{
		tu,
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.ApprovalPolicy = config.ApprovalPrompt
	ag.AttachPermissions(permissions.Load(dir))

	err := ag.Run(context.Background(), "write once", func(ev Event) {
		if e, ok := ev.(ApprovalRequest); ok {
			e.Reply <- ApproveAlways
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A freshly loaded ruleset (as on the next launch) must pre-approve the tool.
	reloaded := permissions.Load(dir)
	if got := reloaded.Check("writer", ""); got != permissions.Allow {
		t.Errorf("after ApproveAlways, reloaded permissions Check = %v, want Allow", got)
	}
}

// TestAlwaysAllowRule checks the rule keyed for "always allow": the tool name for
// most tools, but a command-scoped "bash:<command>" rule for bash, and "" (don't
// persist) for an empty bash command.
func TestAlwaysAllowRule(t *testing.T) {
	if got := alwaysAllowRule("write_file", nil); got != "write_file" {
		t.Errorf("write_file => %q, want write_file", got)
	}
	if got := alwaysAllowRule("bash", json.RawMessage(`{"command":"npm test"}`)); got != "bash:npm test" {
		t.Errorf("bash npm test => %q, want bash:npm test", got)
	}
	if got := alwaysAllowRule("bash", json.RawMessage(`{"command":"   "}`)); got != "" {
		t.Errorf("bash empty command => %q, want \"\"", got)
	}
}

// TestRunApproveAlwaysBashScopesToCommand verifies that "always allow" for a bash
// call persists a command-scoped rule (not a blanket bash allow): the approved
// command is pre-approved on reload, but a different command still needs review.
func TestRunApproveAlwaysBashScopesToCommand(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "bash", mutates: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	tu := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "bash", `{"command":"npm test"}`)}}
	fake := &scriptedStreamer{turns: []llm.Response{
		tu,
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.ApprovalPolicy = config.ApprovalPrompt
	ag.AttachPermissions(permissions.Load(dir))

	if err := ag.Run(context.Background(), "run the tests", func(ev Event) {
		if e, ok := ev.(ApprovalRequest); ok {
			e.Reply <- ApproveAlways
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reloaded := permissions.Load(dir)
	if got := reloaded.Check("bash", "npm test"); got != permissions.Allow {
		t.Errorf("approved command Check = %v, want Allow", got)
	}
	// A bare "bash" tool grant would have allowed everything — make sure it didn't.
	if got := reloaded.Check("bash", "rm -rf /"); got == permissions.Allow {
		t.Error("always-allow on one command must not blanket-allow all bash (rm -rf got Allow)")
	}
}

// TestRunPlanModeBlocksMutatingTool verifies that in plan mode a mutating tool
// requested by name is refused with a Denied error result and is not executed,
// and that visibleTools hides it from the model.
func TestRunPlanModeBlocksMutatingTool(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	// Name it edit_file so it lands in planModeBlocked.
	reg.Register(&recordingTool{name: "edit_file", mutates: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
	reg.Register(&recordingTool{name: "read_thing", mutates: false, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "edit_file", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("can't, plan mode")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetPlanMode(true)

	// visibleTools must hide the blocked tool but keep the read-only one.
	var names []string
	for _, s := range ag.visibleTools() {
		names = append(names, s.Name)
	}
	if contains(names, "edit_file") {
		t.Errorf("plan mode should hide edit_file from the model, got %v", names)
	}
	if !contains(names, "read_thing") {
		t.Errorf("plan mode should keep read-only tools visible, got %v", names)
	}

	var blocked bool
	err := ag.Run(context.Background(), "edit something", func(ev Event) {
		if e, ok := ev.(ToolEnd); ok && e.Denied {
			blocked = true
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if maxSeen != 0 {
		t.Errorf("blocked tool must not execute, ran %d times", maxSeen)
	}
	if !blocked {
		t.Error("expected a Denied ToolEnd event for the plan-mode block")
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "Plan mode") {
		t.Errorf("expected a plan-mode denial tool_result, got %+v", res)
	}
}

// TestRunPlanModeInjectsReadOnlyContext verifies that plan mode injects a
// read-only steering preface into the SYSTEM prompt sent to the model (even with
// no plan artifact present), and that build mode does not.
func TestRunPlanModeInjectsReadOnlyContext(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "read_thing", mutates: false, active: new(int32), maxSeen: new(int32), soloFail: new(int32)})
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("here is a plan")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	// Plan mode ON: the first turn's SYSTEM prompt must carry the read-only steering
	// (it rides in the uncached per-turn system suffix, not the message history).
	ag.SetPlanMode(true)
	if err := ag.Run(context.Background(), "investigate the bug", func(Event) {}); err != nil {
		t.Fatalf("Run (plan): %v", err)
	}
	got := fake.sentSystem[0]
	if !strings.Contains(got, "Plan mode is ON") {
		t.Errorf("plan mode should steer via the system prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "/build") {
		t.Errorf("plan-mode steering should tell the user how to leave (/build), got:\n%s", got)
	}
	// It must NOT be injected into the message history (that would bust the cache).
	if mt := messagesText(fake.sent[0]); strings.Contains(mt, "Plan mode is ON") {
		t.Errorf("plan-mode steering must ride in the system prompt, not the messages, got:\n%s", mt)
	}

	// Build mode: a fresh turn must NOT carry the steering (it's transient, never
	// stored in history).
	ag.SetPlanMode(false)
	if err := ag.Run(context.Background(), "now fix it", func(Event) {}); err != nil {
		t.Fatalf("Run (build): %v", err)
	}
	if got := fake.sentSystem[1]; strings.Contains(got, "Plan mode is ON") {
		t.Errorf("build mode must not inject the plan-mode steering, got:\n%s", got)
	}
}

// planFlipTool is a non-mutating test tool that leaves plan mode when invoked, so
// a turn can change plan→build mid-flight (mimicking exit_plan).
type planFlipTool struct{ a *Agent }

func (planFlipTool) Name() string            { return "flip_plan" }
func (planFlipTool) Description() string     { return "test tool: leaves plan mode" }
func (planFlipTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (planFlipTool) Mutates() bool           { return false }
func (p planFlipTool) Execute(context.Context, json.RawMessage) (string, error) {
	p.a.SetPlanMode(false)
	return "left plan mode", nil
}

// TestRunPlanPrefaceReevaluatedPerStep guards against a frozen-preface regression:
// the per-turn session preface (plan-mode steering) must be rebuilt each STEP, not
// once at turn start, so an exit_plan that flips plan→build mid-turn drops the
// read-only steering on the very next step instead of leaving it stale.
func TestRunPlanPrefaceReevaluatedPerStep(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("f1", "flip_plan", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	reg.Register(planFlipTool{a: ag})
	ag.SetPlanMode(true)

	if err := ag.Run(context.Background(), "investigate then build", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Step 0 ran in plan mode → steering present.
	if !strings.Contains(fake.sentSystem[0], "Plan mode is ON") {
		t.Errorf("step 0 should carry plan-mode steering, got:\n%s", fake.sentSystem[0])
	}
	// flip_plan left plan mode at step 0, so step 1's system prompt must NOT still
	// claim plan mode is on (the preface is re-evaluated per step).
	if strings.Contains(fake.sentSystem[1], "Plan mode is ON") {
		t.Errorf("step 1 should NOT carry stale plan-mode steering after exiting plan mode, got:\n%s", fake.sentSystem[1])
	}
}

// TestRunCancelledContextReturnsError verifies Run honors a cancelled context
// before making any model call.
func TestRunCancelledContextReturnsError(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("unreached")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled up front

	err := ag.Run(ctx, "hi", func(Event) {})
	if err == nil {
		t.Fatal("expected a context error")
	}
	if fake.calls != 0 {
		t.Errorf("no model call should be made on a cancelled context, got %d", fake.calls)
	}
}

// TestRunStreamErrorIsFatal verifies a model/stream error aborts the turn.
func TestRunStreamErrorIsFatal(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &erroringStreamer{err: fmt.Errorf("upstream 500")}
	ag := runTestAgent(t, reg, dir, &scriptedStreamer{}) // placeholder, replaced below
	ag.client = fake

	err := ag.Run(context.Background(), "hi", func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "upstream 500") {
		t.Fatalf("expected the stream error to propagate, got %v", err)
	}
}

// TestRunSnapshotsWriteForUndo verifies that a real write_file in the loop is
// snapshotted so /undo can revert it.
func TestRunSnapshotsWriteForUndo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{
			toolUse("t1", "write_file", `{"path":"out.txt","content":"hello"}`),
		}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("wrote it")}},
	}}
	ag := runTestAgent(t, reg, dir, fake) // ApprovalAuto: no prompt

	if err := ag.Run(context.Background(), "write out.txt", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "hello" {
		t.Fatalf("file not written, got %q", got)
	}
	// A snapshot should have been recorded; undo removes the created file.
	if _, err := ag.Undo(context.Background()); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("expected out.txt to be removed by undo")
	}
}

// TestRunApplyPatchSnapshotsAllFilesForUndo verifies that a multi-file
// apply_patch through the loop snapshots every touched file so /undo reverts
// the add, the update, and the delete together.
func TestRunApplyPatchSnapshotsAllFilesForUndo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "upd.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "del.txt"), []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: add.txt",
		"+brand new",
		"*** Update File: upd.txt",
		"@@",
		"-old",
		"+new",
		"*** Delete File: del.txt",
		"*** End Patch",
	}, "\n")
	patchJSON, _ := json.Marshal(map[string]string{"patch": patch})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t1", Name: "apply_patch", Input: patchJSON},
		}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("patched")}},
	}}
	ag := runTestAgent(t, reg, dir, fake) // ApprovalAuto

	if err := ag.Run(context.Background(), "apply the patch", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Patch took effect.
	if got, _ := os.ReadFile(filepath.Join(dir, "add.txt")); string(got) != "brand new" {
		t.Errorf("add.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "upd.txt")); string(got) != "new\n" {
		t.Errorf("upd.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "del.txt")); !os.IsNotExist(err) {
		t.Errorf("del.txt should be deleted")
	}

	// Undo reverts all three.
	if _, err := ag.Undo(context.Background()); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "add.txt")); !os.IsNotExist(err) {
		t.Errorf("undo should remove the added file")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "upd.txt")); string(got) != "old\n" {
		t.Errorf("undo should restore upd.txt, got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "del.txt")); string(got) != "bye\n" {
		t.Errorf("undo should restore del.txt, got %q", got)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
