package llm

// Ollama backend: talks to a local/remote Ollama server through its NATIVE chat
// API (POST <host>/api/chat). The native API (not the OpenAI-compatible /v1) is
// used because only it honors runtime options — notably options.num_ctx, which
// moony must raise so its large system prompt + tool schemas don't overflow the
// model's default context. The request/response wire shapes and NDJSON stream
// parser live in ollama_native.go. Config: the base URL (config.OllamaBaseURL,
// from MN_OLLAMA_HOST / OLLAMA_HOST; its "/v1" suffix is stripped for the
// native path) and an optional bearer token via MN_OLLAMA_TOKEN.
//
// Tool-name translation to a wire-safe form is handled by the caller
// (StreamMessages), so this file deals only in already-translated names.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// usesOllama reports whether the selected backend provider is Ollama, in which
// case requests route through streamOllama instead of the hosted-provider paths.
func (c *Client) usesOllama() bool {
	return c.cfg != nil && provider.Normalize(c.cfg.LLMProvider) == provider.Ollama
}

// ollamaBaseURL returns the configured Ollama OpenAI-compatible base URL (incl.
// the "/v1" segment), falling back to the local default.
func (c *Client) ollamaBaseURL() string {
	if c.cfg != nil && strings.TrimSpace(c.cfg.OllamaBaseURL) != "" {
		return strings.TrimRight(c.cfg.OllamaBaseURL, "/")
	}
	return "http://localhost:11434/v1"
}

// resolveOllamaModel picks the model id to send: a per-call override on ctx wins,
// else the session model. Ollama model ids are free-form (e.g. "gemma4:31b"). A
// per-call override carrying an unmistakable cloud id (anthropic./gcp:/aws:
// prefix) is ignored — the Ollama server has no such model and would 404 — so we
// fall back to the session model. This guards against any caller that passes a
// cloud id (e.g. a fast-model pick) while the backend is Ollama.
func (c *Client) resolveOllamaModel(ctx context.Context) string {
	if m := modelFrom(ctx); m != "" && !IsCloudModel(m) {
		return m
	}
	if c.cfg != nil {
		return c.cfg.Model
	}
	return ""
}

// IsCloudModel reports whether m has a hosted/cloud model-id prefix
// (anthropic./gcp:/aws:) that could never be a local Ollama model name (which
// uses "repo:tag" form). Exported so callers (e.g. sub-agent model selection)
// can avoid pinning a cloud id onto a local backend.
func IsCloudModel(m string) bool {
	m = strings.ToLower(strings.TrimSpace(m))
	return strings.HasPrefix(m, "anthropic.") || strings.HasPrefix(m, "gcp:") || strings.HasPrefix(m, "aws:")
}

// ollamaOptions carries Ollama runtime options (native /api/chat). num_ctx sets
// the context window the model is (re)loaded with; num_predict caps output tokens.
type ollamaOptions struct {
	NumCtx     int `json:"num_ctx,omitempty"`
	NumPredict int `json:"num_predict,omitempty"`
}

// defaultOllamaNumCtx is the context window moony asks Ollama to load the model
// with. moony's system prompt + tool schemas run several thousand tokens, so the
// common Ollama default (num_ctx=4096) overflows and truncates the prompt —
// which drops the model's thinking output and often halts generation after a
// token or two. Requesting a larger window makes Ollama reload the model with
// room to actually respond.
const defaultOllamaNumCtx = 32768

// ollamaNumCtx is the context window sent as options.num_ctx. Override with
// MN_OLLAMA_NUM_CTX; set it to 0 to omit the option and inherit whatever
// context the server already loaded the model with.
func ollamaNumCtx() int {
	if v := strings.TrimSpace(os.Getenv("MN_OLLAMA_NUM_CTX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultOllamaNumCtx
}

// ollamaKeepAlive is how long the server should keep the model loaded after a
// request. Overridable via MN_OLLAMA_KEEP_ALIVE (e.g. "60m", "-1" for forever).
func ollamaKeepAlive() string {
	if v := strings.TrimSpace(os.Getenv("MN_OLLAMA_KEEP_ALIVE")); v != "" {
		return v
	}
	return "30m"
}

// userAgent is the HTTP User-Agent sent to the Ollama server.
func userAgent() string { return "moony/" + config.Version }

// streamOllama sends one turn to the Ollama server via its NATIVE /api/chat
// endpoint and assembles the streamed NDJSON reply into a Response. The native
// API (not the OpenAI-compatible /v1) is used so options.num_ctx is honored —
// see ollamaNumCtx. Thinking is always requested via the "think" flag so a
// thinking-capable model's reasoning shows whenever it produces any (a model that
// doesn't think ignores the flag).
func (c *Client) streamOllama(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	think := true
	body := ollamaNativeReq{
		Model:     c.resolveOllamaModel(ctx),
		Messages:  toOllamaMessages(system, messages),
		Tools:     toOpenAITools(tools),
		Stream:    true,
		Think:     &think,
		KeepAlive: ollamaKeepAlive(),
	}
	opts := ollamaOptions{}
	if n := ollamaNumCtx(); n > 0 {
		opts.NumCtx = n
	}
	if c.cfg != nil && c.cfg.MaxTokens > 0 {
		opts.NumPredict = c.cfg.MaxTokens
	}
	if opts != (ollamaOptions{}) {
		body.Options = &opts
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	dbg(">>> ollama native request: %s", raw)

	url := strings.TrimSuffix(c.ollamaBaseURL(), "/v1") + "/api/chat"
	newReq := func() (*http.Request, error) { return c.newOllamaRequest(ctx, url, raw) }
	return c.streamWithRetry(ctx, newReq, "ollama", parseOllamaNativeStream, h)
}

// newOllamaRequest builds a streaming POST to the Ollama server. Unlike the
// hosted-provider routes it uses no provider auth; an optional bearer
// token (MN_OLLAMA_TOKEN) is sent when set, for servers behind a proxy.
func (c *Client) newOllamaRequest(ctx context.Context, url string, raw []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson") // native /api/chat streams NDJSON, not SSE
	req.Header.Set("User-Agent", userAgent())
	if tok := strings.TrimSpace(os.Getenv("MN_OLLAMA_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

// ollamaTagsResponse is the shape of GET <host>/api/tags (the native Ollama model
// list). It lives outside the OpenAI surface, so it's fetched directly.
type ollamaTagsResponse struct {
	Models []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	} `json:"models"`
}

// listOllamaModels fetches the server's installed models via the native
// /api/tags endpoint (the host's base, with the "/v1" suffix stripped).
func (c *Client) listOllamaModels(ctx context.Context) ([]config.ModelInfo, error) {
	url := strings.TrimSuffix(c.ollamaBaseURL(), "/v1") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := strings.TrimSpace(os.Getenv("MN_OLLAMA_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags: %s: %s", resp.Status, strings.TrimSpace(string(bodyBytes)))
	}
	var parsed ollamaTagsResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("ollama: parse models: %w", err)
	}
	out := make([]config.ModelInfo, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := m.Model
		if id == "" {
			id = m.Name
		}
		if id == "" {
			continue
		}
		out = append(out, config.ModelInfo{ID: id, Name: m.Name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ollama: no models installed on %s", url)
	}
	return out, nil
}

// checkOllamaAuth verifies the Ollama server is reachable by listing its models.
// There's no real auth (it's a local/self-hosted server); this just surfaces a
// clear connectivity error in the doctor rather than failing on the first turn.
// It also warns when a loaded model is running with no GPU offload (size_vram=0),
// the usual cause of "Ollama never answers" — a large model on CPU is too slow to
// finish a turn within any reasonable timeout.
func (c *Client) checkOllamaAuth(ctx context.Context) error {
	if _, err := c.listOllamaModels(ctx); err != nil {
		return fmt.Errorf("cannot reach the Ollama server at %s — set MN_OLLAMA_HOST to your server (e.g. http://host:11434): %w",
			strings.TrimSuffix(c.ollamaBaseURL(), "/v1"), err)
	}
	if msg := c.ollamaLoadWarning(ctx); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// ollamaSmallContext is the loaded-context threshold below which the agent's
// system prompt + tool schemas overflow it. On overflow Ollama truncates the
// prompt, which silently drops "thinking" output (and degrades tool use). The
// agent's prompt with tools is well over 8k tokens, so warn under this.
const ollamaSmallContext = 16384

// ollamaPSModel is one entry from GET /api/ps — a model currently loaded
// (resident) on the server.
type ollamaPSModel struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ContextLength int    `json:"context_length"`
}

// ollamaPS fetches the server's currently-loaded models (GET /api/ps). ok is
// false when the call can't be made or fails — callers then skip their check
// rather than guess. A short timeout keeps it from delaying a turn.
func (c *Client) ollamaPS(ctx context.Context) ([]ollamaPSModel, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := strings.TrimSuffix(c.ollamaBaseURL(), "/v1") + "/api/ps"
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	if tok := strings.TrimSpace(os.Getenv("MN_OLLAMA_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var ps struct {
		Models []ollamaPSModel `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ps) != nil {
		return nil, false
	}
	return ps.Models, true
}

// ollamaLoadWarning returns a non-empty message about a loaded model that will
// behave badly: running on CPU (size_vram=0 → far too slow), or loaded with a
// context window too small for the agent's prompt+tools (→ truncation drops
// thinking and breaks tool calls). Returns "" when nothing is loaded yet or the
// model looks healthy.
func (c *Client) ollamaLoadWarning(ctx context.Context) string {
	ps, ok := c.ollamaPS(ctx)
	if !ok {
		return ""
	}
	for _, m := range ps {
		if m.Size > 0 && m.SizeVRAM == 0 {
			return fmt.Sprintf("Ollama model %q is loaded on CPU (no GPU offload: size_vram=0) — responses will be very slow and may time out. Use a smaller model or a GPU-backed server.", m.Name)
		}
		if m.ContextLength > 0 && m.ContextLength < ollamaSmallContext {
			return fmt.Sprintf("Ollama model %q is loaded with a small context window (%d tokens) — the agent's system prompt + tool schemas overflow it, which truncates the prompt and drops the model's thinking output (and can break tool calls). Reload the model with a larger context, e.g. set OLLAMA_CONTEXT_LENGTH=32768 on the server (or `/set parameter num_ctx 32768` in `ollama run`).", m.Name, m.ContextLength)
		}
	}
	return ""
}

// PreTurnNotice returns a one-line info message to show before a turn when the
// Ollama backend is active and the model the turn will use is NOT currently
// loaded on the server — so a slow first response is explained (Ollama loads the
// model into memory on first use) instead of looking like a hang. Returns "" when
// not on Ollama, when the model is already resident, or when the check can't run.
// Best-effort and quick; it never blocks the turn.
func (c *Client) PreTurnNotice(ctx context.Context) string {
	if !c.usesOllama() {
		return ""
	}
	model := c.resolveOllamaModel(ctx) // the model this turn will send
	if model == "" {
		return ""
	}
	ps, ok := c.ollamaPS(ctx)
	if !ok {
		return "" // can't tell whether it's loaded — don't guess
	}
	for _, m := range ps {
		if ollamaModelResident(m, model) {
			return "" // already loaded — the response will be prompt
		}
	}
	return fmt.Sprintf("Loading the Ollama model %q on the server — the first response may be slow while it loads into memory.", model)
}

// ollamaModelResident reports whether the loaded /api/ps entry m is the wanted
// model: an exact id match on either field, tolerating an untagged want (e.g.
// "gemma4" matching a resident "gemma4:latest").
func ollamaModelResident(m ollamaPSModel, want string) bool {
	want = strings.TrimSpace(want)
	for _, name := range []string{m.Model, m.Name} {
		if name == "" {
			continue
		}
		if name == want {
			return true
		}
		if !strings.Contains(want, ":") && strings.HasPrefix(name, want+":") {
			return true
		}
	}
	return false
}
