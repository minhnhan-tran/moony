package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// exitPlanRunAgent builds an agent with a session + plan store + scripted
// streamer, seeds a draft plan, and turns on plan mode — the state in which the
// model would call exit_plan.
func exitPlanRunAgent(t *testing.T, fake *scriptedStreamer) *Agent {
	t.Helper()
	isolateConfig(t)
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20) // registers exit_plan (and ask_user, …)
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	ag := New(cfg, fake, reg, store, session.New("test-model", dir))
	ag.SetInteractive(true) // exit_plan confirmation requires an interactive user
	ag.AttachPlanStore(tools.NewPlanStore())
	ag.SetPlan(&session.Plan{Title: "Refactor", Steps: []session.PlanStep{{Title: "step one"}}})
	ag.SetPlanMode(true)
	return ag
}

// TestRunExitPlanApproveLeavesPlanMode verifies that when the model calls
// exit_plan and the user approves, the plan is marked approved, plan mode is
// turned off (mutating tools re-enabled), and the tool tells the model it's in
// build mode so the turn continues into execution.
func TestRunExitPlanApproveLeavesPlanMode(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "exit_plan", `{"summary":"do the refactor"}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("executing now")}},
	}}
	ag := exitPlanRunAgent(t, fake)

	var asked bool
	err := ag.Run(context.Background(), "plan and build it", func(ev Event) {
		if q, ok := ev.(QuestionRequest); ok {
			asked = true
			q.Reply <- []string{exitPlanApproveLabel}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !asked {
		t.Fatal("exit_plan should have emitted a confirmation question")
	}
	if ag.PlanMode() {
		t.Error("approving exit_plan must turn plan mode OFF")
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanApproved {
		t.Errorf("plan should be approved after exit_plan, got %+v", p)
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !strings.Contains(res[0].Content, "BUILD mode") {
		t.Errorf("expected a build-mode tool_result, got %+v", res)
	}
}

// TestRunExitPlanDeclineStaysInPlanMode verifies that declining the exit_plan
// confirm keeps the agent read-only: plan mode stays on, the plan stays a draft,
// and the model is told to keep planning.
func TestRunExitPlanDeclineStaysInPlanMode(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "exit_plan", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok, refining")}},
	}}
	ag := exitPlanRunAgent(t, fake)

	err := ag.Run(context.Background(), "plan it", func(ev Event) {
		if q, ok := ev.(QuestionRequest); ok {
			q.Reply <- []string{"Keep planning"}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ag.PlanMode() {
		t.Error("declining exit_plan must keep plan mode ON")
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanDraft {
		t.Errorf("plan should stay a draft after declining, got %+v", p)
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !strings.Contains(res[0].Content, "plan mode") {
		t.Errorf("expected a keep-planning tool_result, got %+v", res)
	}
}

// TestExitPlanNotParallelSafe ensures exit_plan runs on the serial path so its
// confirm round-trip never overlaps another prompt.
func TestExitPlanNotParallelSafe(t *testing.T) {
	a := &Agent{tools: tools.NewRegistry(t.TempDir())}
	if a.parallelSafe("exit_plan") {
		t.Fatal("exit_plan must not be parallel-safe")
	}
}

// exitPlanDraftAgent is like exitPlanRunAgent but leaves plan mode OFF — the
// state where the model self-drafted a plan during an ordinary build-mode turn
// (via plan_write). A pending draft must still gate execution even though
// plan mode was never entered.
func exitPlanDraftAgent(t *testing.T, fake *scriptedStreamer) *Agent {
	t.Helper()
	ag := exitPlanRunAgent(t, fake)
	ag.SetPlanMode(false) // draft exists, but plan mode is OFF
	return ag
}

// TestRunExitPlanDraftWithoutPlanModeStillConfirms is the regression guard for
// the reported bug: a draft plan was being approved and executed WITHOUT user
// confirmation whenever plan mode happened to be off. exit_plan must still
// prompt the user (and only approve on their say-so) when a draft is pending.
func TestRunExitPlanDraftWithoutPlanModeStillConfirms(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "exit_plan", `{"summary":"do the refactor"}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("executing now")}},
	}}
	ag := exitPlanDraftAgent(t, fake)
	if ag.PlanMode() {
		t.Fatal("precondition: plan mode must be OFF for this test")
	}

	var asked bool
	err := ag.Run(context.Background(), "build it", func(ev Event) {
		if q, ok := ev.(QuestionRequest); ok {
			asked = true
			q.Reply <- []string{exitPlanApproveLabel}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !asked {
		t.Fatal("exit_plan must prompt for confirmation even with plan mode off when a draft is pending")
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanApproved {
		t.Errorf("plan should be approved only after the user confirmed, got %+v", p)
	}
}

// TestRunExitPlanDraftWithoutPlanModeDeclineKeepsDraft verifies that declining
// the confirmation while a draft is pending (plan mode off) leaves the plan a
// draft — it is not silently approved.
func TestRunExitPlanDraftWithoutPlanModeDeclineKeepsDraft(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "exit_plan", `{}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok, refining")}},
	}}
	ag := exitPlanDraftAgent(t, fake)

	err := ag.Run(context.Background(), "build it", func(ev Event) {
		if q, ok := ev.(QuestionRequest); ok {
			q.Reply <- []string{"Keep planning"}
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanDraft {
		t.Errorf("declining must keep the plan a draft, got %+v", p)
	}
}

// TestDraftPlanBlocksMutatingToolsWithoutPlanMode verifies the defense-in-depth
// gate: with a pending draft and plan mode off, a mutating tool the model tries
// to run directly is refused (so it can't execute its own un-approved plan).
func TestDraftPlanBlocksMutatingToolsWithoutPlanMode(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "bash", `{"command":"echo executing"}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := exitPlanDraftAgent(t, fake)

	err := ag.Run(context.Background(), "build it", func(ev Event) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := toolResults(fake.sent[1])
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "draft plan is awaiting") {
		t.Errorf("a pending draft must block mutating tools, got %+v", res)
	}
}
