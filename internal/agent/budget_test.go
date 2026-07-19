package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// budgetTestAgent builds a minimal single-round agent (reusing adaptiveAgent's
// isolateConfig-based hermetic HOME/XDG_CONFIG_HOME setup, so budget.Record's
// ledger file lands in a temp dir, not the real ~/.config/moony).
func budgetTestAgent(t *testing.T) (*Agent, *scriptedStreamer) {
	t.Helper()
	config.ClearRuntimePricing()
	t.Cleanup(config.ClearRuntimePricing)
	config.OverlayRuntimePricing(adaptiveSessionModel, 15e-6, 75e-6, 0, 0) // nonzero, so a turn actually costs something
	fake := &scriptedStreamer{turns: []llm.Response{{
		StopReason: "end_turn",
		Blocks:     []llm.ContentBlock{llm.TextBlock("done")},
		Usage:      llm.Usage{InputTokens: 1000, OutputTokens: 1000},
	}}}
	ag := adaptiveAgent(t, fake, false, adaptiveSessionModel, nil)
	return ag, fake
}

func runAndCollectInfo(t *testing.T, ag *Agent) []string {
	t.Helper()
	var infos []string
	if err := ag.RunWith(context.Background(), "hi", nil, func(ev Event) {
		if e, ok := ev.(Info); ok {
			infos = append(infos, e.Text)
		}
	}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	return infos
}

func TestWarnBudgetSilentWhenUnset(t *testing.T) {
	ag, _ := budgetTestAgent(t)
	t.Setenv("MN_DAILY_BUDGET", "")
	t.Setenv("MN_MONTHLY_BUDGET", "")

	for _, info := range runAndCollectInfo(t, ag) {
		if strings.Contains(info, "budget") {
			t.Errorf("unexpected budget warning with no cap configured: %q", info)
		}
	}
}

func TestWarnBudgetFiresOnceCapCrossed(t *testing.T) {
	ag, _ := budgetTestAgent(t)
	// A cap of $0 worth of "already spent" guarantees this turn's (nonzero)
	// cost crosses it immediately, regardless of the exact priced amount.
	t.Setenv("MN_DAILY_BUDGET", "0.0000001")
	t.Setenv("MN_MONTHLY_BUDGET", "")

	var found bool
	for _, info := range runAndCollectInfo(t, ag) {
		if strings.Contains(info, "daily budget") {
			found = true
		}
	}
	if !found {
		t.Error("expected a daily-budget Info warning once the cap is crossed")
	}
}

func TestWarnBudgetMonthlyOnly(t *testing.T) {
	ag, _ := budgetTestAgent(t)
	t.Setenv("MN_DAILY_BUDGET", "")
	t.Setenv("MN_MONTHLY_BUDGET", "0.0000001")

	var found bool
	for _, info := range runAndCollectInfo(t, ag) {
		if strings.Contains(info, "monthly budget") {
			found = true
		}
	}
	if !found {
		t.Error("expected a monthly-budget Info warning once the cap is crossed")
	}
}
