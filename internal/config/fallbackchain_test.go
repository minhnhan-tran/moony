package config

import (
	"reflect"
	"testing"

	"github.com/pysty1995/moony/internal/provider"
)

func TestParseFallbackChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []FallbackTarget
	}{
		{name: "empty", in: "", want: []FallbackTarget{}},
		{
			name: "bare providers",
			in:   "anthropic,openai",
			want: []FallbackTarget{{Provider: provider.Anthropic}, {Provider: provider.OpenAI}},
		},
		{
			name: "whitespace and semicolons separate too",
			in:   " anthropic ; openai \n groq ",
			want: []FallbackTarget{{Provider: provider.Anthropic}, {Provider: provider.OpenAI}, {Provider: provider.Groq}},
		},
		{
			name: "pinned model",
			in:   "openai:gpt-4o",
			want: []FallbackTarget{{Provider: provider.OpenAI, Model: "gpt-4o"}},
		},
		{
			name: "pinned model and reasoning",
			in:   "openai:gpt-4o:high",
			want: []FallbackTarget{{Provider: provider.OpenAI, Model: "gpt-4o", Reasoning: ReasoningHigh}},
		},
		{
			// Ollama ids legitimately contain colons, so only a segment that really is
			// a reasoning level may be peeled off the end.
			name: "colon-bearing model id is kept whole",
			in:   "ollama:qwen3-coder:30b",
			want: []FallbackTarget{{Provider: provider.Ollama, Model: "qwen3-coder:30b"}},
		},
		{
			name: "colon-bearing model id plus reasoning",
			in:   "ollama:qwen3-coder:30b:high",
			want: []FallbackTarget{{Provider: provider.Ollama, Model: "qwen3-coder:30b", Reasoning: ReasoningHigh}},
		},
		{
			name: "empty model slot sets reasoning only",
			in:   "anthropic::low",
			want: []FallbackTarget{{Provider: provider.Anthropic, Reasoning: ReasoningLow}},
		},
		{
			// A typo must never become a live switch target.
			name: "unknown providers are dropped",
			in:   "anthropic,opneai,definitely-not-real",
			want: []FallbackTarget{{Provider: provider.Anthropic}},
		},
		{
			name: "duplicates collapse, first wins",
			in:   "openai:gpt-4o,openai:gpt-4o-mini",
			want: []FallbackTarget{{Provider: provider.OpenAI, Model: "gpt-4o"}},
		},
		{
			name: "case is normalized",
			in:   "OpenAI,ANTHROPIC",
			want: []FallbackTarget{{Provider: provider.OpenAI}, {Provider: provider.Anthropic}},
		},
	} {
		got := ParseFallbackChain(tc.in)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ParseFallbackChain(%q) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}
}

// The spec form must round-trip, since it's what gets persisted in prefs.
func TestFallbackChainSpecRoundTrip(t *testing.T) {
	for _, in := range []string{
		"anthropic,openai",
		"openai:gpt-4o",
		"openai:gpt-4o:high",
		"ollama:qwen3-coder:30b:high",
		"anthropic::low",
	} {
		chain := ParseFallbackChain(in)
		spec := FallbackChainSpec(chain)
		again := ParseFallbackChain(spec)
		if !reflect.DeepEqual(chain, again) {
			t.Errorf("round-trip of %q via %q: %+v != %+v", in, spec, chain, again)
		}
	}
}

func TestFallbackChainProviders(t *testing.T) {
	got := FallbackChainProviders(ParseFallbackChain("anthropic,openai:gpt-4o,ollama:qwen3-coder:30b:high"))
	want := []string{provider.Anthropic, provider.OpenAI, provider.Ollama}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStrictReasoning(t *testing.T) {
	for _, lvl := range []string{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningAuto} {
		if got, ok := StrictReasoning(lvl); !ok || got != lvl {
			t.Errorf("StrictReasoning(%q) = %q,%v; want %q,true", lvl, got, ok, lvl)
		}
	}
	if got, ok := StrictReasoning("HIGH"); !ok || got != ReasoningHigh {
		t.Errorf("StrictReasoning is case-insensitive: got %q,%v", got, ok)
	}
	// Crucially NOT a reasoning level — these appear as model-id fragments, and
	// treating one as a level would corrupt the model id.
	for _, s := range []string{"", "30b", "qwen3-coder", "gpt-4o", "turbo"} {
		if got, ok := StrictReasoning(s); ok {
			t.Errorf("StrictReasoning(%q) = %q,true; want false", s, got)
		}
	}
}

// FallbackChain reads the live config, which is what the agent switches on.
func TestConfigFallbackChain(t *testing.T) {
	c := &Config{ProviderFallbacks: []string{"anthropic", "ollama:qwen3-coder:high"}}
	chain := c.FallbackChain()
	if len(chain) != 2 {
		t.Fatalf("chain = %+v, want 2 hops", chain)
	}
	if chain[1].Provider != provider.Ollama || chain[1].Model != "qwen3-coder" || chain[1].Reasoning != ReasoningHigh {
		t.Errorf("hop 2 = %+v", chain[1])
	}
	var nilCfg *Config
	if got := nilCfg.FallbackChain(); got != nil {
		t.Errorf("nil config must yield a nil chain, got %+v", got)
	}
}

// The env vars are the deployment-side surface for the feature.
func TestQuotaFallbackEnv(t *testing.T) {
	t.Setenv("MN_QUOTA_FALLBACK", "1")
	t.Setenv("MN_QUOTA_BUDGET_USD", "25.50")
	t.Setenv("MN_QUOTA_BUDGET_PCT", "80")
	t.Setenv("MN_PROVIDER_FALLBACKS", "anthropic,openai:gpt-4o")
	t.Setenv("MN_QUOTA_RESET_AT", "07:15")

	c := Load()
	if !c.QuotaFallback {
		t.Error("MN_QUOTA_FALLBACK=1 must enable the policy")
	}
	if c.QuotaBudgetUSD != 25.50 {
		t.Errorf("QuotaBudgetUSD = %v, want 25.50", c.QuotaBudgetUSD)
	}
	if c.QuotaBudgetPct != 80 {
		t.Errorf("QuotaBudgetPct = %v, want 80", c.QuotaBudgetPct)
	}
	if c.QuotaResetAt != "07:15" {
		t.Errorf("QuotaResetAt = %q, want 07:15", c.QuotaResetAt)
	}
	chain := c.FallbackChain()
	if len(chain) != 2 || chain[1].Model != "gpt-4o" {
		t.Errorf("chain = %+v, want anthropic then openai:gpt-4o", chain)
	}
}

// An explicitly EMPTY reset time means "disable the timed restore", which must beat
// the built-in default — the reason it's read with LookupEnv.
func TestQuotaResetAtEmptyEnvDisables(t *testing.T) {
	t.Setenv("MN_QUOTA_RESET_AT", "")
	if got := Load().QuotaResetAt; got != "" {
		t.Errorf("QuotaResetAt = %q, want empty (explicitly disabled)", got)
	}
}

// Unset must leave the built-in default in place, so the timed restore works out of
// the box once the feature is enabled.
func TestQuotaResetAtDefault(t *testing.T) {
	if got := Load().QuotaResetAt; got == "" {
		t.Error("QuotaResetAt must have a default when the env var is unset")
	}
}

// The fallback schema must survive a restart, and the pointer fields exist so that
// "explicitly off / explicitly empty" is distinguishable from "never set".
func TestQuotaFallbackPrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Keep the env from overriding what we're testing.
	for _, k := range []string{"MN_QUOTA_FALLBACK", "MN_QUOTA_BUDGET_USD", "MN_QUOTA_BUDGET_PCT", "MN_PROVIDER_FALLBACKS"} {
		t.Setenv(k, "")
	}

	on := true
	usd, pct := 30.0, 70.0
	chain := []string{"anthropic", "openai:gpt-4o:high"}
	reset := ""
	save := &Config{
		QuotaFallback:            true,
		QuotaFallbackPersona:     &on,
		QuotaBudgetUSD:           usd,
		QuotaBudgetUSDPersona:    &usd,
		QuotaBudgetPct:           pct,
		QuotaBudgetPctPersona:    &pct,
		QuotaResetAtPersona:      &reset, // explicitly disabled
		ProviderFallbacks:        chain,
		ProviderFallbacksPersona: &chain,
	}
	if err := save.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load()
	if !got.QuotaFallback {
		t.Error("QuotaFallback must survive a reload")
	}
	if got.QuotaBudgetUSD != 30 || got.QuotaBudgetPct != 70 {
		t.Errorf("budgets = %v/%v, want 30/70", got.QuotaBudgetUSD, got.QuotaBudgetPct)
	}
	// An explicitly emptied reset time must stay empty, not fall back to the default.
	if got.QuotaResetAt != "" {
		t.Errorf("QuotaResetAt = %q, want the explicitly-disabled empty value", got.QuotaResetAt)
	}
	parsed := got.FallbackChain()
	if len(parsed) != 2 {
		t.Fatalf("chain = %+v, want 2 hops", parsed)
	}
	if parsed[1].Model != "gpt-4o" || parsed[1].Reasoning != ReasoningHigh {
		t.Errorf("hop 2 = %+v, want the pinned model and reasoning to survive", parsed[1])
	}
}
