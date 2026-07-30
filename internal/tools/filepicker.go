package tools

// filepicker.go implements the find_files tool: given a natural-language query
// or a set of keywords, it ranks repo files by relevance using the scored symbol
// map (symbolmap.go) — no LLM call, no embeddings. It's the deterministic
// counterpart to Codebuff's "file picker": query terms are matched against
// symbol names, file paths, and the caller graph, and the highest-scoring files
// are returned for the agent to read in one batch.
//
// Ranking per file combines:
//   - the file's symbol scores (importance from the code map),
//   - term hits in symbol names / the path (query relevance),
//   - a small boost for being referenced by many files (popularity).

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

type findFilesTool struct{ r *Registry }

func (t *findFilesTool) Name() string  { return "find_files" }
func (t *findFilesTool) Mutates() bool { return false }

func (t *findFilesTool) Description() string {
	return "Rank the repo's files by relevance to a QUERY using a local, prebuilt symbol/caller map (no LLM, no embeddings) and return the most relevant file paths to read. Fast way to answer 'which files are involved in X' before diving in: query terms are matched against symbol names, file paths, and the cross-file caller graph, and files are ranked by symbol importance + term hits + how widely their symbols are referenced. Prefer this for a quick relevant-file shortlist; use grep for exact strings, find_symbol to jump to a definition, and semantic_search for meaning-based lookup. Options: query (required), count (max files, default 12, max 40)."
}

func (t *findFilesTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "What to look for: keywords or a short phrase. Split on spaces; identifiers are matched flexibly (camelCase/snake_case aware)."},
			"count": {"type": "integer", "description": "Max files to return (default 12, max 40)."}
		},
		"required": ["query"]
	}`)
}

type findFilesInput struct {
	Query string  `json:"query"`
	Count flexInt `json:"count"`
}

func (t *findFilesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in findFilesInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	count := int(in.Count)
	if count <= 0 {
		count = 12
	}
	if count > 40 {
		count = 40
	}

	m := t.r.SymbolMap()
	if len(m.Entries) == 0 {
		return "No symbol map available (no supported source files found).", nil
	}

	terms := queryTerms(in.Query)
	ranked := rankFiles(m, terms)
	if len(ranked) == 0 {
		return "No files matched the query. Try broader keywords, or use grep/semantic_search.", nil
	}
	if len(ranked) > count {
		ranked = ranked[:count]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Top %d files for %q (ranked by symbol relevance):\n", len(ranked), in.Query)
	for i, f := range ranked {
		fmt.Fprintf(&b, "%2d. %s", i+1, f.file)
		if len(f.hitSyms) > 0 {
			fmt.Fprintf(&b, "  — %s", strings.Join(f.hitSyms, ", "))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nRead the ones relevant to your task (batch the reads).\n")
	return b.String(), nil
}

// queryTerms lowercases the query and splits it into search terms, also breaking
// identifiers on camelCase / snake_case / digit boundaries so "parseConfig"
// matches "parse", "config".
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) >= 2 && !seen[s] && !isKeyword(s) {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, raw := range splitIdentifiers(q) {
		add(raw)
	}
	return out
}

var identSplitRe = regexp.MustCompile(`[^A-Za-z0-9]+`)
var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func splitIdentifiers(s string) []string {
	s = camelRe.ReplaceAllString(s, "$1 $2")
	return identSplitRe.Split(s, -1)
}

type fileRank struct {
	file    string
	score   float64
	hitSyms []string
}

// rankFiles scores each file in the symbol map against the query terms.
func rankFiles(m *SymbolMap, terms []string) []fileRank {
	type acc struct {
		score   float64
		hits    map[string]bool
		hitSyms []string
	}
	byFile := map[string]*acc{}
	get := func(f string) *acc {
		a := byFile[f]
		if a == nil {
			a = &acc{hits: map[string]bool{}}
			byFile[f] = a
		}
		return a
	}

	for _, e := range m.Entries {
		nameTerms := splitIdentifiersLower(e.Name)
		pathTerms := splitIdentifiersLower(e.File)
		var relevance float64
		matched := false
		for _, term := range terms {
			if containsTerm(nameTerms, term) {
				relevance += 3 // symbol-name hit is strongest
				matched = true
			} else if strings.Contains(strings.ToLower(e.Name), term) {
				relevance += 1.5
				matched = true
			}
			if containsTerm(pathTerms, term) {
				relevance += 1 // path hit
				matched = true
			}
		}
		if !matched {
			continue
		}
		a := get(e.File)
		// Weight the symbol's own importance (code-map score) and popularity.
		a.score += relevance * (1 + e.Score) * (1 + float64(len(e.Callers))/float64(symbolMaxCallers))
		if !a.hits[e.Name] && len(a.hitSyms) < 6 {
			a.hits[e.Name] = true
			a.hitSyms = append(a.hitSyms, e.Name)
		}
	}

	out := make([]fileRank, 0, len(byFile))
	for f, a := range byFile {
		out = append(out, fileRank{file: f, score: a.score, hitSyms: a.hitSyms})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].file < out[j].file
	})
	return out
}

func splitIdentifiersLower(s string) []string {
	parts := splitIdentifiers(path.Base(s))
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return parts
}

func containsTerm(parts []string, term string) bool {
	for _, p := range parts {
		if p == term {
			return true
		}
	}
	return false
}
