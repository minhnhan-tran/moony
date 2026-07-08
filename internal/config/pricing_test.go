package config

import "testing"

func TestPriceForStripsPrefix(t *testing.T) {
	want, ok := PriceFor("anthropic.claude-opus-4-8")
	if !ok || want.OutputUSDPer1M == 0 {
		t.Fatalf("expected price for opus, got %+v ok=%v", want, ok)
	}
	awsPrice, ok := PriceFor("aws:anthropic.claude-opus-4-8")
	if !ok || awsPrice != want {
		t.Errorf("aws: prefix should resolve to same price, got %+v", awsPrice)
	}
	gemini, ok := PriceFor("gcp:gemini-2.5-pro")
	if !ok || gemini.OutputUSDPer1M == 0 {
		t.Errorf("gemini price missing: %+v", gemini)
	}
}

func TestPriceUnknownReturnsZero(t *testing.T) {
	if got := CostUSD("not.a.real.model", 1_000_000, 1_000_000); got != 0 {
		t.Errorf("unknown model should cost 0, got %v", got)
	}
}

func TestCostUSDArithmetic(t *testing.T) {
	// 1M in × 1M out at $3/$15 → $18.00
	got := CostUSD("anthropic.claude-sonnet-4-6", 1_000_000, 1_000_000)
	if got < 17.99 || got > 18.01 {
		t.Errorf("expected ~$18, got %v", got)
	}
}

func TestRuntimeOverlayWinsOverStaticTable(t *testing.T) {
	t.Cleanup(ClearRuntimePricing)
	// Static table says $15/$75 per 1M for opus. Overlay something different.
	OverlayRuntimePricing("aws:anthropic.claude-opus-4-8", 0.000020, 0.000080, 0, 0) // $20/$80 per 1M
	got, ok := PriceFor("aws:anthropic.claude-opus-4-8")
	if !ok || got.InputUSDPer1M != 20 || got.OutputUSDPer1M != 80 {
		t.Fatalf("runtime overlay should win: got %+v", got)
	}
	// And cost computes from those.
	if c := CostUSD("aws:anthropic.claude-opus-4-8", 1_000_000, 0); c != 20 {
		t.Errorf("expected $20, got %v", c)
	}
	// Without prefix should still hit the same overlay.
	if got, _ := PriceFor("anthropic.claude-opus-4-8"); got.InputUSDPer1M != 20 {
		t.Errorf("prefix-stripped lookup missed overlay: %+v", got)
	}
}

func TestRuntimeOverlayIgnoresZeros(t *testing.T) {
	t.Cleanup(ClearRuntimePricing)
	OverlayRuntimePricing("anthropic.claude-sonnet-4-6", 0, 0, 0, 0)
	// Should still resolve to the static table.
	got, _ := PriceFor("anthropic.claude-sonnet-4-6")
	if got.InputUSDPer1M != 3 {
		t.Errorf("zero overlay should not poison the static table: %+v", got)
	}
}

func TestClearRuntimePricing(t *testing.T) {
	OverlayRuntimePricing("anthropic.claude-sonnet-4-6", 0.001, 0.001, 0, 0)
	ClearRuntimePricing()
	got, _ := PriceFor("anthropic.claude-sonnet-4-6")
	if got.InputUSDPer1M == 1000 {
		t.Errorf("clear should remove the overlay; still seeing %+v", got)
	}
}

// TestCostUSDFullDerivesCacheRates checks that, with no explicit cache rates,
// cached reads bill at ~0.1x input and cache writes at ~1.25x input — and that
// InputTokens is treated as the full prompt (cache tokens are subsets of it).
func TestCostUSDFullDerivesCacheRates(t *testing.T) {
	t.Cleanup(ClearRuntimePricing)
	// Sonnet: $3/1M input, $15/1M output. Derived: read $0.30/1M, write $3.75/1M.
	const model = "anthropic.claude-sonnet-4-6"

	// All input cached-read: 100k uncached + 900k read (subset of the 1M in).
	// 100k*$3 + 900k*$0.30 = $0.30 + $0.27 = $0.57.
	got := CostUSDFull(model, 1_000_000, 0, 900_000, 0)
	if got < 0.569 || got > 0.571 {
		t.Errorf("cache-read cost = %v, want ~0.57", got)
	}
	// Cache write: 800k uncached + 200k written. 800k*$3 + 200k*$3.75 = $2.40 + $0.75 = $3.15.
	got = CostUSDFull(model, 1_000_000, 0, 0, 200_000)
	if got < 3.149 || got > 3.151 {
		t.Errorf("cache-write cost = %v, want ~3.15", got)
	}
	// With zero cache tokens it must equal the full-price CostUSD.
	if full, fresh := CostUSD(model, 1_000_000, 1_000_000), CostUSDFull(model, 1_000_000, 1_000_000, 0, 0); full != fresh {
		t.Errorf("no-cache CostUSDFull (%v) must equal CostUSD (%v)", fresh, full)
	}
	// And caching must be cheaper than paying full price for the same prompt.
	if cached, full := CostUSDFull(model, 1_000_000, 0, 900_000, 0), CostUSD(model, 1_000_000, 0); cached >= full {
		t.Errorf("cached cost %v should be below full price %v", cached, full)
	}
}

// TestCostUSDFullUsesExplicitCacheRates checks that Floodgate-supplied cache
// rates override the derived multipliers.
func TestCostUSDFullUsesExplicitCacheRates(t *testing.T) {
	t.Cleanup(ClearRuntimePricing)
	const model = "anthropic.claude-sonnet-4-6"
	// Explicit cache-read rate of $1/1M (not the derived $0.30/1M).
	OverlayRuntimePricing(model, 0.000003, 0.000015, 0.000001, 0.000004)
	// 0 uncached + 1M read at the explicit $1/1M = $1.00 (derived would be $0.30).
	if got := CostUSDFull(model, 1_000_000, 0, 1_000_000, 0); got < 0.999 || got > 1.001 {
		t.Errorf("explicit cache-read rate not used: got %v, want ~1.00", got)
	}
}

// TestCostUSDFullClampsNegative guards the defensive floor: if cache tokens
// somehow exceed input, uncached never goes negative.
func TestCostUSDFullClampsNegative(t *testing.T) {
	const model = "anthropic.claude-sonnet-4-6"
	if got := CostUSDFull(model, 100, 0, 200, 0); got < 0 {
		t.Errorf("cost must never be negative, got %v", got)
	}
}
