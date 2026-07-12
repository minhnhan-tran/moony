package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/pysty1995/moony/internal/provider"
)

// TestWithModelRoundTrip verifies the per-call model override carried on the
// context: WithModel sets it, WithModel("") is a no-op, and modelFrom reads it
// back. This is what lets the web /model picker (which mutates a per-session
// config) take effect through the shared *Client.
func TestWithModelRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := modelFrom(ctx); got != "" {
		t.Errorf("modelFrom(empty ctx) = %q, want \"\"", got)
	}
	if got := modelFrom(WithModel(ctx, "")); got != "" {
		t.Errorf("WithModel(ctx, \"\") should be a no-op, got %q", got)
	}
	if got := modelFrom(WithModel(ctx, "claude-opus-4-8")); got != "claude-opus-4-8" {
		t.Errorf("modelFrom after WithModel = %q, want claude-opus-4-8", got)
	}
}

// anthropicModelSSE is a minimal successful Anthropic stream.
const anthropicModelSSE = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"

// TestStreamMessagesModelOverride is the regression test for the web /model bug:
// the LLM client is shared across sessions, so a per-session model change must
// ride on the context (WithModel) to reach the request, rather than relying on
// the client's own configured model. It also checks the override is side-effect
// free — the shared client's config is never mutated.
func TestStreamMessagesModelOverride(t *testing.T) {
	var sentModel string
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		sentModel = req.Model
		return resp(200, anthropicModelSSE), nil
	}))
	c.cfg.Model = "base-model"

	// With an override, the request carries the overridden model.
	if _, err := c.StreamMessages(WithModel(context.Background(), "picked-model"), "sys", []Message{UserText("hi")}, nil, Handler{}); err != nil {
		t.Fatalf("StreamMessages (override): %v", err)
	}
	if sentModel != "picked-model" {
		t.Errorf("request model = %q, want the overridden \"picked-model\"", sentModel)
	}

	// Without an override, the client's configured model is used — and the prior
	// override must not have mutated the shared client's config.
	if _, err := c.StreamMessages(context.Background(), "sys", []Message{UserText("hi")}, nil, Handler{}); err != nil {
		t.Fatalf("StreamMessages (default): %v", err)
	}
	if sentModel != "base-model" {
		t.Errorf("request model = %q, want the client's \"base-model\"", sentModel)
	}
	if c.cfg.Model != "base-model" {
		t.Errorf("override mutated the shared client's config: cfg.Model = %q", c.cfg.Model)
	}
}

// TestWithProviderRoundTrip verifies the per-call provider override carried on
// the context: WithProvider sets it, WithProvider("") is a no-op, and
// providerFrom reads it back. This is what lets the Fast / Self-review tiers
// route a single call to a different provider than the session.
func TestWithProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := providerFrom(ctx); got != "" {
		t.Errorf("providerFrom(empty ctx) = %q, want \"\"", got)
	}
	if got := providerFrom(WithProvider(ctx, "")); got != "" {
		t.Errorf("WithProvider(ctx, \"\") should be a no-op, got %q", got)
	}
	if got := providerFrom(WithProvider(ctx, "anthropic")); got != "anthropic" {
		t.Errorf("providerFrom after WithProvider = %q, want anthropic", got)
	}
}

// TestStreamMessagesProviderOverride verifies the per-call provider override
// re-routes a single call: an Ollama-configured client, told via WithProvider to
// use Anthropic, sends the request to the Anthropic base URL — without mutating
// the shared client's provider.
func TestStreamMessagesProviderOverride(t *testing.T) {
	var sentHost, sentModel string
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sentHost = r.URL.Host
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		sentModel = req.Model
		return resp(200, anthropicModelSSE), nil
	}))
	c.cfg.AnthropicBaseURL = "https://anthropic.test"
	c.cfg.AnthropicAPIKey = "test-key"

	// Route THIS call to Anthropic (different from the ollama session) with an
	// Anthropic model: the clone re-selects the Anthropic wire + its base URL.
	ctx := WithModel(WithProvider(context.Background(), "anthropic"), "claude-haiku-4-5")
	if _, err := c.StreamMessages(ctx, "sys", []Message{UserText("hi")}, nil, Handler{}); err != nil {
		t.Fatalf("StreamMessages (provider override): %v", err)
	}
	if sentHost != "anthropic.test" {
		t.Errorf("request host = %q, want anthropic.test (routed to the overridden provider)", sentHost)
	}
	if sentModel != "claude-haiku-4-5" {
		t.Errorf("request model = %q, want claude-haiku-4-5", sentModel)
	}
	if c.cfg.LLMProvider != provider.Ollama {
		t.Errorf("provider override mutated the shared client: cfg.LLMProvider = %q, want ollama", c.cfg.LLMProvider)
	}
}
