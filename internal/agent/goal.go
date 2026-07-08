package agent

// Goal / stop-condition: a session-scoped stopping criterion. When a goal is
// set, the agent does not stop the moment the model emits no tool calls;
// instead an independent "judge" model evaluates the conversation against the
// goal's completion criteria. If the goal isn't truly satisfied, the judge's
// feedback is injected as a synthetic user turn and the loop continues —
// preventing premature "optimistic stops" during long, autonomous work.
//
// The goal carries three extra dimensions beyond the objective text:
//   - completion criteria: concrete, individually-verifiable checks the agent
//     proposes (and the user can amend); the judge must confirm each with
//     evidence before declaring the goal met.
//   - a token budget: an optional cap that stops the pursuit once spent.
//   - a "blocked" state: set when repeated turns exhaust their nudges without
//     progress, so the agent stops nudging instead of looping indefinitely.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
)

// maxGoalNudges bounds how many times a single turn may be nudged to keep
// working after the model tried to stop but the judge rejected it. It's a guard
// against a judge/model disagreement looping forever within one turn.
const maxGoalNudges = 3

// blockedThreshold is how many consecutive turns may exhaust their nudges
// without the judge confirming the goal before it's marked blocked.
const blockedThreshold = 3

// goalDirective is the boundary marker prefixing a judge nudge injected back
// into the conversation, so it's recognizable in history and transcripts.
const goalDirective = "[goal check] "

// loadGoalFromSession restores the goal + its criteria/budget/blocked state from
// the persisted session into the agent.
func (a *Agent) loadGoalFromSession() {
	if a.sess == nil {
		return
	}
	a.goal = a.sess.Goal
	a.goalCriteria = a.sess.GoalCriteria
	a.goalBudgetTokens = a.sess.GoalBudgetTokens
	a.goalTokensUsed = a.sess.GoalTokensUsed
	a.goalBlocked = a.sess.GoalBlocked
	a.goalNotMetStreak = a.sess.GoalNotMetStreak
}

// saveGoalToSession mirrors the agent's goal state onto the session for persist.
func (a *Agent) saveGoalToSession() {
	if a.sess == nil {
		return
	}
	a.sess.Goal = a.goal
	a.sess.GoalCriteria = a.goalCriteria
	a.sess.GoalBudgetTokens = a.goalBudgetTokens
	a.sess.GoalTokensUsed = a.goalTokensUsed
	a.sess.GoalBlocked = a.goalBlocked
	a.sess.GoalNotMetStreak = a.goalNotMetStreak
}

// clearGoalState wipes all goal state (used by Reset and /goal clear).
func (a *Agent) clearGoalState() {
	a.goal = ""
	a.goalCriteria = nil
	a.goalBudgetTokens = 0
	a.goalTokensUsed = 0
	a.goalBlocked = false
	a.goalNotMetStreak = 0
}

// SetGoal sets (or, with an empty string, clears) the session stop-condition.
// Setting a new objective resets the criteria and progress (criteria belong to
// the old goal) but keeps any token budget the user configured. While set, a
// turn won't end until the judge confirms the goal is met. Persisted with the
// session so it survives resume.
func (a *Agent) SetGoal(goal string) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		a.clearGoalState()
	} else {
		a.goal = goal
		a.goalCriteria = nil // re-proposed for the new objective
		a.goalTokensUsed = 0
		a.goalBlocked = false
		a.goalNotMetStreak = 0
	}
	if a.sess != nil {
		a.saveGoalToSession()
		a.persist()
	}
}

// Goal returns the active stop-condition objective, or "" when none is set.
func (a *Agent) Goal() string {
	if a.goal == "" && a.sess != nil {
		// A resumed session may carry a goal the agent wasn't constructed with.
		return a.sess.Goal
	}
	return a.goal
}

// SetGoalCriteria replaces the goal's completion criteria with a user-supplied
// list (blank entries dropped). It resets the blocked state so judging starts
// fresh against the new criteria.
func (a *Agent) SetGoalCriteria(criteria []string) {
	clean := make([]string, 0, len(criteria))
	for _, c := range criteria {
		if c = strings.TrimSpace(c); c != "" {
			clean = append(clean, c)
		}
	}
	a.goalCriteria = clean
	a.goalBlocked = false
	a.goalNotMetStreak = 0
	if a.sess != nil {
		a.saveGoalToSession()
		a.persist()
	}
}

// GoalCriteria returns the current completion criteria.
func (a *Agent) GoalCriteria() []string { return a.goalCriteria }

// SplitCriteria parses a user-entered criteria string (separated by ';' or
// newlines) into a trimmed, blank-free slice — used by the /goal criteria
// command on every surface.
func SplitCriteria(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SetGoalBudget sets (or, with <=0, clears) the token budget for the goal.
func (a *Agent) SetGoalBudget(tokens int) {
	if tokens < 0 {
		tokens = 0
	}
	a.goalBudgetTokens = tokens
	if a.sess != nil {
		a.saveGoalToSession()
		a.persist()
	}
}

// addGoalTokens accrues a turn's token usage toward the goal's budget (only
// while a goal is active).
func (a *Agent) addGoalTokens(u llm.Usage) {
	if a.Goal() == "" {
		return
	}
	a.goalTokensUsed += u.InputTokens + u.OutputTokens
}

// goalBudgetExceeded reports whether the goal's token budget (if any) is spent.
func (a *Agent) goalBudgetExceeded() bool {
	return a.goalBudgetTokens > 0 && a.goalTokensUsed >= a.goalBudgetTokens
}

// noteGoalMet records that the judge confirmed the goal: the blocked streak
// resets.
func (a *Agent) noteGoalMet() {
	a.goalNotMetStreak = 0
	a.goalBlocked = false
}

// noteGoalGaveUp records a turn that exhausted its nudges without the judge
// confirming the goal, marking it blocked once the streak crosses the
// threshold. Returns true if the goal just became blocked.
func (a *Agent) noteGoalGaveUp() bool {
	a.goalNotMetStreak++
	if !a.goalBlocked && a.goalNotMetStreak >= blockedThreshold {
		a.goalBlocked = true
		return true
	}
	return false
}

// judgeVerdict is the structured outcome of a goal evaluation.
type judgeVerdict struct {
	met      bool
	feedback string
}

// ensureGoalCriteria proposes completion criteria the first time a goal is
// checked without any (so the user sees concrete, verifiable checks). A failure
// to generate them is non-fatal — the judge falls back to the objective text.
func (a *Agent) ensureGoalCriteria(ctx context.Context, emit func(Event)) {
	if a.Goal() == "" || len(a.goalCriteria) > 0 || a.client == nil {
		return
	}
	crit := a.proposeCriteria(ctx)
	if len(crit) == 0 {
		return
	}
	a.goalCriteria = crit
	a.persist()
	emit(Info{Text: "Goal completion criteria (edit with /goal criteria <a; b; c>):\n" + formatCriteria(crit)})
}

// proposeCriteria asks the aux model to break the goal objective into a few
// concrete, individually-verifiable completion criteria.
func (a *Agent) proposeCriteria(ctx context.Context) []string {
	goal := a.Goal()
	if goal == "" || a.client == nil {
		return nil
	}
	const sys = "You turn a coding-agent goal into completion criteria. Output 2-5 " +
		"concrete, individually-verifiable criteria that together mean the goal is done. " +
		"Each must be a single checkable statement (a file/command/test/observable " +
		"behavior could prove it) — atomic, no 'and'/'or'. Output ONE criterion per line, " +
		"plain text, no numbering, bullets, or commentary."
	jctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := a.streamAux(jctx, sys, []llm.Message{llm.UserText("GOAL:\n" + clipText(goal, 2000))})
	if err != nil {
		return nil
	}
	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	var crit []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*0123456789.) \t")
		if line != "" {
			crit = append(crit, line)
		}
		if len(crit) >= 5 {
			break
		}
	}
	return crit
}

// evaluateGoal asks an independent judge model whether the goal's completion
// criteria have been satisfied by the conversation so far. It runs with no
// tools and a short timeout; any error is treated as "undecided" (met=false)
// with the error as feedback, so a transient judge failure never silently ends
// an unfinished task. When no goal is set it returns met=true immediately.
func (a *Agent) evaluateGoal(ctx context.Context) judgeVerdict {
	goal := a.Goal()
	if goal == "" || a.client == nil {
		return judgeVerdict{met: true}
	}

	const sys = "You are an impartial judge evaluating whether a coding agent has " +
		"fully satisfied a user-specified goal. You are NOT the agent; do not do the " +
		"work or be charitable. Check EACH completion criterion against concrete " +
		"evidence in the conversation (files written, commands run, test/build output, " +
		"observed behavior). A criterion with no such evidence is NOT met.\n\n" +
		"Reply on the FIRST line with exactly DONE (every criterion is met with " +
		"evidence) or NOT_DONE. If NOT_DONE, add one or two short sentences naming the " +
		"specific criteria that remain and what evidence is missing. Be concrete and " +
		"actionable. Do not restate the whole goal."

	var b strings.Builder
	b.WriteString("GOAL (stop condition):\n")
	b.WriteString(clipText(goal, 2000))
	if len(a.goalCriteria) > 0 {
		b.WriteString("\n\nCOMPLETION CRITERIA (all must be met with evidence):\n")
		b.WriteString(formatCriteria(a.goalCriteria))
	}
	b.WriteString("\n\nRECENT CONVERSATION (the agent just tried to stop):\n")
	b.WriteString(serialize(a.recentForJudge()))
	b.WriteString("\n\nIs every criterion met with evidence? Answer DONE or NOT_DONE.")

	jctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	resp, err := a.streamAux(jctx, sys, []llm.Message{llm.UserText(b.String())})
	if err != nil {
		return judgeVerdict{met: false, feedback: "the goal-check judge could not be reached (" + err.Error() + "); continuing"}
	}

	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	return parseVerdict(out.String())
}

// recentForJudge returns the tail of the conversation to show the judge,
// bounded so the evaluation stays cheap. The judge only needs recent context
// (the goal itself is supplied separately).
func (a *Agent) recentForJudge() []llm.Message {
	const keep = 24
	if len(a.messages) <= keep {
		return a.messages
	}
	return a.messages[len(a.messages)-keep:]
}

// parseVerdict interprets the judge's reply. The first token decides the
// outcome (DONE vs NOT_DONE); the remainder is feedback. Anything that doesn't
// clearly say DONE is treated as not-done so the agent errs toward finishing
// the work rather than stopping early.
func parseVerdict(s string) judgeVerdict {
	trimmed := strings.TrimSpace(s)
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "NOT_DONE"), strings.HasPrefix(upper, "NOT DONE"):
		fb := strings.TrimSpace(trimmed[min(len("NOT_DONE"), len(trimmed)):])
		fb = strings.TrimLeft(fb, ":–-. ")
		if fb == "" {
			fb = "the goal is not yet fully met; keep working."
		}
		return judgeVerdict{met: false, feedback: fb}
	case strings.HasPrefix(upper, "DONE"):
		return judgeVerdict{met: true}
	default:
		// Ambiguous reply: treat the whole thing as remaining-work feedback.
		fb := trimmed
		if fb == "" {
			fb = "the goal status is unclear; verify it is fully met before stopping."
		}
		return judgeVerdict{met: false, feedback: fb}
	}
}

// goalNudgeMessage builds the synthetic user turn that nudges the agent to keep
// working when the judge rejects a stop. It's phrased as guidance from the
// stop-condition checker, not the user.
func goalNudgeMessage(feedback string) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"%sThe stop condition for this session is not yet satisfied. %s\n\n"+
			"Continue working toward the goal. Do not ask the user to confirm; only stop "+
			"once the goal is genuinely complete.", goalDirective, feedback))
}

// formatCriteria renders criteria as a numbered checklist.
func formatCriteria(criteria []string) string {
	var b strings.Builder
	for i, c := range criteria {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, c)
	}
	return strings.TrimRight(b.String(), "\n")
}

// GoalStatus returns a human description of the goal state for /goal with no
// argument, including criteria, token budget, and any blocked state.
func (a *Agent) GoalStatus() string {
	g := a.Goal()
	if g == "" {
		return "No goal set. Use /goal <description> to set a stop condition the agent must satisfy before it stops. " +
			"Then /goal budget <tokens> caps the effort, /goal criteria <a; b; c> overrides the auto-proposed criteria, and /goal clear removes it."
	}
	var b strings.Builder
	b.WriteString("Goal (stop condition): " + g + "\n")
	if len(a.goalCriteria) > 0 {
		b.WriteString("Completion criteria:\n" + formatCriteria(a.goalCriteria) + "\n")
	} else {
		b.WriteString("Completion criteria: (auto-proposed on the first check; set your own with /goal criteria <a; b; c>)\n")
	}
	if a.goalBudgetTokens > 0 {
		b.WriteString(fmt.Sprintf("Token budget: %d used / %d (stops when reached; /goal budget off to remove)\n", a.goalTokensUsed, a.goalBudgetTokens))
	} else {
		b.WriteString(fmt.Sprintf("Token budget: none (%d used so far; /goal budget <n> to cap)\n", a.goalTokensUsed))
	}
	if a.goalBlocked {
		b.WriteString("Status: BLOCKED — repeatedly judged unmet without progress; refine the goal or /goal clear.\n")
	}
	b.WriteString("The agent won't stop until a judge confirms every criterion is met. /goal clear to remove it.")
	return b.String()
}
