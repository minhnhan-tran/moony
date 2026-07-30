// Package rag implements a small, dependency-free local vector store for
// semantic (embedding-based) code search. It persists embedded source chunks to
// a gob file and answers nearest-neighbour queries with brute-force cosine
// similarity — no cgo/SQLite, so it cross-compiles cleanly like the rest of the
// agent. It is sized for a single workspace (tens of thousands of chunks).
package rag

import (
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Chunk is a contiguous span of a source file together with its embedding.
type Chunk struct {
	Path  string // workspace-relative path
	Start int    // 1-based first line
	End   int    // 1-based last line (inclusive)
	Text  string // the chunk text (used for display/snippets)
	Vec   []float32
}

// fileEntry records everything stored for one file so unchanged files can be
// skipped on reindex (hash match) and deleted files can be pruned.
type fileEntry struct {
	Hash   string
	Chunks []Chunk
}

// persisted is the on-disk representation.
type persisted struct {
	Model string
	Files map[string]*fileEntry
}

// Store is a persistent, in-memory vector index guarded by a mutex.
type Store struct {
	path  string
	model string

	mu    sync.Mutex
	files map[string]*fileEntry
	dirty bool
}

// Open loads the index at path if it exists and was built with the same
// embedding model; otherwise it returns a fresh, empty store. A model mismatch
// discards the old vectors (they aren't comparable across models).
func Open(path, model string) (*Store, error) {
	s := &Store{path: path, model: model, files: map[string]*fileEntry{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, nil // corrupt/unreadable index is non-fatal; start empty
	}
	defer f.Close()
	var p persisted
	if err := gob.NewDecoder(f).Decode(&p); err != nil {
		return s, nil // ignore a corrupt index
	}
	if p.Model != model || p.Files == nil {
		return s, nil
	}
	s.files = p.Files
	return s, nil
}

// Hash returns the stored content hash for a file, if indexed.
func (s *Store) Hash(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.files[path]; ok {
		return e.Hash, true
	}
	return "", false
}

// Put replaces the chunks stored for a file.
func (s *Store) Put(path, hash string, chunks []Chunk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = &fileEntry{Hash: hash, Chunks: chunks}
	s.dirty = true
}

// Prune removes indexed files whose path is not present in keep (i.e. files
// that were deleted or are no longer eligible). Returns how many were removed.
func (s *Store) Prune(keep map[string]bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for p := range s.files {
		if !keep[p] {
			delete(s.files, p)
			s.dirty = true
			n++
		}
	}
	return n
}

// Stats reports the number of indexed files and chunks.
func (s *Store) Stats() (files, chunks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files = len(s.files)
	for _, e := range s.files {
		chunks += len(e.Chunks)
	}
	return files, chunks
}

// Result is a scored search hit.
type Result struct {
	Chunk
	Score float32
}

// Search returns the top-k chunks by cosine similarity to vec.
func (s *Store) Search(vec []float32, k int) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k <= 0 {
		k = 8
	}
	qn := norm(vec)
	if qn == 0 {
		return nil
	}
	var out []Result
	for _, e := range s.files {
		for _, c := range e.Chunks {
			cn := norm(c.Vec)
			if cn == 0 {
				continue
			}
			score := dot(vec, c.Vec) / (qn * cn)
			out = append(out, Result{Chunk: c, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Save writes the index to disk if it has changed since the last save.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(persisted{Model: s.model, Files: s.files}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func dot(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var s float32
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return s
}

func norm(a []float32) float32 {
	var s float32
	for _, v := range a {
		s += v * v
	}
	return float32(math.Sqrt(float64(s)))
}

// ContentHash is a stable hash of file bytes for change detection. Exposed so
// the indexer and store agree on the hashing scheme.
func ContentHash(data []byte) string {
	// FNV-1a 64-bit — fast and good enough for change detection.
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for _, b := range data {
		h ^= uint64(b)
		h *= prime64
	}
	return fmt.Sprintf("%016x", h)
}
