package agent

import (
	"regexp"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// Repeated-ask / correction detection: a gap in the AdaptiveModel/CheapReads
// model-routing logic (see turnHard in difficulty.go and its use in agent.go).
// turnHard and turnFriction are both purely per-turn — a fresh turn always starts
// on a clean slate, evaluated only on its own raw text. So if a (possibly
// FastModel-routed) pass didn't actually resolve something and the user asks
// essentially the same thing again — "check again", a short follow-up, a
// rephrased bug report, or a terse "that didn't work" — nothing about that
// repetition escalates the turn to the strong model; it silently repeats the
// same, possibly-insufficient cheap pass, potentially many times over.
//
// Two complementary detectors give turnHard that missing cross-turn signal:
//   - looksLikeRepeatedAsk — LEXICAL: the current turn re-uses most of a recent
//     turn's significant vocabulary (a literal re-ask or close rephrasing).
//   - looksLikeCorrection — INTENT: the current turn expresses dissatisfaction or
//     asks to retry ("still broken", "didn't work", "try again", "same issue"),
//     which by design shares little vocabulary with the original question — so
//     the lexical detector alone is blind to it. This is the common case behind
//     "the model stays on the cheap model no matter how many times I re-ask."

const (
	// repeatedAskLookback bounds how many of the most recent user turns we
	// compare against. Recent turns are what "asking again" means; similar
	// wording much earlier in a long session is coincidence, not repetition.
	repeatedAskLookback = 6
	// repeatedAskJaccardMin and repeatedAskMinSharedTerms both must hold for a
	// prior turn to count as a repeat of the current one: the overlap must be
	// a large fraction of each turn's significant vocabulary (catches literal
	// repeats and short rephrasings/reorderings alike), and there must be
	// enough shared terms that the match isn't just one common word.
	repeatedAskJaccardMin     = 0.6
	repeatedAskMinSharedTerms = 2
	// correctionShortWords bounds how many words a message may have for a weak
	// pushback marker ("again", "still", "wrong") to count on its own — a terse
	// nudge, not that word buried in a fresh multi-part instruction.
	correctionShortWords = 6
)

// correctionRE matches clear "the previous attempt was wrong / do it again"
// phrasing at any length. Over-matching is deliberately safe (mirrors
// turnLooksHard's asymmetry): a false "hard" only spends the strong model on an
// easy turn, while a missed one is the bug we're fixing — the cheap model
// looping on an unresolved issue.
var correctionRE = regexp.MustCompile(`(?i)\b(` +
	`still\s+(not|isn'?t|does\s?n'?t|do\s?n'?t|wo\s?n'?t|broken|failing|happening|there|wrong|the\s+same|persist(s|ing)?|occurr?(s|ing)?|nope|not\s+(fixed|working|solved|right))|` +
	`(did|does|do)\s?n'?t\s+(work|fix|help|solve|resolve|change)|` +
	`not\s+(fixed|working|solved|resolved|right|correct)|` +
	`same\s+(issue|problem|error|bug|thing|behaviou?r|result|output)|` +
	`(try|check|look|test|verify|read|run|do|go)\s+(it\s+|this\s+|that\s+)?again|` +
	`once\s+more|` +
	`re-?(check|read|examine|visit|do|try|run)|` +
	`you\s+(missed|forgot|overlooked|still)|` +
	`(that'?s|it'?s|this\s+is)\s+(wrong|incorrect|not\s+right|not\s+correct|the\s+same)|` +
	`keeps?\s+(happening|coming\s+back|occurr?ing|showing|failing|breaking)|` +
	`was\s?n'?t\s+(fixed|solved|resolved)` +
	`)\b`)

// weakCorrectionRE holds terse pushback tokens that only count on a short message
// (see correctionShortWords) — where they read as "that answer didn't land"
// rather than as part of a new request.
var weakCorrectionRE = regexp.MustCompile(`(?i)\b(again|still|nope|wrong)\b`)

// looksLikeCorrection reports whether userInput reads as dissatisfaction with, or
// a retry of, the previous answer — the intent signal the lexical
// looksLikeRepeatedAsk cannot see. Deterministic and cheap.
func looksLikeCorrection(userInput string) bool {
	s := strings.TrimSpace(userInput)
	if s == "" {
		return false
	}
	if correctionRE.MatchString(s) {
		return true
	}
	if len(strings.Fields(s)) <= correctionShortWords && weakCorrectionRE.MatchString(s) {
		return true
	}
	return false
}

// looksLikeRepeatedAsk reports whether userInput closely repeats one of the
// last repeatedAskLookback genuine user turns in messages, by significant-word
// (goalTerms) overlap. Deterministic and cheap, mirroring relevanceScore's
// lexical approach rather than a network embedding call.
func looksLikeRepeatedAsk(userInput string, messages []llm.Message) bool {
	cur := goalTerms(userInput)
	if len(cur) == 0 {
		return false
	}
	seen := 0
	for i := len(messages) - 1; i >= 0 && seen < repeatedAskLookback; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}
		text := userMessageTextHelper(messages[i])
		if text == "" {
			continue
		}
		seen++
		prior := goalTerms(text)
		if len(prior) == 0 {
			continue
		}
		shared := 0
		for t := range cur {
			if prior[t] {
				shared++
			}
		}
		if shared < repeatedAskMinSharedTerms {
			continue
		}
		if union := len(cur) + len(prior) - shared; float64(shared)/float64(union) >= repeatedAskJaccardMin {
			return true
		}
	}
	return false
}

func userMessageTextHelper(m llm.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
			sb.WriteByte(' ')
		}
	}
	return strings.TrimSpace(sb.String())
}
