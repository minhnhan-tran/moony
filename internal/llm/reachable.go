package llm

import (
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// ProviderReachable reports whether provider id could actually serve a request
// with the credentials and endpoints in cfg.
//
// This exists for the quota fallback chain (internal/agent/quotafallback.go):
// hopping to a backend with no API key would turn "you are near your quota" into
// "every turn now fails", which is worse than staying put. So the chain skips
// providers that aren't reachable.
//
// It deliberately reuses the SAME resolution the streaming path uses — the
// remoteBackendFor switch, usesAnthropic/usesBedrock/usesCopilot — instead of
// re-listing which provider needs which key. A second copy of that mapping would
// drift the moment a provider is added, and the failure mode (skipping a usable
// backend, or hopping to an unusable one) is silent.
//
// It is a static credential check, not a health check: no network request is made,
// so a configured-but-down endpoint still counts as reachable. Being wrong in that
// direction is safe — the switch is retried on the next reading.
func ProviderReachable(cfg *config.Config, id string) bool {
	// Validate BEFORE normalizing: Normalize falls back to the default provider for
	// anything it doesn't recognize, so normalizing first would silently turn a
	// typo'd chain entry ("opneai") into the default backend and report it
	// reachable — making a misspelling a live switch target.
	p, ok := provider.Lookup(id)
	if cfg == nil || !ok {
		return false
	}
	id = p.ID

	// Resolve against a clone pointed at the candidate provider: every credential
	// getter keys off cfg.LLMProvider, so this asks "would a client configured for
	// id find what it needs?" without disturbing the live config.
	probe := *cfg
	probe.LLMProvider = id
	if provider.UsesAnthropicWire(id) {
		probe.Provider = config.ProviderAnthropic
	} else {
		probe.Provider = config.ProviderOpenAI
	}
	c := &Client{cfg: &probe}

	switch {
	case c.usesCopilot():
		// The Copilot SDK talks to a local CLI; it can authenticate from the CLI's
		// own stored login, so neither the token nor an explicit path is required.
		return true
	case c.usesBedrock():
		// Static keys, a named profile, or the ambient AWS chain (instance role,
		// SSO cache, env). Only the first two are visible without a network call;
		// treat a configured region as enough to try, since LoadDefaultConfig may
		// still find ambient credentials.
		hasStatic := strings.TrimSpace(probe.AWSAccessKeyID) != "" && strings.TrimSpace(probe.AWSSecretAccessKey) != ""
		return hasStatic || strings.TrimSpace(probe.AWSProfile) != "" || strings.TrimSpace(probe.AWSRegion) != ""
	case c.usesAnthropic():
		return c.anthropicKey() != ""
	case c.usesRemote():
		b := c.remoteBackendFor(id)
		if strings.TrimSpace(b.baseURL) == "" {
			return false
		}
		// noKeyOK marks the local/self-hosted backends (LM Studio, llama.cpp, …)
		// that serve unauthenticated requests.
		return b.noKeyOK || strings.TrimSpace(b.apiKey) != ""
	default:
		// Ollama: local server, no key. Reachable as long as a base URL is set.
		return strings.TrimSpace(config.NormalizeOllamaBaseURL(probe.OllamaBaseURL)) != "" ||
			strings.TrimSpace(probe.OllamaBaseURL) != ""
	}
}
