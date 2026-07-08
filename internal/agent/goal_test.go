package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		in      string
		wantMet bool
		wantFB  string // substring expected in feedback when not met
	}{
		{"DONE", true, ""},
		{"done\nlooks complete", true, ""},
		{"NOT_DONE: tests are still failing", false, "tests are still failing"},
		{"NOT DONE - the README wasn't updated", false, "README"},
		{"", false, "unclear"},
		{"I think maybe it's fine?", false, "maybe it's fine"},
	}
	for _, c := range cases {
		v := parseVerdict(c.in)
		if v.met != c.wantMet {
			t.Errorf("parseVerdict(%q).met = %v, want %v", c.in, v.met, c.wantMet)
		}
		if !c.wantMet && c.wantFB != "" && !strings.Contains(v.feedback, c.wantFB) {
			t.Errorf("parseVerdict(%q).feedback = %q, want it to contain %q", c.in, v.feedback, c.wantFB)
		}
	}
}

func TestSetAndGetGoal(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	ag := runTestAgent(t, reg, dir, &scriptedStreamer{})

	if ag.Goal() != "" {
		t.Fatalf("new agent should have no goal, got %q", ag.Goal())
	}
	ag.SetGoal("  ship the feature  ")
	if ag.Goal() != "ship the feature" {
		t.Errorf("Goal() = %q, want trimmed 'ship the feature'", ag.Goal())
	}
	ag.SetGoal("")
	if ag.Goal() != "" {
		t.Errorf("cleared goal should be empty, got %q", ag.Goal())
	}
}

// TestRunWithGoalKeepsGoingUntilJudgeApproves verifies that when a goal is set,
// the agent does not stop on the first end_turn if the judge says NOT_DONE; it
// injects a nudge, the model works again, and only stops once the judge says
// DONE.
func TestRunWithGoalKeepsGoingUntilJudgeApproves(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)

	// Sequence of StreamMessages calls when a goal is set:
	//   1. model -> end_turn ("I think I'm done")
	//   2. judge -> NOT_DONE (more work needed)
	//   3. model -> end_turn ("ok, finished now")
	//   4. judge -> DONE
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("I think I'm done")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("NOT_DONE you still need to add tests")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok, finished now")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("DONE")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetGoal("add a feature with tests")
	ag.SetGoalCriteria([]string{"the feature works with tests"}) // skip auto-proposal so the scripted call counts stay deterministic

	var turnDones int
	var sawNudgeInfo bool
	err := ag.Run(context.Background(), "do it", func(ev Event) {
		switch e := ev.(type) {
		case TurnDone:
			turnDones++
		case Info:
			if strings.Contains(e.Text, "not met") || strings.Contains(strings.ToLower(e.Text), "continuing") {
				sawNudgeInfo = true
			}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 4 {
		t.Errorf("StreamMessages calls = %d, want 4 (model, judge, model, judge)", fake.calls)
	}
	if turnDones != 1 {
		t.Errorf("TurnDone events = %d, want exactly 1 (only after the judge approved)", turnDones)
	}
	if !sawNudgeInfo {
		t.Error("expected an Info event noting the stop condition was not met")
	}
	// The conversation should contain the injected goal nudge.
	var foundNudge bool
	for _, m := range ag.Messages() {
		for _, c := range m.Content {
			if c.Type == llm.BlockText && strings.Contains(c.Text, goalDirective) {
				foundNudge = true
			}
		}
	}
	if !foundNudge {
		t.Error("expected a goal-nudge user message in history")
	}
}

// TestRunWithGoalStopsWhenJudgeApprovesImmediately verifies the happy path: the
// model finishes and the judge approves on the first check, so the turn ends
// after exactly one model + one judge call.
func TestRunWithGoalStopsWhenJudgeApprovesImmediately(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("all set")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("DONE")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetGoal("make it work")
	ag.SetGoalCriteria([]string{"it works"}) // skip auto-proposal so the scripted call counts stay deterministic

	if err := ag.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("StreamMessages calls = %d, want 2 (model + judge)", fake.calls)
	}
}

// TestRunWithoutGoalStopsImmediately is a control: with no goal set, no judge
// call happens and the turn ends on the first end_turn.
func TestRunWithoutGoalStopsImmediately(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	if err := ag.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("StreamMessages calls = %d, want 1 (no judge without a goal)", fake.calls)
	}
}

// TestRunWithGoalBoundsNudges verifies the nudge loop is bounded: even if the
// judge keeps saying NOT_DONE, the turn ends after maxGoalNudges checks rather
// than looping forever.
func TestRunWithGoalBoundsNudges(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)

	// Always end_turn from the model, always NOT_DONE from the judge.
	end := llm.Response{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("still going")}}
	notDone := llm.Response{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("NOT_DONE keep going")}}
	turns := make([]llm.Response, 0, 16)
	for i := 0; i < 8; i++ {
		turns = append(turns, end, notDone)
	}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.MaxAgentSteps = 50 // plenty of room; the nudge bound should stop us first
	ag.SetGoal("an impossible goal")
	ag.SetGoalCriteria([]string{"the impossible happens"}) // skip auto-proposal so the scripted call counts stay deterministic

	var gaveUp bool
	if err := ag.Run(context.Background(), "go", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "still not confirmed after") {
			gaveUp = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// maxGoalNudges checks: each rejected check is (model end_turn + judge), then
	// after the bound is hit the next end_turn ends the turn without a judge call.
	// model,judge x maxGoalNudges, then a final model end_turn = 2*maxGoalNudges+1.
	want := 2*maxGoalNudges + 1
	if fake.calls != want {
		t.Errorf("StreamMessages calls = %d, want %d (bounded nudges)", fake.calls, want)
	}
	if !gaveUp {
		t.Error("expected a clear give-up Info once the per-turn nudge cap was hit, not a silent stop")
	}
}

// TestGoalJudgeUsesNoThinkPath verifies the goal judge runs via the no-think
// auxiliary path (StreamMessagesNoThink), so it doesn't burn the session's
// reasoning budget on a DONE/NOT_DONE verdict. The main turn still uses the
// normal path.
func TestGoalJudgeUsesNoThinkPath(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("all set")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("DONE")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.SetGoal("make it work")
	ag.SetGoalCriteria([]string{"it works"}) // skip auto-proposal so only the judge uses the no-think path

	if err := ag.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("StreamMessages calls = %d, want 2 (model + judge)", fake.calls)
	}
	if fake.noThinkCalls != 1 {
		t.Errorf("judge should run via the no-think path: noThinkCalls = %d, want 1 (and the main turn should not)", fake.noThinkCalls)
	}
}
