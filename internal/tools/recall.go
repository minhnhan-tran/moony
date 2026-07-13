package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	defaultRecallLimit = 5
	maxRecallLimit     = 25
	// recallRerankCandidates bounds how many top-BM25 notes semanticRerank
	// re-scores by embedding similarity, mirroring search_code's
	// semanticRerankCandidates — small since a memory-note list is typically
	// far smaller than a codebase.
	recallRerankCandidates = 40
)

// recallTool retrieves persisted memory notes by RELEVANCE to a query, rather
// than dumping them all. It ranks the same notes the remember tool writes
// (project + global AGENTS.md memory) with BM25 over identifier-aware tokens —
// the same tokenizer search_code uses — so "config parsing" matches a note
// about parseConfig. When an embedder is configured (MOONY_EMBEDDINGS_MODEL),
// the top BM25 candidates also get the same hybrid semantic rerank search_code
// uses, so a paraphrased query ("don't tag versions by hand") can still surface
// a note about the release flow even without shared keywords. Complements
// remember (write) with relevance recall (read).
type recallTool struct {
	sink *MemorySink
	r    *Registry
}

func (t *recallTool) Name() string { return "recall" }

func (t *recallTool) Description() string {
	return "Search your persisted long-term memory notes (the ones saved with the remember tool, from project " +
		"and global AGENTS.md) and return the ones most RELEVANT to a query, ranked by relevance. Use this to pull " +
		"back conventions, rules, and preferences that apply to the current task instead of relying only on what's " +
		"already in the prompt. Returns the top matches; raise limit for more."
}

func (t *recallTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"query": {"type": "string", "description": "What to recall — keywords or a short phrase."},
		"limit": {"type": "integer", "description": "Maximum notes to return (default 5, max 25)."}
	}, "required": ["query"]}`)
}

func (t *recallTool) Mutates() bool { return false }

func (t *recallTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return "", fmt.Errorf("query is required")
	}
	limit := in.Limit
	if limit <= 0 || limit > maxRecallLimit {
		limit = defaultRecallLimit
	}
	if t.sink == nil || t.sink.list == nil {
		return "Long-term memory is not available in this session.", nil
	}
	notes := t.sink.list()
	if len(notes) == 0 {
		return "No memory notes saved yet.", nil
	}

	candidates := rankNotesBM25(q, notes, recallRerankCandidates)
	if len(candidates) == 0 {
		return fmt.Sprintf("No memory notes relevant to %q (out of %d saved).", q, len(notes)), nil
	}
	t.semanticRerank(ctx, q, candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Top %d of %d memory notes for %q:\n", len(candidates), len(notes), q)
	for _, c := range candidates {
		if c.note.Section != "" {
			fmt.Fprintf(&b, "\n- [%s] %s", c.note.Section, c.note.Text)
		} else {
			fmt.Fprintf(&b, "\n- %s", c.note.Text)
		}
	}
	return b.String(), nil
}

// scoredNote pairs a memory note with its current relevance score, mutated in
// place by semanticRerank the same way searchcode.go's codeChunk.score is.
type scoredNote struct {
	note  MemoryNote
	score float64
}

// rankNotesBM25 scores notes against the query with Okapi BM25 (k1=1.5, b=0.75)
// over identifier-aware tokens and returns the top-limit, highest first. Notes
// with no query-term overlap are dropped.
func rankNotesBM25(query string, notes []MemoryNote, limit int) []scoredNote {
	const k1, b = 1.5, 0.75

	qTerms := tokenizeCode(query)
	if len(qTerms) == 0 {
		return nil
	}
	qSet := make(map[string]bool, len(qTerms))
	for _, term := range qTerms {
		qSet[term] = true
	}

	// Per-note token frequencies and document lengths.
	docTF := make([]map[string]int, len(notes))
	totalLen := 0
	df := make(map[string]int)
	for i, n := range notes {
		toks := tokenizeCode(n.Section + " " + n.Text)
		tf := make(map[string]int, len(toks))
		for _, tok := range toks {
			tf[tok]++
		}
		docTF[i] = tf
		totalLen += len(toks)
		for tok := range tf {
			if qSet[tok] {
				df[tok]++
			}
		}
	}
	if totalLen == 0 {
		return nil
	}
	avgLen := float64(totalLen) / float64(len(notes))
	N := float64(len(notes))

	type scored struct {
		idx   int
		score float64
	}
	var results []scored
	for i, tf := range docTF {
		docLen := 0
		for _, c := range tf {
			docLen += c
		}
		var score float64
		for term := range qSet {
			f := float64(tf[term])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (N-float64(df[term])+0.5)/(float64(df[term])+0.5))
			score += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*float64(docLen)/avgLen))
		}
		if score > 0 {
			results = append(results, scored{i, score})
		}
	}
	sort.SliceStable(results, func(a, b int) bool { return results[a].score > results[b].score })
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]scoredNote, len(results))
	for i, r := range results {
		out[i] = scoredNote{note: notes[r.idx], score: r.score}
	}
	return out
}

// semanticRerank re-scores the top BM25 candidates by embedding cosine
// similarity to the query and blends the two signals, then re-sorts in place —
// the same blend searchcode.go's semanticRerank uses. A no-op when no embedder
// is configured, and on any embedding error it silently leaves the BM25 order
// intact.
func (t *recallTool) semanticRerank(ctx context.Context, query string, candidates []scoredNote) {
	if t.r == nil || t.r.embedder == nil || len(candidates) < 2 || strings.TrimSpace(query) == "" {
		return
	}
	inputs := make([]string, 0, len(candidates)+1)
	inputs = append(inputs, query)
	for i := range candidates {
		inputs = append(inputs, noteEmbedText(candidates[i].note))
	}
	vecs, err := t.r.embedder.Embed(ctx, inputs)
	if err != nil || len(vecs) != len(inputs) || len(vecs[0]) == 0 {
		return // fall back to pure BM25
	}
	qv := vecs[0]

	// Normalize BM25 scores across the candidate window to [0,1] so the blend
	// is scale-independent.
	maxB, minB := candidates[0].score, candidates[0].score
	for i := range candidates {
		if candidates[i].score > maxB {
			maxB = candidates[i].score
		}
		if candidates[i].score < minB {
			minB = candidates[i].score
		}
	}
	span := maxB - minB
	for i := range candidates {
		cos := cosineSim(qv, vecs[i+1])
		normB := 0.5
		if span > 0 {
			normB = (candidates[i].score - minB) / span
		}
		candidates[i].score = semanticWeight*cos + (1-semanticWeight)*normB
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
}

// noteEmbedText renders a note for embedding: its section plus text, capped so
// a pathological note can't bloat the request.
func noteEmbedText(n MemoryNote) string {
	body := n.Text
	const maxLen = 4000
	if len(body) > maxLen {
		body = body[:maxLen]
	}
	if n.Section == "" {
		return body
	}
	return n.Section + "\n" + body
}
