package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEmbedder returns a 2-D vector per input: the query and any text containing
// the marker point one way ([1,0]); everything else points the opposite way
// ([-1,0]). So a chunk carrying the marker has cosine +1 to the query and the
// rest have -1, letting a test force a semantic flip.
type fakeEmbedder struct{ marker string }

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, tx := range texts {
		if i == 0 || strings.Contains(tx, f.marker) {
			out[i] = []float32{1, 0}
		} else {
			out[i] = []float32{-1, 0}
		}
	}
	return out, nil
}

func rerankReg(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewRegistry(dir)
}

func TestSearchCodeSemanticRerankFlipsOrder(t *testing.T) {
	files := map[string]string{
		// Higher lexical frequency of the query term -> ranks first under BM25.
		"a_file.go": "package a\n\n// widget widget widget helper here\nfunc AWidget() {}\n",
		// Lower frequency, but carries the semantic marker.
		"b_file.go": "package b\n\n// widget MARKER relevant\nfunc BWidget() {}\n",
	}
	r := rerankReg(t, files)

	idx := func(out, s string) int { return strings.Index(out, s) }

	// Control: no embedder -> pure BM25, a_file.go first.
	base := &searchCodeTool{r: r}
	in, _ := json.Marshal(map[string]any{"query": "widget"})
	out, err := base.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if ai, bi := idx(out, "a_file.go"), idx(out, "b_file.go"); ai < 0 || bi < 0 || ai > bi {
		t.Fatalf("BM25 baseline expected a_file.go before b_file.go:\n%s", out)
	}

	// With a semantic embedder favoring the marker chunk -> b_file.go first.
	r.SetEmbedder(fakeEmbedder{marker: "MARKER"})
	out, err = base.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if ai, bi := idx(out, "a_file.go"), idx(out, "b_file.go"); ai < 0 || bi < 0 || bi > ai {
		t.Fatalf("semantic rerank expected b_file.go before a_file.go:\n%s", out)
	}
}

func TestSearchCodeRerankNoopOnEmbedError(t *testing.T) {
	r := rerankReg(t, map[string]string{
		"x.go": "package x\n// widget one\nfunc X() {}\n",
		"y.go": "package y\n// widget two three\nfunc Y() {}\n",
	})
	// Embedder returns wrong-sized output -> rerank must silently keep BM25.
	r.SetEmbedder(badEmbedder{})
	tool := &searchCodeTool{r: r}
	in, _ := json.Marshal(map[string]any{"query": "widget"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatalf("rerank error should not surface: %v", err)
	}
}

type badEmbedder struct{}

func (badEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return nil, context.DeadlineExceeded
}
