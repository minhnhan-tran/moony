package llm

import (
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// EmbedCache wraps an embedding function with a persistent, content-addressed
// cache so identical inputs — code chunks that haven't changed, repeated queries
// — aren't re-embedded across searches or sessions. Keys are sha256(model+text),
// so one file can safely hold vectors for multiple models. Vectors persist via
// gob under the config dir.
//
// This is what makes the search_code hybrid semantic rerank affordable enough to
// leave always-on: on a warm repo almost every candidate chunk is a cache hit,
// so a search embeds only the (novel) query. Concurrency-safe.
type EmbedCache struct {
	base  func(context.Context, []string) ([][]float32, error)
	model string
	path  string

	mu      sync.Mutex
	entries map[string]cacheEntry
	dirty   int // new/updated entries since last persist

	saveMu sync.Mutex // serializes Save so concurrent flushes can't race the file
}

type cacheEntry struct {
	Vec  []float32
	Used int64 // unix seconds of last use, for LRU eviction
}

const (
	// embedCacheMax caps stored vectors; beyond it the least-recently-used are
	// evicted (a warm repo of a few thousand chunks fits comfortably).
	embedCacheMax = 10000
	// embedCacheSaveEvery persists after this many new/updated entries, so we
	// amortize disk writes (a typical search adds ~40 chunks).
	embedCacheSaveEvery = 16
)

// NewEmbedCache opens (or creates) a cache at path wrapping base. A missing or
// unreadable file yields an empty cache; path "" disables persistence (memory
// only). model namespaces keys so different embedding models never collide.
func NewEmbedCache(path, model string, base func(context.Context, []string) ([][]float32, error)) *EmbedCache {
	c := &EmbedCache{base: base, model: model, path: path, entries: map[string]cacheEntry{}}
	c.load()
	return c
}

func (c *EmbedCache) key(text string) string {
	h := sha256.New()
	h.Write([]byte(c.model))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// Embed returns a vector per input, serving hits from the cache and calling the
// wrapped function only for the misses. Output preserves input order; a nil
// vector at position i means the backend produced none for it.
func (c *EmbedCache) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(inputs))
	keys := make([]string, len(inputs))
	var missIdx []int
	var missTexts []string

	now := time.Now().Unix()
	c.mu.Lock()
	for i, in := range inputs {
		k := c.key(in)
		keys[i] = k
		if e, ok := c.entries[k]; ok && len(e.Vec) > 0 {
			out[i] = e.Vec
			e.Used = now
			c.entries[k] = e
		} else {
			missIdx = append(missIdx, i)
			missTexts = append(missTexts, in)
		}
	}
	c.mu.Unlock()

	if len(missTexts) == 0 {
		return out, nil
	}
	vecs, err := c.base(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for j, idx := range missIdx {
		if j < len(vecs) && len(vecs[j]) > 0 {
			out[idx] = vecs[j]
			c.entries[keys[idx]] = cacheEntry{Vec: vecs[j], Used: now}
			c.dirty++
		}
	}
	c.evictLocked()
	save := c.dirty >= embedCacheSaveEvery
	c.mu.Unlock()
	if save {
		c.Save()
	}
	return out, nil
}

// evictLocked drops least-recently-used entries once the cache exceeds its cap,
// trimming to 90% so eviction isn't triggered on every subsequent insert.
func (c *EmbedCache) evictLocked() {
	if len(c.entries) <= embedCacheMax {
		return
	}
	type ku struct {
		k string
		u int64
	}
	all := make([]ku, 0, len(c.entries))
	for k, e := range c.entries {
		all = append(all, ku{k, e.Used})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].u < all[j].u })
	target := embedCacheMax * 9 / 10
	for i := 0; i < len(all)-target; i++ {
		delete(c.entries, all[i].k)
	}
}

type wireCache struct{ Entries map[string]cacheEntry }

func (c *EmbedCache) load() {
	if c.path == "" {
		return
	}
	f, err := os.Open(c.path)
	if err != nil {
		return
	}
	defer f.Close()
	var w wireCache
	if gob.NewDecoder(f).Decode(&w) == nil && w.Entries != nil {
		c.entries = w.Entries
	}
}

// Save persists the cache atomically. Called automatically once enough new
// entries accumulate; safe to call directly (e.g. on shutdown) and safe under
// concurrent callers.
func (c *EmbedCache) Save() {
	if c.path == "" {
		return
	}
	// Serialize saves so concurrent Embed-triggered flushes can't write the same
	// file at once. saveMu is separate from mu so encoding doesn't block Embed.
	c.saveMu.Lock()
	defer c.saveMu.Unlock()

	// Snapshot under mu (vectors are never mutated in place) then encode outside
	// it so the disk write doesn't block concurrent Embed calls. Remember how many
	// dirty entries we're flushing so we can restore the counter if the write
	// fails, rather than silently dropping the debt.
	c.mu.Lock()
	cp := make(map[string]cacheEntry, len(c.entries))
	for k, v := range c.entries {
		cp[k] = v
	}
	flushing := c.dirty
	c.dirty = 0
	c.mu.Unlock()

	restore := func() {
		c.mu.Lock()
		c.dirty += flushing
		c.mu.Unlock()
	}

	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		restore()
		return
	}
	// Unique temp file per save so concurrent/other-instance saves can't clobber
	// each other's in-flight bytes; only the atomic rename is observable.
	f, err := os.CreateTemp(filepath.Dir(c.path), ".embed-*.tmp")
	if err != nil {
		restore()
		return
	}
	tmp := f.Name()
	if err := gob.NewEncoder(f).Encode(wireCache{Entries: cp}); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		restore()
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		restore()
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		restore()
	}
}

// Flush persists the cache iff it has unsaved entries — a cheap no-op otherwise.
// Meant for shutdown: the registry runs it via a Close closer (see
// Registry.SetEmbedder), so embeds buffered since the last periodic Save aren't
// lost when a short session exits before hitting the save threshold. Save is
// concurrency-safe, so a racing auto-save is harmless.
func (c *EmbedCache) Flush() {
	c.mu.Lock()
	pending := c.dirty > 0
	c.mu.Unlock()
	if pending {
		c.Save()
	}
}

// Len reports the number of cached vectors (for telemetry/tests).
func (c *EmbedCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
