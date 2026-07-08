package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/tools"
)

// frictionTool always fails, so each call increments the turn's friction counter
// (the same signal that drives the auto effort ramp). It is non-mutating, so it
// auto-runs under ApprovalAuto with no approval prompt, and its empty-object
// schema has no required fields so the self-correcting missing-args guard lets
// it through to Execute.
type frictionTool struct{}

func (frictionTool) Name() string            { return "boom" }
func (frictionTool) Description() string     { return "always fails (test friction source)" }
func (frictionTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (frictionTool) Mutates() bool           { return false }
func (frictionTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("boom")
}

// adaptiveAgent builds an agent with adaptive model routing configured to a
// known Ollama session model. SQE_FAST_MODEL is pinned so config.FastModel
// resolves to a cheaper fast model (distinct from the strong session model);
// without a pin, the Ollama provider keeps the session model for every tier.
func adaptiveAgent(t *testing.T, fake *scriptedStreamer, adaptive bool, model string, reg *tools.Registry) *Agent {
	t.Helper()
	isolateConfig(t)
	t.Setenv("SQE_FAST_MODEL", adaptiveFastModel) // makes FastModel diverge from the session model
	dir := t.TempDir()
	if reg == nil {
		reg = tools.NewRegistry(dir)
	}
	cfg := &config.Config{
		Model:              model,
		LLMProvider:        provider.Ollama,
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
		AdaptiveModel:      adaptive,
	}
	return New(cfg, fake, reg, nil, nil)
}

const (
	adaptiveSessionModel = "qwen3.5:122b" // strong; the user's choice
	adaptiveFastModel    = "gemma4:8b"    // cheaper fast tier (pinned via SQE_FAST_MODEL)
)

// TestAdaptiveModelStartsCheap verifies that with adaptive routing on and no
// friction, the (single-step) turn runs on the cheaper fast model rather than
// the strong session model the user selected.
func TestAdaptiveModelStartsCheap(t *testing.T) {
	t.Setenv("SQE_FAST_MODEL", adaptiveFastModel)
	if got := config.FastModel(adaptiveSessionModel, provider.Ollama); got != adaptiveFastModel {
		t.Fatalf("test premise broken: FastModel(%s) = %s, want %s", adaptiveSessionModel, got, adaptiveFastModel)
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, nil)
	if err := ag.Run(context.Background(), "easy question", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.sentModels) != 1 || fake.sentModels[0] != adaptiveFastModel {
		t.Errorf("models used = %v, want one call on the fast model %q", fake.sentModels, adaptiveFastModel)
	}
}

// TestAdaptiveModelEscalatesOnFriction verifies the ramp: the turn starts on the
// fast model, but once friction reaches the threshold (autoFrictionMedium=2) it
// escalates to the session model for the rest of the turn, and the escalation
// notice fires exactly once.
func TestAdaptiveModelEscalatesOnFriction(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(frictionTool{})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("a", "boom", `{}`)}},
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("b", "boom", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("recovered")}},
	}}
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, reg)

	escalations := 0
	if err := ag.Run(context.Background(), "do the thing", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "escalating to") {
			escalations++
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Friction increments AFTER each failing tool call: step 0 (friction 0) and
	// step 1 (friction 1) both run on the fast model; by step 2 friction has
	// reached the threshold (2), so it escalates to the session model.
	want := []string{adaptiveFastModel, adaptiveFastModel, adaptiveSessionModel}
	if len(fake.sentModels) != 3 {
		t.Fatalf("model calls = %d (%v), want 3", len(fake.sentModels), fake.sentModels)
	}
	for i, w := range want {
		if fake.sentModels[i] != w {
			t.Errorf("call %d used %q, want %q (full: %v)", i, fake.sentModels[i], w, fake.sentModels)
		}
	}
	if escalations != 1 {
		t.Errorf("escalation notices = %d, want exactly 1", escalations)
	}
}

// TestAdaptiveModelOffUsesSessionModel verifies that with adaptive routing off
// (the default), every call uses the session model the user chose — the feature
// is strictly opt-in and never silently swaps the model.
func TestAdaptiveModelOffUsesSessionModel(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := adaptiveAgent(t, fake, false, adaptiveSessionModel, nil)
	if err := ag.Run(context.Background(), "easy question", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.sentModels) != 1 || fake.sentModels[0] != adaptiveSessionModel {
		t.Errorf("models used = %v, want one call on the session model %q", fake.sentModels, adaptiveSessionModel)
	}
}

// TestAdaptiveModelNoopWhenFastEqualsSession verifies the no-op guard: when the
// session model already IS the fast model, adaptive routing changes nothing —
// every call uses that model and no escalation fires.
func TestAdaptiveModelNoopWhenFastEqualsSession(t *testing.T) {
	t.Setenv("SQE_FAST_MODEL", adaptiveFastModel)
	if got := config.FastModel(adaptiveFastModel, provider.Ollama); got != adaptiveFastModel {
		t.Fatalf("test premise broken: FastModel(%s) = %s, want it to return itself", adaptiveFastModel, got)
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := adaptiveAgent(t, fake, true, adaptiveFastModel, nil)
	escalations := 0
	if err := ag.Run(context.Background(), "easy", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "escalating to") {
			escalations++
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.sentModels) != 1 || fake.sentModels[0] != adaptiveFastModel {
		t.Errorf("models used = %v, want one call on %q", fake.sentModels, adaptiveFastModel)
	}
	if escalations != 0 {
		t.Errorf("escalation notices = %d, want 0 (no-op when fast == session)", escalations)
	}
}
