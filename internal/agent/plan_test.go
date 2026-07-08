package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// newPlanAgent builds an agent with a fresh session store + plan store wired,
// mirroring the production wiring (main.go / app.go).
func newPlanAgent(t *testing.T) (*Agent, *tools.PlanStore) {
	t.Helper()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	dir := t.TempDir()
	ag := New(&config.Config{WorkingDir: dir}, nil, tools.NewRegistry(dir), store, session.New("m", dir))
	ps := tools.NewPlanStore()
	ag.AttachPlanStore(ps)
	return ag, ps
}

func TestPlanWriteSyncsDraftToSession(t *testing.T) {
	ag, ps := newPlanAgent(t)
	// Simulate the plan_write tool firing its notify (as RunWith would wire it).
	ps.SetNotify(func(p *tools.Plan) { ag.applyDraftPlan(p) })
	ps.Set(&tools.Plan{Title: "Refactor", Steps: []tools.PlanStep{
		{Title: "step one"}, {Title: "step two", Detail: "with detail"},
	}})

	p := ag.Plan()
	if p == nil {
		t.Fatal("expected a plan after plan_write")
	}
	if p.Status != session.PlanDraft {
		t.Fatalf("new plan should be a draft, got %q", p.Status)
	}
	if len(p.Steps) != 2 || p.Steps[1].Detail != "with detail" {
		t.Fatalf("steps not synced: %+v", p.Steps)
	}
	if ag.PlanApproved() {
		t.Fatal("a fresh draft must not be approved")
	}
}

func TestApprovePlanAndContext(t *testing.T) {
	ag, _ := newPlanAgent(t)
	if ag.ApprovePlan() {
		t.Fatal("approving with no plan should fail")
	}

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "do it"}}})
	if got := ag.planContext(); !strings.Contains(got, "Draft plan (awaiting approval)") {
		t.Fatalf("draft context wrong:\n%s", got)
	}

	if !ag.ApprovePlan() {
		t.Fatal("approving a draft should succeed")
	}
	if !ag.PlanApproved() {
		t.Fatal("plan should be approved")
	}
	ctx := ag.planContext()
	if !strings.Contains(ctx, "Approved plan") || !strings.Contains(ctx, "do it") {
		t.Fatalf("approved context wrong:\n%s", ctx)
	}
}

func TestClearPlanResetsStore(t *testing.T) {
	ag, ps := newPlanAgent(t)
	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "x"}}})
	if ps.Get() == nil {
		t.Fatal("SetPlan should sync to the store")
	}
	ag.ClearPlan()
	if ag.Plan() != nil {
		t.Fatal("ClearPlan should drop the session plan")
	}
	if ps.Get() != nil {
		t.Fatal("ClearPlan should clear the store")
	}
}

func TestMaybeCompletePlanAutoDone(t *testing.T) {
	ag, _ := newPlanAgent(t)
	td := tools.NewTodoStore()
	ag.AttachTodoStore(td)

	// No plan yet → nothing to complete.
	if ag.maybeCompletePlan() {
		t.Fatal("no plan should not complete")
	}

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "do it"}}})
	if !ag.ApprovePlan() {
		t.Fatal("approve should succeed")
	}

	// Approved but no todos mirrored yet → not done (the plan is about to execute).
	if ag.maybeCompletePlan() {
		t.Fatal("approved plan with no todos must not auto-complete")
	}
	if ag.planContext() == "" {
		t.Fatal("approved plan should still inject context")
	}

	// Some todos still pending → not done; the approved-plan context still injects.
	td.Set([]tools.Todo{
		{Content: "a", Status: tools.TodoCompleted},
		{Content: "b", Status: tools.TodoInProgress},
	})
	if ag.maybeCompletePlan() {
		t.Fatal("partially complete todos must not auto-complete the plan")
	}
	if ag.planContext() == "" {
		t.Fatal("an unfinished approved plan should still inject context")
	}

	// All todos resolved (completed or cancelled) → auto-done. A cancelled step
	// counts as resolved; the old code blocked completion forever on a cancel.
	td.Set([]tools.Todo{
		{Content: "a", Status: tools.TodoCompleted},
		{Content: "b", Status: tools.TodoCancelled},
	})
	if !ag.maybeCompletePlan() {
		t.Fatal("all todos resolved (incl. cancelled) should auto-complete the approved plan")
	}
	if got := ag.Plan(); got == nil || got.Status != session.PlanDone {
		t.Fatalf("plan should be done, got %+v", got)
	}
	// A done plan injects no context, and re-running is a no-op.
	if ctx := ag.planContext(); ctx != "" {
		t.Fatalf("done plan should inject no context, got:\n%s", ctx)
	}
	if ag.maybeCompletePlan() {
		t.Fatal("a done plan must not transition again")
	}
}

// TestRetireExecutedPlanNoTodos verifies that an approved plan carried out
// WITHOUT a fully-tracked todo list is retired (→ done) on a LATER turn — but not
// before it's had its execution turn. maybeCompletePlan can't complete a no-todos
// plan, so without this it would re-inject "carry it out" into every follow-up.
func TestRetireExecutedPlanNoTodos(t *testing.T) {
	ag, _ := newPlanAgent(t)
	ag.AttachTodoStore(tools.NewTodoStore()) // attached but never populated

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "do it"}}})
	if !ag.ApprovePlan() {
		t.Fatal("approve should succeed")
	}

	// Just approved — it hasn't executed yet, so it must NOT be retired (it would
	// otherwise be killed before its execution turn runs).
	if ag.retireExecutedPlan() {
		t.Fatal("a just-approved plan must not be retired before it executes")
	}
	if !ag.PlanApproved() {
		t.Fatal("plan should still be approved")
	}

	// After its execution turn ended (planRanATurn), a later turn retires it
	// even though no todos were tracked.
	ag.planRanATurn = true
	if !ag.retireExecutedPlan() {
		t.Fatal("an executed, fully-resolved plan should retire")
	}
	if got := ag.Plan(); got == nil || got.Status != session.PlanDone {
		t.Fatalf("plan should be done, got %+v", got)
	}
	if ctx := ag.planContext(); ctx != "" {
		t.Fatalf("done plan should inject no context, got:\n%s", ctx)
	}
}

// TestRetireExecutedPlanKeepsPending verifies a plan with a step still in progress
// is NOT retired even after it has executed — genuine multi-turn work continues.
func TestRetireExecutedPlanKeepsPending(t *testing.T) {
	ag, _ := newPlanAgent(t)
	td := tools.NewTodoStore()
	ag.AttachTodoStore(td)

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "do it"}}})
	if !ag.ApprovePlan() {
		t.Fatal("approve should succeed")
	}
	ag.planRanATurn = true
	td.Set([]tools.Todo{{Content: "a", Status: tools.TodoInProgress}})

	if ag.retireExecutedPlan() {
		t.Fatal("a plan with a pending step must not be retired")
	}
	if !ag.PlanApproved() {
		t.Fatal("plan should remain approved while work is pending")
	}
}

// TestRetireStaleDraftBuildMode verifies that an unapproved draft from a previous
// turn is discarded once the user moves on in build mode — so it stops gating
// bash/edits and haunting later, unrelated questions.
func TestRetireStaleDraftBuildMode(t *testing.T) {
	ag, _ := newPlanAgent(t)
	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "draft step"}}}) // draft

	// Freshly drafted (this turn): must NOT be retired — it needs its presentation
	// turn, and the execution gate must hold.
	if ag.retireStaleDraft() {
		t.Fatal("a fresh draft must not be retired before it's presented")
	}
	if !ag.hasDraftPlan() {
		t.Fatal("draft should still gate mutating tools this turn")
	}

	// It sat through a turn; the user moved on (build mode) → retire it.
	ag.planRanATurn = true
	if !ag.retireStaleDraft() {
		t.Fatal("a stale build-mode draft should be retired on a later turn")
	}
	if ag.Plan() != nil {
		t.Fatalf("stale draft should be cleared, got %+v", ag.Plan())
	}
	if ag.hasDraftPlan() {
		t.Fatal("the execution gate must be released once the draft is retired")
	}
}

// TestRetireStaleDraftKeepsPlanMode verifies a draft is preserved while in plan
// mode — the user is actively refining it across turns.
func TestRetireStaleDraftKeepsPlanMode(t *testing.T) {
	ag, _ := newPlanAgent(t)
	ag.SetPlanMode(true)
	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "draft step"}}})
	ag.planRanATurn = true // even after sitting through a turn

	if ag.retireStaleDraft() {
		t.Fatal("a plan-mode draft must not be retired — it's being refined")
	}
	if ag.Plan() == nil {
		t.Fatal("plan-mode draft should be preserved")
	}
}

func TestResumeRestoresPlan(t *testing.T) {
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	dir := t.TempDir()
	ag := New(&config.Config{WorkingDir: dir}, nil, tools.NewRegistry(dir), store, session.New("m", dir))
	ps := tools.NewPlanStore()
	ag.AttachPlanStore(ps)

	resumed := session.New("m", dir)
	resumed.Plan = &session.Plan{Title: "carried", Status: session.PlanApproved,
		Steps: []session.PlanStep{{Title: "kept"}}}
	ag.Resume(resumed)

	if got := ps.Get(); got == nil || got.Title != "carried" {
		t.Fatalf("resume should restore the plan store, got %+v", got)
	}
	if !ag.PlanApproved() {
		t.Fatal("resumed approved plan should report approved")
	}
}

// TestResumeMarksPlanRanATurn is a regression test for the web-UI bug where an
// already-approved-AND-executed plan kept being injected into follow-up turns
// after a session reload / server restart. planRanATurn is in-memory only and was
// not restored on Resume, so both retirement gates bailed forever and planContext()
// never cleared. Resume now marks a restored, not-done plan as having run a turn.
func TestResumeMarksPlanRanATurn(t *testing.T) {
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	dir := t.TempDir()
	ag := New(&config.Config{WorkingDir: dir}, nil, tools.NewRegistry(dir), store, session.New("m", dir))
	ag.AttachPlanStore(tools.NewPlanStore())

	// Simulate reopening a session whose plan was approved and executed last turn.
	resumed := session.New("m", dir)
	resumed.Plan = &session.Plan{Title: "carried", Status: session.PlanApproved,
		Steps: []session.PlanStep{{Title: "kept"}}}
	ag.Resume(resumed)

	// Right after resume the approved plan is restored and still shown.
	if ag.planContext() == "" {
		t.Fatal("resumed approved plan should be in context before retirement")
	}
	// On the next top-level turn the executed plan must retire (no pending todos),
	// instead of lingering — this is the fix. Pre-fix, planRanATurn==false made
	// retireExecutedPlan() a no-op.
	if !ag.retireExecutedPlan() {
		t.Fatal("resumed approved+executed plan should retire on the next turn")
	}
	if got := ag.Plan(); got == nil || got.Status != session.PlanDone {
		t.Fatalf("retired plan should be marked done, got %+v", got)
	}
	if ctx := ag.planContext(); ctx != "" {
		t.Fatalf("retired plan must not leak into follow-up context, got %q", ctx)
	}
}

// TestTrackPlanAbandonRetiresIdle verifies an approved plan the user has stopped
// driving — no step progress across the grace window — is retired (→ done) so it
// stops haunting follow-ups. This is the approved-plan analogue of retireStaleDraft;
// a FINISHED plan is instead handled by maybeCompletePlan / retireExecutedPlan.
func TestTrackPlanAbandonRetiresIdle(t *testing.T) {
	ag, _ := newPlanAgent(t)
	td := tools.NewTodoStore()
	ag.AttachTodoStore(td)

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "a"}, {Title: "b"}}})
	if !ag.ApprovePlan() {
		t.Fatal("approve should succeed")
	}

	// Execution turn resolves step 1, leaves step 2 pending. ranBefore=false, so it
	// only baselines progress and never retires on the execution turn itself.
	td.Set([]tools.Todo{
		{Content: "a", Status: tools.TodoCompleted},
		{Content: "b", Status: tools.TodoInProgress},
	})
	if ag.trackPlanAbandon(false) {
		t.Fatal("the execution turn must not retire the plan")
	}
	if !ag.PlanApproved() {
		t.Fatal("plan should still be approved after its execution turn")
	}

	// First follow-up with no further progress: within the grace window → kept.
	if ag.trackPlanAbandon(true) {
		t.Fatal("plan must survive the first idle turn (grace window)")
	}
	if ag.planContext() == "" {
		t.Fatal("a still-live approved plan should keep injecting context")
	}

	// Second consecutive idle turn → abandoned → retired.
	if !ag.trackPlanAbandon(true) {
		t.Fatal("plan should retire once idle past the grace window")
	}
	if got := ag.Plan(); got == nil || got.Status != session.PlanDone {
		t.Fatalf("abandoned plan should be marked done, got %+v", got)
	}
	if ctx := ag.planContext(); ctx != "" {
		t.Fatalf("retired plan must not leak into follow-up context, got %q", ctx)
	}
}

// TestTrackPlanAbandonKeepsProgressing verifies a plan that keeps advancing — e.g.
// resumed across step-cap "continue" turns — is never retired, however many turns
// it spans, because each resolved step resets the idle tally. Progress, not turn
// count, is the signal.
func TestTrackPlanAbandonKeepsProgressing(t *testing.T) {
	ag, _ := newPlanAgent(t)
	td := tools.NewTodoStore()
	ag.AttachTodoStore(td)

	ag.SetPlan(&session.Plan{Steps: []session.PlanStep{{Title: "a"}, {Title: "b"}, {Title: "c"}}})
	if !ag.ApprovePlan() {
		t.Fatal("approve should succeed")
	}

	// Execution turn resolves 1/3.
	td.Set([]tools.Todo{{Content: "a", Status: tools.TodoCompleted}, {Content: "b", Status: tools.TodoInProgress}, {Content: "c"}})
	ag.trackPlanAbandon(false)

	// Each later turn resolves one more step — always progress. Run more turns than
	// the grace window to prove a still-advancing plan is never dropped.
	td.Set([]tools.Todo{{Content: "a", Status: tools.TodoCompleted}, {Content: "b", Status: tools.TodoCompleted}, {Content: "c", Status: tools.TodoInProgress}})
	if ag.trackPlanAbandon(true) {
		t.Fatal("a progressing plan must not be retired (step 2 resolved)")
	}
	td.Set([]tools.Todo{{Content: "a", Status: tools.TodoCompleted}, {Content: "b", Status: tools.TodoCompleted}, {Content: "c", Status: tools.TodoCompleted}})
	if ag.trackPlanAbandon(true) {
		t.Fatal("a progressing plan must not be retired (step 3 resolved)")
	}
	if !ag.PlanApproved() {
		t.Fatal("a continuously progressing plan stays approved until maybeCompletePlan marks it done")
	}
}
