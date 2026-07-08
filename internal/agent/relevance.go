package agent

import (
	"regexp"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// Relevance-based retention: when an old tool result is elided to reclaim
// context, one that still relates to the user's current goal keeps a bounded
// excerpt instead of a bare stub, so useful information survives longer than
// pure recency would allow. Relevance is scored lexically (goal-term overlap) —
// deterministic, cheap, and safe to run in the synchronous prune path, unlike a
// network embedding call. Elision stays one-time and contiguous, so this doesn't
// add prompt-cache churn.

const (
	// relevanceKeepThreshold: fraction of the goal's distinct terms that must
	// appear in a result for it to be worth retaining an excerpt.
	relevanceKeepThreshold = 0.34
	relevantExcerptHead    = 700
	relevantExcerptTail    = 300
)

var wordRE = regexp.MustCompile(`[a-z0-9_]+`)

// lastUserText returns the most recent genuine user message text (skipping the
// user-role messages that only carry tool_results), i.e. the current goal.
func lastUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != llm.RoleUser {
			continue
		}
		var sb strings.Builder
		for _, c := range m.Content {
			if c.Text != "" {
				sb.WriteString(c.Text)
				sb.WriteByte(' ')
			}
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			return s
		}
	}
	return ""
}

// goalTerms extracts the significant lowercased terms of a goal string (drops
// short words and stopwords), for lexical relevance scoring.
func goalTerms(text string) map[string]bool {
	terms := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.ToLower(text), -1) {
		if len(w) < 3 || goalStopwords[w] {
			continue
		}
		terms[w] = true
	}
	return terms
}

// relevanceScore is the fraction of the goal's distinct terms that appear in
// text (0..1).
func relevanceScore(text string, terms map[string]bool) float64 {
	if len(terms) == 0 {
		return 0
	}
	low := strings.ToLower(text)
	hit := 0
	for t := range terms {
		if strings.Contains(low, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(terms))
}

// relevantExcerpt keeps the head and tail of content with the middle removed, so
// a relevant-but-elided result retains its most informative parts at a fraction
// of the size. Rune-safe.
func relevantExcerpt(content string, head, tail int) string {
	r := []rune(content)
	if len(r) <= head+tail {
		return content
	}
	return string(r[:head]) + "\n… [middle elided] …\n" + string(r[len(r)-tail:])
}

var goalStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "with": true, "that": true,
	"this": true, "have": true, "are": true, "was": true, "but": true, "not": true,
	"can": true, "will": true, "your": true, "from": true, "get": true, "use": true,
	"add": true, "any": true, "all": true, "into": true, "out": true, "how": true,
	"why": true, "what": true, "when": true, "where": true, "should": true, "would": true,
	"could": true, "make": true, "need": true, "want": true, "please": true, "then": true,
	"there": true, "here": true, "its": true, "our": true, "let": true, "now": true,
}
