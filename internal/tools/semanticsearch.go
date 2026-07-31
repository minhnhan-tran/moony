package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pysty1995/moony/internal/rag"
)

const (
	ragChunkWindow = 50   // lines per chunk
	ragChunkStride = 40   // advance per chunk (window-stride = overlap)
	ragMaxChunkLen = 4000 // cap chunk text bytes fed to the embedder
	ragMaxChunks   = 20000
	ragEmbedBatch  = 96
	ragMaxSnippet  = 8 // snippet lines shown per hit
)

// semanticSearchTool answers conceptual "where/how do we do X" queries by
// embedding the query and finding the nearest source chunks in a local vector
// index (see package rag). It complements the lexical search_code/grep tools:
// it matches on meaning, so it can find code even when the exact keywords
// aren't present. The workspace is (re)indexed incrementally on demand — only
// new or changed files are re-embedded.
type semanticSearchTool struct {
	r         *Registry
	storePath string

	mu    sync.Mutex
	store *rag.Store
	model string
}

// NewSemanticSearchTool builds the semantic_search tool. storePath is where the
// vector index is persisted; model is the embedding model id (used to
// invalidate the index if the model changes).
func NewSemanticSearchTool(r *Registry, storePath, model string) Tool {
	return &semanticSearchTool{r: r, storePath: storePath, model: model}
}

func (t *semanticSearchTool) Name() string  { return "semantic_search" }
func (t *semanticSearchTool) Mutates() bool { return false }

func (t *semanticSearchTool) Description() string {
	return "Semantic (embedding-based) code search over a local vector index of the workspace. Given a natural-language query, returns the most RELEVANT source chunks by meaning — so it finds code even when the exact keywords don't appear (e.g. \"where do we handle API rate limiting?\" matches a throttle/backoff implementation). Use it for conceptual 'where/how is X done' questions; prefer grep for exact strings/regex and find_symbol to jump to a definition. The index is built and refreshed automatically on first use and when files change."
}

func (t *semanticSearchTool) Schema() json.RawMessage {
	return schema(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What to look for, in natural language or keywords (matched by meaning, not exact text)."},
    "limit": {"type": "integer", "description": "Max chunks to return (default 8, max 30)."},
    "path": {"type": "string", "description": "Optional workspace-relative directory to restrict results to."}
  },
  "required": ["query"]
}`)
}

func (t *semanticSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
		Path  string `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if t.r.embedder == nil {
		return "", fmt.Errorf("semantic search needs an embeddings model; set SC_EMBEDDINGS_MODEL to enable it")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	if limit > 30 {
		limit = 30
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.store == nil {
		st, err := rag.Open(t.storePath, t.model)
		if err != nil {
			return "", fmt.Errorf("open index: %w", err)
		}
		t.store = st
	}

	indexedNow, err := t.reindex(ctx)
	if err != nil {
		return "", err
	}

	qvecs, err := t.r.embedder.Embed(ctx, []string{in.Query})
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}
	if len(qvecs) == 0 {
		return "", fmt.Errorf("embedding the query returned no vector")
	}

	prefix := normalizePrefix(in.Path)
	// Over-fetch when filtering by path so we can still return `limit` hits.
	fetch := limit
	if prefix != "" {
		fetch = limit * 6
	}
	hits := t.store.Search(qvecs[0], fetch)
	if prefix != "" {
		filtered := hits[:0]
		for _, h := range hits {
			if strings.HasPrefix(filepath.ToSlash(h.Path), prefix) {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}

	files, chunks := t.store.Stats()
	return formatSemanticHits(in.Query, hits, files, chunks, indexedNow), nil
}

// reindex walks the workspace and re-embeds new or changed files, pruning
// deleted ones. Returns the number of files (re)embedded this call.
func (t *semanticSearchTool) reindex(ctx context.Context) (int, error) {
	type pending struct {
		hash   string
		chunks []rag.Chunk
	}
	pend := map[string]*pending{}
	seen := map[string]bool{}

	var texts []string
	type ref struct {
		file string
		idx  int
	}
	var refs []ref
	capped := false

	walkErr := filepath.WalkDir(t.r.Root(), func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(t.r.Root(), p)
		if d.IsDir() {
			if p != t.r.Root() && t.r.ignoreSet().Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.r.ignoreSet().Match(rel, false) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > maxGrepFileSize {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil || isBinary(data) {
			return nil
		}
		seen[rel] = true
		hash := rag.ContentHash(data)
		if h, ok := t.store.Hash(rel); ok && h == hash {
			return nil // unchanged
		}
		if capped {
			return nil
		}
		chunks := ragChunkFile(rel, data)
		if len(chunks) == 0 {
			return nil
		}
		pf := &pending{hash: hash, chunks: chunks}
		pend[rel] = pf
		for i := range chunks {
			if len(texts) >= ragMaxChunks {
				capped = true
				break
			}
			texts = append(texts, embedInput(rel, chunks[i].Text))
			refs = append(refs, ref{file: rel, idx: i})
		}
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}

	if len(texts) > 0 {
		progress := ProgressFrom(ctx)
		progress(fmt.Sprintf("indexing %d chunk(s) across %d file(s) for the first time — this can take a few minutes on a large workspace…", len(texts), len(pend)))
		vecs, err := embedBatched(ctx, t.r.embedder, texts, ragEmbedBatch, progress)
		if err != nil {
			return 0, fmt.Errorf("embed workspace: %w", err)
		}
		for j, v := range vecs {
			r := refs[j]
			pend[r.file].chunks[r.idx].Vec = v
		}
		for file, pf := range pend {
			// Drop any chunks left without a vector due to the cap.
			kept := pf.chunks[:0]
			for _, c := range pf.chunks {
				if len(c.Vec) > 0 {
					kept = append(kept, c)
				}
			}
			if len(kept) > 0 {
				t.store.Put(file, pf.hash, kept)
			}
		}
	}

	t.store.Prune(seen)
	if err := t.store.Save(); err != nil {
		return len(pend), fmt.Errorf("save index: %w", err)
	}
	return len(pend), nil
}

// ragChunkFile splits a file into overlapping line windows.
func ragChunkFile(rel string, data []byte) []rag.Chunk {
	lines := strings.Split(string(data), "\n")
	var chunks []rag.Chunk
	for start := 0; start < len(lines); start += ragChunkStride {
		end := start + ragChunkWindow
		if end > len(lines) {
			end = len(lines)
		}
		seg := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(seg) != "" {
			if len(seg) > ragMaxChunkLen {
				seg = seg[:ragMaxChunkLen]
			}
			chunks = append(chunks, rag.Chunk{Path: rel, Start: start + 1, End: end, Text: seg})
		}
		if end == len(lines) {
			break
		}
	}
	return chunks
}

// embedInput prefixes the path so the file's location informs the embedding.
func embedInput(rel, text string) string { return rel + "\n" + text }

// embedBatched embeds texts in chunks of size batch (Floodgate has no working
// batch-embeddings endpoint, so each call still fans out individual requests —
// see llm.Client.Embed — but capping batch size bounds memory/goroutines per
// call). Reports progress after each batch via progress (nil-safe; pass
// ProgressFrom(ctx) or a no-op) so a long first-time reindex doesn't look hung.
func embedBatched(ctx context.Context, emb Embedder, texts []string, batch int, progress ProgressFn) ([][]float32, error) {
	if progress == nil {
		progress = func(string) {}
	}
	out := make([][]float32, 0, len(texts))
	total := len(texts)
	totalBatches := (total + batch - 1) / batch
	for i := 0; i < total; i += batch {
		j := i + batch
		if j > total {
			j = total
		}
		vecs, err := emb.Embed(ctx, texts[i:j])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
		progress(fmt.Sprintf("embedded %d/%d chunks (batch %d/%d)…", len(out), total, i/batch+1, totalBatches))
	}
	return out, nil
}

func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func formatSemanticHits(query string, hits []rag.Result, files, chunks, indexedNow int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Semantic matches for %q (%d files, %d chunks indexed", query, files, chunks)
	if indexedNow > 0 {
		fmt.Fprintf(&b, "; %d re-embedded just now", indexedNow)
	}
	b.WriteString("):\n")
	if len(hits) == 0 {
		b.WriteString("\n(no matches)")
		return b.String()
	}
	for i, h := range hits {
		fmt.Fprintf(&b, "\n%d. %s:%d-%d  (score %.2f)\n", i+1, h.Path, h.Start, h.End, h.Score)
		snippet := strings.Split(h.Text, "\n")
		if len(snippet) > ragMaxSnippet {
			snippet = snippet[:ragMaxSnippet]
		}
		for _, ln := range snippet {
			b.WriteString("    ")
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
