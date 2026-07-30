package rag

import (
	"path/filepath"
	"testing"
)

func TestStoreSearchAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.gob")

	s, err := Open(path, "model-a")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Put("a.go", "h1", []Chunk{{Path: "a.go", Start: 1, End: 5, Text: "alpha", Vec: []float32{1, 0, 0}}})
	s.Put("b.go", "h2", []Chunk{{Path: "b.go", Start: 1, End: 5, Text: "beta", Vec: []float32{0, 1, 0}}})

	// A query aligned with the first vector should rank a.go first.
	hits := s.Search([]float32{0.9, 0.1, 0}, 2)
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	if hits[0].Path != "a.go" {
		t.Fatalf("want a.go first, got %s (score %.3f)", hits[0].Path, hits[0].Score)
	}

	if f, c := s.Stats(); f != 2 || c != 2 {
		t.Fatalf("stats = (%d,%d), want (2,2)", f, c)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reopen with the same model: contents persist.
	s2, err := Open(path, "model-a")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if f, _ := s2.Stats(); f != 2 {
		t.Fatalf("persisted files = %d, want 2", f)
	}
	if h, ok := s2.Hash("a.go"); !ok || h != "h1" {
		t.Fatalf("hash a.go = %q,%v; want h1,true", h, ok)
	}

	// Reopen with a different model: index is invalidated (empty).
	s3, err := Open(path, "model-b")
	if err != nil {
		t.Fatalf("reopen model-b: %v", err)
	}
	if f, _ := s3.Stats(); f != 0 {
		t.Fatalf("model mismatch should invalidate, got %d files", f)
	}
}

func TestStorePrune(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "i.gob"), "m")
	s.Put("keep.go", "h", []Chunk{{Path: "keep.go", Vec: []float32{1, 0}}})
	s.Put("drop.go", "h", []Chunk{{Path: "drop.go", Vec: []float32{0, 1}}})
	if n := s.Prune(map[string]bool{"keep.go": true}); n != 1 {
		t.Fatalf("prune removed %d, want 1", n)
	}
	if _, ok := s.Hash("drop.go"); ok {
		t.Fatalf("drop.go should have been pruned")
	}
	if _, ok := s.Hash("keep.go"); !ok {
		t.Fatalf("keep.go should remain")
	}
}
