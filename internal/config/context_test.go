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
