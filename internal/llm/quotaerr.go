package llm

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// ErrQuotaExhausted marks an error as a provider's HARD quota stop — the account
// is spent out for the period and no retry or smaller request can succeed until it
// resets.
//
// Wrapped (not returned directly) so the user-facing message survives, letting
// callers react structurally via errors.Is instead of matching on message text.
// The agent's quota fallback uses this to switch to another backend.
//
// Deliberately NOT used for ordinary per-minute rate limits: those clear on their
// own, so retrying is the right response and changing providers would be wrong.
var ErrQuotaExhausted = errors.New("provider quota exhausted for the current period")

// hardQuota429Markers are body substrings that distinguish a spent-out account
// from a transient rate limit. Both arrive as HTTP 429, so the status alone can't
// tell them apart.
//
// Deliberately conservative: a false positive silently moves someone's traffic to
// another backend (different models, different billing) for the rest of the
// period, which is worse than the status quo of retrying. Phrases that merely
// indicate "too fast" (requests per minute, TPM, "slow down", "overloaded") are
// excluded on purpose — see transientRateLimitMarkers.
var hardQuota429Markers = []string{
	"insufficient_quota",              // OpenAI: billing hard stop
	"exceeded your current quota",     // OpenAI
	"billing_hard_limit_reached",      // OpenAI
	"quota exceeded",                  // Gemini / generic
	"daily quota",                     // generic daily cap
	"daily limit",                     // generic daily cap
	"monthly quota",                   // generic period cap
	"credit balance is too low",       // Anthropic: out of credits
	"out of credits",                  // OpenRouter / others
	"insufficient credit",             // DeepSeek / Together / others
	"insufficient_user_quota",         // Moonshot / Z.AI
	"exhausted your monthly",          // Copilot-style period cap
	"spending limit",                  // Azure / vendor spend cap
	"budget has been exceeded",        // vendor budget cap
	"resource_exhausted",              // Vertex/gRPC status surfaced as text
	"free tier limit",                 // free-plan hard stop
	"no remaining balance",            // generic
	"account balance is insufficient", // DashScope / Qwen
}

// transientRateLimitMarkers indicate an ordinary throttle that WILL clear, so they
// must never be treated as a hard quota stop even when a body also happens to
// contain a generic word like "limit".
var transientRateLimitMarkers = []string{
	"requests per minute",
	"tokens per minute",
	"requests per second",
	"rate limit reached for",
	"please slow down",
	"try again in",
	"overloaded",
	"concurrent",
}

// isHardQuotaExceeded reports whether a 429 body describes a spent-out account
// rather than a transient throttle.
func isHardQuotaExceeded(body string) bool {
	b := strings.ToLower(body)
	if b == "" {
		return false
	}
	// A transient marker wins: "rate limit reached for gpt-4o … requests per minute"
	// mentions a limit but clears on its own.
	for _, m := range transientRateLimitMarkers {
		if strings.Contains(b, m) {
			return false
		}
	}
	for _, m := range hardQuota429Markers {
		if strings.Contains(b, m) {
			return true
		}
	}
	return false
}

// quotaStopError wraps a hard-quota response body in ErrQuotaExhausted, keeping the
// provider's own message for the user while letting callers branch via errors.Is.
// Returns nil when the response is not a hard quota stop.
func quotaStopError(status int, body string) error {
	if status != http.StatusTooManyRequests && status != http.StatusPaymentRequired {
		return nil
	}
	// 402 Payment Required is unambiguous; a 429 needs the body to disambiguate.
	if status == http.StatusTooManyRequests && !isHardQuotaExceeded(body) {
		return nil
	}
	msg := redactSecrets(strings.TrimSpace(body))
	if msg == "" {
		msg = "provider reported its quota exhausted"
	}
	if len(msg) > 400 { // keep the notice to one readable line
		msg = msg[:400] + "…"
	}
	return &quotaExhaustedError{msg: msg}
}

// quotaExhaustedError keeps the provider's message as the error text while
// reporting ErrQuotaExhausted through errors.Is/Unwrap.
type quotaExhaustedError struct{ msg string }

func (e *quotaExhaustedError) Error() string { return e.msg }
func (e *quotaExhaustedError) Unwrap() error { return ErrQuotaExhausted }

// secretPattern matches API-key-shaped tokens that providers sometimes echo back in
// error bodies (e.g. "Incorrect API key provided: sk-abc…").
//
// This body is surfaced to the user AND persisted into the transcript, which is
// searchable and syncable — so a key echoed by the provider would otherwise be written
// to disk in plain text. Cheap insurance: the message stays readable either way.
var secretPattern = regexp.MustCompile(`(?i)\b(sk|xai|gsk|pplx|nvapi|hf|csk|glpat|ghp|gho|ghu|ghs|ghr|api|key|token|bearer)[-_]?[A-Za-z0-9_\-]{16,}`)

// redactSecrets masks key-shaped substrings in provider-supplied text.
func redactSecrets(s string) string {
	return secretPattern.ReplaceAllString(s, "[redacted]")
}
