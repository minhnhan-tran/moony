package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pysty1995/moony/internal/provider"
)

// Embed returns an embedding vector for each input string using the given
// model. Vectors come back in input order; a nil slice at position i means
// the server returned no embedding for it. Embeddings power the optional
// hybrid semantic reranking in the search_code and recall tools.
//
// The backend is independent of the session's main chat provider: it's
// Ollama's native {host}/v1/embeddings endpoint by default (unchanged from
// before), or c.cfg.EmbeddingsProvider's OpenAI-compatible {baseURL}/embeddings
// endpoint when that's set to something other than "ollama" — so a session
// running on e.g. Anthropic can still point embeddings at OpenAI without
// switching its main model. Anthropic has no embeddings API and is rejected
// with a clear error rather than silently misrouted.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("embeddings model not configured")
	}
	provID := ""
	if c.cfg != nil {
		provID = strings.TrimSpace(c.cfg.EmbeddingsProvider)
	}
	if provID != "" && !strings.EqualFold(provID, provider.Ollama) {
		return c.embedRemote(ctx, provID, model, inputs)
	}
	return c.embedOllama(ctx, model, inputs)
}

// embedOllama is the original, Ollama-native embeddings call (the default
// when EmbeddingsProvider is unset).
func (c *Client) embedOllama(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	reqBody, err := json.Marshal(map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	url := c.ollamaBaseURL() + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if tok := strings.TrimSpace(os.Getenv("MN_OLLAMA_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return doEmbedRequest(c.http, req, len(inputs))
}

// embedRemote requests embeddings from a hosted OpenAI-compatible backend,
// reusing the same remoteBackendFor/newRemoteRequest plumbing chat completions
// use — one code path covers OpenAI, OpenRouter, Gemini, DeepSeek, Groq, xAI,
// Mistral, Together, Fireworks, NVIDIA NIM, HuggingFace, Venice, DashScope,
// Moonshot, Z.AI, and any other OpenAI-compatible id remoteBackendFor resolves.
func (c *Client) embedRemote(ctx context.Context, providerID, model string, inputs []string) ([][]float32, error) {
	id := provider.Normalize(providerID)
	if id == provider.Anthropic || id == provider.SQEAnthropic {
		return nil, fmt.Errorf("embeddings aren't available on %s (no embeddings API) — set MN_EMBEDDINGS_PROVIDER to a different provider, or unset it to use Ollama", provider.Label(id))
	}
	rb := c.remoteBackendFor(id)
	if rb.apiKey == "" {
		return nil, fmt.Errorf("%s API key not set — set %s", provider.Label(rb.id), rb.keyEnv)
	}
	reqBody, err := json.Marshal(map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	req, err := c.newRemoteRequest(ctx, rb, rb.baseURL+"/embeddings", reqBody)
	if err != nil {
		return nil, err
	}
	return doEmbedRequest(c.http, req, len(inputs))
}

// doEmbedRequest runs an already-built embeddings request and decodes the
// OpenAI-shaped response ({"data": [{"index", "embedding"}, ...]}), shared by
// both the Ollama-native and remote-provider paths.
func doEmbedRequest(httpClient *http.Client, req *http.Request, n int) ([][]float32, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse embeddings: %w", err)
	}

	out := make([][]float32, n)
	for _, d := range parsed.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	// Servers that omit "index" return data in request order; fill any gaps.
	for i := range out {
		if out[i] == nil && i < len(parsed.Data) {
			out[i] = parsed.Data[i].Embedding
		}
	}
	return out, nil
}
