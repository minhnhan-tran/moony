package config

import "testing"

func TestContextWindowKnownModels(t *testing.T) {
	cases := map[string]int{
		"anthropic.claude-sonnet-4-6":              200_000,
		"anthropic.claude-opus-4-8":                200_000,
		"anthropic.claude-haiku-4-5-20251001-v1:0": 200_000,
		"aws:anthropic.claude-sonnet-4-6":          200_000, // prefix stripped
		"gcp:gemini-2.5-pro":                       1_048_576,
	}
	for model, want := range cases {
		if got := ContextWindow(model); got != want {
			t.Errorf("ContextWindow(%s) = %d, want %d", model, got, want)
		}
	}
}

func TestContextWindowUnknownGetsDefault(t *testing.T) {
	if got := ContextWindow("not-a-real-model"); got != defaultContextWindow {
		t.Errorf("unknown model = %d, want default %d", got, defaultContextWindow)
	}
}

func TestParseTokenCount(t *testing.T) {
	cases := map[string]int{
		"1m": 1_000_000, "1M": 1_000_000, "200k": 200_000, "200K": 200_000,
		"1000000": 1_000_000, "1_000_000": 1_000_000, "1,000,000": 1_000_000, " 131072 ": 131072,
	}
	for in, want := range cases {
		if got, err := ParseTokenCount(in); err != nil || got != want {
			t.Errorf("ParseTokenCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1x", "m", "-5", "-1k", "9999999999999m"} {
		if _, err := ParseTokenCount(bad); err == nil {
			t.Errorf("ParseTokenCount(%q) should error", bad)
		}
	}
}

func TestContextWindowForOverride(t *testing.T) {
	// No override → per-model default.
	c := &Config{}
	if got := c.ContextWindowFor("anthropic.claude-opus-4-8"); got != 200_000 {
		t.Errorf("auto = %d, want 200000", got)
	}
	// Override wins regardless of model.
	c.ContextWindow = 1_000_000
	if got := c.ContextWindowFor("anthropic.claude-opus-4-8"); got != 1_000_000 {
		t.Errorf("override = %d, want 1000000", got)
	}
}

func TestCompactThresholdAndKeep(t *testing.T) {
	c := &Config{}
	// Default dynamic: 80% of context window for threshold, 40% of threshold for keep.
	// 200k model -> 160k threshold, 64k keep.
	if got := c.CompactThresholdFor("anthropic.claude-sonnet-4-6"); got != 160_000 {
		t.Errorf("CompactThresholdFor(200k) = %d, want 160000", got)
	}
	if got := c.CompactKeepFor("anthropic.claude-sonnet-4-6"); got != 64_000 {
		t.Errorf("CompactKeepFor(200k) = %d, want 64000", got)
	}

	// 1M model -> 800k threshold, 320k keep.
	c.ContextWindow = 1_000_000
	if got := c.CompactThresholdFor("any-model"); got != 800_000 {
		t.Errorf("CompactThresholdFor(1M override) = %d, want 800000", got)
	}
	if got := c.CompactKeepFor("any-model"); got != 320_000 {
		t.Errorf("CompactKeepFor(1M override) = %d, want 320000", got)
	}

	// Explicit override of CompactThresholdTokens takes precedence.
	c.CompactThresholdTokens = 50_000
	if got := c.CompactThresholdFor("any-model"); got != 50_000 {
		t.Errorf("CompactThresholdFor(explicit 50k) = %d, want 50000", got)
	}
	// Keep dynamically follows 40% of explicit threshold (20k).
	if got := c.CompactKeepFor("any-model"); got != 20_000 {
		t.Errorf("CompactKeepFor(from explicit 50k threshold) = %d, want 20000", got)
	}

	// Explicit override of CompactKeepTokens takes precedence.
	c.CompactKeepTokens = 15_000
	if got := c.CompactKeepFor("any-model"); got != 15_000 {
		t.Errorf("CompactKeepFor(explicit 15k) = %d, want 15000", got)
	}

	// Disabled threshold (<0) passes through.
	c.CompactThresholdTokens = -1
	if got := c.CompactThresholdFor("any-model"); got != -1 {
		t.Errorf("CompactThresholdFor(disabled -1) = %d, want -1", got)
	}
}
