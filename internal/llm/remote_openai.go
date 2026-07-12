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

// Hosted OpenAI-compatible backends: OpenAI, OpenRouter, Gemini, DeepSeek,
// Groq, xAI, Mistral, Together.ai, Fireworks.ai, Moonshot (Kimi), Z.AI (GLM),
// DashScope (Qwen), NVIDIA NIM, Hugging Face, and Venice. They all speak the
// OpenAI Chat Completions wire format, so they share the request builder + SSE
// parser used by the Ollama backend; they differ only in base URL, auth
// header, extra headers, and reasoning handling.

// remoteBackend captures the per-provider connection details for a hosted
// OpenAI-compatible API.
type remoteBackend struct {
	id      string            // provider id, e.g. provider.OpenAI
	baseURL string            // includes /v1 (or equivalent), no trailing slash
	apiKey  string            // bearer token
	extra   map[string]string // extra request headers (e.g. OpenRouter attribution)
	keyEnv  string            // env var name, for error messages
}

// isRemoteProvider reports whether id is one of the hosted OpenAI-compatible
// backends (everything except Ollama, which is local, and Anthropic, which has
// its own native backend — see anthropic.go).
func isRemoteProvider(id string) bool {
	switch id {
	case provider.OpenAI, provider.OpenRouter, provider.Gemini, provider.DeepSeek,
		provider.Groq, provider.XAI, provider.Mistral, provider.Together, provider.Fireworks,
		provider.Moonshot, provider.ZAI, provider.DashScope, provider.NVIDIA, provider.HuggingFace, provider.Venice, provider.OmniRoute:
		return true
	}
	return false
}

func (c *Client) usesRemote() bool {
	return c.cfg != nil && isRemoteProvider(provider.Normalize(c.cfg.LLMProvider))
}

// remoteBackend returns the connection details for the active hosted backend.
// Only valid when usesRemote().
func (c *Client) remoteBackend() remoteBackend {
	id := provider.Ollama
	if c.cfg != nil {
		id = provider.Normalize(c.cfg.LLMProvider)
	}
	switch id {
	case provider.Moonshot:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.MoonshotBaseURL, "https://api.moonshot.ai/v1"), apiKey: strings.TrimSpace(c.cfg.MoonshotAPIKey), keyEnv: "MOONY_MOONSHOT_API_KEY"}
	case provider.ZAI:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.ZAIBaseURL, "https://api.z.ai/api/paas/v4"), apiKey: strings.TrimSpace(c.cfg.ZAIAPIKey), keyEnv: "MOONY_ZAI_API_KEY"}
	case provider.DashScope:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.DashScopeBaseURL, "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"), apiKey: strings.TrimSpace(c.cfg.DashScopeAPIKey), keyEnv: "MOONY_DASHSCOPE_API_KEY"}
	case provider.NVIDIA:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.NvidiaBaseURL, "https://integrate.api.nvidia.com/v1"), apiKey: strings.TrimSpace(c.cfg.NvidiaAPIKey), keyEnv: "MOONY_NVIDIA_API_KEY"}
	case provider.HuggingFace:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.HuggingFaceBaseURL, "https://router.huggingface.co/v1"), apiKey: strings.TrimSpace(c.cfg.HuggingFaceAPIKey), keyEnv: "MOONY_HUGGINGFACE_API_KEY"}
	case provider.Venice:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.VeniceBaseURL, "https://api.venice.ai/api/v1"), apiKey: strings.TrimSpace(c.cfg.VeniceAPIKey), keyEnv: "MOONY_VENICE_API_KEY"}
	case provider.OmniRoute:
		return remoteBackend{id: id, baseURL: firstNonEmpty(c.cfg.OmniRouteBaseURL, "http://localhost:20128/v1"), apiKey: strings.TrimSpace(c.cfg.OmniRouteAPIKey), keyEnv: "MOONY_OMNIROUTE_API_KEY"}
	case provider.OpenRouter:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.OpenRouterBaseURL, "https://openrouter.ai/api/v1"),
			apiKey:  strings.TrimSpace(c.cfg.OpenRouterAPIKey),
			keyEnv:  "MOONY_OPENROUTER_API_KEY",
			extra: map[string]string{
				"HTTP-Referer": "https://github.com/pysty1995/moony",
				"X-Title":      "moony",
			},
		}
	case provider.Gemini:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.GeminiBaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			apiKey:  strings.TrimSpace(c.cfg.GeminiAPIKey),
			keyEnv:  "MOONY_GEMINI_API_KEY",
		}
	case provider.DeepSeek:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.DeepSeekBaseURL, "https://api.deepseek.com"),
			apiKey:  strings.TrimSpace(c.cfg.DeepSeekAPIKey),
			keyEnv:  "MOONY_DEEPSEEK_API_KEY",
		}
	case provider.Groq:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.GroqBaseURL, "https://api.groq.com/openai/v1"),
			apiKey:  strings.TrimSpace(c.cfg.GroqAPIKey),
			keyEnv:  "MOONY_GROQ_API_KEY",
		}
	case provider.XAI:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.XAIBaseURL, "https://api.x.ai/v1"),
			apiKey:  strings.TrimSpace(c.cfg.XAIAPIKey),
			keyEnv:  "MOONY_XAI_API_KEY",
		}
	case provider.Mistral:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.MistralBaseURL, "https://api.mistral.ai/v1"),
			apiKey:  strings.TrimSpace(c.cfg.MistralAPIKey),
			keyEnv:  "MOONY_MISTRAL_API_KEY",
		}
	case provider.Together:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.TogetherBaseURL, "https://api.together.xyz/v1"),
			apiKey:  strings.TrimSpace(c.cfg.TogetherAPIKey),
			keyEnv:  "MOONY_TOGETHER_API_KEY",
		}
	case provider.Fireworks:
		return remoteBackend{
			id:      id,
			baseURL: firstNonEmpty(c.cfg.FireworksBaseURL, "https://api.fireworks.ai/inference/v1"),
			apiKey:  strings.TrimSpace(c.cfg.FireworksAPIKey),
			keyEnv:  "MOONY_FIREWORKS_API_KEY",
		}
	default: // provider.OpenAI
		return remoteBackend{
			id:      provider.OpenAI,
			baseURL: firstNonEmpty(c.cfg.OpenAIBaseURL, "https://api.openai.com/v1"),
			apiKey:  strings.TrimSpace(c.cfg.OpenAIAPIKey),
			keyEnv:  "MOONY_OPENAI_API_KEY",
		}
	}
}

// firstNonEmpty returns the first non-blank string, trimmed and with any
// trailing slash removed.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			return v
		}
	}
	return ""
}

func (c *Client) resolveRemoteModel(ctx context.Context, rb remoteBackend) string {
	if m := modelFrom(ctx); m != "" {
		return m
	}
	if c.cfg != nil && c.cfg.Model != "" {
		return c.cfg.Model
	}
	return provider.DefaultModelFor(rb.id)
}

// openAIEffort maps a reasoning level to OpenAI/OpenRouter's reasoning_effort
// values (they accept low/medium/high).
func openAIEffort(level string) string {
	switch config.NormalizeReasoning(level) {
	case config.ReasoningMinimal, config.ReasoningLow:
		return "low"
	case config.ReasoningHigh, config.ReasoningXHigh, config.ReasoningMax:
		return "high"
	default:
		return "medium"
	}
}

// streamRemote runs one turn against a hosted OpenAI-compatible backend.
func (c *Client) streamRemote(ctx context.Context, rb remoteBackend, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	if rb.apiKey == "" {
		return nil, fmt.Errorf("%s API key not set — set %s", provider.Label(rb.id), rb.keyEnv)
	}
	model := c.resolveRemoteModel(ctx, rb)

	level := effortFrom(ctx)
	if level == "" && c.cfg != nil {
		level = c.cfg.ReasoningLevel
	}

	req := oaiRequest{
		Model:         model,
		Messages:      toOpenAIMessages(system, messages),
		Tools:         toOpenAITools(tools),
		Stream:        true,
		StreamOptions: &oaiStreamOptions{IncludeUsage: true},
	}
	// OpenAI reasoning models reject max_tokens (they use max_completion_tokens
	// and size their own budget), so omit it there; otherwise cap the response.
	reasoningModel := rb.id == provider.OpenAI && config.IsOpenAIReasoningModel(model)
	if c.cfg != nil && c.cfg.MaxTokens > 0 && !reasoningModel {
		req.MaxTokens = c.cfg.MaxTokens
	}
	if config.ReasoningActive(rb.id, model, level) {
		req.ReasoningEffort = openAIEffort(level)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	dbg(">>> %s request: %s", rb.id, raw)

	url := rb.baseURL + "/chat/completions"
	newReq := func() (*http.Request, error) { return c.newRemoteRequest(ctx, rb, url, raw) }
	return c.streamWithRetry(ctx, newReq, rb.id, parseOpenAIStream, h)
}

func (c *Client) newRemoteRequest(ctx context.Context, rb remoteBackend, url string, raw []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Authorization", "Bearer "+rb.apiKey)
	for k, v := range rb.extra {
		req.Header.Set(k, v)
	}
	return req, nil
}

// checkRemoteAuth verifies the API key by listing models.
func (c *Client) checkRemoteAuth(ctx context.Context, rb remoteBackend) error {
	if rb.apiKey == "" {
		return fmt.Errorf("%s API key not set — set %s", provider.Label(rb.id), rb.keyEnv)
	}
	if _, err := c.listRemoteModels(ctx, rb); err != nil {
		return fmt.Errorf("cannot reach %s: %w", provider.Label(rb.id), err)
	}
	return nil
}

// listRemoteModels returns the models available on a hosted OpenAI-compatible
// backend (GET {base}/models).
func (c *Client) listRemoteModels(ctx context.Context, rb remoteBackend) ([]config.ModelInfo, error) {
	if rb.apiKey == "" {
		return nil, fmt.Errorf("%s API key not set", provider.Label(rb.id))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rb.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+rb.apiKey)
	req.Header.Set("User-Agent", userAgent())
	for k, v := range rb.extra {
		req.Header.Set(k, v)
	}
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
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]config.ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, config.ModelInfo{ID: m.ID, Name: name})
	}
	return models, nil
}
