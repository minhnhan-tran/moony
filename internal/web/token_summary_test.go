package web

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// /tokens surfaces the prompt-cache split so you can tell whether the route is
// discounting cached reads (compare the full-price estimate to the quota).
func TestTokensSummaryShowsCache(t *testing.T) {
	cfg := &config.Config{Model: "anthropic.claude-opus-4-8"}

	hot := &session{tokensIn: 1000, tokensOut: 100, cachedIn: 900, cacheCreateIn: 50, lastTurnIn: 1000, quotaKnown: true, quotaSpend: 0.30}
	got := tokensSummary(hot, cfg)
	if !strings.Contains(got, "Prompt cache — 900 read from cache (90% of input), 50 written") {
		t.Errorf("missing/incorrect cache split:\n%s", got)
	}
	if !strings.Contains(got, "cache-adjusted") {
		t.Errorf("cost line should be labeled as a cache-adjusted estimate:\n%s", got)
	}
	if !strings.Contains(got, "at full price") || !strings.Contains(got, "saved") {
		t.Errorf("cost line should compare against the full price and show savings:\n%s", got)
	}
	if !strings.Contains(got, "discounted") {
		t.Errorf("should hint at comparing the quota to the estimate:\n%s", got)
	}

	cold := &session{tokensIn: 1000, tokensOut: 100}
	if got := tokensSummary(cold, cfg); !strings.Contains(got, "caching not engaging") {
		t.Errorf("zero cached reads should report caching not engaging:\n%s", got)
	}
}
