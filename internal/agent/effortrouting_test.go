package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestPassiveToolBatch(t *testing.T) {
	passive := []llm.ContentBlock{toolUse("a", "edit_file", `{}`), toolUse("b", "list_dir", `{}`)}
	if !passiveToolBatch(passive) {
		t.Error("a batch of only edit_file/list_dir should be passive")
	}
	// A content tool in the batch makes it active (the model reasons about output).
	mixed := []llm.ContentBlock{toolUse("a", "edit_file", `{}`), toolUse("b", "read_file", `{}`)}
	if passiveToolBatch(mixed) {
		t.Error("a batch containing read_file should NOT be passive")
	}
	if passiveToolBatch(nil) {
		t.Error("an empty batch should not be passive")
	}
}

func TestPassiveEffort(t *testing.T) {
	a := &Agent{cfg: &config.Config{Model: "anthropic.claude-opus-4-8", Provider: config.ProviderAnthropic}}
	if got := a.passiveEffort(config.ReasoningHigh); got != config.ReasoningLow {
		t.Errorf("passiveEffort(high) = %q, want low", got)
	}
	if got := a.passiveEffort(config.ReasoningLow); got != config.ReasoningLow {
		t.Errorf("passiveEffort(low) = %q, want low", got)
	}
	if got := a.passiveEffort(config.ReasoningOff); got != config.ReasoningOff {
		t.Errorf("passiveEffort(off) = %q, want off", got)
	}
}

// effortRoutingAgent builds an auto-mode agent on a reasoning-capable model with a
// single passive (or content) recording tool, plus a 2-turn script: a tool call
// then an answer. lastTurnHard forces the auto base to high so a passive dip to
// low is observable.
func effortRoutingAgent(t *testing.T, toolName string, dynamic bool) (*Agent, *scriptedStreamer) {
	t.Helper()
	isolateConfig(t)
	dir := t.TempDir()
	var a, m, s int32
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: toolName, active: &a, maxSeen: &m, soloFail: &s})
	cfg := &config.Config{
		Model:              "anthropic.claude-opus-4-8",
		Provider:           config.ProviderAnthropic,
		WorkingDir:         dir,
		ReasoningLevel:     config.ReasoningAuto,
		DynamicEffort:      dynamic,
		ApprovalPolicy:     config.ApprovalAuto,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      5,
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", toolName, `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := New(cfg, fake, reg, nil, nil)
	ag.lastTurnHard = true // force the auto base to high
	return ag, fake
}

// A step that only digested a passive tool batch runs the next model call at a
// lowered (low) reasoning floor; the first call (responding to the prompt) stays
// at the high base.
func TestDynamicEffortLowersOnPassiveStep(t *testing.T) {
	ag, fake := effortRoutingAgent(t, "list_dir", true)
	if err := ag.Run(context.Background(), "go do it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(fake.sentEfforts) != 2 {
		t.Fatalf("expected 2 model calls, got %d: %v", len(fake.sentEfforts), fake.sentEfforts)
	}
	if fake.sentEfforts[0] != config.ReasoningHigh {
		t.Errorf("step 0 effort = %q, want high (responding to the prompt)", fake.sentEfforts[0])
	}
	if fake.sentEfforts[1] != config.ReasoningLow {
		t.Errorf("step 1 effort = %q, want low (passive continuation)", fake.sentEfforts[1])
	}
}

// With dynamic routing off, the passive step keeps the high base (existing behavior).
func TestDynamicEffortDisabledKeepsBase(t *testing.T) {
	ag, fake := effortRoutingAgent(t, "list_dir", false)
	if err := ag.Run(context.Background(), "go do it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(fake.sentEfforts) != 2 {
		t.Fatalf("expected 2 model calls, got %d: %v", len(fake.sentEfforts), fake.sentEfforts)
	}
	if fake.sentEfforts[1] != config.ReasoningHigh {
		t.Errorf("step 1 effort = %q, want high when DynamicEffort is off", fake.sentEfforts[1])
	}
}

// A content tool (read_file) is not a passive batch, so effort is NOT dipped — the
// model keeps full reasoning to digest what it read.
func TestDynamicEffortContentStepKeepsBase(t *testing.T) {
	ag, fake := effortRoutingAgent(t, "read_file", true)
	if err := ag.Run(context.Background(), "go do it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(fake.sentEfforts) != 2 {
		t.Fatalf("expected 2 model calls, got %d: %v", len(fake.sentEfforts), fake.sentEfforts)
	}
	if fake.sentEfforts[1] != config.ReasoningHigh {
		t.Errorf("step 1 effort = %q, want high after a content (read_file) step", fake.sentEfforts[1])
	}
}
