package web

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// The persona panel is the only place the fallback can be configured from the UI, so
// each knob must be present, and each must round-trip through applyPersonaEdit.
func TestPersonaExposesQuotaFallbackFields(t *testing.T) {
	c := &config.Config{
		QuotaFallback:     true,
		QuotaBudgetUSD:    25,
		QuotaBudgetPct:    80,
		QuotaResetAt:      "07:15",
		ProviderFallbacks: []string{"anthropic", "openai:gpt-4o"},
	}
	fields := personaFields(c, nil, nil, nil)
	want := map[string]bool{
		"quotaFallback": false, "providerFallbacks": false,
		"quotaBudgetUsd": false, "quotaBudgetPct": false, "quotaResetAt": false,
	}
	byKey := map[string]personaField{}
	for _, f := range fields {
		byKey[f.Key] = f
		if _, ok := want[f.Key]; ok {
			want[f.Key] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("persona panel is missing the %q field", k)
		}
	}
	if got := byKey["quotaFallback"].Value; got != true {
		t.Errorf("quotaFallback value = %v, want true", got)
	}
	if got := byKey["providerFallbacks"].Value; got != "anthropic,openai:gpt-4o" {
		t.Errorf("providerFallbacks value = %v, want the chain spec", got)
	}
	// An unset budget must read as empty, not "0" — "0" looks like a configured
	// threshold of zero, which would mean "fall back immediately".
	if got := byKey["quotaBudgetUsd"].Value; got != "25" {
		t.Errorf("quotaBudgetUsd value = %v, want \"25\"", got)
	}
	empty := personaFields(&config.Config{}, nil, nil, nil)
	for _, f := range empty {
		if f.Key == "quotaBudgetUsd" || f.Key == "quotaBudgetPct" {
			if f.Value != "" {
				t.Errorf("%s with no budget = %q, want empty", f.Key, f.Value)
			}
		}
	}
}

func TestApplyPersonaEditQuotaFallback(t *testing.T) {
	str := func(s string) *string { return &s }
	tru := true

	t.Run("sets every knob and remembers it as a persona choice", func(t *testing.T) {
		c := &config.Config{}
		applyPersonaEdit(c, personaEdit{
			QuotaFallback:     &tru,
			QuotaBudgetUSD:    str("30.5"),
			QuotaBudgetPct:    str("75"),
			QuotaResetAt:      str("06:30"),
			ProviderFallbacks: str("anthropic, openai:gpt-4o"),
		})
		if !c.QuotaFallback || c.QuotaFallbackPersona == nil || !*c.QuotaFallbackPersona {
			t.Error("QuotaFallback must be set and remembered")
		}
		if c.QuotaBudgetUSD != 30.5 || c.QuotaBudgetPct != 75 {
			t.Errorf("budgets = %v/%v, want 30.5/75", c.QuotaBudgetUSD, c.QuotaBudgetPct)
		}
		if c.QuotaResetAt != "06:30" {
			t.Errorf("QuotaResetAt = %q", c.QuotaResetAt)
		}
		if len(c.ProviderFallbacks) != 2 || !strings.Contains(c.ProviderFallbacks[1], "gpt-4o") {
			t.Errorf("chain = %v", c.ProviderFallbacks)
		}
		if c.ProviderFallbacksPersona == nil {
			t.Error("the chain must be remembered as a persona choice")
		}
	})

	t.Run("a typo'd budget clears the trigger rather than keeping a stale one", func(t *testing.T) {
		c := &config.Config{QuotaBudgetUSD: 99, QuotaBudgetPct: 50}
		applyPersonaEdit(c, personaEdit{QuotaBudgetUSD: str("abc"), QuotaBudgetPct: str("-3")})
		if c.QuotaBudgetUSD != 0 || c.QuotaBudgetPct != 0 {
			t.Errorf("budgets = %v/%v, want both cleared", c.QuotaBudgetUSD, c.QuotaBudgetPct)
		}
	})

	t.Run("an out-of-range percentage is rejected", func(t *testing.T) {
		c := &config.Config{}
		applyPersonaEdit(c, personaEdit{QuotaBudgetPct: str("150")})
		if c.QuotaBudgetPct != 0 {
			t.Errorf("QuotaBudgetPct = %v, want 0 for an out-of-range value", c.QuotaBudgetPct)
		}
	})

	t.Run("an empty reset time is a real choice (disables the timed restore)", func(t *testing.T) {
		c := &config.Config{QuotaResetAt: "07:15"}
		applyPersonaEdit(c, personaEdit{QuotaResetAt: str("")})
		if c.QuotaResetAt != "" {
			t.Errorf("QuotaResetAt = %q, want empty", c.QuotaResetAt)
		}
		if c.QuotaResetAtPersona == nil || *c.QuotaResetAtPersona != "" {
			t.Error("the empty choice must be remembered, not treated as unset")
		}
	})

	t.Run("typo'd providers never become live hops", func(t *testing.T) {
		c := &config.Config{}
		applyPersonaEdit(c, personaEdit{ProviderFallbacks: str("anthropic,opneai,not-a-provider")})
		if len(c.ProviderFallbacks) != 1 || !strings.HasPrefix(c.ProviderFallbacks[0], "anthropic") {
			t.Errorf("chain = %v, want only the valid provider", c.ProviderFallbacks)
		}
	})
}

// The image-provider row must exist and round-trip, since it's the only UI for it.
func TestPersonaImageProviderRow(t *testing.T) {
	c := &config.Config{ImageProvider: "openai"}
	var found bool
	for _, f := range personaFields(c, nil, nil, nil) {
		if f.Key == "imageProvider" {
			found = true
			if f.Value != "openai" {
				t.Errorf("imageProvider value = %v, want openai", f.Value)
			}
			if f.Type != "select" || len(f.Options) == 0 {
				t.Errorf("imageProvider should be a populated select, got type=%q %d options", f.Type, len(f.Options))
			}
		}
	}
	if !found {
		t.Fatal("the persona panel is missing the imageProvider row")
	}
}

func TestApplyPersonaEditImageProvider(t *testing.T) {
	str := func(s string) *string { return &s }

	t.Run("sets the field, not just the env", func(t *testing.T) {
		// The field matters: unlike the Fast/Aux tiers (read from the env at call
		// time), the image route lives on Config, so an env-only write wouldn't take
		// effect until the next restart.
		c := &config.Config{}
		applyPersonaEdit(c, personaEdit{ImageProvider: str("openai")})
		if c.ImageProvider != "openai" {
			t.Errorf("ImageProvider = %q, want openai", c.ImageProvider)
		}
		if c.ImageProviderPersona == nil || *c.ImageProviderPersona != "openai" {
			t.Error("the choice must be remembered for persistence")
		}
	})

	t.Run("an explicit clear means inherit", func(t *testing.T) {
		c := &config.Config{ImageProvider: "openai"}
		applyPersonaEdit(c, personaEdit{ImageProvider: str("")})
		if c.ImageProvider != "" {
			t.Errorf("ImageProvider = %q, want empty", c.ImageProvider)
		}
		if c.ImageProviderPersona == nil || *c.ImageProviderPersona != "" {
			t.Error("an explicit inherit is a real choice and must be remembered")
		}
	})

	t.Run("a typo never becomes a live route", func(t *testing.T) {
		c := &config.Config{}
		applyPersonaEdit(c, personaEdit{ImageProvider: str("opneai")})
		if c.ImageProvider != "" {
			t.Errorf("ImageProvider = %q, want empty for an unknown provider", c.ImageProvider)
		}
	})
}
