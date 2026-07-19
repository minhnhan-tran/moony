package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// TestRecordTurnMetaPersists verifies a completed turn records a per-turn footer
// (datetime, duration, cost, model, reasoning) on the session, anchored to the
// message count so the web UI can replay it.
func TestRecordTurnMetaPersists(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Model:              "anthropic.claude-opus-4-8",
		ReasoningLevel:     "high",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hello there")}, Usage: llm.Usage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 800}},
	}}
	ag := New(cfg, fake, tools.NewRegistry(dir), store, nil)

	if err := ag.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.TurnMetas) != 1 {
		t.Fatalf("TurnMetas = %d, want 1: %+v", len(saved.TurnMetas), saved.TurnMetas)
	}
	tm := saved.TurnMetas[0]
	if tm.Time.IsZero() {
		t.Error("TurnMeta.Time is zero; the answer's datetime was not recorded")
	}
	if tm.Model != "anthropic.claude-opus-4-8" || tm.Reasoning != "high" {
		t.Errorf("TurnMeta model/reasoning = %q/%q, want opus/high", tm.Model, tm.Reasoning)
	}
	// The answering cost must be persisted (opus pricing is in the table, so the
	// 1000-in/500-out turn has a positive estimate).
	if tm.CostUSD <= 0 {
		t.Errorf("TurnMeta.CostUSD = %v, want > 0 (the answering cost should be persisted)", tm.CostUSD)
	}
	// The footer must be anchored to the persisted message count so replay can
	// place it after the right turn.
	if tm.MsgCount != len(saved.Messages) {
		t.Errorf("TurnMeta.MsgCount = %d, want %d (len messages)", tm.MsgCount, len(saved.Messages))
	}
	// A single-response turn (no tool calls) makes exactly one model call.
	if tm.Calls != 1 {
		t.Errorf("TurnMeta.Calls = %d, want 1 (one model call this turn)", tm.Calls)
	}
	// Token totals + cached-input subset must be persisted for the footer's
	// cache-hit telemetry.
	if tm.InTokens != 1000 || tm.OutTokens != 500 {
		t.Errorf("TurnMeta in/out tokens = %d/%d, want 1000/500", tm.InTokens, tm.OutTokens)
	}
	if tm.CacheReadTokens != 800 {
		t.Errorf("TurnMeta.CacheReadTokens = %d, want 800", tm.CacheReadTokens)
	}
}

// TestRecordTurnMetaUsesServingModelOnDownshift is the regression guard for the
// footer mislabeling bug: when AdaptiveModel downshifts an easy turn to the
// FastModel, the configured main model never runs — so the recorded TurnMeta
// (and the live turn footer) must name the model that ACTUALLY served the turn,
// not a.cfg.Model.
func TestRecordTurnMetaUsesServingModelOnDownshift(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := "anthropic.claude-opus-4-8"
	fast := config.FastModel(main, "")
	if fast == main {
		t.Skipf("no distinct FastModel for %q; can't exercise a downshift", main)
	}
	cfg := &config.Config{
		Model:              main,
		AdaptiveModel:      true, // downshift easy turns to FastModel
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}},
	}}
	ag := New(cfg, fake, tools.NewRegistry(dir), store, nil)

	// An obviously-easy prompt so turnLooksHard stays false and the round
	// downshifts to the fast model.
	if err := ag.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.TurnMetas) != 1 {
		t.Fatalf("TurnMetas = %d, want 1", len(saved.TurnMetas))
	}
	if got := saved.TurnMetas[0].Model; got != fast {
		t.Errorf("TurnMeta.Model = %q, want the serving (fast) model %q — not the session model %q", got, fast, main)
	}
}

// TestRecordTurnMetaUsesFastReasoningOnDownshift is the reasoning-level analogue
// of TestRecordTurnMetaUsesServingModelOnDownshift: when AdaptiveModel
// downshifts a round to the Fast tier and MN_FAST_REASONING pins a distinct
// level, the per-turn footer must report the Fast tier's reasoning (what
// actually ran), not the session model's configured level.
func TestRecordTurnMetaUsesFastReasoningOnDownshift(t *testing.T) {
	isolateConfig(t)
	t.Setenv("MN_FAST_MODEL", "test-fast-model")
	t.Setenv("MN_FAST_REASONING", "low")
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := "anthropic.claude-opus-4-8"
	fast := config.FastModel(main, "")
	if fast == main {
		t.Skipf("no distinct FastModel for %q; can't exercise a downshift", main)
	}
	cfg := &config.Config{
		Model:              main,
		ReasoningLevel:     "high", // the session (main) model's level
		AdaptiveModel:      true,
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}},
	}}
	ag := New(cfg, fake, tools.NewRegistry(dir), store, nil)

	if err := ag.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.TurnMetas) != 1 {
		t.Fatalf("TurnMetas = %d, want 1", len(saved.TurnMetas))
	}
	if got := saved.TurnMetas[0].Reasoning; got != "low" {
		t.Errorf("TurnMeta.Reasoning = %q, want the Fast tier's %q — not the session level %q", got, "low", cfg.ReasoningLevel)
	}
}
