package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// buildModeDraftAgent builds a BUILD-mode agent (plan mode OFF) with a plan store
// and scripted streamer. autoPlan turns on the AutoPlan heuristic.
func buildModeDraftAgent(t *testing.T, fake *scriptedStreamer, autoPlan bool) *Agent {
	t.Helper()
	isolateConfig(t)
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
		AutoPlan:           autoPlan,
	}
	ag := New(cfg, fake, reg, store, session.New("test-model", dir))
	ag.AttachPlanStore(tools.NewPlanStore())
	return ag
}

func endTurns(n int) *scriptedStreamer {
	s := &scriptedStreamer{}
	for i := 0; i < n; i++ {
		s.turns = append(s.turns, llm.Response{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok")}})
	}
	return s
}

// TestBuildModeDraftRetiredOnFollowup: a draft proposed in build mode that the
// user moves past is auto-retired on the next top-level turn (not re-shown).
func TestBuildModeDraftRetiredOnFollowup(t *testing.T) {
	ag := buildModeDraftAgent(t, endTurns(2), false)
	ag.applyDraftPlan(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "step one"}}})
	if p := ag.Plan(); p == nil || p.Status != session.PlanDraft {
		t.Fatalf("precondition: expected a seeded draft, got %+v", p)
	}

	// Turn 1: the draft gets its presentation turn (planRanATurn → true at end).
	if err := ag.Run(context.Background(), "first followup", func(Event) {}); err != nil {
		t.Fatalf("Run turn 1: %v", err)
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanDraft {
		t.Fatalf("after turn 1 the draft should still be present, got %+v", p)
	}

	// Turn 2 (top-level, build mode): retireStaleDraft clears the stale draft.
	if err := ag.Run(context.Background(), "second, unrelated followup", func(Event) {}); err != nil {
		t.Fatalf("Run turn 2: %v", err)
	}
	if p := ag.Plan(); p != nil {
		t.Errorf("BUG: draft plan still present after an unrelated build-mode followup: %+v", p)
	}
	if ag.dismissedDraftFP == "" {
		t.Error("retiring a stale draft should record its fingerprint for re-proposal suppression")
	}
}

// TestBuildModeSuppressesReproposedDismissedDraft covers cause 1: after a draft is
// retired, the model re-issuing the IDENTICAL plan (plan_write from stale history)
// is suppressed in build mode — but a genuinely different plan is applied.
func TestBuildModeSuppressesReproposedDismissedDraft(t *testing.T) {
	ag := buildModeDraftAgent(t, endTurns(2), false)
	same := &tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "step one", Detail: "d"}}}
	ag.applyDraftPlan(same)
	// Two turns so the draft becomes stale, then gets retired at turn 2's start.
	for i := 0; i < 2; i++ {
		if err := ag.Run(context.Background(), "followup", func(Event) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	if ag.Plan() != nil || ag.dismissedDraftFP == "" {
		t.Fatalf("precondition: draft should be retired + fingerprint recorded (plan=%+v fp=%q)", ag.Plan(), ag.dismissedDraftFP)
	}

	// The model re-proposes the SAME plan → suppressed, no draft resurfaces.
	ag.applyDraftPlan(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "step one", Detail: "d"}}})
	if p := ag.Plan(); p != nil {
		t.Errorf("re-proposing the dismissed draft must be suppressed, got %+v", p)
	}

	// A genuinely different plan IS applied, and clears the suppression.
	ag.applyDraftPlan(&tools.Plan{Title: "New feature", Steps: []tools.PlanStep{{Title: "add endpoint"}}})
	if p := ag.Plan(); p == nil || p.Title != "New feature" {
		t.Errorf("a different plan must be applied, got %+v", p)
	}
	if ag.dismissedDraftFP != "" {
		t.Error("applying a genuine new draft must clear the dismissed-fingerprint suppression")
	}
}

// TestBuildModeDismissedDraftClearedByPlanMode: entering plan mode (an explicit
// user request to plan) clears the suppression, so the model may re-propose.
func TestBuildModeDismissedDraftClearedByPlanMode(t *testing.T) {
	ag := buildModeDraftAgent(t, endTurns(2), false)
	ag.applyDraftPlan(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "s"}}})
	for i := 0; i < 2; i++ {
		_ = ag.Run(context.Background(), "followup", func(Event) {})
	}
	if ag.dismissedDraftFP == "" {
		t.Fatal("precondition: expected a recorded dismissal")
	}
	ag.SetPlanMode(true)
	if ag.dismissedDraftFP != "" {
		t.Error("explicitly entering plan mode must clear the dismissal suppression")
	}
}

// TestAutoPlanDraftRetiredAndRevertsToBuild covers cause 2: a draft under an
// AUTO-entered plan mode (MaybeAutoPlan) that the user ignores is retired AND plan
// mode reverts to build — so an unwanted heuristic flip doesn't trap the user with
// a stale draft every followup. A USER-selected plan mode keeps the draft.
func TestAutoPlanDraftRetiredAndRevertsToBuild(t *testing.T) {
	ag := buildModeDraftAgent(t, endTurns(2), true)
	if !ag.MaybeAutoPlan("refactor the entire codebase") {
		t.Fatal("MaybeAutoPlan should flip a large-task message to plan mode")
	}
	if !ag.PlanMode() || !ag.autoPlanned {
		t.Fatalf("precondition: expected auto-entered plan mode (planMode=%v autoPlanned=%v)", ag.PlanMode(), ag.autoPlanned)
	}
	ag.applyDraftPlan(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "step one"}}})

	// Turn 1: presentation turn. Turn 2: the user ignored the plan → retire + revert.
	for i := 0; i < 2; i++ {
		if err := ag.Run(context.Background(), "actually, unrelated question", func(Event) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	if p := ag.Plan(); p != nil {
		t.Errorf("BUG: auto-plan draft still present after being ignored: %+v", p)
	}
	if ag.PlanMode() {
		t.Error("an ignored auto-plan must revert to build mode")
	}
	if ag.autoPlanned {
		t.Error("autoPlanned must reset once the auto-plan is retired")
	}
}

// TestUserPlanModeKeepsDraft: a USER-selected plan mode preserves its draft across
// turns (active refinement) — retireStaleDraft must NOT fire there.
func TestUserPlanModeKeepsDraft(t *testing.T) {
	ag := buildModeDraftAgent(t, endTurns(2), false)
	ag.SetPlanMode(true) // explicit user choice, not auto
	ag.applyDraftPlan(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{{Title: "step one"}}})
	for i := 0; i < 2; i++ {
		if err := ag.Run(context.Background(), "keep refining", func(Event) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	if p := ag.Plan(); p == nil || p.Status != session.PlanDraft {
		t.Errorf("a user-selected plan mode must keep its draft for refinement, got %+v", p)
	}
	if !ag.PlanMode() {
		t.Error("user-selected plan mode must stay on")
	}
}
