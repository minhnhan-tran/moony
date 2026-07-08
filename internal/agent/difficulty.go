package agent

import (
	"regexp"
	"strings"
)

// turnLooksHard is a cheap, proactive difficulty classifier for a user turn. It
// returns true when the request is obviously non-trivial, so adaptive model
// routing can start it on the strong model immediately instead of trying the
// cheap FastModel first and escalating on friction.
//
// The asymmetry is deliberate and safe: a false "hard" only spends the strong
// model on an easy turn (a little more cost, no quality loss), while a missed
// "hard" is still caught by the reactive friction escalation. So it errs toward
// hard on any real signal and keeps only clearly-small talk on the fast path.
func turnLooksHard(userInput string) bool {
	s := strings.TrimSpace(userInput)
	if s == "" {
		return false
	}
	// Long requests are rarely trivial.
	if len(s) > 600 {
		return true
	}
	// Fenced code / stack traces / diffs pasted in → analysis-heavy.
	if strings.Contains(s, "```") || strings.Count(s, "\n") >= 8 {
		return true
	}
	low := strings.ToLower(s)
	// Intents that call for real reasoning, multi-file work, or debugging.
	if hardIntent.MatchString(low) {
		return true
	}
	// References to many files/paths suggest cross-cutting work.
	if len(pathLike.FindAllString(s, 4)) >= 3 {
		return true
	}
	return false
}

var (
	hardIntent = regexp.MustCompile(`\b(refactor|redesign|architect(ure)?|design|implement|migrat(e|ion)|optimi[sz]e|debug|root[ -]?cause|investigat|diagnos|concurren|race condition|deadlock|why (is|does|are|did)|figure out|trace|profile|benchmark|rewrite|overhaul|integrat|end[- ]to[- ]end|across (the|all|multiple)|throughout|security|vulnerab|thread[- ]safe|performance)\b`)
	// A token that looks like a file path or dotted identifier (a.b/c, foo.go).
	pathLike = regexp.MustCompile(`\b[\w./-]+\.[a-zA-Z]{1,4}\b|\b[\w-]+/[\w./-]+\b`)
)
