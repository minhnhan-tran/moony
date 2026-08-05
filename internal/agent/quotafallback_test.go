package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/session"
)

// agentForQuota builds the minimum Agent the quota-fallback policy needs. The
// policy only touches cfg and quotaFB, so no client/registry/store is required.
func agentForQuota(t *testing.T, cfg *config.Config) *Agent {
	t.Helper()
	return &Agent{cfg: cfg}
}

// usableCfg is a config where the providers used across these tests are all
// reachable, so a hop is never skipped for a missing credential unless the test is
// specifically about that.
func usableCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		OpenAIAPIKey:    "sk-openai",
		AnthropicAPIKey: "sk-anthropic",
		GroqAPIKey:      "sk-groq",
		OllamaBaseURL:   "http://localhost:11434/v1",
	}
}

func TestQuotaBreached(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		spend, budget, limUSD, limPct float64
		want                          bool
	}{
		{name: "no thresholds configured never trips", spend: 999, budget: 100, want: false},
		{name: "under the USD budget", spend: 10, limUSD: 40, want: false},
		{name: "at the USD budget", spend: 40, limUSD: 40, want: true},
		{name: "over the USD budget", spend: 41, limUSD: 40, want: true},
		{name: "under the percentage", spend: 50, budget: 100, limPct: 80, want: false},
		{name: "at the percentage", spend: 80, budget: 100, limPct: 80, want: true},
		{name: "either threshold alone can trip (USD)", spend: 45, budget: 1000, limUSD: 40, limPct: 90, want: true},
		{name: "either threshold alone can trip (pct)", spend: 95, budget: 100, limUSD: 500, limPct: 90, want: true},
		// A provider that reports no cap can't be judged on a percentage. Treating
		// the missing cap as 0 would make any spend "100% of budget" and fire on the
		// first dollar, so the relative check must be skipped entirely.
		{name: "percentage is skipped when the provider reports no cap", spend: 500, budget: 0, limPct: 1, want: false},
		{name: "zero spend never trips", spend: 0, budget: 100, limUSD: 40, limPct: 80, want: false},
	} {
		if got := quotaBreached(tc.spend, tc.budget, tc.limUSD, tc.limPct); got != tc.want {
			t.Errorf("%s: quotaBreached(%v,%v,%v,%v) = %v, want %v",
				tc.name, tc.spend, tc.budget, tc.limUSD, tc.limPct, got, tc.want)
		}
	}
}

func TestNextFallbackProvider(t *testing.T) {
	all := func(string) bool { return true }
	chain := []string{provider.Anthropic, provider.OpenAI, provider.Ollama}

	for _, tc := range []struct {
		name    string
		chain   []string
		current string
		usable  func(string) bool
		want    string
	}{
		{name: "advances one step", chain: chain, current: provider.Anthropic, usable: all, want: provider.OpenAI},
		{name: "advances from the middle", chain: chain, current: provider.OpenAI, usable: all, want: provider.Ollama},
		// No wrap-around: cycling back to a provider we just left for being over
		// budget would thrash between backends on every turn.
		{name: "exhausted at the end of the chain", chain: chain, current: provider.Ollama, usable: all, want: ""},
		{name: "empty chain", chain: nil, current: provider.Anthropic, usable: all, want: ""},
		{name: "current not in the chain considers all of it", chain: []string{provider.Ollama}, current: provider.Groq, usable: all, want: provider.Ollama},
		{name: "single-entry chain equal to current is exhausted", chain: []string{provider.Anthropic}, current: provider.Anthropic, usable: all, want: ""},
		{
			name: "skips an unusable hop", chain: chain, current: provider.Anthropic,
			usable: func(id string) bool { return id != provider.OpenAI },
			want:   provider.Ollama,
		},
		{
			name: "exhausted when every later hop is unusable", chain: chain, current: provider.Anthropic,
			usable: func(string) bool { return false },
			want:   "",
		},
	} {
		got := nextFallbackProvider(tc.chain, tc.current, tc.usable)
		if got != tc.want {
			t.Errorf("%s: nextFallbackProvider(%v, %q) = %q, want %q", tc.name, tc.chain, tc.current, got, tc.want)
		}
	}
}

// A chain must never hop to a backend with no credentials: that would turn "you're
// near your quota" into "every turn now fails".
func TestProviderUsable(t *testing.T) {
	bare := &config.Config{}
	if providerUsable(bare, provider.OpenAI) {
		t.Error("openai must be unusable without an API key")
	}
	if providerUsable(bare, "definitely-not-a-provider") {
		t.Error("an unknown provider id must never be usable")
	}
	withKey := &config.Config{OpenAIAPIKey: "sk-x"}
	if !providerUsable(withKey, provider.OpenAI) {
		t.Error("openai must be usable with an API key")
	}
	// Local backends serve unauthenticated requests, so a base URL is enough.
	local := &config.Config{OllamaBaseURL: "http://localhost:11434/v1"}
	if !providerUsable(local, provider.Ollama) {
		t.Error("ollama must be usable with a base URL and no key")
	}
}

func TestMaybeQuotaFallbackDisabledIsNoOp(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = false // the whole point: opt-in
	cfg.LLMProvider = provider.Anthropic
	cfg.QuotaBudgetUSD = 1
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 999, Budget: 100}); msg != "" {
		t.Errorf("disabled policy must not switch: %q", msg)
	}
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("provider = %q, want it untouched", cfg.LLMProvider)
	}
}

// Switching provider must switch model too: model ids are not portable across
// backends, so carrying the old one over would make every later turn fail.
func TestMaybeQuotaFallbackSwitchesAndSetsModel(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100})
	if msg == "" {
		t.Fatal("expected a switch at the budget")
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Fatalf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
	}
	if cfg.Model == "claude-sonnet-4" || cfg.Model == "" {
		t.Errorf("model = %q, want a model valid on the new backend", cfg.Model)
	}
	if want := provider.DefaultModelFor(provider.OpenAI); cfg.Model != want {
		t.Errorf("model = %q, want the new backend's default %q", cfg.Model, want)
	}
	if !strings.Contains(msg, "switched to") {
		t.Errorf("notice should say what happened: %q", msg)
	}
}

func TestMaybeQuotaFallbackPercentThreshold(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.QuotaBudgetPct = 90 // no absolute budget at all
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 50, Budget: 100}); msg != "" {
		t.Fatalf("50%% must not trip a 90%% threshold: %q", msg)
	}
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 90, Budget: 100}); msg == "" {
		t.Fatal("90% must trip a 90% threshold")
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
	}
}

// A hard refusal from the provider outranks the arithmetic: there may be no
// budget numbers at all, and no threshold configured.
func TestMaybeQuotaFallbackHardLimitIgnoresThresholds(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, HardLimit: true}); msg == "" {
		t.Fatal("a hard limit must switch even with no thresholds set")
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
	}
}

// A reading that arrives late, describing a provider we already left, must not
// trigger another hop.
func TestMaybeQuotaFallbackIgnoresStaleSnapshot(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.OpenAI
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI, provider.Ollama}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 999, Budget: 100}); msg != "" {
		t.Errorf("a reading for another provider must be ignored: %q", msg)
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Errorf("provider = %q, want it untouched", cfg.LLMProvider)
	}
}

func TestMaybeQuotaFallbackChainExhausted(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.OpenAI
	cfg.Model = "gpt-4o"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI} // nothing after openai
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.OpenAI, Spend: 99, Budget: 100}); msg != "" {
		t.Errorf("nothing left in the chain, so no notice: %q", msg)
	}
	if cfg.LLMProvider != provider.OpenAI || cfg.Model != "gpt-4o" {
		t.Errorf("selection = %q/%q, want it untouched", cfg.LLMProvider, cfg.Model)
	}
}

func TestMaybeQuotaFallbackRestoresAfterReset(t *testing.T) {
	const primaryModel = "claude-sonnet-4"
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = primaryModel
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	reset := time.Now().Add(50 * time.Millisecond)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the initial switch")
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Fatalf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
	}

	// Before the reset: stay on the fallback.
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.OpenAI}); msg != "" {
		t.Errorf("must not restore before the reset time: %q", msg)
	}

	time.Sleep(80 * time.Millisecond)
	msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.OpenAI})
	if msg == "" {
		t.Fatal("expected a restore after the reset time passed")
	}
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("provider = %q, want to be restored to %q", cfg.LLMProvider, provider.Anthropic)
	}
	if cfg.Model != primaryModel {
		t.Errorf("model = %q, want the original %q restored", cfg.Model, primaryModel)
	}
	// State is cleared, so a later reading doesn't restore again.
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic}); msg != "" {
		t.Errorf("restore should happen once: %q", msg)
	}
}

// With no reset instant from the provider AND no configured rollover we must not
// guess: bouncing back onto an exhausted quota would fail every turn.
func TestMaybeQuotaFallbackNoRestoreWithoutResetTime(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "m"
	cfg.QuotaBudgetUSD = 40
	cfg.QuotaResetAt = "" // no daily rollover configured either
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40}); msg == "" {
		t.Fatal("expected the initial switch")
	}
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.OpenAI}); msg != "" {
		t.Errorf("no reset time means no auto-restore: %q", msg)
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Errorf("provider = %q, want to stay on the fallback", cfg.LLMProvider)
	}
}

// Without a provider-reported reset, the configured daily rollover supplies one —
// otherwise a hard 429 (which carries no timestamp) would strand the session.
func TestMaybeQuotaFallbackUsesConfiguredDailyReset(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.QuotaResetAt = "00:00"
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, HardLimit: true}); msg == "" {
		t.Fatal("expected the switch")
	}
	if got := a.QuotaFallbackInfo(); got.ResetAt.IsZero() {
		t.Error("a configured daily rollover must supply the reset instant")
	}
}

// A multi-hop chain must return all the way to the ORIGINAL provider, not to the
// previous hop.
func TestMaybeQuotaFallbackRestoresToOriginalAfterMultipleHops(t *testing.T) {
	const primaryModel = "claude-sonnet-4"
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = primaryModel
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI, provider.Ollama}
	a := agentForQuota(t, cfg)

	reset := time.Now().Add(50 * time.Millisecond)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the first switch")
	}
	if cfg.LLMProvider != provider.OpenAI {
		t.Fatalf("after hop 1: provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
	}
	// Second hop, still before the reset.
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.OpenAI, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the second switch")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Fatalf("after hop 2: provider = %q, want %q", cfg.LLMProvider, provider.Ollama)
	}

	time.Sleep(80 * time.Millisecond)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Ollama}); msg == "" {
		t.Fatal("expected a restore after the reset")
	}
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("provider = %q, want the ORIGINAL %q (not the previous hop)", cfg.LLMProvider, provider.Anthropic)
	}
	if cfg.Model != primaryModel {
		t.Errorf("model = %q, want the original %q", cfg.Model, primaryModel)
	}
}

// The restore must only revert its OWN change: a deliberate manual pick after the
// hop wins and is left alone.
func TestQuotaRestoreLeavesManualPickAlone(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	reset := time.Now().Add(20 * time.Millisecond)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the switch")
	}
	// The user now picks a third backend by hand.
	cfg.LLMProvider = provider.Groq
	cfg.SetModel("llama-3.3-70b-versatile")

	time.Sleep(50 * time.Millisecond)
	if msg := a.MaybeQuotaRestore(); msg != "" {
		t.Errorf("must not revert a manual pick: %q", msg)
	}
	if cfg.LLMProvider != provider.Groq {
		t.Errorf("provider = %q, want the manual choice %q kept", cfg.LLMProvider, provider.Groq)
	}
}

// MaybeQuotaRestore must work WITHOUT a quota reading: the chain lands on backends
// with no usage endpoint (Ollama, LM Studio, llama.cpp), where Usage() errors, so a
// restore that needed a reading would strand the session forever.
func TestMaybeQuotaRestoreNeedsNoReading(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.Ollama}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{
		Provider: provider.Anthropic, Spend: 40, Budget: 100,
		ResetAt: time.Now().Add(20 * time.Millisecond),
	}); msg == "" {
		t.Fatal("expected the switch")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Fatalf("provider = %q, want %q", cfg.LLMProvider, provider.Ollama)
	}
	time.Sleep(50 * time.Millisecond)
	if msg := a.MaybeQuotaRestore(); msg == "" {
		t.Fatal("expected a restore driven purely by the clock")
	}
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.Anthropic)
	}
}

// An unusable hop is skipped rather than switched to — hopping to a backend with no
// key would break every subsequent turn.
func TestMaybeQuotaFallbackSkipsUnusableTarget(t *testing.T) {
	cfg := &config.Config{
		QuotaFallback:     true,
		LLMProvider:       provider.Anthropic,
		AnthropicAPIKey:   "sk-anthropic",
		OllamaBaseURL:     "http://localhost:11434/v1",
		QuotaBudgetUSD:    40,
		ProviderFallbacks: []string{provider.Anthropic, provider.OpenAI, provider.Ollama},
		// Note: no OpenAI key, so that hop must be skipped.
	}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100}); msg == "" {
		t.Fatal("expected a switch past the unusable hop")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Errorf("provider = %q, want to skip keyless openai and land on %q", cfg.LLMProvider, provider.Ollama)
	}
}

// A hop may pin its own model and reasoning level.
func TestMaybeQuotaFallbackHonorsPinnedModelAndReasoning(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, "ollama:qwen3-coder:30b:high"}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100}); msg == "" {
		t.Fatal("expected the switch")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Fatalf("provider = %q, want %q", cfg.LLMProvider, provider.Ollama)
	}
	// The colon-bearing model id must survive intact; only the trailing reasoning
	// level is peeled off.
	if cfg.Model != "qwen3-coder:30b" {
		t.Errorf("model = %q, want the pinned %q", cfg.Model, "qwen3-coder:30b")
	}
	if cfg.ReasoningLevel != config.ReasoningHigh {
		t.Errorf("reasoning = %q, want the pinned %q", cfg.ReasoningLevel, config.ReasoningHigh)
	}
}

// A hop's pinned reasoning must not stick to the primary's model after returning
// home.
func TestQuotaFallbackRestoresPrimaryReasoning(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.ReasoningLevel = config.ReasoningLow
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, "ollama:qwen3-coder:high"}
	a := agentForQuota(t, cfg)

	reset := time.Now().Add(20 * time.Millisecond)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the switch")
	}
	if cfg.ReasoningLevel != config.ReasoningHigh {
		t.Fatalf("reasoning = %q, want the hop's %q", cfg.ReasoningLevel, config.ReasoningHigh)
	}
	time.Sleep(50 * time.Millisecond)
	if msg := a.MaybeQuotaRestore(); msg == "" {
		t.Fatal("expected the restore")
	}
	if cfg.ReasoningLevel != config.ReasoningLow {
		t.Errorf("reasoning = %q, want the primary's %q back", cfg.ReasoningLevel, config.ReasoningLow)
	}
}

// The reactive path: a hard-quota error advances the chain, anything else doesn't.
func TestQuotaFallbackOnError(t *testing.T) {
	newAgent := func(enabled bool) (*Agent, *config.Config) {
		cfg := usableCfg(t)
		cfg.QuotaFallback = enabled
		cfg.LLMProvider = provider.Anthropic
		cfg.Model = "claude-sonnet-4"
		cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
		return agentForQuota(t, cfg), cfg
	}

	t.Run("quota error switches", func(t *testing.T) {
		a, cfg := newAgent(true)
		err := fmt.Errorf("you exceeded your current quota: %w", llm.ErrQuotaExhausted)
		if note := a.quotaFallbackOnError(err); note == "" {
			t.Fatal("expected a switch on ErrQuotaExhausted")
		}
		if cfg.LLMProvider != provider.OpenAI {
			t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
		}
	})
	t.Run("other errors are ignored", func(t *testing.T) {
		a, cfg := newAgent(true)
		if note := a.quotaFallbackOnError(errors.New("connection reset")); note != "" {
			t.Errorf("unrelated error must not switch: %q", note)
		}
		if cfg.LLMProvider != provider.Anthropic {
			t.Errorf("provider = %q, want it untouched", cfg.LLMProvider)
		}
	})
	t.Run("nil error is ignored", func(t *testing.T) {
		a, _ := newAgent(true)
		if note := a.quotaFallbackOnError(nil); note != "" {
			t.Errorf("nil error must be a no-op: %q", note)
		}
	})
	t.Run("disabled policy ignores quota errors", func(t *testing.T) {
		a, cfg := newAgent(false)
		err := fmt.Errorf("wrapped: %w", llm.ErrQuotaExhausted)
		if note := a.quotaFallbackOnError(err); note != "" {
			t.Errorf("disabled policy must not switch: %q", note)
		}
		if cfg.LLMProvider != provider.Anthropic {
			t.Errorf("provider = %q, want it untouched", cfg.LLMProvider)
		}
	})
}

// A hop must survive a restart: the persisted record is what lets the restore fire
// in a fresh process, where the config alone looks like a deliberate user choice.
func TestQuotaFallbackSnapshotRoundTrip(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)

	if snap := a.QuotaFallbackSnapshot(); snap != nil {
		t.Fatalf("no fallback yet, want nil snapshot, got %+v", snap)
	}
	reset := time.Now().Add(time.Hour)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: reset}); msg == "" {
		t.Fatal("expected the switch")
	}
	snap := a.QuotaFallbackSnapshot()
	if snap == nil {
		t.Fatal("expected a snapshot while switched")
	}
	if snap.PrimaryProvider != provider.Anthropic || snap.PrimaryModel != "claude-sonnet-4" {
		t.Errorf("snapshot primary = %q/%q, want %q/%q",
			snap.PrimaryProvider, snap.PrimaryModel, provider.Anthropic, "claude-sonnet-4")
	}
	if snap.FallbackProvider != provider.OpenAI {
		t.Errorf("snapshot fallback = %q, want %q", snap.FallbackProvider, provider.OpenAI)
	}

	// Reload into a fresh agent, as a restart would.
	cfg2 := usableCfg(t)
	cfg2.QuotaFallback = true
	cfg2.LLMProvider = snap.FallbackProvider
	cfg2.Model = snap.FallbackModel
	cfg2.ProviderFallbacks = cfg.ProviderFallbacks
	b := agentForQuota(t, cfg2)
	b.RestoreQuotaFallback(snap)
	if got := b.QuotaFallbackInfo(); !got.Switched {
		t.Fatal("restored agent must know it is mid-fallback")
	}
	// Roll the clock past the reset and confirm the restore still fires.
	b.ForceQuotaResetAtForTest(time.Now().Add(-time.Second))
	if msg := b.MaybeQuotaRestore(); msg == "" {
		t.Fatal("expected the restore to fire in the reloaded agent")
	}
	if cfg2.LLMProvider != provider.Anthropic {
		t.Errorf("provider = %q, want %q restored after reload", cfg2.LLMProvider, provider.Anthropic)
	}
}

// resetQuotaFallback must clear the state, or a new session would inherit a
// pending restore from the previous one.
func TestResetQuotaFallbackClearsState(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100, ResetAt: time.Now().Add(time.Hour)}); msg == "" {
		t.Fatal("expected the switch")
	}
	a.resetQuotaFallback()
	if got := a.QuotaFallbackInfo(); got.Switched {
		t.Error("state must be cleared after a reset")
	}
	if snap := a.QuotaFallbackSnapshot(); snap != nil {
		t.Errorf("snapshot must be nil after a reset, got %+v", snap)
	}
}

// A nil persisted record must clear rather than half-populate the state.
func TestRestoreQuotaFallbackNil(t *testing.T) {
	a := agentForQuota(t, usableCfg(t))
	a.RestoreQuotaFallback(nil)
	if got := a.QuotaFallbackInfo(); got.Switched {
		t.Error("a nil record must leave the agent not-switched")
	}
	a.RestoreQuotaFallback(&session.QuotaFallback{
		PrimaryProvider: provider.Anthropic, FallbackProvider: provider.OpenAI,
	})
	if got := a.QuotaFallbackInfo(); !got.Switched {
		t.Error("a populated record must mark the agent switched")
	}
}

func TestQuotaFallbackInfo(t *testing.T) {
	t.Run("empty chain makes the toggle inert", func(t *testing.T) {
		cfg := usableCfg(t)
		cfg.QuotaFallback = true // on, but nowhere to go
		a := agentForQuota(t, cfg)
		if got := a.QuotaFallbackInfo(); got.Enabled {
			t.Error("Enabled must be false with an empty chain")
		}
	})
	t.Run("reports the parsed chain and budgets", func(t *testing.T) {
		cfg := usableCfg(t)
		cfg.QuotaFallback = true
		cfg.QuotaBudgetUSD = 25
		cfg.QuotaBudgetPct = 80
		cfg.ProviderFallbacks = []string{provider.Anthropic, "ollama:qwen3-coder:high"}
		got := agentForQuota(t, cfg).QuotaFallbackInfo()
		if !got.Enabled {
			t.Error("Enabled must be true when on with a non-empty chain")
		}
		if len(got.Chain) != 2 {
			t.Fatalf("chain = %+v, want 2 hops", got.Chain)
		}
		if got.Chain[1].Model != "qwen3-coder" || got.Chain[1].Reasoning != config.ReasoningHigh {
			t.Errorf("hop 2 = %+v, want model qwen3-coder / reasoning high", got.Chain[1])
		}
		if got.BudgetUSD != 25 || got.BudgetPct != 80 {
			t.Errorf("budgets = %v/%v, want 25/80", got.BudgetUSD, got.BudgetPct)
		}
	})
}

func TestNextDailyReset(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.Local)
	t.Run("later today", func(t *testing.T) {
		got := NextDailyReset("18:30", now)
		want := time.Date(2026, 3, 4, 18, 30, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("already passed rolls to tomorrow", func(t *testing.T) {
		got := NextDailyReset("06:00", now)
		want := time.Date(2026, 3, 5, 6, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("empty disables the timed restore", func(t *testing.T) {
		if got := NextDailyReset("", now); !got.IsZero() {
			t.Errorf("got %v, want the zero time", got)
		}
	})
	t.Run("malformed disables the timed restore", func(t *testing.T) {
		if got := NextDailyReset("half past nine", now); !got.IsZero() {
			t.Errorf("got %v, want the zero time", got)
		}
	})
}

func TestParseQuotaResetTime(t *testing.T) {
	t.Run("rfc3339", func(t *testing.T) {
		got := ParseQuotaResetTime("2026-03-04T07:15:00Z")
		if got.IsZero() || got.UTC().Hour() != 7 {
			t.Errorf("got %v, want 07:15Z", got)
		}
	})
	t.Run("epoch seconds", func(t *testing.T) {
		want := time.Now().Add(time.Hour).Truncate(time.Second)
		if got := ParseQuotaResetTime(fmt.Sprintf("%d", want.Unix())); !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("epoch milliseconds", func(t *testing.T) {
		want := time.Now().Add(time.Hour).Truncate(time.Millisecond)
		if got := ParseQuotaResetTime(fmt.Sprintf("%d", want.UnixMilli())); !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("empty and garbage yield zero", func(t *testing.T) {
		for _, s := range []string{"", "   ", "not a time"} {
			if got := ParseQuotaResetTime(s); !got.IsZero() {
				t.Errorf("ParseQuotaResetTime(%q) = %v, want zero", s, got)
			}
		}
	})
}

func TestShortModelLabel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "default model"},
		{"gpt-4o", "gpt-4o"},
		{"openai/gpt-4o", "gpt-4o"},
		{"aws:anthropic.claude-sonnet-4:1", "claude-sonnet-4"},
		{"gcp:gemini-2.5-pro", "gemini-2.5-pro"},
		{"qwen3-coder:30b", "qwen3-coder"},
	} {
		if got := shortModelLabel(tc.in); got != tc.want {
			t.Errorf("shortModelLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The event carries the NEW selection, so a front end can update its chip without
// parsing the prose notice.
func TestBackendSwitchedEvent(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
	a := agentForQuota(t, cfg)
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 40, Budget: 100}); msg == "" {
		t.Fatal("expected the switch")
	}
	ev := a.backendSwitchedEvent()
	if ev.Provider != provider.OpenAI {
		t.Errorf("event provider = %q, want %q", ev.Provider, provider.OpenAI)
	}
	if ev.Model != cfg.Model {
		t.Errorf("event model = %q, want %q", ev.Model, cfg.Model)
	}
}

// The recovery STATE is shared between the background usage poll and the turn
// goroutine, so its accessors must be safe to call concurrently. cfg itself is not
// shared-writable — MaybeQuotaFallback/MaybeQuotaRestore refuse while a turn is
// running (see the mid-turn guard) — so this exercises the concurrent readers plus
// the note queue, which really is written from another goroutine.
func TestQuotaFallbackConcurrentAccess(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.Model = "claude-sonnet-4"
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI, provider.Ollama}
	a := agentForQuota(t, cfg)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				switch (n + j) % 5 {
				case 0:
					// Simulate the poll firing while a turn holds cfg: the guard must make
					// this a no-op rather than a racing write.
					a.inTurn.Add(1)
					a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 99, Budget: 100})
					a.inTurn.Add(-1)
				case 1:
					a.inTurn.Add(1)
					a.MaybeQuotaRestore()
					a.inTurn.Add(-1)
				case 2:
					_ = a.QuotaFallbackInfo()
				case 3:
					_ = a.QuotaFallbackSnapshot()
				case 4:
					a.NoteInfo("note")
				}
			}
		}(i)
	}
	wg.Wait()
	// Drain the queue the way the turn goroutine does; the point is that none of the
	// above raced or panicked.
	a.flushPendingNotes()
}

// NoteInfo must not lose notes, and flushing must be idempotent once drained.
func TestNoteInfoQueueDrains(t *testing.T) {
	a := agentForQuota(t, usableCfg(t))
	if a.flushPendingNotes() {
		t.Error("nothing queued, so flush must report no work")
	}
	a.NoteInfo("  ") // whitespace-only is dropped
	a.NoteInfo("")   // empty is dropped
	if a.flushPendingNotes() {
		t.Error("blank notes must not count as queued work")
	}
}

// The background poll must not rewrite the provider under a live turn: cfg is read on
// every request by the turn goroutine, so a switch there is both a data race and a
// turn served half by one backend and half by another. The REACTIVE path is exempt,
// because it runs on the turn goroutine itself.
func TestQuotaFallbackRefusesMidTurn(t *testing.T) {
	newAgent := func() (*Agent, *config.Config) {
		cfg := usableCfg(t)
		cfg.QuotaFallback = true
		cfg.LLMProvider = provider.Anthropic
		cfg.Model = "claude-sonnet-4"
		cfg.QuotaBudgetUSD = 40
		cfg.ProviderFallbacks = []string{provider.Anthropic, provider.OpenAI}
		return agentForQuota(t, cfg), cfg
	}

	t.Run("poll-driven fallback is skipped mid-turn", func(t *testing.T) {
		a, cfg := newAgent()
		a.inTurn.Add(1)
		defer a.inTurn.Add(-1)
		if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 99, Budget: 100}); msg != "" {
			t.Errorf("expected a no-op mid-turn, got %q", msg)
		}
		if cfg.LLMProvider != provider.Anthropic {
			t.Errorf("provider = %q, want it untouched mid-turn", cfg.LLMProvider)
		}
	})

	t.Run("it applies once the turn ends", func(t *testing.T) {
		a, cfg := newAgent()
		if msg := a.MaybeQuotaFallback(QuotaSnapshot{Provider: provider.Anthropic, Spend: 99, Budget: 100}); msg == "" {
			t.Fatal("expected the switch off-turn")
		}
		if cfg.LLMProvider != provider.OpenAI {
			t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
		}
	})

	t.Run("poll-driven restore is skipped mid-turn", func(t *testing.T) {
		a, cfg := newAgent()
		if msg := a.MaybeQuotaFallback(QuotaSnapshot{
			Provider: provider.Anthropic, Spend: 99, Budget: 100,
			ResetAt: time.Now().Add(10 * time.Millisecond),
		}); msg == "" {
			t.Fatal("expected the switch")
		}
		time.Sleep(30 * time.Millisecond)
		a.inTurn.Add(1)
		if msg := a.MaybeQuotaRestore(); msg != "" {
			t.Errorf("expected a no-op mid-turn, got %q", msg)
		}
		a.inTurn.Add(-1)
		if cfg.LLMProvider != provider.OpenAI {
			t.Errorf("provider = %q, want the fallback kept until the turn ends", cfg.LLMProvider)
		}
		// And it lands as soon as the turn is over.
		if msg := a.MaybeQuotaRestore(); msg == "" {
			t.Error("expected the restore once the turn ended")
		}
	})

	t.Run("the reactive path still switches mid-turn", func(t *testing.T) {
		a, cfg := newAgent()
		a.inTurn.Add(1) // the reactive path runs ON the turn goroutine
		defer a.inTurn.Add(-1)
		err := fmt.Errorf("quota: %w", llm.ErrQuotaExhausted)
		if note := a.quotaFallbackOnError(err); note == "" {
			t.Fatal("the reactive path must not be blocked by the mid-turn guard")
		}
		if cfg.LLMProvider != provider.OpenAI {
			t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.OpenAI)
		}
	})
}
