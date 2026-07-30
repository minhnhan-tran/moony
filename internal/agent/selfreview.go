package agent

// selfreview.go implements an optional self-review pass: before a turn that
// edited files is allowed to finish, a cheap fast-model reviewer reads the turn's
// OWN diff and flags likely introduced bugs (logic errors, wrong variable,
// inverted condition, missing error handling, …). A flagged issue is fed back
// once so the agent fixes it before stopping. Complementary to the verify loop
// (which runs the build/test) — this catches logic bugs a green build wouldn't.
//
// Off by default (MN_SELF_REVIEW=1 / cfg.SelfReview). Gated on the turn actually
// changing code, bounded to a single feedback cycle, and skipped for sub-agents
// (subConfig clears cfg.SelfReview). Mirrors the verify gate's wiring in RunWith.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/diff"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
)

const (
	// maxSelfReviewFeedback caps self-review to one feedback cycle per turn —
	// review findings are softer than a build failure, so we fix once and finish
	// rather than risk subjective back-and-forth (and extra cost).
	maxSelfReviewFeedback = 1
	// maxReviewDiffBytes bounds the diff sent to the reviewer so a huge change set
	// can't blow up the (cheap, but not free) review call.
	maxReviewDiffBytes = 24000
)

// reviewableDiff renders a unified before→after diff of just the files the
// current turn changed, for the self-reviewer. Returns "" when nothing changed.
func (a *Agent) reviewableDiff() string { return a.combinedTurnDiff(maxReviewDiffBytes) }

// TurnChangesDiff returns one combined unified diff of every file changed in the
// current turn, capped to maxBytes (<=0 picks a sane default) — for the
// review_changes tool and the /changes command. "" when nothing changed.
func (a *Agent) TurnChangesDiff(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxReviewDiffBytes
	}
	return a.combinedTurnDiff(maxBytes)
}

// combinedTurnDiff renders ONE unified before→after diff covering every file the
// current turn changed, bounded to maxBytes. It backs both the self-reviewer
// (reviewableDiff) and the read-only review_changes tool / the /changes command,
// so all three agree on exactly what "this turn changed" means. "" when nothing
// net-changed.
func (a *Agent) combinedTurnDiff(maxBytes int) string {
	if a.snapshots == nil {
		return ""
	}
	changes := a.snapshots.currentTurnChanges()
	if len(changes) == 0 {
		return ""
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].path < changes[j].path })

	var b strings.Builder
	truncated := false
	for _, c := range changes {
		remaining := maxBytes - b.Len()
		if remaining <= 0 {
			truncated = true
			break
		}
		old := ""
		if !c.missing {
			old = string(c.prior)
		}
		newText := ""
		if data, err := os.ReadFile(c.path); err == nil {
			newText = string(data)
		}
		d, _ := diff.Unified(old, newText)
		if strings.TrimSpace(d) == "" {
			continue // net no-op (e.g. edited then reverted within the turn)
		}
		note := ""
		if c.missing {
			note = " (new file)"
		}
		header := fmt.Sprintf("### %s%s\n", mustRel(a.cfg.WorkingDir, c.path), note)
		// Clip this file's diff to the remaining budget so a single huge file can't
		// blow past the cap — the byte budget bounds the whole review payload, not
		// just the number of files included.
		if budget := remaining - len(header); budget < len(d) {
			d = clipTo(d, budget)
			truncated = true
		}
		if strings.TrimSpace(d) == "" {
			truncated = true
			break // no room left for even a clipped diff of this file
		}
		b.WriteString(header)
		b.WriteString(d)
		b.WriteString("\n\n")
		if truncated {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if truncated {
		out += "\n\n[diff truncated for review]"
	}
	return out
}

// clipTo truncates s to at most n bytes, backing up to the last newline so it
// doesn't end mid-line. Returns "" when n <= 0.
func clipTo(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[:n]
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[:i+1]
	} else {
		// No newline found; trim any trailing partial UTF-8 sequence
		for len(s) > 0 && !utf8.RuneStart(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
		if len(s) > 0 {
			s = s[:len(s)-1] // Remove the starting byte of the split rune as well
		}
	}
	return s
}

// runSelfReview asks the fast-model reviewer to inspect the diff. It returns
// ok=true (no real issues) when the reviewer says LGTM, the reviewer can't be
// reached, or the diff is empty — erring toward letting the turn finish rather
// than blocking on a flaky/absent reviewer. Otherwise ok=false with the findings.
// usage is the reviewer call's real token usage (zero when it didn't run).
func (a *Agent) runSelfReview(ctx context.Context, d string) (ok bool, findings string, usage llm.Usage) {
	if a.client == nil || strings.TrimSpace(d) == "" {
		return true, "", llm.Usage{}
	}
	const sys = "You are a meticulous senior engineer reviewing a diff a coding " +
		"agent just produced, looking ONLY for real bugs the change INTRODUCES: " +
		"logic errors, off-by-one, a wrong or blanked-out variable, an inverted " +
		"condition, missing error/nil handling, resource leaks, a call that won't " +
		"compile (wrong arity/name), or leftover debug code. IGNORE style, naming, " +
		"formatting, missing tests, and pre-existing issues this diff didn't cause. " +
		"Precision matters far more than recall: if you are not confident a line is " +
		"a real introduced bug, say nothing.\n\n" +
		"Reply on the FIRST line with exactly LGTM (no real introduced bug) or " +
		"ISSUES. If ISSUES, follow with one bullet per problem: `- path:line — " +
		"what's wrong and why`. Do not summarize the diff or suggest improvements."
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := a.streamAux(rctx, sys, []llm.Message{llm.UserText("Review this diff for introduced bugs:\n\n" + d)})
	if err != nil {
		return true, "", llm.Usage{} // reviewer unreachable — don't block the turn
	}
	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	u := resp.Usage
	u.InputTokens += resp.RetryUsage.InputTokens
	u.OutputTokens += resp.RetryUsage.OutputTokens
	ok, findings = parseReview(out.String())
	return ok, findings, u
}

// parseReview interprets the reviewer's reply: the first token decides the
// outcome. A clear LGTM (or an empty/garbled reply — so a confused reviewer can't
// wedge the turn) is ok; anything that says ISSUES or lists bullets is treated as
// findings, carried back verbatim.
func parseReview(s string) (ok bool, findings string) {
	t := strings.TrimSpace(s)
	if t == "" {
		return true, ""
	}
	first := t
	if i := strings.IndexAny(t, " \t\r\n"); i >= 0 {
		first = t[:i]
	}
	first = strings.TrimRight(first, ".:!,")
	if strings.EqualFold(first, "LGTM") {
		return true, ""
	}
	// Not a clear LGTM: only treat as issues if it actually flagged something
	// (said ISSUES or listed a bullet), else err toward finishing.
	if !strings.EqualFold(first, "ISSUES") && !strings.Contains(t, "- ") {
		return true, ""
	}
	return false, t
}

// maybeSelfReview runs the optional self-review gate when a turn that edited code
// tries to finish. Returns true when the caller should CONTINUE the loop (a
// finding was fed back to fix), false to let the turn finish (disabled, nothing
// edited, clean, or the one feedback cycle already spent). attempts is the
// per-turn counter.
func (a *Agent) maybeSelfReview(ctx context.Context, emit func(Event), attempts *int) bool {
	if !a.cfg.SelfReview {
		return false
	}
	if a.snapshots == nil || !a.snapshots.mutatedThisTurn() {
		return false
	}
	if *attempts >= maxSelfReviewFeedback {
		return false // already reviewed + fed back once this turn; don't loop
	}
	d := a.reviewableDiff()
	if d == "" {
		return false
	}
	// A real textual diff isn't necessarily reviewable: a release turn that only
	// bumps const Version and writes the CHANGELOG has nothing a bug-reviewer can
	// act on, so skip the model call instead of paying for a certain "no issues
	// found". Any real source file mixed in still gets reviewed.
	if skip, why := a.turnIsNonReviewable(); skip {
		note := "Skipped self-review: " + why + "."
		emit(Info{Text: note})
		a.noteInfo(note)
		return false
	}
	reviewModel, reviewProv, _ := config.AuxModelRoute(a.cfg.Model, a.cfg.LLMProvider)
	// Name the provider + reasoning the review actually runs at, like the
	// downshift notice. Provider defaults to the session provider; aux forces
	// reasoning OFF unless MN_AUX_REASONING pins a level (see streamAux).
	if reviewProv == "" {
		reviewProv = a.cfg.LLMProvider
	}
	reviewProvLabel := reviewProv
	if p, ok := provider.Lookup(reviewProv); ok {
		reviewProvLabel = p.Label
	}
	reviewReasoning := config.AuxReasoning()
	if reviewReasoning == "" {
		reviewReasoning = config.ReasoningOff
	}
	reviewingText := fmt.Sprintf("Self-reviewing the diff for introduced bugs… (%s on %s, reasoning: %s)", reviewModel, reviewProvLabel, reviewReasoning)
	emit(Info{Text: reviewingText})
	a.noteInfo(reviewingText)
	ok, findings, usage := a.runSelfReview(ctx, d)
	meta := selfReviewMeta(reviewModel, reviewProvLabel, reviewReasoning, usage)
	if ok {
		doneText := "Self-review: no issues found. (" + meta + ")"
		emit(Info{Text: doneText})
		a.noteInfo(doneText)
		return false
	}
	*attempts++
	a.turnFriction.Add(1) // a flagged regression is friction: the turn hasn't converged
	flaggedText := "Self-review flagged possible issues — feeding them back to fix. (" + meta + ")"
	emit(Info{Text: flaggedText})
	a.noteInfo(flaggedText)
	a.messages = append(a.messages, selfReviewFeedbackMessage(findings))
	a.persist()
	return true
}

// selfReviewFeedbackMessage builds the synthetic user turn carrying the reviewer's
// findings back to the model so it fixes them before finishing.
func selfReviewFeedbackMessage(findings string) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"[automatic self-review] A review of the changes you just made flagged "+
			"possible bugs:\n\n%s\n\nIf a point is a real bug you introduced, fix it; "+
			"if it's a false alarm, briefly say why, then finish. Don't ask the user "+
			"to confirm.",
		strings.TrimSpace(findings)))
}

// shortModelName trims a hosted model id to a compact, human-friendly label
// for status lines: drops the cloud-provider prefix (anthropic./gcp:) and the
// trailing date/version suffix, e.g.
// "anthropic.claude-haiku-4-5-20251001-v1:0" -> "claude-haiku-4-5".
func shortModelName(model string) string {
	m := model
	if i := strings.IndexAny(m, ".:"); i >= 0 {
		m = m[i+1:]
	}
	// Drop a trailing "-YYYYMMDD..." date/version stamp if present.
	if i := strings.Index(m, "-2"); i > 0 {
		if rest := m[i+1:]; len(rest) >= 9 && isAllDigits(rest[:8]) {
			m = m[:i]
		}
	}
	if m == "" {
		return model
	}
	return m
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// selfReviewMeta formats the reviewer call's model, token usage, and real cost
// for a status line, e.g. "claude-haiku-4-5 · 1.2k↑ 340↓ · $0.0021". Falls back
// to just the model name when no usage was recorded (reviewer skipped/unreachable).
// selfReviewMeta formats the reviewer call's model, provider, reasoning level,
// token usage, and real cost for a status line, e.g. "claude-haiku-4-5 on
// Anthropic, reasoning: off · 1.2k↑ 340↓ · $0.0021" — the same provider/
// reasoning naming as the "Self-reviewing…" starting notice, so the result
// line can be attributed to a specific tier/config too. Falls back to just
// "model on provider, reasoning: level" when no usage was recorded (reviewer
// skipped/unreachable).
func selfReviewMeta(model, providerLabel, reasoning string, u llm.Usage) string {
	name := shortModelName(model)
	head := fmt.Sprintf("%s on %s, reasoning: %s", name, providerLabel, reasoning)
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return head
	}
	return fmt.Sprintf("%s · %s↑ %s↓ · $%.4f",
		head, compactTokens(u.InputTokens), compactTokens(u.OutputTokens), u.CostUSD(model))
}

// compactTokens renders a token count compactly: 1234 -> "1.2k", 950 -> "950".
func compactTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
