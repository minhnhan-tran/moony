package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// A hard quota stop must be told apart from an ordinary rate limit: both arrive as
// HTTP 429, but only the former justifies moving the user's traffic to a different
// backend for the rest of the period.
func TestIsHardQuotaExceeded(t *testing.T) {
	hard := []string{
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
		`{"error":{"message":"Your credit balance is too low to access the API"}}`,
		`{"error":"quota exceeded for this project"}`,
		`{"error":{"code":"billing_hard_limit_reached"}}`,
		`{"error":{"message":"You have exhausted your monthly allowance"}}`,
		`{"error":{"message":"insufficient_user_quota"}}`,
		`{"error":{"message":"Account balance is insufficient"}}`,
		`{"error":{"status":"RESOURCE_EXHAUSTED"}}`,
		`daily quota reached`,
		`{"error":{"message":"You are out of credits"}}`,
	}
	for _, b := range hard {
		if !isHardQuotaExceeded(b) {
			t.Errorf("expected a HARD quota stop for %q", b)
		}
	}

	// These clear on their own; retrying is correct and switching provider is not.
	transient := []string{
		`{"error":{"message":"Rate limit reached for gpt-4o in organization org-x on requests per minute (RPM)"}}`,
		`{"error":{"message":"Rate limit exceeded: 40000 tokens per minute"}}`,
		`{"error":{"message":"Please slow down"}}`,
		`{"error":{"message":"Overloaded"}}`,
		`{"error":{"message":"Too many concurrent requests"}}`,
		`{"error":{"message":"Rate limit reached for requests, try again in 20s"}}`,
		``,
		`{"error":{"message":"internal server error"}}`,
	}
	for _, b := range transient {
		if isHardQuotaExceeded(b) {
			t.Errorf("expected NOT a hard quota stop for %q", b)
		}
	}
}

// A body that mentions a limit AND a per-minute window is transient: the explicit
// throttle marker must win, or a plain rate limit would silently reroute traffic.
func TestTransientMarkerWinsOverGenericLimit(t *testing.T) {
	body := `{"error":{"message":"You have hit your daily limit of requests per minute, try again in 30s"}}`
	if isHardQuotaExceeded(body) {
		t.Error("a per-minute/try-again body must be treated as transient")
	}
}

func TestQuotaStopError(t *testing.T) {
	t.Run("429 with a hard-quota body wraps ErrQuotaExhausted", func(t *testing.T) {
		err := quotaStopError(http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, ErrQuotaExhausted) {
			t.Error("must satisfy errors.Is(err, ErrQuotaExhausted)")
		}
		// The provider's own message must survive for the user.
		if !strings.Contains(err.Error(), "insufficient_quota") {
			t.Errorf("provider message lost: %q", err.Error())
		}
	})
	t.Run("429 with a transient body is not a quota stop", func(t *testing.T) {
		if err := quotaStopError(http.StatusTooManyRequests, "rate limit reached for requests per minute"); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
	t.Run("402 is unambiguous regardless of body", func(t *testing.T) {
		err := quotaStopError(http.StatusPaymentRequired, "")
		if err == nil || !errors.Is(err, ErrQuotaExhausted) {
			t.Fatalf("402 must be a quota stop, got %v", err)
		}
		if err.Error() == "" {
			t.Error("an empty body must still yield a readable message")
		}
	})
	t.Run("other statuses are never quota stops", func(t *testing.T) {
		for _, code := range []int{http.StatusOK, http.StatusInternalServerError, http.StatusUnauthorized, http.StatusServiceUnavailable} {
			if err := quotaStopError(code, `{"error":{"code":"insufficient_quota"}}`); err != nil {
				t.Errorf("status %d must not be a quota stop, got %v", code, err)
			}
		}
	})
	t.Run("long bodies are truncated to one readable line", func(t *testing.T) {
		err := quotaStopError(http.StatusTooManyRequests, "insufficient_quota "+strings.Repeat("x", 2000))
		if err == nil {
			t.Fatal("expected an error")
		}
		if len(err.Error()) > 420 {
			t.Errorf("message not truncated: %d chars", len(err.Error()))
		}
	})
}

// The fallback chain must never hop to a backend that can't serve a request: doing
// so turns "near your quota" into "every turn fails".
func TestProviderReachable(t *testing.T) {
	t.Run("hosted providers need a key", func(t *testing.T) {
		bare := &config.Config{}
		for _, id := range []string{provider.OpenAI, provider.Anthropic, provider.Groq} {
			if ProviderReachable(bare, id) {
				t.Errorf("%s must be unreachable with no key", id)
			}
		}
	})
	t.Run("a key makes them reachable", func(t *testing.T) {
		if !ProviderReachable(&config.Config{OpenAIAPIKey: "sk-x"}, provider.OpenAI) {
			t.Error("openai with a key must be reachable")
		}
		if !ProviderReachable(&config.Config{AnthropicAPIKey: "sk-y"}, provider.Anthropic) {
			t.Error("anthropic with a key must be reachable")
		}
	})
	t.Run("local backends need no key", func(t *testing.T) {
		if !ProviderReachable(&config.Config{OllamaBaseURL: "http://localhost:11434/v1"}, provider.Ollama) {
			t.Error("ollama with a base URL must be reachable")
		}
	})
	t.Run("unknown ids are never reachable", func(t *testing.T) {
		// Regression: Normalize() falls back to the DEFAULT provider for anything it
		// doesn't recognize, so validating after normalizing would silently accept a
		// typo'd chain entry as the default backend.
		for _, id := range []string{"", "opneai", "definitely-not-a-provider"} {
			if ProviderReachable(&config.Config{OpenAIAPIKey: "sk-x", OllamaBaseURL: "http://x/v1"}, id) {
				t.Errorf("%q must never be reachable", id)
			}
		}
	})
	t.Run("nil config is never reachable", func(t *testing.T) {
		if ProviderReachable(nil, provider.OpenAI) {
			t.Error("a nil config must not be reachable")
		}
	})
	t.Run("probing does not mutate the caller's config", func(t *testing.T) {
		cfg := &config.Config{LLMProvider: provider.Ollama, OpenAIAPIKey: "sk-x"}
		ProviderReachable(cfg, provider.OpenAI)
		if cfg.LLMProvider != provider.Ollama {
			t.Errorf("LLMProvider = %q, want the caller's %q untouched", cfg.LLMProvider, provider.Ollama)
		}
	})
}

// The provider's response body is shown to the user AND persisted into the transcript,
// so a key echoed back by the provider must not be written to disk in plain text.
func TestQuotaStopErrorRedactsSecrets(t *testing.T) {
	cases := []struct{ name, body, leak string }{
		{"openai style", `{"error":{"code":"insufficient_quota","message":"Incorrect API key provided: sk-proj-AbCdEf0123456789XyZw"}}`, "sk-proj-AbCdEf0123456789XyZw"},
		{"bearer header echo", `insufficient_quota; sent Authorization: Bearer gsk_0123456789abcdefghij`, "gsk_0123456789abcdefghij"},
		{"xai style", `quota exceeded for xai-0123456789abcdefghijklmn`, "xai-0123456789abcdefghijklmn"},
	}
	for _, tc := range cases {
		err := quotaStopError(http.StatusTooManyRequests, tc.body)
		if err == nil {
			t.Fatalf("%s: expected a quota stop for %q", tc.name, tc.body)
		}
		if strings.Contains(err.Error(), tc.leak) {
			t.Errorf("%s: secret leaked into the error text: %q", tc.name, err.Error())
		}
		if !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("%s: expected a [redacted] marker, got %q", tc.name, err.Error())
		}
	}
}

// Redaction must not eat the human-readable part of the message.
func TestRedactSecretsKeepsProse(t *testing.T) {
	got := redactSecrets("You exceeded your current quota, please check your plan and billing details.")
	if got != "You exceeded your current quota, please check your plan and billing details." {
		t.Errorf("prose was altered: %q", got)
	}
	// And the hard-quota classification still works on a redacted body.
	if !isHardQuotaExceeded(redactSecrets(`{"error":{"code":"insufficient_quota","message":"key sk-abcdefghijklmnopqrst"}}`)) {
		t.Error("redaction must not break hard-quota detection")
	}
}
