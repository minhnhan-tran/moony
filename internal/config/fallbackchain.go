package config

import (
	"strings"

	"github.com/pysty1995/moony/internal/provider"
)

// Quota-fallback chain parsing.
//
// The chain is a user-editable, comma-separated list of backends to move through
// as quotas run out, e.g.
//
//	"anthropic, openai:gpt-4o, ollama:qwen3-coder:30b:high"
//
// Switching provider ALWAYS means switching model too — model ids are not
// portable across backends (an `anthropic.claude-…` id means nothing to Ollama) —
// so every hop carries a model, either explicitly or via the provider's default.

// StrictReasoning reports whether s names a real reasoning level, returning it
// normalized.
//
// NormalizeReasoning is deliberately lenient (it coerces anything unrecognized to
// a default), which makes it useless for PARSING: it can't tell a reasoning level
// from a model-id fragment. The chain parser needs to know whether a trailing
// ":30b" is a reasoning level or part of the model id, so it needs this strict
// form instead.
func StrictReasoning(s string) (string, bool) {
	lvl := strings.ToLower(strings.TrimSpace(s))
	if lvl == "" {
		return "", false
	}
	// reasoningOrder is the authoritative set of concrete levels; "auto" is a
	// valid selection too but is resolved per turn rather than stored as effort.
	if _, ok := reasoningOrder[lvl]; ok {
		return lvl, true
	}
	if lvl == ReasoningAuto {
		return ReasoningAuto, true
	}
	return "", false
}

// FallbackTarget is one hop of the quota fallback chain: which provider to switch
// to, and optionally the model and reasoning level to use there.
//
// Model empty means provider.DefaultModelFor(Provider) — model ids are not
// portable across backends, so a hop always resolves to *some* model. Reasoning
// empty means "leave it to the existing per-model memory" (Config.ReasoningByModel
// via SetModel), which is what a bare provider chain expects.
type FallbackTarget struct {
	Provider  string
	Model     string
	Reasoning string
}

// String renders the hop back to its "provider[:model[:reasoning]]" spec.
func (t FallbackTarget) String() string {
	s := t.Provider
	if t.Model != "" {
		s += ":" + t.Model
	} else if t.Reasoning != "" {
		s += ":" // keep the slot so "provider::reasoning" round-trips
	}
	if t.Reasoning != "" {
		s += ":" + t.Reasoning
	}
	return s
}

// ParseFallbackChain parses the comma-separated fallback spec into ordered,
// de-duplicated hops, each optionally carrying a model and reasoning level:
//
//	"openai, ollama:qwen3-coder:30b:high, anthropic::low"
//
// The trailing segment is treated as a reasoning level only when StrictReasoning
// recognizes it, so "ollama:qwen3-coder:30b" keeps its full model id while
// "ollama:qwen3-coder:30b:high" splits correctly — Ollama model ids legitimately
// contain colons. An empty model slot ("anthropic::low") sets reasoning while
// keeping the provider's default model.
//
// Entries whose provider isn't a known backend are dropped: a typo must never
// become a live switch target.
func ParseFallbackChain(s string) []FallbackTarget {
	// Commas/semicolons/newlines separate entries, and so does whitespace — no
	// provider or model id contains a space, so a space-separated form
	// ("openai ollama") keeps working too.
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == ' ' || r == '\t' || r == '\r'
	})
	out := make([]FallbackTarget, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		var model, reasoning string
		rest := f
		// Right-split: peel a trailing reasoning level first (only if it really is
		// one), then take everything after the FIRST colon as the model, so
		// colon-bearing ollama ids survive intact.
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			if lvl, ok := StrictReasoning(rest[i+1:]); ok {
				reasoning = lvl
				rest = rest[:i]
			}
		}
		if i := strings.Index(rest, ":"); i >= 0 {
			model = strings.TrimSpace(rest[i+1:])
			rest = rest[:i]
		}
		p, ok := provider.Lookup(strings.TrimSpace(rest))
		if !ok || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, FallbackTarget{Provider: p.ID, Model: model, Reasoning: reasoning})
	}
	return out
}

// FallbackChainSpec renders a parsed chain back to its canonical spec string.
func FallbackChainSpec(chain []FallbackTarget) string {
	parts := make([]string, 0, len(chain))
	for _, t := range chain {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, ",")
}

// FallbackChainProviders projects a chain down to its provider ids, which is all
// the switch-order logic (nextFallbackProvider) needs.
func FallbackChainProviders(chain []FallbackTarget) []string {
	out := make([]string, 0, len(chain))
	for _, t := range chain {
		out = append(out, t.Provider)
	}
	return out
}

// ParseProviderFallbacks parses a chain spec down to bare, validated provider
// ids, dropping any per-hop model/reasoning detail. Used for the plain
// MN_PROVIDER_FALLBACKS form and anywhere only the switch order matters.
func ParseProviderFallbacks(s string) []string {
	return FallbackChainProviders(ParseFallbackChain(s))
}

// FallbackChain returns the configured chain as parsed hops.
func (c *Config) FallbackChain() []FallbackTarget {
	if c == nil {
		return nil
	}
	return ParseFallbackChain(strings.Join(c.ProviderFallbacks, ","))
}

// FallbackChainSpecs is the exported form of fallbackSpecs, for callers outside
// this package that persist a chain (the web persona editor).
func FallbackChainSpecs(chain []FallbackTarget) []string { return fallbackSpecs(chain) }

// fallbackSpecs renders each hop to its canonical "provider[:model[:reasoning]]"
// spec. ProviderFallbacks stores these strings (rather than bare provider ids) so
// a per-hop model/reasoning choice round-trips through prefs and the env.
func fallbackSpecs(chain []FallbackTarget) []string {
	out := make([]string, 0, len(chain))
	for _, t := range chain {
		out = append(out, t.String())
	}
	return out
}
