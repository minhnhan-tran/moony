package config

// Pricing tables for the curated Anthropic/Bedrock and Gemini models exposed
// via Floodgate. The `modelPrices` map contains best-effort public list prices
// ($ per 1M tokens) at the time of writing — Apple's actual cost may differ.
//
// At startup the agent calls Floodgate's /api/usage/v1/{personal,project}
// endpoint and overlays the authoritative per-model pricing returned there
// onto the runtime store, via OverlayRuntimePricing. CostUSD checks the
// runtime store first and falls back to the static table.

import "sync"

// ModelPrice holds per-million-token rates in US dollars. The cache rates are
// optional: when zero (the static table never states them, and Floodgate may
// omit them), they're derived from the input rate via the standard Anthropic
// multipliers — see cacheReadPer1M / cacheWritePer1M.
type ModelPrice struct {
	InputUSDPer1M      float64
	OutputUSDPer1M     float64
	CacheReadUSDPer1M  float64 // 0 => derive from input (cacheReadMultiplier)
	CacheWriteUSDPer1M float64 // 0 => derive from input (cacheWriteMultiplier)
}

const (
	// Standard Anthropic prompt-cache multipliers relative to the base input
	// rate: cached reads bill ~0.1x, and (5-minute) cache writes ~1.25x. Used to
	// derive cache rates when a model's pricing doesn't state them explicitly.
	cacheReadMultiplier  = 0.1
	cacheWriteMultiplier = 1.25
)

// cacheReadPer1M is the per-million-token rate for cached input reads: the
// explicit rate when known, otherwise derived from the input rate.
func (p ModelPrice) cacheReadPer1M() float64 {
	if p.CacheReadUSDPer1M > 0 {
		return p.CacheReadUSDPer1M
	}
	return p.InputUSDPer1M * cacheReadMultiplier
}

// cacheWritePer1M is the per-million-token rate for cache-creation (write)
// tokens: the explicit rate when known, otherwise derived from the input rate.
func (p ModelPrice) cacheWritePer1M() float64 {
	if p.CacheWriteUSDPer1M > 0 {
		return p.CacheWriteUSDPer1M
	}
	return p.InputUSDPer1M * cacheWriteMultiplier
}

// modelPrices maps a Floodgate model id (sans cloud-provider prefix) to its
// public list price. Cache rates are left unset here (derived from the input
// rate via the standard multipliers); the Floodgate runtime overlay supplies
// explicit cache rates when it has them.
var modelPrices = map[string]ModelPrice{
	// Anthropic Claude
	"anthropic.claude-opus-4-8":                 {InputUSDPer1M: 5, OutputUSDPer1M: 25},
	"anthropic.claude-opus-4-1-20250805-v1:0":   {InputUSDPer1M: 15, OutputUSDPer1M: 75},
	"anthropic.claude-opus-4-20250514-v1:0":     {InputUSDPer1M: 15, OutputUSDPer1M: 75},
	"anthropic.claude-sonnet-5":                 {InputUSDPer1M: 3, OutputUSDPer1M: 15},
	"anthropic.claude-sonnet-4-6":               {InputUSDPer1M: 3, OutputUSDPer1M: 15},
	"anthropic.claude-sonnet-4-5-20250929-v1:0": {InputUSDPer1M: 3, OutputUSDPer1M: 15},
	"anthropic.claude-sonnet-4-20250514-v1:0":   {InputUSDPer1M: 3, OutputUSDPer1M: 15},
	"anthropic.claude-3-7-sonnet-20250219-v1:0": {InputUSDPer1M: 3, OutputUSDPer1M: 15},
	"anthropic.claude-haiku-4-5-20251001-v1:0":  {InputUSDPer1M: 1, OutputUSDPer1M: 5},
	"anthropic.claude-3-5-haiku-20241022-v1:0":  {InputUSDPer1M: 0.80, OutputUSDPer1M: 4},
	// Google Gemini (via Vertex pricing)
	"gemini-3.1-pro-preview": {InputUSDPer1M: 1.25, OutputUSDPer1M: 5},
	"gemini-2.5-pro":         {InputUSDPer1M: 1.25, OutputUSDPer1M: 10},
}

// runtimePrices holds Floodgate-supplied prices fetched at startup. Reads can
// happen from any goroutine (the TUI's update loop while a streaming-token
// response is being priced); writes only happen from the startup overlay
// callback. A RWMutex keeps it safe even though contention is near zero.
var (
	runtimeMu     sync.RWMutex
	runtimePrices = map[string]ModelPrice{}
)

// stripPrefix removes the OpenAI-route cloud prefix so model lookups work
// regardless of which route the id came from.
func stripPrefix(model string) string {
	for _, p := range []string{"aws:", "gcp:"} {
		if len(model) > len(p) && model[:len(p)] == p {
			return model[len(p):]
		}
	}
	return model
}

// OverlayRuntimePricing replaces the runtime price for one model id. Pass
// per-token costs (the API's native unit); they are converted to per-million
// internally. cacheRead/cacheWrite may be 0 when Floodgate doesn't report them,
// in which case CostUSDFull derives them from the input rate.
func OverlayRuntimePricing(model string, inputCostPerToken, outputCostPerToken, cacheReadCostPerToken, cacheWriteCostPerToken float64) {
	if inputCostPerToken == 0 && outputCostPerToken == 0 {
		return
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtimePrices[stripPrefix(model)] = ModelPrice{
		InputUSDPer1M:      inputCostPerToken * 1_000_000,
		OutputUSDPer1M:     outputCostPerToken * 1_000_000,
		CacheReadUSDPer1M:  cacheReadCostPerToken * 1_000_000,
		CacheWriteUSDPer1M: cacheWriteCostPerToken * 1_000_000,
	}
}

// ClearRuntimePricing wipes the runtime overlay (used by tests).
func ClearRuntimePricing() {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtimePrices = map[string]ModelPrice{}
}

// PriceFor returns the pricing for a model id, stripping the "aws:"/"gcp:"
// cloud-provider prefix used on the OpenAI route. The runtime overlay (set
// from Floodgate at startup) wins; otherwise the static table is consulted.
func PriceFor(model string) (ModelPrice, bool) {
	key := stripPrefix(model)
	runtimeMu.RLock()
	if p, ok := runtimePrices[key]; ok {
		runtimeMu.RUnlock()
		return p, true
	}
	runtimeMu.RUnlock()
	p, ok := modelPrices[key]
	return p, ok
}

// CostUSD returns the dollar cost of (in, out) tokens for a model at full input
// price (no prompt-cache discount). Unknown models return 0. Prefer CostUSDFull
// when cache-token counts are available, so cached reads bill at their cheaper
// rate.
func CostUSD(model string, inTokens, outTokens int) float64 {
	p, ok := PriceFor(model)
	if !ok {
		return 0
	}
	return float64(inTokens)*p.InputUSDPer1M/1_000_000 + float64(outTokens)*p.OutputUSDPer1M/1_000_000
}

// costUSD prices the given token counts at this model's rates, accounting for
// prompt caching: uncached input (inTokens − cacheRead − cacheWrite) bills at the
// base rate, cache reads at the discounted read rate (~0.1x), and cache writes at
// the write rate (~1.25x).
func (p ModelPrice) costUSD(inTokens, outTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	uncached := inTokens - cacheReadTokens - cacheWriteTokens
	if uncached < 0 {
		uncached = 0 // defensive: never let a route/rounding quirk go negative
	}
	return float64(uncached)*p.InputUSDPer1M/1_000_000 +
		float64(cacheReadTokens)*p.cacheReadPer1M()/1_000_000 +
		float64(cacheWriteTokens)*p.cacheWritePer1M()/1_000_000 +
		float64(outTokens)*p.OutputUSDPer1M/1_000_000
}

// CostUSDFull prices a turn/call accounting for prompt caching. inTokens is the
// FULL prompt size: the llm parser folds the cached portions into InputTokens on
// both routes (Anthropic via totalInput; the OpenAI route's prompt_tokens already
// includes them), so cacheReadTokens and cacheWriteTokens are subsets of it.
// Unknown models return 0.
func CostUSDFull(model string, inTokens, outTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	p, ok := PriceFor(model)
	if !ok {
		return 0
	}
	return p.costUSD(inTokens, outTokens, cacheReadTokens, cacheWriteTokens)
}

// fallbackModelPrice prices a model with no known rate. It is deliberately set to
// the most expensive tier in the table (Opus-class): for a spend cap, over-counting
// an unrecognized model is the safe direction — far better than pricing it at $0
// and letting it run uncapped.
var fallbackModelPrice = ModelPrice{InputUSDPer1M: 15, OutputUSDPer1M: 75}

// CostUSDFullFallback is CostUSDFull but prices an unknown model at the conservative
// fallbackModelPrice instead of $0. The relay's per-user quota meters with this, so
// an unrecognized model id (a manual /model pick, or one Floodgate serves before the
// price table catches up) still accrues spend and can never run uncapped.
func CostUSDFullFallback(model string, inTokens, outTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	p, ok := PriceFor(model)
	if !ok {
		p = fallbackModelPrice
	}
	return p.costUSD(inTokens, outTokens, cacheReadTokens, cacheWriteTokens)
}
