package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

// TestUltrathinkInjectsDeepDiveDirective verifies that an "ultrathink" prompt
// turns the turn into a sub-agent deep dive: the orchestrator's system prompt
// gains the ULTRATHINK directive (which tells it to fan out parallel sub-agents
// via the task tool). A normal prompt doesn't, and neither does ultrathink when
// there's no task tool (no sub-agents to spawn).
func TestUltrathinkInjectsDeepDiveDirective(t *testing.T) {
	dir := t.TempDir()
	run := func(prompt string, withTask bool) string {
		cfg := &config.Config{Model: "test", WorkingDir: dir, MaxAgentSteps: 1}
		fake := &scriptedStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok")}}}}
		ag := New(cfg, fake, tools.NewRegistry(dir), nil, nil)
		if withTask {
			ag.AttachSubAgents([]subagents.Persona{{Name: "explore", Description: "read-only explorer"}})
		}
		if err := ag.RunWith(context.Background(), prompt, nil, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		if len(fake.sentSystem) == 0 {
			t.Fatal("no system prompt captured")
		}
		return fake.sentSystem[0] // the main turn's first model call
	}

	if sys := run("@ultrathink how does auth work?", true); !strings.Contains(sys, "ULTRATHINK MODE") {
		t.Errorf("@ultrathink turn should inject the deep-dive directive; system did not contain it:\n%s", sys)
	}
	if sys := run("how does auth work?", true); strings.Contains(sys, "ULTRATHINK MODE") {
		t.Error("a normal prompt must NOT inject the deep-dive directive")
	}
	if sys := run("ultrathink the bug", true); strings.Contains(sys, "ULTRATHINK MODE") {
		t.Error("the bare word (no @) must NOT trigger the deep dive")
	}
	if sys := run("@ultrathink x", false); strings.Contains(sys, "ULTRATHINK MODE") {
		t.Error("without the task tool there are no sub-agents to spawn, so no directive should be injected")
	}
}

// TestDirectivesMatchCacheMarkers guards the cross-package contract behind the
// prompt-cache split: the per-turn directives MUST begin with the markers
// llm.applyPromptCache splits on, and Run MUST join them with the shared
// separator. If a directive is ever rewritten as a literal that drifts from the
// marker, the split silently stops working (no error, just lost cache hits) — so
// fail loudly here instead.
func TestDirectivesMatchCacheMarkers(t *testing.T) {
	if !strings.HasPrefix(ultrathinkDirective, llm.UltrathinkDirectiveMarker) {
		t.Errorf("ultrathinkDirective must begin with llm.UltrathinkDirectiveMarker (%q) so applyPromptCache can split on it", llm.UltrathinkDirectiveMarker)
	}
	if !strings.HasPrefix(autoDelegateDirective, llm.InvestigationDirectiveMarker) {
		t.Errorf("autoDelegateDirective must begin with llm.InvestigationDirectiveMarker (%q) so applyPromptCache can split on it", llm.InvestigationDirectiveMarker)
	}
}

// TestAutoReasoningEffort covers the "auto" level: effort is predicted from the
// prompt (off for trivial turns, high for reasoning/debug cues) and lifted to
// high by a hard previous turn (the cross-turn signal). Also checks the effort
// carries to the model call via the turn's context.
func TestAutoReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Model: "anthropic.claude-opus-4-8", Provider: config.ProviderAnthropic, WorkingDir: dir, ReasoningLevel: config.ReasoningAuto, MaxAgentSteps: 1}
	ag := New(cfg, &scriptedStreamer{}, tools.NewRegistry(dir), nil, nil)

	if got := ag.autoReasoningEffort("continue"); got != config.ReasoningOff {
		t.Errorf("auto on a trivial prompt = %q, want off (no thinking)", got)
	}
	if got := ag.autoReasoningEffort("why does the parser fail on nested input?"); got != config.ReasoningHigh {
		t.Errorf("auto on a debug prompt = %q, want high (predicted)", got)
	}
	ag.lastTurnHard = true
	if got := ag.autoReasoningEffort("continue"); got != config.ReasoningHigh {
		t.Errorf("auto after a hard turn = %q, want high (carryover)", got)
	}
}

// TestRampedAutoEffort covers the in-turn ratchet: "auto" effort rises with the
// turn's FRICTION count (off → medium at autoFrictionMedium → high at
// autoFrictionHigh) and never lowers the predicted base (up-only within a turn).
// A single isolated failure (friction 1) does not escalate.
func TestRampedAutoEffort(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Model: "anthropic.claude-opus-4-8", Provider: config.ProviderAnthropic, WorkingDir: dir}
	ag := New(cfg, &scriptedStreamer{}, tools.NewRegistry(dir), nil, nil)

	if got := ag.rampedAutoEffort(config.ReasoningOff, 1); got != config.ReasoningOff {
		t.Errorf("off base, friction 1 = %q, want off (one failure doesn't escalate)", got)
	}
	if got := ag.rampedAutoEffort(config.ReasoningOff, autoFrictionMedium); got != config.ReasoningMedium {
		t.Errorf("off base, friction %d = %q, want medium", autoFrictionMedium, got)
	}
	if got := ag.rampedAutoEffort(config.ReasoningOff, autoFrictionHigh); got != config.ReasoningHigh {
		t.Errorf("off base, friction %d = %q, want high", autoFrictionHigh, got)
	}
	// Never lowers the base: a high prediction stays high even with no friction.
	if got := ag.rampedAutoEffort(config.ReasoningHigh, 1); got != config.ReasoningHigh {
		t.Errorf("high base, friction 1 = %q, want high (never lowered)", got)
	}
}

// TestTurnFrictionCountsFailures verifies the signal the friction-based "auto"
// ramp depends on: a tool that FAILS bumps turnFriction (so reasoning can ramp),
// a successful tool does not, and reaching autoFrictionMedium marks the turn hard
// for the next turn's starting effort. Friction — not raw tool-round count — is
// what escalates. The two-failures case also runs two read_file calls in one
// step (read-only ⇒ concurrent), exercising the atomic increment.
func TestTurnFrictionCountsFailures(t *testing.T) {
	isolateConfig(t)
	t.Setenv("MOONY_AUTO_TITLE", "0")

	newAgent := func(dir string, turns []llm.Response) *Agent {
		cfg := &config.Config{Model: "test-model", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir}
		return New(cfg, &scriptedStreamer{turns: turns}, tools.Default(dir, 1<<20), nil, nil)
	}
	readCall := func(id, path string) llm.ContentBlock {
		return llm.ContentBlock{Type: llm.BlockToolUse, ID: id, Name: "read_file", Input: json.RawMessage(`{"path":"` + path + `"}`)}
	}
	done := llm.Response{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}}

	// One failed read → friction 1; below autoFrictionMedium, so not "hard".
	ag1 := newAgent(t.TempDir(), []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{readCall("a", "missing-1.xyz")}},
		done,
	})
	if err := ag1.Run(context.Background(), "read a missing file", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ag1.turnFriction.Load(); got != 1 {
		t.Errorf("one failed read: turnFriction = %d, want 1", got)
	}
	if ag1.lastTurnHard {
		t.Errorf("one failure (< autoFrictionMedium=%d) should not mark the turn hard", autoFrictionMedium)
	}

	// Two failed reads in one step → friction reaches autoFrictionMedium → hard.
	ag2 := newAgent(t.TempDir(), []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{readCall("a", "missing-1.xyz"), readCall("b", "missing-2.xyz")}},
		done,
	})
	if err := ag2.Run(context.Background(), "read two missing files", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ag2.turnFriction.Load(); got != int64(autoFrictionMedium) {
		t.Errorf("two failed reads: turnFriction = %d, want %d", got, autoFrictionMedium)
	}
	if !ag2.lastTurnHard {
		t.Errorf("friction %d should mark the turn hard (>= %d)", ag2.turnFriction.Load(), autoFrictionMedium)
	}

	// A successful read is not friction.
	dir3 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir3, "real.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag3 := newAgent(dir3, []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{readCall("a", "real.txt")}},
		done,
	})
	if err := ag3.Run(context.Background(), "read a real file", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := ag3.turnFriction.Load(); got != 0 {
		t.Errorf("successful read: turnFriction = %d, want 0 (success is not friction)", got)
	}
}
