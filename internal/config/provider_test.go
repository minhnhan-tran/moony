package config

import "testing"

func TestProviderLabel(t *testing.T) {
	cases := map[string]string{
		ProviderAnthropic: "anthropic-native",
		ProviderOpenAI:    "openai-compatible",
		"":                "anthropic-native", // unknown → default
		"bogus":           "anthropic-native",
	}
	for in, want := range cases {
		if got := ProviderLabel(in); got != want {
			t.Errorf("ProviderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
