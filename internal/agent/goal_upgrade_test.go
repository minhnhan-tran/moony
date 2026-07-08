package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// TestGoalCriteriaProposedOnFirstCheck verifies the agent auto-proposes
// completion criteria the first time a goal is checked (when none are set), and
// then judges against them.
func TestGoalCriteriaProposedOnFirstCheck(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	// Calls: 1) model end_turn, 2) criteria proposal (aux), 3) judge DONE (aux).
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("all done")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("tests pass\nthe endpoint returns 200\ndocs updated")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("DONE")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetGoal("add the endpoint")

	var sawCriteriaInfo bool
	if err := ag.Run(context.Background(), "go", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(strings.ToLower(e.Text), "completion criteria") {
			sawCriteriaInfo = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	crit := ag.GoalCriteria()
	if len(crit) != 3 || crit[0] != "tests pass" {
		t.Errorf("expected 3 proposed criteria parsed from the aux reply, got %v", crit)
	}
	if !sawCriteriaInfo {
		t.Error("expected an Info event surfacing the proposed criteria")
	}
}

// TestGoalTokenBudgetStops verifies the turn stops as soon as the goal's token
// budget is spent, before any further work.
func TestGoalTokenBudgetStops(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	// One model round whose usage alone blows the budget; it has a tool call so
	// the turn would otherwise continue — the budget check must cut it off.
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "noop", `{}`)}, Usage: llm.Usage{InputTokens: 500, OutputTokens: 100}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetGoal("do a lot of work")
	ag.SetGoalBudget(100)

	var sawBudget bool
	if err := ag.Run(context.Background(), "go", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "token budget reached") {
			sawBudget = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawBudget {
		t.Error("expected a budget-reached Info that stopped the turn")
	}
	if fake.calls != 1 {
		t.Errorf("turn should stop after the first round once the budget is blown, got %d calls", fake.calls)
	}
}

// TestGoalBlockedAfterRepeatedGiveups verifies the blocked state is set once the
// give-up streak crosses the threshold, and that confirming the goal resets it.
func TestGoalBlockedAfterRepeatedGiveups(t *testing.T) {
	a := &Agent{}
	for i := 1; i < blockedThreshold; i++ {
		if a.noteGoalGaveUp() {
			t.Fatalf("became blocked too early at give-up %d", i)
		}
		if a.goalBlocked {
			t.Fatalf("goalBlocked set too early at give-up %d", i)
		}
	}
	if !a.noteGoalGaveUp() {
		t.Fatalf("expected to become blocked at give-up %d", blockedThreshold)
	}
	if !a.goalBlocked {
		t.Error("goalBlocked should be true after the threshold")
	}
	a.noteGoalMet()
	if a.goalBlocked || a.goalNotMetStreak != 0 {
		t.Error("confirming the goal should reset the blocked state and streak")
	}
}

// TestSetGoalResetsCriteriaAndProgress verifies a new objective clears the old
// criteria/progress while /goal clear wipes everything including the budget.
func TestSetGoalResetsCriteriaAndProgress(t *testing.T) {
	a := &Agent{}
	a.SetGoal("first")
	a.SetGoalCriteria([]string{"a", "b"})
	a.SetGoalBudget(1000)
	a.goalTokensUsed = 500
	a.goalBlocked = true

	a.SetGoal("second")
	if len(a.GoalCriteria()) != 0 {
		t.Error("setting a new objective should clear the old criteria")
	}
	if a.goalTokensUsed != 0 || a.goalBlocked {
		t.Error("a new objective should reset progress and blocked state")
	}
	if a.goalBudgetTokens != 1000 {
		t.Error("a new objective should keep the user-configured budget")
	}

	a.SetGoal("")
	if a.Goal() != "" || a.goalBudgetTokens != 0 || len(a.GoalCriteria()) != 0 {
		t.Error("/goal clear should wipe all goal state including the budget")
	}
}

// TestSplitCriteria checks the ';'/newline parser used by /goal criteria.
func TestSplitCriteria(t *testing.T) {
	got := SplitCriteria(" tests pass ; builds clean \n  docs updated ;; ")
	want := []string{"tests pass", "builds clean", "docs updated"}
	if len(got) != len(want) {
		t.Fatalf("SplitCriteria len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitCriteria[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
