package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bowEmbedder is a deterministic bag-of-words embedder: each token maps to a
// fixed dimension, so cosine similarity reflects token overlap. No network — it
// lets us test the indexer + vector store end to end.
type bowEmbedder struct{ calls int }

func (f *bowEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, 64)
		for _, tok := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
		}) {
			var h uint32 = 2166136261
			for _, c := range tok {
				h = (h ^ uint32(c)) * 16777619
			}
			vec[h%64]++
		}
		out[i] = vec
	}
	return out, nil
}

func newSemanticTool(t *testing.T, files map[string]string) (*semanticSearchTool, *bowEmbedder) {
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
	r := NewRegistry(dir)
	emb := &bowEmbedder{}
	r.SetEmbedder(emb)
	tool := &semanticSearchTool{
		r:         r,
		storePath: filepath.Join(t.TempDir(), "index.gob"),
		model:     "fake",
	}
	return tool, emb
}

func runSemantic(t *testing.T, tool *semanticSearchTool, args map[string]any) string {
	t.Helper()
	in, _ := json.Marshal(args)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("semantic_search: %v", err)
	}
	return out
}

func TestSemanticSearchRanksByMeaning(t *testing.T) {
	tool, _ := newSemanticTool(t, map[string]string{
		"math.go": "package math\n\n// Add returns the sum of two integer numbers.\nfunc Add(a, b int) int { return a + b }\n",
		"http.go": "package net\n\n// fetchURL downloads the body of a remote http request over the network.\nfunc fetchURL(u string) ([]byte, error) { return nil, nil }\n",
	})

	out := runSemantic(t, tool, map[string]any{"query": "sum two integer numbers add", "limit": 2})
	// The first ranked hit should be the math file.
	firstHit := ""
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "1. ") {
			firstHit = ln
			break
		}
	}
	if !strings.Contains(firstHit, "math.go") {
		t.Fatalf("expected math.go ranked first, got:\n%s", out)
	}
}

func TestSemanticSearchIncrementalIndex(t *testing.T) {
	tool, emb := newSemanticTool(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package b\nfunc B() {}\n",
	})

	// First call indexes both files (2 embed calls: 1 batch for the workspace
	// + 1 for the query).
	out := runSemantic(t, tool, map[string]any{"query": "func"})
	if !strings.Contains(out, "2 files") {
		t.Fatalf("first index should cover 2 files, got:\n%s", out)
	}
	if !strings.Contains(out, "re-embedded just now") {
		t.Fatalf("first call should re-embed files, got:\n%s", out)
	}
	callsAfterFirst := emb.calls

	// Second call with no changes: nothing re-embedded (only the query embed).
	out = runSemantic(t, tool, map[string]any{"query": "func"})
	if strings.Contains(out, "re-embedded just now") {
		t.Fatalf("unchanged workspace should not re-embed, got:\n%s", out)
	}
	if emb.calls != callsAfterFirst+1 {
		t.Fatalf("expected exactly one query embed on 2nd call, delta=%d", emb.calls-callsAfterFirst)
	}

	// Modify a file: only that file is re-embedded.
	if err := os.WriteFile(filepath.Join(tool.r.root, "a.go"), []byte("package a\nfunc A() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runSemantic(t, tool, map[string]any{"query": "func"})
	if !strings.Contains(out, "1 re-embedded just now") {
		t.Fatalf("expected 1 file re-embedded after edit, got:\n%s", out)
	}
}

func TestSemanticSearchPathFilter(t *testing.T) {
	tool, _ := newSemanticTool(t, map[string]string{
		"pkg/one.go": "package one\nfunc One() {}\n",
		"cmd/two.go": "package main\nfunc main() {}\n",
	})
	out := runSemantic(t, tool, map[string]any{"query": "func", "path": "cmd"})
	if strings.Contains(out, "pkg/one.go") {
		t.Fatalf("path filter should exclude pkg/, got:\n%s", out)
	}
	if !strings.Contains(out, "cmd/two.go") {
		t.Fatalf("path filter should include cmd/, got:\n%s", out)
	}
}

func TestSemanticSearchRequiresEmbedder(t *testing.T) {
	dir := t.TempDir()
	tool := &semanticSearchTool{r: NewRegistry(dir), storePath: filepath.Join(dir, "i.gob"), model: "fake"}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected an error when no embedder is configured")
	}
}
