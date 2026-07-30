package agent

// First-class plan artifact: a titled, ordered, editable list of steps the
// agent drafts (typically in plan mode) and the user amends and approves before
// execution. Unlike the ephemeral todo list (todo_write, replaced freely as work
// progresses), the plan is a deliberate, persisted artifact with a lifecycle —
// draft → approved — that the user controls. While it exists it is injected as
// per-turn context (see planContext, carried by appendTurnContext) so the model
// keeps it in view; an approved plan additionally instructs the model to carry
// it out.

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// AttachPlanStore wires the plan store backing the plan_write tool. Drafts the
// model writes are synced to the session and surfaced as PlanUpdated events
// during a Run. When the agent resumed a session that already carries a plan,
// the store is seeded with it so a reopened session restores its plan panel.
func (a *Agent) AttachPlanStore(s *tools.PlanStore) {
	a.plan = s
	if s != nil && a.sess != nil && a.sess.Plan != nil {
		s.Set(toToolPlan(a.sess.Plan))
	}
}

// Plan returns the session's current plan artifact, or nil when none is set
// (or persistence is disabled — the plan lives on the session).
func (a *Agent) Plan() *session.Plan {
	if a.sess == nil {
		return nil
	}
	return a.sess.Plan
}

// applyDraftPlan records a draft the model produced via plan_write onto the
// session. A new draft replaces any prior plan and (re)sets it to draft status,
// since editing a plan invalidates a previous approval.
func (a *Agent) applyDraftPlan(p *tools.Plan) {
	if a.sess == nil || p == nil {
		return
	}
	draft := fromToolPlan(p, session.PlanDraft)
	// Suppress a BUILD-mode re-proposal of a draft the user already moved past:
	// the model re-issues plan_write from its own stale history, which otherwise
	// resurrects the same draft-plan head on every follow-up. Keep the plan store
	// in sync SILENTLY so it doesn't diverge from the (unchanged) session plan —
	// Set would fire the notify and force Status=draft. In plan mode the user is
	// actively refining, so re-proposals are allowed.
	if !a.PlanMode() && a.dismissedDraftFP != "" && planFingerprint(draft) == a.dismissedDraftFP {
		if a.plan != nil {
			a.plan.Sync(toToolPlan(a.sess.Plan))
		}
		return
	}
	a.sess.Plan = draft
	a.dismissedDraftFP = "" // a genuine draft is active again; stop suppressing
	a.planRanATurn = false  // a fresh draft gets its presentation turn before it can be retired
	a.resetPlanProgress()
	a.persist()
}

// SetPlan replaces the session plan with the given steps (e.g. from a /plan
// edit). It is recorded as a draft and synced to the plan store so the model's
// next turn sees it. A nil/empty plan clears it.
func (a *Agent) SetPlan(p *session.Plan) {
	if a.sess == nil {
		return
	}
	if p == nil || len(p.Steps) == 0 {
		a.ClearPlan()
		return
	}
	if p.Status == "" {
		p.Status = session.PlanDraft
	}
	a.sess.Plan = p
	a.persist()
	if a.plan != nil {
		a.plan.Set(toToolPlan(p))
	}
}

// ApprovePlan marks the current draft plan approved, so the agent executes it.
// It returns false when there is no plan to approve.
func (a *Agent) ApprovePlan() bool {
	if a.sess == nil || a.sess.Plan == nil || len(a.sess.Plan.Steps) == 0 {
		return false
	}
	a.sess.Plan.Status = session.PlanApproved
	a.planRanATurn = false  // it hasn't executed yet; don't retire it until it has
	a.autoPlanned = false   // an approved plan is no longer an unconfirmed auto-plan
	a.dismissedDraftFP = "" // approved a plan; nothing to suppress
	a.resetPlanProgress()
	a.persist()
	return true
}

// ClearPlan drops the session's plan artifact and the backing draft store.
func (a *Agent) ClearPlan() {
	if a.sess == nil {
		return
	}
	a.sess.Plan = nil
	a.resetPlanProgress()
	a.persist()
	if a.plan != nil {
		a.plan.Set(nil)
	}
}

// PlanApproved reports whether the session has an approved plan in flight.
func (a *Agent) PlanApproved() bool {
	p := a.Plan()
	return p != nil && p.Status == session.PlanApproved
}

// hasDraftPlan reports whether a drafted plan is awaiting the user's approval.
// This is the status-based half of the execution gate: while a draft is pending,
// mutating tools must stay blocked and exit_plan must prompt for confirmation —
// even when read-only plan mode is not on. The model can produce a draft via
// plan_write during an ordinary build-mode turn (the tool is always available),
// and without this gate it would immediately execute the plan it just drafted,
// bypassing the user's approval entirely.
func (a *Agent) hasDraftPlan() bool {
	p := a.Plan()
	return p != nil && len(p.Steps) > 0 && p.Status == session.PlanDraft
}

// maybeCompletePlan auto-transitions an approved plan to done once the agent has
// mirrored it onto the todo list and every todo is resolved (completed OR
// cancelled). This stops the approved-plan context (see planContext) from being
// re-injected on every future turn after the work is actually finished — without
// it, a plan whose final step the model cancels (rather than completes) would
// stay "approved" forever and haunt every later answer. It is a no-op unless
// there is an approved plan with at least one todo, all resolved. An empty todo
// list is NOT treated as done: a freshly approved plan has no todos yet (the
// model mirrors them as it executes), so completing on "no todos" would retire a
// plan that's about to run. Returns true when it transitioned the plan (so the
// caller can emit a PlanUpdated event).
func (a *Agent) maybeCompletePlan() bool {
	if a.sess == nil || a.sess.Plan == nil || a.sess.Plan.Status != session.PlanApproved {
		return false
	}
	if a.todos == nil {
		return false
	}
	items := a.todos.Items()
	if len(items) == 0 {
		return false
	}
	for _, t := range items {
		switch t.Status {
		case tools.TodoCompleted, tools.TodoCancelled:
			// resolved — not pending work
		default:
			return false // a step is still pending; the plan isn't finished
		}
	}
	a.sess.Plan.Status = session.PlanDone
	a.persist()
	if a.plan != nil {
		a.plan.Sync(toToolPlan(a.sess.Plan))
	}
	return true
}

// planTodosAllResolved reports whether no mirrored todo is still pending — every
// todo is completed or cancelled (true when there are no todos at all).
func (a *Agent) planTodosAllResolved() bool {
	if a.todos == nil {
		return true
	}
	for _, t := range a.todos.Items() {
		switch t.Status {
		case tools.TodoCompleted, tools.TodoCancelled:
		default:
			return false
		}
	}
	return true
}

// retireExecutedPlan marks an already-executed approved plan done when no step is
// still pending. maybeCompletePlan needs ≥1 resolved todo, so a plan carried out
// WITHOUT a tracked todo list never auto-completes and planContext re-injects
// "carry it out" into every later turn. This catches that on a turn AFTER the
// plan ran (planRanATurn) — so a just-approved, about-to-execute plan (no todos
// yet) is left alone, but a finished one stops haunting follow-ups. Returns true
// if it transitioned the plan.
func (a *Agent) retireExecutedPlan() bool {
	if a.sess == nil || a.sess.Plan == nil || a.sess.Plan.Status != session.PlanApproved {
		return false
	}
	if !a.planRanATurn || !a.planTodosAllResolved() {
		return false
	}
	a.sess.Plan.Status = session.PlanDone
	a.persist()
	if a.plan != nil {
		a.plan.Sync(toToolPlan(a.sess.Plan))
	}
	return true
}

// planAbandonGraceTurns is how many consecutive top-level turns an approved,
// unfinished plan may make no progress before trackPlanAbandon retires it. One
// grace turn — so a single interrupting question mid-execution ("wait, what does
// step 3 do?") doesn't discard a plan the user then resumes — then retired on the
// next still-idle turn.
const planAbandonGraceTurns = 2

// resolvedTodoCount is the number of mirrored todos in a terminal state
// (completed or cancelled) — the progress yardstick for trackPlanAbandon.
func (a *Agent) resolvedTodoCount() int {
	if a.todos == nil {
		return 0
	}
	n := 0
	for _, t := range a.todos.Items() {
		if t.Status == tools.TodoCompleted || t.Status == tools.TodoCancelled {
			n++
		}
	}
	return n
}

// resetPlanProgress clears the abandonment tracker — called whenever the plan
// lifecycle restarts (a plan is drafted, approved, or cleared).
func (a *Agent) resetPlanProgress() {
	a.planIdleTurns = 0
	a.planResolvedMark = 0
}

// trackPlanAbandon runs at the end of a top-level turn that left an approved,
// unfinished plan in place, and retires it (→ done) once the user has clearly
// stopped driving it. It's the approved-plan analogue of retireStaleDraft (which
// discards an abandoned DRAFT) and retireExecutedPlan (which retires a FINISHED
// one): neither of those fires for an approved plan with steps still pending, so
// without this such a plan — e.g. one the user walked away from mid-execution —
// keeps its "carry it out" context (planContext) and its bar on every later,
// unrelated answer.
//
// "Progress" is a plan step resolving (the resolved mirrored-todo count rising);
// any progress resets the idle tally, so a plan still advancing — crucially
// including one resumed after a step-cap "continue" — is never dropped. Only a plan
// that has already had its execution turn (ranBefore) is eligible; the execution
// turn itself just baselines the progress mark. Returns true if it retired the plan.
func (a *Agent) trackPlanAbandon(ranBefore bool) bool {
	if a.sess == nil || a.sess.Plan == nil || a.sess.Plan.Status != session.PlanApproved {
		return false
	}
	resolved := a.resolvedTodoCount()
	progressed := resolved > a.planResolvedMark
	a.planResolvedMark = resolved
	switch {
	case !ranBefore:
		a.planIdleTurns = 0 // its own execution turn — baseline only, never retire here
		return false
	case progressed:
		a.planIdleTurns = 0 // still advancing (incl. resuming after a continue)
		return false
	}
	a.planIdleTurns++
	if a.planIdleTurns < planAbandonGraceTurns {
		return false // within the grace window — keep the plan
	}
	a.sess.Plan.Status = session.PlanDone
	a.planIdleTurns = 0
	a.persist()
	if a.plan != nil {
		a.plan.Sync(toToolPlan(a.sess.Plan))
	}
	return true
}

// retireStaleDraft discards an unapproved DRAFT plan the user has moved past: one
// that already sat through a turn (planRanATurn) while NOT in plan mode. The
// plan_write steering makes the model propose a plan as a draft before non-trivial
// work; left pending, that draft keeps the execution gate on (bash/edit_file
// disabled, see hasDraftPlan/planModeBlocked) AND re-injects "awaiting approval"
// context — so a draft from one question blocks and haunts the next, unrelated
// one. In build mode a draft is the model's transient proposal, so once the user
// moves on it's abandoned and cleared. Plan mode (active refinement) keeps it.
// Returns true if it cleared the plan.
func (a *Agent) retireStaleDraft() bool {
	if a.sess == nil || a.sess.Plan == nil || a.sess.Plan.Status != session.PlanDraft {
		return false
	}
	if !a.planRanATurn {
		return false // a fresh draft still gets its presentation turn
	}
	if a.PlanMode() && !a.autoPlanned {
		return false // user chose plan mode → keep the draft for refinement
	}
	a.dismissedDraftFP = planFingerprint(a.sess.Plan) // suppress a build-mode re-proposal of this exact plan
	if a.autoPlanned {
		a.active.Store(config.BuildAgent()) // undo the heuristic plan-mode flip the user ignored
		a.autoPlanned = false
	}
	a.ClearPlan()
	return true
}

// planFingerprint is a stable content hash of a plan's title + step titles and
// details. It identifies a specific plan so a re-proposal of a dismissed draft
// can be detected (see applyDraftPlan / retireStaleDraft). "" for an empty plan.
func planFingerprint(p *session.Plan) string {
	if p == nil || len(p.Steps) == 0 {
		return ""
	}
	h := fnv.New64a()
	w := func(s string) { _, _ = h.Write([]byte(s)); _, _ = h.Write([]byte{0}) }
	w(p.Title)
	for _, s := range p.Steps {
		w(s.Title)
		w(s.Detail)
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// planContext returns the plan wrapped as a per-turn context preface (carried in
// the uncached system suffix by appendTurnContext), or "" when there is no plan
// or the plan is done. A draft is presented as awaiting approval; an approved
// plan instructs the model to execute it and keep the todo list in sync.
func (a *Agent) planContext() string {
	p := a.Plan()
	if p == nil || len(p.Steps) == 0 || p.Status == session.PlanDone {
		// No active plan. If a draft was just dismissed (the user moved past it),
		// tell the model so it stops re-proposing the same plan from its own stale
		// history — the advisory half of the suppression in applyDraftPlan.
		if a.dismissedDraftFP != "" {
			return "# No active plan\n\nA plan you proposed earlier was NOT approved — the user moved on to other work, so it has been discarded. Do not re-propose or restate that plan; draft a new plan only if the user explicitly asks for one.\n"
		}
		return ""
	}
	var b strings.Builder
	if p.Status == session.PlanApproved {
		b.WriteString("# Approved plan\n\n")
		b.WriteString("The user APPROVED this plan. Carry it out step by step. " +
			"Mirror progress with the todo_write task list (one step in_progress at " +
			"a time, mark steps completed as you finish them). If you've already " +
			"completed every step, consider the plan done and proceed normally. If " +
			"you must deviate materially, briefly say why.\n\n")
	} else {
		b.WriteString("# Draft plan (awaiting approval)\n\n")
		b.WriteString("This plan is a DRAFT the user is reviewing. Do NOT start " +
			"executing it or editing files yet — refine it if asked, and wait for the " +
			"user to approve it.\n\n")
	}
	b.WriteString(renderPlanText(p))
	return b.String()
}

// PlanStatusLine returns a short human description of the plan state for the
// /plan command with no argument.
func (a *Agent) PlanStatusLine() string {
	p := a.Plan()
	if p == nil || len(p.Steps) == 0 {
		return "No plan yet. Use /plan <description> to have the agent draft one (read-only plan mode), " +
			"then /plan approve to execute it. /plan clear removes it."
	}
	state := "draft (awaiting approval — /plan approve to execute, or /plan clear to discard)"
	switch p.Status {
	case session.PlanApproved:
		state = "approved (the agent is executing it)"
	case session.PlanDone:
		state = "done (all steps completed — /plan clear to discard)"
	}
	var b strings.Builder
	b.WriteString("Plan — " + state + "\n\n")
	b.WriteString(renderPlanText(p))
	return b.String()
}

// PlanDraftPrompt builds the model instruction sent by the /plan <description>
// command (on every surface) to draft a plan for desc in read-only plan mode.
func PlanDraftPrompt(desc string) string {
	return "Draft a concise, ordered plan for the following task, then STOP and wait " +
		"for my approval — do not start editing files yet.\n\n" +
		"Task: " + desc + "\n\n" +
		"First explore the codebase as needed to ground the plan in reality (read-only " +
		"tools are available). Then call the `plan_write` tool with the full ordered list " +
		"of steps (each a short imperative title, plus an optional one-line detail). Keep " +
		"it focused — break the work into the smallest number of meaningful steps. After " +
		"writing the plan, give me a one-line summary and ask me to approve or amend it. " +
		"When I'm ready, call `exit_plan` (or I'll run /plan approve) to switch to build mode."
}

// PlanExecutePrompt is the instruction sent when the user approves a plan via
// the /plan approve command (on every surface).
const PlanExecutePrompt = "I've approved the plan. Carry it out now, step by step. " +
	"Use the todo_write task list to track progress (one step in_progress at a time, " +
	"mark each completed as you finish it). Work through the whole plan; only stop when " +
	"it is genuinely done or you hit something that needs my input."

// renderPlanText renders a plan as a numbered list with optional detail lines.
func renderPlanText(p *session.Plan) string {
	var b strings.Builder
	if t := strings.TrimSpace(p.Title); t != "" {
		b.WriteString(t + "\n")
	}
	for i, s := range p.Steps {
		b.WriteString(itoa(i+1) + ". " + strings.TrimSpace(s.Title) + "\n")
		if d := strings.TrimSpace(s.Detail); d != "" {
			b.WriteString("   " + d + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// itoa avoids importing strconv just for small positive ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// toToolPlan converts a session plan to the tools-package shape used by the
// plan_write store (which can't import the session package).
func toToolPlan(p *session.Plan) *tools.Plan {
	if p == nil {
		return nil
	}
	out := &tools.Plan{Title: p.Title}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, tools.PlanStep{Title: s.Title, Detail: s.Detail})
	}
	return out
}

// fromToolPlan converts a tools-package plan into a session plan with the given
// status.
func fromToolPlan(p *tools.Plan, status session.PlanStatus) *session.Plan {
	if p == nil {
		return nil
	}
	out := &session.Plan{Title: p.Title, Status: status}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, session.PlanStep{Title: s.Title, Detail: s.Detail})
	}
	return out
}
