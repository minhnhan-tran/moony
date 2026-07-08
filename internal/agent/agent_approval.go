// agent_approval.go — turn-budget thresholds and the approval / question / exit-plan request flow.

package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// parallelToolConfig reports whether independent tool calls in a turn may run
// concurrently, and the maximum concurrency. Controlled by MOONY_PARALLEL_TOOLS:
//
//	unset / "on" / "true"   → enabled at the default limit
//	"0" / "off" / "false"   → disabled (calls run sequentially)
//	a positive integer N    → enabled with a limit of N (capped at 64)
func parallelToolConfig() (enabled bool, limit int) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("MOONY_PARALLEL_TOOLS"))); v {
	case "", "on", "true", "yes":
		return true, defaultParallelToolLimit
	case "0", "off", "false", "no":
		return false, 0
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return true, defaultParallelToolLimit
		}
		if n > 64 {
			n = 64
		}
		return true, n
	}
}

// turnWarnThresholds returns the per-turn soft budgets at which the agent warns
// about a possibly-runaway turn: a model-call count and a USD cost. Defaults are
// 50 calls and $5.00; override with MOONY_TURN_WARN_CALLS / MOONY_TURN_WARN_COST
// (set either to 0 to disable that warning). The warnings are advisory — they
// never stop the turn (the step ceiling does that); they just surface a long,
// expensive turn so the user can interrupt before it burns more budget.
func turnWarnThresholds() (calls int, cost float64) {
	calls, cost = 50, 5.0
	if v := strings.TrimSpace(os.Getenv("MOONY_TURN_WARN_CALLS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			calls = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOONY_TURN_WARN_COST")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cost = f
		}
	}
	return calls, cost
}

// turnHardLimits returns the per-turn HARD budgets at which the agent STOPS the
// turn (unlike turnWarnThresholds, which only warns). Both are OFF by default
// (0); set MOONY_TURN_MAX_CALLS (model calls) and/or MOONY_TURN_MAX_COST (USD) to
// enable. A hit ends the turn cleanly — the session resumes with "continue".
func turnHardLimits() (calls int, cost float64) {
	if v := strings.TrimSpace(os.Getenv("MOONY_TURN_MAX_CALLS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			calls = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOONY_TURN_MAX_COST")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cost = f
		}
	}
	return calls, cost
}

// turnHardStopReason returns a non-empty stop message when this turn has crossed
// a configured hard limit (calls or cost), or "" otherwise. rounds is the number
// of model calls made so far this turn; cost is the REAL accumulated turn cost
// (see turnCostUSD in RunWith — priced per call at whichever model actually
// served it, not the session model's rate for every token).
func (a *Agent) turnHardStopReason(maxCalls int, maxCost float64, rounds int, cost float64) string {
	if maxCalls > 0 && rounds >= maxCalls {
		return fmt.Sprintf("Stopping this turn: reached the hard limit of %d model calls (MOONY_TURN_MAX_CALLS). Send \"continue\" to resume, or raise/clear the limit.", maxCalls)
	}
	if maxCost > 0 && cost >= maxCost {
		return fmt.Sprintf("Stopping this turn: reached the hard cost limit of $%.2f (MOONY_TURN_MAX_COST) — ~$%.2f spent over %d calls. Send \"continue\" to resume, or raise/clear the limit.", maxCost, cost, rounds)
	}
	return ""
}

// requestApproval emits an ApprovalRequest and blocks for the decision.
func (a *Agent) requestApproval(ctx context.Context, emit func(Event), tool, summary, diff string) Decision {
	reply := make(chan Decision, 1)
	emit(ApprovalRequest{Tool: tool, Summary: summary, Diff: diff, Reply: reply})
	select {
	case d := <-reply:
		return d
	case <-ctx.Done():
		return Deny
	}
}

// requestQuestion emits a QuestionRequest (the ask_user tool) and blocks for the
// user's answer, returning the chosen option label(s). A cancelled context
// aborts the ask with an error so the turn unwinds cleanly.
func (a *Agent) requestQuestion(ctx context.Context, emit func(Event), req tools.AskRequest) ([]string, error) {
	reply := make(chan []string, 1)
	opts := make([]QuestionOption, 0, len(req.Options))
	for _, o := range req.Options {
		opts = append(opts, QuestionOption{Label: o.Label, Description: o.Description})
	}
	emit(QuestionRequest{Question: req.Question, Options: opts, Multiple: req.Multiple, AllowText: req.AllowText, Reply: reply})
	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// requestQuestionGroup handles the ask_user tool's questions[] form. When the
// surface can render a grouped panel (the web tabs UI, gated by SetQuestionGroups)
// it emits ONE QuestionGroupRequest and blocks for every answer at once; on any
// other surface (the TUI, sub-agents) it falls back to asking each question in
// turn via requestQuestion, assembling the same per-question answer slices — so
// those surfaces need no grouped-panel handling. A cancelled context aborts the
// ask so the turn unwinds cleanly.
func (a *Agent) requestQuestionGroup(ctx context.Context, emit func(Event), reqs []tools.AskRequest) ([][]string, error) {
	if !a.groupedQuestions {
		out := make([][]string, 0, len(reqs))
		for _, r := range reqs {
			ans, err := a.requestQuestion(ctx, emit, r)
			if err != nil {
				return nil, err
			}
			out = append(out, ans)
		}
		return out, nil
	}
	qs := make([]QuestionRequest, 0, len(reqs))
	for _, r := range reqs {
		opts := make([]QuestionOption, 0, len(r.Options))
		for _, o := range r.Options {
			opts = append(opts, QuestionOption{Label: o.Label, Description: o.Description})
		}
		qs = append(qs, QuestionRequest{Question: r.Question, Options: opts, Multiple: r.Multiple, AllowText: r.AllowText})
	}
	reply := make(chan [][]string, 1)
	emit(QuestionGroupRequest{Questions: qs, Reply: reply})
	select {
	case ans := <-reply:
		return ans, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// exitPlanApproveLabel is the option a user picks to approve leaving plan mode.
const exitPlanApproveLabel = "Approve & build"

// requestExitPlan handles the exit_plan tool. When plan mode is on it asks the
// user — via a QuestionRequest, reusing the ask_user prompt UI on every surface
// — to approve leaving read-only mode. On approval it marks the plan approved
// (when one is drafted), clears plan mode so the mutating tools become visible
// again, and emits PlanUpdated so the UIs refresh; the same turn then continues
// into execution. It returns whether the agent should proceed in build mode.
// When plan mode is off AND no drafted plan is awaiting approval there is
// nothing to confirm, so it returns approved without prompting. A pending draft
// (even with plan mode off) still requires confirmation, so the model can't
// approve and execute its own freshly drafted plan unprompted.
func (a *Agent) requestExitPlan(ctx context.Context, emit func(Event), req tools.ExitPlanRequest) (bool, error) {
	if !a.PlanMode() && !a.hasDraftPlan() {
		return true, nil
	}
	// Leaving plan mode to execute the plan must be user-approved. With no
	// interactive approver (headless, sub-agent) we stay in plan mode rather
	// than silently executing.
	if !a.interactive {
		return false, nil
	}
	question := "Ready to leave read-only plan mode and start making changes?"
	if req.Summary != "" {
		question = req.Summary + "\n\nApprove to switch to build mode and carry out the plan?"
	}
	reply := make(chan []string, 1)
	emit(QuestionRequest{
		Question: question,
		Options: []QuestionOption{
			{Label: exitPlanApproveLabel, Description: "Approve the plan, re-enable mutating tools, and execute it now."},
			{Label: "Keep planning", Description: "Stay in read-only plan mode and keep refining the plan."},
		},
		Reply: reply,
	})
	var ans []string
	select {
	case ans = <-reply:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	approved := false
	for _, label := range ans {
		if strings.TrimSpace(label) == exitPlanApproveLabel {
			approved = true
			break
		}
	}
	if !approved {
		return false, nil
	}
	// Approve the drafted plan (no-op when none exists) and leave plan mode so
	// the mutating tools reappear for the rest of this turn.
	if a.ApprovePlan() {
		emit(PlanUpdated{Plan: a.Plan()})
	}
	a.active.Store(config.BuildAgent())
	return true, nil
}
