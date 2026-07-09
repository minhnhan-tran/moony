package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
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

// anthropicVerRe extracts the family version from a model id in the modern
// "claude-<family>-<major>[-<minor>]" layout (claude-opus-4-8, claude-sonnet-5,
// claude-fable-5). A provider prefix ("anthropic.") and dated/Bedrock suffixes
// ("-20250929-v1:0") don't interfere — it matches the family token wherever it sits.
var anthropicVerRe = regexp.MustCompile(`(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d+))?`)

// anthropicModelRank returns the model version as major*100+minor (4.8 -> 408,
// 5 -> 500), or 0 when it can't be determined. The 2.x/3.x families put their
// version digits *before* the family name and never support adaptive thinking,
// so they short-circuit to a low rank before the modern-layout regex (which would
// otherwise mis-read a date suffix like "claude-3-5-sonnet-20241022" as the version).
func anthropicModelRank(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "claude-2") || strings.Contains(m, "claude-3") {
		return 300
	}
	mm := anthropicVerRe.FindStringSubmatch(m)
	if mm == nil {
		return 0
	}
	major, _ := strconv.Atoi(mm[1])
	minor := 0
	if mm[2] != "" {
		minor, _ = strconv.Atoi(mm[2])
	}
	return major*100 + minor
}

// anthropicAdaptiveThinking reports whether a model uses the modern adaptive
// thinking API (thinking.type "adaptive" + output_config.effort) instead of the
// legacy fixed-budget mode (thinking.type "enabled" + budget_tokens). Adaptive
// arrived with the 4.6 family: 4.6/4.7/4.8, Sonnet 5, Fable/Mythos 5, and newer
// accept it (and from 4.7 on REQUIRE it — legacy "enabled" 400s there); 4.5 and
// earlier still use budget_tokens. Unrecognized ids fall back to the legacy shape
// (moony's long-standing default, and the API contract for the default 4.5 model).
func anthropicAdaptiveThinking(model string) bool {
	return anthropicModelRank(model) >= 406
}

// anthropicEffort maps a moony reasoning level to an Anthropic output_config
// effort for adaptive-thinking models. xhigh only exists from 4.7 on, so it
// clamps to high on 4.6. Called only while thinking is active, so it always
// returns a concrete effort (medium for auto/unspecified).
func anthropicEffort(model, level string) string {
	switch config.NormalizeReasoning(level) {
	case config.ReasoningMinimal, config.ReasoningLow:
		return "low"
	case config.ReasoningHigh:
		return "high"
	case config.ReasoningXHigh:
		if anthropicModelRank(model) < 407 {
			return "high" // xhigh unsupported on 4.6
		}
		return "xhigh"
	case config.ReasoningMax:
		return "max"
	default: // medium / auto
		return "medium"
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
		// Extended thinking needs headroom under max_tokens for both the reasoning
		// and a visible answer; bump the cap when the session limit is too low.
		if body.MaxTokens <= budget {
			body.MaxTokens = budget + 4096
		}
		if anthropicAdaptiveThinking(model) {
			// 4.6+ models: adaptive thinking + effort. The legacy
			// {type:"enabled", budget_tokens} shape is rejected with a 400 on
			// 4.7/4.8, Sonnet 5, and Fable 5. display "summarized" surfaces a
			// readable reasoning summary (the default "omitted" streams empty
			// thinking text, which shows as a blank pause in the UI).
			body.Thinking = &thinking{Type: "adaptive", Display: "summarized"}
			body.OutputConfig = &outputConfig{Effort: anthropicEffort(model, level)}
		} else {
			// 4.5 and earlier: fixed thinking budget.
			body.Thinking = &thinking{Type: "enabled", BudgetTokens: budget}
		}
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
