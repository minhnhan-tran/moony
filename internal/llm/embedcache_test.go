package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// TestEmbedCacheFlushPersistsPending verifies Flush() persists entries buffered
// below the auto-save threshold (the shutdown path), and is a no-op when clean.
func TestEmbedCacheFlushPersistsPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "embed.gob")
	calls := 0
	base := func(_ context.Context, in []string) ([][]float32, error) {
		calls++
		out := make([][]float32, len(in))
		for i := range in {
			out[i] = []float32{1, 2, 3}
		}
		return out, nil
	}

	c := NewEmbedCache(path, "m", base)
	// A single small embed stays below embedCacheSaveEvery → not auto-persisted.
	if _, err := c.Embed(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache should not have auto-saved below the threshold yet (err=%v)", err)
	}

	// Flush persists the pending entries (the shutdown path).
	c.Flush()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Flush should have persisted the cache: %v", err)
	}

	// A fresh cache loads them from disk — no re-embed.
	c2 := NewEmbedCache(path, "m", base)
	before := calls
	if _, err := c2.Embed(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Errorf("reload should serve from disk; base was re-called (%d → %d)", before, calls)
	}
	// Flush with nothing dirty is a no-op (no panic, no error path).
	c2.Flush()
}

func TestEmbedCacheHitsMissesAndPersistence(t *testing.T) {
	var calls [][]string // records the inputs of each base call
	base := func(_ context.Context, inputs []string) ([][]float32, error) {
		calls = append(calls, append([]string(nil), inputs...))
		out := make([][]float32, len(inputs))
		for i, in := range inputs {
			out[i] = []float32{float32(len(in)), 1.0} // deterministic per text
		}
		return out, nil
	}
	path := filepath.Join(t.TempDir(), "sub", "embed.gob")
	c := NewEmbedCache(path, "m1", base)
	ctx := context.Background()

	// First call: both are misses → one base call with both inputs.
	got, err := c.Embed(ctx, []string{"alpha", "bb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"alpha", "bb"}) {
		t.Fatalf("first call base inputs = %v", calls)
	}
	if !reflect.DeepEqual(got[0], []float32{5, 1}) || !reflect.DeepEqual(got[1], []float32{2, 1}) {
		t.Fatalf("unexpected vectors %v", got)
	}

	// Repeat: all cached → no new base call, order preserved.
	got2, _ := c.Embed(ctx, []string{"alpha", "bb"})
	if len(calls) != 1 {
		t.Fatalf("cached repeat should not call base again; calls=%d", len(calls))
	}
	if !reflect.DeepEqual(got2, got) {
		t.Fatalf("cached result mismatch: %v vs %v", got2, got)
	}

	// Partial: only the novel input is embedded.
	if _, err := c.Embed(ctx, []string{"alpha", "cc"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], []string{"cc"}) {
		t.Fatalf("partial call should embed only 'cc', got %v", calls[1:])
	}

	// Persist and reload into a fresh cache → served from disk, no base calls.
	c.Save()
	var calls2 [][]string
	base2 := func(_ context.Context, inputs []string) ([][]float32, error) {
		calls2 = append(calls2, inputs)
		return make([][]float32, len(inputs)), nil
	}
	c2 := NewEmbedCache(path, "m1", base2)
	got3, _ := c2.Embed(ctx, []string{"alpha", "bb", "cc"})
	if len(calls2) != 0 {
		t.Fatalf("reloaded cache should serve from disk; base calls=%v", calls2)
	}
	if !reflect.DeepEqual(got3[0], []float32{5, 1}) {
		t.Fatalf("reloaded vector mismatch %v", got3[0])
	}
}

// Different models must not share cache entries even for identical text.
func TestEmbedCacheModelNamespacing(t *testing.T) {
	var calls int
	base := func(_ context.Context, inputs []string) ([][]float32, error) {
		calls++
		return [][]float32{{1}}, nil
	}
	path := filepath.Join(t.TempDir(), "e.gob")
	c1 := NewEmbedCache(path, "modelA", base)
	c1.Embed(context.Background(), []string{"x"})
	c1.Save()

	c2 := NewEmbedCache(path, "modelB", base) // same file, different model
	c2.Embed(context.Background(), []string{"x"})
	if calls != 2 {
		t.Errorf("different models should not share entries; base calls=%d, want 2", calls)
	}
}

func TestEmbedCacheNoPersistPath(t *testing.T) {
	c := NewEmbedCache("", "m", func(_ context.Context, in []string) ([][]float32, error) {
		return make([][]float32, len(in)), nil
	})
	if _, err := c.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	c.Save() // must be a no-op, not panic
}

// Save under concurrent Embed must not race the file or lose dirty entries.
func TestEmbedCacheConcurrentSave(t *testing.T) {
	base := func(_ context.Context, in []string) ([][]float32, error) {
		out := make([][]float32, len(in))
		for i := range in {
			out[i] = []float32{1}
		}
		return out, nil
	}
	path := filepath.Join(t.TempDir(), "c.gob")
	c := NewEmbedCache(path, "m", base)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Embed(ctx, []string{fmt.Sprintf("t-%d-%d", i, j)})
			}
			c.Save()
		}(i)
	}
	wg.Wait()
	c.Save()
	// Everything embedded should have persisted; reload sees it with no base call.
	var calls int
	c2 := NewEmbedCache(path, "m", func(_ context.Context, in []string) ([][]float32, error) {
		calls++
		return make([][]float32, len(in)), nil
	})
	if _, err := c2.Embed(ctx, []string{"t-0-0", "t-7-49"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("reloaded cache missed persisted entries; base calls=%d", calls)
	}
}
