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
)

// recallTool retrieves persisted memory notes by RELEVANCE to a query, rather
// than dumping them all. It ranks the same notes the remember tool writes
// (project + global AGENTS.md memory) with BM25 over identifier-aware tokens —
// the same tokenizer search_code uses — so "config parsing" matches a note
// about parseConfig. Complements remember (write) with relevance recall (read).
type recallTool struct {
	sink *MemorySink
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

func (t *recallTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
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

	ranked := rankNotesBM25(q, notes, limit)
	if len(ranked) == 0 {
		return fmt.Sprintf("No memory notes relevant to %q (out of %d saved).", q, len(notes)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Top %d of %d memory notes for %q:\n", len(ranked), len(notes), q)
	for _, n := range ranked {
		if n.Section != "" {
			fmt.Fprintf(&b, "\n- [%s] %s", n.Section, n.Text)
		} else {
			fmt.Fprintf(&b, "\n- %s", n.Text)
		}
	}
	return b.String(), nil
}

// rankNotesBM25 scores notes against the query with Okapi BM25 (k1=1.5, b=0.75)
// over identifier-aware tokens and returns the top-limit, highest first. Notes
// with no query-term overlap are dropped.
func rankNotesBM25(query string, notes []MemoryNote, limit int) []MemoryNote {
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
	out := make([]MemoryNote, len(results))
	for i, r := range results {
		out[i] = notes[r.idx]
	}
	return out
}
