package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// newVerifyTestAgent builds a minimal agent for exercising the verification loop:
// a working dir, the given verify command, and a snapshot store pre-seeded to
// look like the current turn did (or did not) edit a file.
func newVerifyTestAgent(t *testing.T, cmd string, mutated bool) *Agent {
	t.Helper()
	snaps := &snapshotStore{turn: 1}
	if mutated {
		snaps.entries = []fileSnapshot{{turn: 1, path: "x.go"}}
	}
	return &Agent{
		cfg: &config.Config{
			WorkingDir:         t.TempDir(),
			VerifyCommand:      cmd,
			VerifyMaxRetries:   2,
			VerifyTimeoutSec:   30,
			MaxToolOutputBytes: 16000,
		},
		snapshots: snaps,
	}
}

func collectInfo(events []Event) []string {
	var out []string
	for _, e := range events {
		if i, ok := e.(Info); ok {
			out = append(out, i.Text)
		}
	}
	return out
}

// TestRunVerifyExitCodes confirms runVerify reports ok on exit 0 and not-ok on a
// non-zero exit, capturing output either way.
func TestRunVerifyExitCodes(t *testing.T) {
	pass := newVerifyTestAgent(t, "echo built ok", true)
	if r := pass.runVerify(context.Background()); !r.ok || !strings.Contains(r.output, "built ok") {
		t.Errorf("passing command: ok=%v output=%q", r.ok, r.output)
	}
	fail := newVerifyTestAgent(t, "echo boom >&2; exit 1", true)
	if r := fail.runVerify(context.Background()); r.ok || !strings.Contains(r.output, "boom") {
		t.Errorf("failing command: ok=%v output=%q (want not-ok, output captured)", r.ok, r.output)
	}
	// No command configured → vacuously ok.
	none := newVerifyTestAgent(t, "", true)
	if r := none.runVerify(context.Background()); !r.ok {
		t.Errorf("empty command should be a no-op success, got ok=%v", r.ok)
	}
}

// TestRunVerifyTimeout confirms a hung command is killed and reported as failed.
func TestRunVerifyTimeout(t *testing.T) {
	a := newVerifyTestAgent(t, "sleep 5", true)
	a.cfg.VerifyTimeoutSec = 1
	r := a.runVerify(context.Background())
	if r.ok || !strings.Contains(r.output, "timed out") {
		t.Errorf("timeout: ok=%v output=%q (want not-ok + timed-out note)", r.ok, r.output)
	}
}

// TestRunVerifyScrubsSecrets confirms the verify subprocess does NOT inherit the
// agent's auth/Floodgate secrets (parity with the bash tool's scrubbed env), while
// ordinary environment variables still pass through.
func TestRunVerifyScrubsSecrets(t *testing.T) {
	t.Setenv("MOONY_OLLAMA_TOKEN", "SENTINEL-must-not-leak")
	t.Setenv("VERIFY_PROBE_VAR", "probe-visible")
	a := newVerifyTestAgent(t, `printf 'secret=[%s] probe=[%s]\n' "$MOONY_AUTH_SECRET" "$VERIFY_PROBE_VAR"`, true)
	r := a.runVerify(context.Background())
	if !r.ok {
		t.Fatalf("probe command should succeed, got ok=%v output=%q", r.ok, r.output)
	}
	if strings.Contains(r.output, "SENTINEL-must-not-leak") {
		t.Errorf("verify subprocess leaked a sensitive env var (MOONY_OLLAMA_TOKEN): %q", r.output)
	}
	if !strings.Contains(r.output, "probe-visible") {
		t.Errorf("verify subprocess should still inherit ordinary env vars, got %q", r.output)
	}
}

// TestMaybeVerifyGating confirms verification is skipped when disabled or when
// the turn edited nothing.
func TestMaybeVerifyGating(t *testing.T) {
	// Disabled (no command): never continues, emits nothing.
	off := newVerifyTestAgent(t, "", true)
	var n int
	var evs []Event
	if off.maybeVerify(context.Background(), func(e Event) { evs = append(evs, e) }, &n) {
		t.Error("disabled verify should not continue the loop")
	}
	if len(evs) != 0 {
		t.Errorf("disabled verify should emit nothing, got %v", collectInfo(evs))
	}

	// Configured but the turn edited nothing: skip.
	noEdit := newVerifyTestAgent(t, "exit 1", false)
	n = 0
	if noEdit.maybeVerify(context.Background(), func(Event) {}, &n) {
		t.Error("verify should be skipped when nothing was edited this turn")
	}
}

// TestMaybeVerifyPasses confirms a passing verification finishes the turn (no
// continue) and announces success.
func TestMaybeVerifyPasses(t *testing.T) {
	a := newVerifyTestAgent(t, "true", true)
	var n int
	var evs []Event
	if a.maybeVerify(context.Background(), func(e Event) { evs = append(evs, e) }, &n) {
		t.Error("passing verify should not continue the loop")
	}
	if n != 0 {
		t.Errorf("attempts should stay 0 on success, got %d", n)
	}
	joined := strings.Join(collectInfo(evs), " | ")
	if !strings.Contains(joined, "passed") {
		t.Errorf("expected a 'passed' info, got %q", joined)
	}
}

// TestMaybeVerifyFailsThenCaps confirms a failing verification feeds the error
// back (continue + a synthetic message) until the retry cap, then stops.
func TestMaybeVerifyFailsThenCaps(t *testing.T) {
	a := newVerifyTestAgent(t, "echo compile error; exit 2", true)
	a.cfg.VerifyMaxRetries = 2
	var n int
	emit := func(Event) {}

	// Attempt 1: fail → continue, message appended, attempts=1.
	if !a.maybeVerify(context.Background(), emit, &n) {
		t.Fatal("first failure should continue the loop")
	}
	if n != 1 || len(a.messages) != 1 {
		t.Fatalf("after attempt 1: attempts=%d messages=%d, want 1 and 1", n, len(a.messages))
	}
	if !strings.Contains(a.messages[0].Content[0].Text, "compile error") {
		t.Errorf("feedback message should carry the failure output, got %q", a.messages[0].Content[0].Text)
	}

	// Attempt 2: fail → continue, attempts=2.
	if !a.maybeVerify(context.Background(), emit, &n) {
		t.Fatal("second failure should still continue (cap is 2)")
	}
	if n != 2 {
		t.Fatalf("after attempt 2: attempts=%d, want 2", n)
	}

	// Attempt 3: cap reached → stop (no continue), no new message.
	before := len(a.messages)
	var evs []Event
	if a.maybeVerify(context.Background(), func(e Event) { evs = append(evs, e) }, &n) {
		t.Fatal("once the retry cap is reached, verify must stop the loop")
	}
	if len(a.messages) != before {
		t.Errorf("no feedback should be appended after the cap, messages went %d→%d", before, len(a.messages))
	}
	if !strings.Contains(strings.Join(collectInfo(evs), " "), "still failing") {
		t.Errorf("expected a 'still failing' give-up notice, got %v", collectInfo(evs))
	}
}
