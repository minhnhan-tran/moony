package llm

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// The Anthropic wire serves TWO providers — api.anthropic.com and the SQE LAN gateway —
// from this one file. Every user-visible string therefore has to distinguish them, or an
// SQE session is told to go look at Anthropic's dashboard and set Anthropic's env var.
func TestAnthropicErrorsNameTheActiveProvider(t *testing.T) {
	sqe := &Client{cfg: &config.Config{LLMProvider: provider.SQEAnthropic}}
	direct := &Client{cfg: &config.Config{LLMProvider: provider.Anthropic}}

	sqeMsg := sqe.anthropicNoKeyErr().Error()
	if !strings.Contains(sqeMsg, "MN_SQE_ANTHROPIC_API_KEY") {
		t.Errorf("SQE missing-key error must name MN_SQE_ANTHROPIC_API_KEY, got %q", sqeMsg)
	}
	if strings.HasPrefix(sqeMsg, "Anthropic API key") {
		t.Errorf("SQE error must not present itself as plain Anthropic: %q", sqeMsg)
	}

	directMsg := direct.anthropicNoKeyErr().Error()
	if !strings.Contains(directMsg, "MN_ANTHROPIC_API_KEY") {
		t.Errorf("direct Anthropic error must name MN_ANTHROPIC_API_KEY, got %q", directMsg)
	}
	if strings.Contains(directMsg, "MN_SQE_ANTHROPIC_API_KEY") {
		t.Errorf("direct Anthropic error must not mention the SQE variable: %q", directMsg)
	}

	if sqe.anthropicLabel() == direct.anthropicLabel() {
		t.Errorf("both providers label as %q; the two must be distinguishable", sqe.anthropicLabel())
	}
}

// The config field and the Persona card both promise that the SQE key falls back to the
// plain Anthropic one. config.Load applies that at startup, but a key entered later (in
// the Persona panel) sets only AnthropicAPIKey — so the fallback has to hold here too,
// or the promise quietly stops being true after startup.
func TestAnthropicKeySQEFallsBackToAnthropicKey(t *testing.T) {
	both := &Client{cfg: &config.Config{
		LLMProvider:        provider.SQEAnthropic,
		SQEAnthropicAPIKey: "sqe-key",
		AnthropicAPIKey:    "anthropic-key",
	}}
	if got := both.anthropicKey(); got != "sqe-key" {
		t.Errorf("key = %q, want the explicit SQE key to win", got)
	}

	fallback := &Client{cfg: &config.Config{
		LLMProvider:     provider.SQEAnthropic,
		AnthropicAPIKey: "anthropic-key",
	}}
	if got := fallback.anthropicKey(); got != "anthropic-key" {
		t.Errorf("key = %q, want the Anthropic key as documented fallback", got)
	}

	// The reverse must NOT hold: a direct Anthropic session has no business
	// authenticating with a credential issued for the SQE gateway.
	reverse := &Client{cfg: &config.Config{
		LLMProvider:        provider.Anthropic,
		SQEAnthropicAPIKey: "sqe-key",
	}}
	if got := reverse.anthropicKey(); got != "" {
		t.Errorf("key = %q, want empty — the SQE key must not leak to api.anthropic.com", got)
	}
}

// The SQE gateway is a LAN address, so a reachability failure must name it. "Cannot reach
// the Anthropic API" sends the user to check their internet and Anthropic's status page.
func TestAnthropicBaseURLDistinguishesSQE(t *testing.T) {
	sqe := &Client{cfg: &config.Config{LLMProvider: provider.SQEAnthropic}}
	direct := &Client{cfg: &config.Config{LLMProvider: provider.Anthropic}}
	if sqe.anthropicBaseURL() == direct.anthropicBaseURL() {
		t.Errorf("both resolve to %q; SQE must use its own gateway", sqe.anthropicBaseURL())
	}
	if strings.Contains(sqe.anthropicBaseURL(), "api.anthropic.com") {
		t.Errorf("SQE base URL points at Anthropic: %q", sqe.anthropicBaseURL())
	}
}
