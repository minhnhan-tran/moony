package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/provider"
)

func TestReasoningBudget(t *testing.T) {
	if ReasoningBudget(ReasoningOff) != 0 {
		t.Error("off should have no budget")
	}
	for _, lvl := range []string{ReasoningLow, ReasoningMedium, ReasoningHigh} {
		if b := ReasoningBudget(lvl); b < 1024 {
			t.Errorf("%s budget = %d, must be >= 1024", lvl, b)
		}
	}
	// Budgets should increase with level.
	if !(ReasoningBudget(ReasoningLow) < ReasoningBudget(ReasoningMedium) &&
		ReasoningBudget(ReasoningMedium) < ReasoningBudget(ReasoningHigh)) {
		t.Error("budgets should increase low < medium < high")
	}
}

func TestNormalizeReasoning(t *testing.T) {
	if NormalizeReasoning("bogus") != ReasoningOff {
		t.Error("unknown level should normalize to off")
	}
	if NormalizeReasoning("high") != ReasoningHigh {
		t.Error("valid level should pass through")
	}
}

func TestReasoningLevelsFor(t *testing.T) {
	has := func(levels []string, want string) bool {
		for _, l := range levels {
			if l == want {
				return true
			}
		}
		return false
	}
	opus := ReasoningLevelsFor("anthropic.claude-opus-4-8")
	if !has(opus, ReasoningXHigh) || !has(opus, ReasoningMax) || has(opus, ReasoningMinimal) || !has(opus, ReasoningAuto) {
		t.Errorf("opus levels = %v, want auto+x-high+max, no minimal", opus)
	}
	gem := ReasoningLevelsFor("gcp:gemini-2.5-pro")
	if !has(gem, ReasoningMinimal) || has(gem, ReasoningMax) || !has(gem, ReasoningAuto) {
		t.Errorf("gemini levels = %v, want auto+minimal, no max", gem)
	}
	if other := ReasoningLevelsFor("anthropic.claude-haiku-4-5"); len(other) != 5 { // off/auto/low/medium/high
		t.Errorf("default levels = %v, want 5 (off/auto/low/medium/high)", other)
	}
}

func TestNormalizeReasoningAliases(t *testing.T) {
	cases := map[string]string{
		"X-High": ReasoningXHigh, "xhigh": ReasoningXHigh, "maximum": ReasoningMax,
		"min": ReasoningMinimal, "MED": ReasoningMedium, "bogus": ReasoningOff,
		"ultrathink": ReasoningMax, "ULTRA": ReasoningMax, // Claude-Code-style alias for max
	}
	for in, want := range cases {
		if got := NormalizeReasoning(in); got != want {
			t.Errorf("NormalizeReasoning(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWireEffort locks in the values that may reach the gateway's output_config.effort
// field, which accepts ONLY low|medium|high|xhigh|max. "off" and "auto" (whose
// baseline is off) must resolve to "" (nothing sent) rather than the invalid
// effort:"off" that 400'd; x-high must go over the wire as "xhigh", not "x-high".
func TestWireEffort(t *testing.T) {
	const (
		opus   = "anthropic.claude-opus-4-8"
		sonnet = "anthropic.claude-sonnet-4-6"
		gemini = "gcp:gemini-3.1-pro-preview"
		gpt    = "openai.gpt-5"
	)
	cases := []struct {
		provider, model, level, want string
	}{
		// The reported bug: auto (baseline off) and off must send NOTHING, not "off".
		{ProviderAnthropic, sonnet, ReasoningAuto, ""},
		{ProviderAnthropic, sonnet, ReasoningOff, ""},
		{ProviderOpenAI, gemini, ReasoningAuto, ""},
		{ProviderOpenAI, gemini, ReasoningOff, ""},
		// Concrete levels pass through as the exact wire spelling.
		{ProviderAnthropic, sonnet, ReasoningHigh, "high"},
		{ProviderAnthropic, opus, ReasoningMax, "max"},
		// x-high must serialize as "xhigh" (the variant the gateway accepts), from
		// either spelling; on a model without an x-high tier it clamps to "high".
		{ProviderAnthropic, opus, ReasoningXHigh, "xhigh"},
		{ProviderAnthropic, opus, "x-high", "xhigh"},
		{ProviderAnthropic, sonnet, ReasoningXHigh, "high"},
		// "minimal" isn't an Anthropic tier; it clamps up to the model's floor (low),
		// never leaking the invalid "minimal" variant onto the Anthropic route.
		{ProviderAnthropic, opus, ReasoningMinimal, "low"},
		// Models that don't support thinking get nothing regardless of level.
		{ProviderOpenAI, gpt, ReasoningHigh, ""},
	}
	allowed := map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for _, c := range cases {
		got := WireEffort(c.provider, c.model, c.level)
		if got != c.want {
			t.Errorf("WireEffort(%s, %s, %q) = %q, want %q", c.provider, c.model, c.level, got, c.want)
		}
		if !allowed[got] {
			t.Errorf("WireEffort(%s, %s, %q) = %q — not a gateway-accepted effort variant", c.provider, c.model, c.level, got)
		}
	}
}

// TestCostUSDFullFallback: a known model prices identically to CostUSDFull, but an
// unknown model — which CostUSDFull prices at $0 — is priced at the conservative
// fallback rate, so the relay quota never lets an unrecognized model run uncapped.
func TestCostUSDFullFallback(t *testing.T) {
	ClearRuntimePricing()
	const known = "anthropic.claude-sonnet-4-6"
	if a, b := CostUSDFullFallback(known, 1_000_000, 1_000_000, 0, 0), CostUSDFull(known, 1_000_000, 1_000_000, 0, 0); a != b || a <= 0 {
		t.Fatalf("known-model fallback = %v, want it to match CostUSDFull (%v) and be > 0", a, b)
	}
	const unknown = "acme.mystery-model-v9"
	if CostUSDFull(unknown, 1_000_000, 1_000_000, 0, 0) != 0 {
		t.Fatal("precondition: CostUSDFull prices an unknown model at $0")
	}
	if got := CostUSDFullFallback(unknown, 1_000_000, 1_000_000, 0, 0); got <= 0 {
		t.Fatalf("unknown-model fallback = %v, want > 0 (never uncapped)", got)
	}
}

// TestReasoningKeyword covers the in-prompt thinking escalators ("ultrathink",
// the "think hard" family) and that casual phrasing — and the now @-only
// "ultrathink" — don't trip them.
func TestReasoningKeyword(t *testing.T) {
	cases := map[string]string{
		"think harder about the edge case": ReasoningHigh,
		"think hard here":                  ReasoningHigh,
		"use megathink":                    ReasoningHigh,
		"I think this is fine":             "", // bare "think" must NOT escalate
		"what do you think?":               "",
		"@ultrathink this":                 "", // ultrathink is handled by its own @-trigger now
		"ultrathink this":                  "",
		"":                                 "",
	}
	for in, want := range cases {
		if got := ReasoningKeyword(in); got != want {
			t.Errorf("ReasoningKeyword(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPromptReasoningHint covers the predictive "auto" heuristic: high on clear
// reasoning/debugging/design cues, off ONLY for confidently trivial/mechanical
// turns, and medium (a generous ceiling) for any other real request.
func TestPromptReasoningHint(t *testing.T) {
	cases := map[string]string{
		"":                                 ReasoningOff,
		"continue":                         ReasoningOff,
		"ok":                               ReasoningOff,
		"rename the helper to parseConfig": ReasoningOff,
		"format the file":                  ReasoningOff,
		// A real, non-trivial request defaults to a MEDIUM ceiling — headroom for the
		// first step to think deeply when the turn is hard (adaptive self-regulates
		// easy ones down; the passive dampener + ramps handle the rest).
		"add a /status endpoint":              ReasoningMedium,
		"make the tests pass":                 ReasoningMedium,
		"why does the build fail?":            ReasoningHigh,
		"debug the parser":                    ReasoningHigh,
		"the login flow is broken":            ReasoningHigh,
		"design a caching layer":              ReasoningHigh,
		"investigate the flaky test":          ReasoningHigh,
		"how should I structure this module?": ReasoningMedium,
		"what does this function return?":     ReasoningMedium,
	}
	for in, want := range cases {
		if got := PromptReasoningHint(in); got != want {
			t.Errorf("PromptReasoningHint(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLooksLikeInvestigation covers the auto-delegate classifier: read-heavy
// "understand how/where/why" requests match; build/design/edit/trivial don't.
func TestLooksLikeInvestigation(t *testing.T) {
	investigation := []string{
		"check why i changed model in /models but it doesnt have effect",
		"how does the auth flow work?",
		"where is the model resolved per request",
		"why doesn't /models take effect",
		"investigate the flaky test",
		"debug the parser",
		"explain how compaction works",
		"what calls renderHeader",
		"trace the request through the relay",
		// Regression: a fronted clause separates "why" from the verb, so the old
		// adjacency-only "why <verb>" pattern missed this real bug report.
		"why on web ui with a live terminal session, i cannot see the mcp server is not displayed and shown No MCP servers",
		"why cannot i see the mcp server",
		"why on the web ui can't I see the tools menu",
		// Symptom reports that never say "why".
		"the panel shows no mcp servers",
		"the laptop tree isn't displaying",
		"the dropdown doesn't show anything",
		"i can't see the usage panel",
	}
	for _, in := range investigation {
		if !LooksLikeInvestigation(in) {
			t.Errorf("LooksLikeInvestigation(%q) = false, want true", in)
		}
	}
	other := []string{
		"add a /status endpoint",
		"rename the helper to parseConfig",
		"design a caching layer",
		"format the file",
		"continue",
		"",
	}
	for _, in := range other {
		if LooksLikeInvestigation(in) {
			t.Errorf("LooksLikeInvestigation(%q) = true, want false (not a read-heavy investigation)", in)
		}
	}
}

// TestClassifyInvestigation covers the scope classifier that right-sizes
// auto-delegation: NARROW (one named target, read inline), BROAD (wide / multi-
// facet, fan out), and NORMAL (a single explore delegate) otherwise.
func TestClassifyInvestigation(t *testing.T) {
	cases := []struct {
		in   string
		want InvestigationScope
	}{
		{"add a /status endpoint", InvestigationNone},
		{"rename the helper", InvestigationNone},
		// Narrow: one concrete target, short, single-clause.
		{"explain how `parseGroupRoster` works", InvestigationNarrow},
		{"walk me through internal/relay/quota.go", InvestigationNarrow},
		// Normal: an investigation with no single-target or broad cue.
		{"how does the quota gate work?", InvestigationNormal},
		{"why can't I see the plan bar?", InvestigationNormal},
		// Broad: explicit wide-scope cue.
		{"how does auth work across the whole codebase?", InvestigationBroad},
		{"walk me through the entire request lifecycle", InvestigationBroad},
		// Broad: multiple questions in one message.
		{"why is X failing? and where is Y set?", InvestigationBroad},
		// A named target but a broad cue → broad wins.
		{"explain how `auth` flows across the entire system", InvestigationBroad},
		// A single backticked symbol, but multi-facet ("and …") → not narrow.
		{"explain how `parseGroupRoster` builds the roster and where the usernames come from", InvestigationNormal},
	}
	for _, c := range cases {
		if got := ClassifyInvestigation(c.in); got != c.want {
			t.Errorf("ClassifyInvestigation(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestFastModel / TestAuxModel: with Ollama as the only provider (which does
// not serve a hosted model catalog), the cheap-model resolvers keep the session
// model unless the user pins MOONY_FAST_MODEL / MOONY_AUX_MODEL.
func TestFastModel(t *testing.T) {
	ClearRuntimePricing()
	const oll = provider.Ollama
	if got := FastModel("gemma4:31b", oll); got != "gemma4:31b" {
		t.Errorf("FastModel on ollama = %q, want the session model gemma4:31b", got)
	}
	t.Setenv("MOONY_FAST_MODEL", "gemma4:8b")
	if got := FastModel("gemma4:31b", oll); got != "gemma4:8b" {
		t.Errorf("FastModel with MOONY_FAST_MODEL = %q, want gemma4:8b", got)
	}
}

func TestAuxModel(t *testing.T) {
	ClearRuntimePricing()
	const oll = provider.Ollama
	t.Setenv("MOONY_AUX_MODEL", "")
	t.Setenv("MOONY_FAST_MODEL", "")
	if got := AuxModel("gemma4:31b", oll); got != "gemma4:31b" {
		t.Errorf("AuxModel on ollama = %q, want gemma4:31b", got)
	}
	// MOONY_AUX_MODEL pins the aux model explicitly.
	t.Setenv("MOONY_AUX_MODEL", "gemma4:8b")
	if got := AuxModel("gemma4:31b", oll); got != "gemma4:8b" {
		t.Errorf("AuxModel with MOONY_AUX_MODEL = %q, want gemma4:8b", got)
	}
}

// TestAuxModelFallsBackToFastEnv verifies a lone MOONY_FAST_MODEL pin also steers
// aux calls, so one env var covers both the fast and aux tiers.
func TestAuxModelFallsBackToFastEnv(t *testing.T) {
	ClearRuntimePricing()
	t.Setenv("MOONY_AUX_MODEL", "")
	t.Setenv("MOONY_FAST_MODEL", "gemma4:8b")
	if got := AuxModel("gemma4:31b", provider.Ollama); got != "gemma4:8b" {
		t.Errorf("AuxModel with only MOONY_FAST_MODEL = %q, want gemma4:8b", got)
	}
}

// TestHigherReasoning verifies the escalation helper never lowers the level.
func TestHigherReasoning(t *testing.T) {
	if got := HigherReasoning(ReasoningHigh, ReasoningMedium); got != ReasoningHigh {
		t.Errorf("HigherReasoning(high, medium) = %q, want high", got)
	}
	if got := HigherReasoning(ReasoningOff, ReasoningMax); got != ReasoningMax {
		t.Errorf("HigherReasoning(off, max) = %q, want max", got)
	}
	if ReasoningRank(ReasoningMax) <= ReasoningRank(ReasoningHigh) {
		t.Error("max should outrank high")
	}
}

// TestAutoReasoningLevel covers the "auto" meta-level: it appears in the picker,
// survives normalization (not silently turned into off/low), and resolves to a
// concrete API effort via EffortFor (its off baseline), never "auto".
func TestAutoReasoningLevel(t *testing.T) {
	if NormalizeReasoning("auto") != ReasoningAuto || NormalizeReasoning("Automatic") != ReasoningAuto {
		t.Fatal("auto/automatic should normalize to ReasoningAuto")
	}
	if !IsAuto("auto") || IsAuto("high") {
		t.Error("IsAuto should be true only for auto")
	}
	for _, model := range []string{"anthropic.claude-opus-4-8", "gcp:gemini-2.5-pro", "anthropic.claude-haiku-4-5"} {
		found := false
		for _, l := range ReasoningLevelsFor(model) {
			if l == ReasoningAuto {
				found = true
			}
		}
		if !found {
			t.Errorf("%s levels should include auto", model)
		}
	}
	if NormalizeReasoningFor("anthropic.claude-opus-4-8", "auto") != ReasoningAuto {
		t.Error("NormalizeReasoningFor should preserve auto")
	}
	// Resolves to a concrete effort for the wire (its off baseline), never "auto".
	if got := EffortFor("anthropic.claude-opus-4-8", "auto"); got != ReasoningOff {
		t.Errorf("EffortFor(opus, auto) = %q, want off baseline", got)
	}
	if got := EffortFor("gcp:gemini-2.5-pro", "auto"); got != ReasoningOff {
		t.Errorf("EffortFor(gemini, auto) = %q, want off baseline", got)
	}
	if got := EffortFor("gcp:gemini-2.5-pro", ReasoningMax); got != ReasoningHigh {
		t.Errorf("EffortFor(gemini, max) = %q, want high (clamped)", got)
	}
}

// TestIsUltrathink covers the @-mention deep-dive trigger: only "@ultrathink"
// fires it — not the bare word, not the "think hard" family, not casual phrasing.
func TestIsUltrathink(t *testing.T) {
	for _, in := range []string{"@ultrathink this", "@ULTRATHINK", "go @ultrathink on the bug", "@ultrathink"} {
		if !IsUltrathink(in) {
			t.Errorf("IsUltrathink(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"ultrathink this", "ULTRATHINK", "think hard", "what do you think", "foo@ultrathink.example", ""} {
		if IsUltrathink(in) {
			t.Errorf("IsUltrathink(%q) = true, want false (only the @ultrathink mention triggers it)", in)
		}
	}
}

// TestStripUltrathink verifies the trigger token is removed for a clean prompt.
func TestStripUltrathink(t *testing.T) {
	cases := map[string]string{
		"@ultrathink how does auth work?": "how does auth work?",
		"fix @ultrathink the parser":      "fix the parser",
		"@ultrathink":                     "",
		"no trigger here":                 "no trigger here",
	}
	for in, want := range cases {
		if got := StripUltrathink(in); got != want {
			t.Errorf("StripUltrathink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeReasoningForClamps(t *testing.T) {
	if got := NormalizeReasoningFor("gcp:gemini-2.5-pro", ReasoningMax); got != ReasoningHigh {
		t.Errorf("gemini max -> %q, want high (clamp down)", got)
	}
	if got := NormalizeReasoningFor("anthropic.claude-opus-4-8", ReasoningMinimal); got != ReasoningLow {
		t.Errorf("opus minimal -> %q, want low (clamp up)", got)
	}
	if got := NormalizeReasoningFor("anthropic.claude-opus-4-8", ReasoningXHigh); got != ReasoningXHigh {
		t.Errorf("opus x-high -> %q, want x-high (valid)", got)
	}
	if got := NormalizeReasoningFor("gcp:gemini-2.5-pro", ReasoningOff); got != ReasoningOff {
		t.Errorf("off -> %q, want off", got)
	}
}

func TestReasoningActive(t *testing.T) {
	// Ollama/Anthropic: fall back to the model catalog (unknown ids default to
	// supporting thinking), and off is never active.
	if !ReasoningActive(provider.Anthropic, "claude-sonnet-4-5", ReasoningHigh) {
		t.Error("anthropic + claude + high should be active")
	}
	if ReasoningActive(provider.Anthropic, "claude-sonnet-4-5", ReasoningOff) {
		t.Error("off is never active")
	}
	if !ReasoningActive(provider.Ollama, "llama3.1:8b", ReasoningMedium) {
		t.Error("ollama unknown id should default to active")
	}
	// OpenAI: only the reasoning-model family accepts reasoning_effort.
	if !ReasoningActive(provider.OpenAI, "o3-mini", ReasoningHigh) {
		t.Error("openai + o3-mini should be active")
	}
	if ReasoningActive(provider.OpenAI, "gpt-4o", ReasoningHigh) {
		t.Error("openai + gpt-4o should not be active")
	}
	// OpenRouter: always active (it normalizes/ignores downstream).
	if !ReasoningActive(provider.OpenRouter, "anything/model", ReasoningLow) {
		t.Error("openrouter should be active for any non-off level")
	}
}

func TestModelSupportsThinking(t *testing.T) {
	if !ModelSupportsThinking(DefaultModel) {
		t.Error("default model should support thinking")
	}
	if ModelSupportsThinking("anthropic.claude-haiku-4-5-20251001-v1:0") {
		t.Error("haiku is not expected to support extended thinking")
	}
	if !ModelSupportsThinking("some.unknown.model") {
		t.Error("unknown models should be assumed to support thinking")
	}
}

func TestSaveAndLoadPrefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Make sure no env overrides interfere with the round-trip.
	t.Setenv("MOONY_MODEL", "")
	t.Setenv("MOONY_REASONING", "")
	t.Setenv("MOONY_APPROVAL", "")
	t.Setenv("MOONY_RELAY", "")

	c := &Config{
		LLMProvider:    provider.Anthropic,
		Model:          "claude-custom-model",
		ReasoningLevel: ReasoningHigh,
		ReasoningByModel: map[string]string{
			"claude-custom-model": ReasoningHigh,
		},
		ApprovalPolicy: ApprovalSafe,
		RelayAddr:      "relay.example.com:9000",
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The file should exist under the sandboxed config dir.
	base, _ := GlobalConfigDir()
	if _, err := os.Stat(filepath.Join(base, "config.json")); err != nil {
		t.Fatalf("expected config.json to exist: %v", err)
	}

	got := Load()
	if got.LLMProvider != provider.Anthropic {
		t.Errorf("persisted provider not loaded: %q", got.LLMProvider)
	}
	if got.Provider != ProviderAnthropic {
		t.Errorf("wire route should derive from the backend: %q", got.Provider)
	}
	if got.Model != "claude-custom-model" {
		t.Errorf("persisted model not loaded: %q", got.Model)
	}
	if got.ReasoningLevel != ReasoningHigh {
		t.Errorf("persisted reasoning not loaded: %q", got.ReasoningLevel)
	}
	if got.ApprovalPolicy != ApprovalSafe {
		t.Errorf("persisted approval policy not loaded: %q", got.ApprovalPolicy)
	}
	if got.ReasoningByModel["claude-custom-model"] != ReasoningHigh {
		t.Errorf("per-model reasoning not loaded: %v", got.ReasoningByModel)
	}
	if got.RelayAddr != "relay.example.com:9000" {
		t.Errorf("persisted relay address not loaded: %q", got.RelayAddr)
	}
}

// TestPersonaAutoPlanSelfReviewSurviveEnvOverride is the regression test for
// the bug where AutoPlan/SelfReview were plain bools applied from prefs BEFORE
// their env vars were read (unlike CheapReads/FastModel/AuxModel), so a
// deployment's MOONY_AUTO_PLAN=0 / MOONY_SELF_REVIEW=0 silently clobbered a saved
// persona "on" choice on every restart. They're persona-pointer fields now,
// applied LAST, so the explicit persona save must win over the env var.
func TestPersonaAutoPlanSelfReviewSurviveEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	// Simulate saving "on" for both via the Persona modal.
	ap, sr := true, true
	c := &Config{AutoPlanPersona: &ap, SelfReviewPersona: &sr}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Simulate a deployment env that tries to force both off.
	t.Setenv("MOONY_AUTO_PLAN", "0")
	t.Setenv("MOONY_SELF_REVIEW", "0")

	got := Load()
	if !got.AutoPlan {
		t.Error("AutoPlan = false, want the persisted persona choice (true) to win over MOONY_AUTO_PLAN=0")
	}
	if !got.SelfReview {
		t.Error("SelfReview = false, want the persisted persona choice (true) to win over MOONY_SELF_REVIEW=0")
	}
}

// TestRelayEnvOverride confirms MOONY_RELAY overrides the saved relay address.
func TestRelayEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	(&Config{RelayAddr: "saved.example:1"}).Save()
	t.Setenv("MOONY_RELAY", "env.example:2")
	if got := Load(); got.RelayAddr != "env.example:2" {
		t.Errorf("RelayAddr = %q, want the MOONY_RELAY override env.example:2", got.RelayAddr)
	}
}

// TestRelayDefaultsOn: with no saved relay and no MOONY_RELAY, the relay defaults
// to defaultRelayAddr (on).
func TestRelayDefaultsOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MOONY_RELAY", "") // Load treats empty as unset
	if got := Load().RelayAddr; got != defaultRelayAddr {
		t.Errorf("RelayAddr = %q, want default %q", got, defaultRelayAddr)
	}
}

// TestRelayOffPersists: `/relay off` saves an explicit empty string. The pointer
// distinguishes that from "never configured", so a saved "" disables the relay on
// the next load instead of reverting to the default.
func TestRelayOffPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MOONY_RELAY", "")
	(&Config{RelayAddr: ""}).Save() // mirrors `/relay off`
	if got := Load().RelayAddr; got != "" {
		t.Errorf("RelayAddr = %q, want \"\" — explicit off must persist, not revert to the default", got)
	}
}

// TestRelayEnvOffDisables: MOONY_RELAY=off disables the relay even when a saved
// (or default) address would otherwise turn it on.
func TestRelayEnvOffDisables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&Config{RelayAddr: "saved.example:1"}).Save()
	t.Setenv("MOONY_RELAY", "off")
	if got := Load().RelayAddr; got != "" {
		t.Errorf("RelayAddr = %q, want \"\" — MOONY_RELAY=off must disable", got)
	}
}

// TestSQEAnthropicProvider: the sqe-anthropic backend routes to the Anthropic
// wire, defaults its base URL to the SQE gateway, and its API key falls back to
// the Anthropic key when its own isn't set.
func TestSQEAnthropicProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MOONY_PROVIDER", "sqe-anthropic")
	t.Setenv("MOONY_ANTHROPIC_API_KEY", "anthropic-key")
	// MOONY_SQE_ANTHROPIC_API_KEY unset → falls back to the Anthropic key.
	c := Load()
	if c.LLMProvider != provider.SQEAnthropic {
		t.Errorf("LLMProvider = %q, want %q", c.LLMProvider, provider.SQEAnthropic)
	}
	if c.Provider != ProviderAnthropic {
		t.Errorf("wire = %q, want %q (Anthropic native)", c.Provider, ProviderAnthropic)
	}
	if c.SQEAnthropicBaseURL != DefaultSQEAnthropicBaseURL {
		t.Errorf("SQE base URL = %q, want default gateway %q", c.SQEAnthropicBaseURL, DefaultSQEAnthropicBaseURL)
	}
	if c.SQEAnthropicAPIKey != "anthropic-key" {
		t.Errorf("SQE key = %q, want fallback to the Anthropic key", c.SQEAnthropicAPIKey)
	}
}

// TestSQEAnthropicOwnCredentials: explicit SQE env vars override the fallback key
// and the default gateway base URL.
func TestSQEAnthropicOwnCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MOONY_ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("MOONY_SQE_ANTHROPIC_API_KEY", "sqe-key")
	t.Setenv("MOONY_SQE_ANTHROPIC_BASE_URL", "http://gw.local:9000")
	c := Load()
	if c.SQEAnthropicAPIKey != "sqe-key" {
		t.Errorf("SQE key = %q, want the explicit sqe-key (no fallback)", c.SQEAnthropicAPIKey)
	}
	if c.SQEAnthropicBaseURL != "http://gw.local:9000" {
		t.Errorf("SQE base URL = %q, want the explicit override", c.SQEAnthropicBaseURL)
	}
}

// TestBatchContextEnvOverride covers the MOONY_BATCH_CONTEXT toggle: on by
// default, disabled when set to a falsey value.
func TestBatchContextEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("MOONY_BATCH_CONTEXT", "")
	if !Load().BatchContext {
		t.Error("BatchContext should default to true when MOONY_BATCH_CONTEXT is unset")
	}
	t.Setenv("MOONY_BATCH_CONTEXT", "off")
	if Load().BatchContext {
		t.Error("MOONY_BATCH_CONTEXT=off should disable BatchContext")
	}
	t.Setenv("MOONY_BATCH_CONTEXT", "on")
	if !Load().BatchContext {
		t.Error("MOONY_BATCH_CONTEXT=on should enable BatchContext")
	}
}

// TestTerseEnvOverride covers the MOONY_TERSE toggle: on by default, disabled when
// set to a falsey value.
func TestTerseEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("MOONY_TERSE", "")
	if !Load().Terse {
		t.Error("Terse should default to true when MOONY_TERSE is unset")
	}
	t.Setenv("MOONY_TERSE", "off")
	if Load().Terse {
		t.Error("MOONY_TERSE=off should disable Terse")
	}
	t.Setenv("MOONY_TERSE", "on")
	if !Load().Terse {
		t.Error("MOONY_TERSE=on should enable Terse")
	}
}

// TestCodeOutlineEnvOverride covers the MOONY_CODE_OUTLINE toggle: on by default,
// disabled when set to a falsey value.
func TestCodeOutlineEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("MOONY_CODE_OUTLINE", "")
	if !Load().CodeOutline {
		t.Error("CodeOutline should default to true when MOONY_CODE_OUTLINE is unset")
	}
	t.Setenv("MOONY_CODE_OUTLINE", "off")
	if Load().CodeOutline {
		t.Error("MOONY_CODE_OUTLINE=off should disable CodeOutline")
	}
	t.Setenv("MOONY_CODE_OUTLINE", "on")
	if !Load().CodeOutline {
		t.Error("MOONY_CODE_OUTLINE=on should enable CodeOutline")
	}
}

// TestDynamicEffortEnvOverride covers the MOONY_DYNAMIC_EFFORT toggle: on by
// default, disabled when set to a falsey value.
func TestDynamicEffortEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("MOONY_DYNAMIC_EFFORT", "")
	if !Load().DynamicEffort {
		t.Error("DynamicEffort should default to true when MOONY_DYNAMIC_EFFORT is unset")
	}
	t.Setenv("MOONY_DYNAMIC_EFFORT", "off")
	if Load().DynamicEffort {
		t.Error("MOONY_DYNAMIC_EFFORT=off should disable DynamicEffort")
	}
	t.Setenv("MOONY_DYNAMIC_EFFORT", "on")
	if !Load().DynamicEffort {
		t.Error("MOONY_DYNAMIC_EFFORT=on should enable DynamicEffort")
	}
}

// TestLowerReasoning checks the lesser-rank helper used by dynamic effort routing.
func TestLowerReasoning(t *testing.T) {
	if got := LowerReasoning(ReasoningHigh, ReasoningLow); got != ReasoningLow {
		t.Errorf("LowerReasoning(high, low) = %q, want low", got)
	}
	if got := LowerReasoning(ReasoningOff, ReasoningLow); got != ReasoningOff {
		t.Errorf("LowerReasoning(off, low) = %q, want off", got)
	}
	if got := LowerReasoning(ReasoningMedium, ReasoningHigh); got != ReasoningMedium {
		t.Errorf("LowerReasoning(medium, high) = %q, want medium", got)
	}
}

// TestPerModelReasoningPersistsAndResolves verifies that distinct reasoning
// levels remembered for different models survive a save/load round-trip and
// that Load() resolves the active model's level from the map (overriding the
// legacy single value).
func TestPerModelReasoningPersistsAndResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOONY_MODEL", "")
	t.Setenv("MOONY_REASONING", "")

	opus := "anthropic.claude-opus-4-8"
	gemini := "gcp:gemini-2.5-pro"

	// Save with Opus active, but record per-model levels for both Opus and a
	// Gemini id (different reasoning levels).
	c := &Config{
		Model:          opus,
		ReasoningLevel: ReasoningHigh,
		ReasoningByModel: map[string]string{
			opus:   ReasoningHigh,
			gemini: ReasoningMedium,
		},
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Loading with Opus still active should keep "high" from the map.
	got := Load()
	if got.Model != opus {
		t.Fatalf("model = %q, want %q", got.Model, opus)
	}
	if got.ReasoningLevel != ReasoningHigh {
		t.Errorf("active level for opus = %q, want %q", got.ReasoningLevel, ReasoningHigh)
	}
	if got.ReasoningByModel[gemini] != ReasoningMedium {
		t.Errorf("gemini level not preserved: %v", got.ReasoningByModel)
	}

	// Forcing the gemini id via env should resolve the active level to the
	// remembered "medium" without touching the legacy single value, and derive
	// the OpenAI endpoint from the id.
	t.Setenv("MOONY_MODEL", gemini)
	got = Load()
	if got.Model != gemini {
		t.Fatalf("env model not honored: %q", got.Model)
	}
	if got.Provider != ProviderOpenAI {
		t.Errorf("gemini model should derive the openai endpoint, got %q", got.Provider)
	}
	if got.ReasoningLevel != ReasoningMedium {
		t.Errorf("active level for gemini = %q, want %q", got.ReasoningLevel, ReasoningMedium)
	}
}

// TestSetReasoningForUpdatesActive verifies SetReasoningFor updates the active
// ReasoningLevel only when the target id matches the current Model.
func TestSetReasoningForUpdatesActive(t *testing.T) {
	c := &Config{Model: "a", ReasoningLevel: ReasoningOff}
	c.SetReasoningFor("a", ReasoningHigh)
	if c.ReasoningLevel != ReasoningHigh {
		t.Errorf("active level should follow current model, got %q", c.ReasoningLevel)
	}
	if c.ReasoningByModel["a"] != ReasoningHigh {
		t.Errorf("map entry not recorded: %v", c.ReasoningByModel)
	}
	// Recording a level for a different id must not touch the active level.
	c.SetReasoningFor("b", ReasoningLow)
	if c.ReasoningLevel != ReasoningHigh {
		t.Errorf("active level changed for unrelated model: %q", c.ReasoningLevel)
	}
	if got := c.GetReasoningFor("b"); got != ReasoningLow {
		t.Errorf("GetReasoningFor(b) = %q, want %q", got, ReasoningLow)
	}
	// Unknown id falls back to the legacy single value.
	c.ReasoningLevel = ReasoningMedium
	if got := c.GetReasoningFor("nope"); got != ReasoningMedium {
		t.Errorf("fallback to legacy ReasoningLevel = %q, want %q", got, ReasoningMedium)
	}
}

func TestEnvOverridesPrefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOONY_MODEL", "")
	t.Setenv("MOONY_REASONING", "")

	c := &Config{Model: "saved.model:0", ReasoningLevel: ReasoningLow}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Setenv("MOONY_MODEL", "env.model:0")
	if got := Load(); got.Model != "env.model:0" {
		t.Errorf("env should override saved model, got %q", got.Model)
	}
}

// TestSetModelDerivesRoute verifies SetModel keeps the endpoint and reasoning
// level in sync with the chosen model id.
func TestSetModelDerivesRoute(t *testing.T) {
	// The wire route now follows the backend (LLMProvider), not the model id.
	c := &Config{LLMProvider: provider.Anthropic, ReasoningByModel: map[string]string{}}
	c.SetModel("claude-sonnet-4-5")
	if c.Provider != ProviderAnthropic {
		t.Errorf("anthropic backend should use the anthropic route, got %q", c.Provider)
	}
	c.LLMProvider = provider.OpenAI
	c.SetModel("gpt-4o")
	if c.Provider != ProviderOpenAI {
		t.Errorf("openai backend should use the openai route, got %q", c.Provider)
	}
	c.LLMProvider = provider.Ollama
	c.SetModel("llama3.1:8b")
	if c.Provider != ProviderOpenAI {
		t.Errorf("ollama speaks the openai-compatible wire, got %q", c.Provider)
	}
}

// TestModelPersistenceDerivesProvider verifies the saved model alone (no saved
// provider) determines the route on reload.
func TestModelPersistenceDerivesProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOONY_MODEL", "")
	t.Setenv("MOONY_PROVIDER", "")

	// Save an anthropic backend + model; a reload derives the anthropic route.
	c := &Config{Model: "claude-sonnet-4-5", LLMProvider: provider.Anthropic, ReasoningByModel: map[string]string{}}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.Model != "claude-sonnet-4-5" {
		t.Errorf("persisted model = %q", got.Model)
	}
	if got.Provider != ProviderAnthropic {
		t.Errorf("anthropic backend should derive the anthropic route, got %q", got.Provider)
	}

	// Switching the backend to OpenAI derives the openai route.
	t.Setenv("MOONY_PROVIDER", "openai")
	got = Load()
	if got.Provider != ProviderOpenAI {
		t.Errorf("openai backend should derive the openai route, got %q", got.Provider)
	}
}

// TestLLMProviderSelection verifies the backend provider preference round-trips
// Load always reports "ollama" regardless of any legacy saved preference or the
// (now-ignored) MOONY_PROVIDER env var.
func TestLLMProviderSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOONY_MODEL", "")
	t.Setenv("MOONY_PROVIDER", "")

	// A saved provider preference round-trips.
	c := &Config{Model: DefaultModel, LLMProvider: "openai", ReasoningByModel: map[string]string{}}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(); got.LLMProvider != "openai" {
		t.Errorf("saved provider = %q, want openai", got.LLMProvider)
	}

	// MOONY_PROVIDER overrides the saved value.
	t.Setenv("MOONY_PROVIDER", "anthropic")
	got := Load()
	if got.LLMProvider != "anthropic" {
		t.Errorf("MOONY_PROVIDER override = %q, want anthropic", got.LLMProvider)
	}
	// The wire route follows the backend.
	if got.Provider != ProviderAnthropic {
		t.Errorf("anthropic backend should use the anthropic wire route, got %q", got.Provider)
	}

	// An unknown provider normalizes to the default (ollama).
	t.Setenv("MOONY_PROVIDER", "bogus")
	if got := Load(); got.LLMProvider != "ollama" {
		t.Errorf("unknown provider = %q, want ollama", got.LLMProvider)
	}
}

// TestLoadDefaultsAdaptiveModelAndCheapReadsOn verifies AdaptiveModel and
// CheapReads default to ON (right-size the model / route reads to the fast
// model out of the box) when no env var or saved persona choice overrides
// them, and that an explicit "0" env still disables each independently.
func TestLoadDefaultsAdaptiveModelAndCheapReadsOn(t *testing.T) {
	c := Load()
	if !c.AdaptiveModel {
		t.Error("AdaptiveModel should default to true")
	}
	if !c.CheapReads {
		t.Error("CheapReads should default to true")
	}

	t.Setenv("MOONY_ADAPTIVE_MODEL", "0")
	if Load().AdaptiveModel {
		t.Error("MOONY_ADAPTIVE_MODEL=0 should still disable it")
	}

	t.Setenv("MOONY_CHEAP_READS", "0")
	if Load().CheapReads {
		t.Error("MOONY_CHEAP_READS=0 should still disable it")
	}
}

// TestModelFallbackScopedToProvider verifies the "/models" live-fetch-failure
// fallback never shows an unrelated provider's models: the curated
// KnownModels/KnownOpenAIModels catalog is Anthropic/Gemini-gateway-specific,
// so every other provider must fall back to its own default model instead.
func TestModelFallbackScopedToProvider(t *testing.T) {
	for _, id := range []string{"anthropic", "sqe-anthropic", "gemini"} {
		got := ModelFallback(id)
		if len(got) == 0 {
			t.Errorf("ModelFallback(%q) empty, want the curated list", id)
		}
	}
	for _, id := range []string{"huggingface", "moonshot", "zai", "dashscope", "nvidia", "venice", "openrouter", "deepseek", "ollama", "unknown-provider"} {
		got := ModelFallback(id)
		if len(got) != 1 {
			t.Fatalf("ModelFallback(%q) = %v, want exactly the provider's own default model", id, got)
		}
		for _, known := range AllKnownModels() {
			if got[0].ID == known.ID {
				t.Errorf("ModelFallback(%q) = %q leaked the Anthropic/Gemini curated catalog", id, got[0].ID)
			}
		}
	}
}
