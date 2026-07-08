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
	"github.com/pysty1995/moony/internal/tools"
)

// TestTurnCostPricedAtActualModel verifies the fix for the cost-accounting bug:
// when AdaptiveModel downshifts a round to the cheaper FastModel, the emitted
// TurnDone.CostUSD must be priced at the model that ACTUALLY served the round
// (FastModel), not at the pricier session model. Before the fix it always used
// a.cfg.Model, overstating cost whenever a round ran cheap.
func TestTurnCostPricedAtActualModel(t *testing.T) {
	// Ollama models aren't in the static price table; overlay test prices so the
	// fast and session tiers cost differently (the whole point of this test).
	config.ClearRuntimePricing()
	t.Cleanup(config.ClearRuntimePricing)
	config.OverlayRuntimePricing(adaptiveFastModel, 1e-6, 5e-6, 0, 0)
	config.OverlayRuntimePricing(adaptiveSessionModel, 15e-6, 75e-6, 0, 0)
	const in, out = 100_000, 10_000
	fake := &scriptedStreamer{turns: []llm.Response{{
		StopReason: "end_turn",
		Blocks:     []llm.ContentBlock{llm.TextBlock("done")},
		Usage:      llm.Usage{InputTokens: in, OutputTokens: out},
	}}}
	// Adaptive on, no friction → the single round runs on FastModel.
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, nil)

	var got float64
	if err := ag.RunWith(context.Background(), "hi", nil, func(ev Event) {
		if e, ok := ev.(TurnDone); ok {
			got = e.CostUSD
		}
	}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}

	// Sanity: the round really did run on the fast model.
	if len(fake.sentModels) != 1 || fake.sentModels[0] != adaptiveFastModel {
		t.Fatalf("expected one call on %q, got %v", adaptiveFastModel, fake.sentModels)
	}

	u := llm.Usage{InputTokens: in, OutputTokens: out}
	wantFast := u.CostUSD(adaptiveFastModel)
	wantSession := u.CostUSD(adaptiveSessionModel)
	if wantFast == wantSession {
		t.Fatal("test premise broken: fast and session model price the same")
	}
	if !floatNear(got, wantFast) {
		t.Errorf("TurnDone.CostUSD = %.6f, want fast-model price %.6f (not session-model price %.6f)", got, wantFast, wantSession)
	}
}

// TestTurnCostIncludesAuxCalls verifies the second half of the fix: a streamAux
// call's real spend (self-review, checkpoint, titling, …) is folded into the
// turn total via the aux-cost accumulator, instead of being silently dropped.
func TestTurnCostIncludesAuxCalls(t *testing.T) {
	config.ClearRuntimePricing()
	t.Cleanup(config.ClearRuntimePricing)
	ag := adaptiveAgent(t, &scriptedStreamer{}, false, adaptiveSessionModel, nil)
	auxModel := config.AuxModel(ag.cfg.Model, ag.cfg.LLMProvider)
	config.OverlayRuntimePricing(auxModel, 1e-6, 5e-6, 0, 0)
	ag.resetAuxCost()
	if c := ag.auxCostUSD(); c != 0 {
		t.Fatalf("fresh aux cost = %v, want 0", c)
	}
	u := llm.Usage{InputTokens: 5000, OutputTokens: 800}
	want := u.CostUSD(auxModel)
	if want == 0 {
		t.Fatal("test premise broken: aux model has no price")
	}
	ag.addAuxCost(want)
	ag.addAuxCost(want) // two aux calls this turn
	if got := ag.auxCostUSD(); !floatNear(got, 2*want) {
		t.Errorf("auxCostUSD after two adds = %.6f, want %.6f", got, 2*want)
	}
	ag.resetAuxCost()
	if c := ag.auxCostUSD(); c != 0 {
		t.Errorf("aux cost after reset = %v, want 0", c)
	}
}

func floatNear(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

func TestShortModelName(t *testing.T) {
	cases := map[string]string{
		"anthropic.claude-haiku-4-5-20251001-v1:0": "claude-haiku-4-5",
		"anthropic.claude-sonnet-5":                "claude-sonnet-5",
		"gcp:gemini-2.5-pro":                       "gemini-2.5-pro",
		"gemma4:31b":                               "31b", // best-effort; no dotted prefix
	}
	for in, want := range cases {
		if got := shortModelName(in); got != want {
			t.Errorf("shortModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelfReviewMeta(t *testing.T) {
	// No usage → model name only.
	if got := selfReviewMeta("anthropic.claude-haiku-4-5-20251001-v1:0", llm.Usage{}); got != "claude-haiku-4-5" {
		t.Errorf("empty usage meta = %q, want bare model name", got)
	}
	// With usage → model · tokens · cost.
	got := selfReviewMeta("anthropic.claude-haiku-4-5-20251001-v1:0", llm.Usage{InputTokens: 1234, OutputTokens: 340})
	if got == "claude-haiku-4-5" || got == "" {
		t.Errorf("meta with usage should include tokens/cost, got %q", got)
	}
}

// TestAdaptiveModelDownshiftNotice verifies MOONY_ADAPTIVE_MODEL's routing
// decision is observable: the first round on the cheap FastModel emits an
// "auto: using <model> for this round (adaptive model)." notice.
func TestAdaptiveModelDownshiftNotice(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{{
		StopReason: "end_turn",
		Blocks:     []llm.ContentBlock{llm.TextBlock("done")},
	}}}
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, nil)

	var notices []string
	if err := ag.RunWith(context.Background(), "hi", nil, func(ev Event) {
		if info, ok := ev.(Info); ok && strings.Contains(info.Text, "auto: using") {
			notices = append(notices, info.Text)
		}
	}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("downshift notices = %v, want exactly 1", notices)
	}
	if !strings.Contains(notices[0], shortModelName(adaptiveFastModel)) || !strings.Contains(notices[0], "adaptive model") {
		t.Errorf("notice = %q, want it to name %q and reason %q", notices[0], shortModelName(adaptiveFastModel), "adaptive model")
	}
}

// TestCheapReadsDownshiftNotice verifies MOONY_CHEAP_READS's routing decision is
// observable and correctly attributed (distinct from AdaptiveModel's reason).
// Round 1 (a read_file call) sets lastStepReadOnly; round 2 should then be the
// one CheapReads downgrades, since AdaptiveModel is off here.
func TestCheapReadsDownshiftNotice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)
	readInput, _ := json.Marshal(map[string]string{"path": "f.txt"})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t1", Name: "read_file", Input: readInput},
		}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := adaptiveAgent(t, fake, false, adaptiveSessionModel, reg)
	ag.cfg.CheapReads = true

	var notices []string
	if err := ag.RunWith(context.Background(), "read the file", nil, func(ev Event) {
		if info, ok := ev.(Info); ok && strings.Contains(info.Text, "auto: using") {
			notices = append(notices, info.Text)
		}
	}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("downshift notices = %v, want exactly 1", notices)
	}
	if !strings.Contains(notices[0], "cheap reads") {
		t.Errorf("notice = %q, want reason %q", notices[0], "cheap reads")
	}
	// Sanity: round 1 (setting up lastStepReadOnly) ran on the strong model;
	// round 2 (the actual downshift) ran on the cheap one.
	if len(fake.sentModels) != 2 || fake.sentModels[0] != adaptiveSessionModel || fake.sentModels[1] != adaptiveFastModel {
		t.Errorf("sentModels = %v, want [%s %s]", fake.sentModels, adaptiveSessionModel, adaptiveFastModel)
	}
}
