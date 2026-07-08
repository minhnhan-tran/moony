package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// searchCodeTool provides relevance-ranked code retrieval. Unlike grep (which
// returns every line matching a regex) it scores line-window chunks across the
// tree with BM25 over the query's terms — after splitting identifiers on
// camelCase/snake_case/digit boundaries — and returns only the top few most
// relevant chunks. This lets the agent answer "where/how is X" in one call with
// a small result, instead of grepping and then reading several whole files, so
// it is both smarter and far cheaper in tokens.
type searchCodeTool struct{ r *Registry }

func (t *searchCodeTool) Name() string  { return "search_code" }
func (t *searchCodeTool) Mutates() bool { return false }

func (t *searchCodeTool) Description() string {
	return "Relevance-ranked code search: given a natural-language or multi-term QUERY, returns the most relevant code CHUNKS (not whole files) across the repo, ranked by BM25. Identifiers are split on camelCase/snake_case/digit boundaries, so 'parse config' matches parseConfig, parse_config, ConfigParser, CONFIG_PATH, etc. Prefer this over grep-then-read when you want to find WHERE or HOW something is implemented and read only the relevant snippets — it answers in one small result instead of many file reads. Use grep for exact/regex/literal matches, and find_symbol to jump to a definition. Read-only. Options: query (required), path (limit to a subdir), include (comma-separated globs like '*.go' or '*.{ts,tsx}'), limit (max chunks, default 10, max 30)."
}

func (t *searchCodeTool) Schema() json.RawMessage {
	return schema(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What to look for: keywords or a short phrase. Identifiers are split on camelCase/snake_case boundaries, so multi-word queries and code names both work."},
    "path": {"type": "string", "description": "Optional directory (relative to the working dir) to restrict the search to."},
    "include": {"type": "string", "description": "Optional comma-separated file globs to include, e.g. '*.go' or '*.{ts,tsx}'."},
    "limit": {"type": "integer", "description": "Maximum number of chunks to return (default 10, max 30)."}
  },
  "required": ["query"]
}`)
}

type searchCodeInput struct {
	Query   string `json:"query"`
	Path    string `json:"path"`
	Include string `json:"include"`
	Limit   int    `json:"limit"`
}

// Chunking window: 20-line windows advanced by 12 lines (8 lines of overlap so a
// match near a boundary is not split across two low-scoring chunks).
const (
	codeChunkLines  = 20
	codeChunkStride = 12
)

// BM25 tuning (standard defaults).
const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// searchCodeMaxOutputBytes caps the rendered result so a broad query can't blow
// the context budget it is meant to save.
const searchCodeMaxOutputBytes = 24000

// codeChunk is a scored line-window of a file.
type codeChunk struct {
	file   string
	start  int            // 1-based line number of the first line in the window
	lines  []string       // the window's lines
	qtf    map[string]int // term frequency, query terms only
	length int            // total tokens in the window (for BM25 length norm)
	score  float64
}

func (t *searchCodeTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in searchCodeInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	qterms := uniqueTerms(tokenizeCode(in.Query))
	if len(qterms) == 0 {
		return "", fmt.Errorf("query has no searchable terms (needs at least one 2+ character word)")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}

	// Identical earlier search this session? Serve the cached rendering.
	if cached, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return cached, nil
	}

	includes, err := parseIncludes(in.Include)
	if err != nil {
		return "", err
	}
	searchPath := in.Path
	if searchPath == "" {
		searchPath = "."
	}
	abs, err := t.r.resolve(searchPath)
	if err != nil {
		return "", err
	}

	// Phase 1: collect candidate files, honoring ignore rules, include globs and
	// the size guard (same walk as grep).
	var files []string
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(t.r.root, p)
		if d.IsDir() {
			if p != abs && t.r.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.r.ignore.Match(rel, false) {
			return nil
		}
		if len(includes) > 0 && !includeMatch(includes, rel) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > maxGrepFileSize {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	sort.Strings(files)

	// Phase 2: read, chunk and pre-score candidate files concurrently. Only
	// chunks containing at least one query term are retained.
	qset := make(map[string]bool, len(qterms))
	for _, q := range qterms {
		qset[q] = true
	}
	perFile := make([][]codeChunk, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				data, rerr := os.ReadFile(filepath.Join(t.r.root, files[i]))
				if rerr != nil || isBinary(data) {
					continue
				}
				perFile[i] = chunkAndScoreFile(files[i], data, qset)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var chunks []codeChunk
	for i := range perFile {
		chunks = append(chunks, perFile[i]...)
	}
	if len(chunks) == 0 {
		return fmt.Sprintf("search_code: no relevant code found for %q (scanned %d files). Try looser terms, drop the include/path filter, or use grep for an exact string.", in.Query, len(files)), nil
	}

	rankCodeChunks(chunks, in.Query)
	t.semanticRerank(ctx, in.Query, chunks)
	res := formatCodeChunks(in.Query, chunks, len(files), limit)
	t.r.searchCacheStore(t.Name(), input, res)
	return res, nil
}

// chunkAndScoreFile splits a file into overlapping line windows and records, for
// each window that contains a query term, its query-term frequencies and total
// token length.
func chunkAndScoreFile(rel string, data []byte, qset map[string]bool) []codeChunk {
	lines := strings.Split(string(data), "\n")
	var chunks []codeChunk
	for start := 0; start < len(lines); start += codeChunkStride {
		end := start + codeChunkLines
		if end > len(lines) {
			end = len(lines)
		}
		seg := lines[start:end]
		qtf := map[string]int{}
		total := 0
		for _, line := range seg {
			for _, tok := range tokenizeCode(line) {
				total++
				if qset[tok] {
					qtf[tok]++
				}
			}
		}
		if len(qtf) > 0 {
			chunks = append(chunks, codeChunk{file: rel, start: start + 1, lines: seg, qtf: qtf, length: total})
		}
		if end == len(lines) {
			break
		}
	}
	return chunks
}

// rankCodeChunks scores each candidate chunk with BM25 over the query terms
// (corpus statistics computed across the candidate chunks), plus a small bonus
// for chunks that match more distinct terms and that contain the query as a
// contiguous substring, then sorts them best-first.
func rankCodeChunks(chunks []codeChunk, query string) {
	n := len(chunks)
	totalLen := 0
	df := map[string]int{}
	for i := range chunks {
		totalLen += chunks[i].length
		for term := range chunks[i].qtf {
			df[term]++
		}
	}
	avgdl := 1.0
	if n > 0 && totalLen > 0 {
		avgdl = float64(totalLen) / float64(n)
	}
	idf := make(map[string]float64, len(df))
	for term, d := range df {
		idf[term] = math.Log(1 + (float64(n)-float64(d)+0.5)/(float64(d)+0.5))
	}
	qlower := strings.ToLower(strings.TrimSpace(query))
	for i := range chunks {
		c := &chunks[i]
		dl := float64(c.length)
		var s float64
		for term, f := range c.qtf {
			tf := float64(f)
			s += idf[term] * (tf * (bm25K1 + 1)) / (tf + bm25K1*(1-bm25B+bm25B*dl/avgdl))
		}
		s *= 1 + 0.15*float64(len(c.qtf)) // reward covering more distinct query terms
		if qlower != "" && strings.Contains(strings.ToLower(strings.Join(c.lines, "\n")), qlower) {
			s *= 1.25 // exact phrase present
		}
		if definesQueryTerm(c.lines, c.qtf) {
			s *= 1.6 // a chunk that DEFINES a query term beats incidental mentions
		}
		c.score = s
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].score != chunks[j].score {
			return chunks[i].score > chunks[j].score
		}
		if chunks[i].file != chunks[j].file {
			return chunks[i].file < chunks[j].file
		}
		return chunks[i].start < chunks[j].start
	})
}

// formatCodeChunks renders the top-ranked chunks (skipping windows that overlap
// an already-emitted one from the same file) with line-numbered snippets,
// bounded by limit and searchCodeMaxOutputBytes.
func formatCodeChunks(query string, chunks []codeChunk, filesScanned, limit int) string {
	emitted := map[string][][2]int{} // file -> [start,end] windows already shown
	var body strings.Builder
	shown := 0
	for i := range chunks {
		if shown >= limit {
			break
		}
		c := &chunks[i]
		end := c.start + len(c.lines) - 1
		overlap := false
		for _, rng := range emitted[c.file] {
			if c.start <= rng[1] && end >= rng[0] {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		// Trim blank lines at the edges of the window for a tighter snippet.
		lines, s := c.lines, c.start
		for len(lines) > 1 && strings.TrimSpace(lines[0]) == "" {
			lines, s = lines[1:], s+1
		}
		for len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		e := s + len(lines) - 1
		var chunk strings.Builder
		fmt.Fprintf(&chunk, "%s:%d-%d\n", c.file, s, e)
		for k, line := range lines {
			fmt.Fprintf(&chunk, "%d: %s\n", s+k, line)
		}
		if shown > 0 && body.Len()+chunk.Len() > searchCodeMaxOutputBytes {
			break
		}
		body.WriteString(chunk.String())
		body.WriteString("\n")
		emitted[c.file] = append(emitted[c.file], [2]int{c.start, end})
		shown++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_code: %q — %d relevant chunk(s) shown (%d candidate chunks across %d files)\n\n", query, shown, len(chunks), filesScanned)
	b.WriteString(strings.TrimRight(body.String(), "\n"))
	return b.String()
}

// tokenizeCode splits text into lowercased retrieval tokens: each maximal run of
// letters/digits/underscore becomes a token, and identifiers are further split
// on underscore, camelCase and letter/digit boundaries so "config" matches
// ParseConfig / parse_config / CONFIG_PATH. Both the whole token and its
// subtokens are emitted; single-character tokens are dropped.
func tokenizeCode(s string) []string {
	var toks []string
	emit := func(w string) {
		if len([]rune(w)) < 2 {
			return
		}
		toks = append(toks, strings.ToLower(w))
	}
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		w := string(cur)
		cur = cur[:0]
		whole := strings.ToLower(w)
		emit(w)
		for _, sub := range splitIdentifier(w) {
			if strings.ToLower(sub) != whole {
				emit(sub)
			}
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return toks
}

// splitIdentifier breaks an identifier into its parts on underscores and
// camelCase / acronym / digit boundaries.
func splitIdentifier(w string) []string {
	var parts []string
	for _, seg := range strings.Split(w, "_") {
		parts = append(parts, splitCamel(seg)...)
	}
	return parts
}

func splitCamel(s string) []string {
	rs := []rune(s)
	if len(rs) == 0 {
		return nil
	}
	var out []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		boundary := false
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur): // fooBar -> foo|Bar
			boundary = true
		case unicode.IsDigit(prev) != unicode.IsDigit(cur): // v2 -> v|2
			boundary = true
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			boundary = true // HTTPServer -> HTTP|Server
		}
		if boundary {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	out = append(out, string(rs[start:]))
	return out
}

// uniqueTerms deduplicates while preserving order.
func uniqueTerms(toks []string) []string {
	seen := make(map[string]bool, len(toks))
	var out []string
	for _, t := range toks {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// codeDefLineRe matches a line that declares something (function/type/class/…),
// optionally after common visibility/modifier keywords. Used to boost chunks
// that DEFINE a query term over ones that merely mention it.
var codeDefLineRe = regexp.MustCompile(`^\s*(?:export |public |private |protected |pub |static |async |final |abstract |default |open |sealed )*(?:func|function|type|class|def|struct|interface|enum|trait|impl|fn|module|package|const|let|var)\b`)

// definesQueryTerm reports whether any line in the chunk is a definition line
// that contains a query term (as a token) — i.e. the chunk likely defines what
// the query is looking for.
func definesQueryTerm(lines []string, qtf map[string]int) bool {
	for _, line := range lines {
		if !codeDefLineRe.MatchString(line) {
			continue
		}
		for _, tok := range tokenizeCode(line) {
			if qtf[tok] > 0 {
				return true
			}
		}
	}
	return false
}

// --- hybrid semantic reranking -------------------------------------------

// Tunables for semantic reranking. Only the top candidates are embedded (to
// bound cost/latency); their blended score mixes lexical BM25 and embedding
// cosine similarity to the query.
const (
	semanticRerankCandidates = 40
	semanticWeight           = 0.5 // weight of embedding cosine vs. normalized BM25
)

// semanticRerank re-scores the top BM25 candidates by embedding cosine
// similarity to the query and blends the two signals, then re-sorts just those
// candidates. It is a no-op when no embedder is configured, and on any embedding
// error it silently leaves the BM25 order intact.
func (t *searchCodeTool) semanticRerank(ctx context.Context, query string, chunks []codeChunk) {
	if t.r.embedder == nil || len(chunks) < 2 || strings.TrimSpace(query) == "" {
		return
	}
	k := len(chunks)
	if k > semanticRerankCandidates {
		k = semanticRerankCandidates
	}
	cand := chunks[:k]

	inputs := make([]string, 0, k+1)
	inputs = append(inputs, query)
	for i := range cand {
		inputs = append(inputs, chunkEmbedText(cand[i]))
	}
	vecs, err := t.r.embedder.Embed(ctx, inputs)
	if err != nil || len(vecs) != len(inputs) || len(vecs[0]) == 0 {
		return // fall back to pure BM25
	}
	qv := vecs[0]

	// Normalize BM25 scores across the candidate window to [0,1] so the blend
	// is scale-independent.
	maxB, minB := cand[0].score, cand[0].score
	for i := range cand {
		if cand[i].score > maxB {
			maxB = cand[i].score
		}
		if cand[i].score < minB {
			minB = cand[i].score
		}
	}
	span := maxB - minB
	for i := range cand {
		cos := cosineSim(qv, vecs[i+1])
		normB := 0.5
		if span > 0 {
			normB = (cand[i].score - minB) / span
		}
		cand[i].score = semanticWeight*cos + (1-semanticWeight)*normB
	}
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].score != cand[j].score {
			return cand[i].score > cand[j].score
		}
		if cand[i].file != cand[j].file {
			return cand[i].file < cand[j].file
		}
		return cand[i].start < cand[j].start
	})
}

// chunkEmbedText renders a chunk for embedding: its path plus the code lines,
// capped so a pathological window can't bloat the request.
func chunkEmbedText(c codeChunk) string {
	body := strings.Join(c.lines, "\n")
	const maxLen = 4000
	if len(body) > maxLen {
		body = body[:maxLen]
	}
	return c.file + "\n" + body
}

// cosineSim returns the cosine similarity of two vectors in [-1,1] (0 if either
// is empty, a different length, or zero-norm).
func cosineSim(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
