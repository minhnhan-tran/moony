package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// Anthropic backend: native Messages API at {base}/v1/messages
// (https://api.anthropic.com by default). Reuses the shared request/response
// types and SSE parser (parseStream), prompt-caching (applyPromptCache), and
// message-shape repairs (prepareMessages) — the wire format is Anthropic's, the
// same one those helpers were written for.

const anthropicVersion = "2023-06-01"

// usesAnthropic reports whether the configured backend is the native Anthropic
// Messages API.
func (c *Client) usesAnthropic() bool {
	return c.cfg != nil && provider.Normalize(c.cfg.LLMProvider) == provider.Anthropic
}

// anthropicBaseURL returns the Anthropic API base (no trailing slash), e.g.
// https://api.anthropic.com. The /v1/... path is appended by callers.
func (c *Client) anthropicBaseURL() string {
	if c.cfg != nil && strings.TrimSpace(c.cfg.AnthropicBaseURL) != "" {
		return strings.TrimRight(c.cfg.AnthropicBaseURL, "/")
	}
	return "https://api.anthropic.com"
}

func (c *Client) anthropicKey() string {
	if c.cfg == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.AnthropicAPIKey)
}

// resolveAnthropicModel picks the model id: a per-call ctx override, then the
// session model, then Anthropic's default.
func (c *Client) resolveAnthropicModel(ctx context.Context) string {
	if m := modelFrom(ctx); m != "" {
		return m
	}
	if c.cfg != nil && c.cfg.Model != "" {
		return c.cfg.Model
	}
	return provider.AnthropicDefaultModel
}

// anthropicBudget maps a reasoning level to a thinking token budget (Anthropic
// requires >=1024, and strictly less than max_tokens).
func anthropicBudget(level string) int {
	switch config.NormalizeReasoning(level) {
	case config.ReasoningMinimal, config.ReasoningLow:
		return 2048
	case config.ReasoningHigh:
		return 16384
	case config.ReasoningXHigh:
		return 24576
	case config.ReasoningMax:
		return 32768
	default: // medium / auto
		return 8192
	}
}

// streamAnthropic runs one turn against the native Messages API.
func (c *Client) streamAnthropic(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	if c.anthropicKey() == "" {
		return nil, fmt.Errorf("Anthropic API key not set — set MOONY_ANTHROPIC_API_KEY (or ANTHROPIC_API_KEY)")
	}
	model := c.resolveAnthropicModel(ctx)

	level := effortFrom(ctx)
	if level == "" && c.cfg != nil {
		level = c.cfg.ReasoningLevel
	}
	thinkingActive := config.ReasoningActive(provider.Anthropic, model, level)

	maxTokens := 8192
	if c.cfg != nil && c.cfg.MaxTokens > 0 {
		maxTokens = c.cfg.MaxTokens
	}

	body := &request{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  prepareMessages(messages, thinkingActive),
		Tools:     tools,
		Stream:    true,
	}
	if thinkingActive {
		budget := anthropicBudget(level)
		// max_tokens must exceed the thinking budget; bump it if the session cap
		// is too low to leave room for a visible answer.
		if body.MaxTokens <= budget {
			body.MaxTokens = budget + 4096
		}
		body.Thinking = &thinking{Type: "enabled", BudgetTokens: budget}
	}
	applyPromptCache(body)

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := c.anthropicBaseURL() + "/v1/messages"
	newReq := func() (*http.Request, error) { return c.newAnthropicRequest(ctx, url, raw) }
	return c.streamWithRetry(ctx, newReq, "anthropic", parseStream, h)
}

func (c *Client) newAnthropicRequest(ctx context.Context, url string, raw []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("x-api-key", c.anthropicKey())
	req.Header.Set("anthropic-version", anthropicVersion)
	return req, nil
}

// checkAnthropicAuth verifies the API key works by listing models.
func (c *Client) checkAnthropicAuth(ctx context.Context) error {
	if c.anthropicKey() == "" {
		return fmt.Errorf("Anthropic API key not set — set MOONY_ANTHROPIC_API_KEY (or ANTHROPIC_API_KEY)")
	}
	if _, err := c.listAnthropicModels(ctx); err != nil {
		return fmt.Errorf("cannot reach the Anthropic API: %w", err)
	}
	return nil
}

// listAnthropicModels returns the caller's available Claude models
// (GET {base}/v1/models).
func (c *Client) listAnthropicModels(ctx context.Context) ([]config.ModelInfo, error) {
	if c.anthropicKey() == "" {
		return nil, fmt.Errorf("Anthropic API key not set")
	}
	url := c.anthropicBaseURL() + "/v1/models?limit=1000"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.anthropicKey())
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("models list failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]config.ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		models = append(models, config.ModelInfo{ID: m.ID, Name: name, Thinking: config.ModelSupportsThinking(m.ID)})
	}
	return models, nil
}
