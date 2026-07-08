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
)

// Embed returns an embedding vector for each input string using the given
// model, via Ollama's OpenAI-compatible {host}/v1/embeddings endpoint. Vectors
// come back in input order; a nil slice at position i means the server returned
// no embedding for it. Embeddings power the optional hybrid semantic reranking
// in the search_code tool.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("embeddings model not configured")
	}
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
	if tok := strings.TrimSpace(os.Getenv("SQE_OLLAMA_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.http.Do(req)
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

	out := make([][]float32, len(inputs))
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
