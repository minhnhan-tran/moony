package agent

import (
	"fmt"
	"time"

	"github.com/pysty1995/moony/internal/budget"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/notify"
)

// finishTurn finalizes a turn's cost/usage bookkeeping — recording turn meta
// and task/board history, then emitting TurnDone — and feeds the turn's real
// final cost into two cross-session concerns the per-turn-only
// turnWarnThresholds/turnHardLimits (agent_approval.go) can't see:
//
//   - the cross-session budget ledger (internal/budget), warning via Info if
//     a configured MN_DAILY_BUDGET/MN_MONTHLY_BUDGET is at/over.
//   - an outbound "turn_done" notification (internal/notify: webhook and/or
//     the TUI's bell), when the turn ran long enough to be worth flagging.
//
// This used to be five duplicated lines at three separate return points
// (normal completion, hard-limit stop, goal-budget stop); consolidating them
// here is what makes the two new behaviors a one-place change instead of
// three.
func (a *Agent) finishTurn(
	emit func(Event), turnStart time.Time, respUsage, turnUsage llm.Usage,
	turnCostUSD, turnGuardCostUSD float64, rounds int, turnModel, turnReasoning string,
	taskAcct *taskMetaAcct, board *messageBoard,
) float64 {
	finalCostUSD := turnCostUSD + a.auxCostUSD()
	finalGuardCostUSD := turnGuardCostUSD + a.auxCostUSD()
	a.recordTurnMeta(turnStart, turnUsage, finalCostUSD, rounds, turnModel, turnReasoning)
	a.recordTaskMetas(taskAcct)
	a.recordBoardMessages(board)
	emit(TurnDone{Usage: respUsage, TurnUsage: turnUsage, CostUSD: finalCostUSD,
		GuardCostUSD: finalGuardCostUSD, Model: turnModel, Reasoning: turnReasoning})

	a.warnBudget(emit, finalCostUSD)
	elapsed := time.Since(turnStart)
	notify.Send(notify.TurnDone, fmt.Sprintf("moony: turn finished in %s (~$%.2f, %d model call(s)).", elapsed.Round(time.Second), finalCostUSD, rounds), elapsed)

	return finalCostUSD
}

// warnBudget records this turn's cost in the cross-session ledger and emits
// an advisory Info once the running daily and/or monthly total is at/over a
// configured MN_DAILY_BUDGET/MN_MONTHLY_BUDGET. Warn-only: it never
// blocks or ends the turn, and a ledger I/O error just skips the check.
func (a *Agent) warnBudget(emit func(Event), turnCost float64) {
	dailyCap, monthlyCap := budget.DailyBudget(), budget.MonthlyBudget()
	if dailyCap <= 0 && monthlyCap <= 0 {
		return
	}
	daily, monthly, err := budget.Record(turnCost)
	if err != nil {
		return
	}
	overDaily := dailyCap > 0 && daily >= dailyCap
	overMonthly := monthlyCap > 0 && monthly >= monthlyCap
	switch {
	case overDaily && overMonthly:
		emit(Info{Text: fmt.Sprintf("Heads up: today's spend is ~$%.2f (daily budget $%.2f) and this month's is ~$%.2f (monthly budget $%.2f).", daily, dailyCap, monthly, monthlyCap)})
	case overDaily:
		emit(Info{Text: fmt.Sprintf("Heads up: today's spend is ~$%.2f, at/over the $%.2f daily budget (MN_DAILY_BUDGET).", daily, dailyCap)})
	case overMonthly:
		emit(Info{Text: fmt.Sprintf("Heads up: this month's spend is ~$%.2f, at/over the $%.2f monthly budget (MN_MONTHLY_BUDGET).", monthly, monthlyCap)})
	}
}
