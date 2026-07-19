package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// TestEmbedOllamaDefault verifies the historical default: EmbeddingsProvider
// unset routes to Ollama's native /embeddings endpoint.
func TestEmbedOllamaDefault(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`)
	}))
	defer srv.Close()

	c := &Client{cfg: &config.Config{OllamaBaseURL: srv.URL + "/v1"}, http: srv.Client()}
	vecs, err := c.Embed(context.Background(), "nomic-embed-text", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 2 || vecs[1][0] != 0.3 {
		t.Fatalf("unexpected vectors: %+v", vecs)
	}
	if gotBody["model"] != "nomic-embed-text" {
		t.Errorf("request model = %v", gotBody["model"])
	}
}

// TestEmbedRemoteProvider verifies EmbeddingsProvider routes through
// remoteBackendFor (baseURL/apiKey/auth header) instead of Ollama, reusing the
// same remote-provider plumbing chat completions use.
func TestEmbedRemoteProvider(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer srv.Close()

	c := &Client{
		cfg: &config.Config{
			EmbeddingsProvider: provider.Groq,
			GroqBaseURL:        srv.URL,
			GroqAPIKey:         "test-groq-key",
		},
		http: srv.Client(),
	}
	vecs, err := c.Embed(context.Background(), "some-embed-model", []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || vecs[0][1] != 2 {
		t.Fatalf("unexpected vectors: %+v", vecs)
	}
	if gotAuth != "Bearer test-groq-key" {
		t.Errorf("Authorization header = %q, want Bearer test-groq-key", gotAuth)
	}
}

// TestEmbedAnthropicRejected verifies Anthropic (no embeddings API) is
// rejected with a clear error and no HTTP call is attempted.
func TestEmbedAnthropicRejected(t *testing.T) {
	c := &Client{cfg: &config.Config{EmbeddingsProvider: provider.Anthropic}, http: http.DefaultClient}
	_, err := c.Embed(context.Background(), "some-model", []string{"x"})
	if err == nil {
		t.Fatal("expected an error for Anthropic")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no embeddings api") {
		t.Errorf("error should explain Anthropic has no embeddings API, got: %v", err)
	}
}

// TestEmbedRemoteProviderMissingAPIKey verifies a clear, actionable error
// (naming the env var) rather than an opaque HTTP failure.
func TestEmbedRemoteProviderMissingAPIKey(t *testing.T) {
	c := &Client{cfg: &config.Config{EmbeddingsProvider: provider.Groq}, http: http.DefaultClient}
	_, err := c.Embed(context.Background(), "some-model", []string{"x"})
	if err == nil {
		t.Fatal("expected an error for a missing API key")
	}
	if !strings.Contains(err.Error(), "MN_GROQ_API_KEY") {
		t.Errorf("error should name the missing key env var, got: %v", err)
	}
}

// TestEmbedNoModelConfigured verifies Embed refuses to run without a model,
// regardless of provider.
func TestEmbedNoModelConfigured(t *testing.T) {
	c := &Client{cfg: &config.Config{}, http: http.DefaultClient}
	if _, err := c.Embed(context.Background(), "", []string{"x"}); err == nil {
		t.Error("expected an error when no model is configured")
	}
}

// TestEmbedEmptyInputsIsNoop verifies Embed short-circuits with no error and
// no HTTP call for an empty input slice.
func TestEmbedEmptyInputsIsNoop(t *testing.T) {
	c := &Client{cfg: &config.Config{OllamaBaseURL: "http://127.0.0.1:1"}, http: http.DefaultClient}
	vecs, err := c.Embed(context.Background(), "model", nil)
	if err != nil || vecs != nil {
		t.Errorf("Embed(nil) = %v, %v; want nil, nil", vecs, err)
	}
}
