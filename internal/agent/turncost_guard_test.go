package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

// The per-turn spend cap is driven by GuardCostUSD, not CostUSD.
//
// CostUSD -> CostUSDFull returns 0 for a model absent from the price table, so a cap fed
// by it silently never fires: a runaway turn on any unpriced model (most non-flagship
// ones) ran on to the step ceiling instead of stopping at MN_MAX_TURN_COST_USD.
func TestGuardCostPricesUnknownModels(t *testing.T) {
	u := llm.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}

	const unknown = "some-model-not-in-the-price-table-xyz"
	if display := u.CostUSD(unknown); display != 0 {
		t.Fatalf("precondition changed: CostUSD(unknown) = %v, expected 0", display)
	}
	guard := u.GuardCostUSD(unknown)
	if guard <= 0 {
		t.Fatalf("GuardCostUSD(unknown) = %v; a cap fed by this can never fire", guard)
	}
	// Conservative direction: over-counting is safe for a cap, under-counting is not.
	if guard < 10 {
		t.Errorf("GuardCostUSD = %v, expected the conservative Opus-class rate (~$90/2M tokens)", guard)
	}
}

// For a KNOWN model the two must agree exactly, so switching the guard rails to
// GuardCostUSD cannot change behaviour for priced models.
func TestGuardCostMatchesDisplayForKnownModels(t *testing.T) {
	u := llm.Usage{InputTokens: 12_345, OutputTokens: 6_789, CacheReadInputTokens: 1_000}
	for _, model := range []string{"claude-sonnet-4-5", "gpt-4o", "claude-haiku-4-5"} {
		display, guard := u.CostUSD(model), u.GuardCostUSD(model)
		if display == 0 {
			continue // not in this build's table; the unknown-model test covers that path
		}
		if display != guard {
			t.Errorf("%s: display %v != guard %v; a priced model must not be re-rated", model, display, guard)
		}
	}
}

// Zero usage must cost zero under both, so an idle turn can't trip a cap.
func TestGuardCostZeroUsage(t *testing.T) {
	var u llm.Usage
	if got := u.GuardCostUSD("anything-at-all"); got != 0 {
		t.Errorf("GuardCostUSD with no tokens = %v, want 0", got)
	}
}

// turnHardStopReason must actually fire once the guard figure crosses the cap.
func TestTurnHardStopFiresOnCost(t *testing.T) {
	a := &Agent{}
	if reason := a.turnHardStopReason(0, 5.0, 3, 6.25); reason == "" {
		t.Error("a turn costing $6.25 against a $5.00 cap must hard stop")
	}
	if reason := a.turnHardStopReason(0, 5.0, 3, 1.10); reason != "" {
		t.Errorf("a turn under the cap must not stop, got %q", reason)
	}
	// No cap configured: never stop on cost.
	if reason := a.turnHardStopReason(0, 0, 3, 999); reason != "" {
		t.Errorf("no cap configured must not stop on cost, got %q", reason)
	}
}

// A sub-agent's cost must reach the parent's cap via the GUARD figure. Folding its
// display cost instead would let delegation to an unpriced model bypass the cap entirely:
// the sub-agent reports $0, so the parent's cap never moves.
func TestSubAgentGuardCostFoldsIntoParentCap(t *testing.T) {
	acct := &subUsageAcct{}
	// A sub-agent turn on an unpriced model: real charge unknown ($0 displayed), guard
	// figure conservative and non-zero.
	acct.addTokens(1000, 500, 0, 0, 4.25)

	in, out, cacheRead, cost, guard := acct.totals()
	if in != 1000 || out != 500 || cacheRead != 0 {
		t.Errorf("tokens = %d/%d/%d, want 1000/500/0", in, out, cacheRead)
	}
	if cost != 0 {
		t.Errorf("display cost = %v, want 0 (the model is unpriced)", cost)
	}
	if guard != 4.25 {
		t.Fatalf("guard cost = %v, want 4.25; the parent cap would under-count", guard)
	}
}

// The two accumulators must stay independent across several sub-agent turns.
func TestSubUsageAcctAccumulatesBothCosts(t *testing.T) {
	acct := &subUsageAcct{}
	acct.addTokens(10, 20, 5, 1.50, 1.50) // priced model: both equal
	acct.addTokens(30, 40, 0, 0, 2.25)    // unpriced: display 0, guard conservative

	_, _, _, cost, guard := acct.totals()
	if cost != 1.50 {
		t.Errorf("display total = %v, want 1.50", cost)
	}
	if guard != 3.75 {
		t.Errorf("guard total = %v, want 3.75", guard)
	}
}

// A nil accumulator must be safe and return zeroes for both figures.
func TestSubUsageAcctNilSafe(t *testing.T) {
	var acct *subUsageAcct
	acct.addTokens(1, 2, 3, 4, 5) // must not panic
	in, out, cacheRead, cost, guard := acct.totals()
	if in|out|cacheRead != 0 || cost != 0 || guard != 0 {
		t.Errorf("nil accumulator returned %d/%d/%d %v %v, want all zero", in, out, cacheRead, cost, guard)
	}
}
