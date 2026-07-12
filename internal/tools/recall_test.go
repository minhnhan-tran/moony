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
	if !strings.Contains(ranked[0].Text, "relay rebuilt") {
		t.Errorf("top match = %q, want the relay note", ranked[0].Text)
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
