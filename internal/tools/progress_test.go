package tools

import (
	"context"
	"testing"
)

// TestEmbedBatchedReportsProgress verifies embedBatched reports a progress
// line after every batch (not just at the end), and tolerates a nil progress
// func, so a long first-time semantic_search reindex doesn't look hung.
func TestEmbedBatchedReportsProgress(t *testing.T) {
	texts := make([]string, 5)
	for i := range texts {
		texts[i] = "chunk"
	}
	var lines []string
	out, err := embedBatched(context.Background(), fakeEmbedder{}, texts, 2, func(s string) {
		lines = append(lines, s)
	})
	if err != nil {
		t.Fatalf("embedBatched: %v", err)
	}
	if len(out) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(out), len(texts))
	}
	if len(lines) != 3 { // ceil(5/2) batches
		t.Fatalf("got %d progress lines, want 3: %v", len(lines), lines)
	}
	if lines[len(lines)-1] == "" {
		t.Error("expected a non-empty final progress line")
	}

	// nil progress must not panic.
	if _, err := embedBatched(context.Background(), fakeEmbedder{}, texts, 2, nil); err != nil {
		t.Fatalf("embedBatched with nil progress: %v", err)
	}
}

// TestProgressFromDefaultsToNoop verifies ProgressFrom never returns nil, and
// that WithProgress round-trips a real emitter.
func TestProgressFromDefaultsToNoop(t *testing.T) {
	if fn := ProgressFrom(context.Background()); fn == nil {
		t.Fatal("ProgressFrom must never return nil")
	}
	ProgressFrom(context.Background())("must not panic") // no-op call

	var got string
	ctx := WithProgress(context.Background(), func(s string) { got = s })
	ProgressFrom(ctx)("hello")
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}
