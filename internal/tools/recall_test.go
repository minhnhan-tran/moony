package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRankNotesBM25(t *testing.T) {
	notes := []MemoryNote{
		{Section: "Rules", Text: "Always run make build and go vet before committing"},
		{Section: "Conventions", Text: "Use tabs for indentation in Go files"},
		{Section: "Rules", Text: "The web UI changes need the relay rebuilt, not just the connector"},
		{Section: "Notes", Text: "Prefer table-driven tests"},
	}

	ranked := rankNotesBM25("rebuild the relay for web ui", notes, 5)
	if len(ranked) == 0 {
		t.Fatal("expected at least one match")
	}
	if !strings.Contains(ranked[0].note.Text, "relay rebuilt") {
		t.Errorf("top match = %q, want the relay note", ranked[0].note.Text)
	}

	// Query with no overlap returns nothing.
	if got := rankNotesBM25("kubernetes helm chart", notes, 5); len(got) != 0 {
		t.Errorf("expected no matches, got %+v", got)
	}

	// Limit is honored.
	if got := rankNotesBM25("go tests build", notes, 1); len(got) > 1 {
		t.Errorf("limit not honored: %d results", len(got))
	}
}

func TestRecallToolExecute(t *testing.T) {
	sink := NewMemorySink()
	sink.SetList(func() []MemoryNote {
		return []MemoryNote{
			{Section: "Rules", Text: "Bump config.Version via make release"},
			{Section: "Notes", Text: "The TUI uses bubbletea"},
		}
	})
	tool := &recallTool{sink: sink}

	in, _ := json.Marshal(map[string]any{"query": "release version bump"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "make release") {
		t.Errorf("recall output = %q", out)
	}

	// Empty query is rejected.
	bad, _ := json.Marshal(map[string]any{"query": "  "})
	if _, err := tool.Execute(context.Background(), bad); err == nil {
		t.Error("empty query should error")
	}

	// Unwired sink reports unavailable.
	empty := &recallTool{sink: NewMemorySink()}
	got, err := empty.Execute(context.Background(), in)
	if err != nil || !strings.Contains(got, "not available") {
		t.Errorf("unwired recall = %q (err %v)", got, err)
	}
}

// TestRecallSemanticRerank mirrors TestSearchCodeSemanticRerankFlipsOrder: a
// BM25-losing candidate that carries the fakeEmbedder's marker should outrank
// a lexically-stronger but unmarked one once an embedder is configured, and
// BM25 order must be untouched without one.
func TestRecallSemanticRerank(t *testing.T) {
	notes := []MemoryNote{
		// Higher lexical frequency of the query term -> ranks first under BM25.
		{Section: "Notes", Text: "database database database plan"},
		// Lower frequency, but carries the semantic marker.
		{Section: "Notes", Text: "database MARKER rollback"},
	}
	query := "database"

	// Control: no embedder configured -> pure BM25 order preserved.
	noEmbed := &recallTool{r: &Registry{}}
	cand := rankNotesBM25(query, notes, recallRerankCandidates)
	if len(cand) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cand))
	}
	if !strings.Contains(cand[0].note.Text, "database database database") {
		t.Fatalf("BM25 baseline expected the high-frequency note first, got %q", cand[0].note.Text)
	}
	noEmbed.semanticRerank(context.Background(), query, cand)
	if !strings.Contains(cand[0].note.Text, "database database database") {
		t.Errorf("no embedder configured should leave BM25 order intact, got %q first", cand[0].note.Text)
	}

	// With a semantic embedder favoring the marker note, rerank should flip it
	// to first despite losing on pure BM25.
	r := &Registry{}
	r.SetEmbedder(fakeEmbedder{marker: "MARKER"})
	withEmbed := &recallTool{r: r}
	cand2 := rankNotesBM25(query, notes, recallRerankCandidates)
	withEmbed.semanticRerank(context.Background(), query, cand2)
	if !strings.Contains(cand2[0].note.Text, "MARKER") {
		t.Errorf("semantic rerank expected the marked note first, got %q", cand2[0].note.Text)
	}
}

// TestRecallRerankNoopOnEmbedError mirrors TestSearchCodeRerankNoopOnEmbedError.
func TestRecallRerankNoopOnEmbedError(t *testing.T) {
	notes := []MemoryNote{
		{Section: "Notes", Text: "widget one"},
		{Section: "Notes", Text: "widget two three"},
	}
	r := &Registry{}
	r.SetEmbedder(badEmbedder{})
	tool := &recallTool{r: r}
	cand := rankNotesBM25("widget", notes, recallRerankCandidates)
	tool.semanticRerank(context.Background(), "widget", cand) // must not panic or corrupt cand
	if len(cand) != 2 {
		t.Errorf("expected candidates to survive an embed error, got %d", len(cand))
	}
}
