package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Plan is a first-class plan artifact: a titled, ordered list of steps the
// agent drafts and the user amends/approves before execution. It mirrors the
// session-level shape (kept duplicated here so the tools package stays free of a
// session-package import cycle); the agent translates between the two.
type Plan struct {
	Title string     `json:"title,omitempty"`
	Steps []PlanStep `json:"steps"`
}

// PlanStep is a single ordered step of a plan.
type PlanStep struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// PlanStore is a thread-safe holder for the session's current draft plan. The
// plan_write tool overwrites it (Set) and the host (TUI/agent) reads it (Get)
// and subscribes to updates (SetNotify) — mirroring TodoStore.
type PlanStore struct {
	mu     sync.RWMutex
	plan   *Plan
	notify func(*Plan)
}

// NewPlanStore creates an empty plan store.
func NewPlanStore() *PlanStore { return &PlanStore{} }

// SetNotify registers a callback fired (with a copy of the plan) after every
// Set. Pass nil to clear it.
func (s *PlanStore) SetNotify(fn func(*Plan)) {
	s.mu.Lock()
	s.notify = fn
	s.mu.Unlock()
}

// Set replaces the draft plan and fires the notification (outside the lock).
// A nil plan clears the store. Called by the plan_write TOOL only — genuine
// model-authored drafts/revisions, which is what the notify callback (wired to
// applyDraftPlan) interprets this as.
func (s *PlanStore) Set(p *Plan) {
	s.mu.Lock()
	s.plan = clonePlan(p)
	cb := s.notify
	snapshot := clonePlan(s.plan)
	s.mu.Unlock()
	if cb != nil {
		cb(snapshot)
	}
}

// Sync updates the cached snapshot WITHOUT firing the notify callback. Use this
// for host-side mirroring of a status change that already happened elsewhere
// (e.g. the agent retiring or completing a plan) — as opposed to Set, which the
// plan_write tool uses for genuine model-authored drafts. Without this, an
// internal mirror write would re-enter applyDraftPlan via the notify and reset
// the plan's Status back to draft, undoing the completion/retirement.
func (s *PlanStore) Sync(p *Plan) {
	s.mu.Lock()
	s.plan = clonePlan(p)
	s.mu.Unlock()
}

// Get returns a copy of the current draft plan, or nil when none is set.
func (s *PlanStore) Get() *Plan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePlan(s.plan)
}

func clonePlan(p *Plan) *Plan {
	if p == nil {
		return nil
	}
	out := &Plan{Title: p.Title}
	out.Steps = append([]PlanStep(nil), p.Steps...)
	return out
}

// planWriteTool is the agent-facing tool that drafts or revises the session
// plan. It is non-mutating (no filesystem/shell effects) and always produces a
// DRAFT — approving a plan is an explicit user action, never the model's.
type planWriteTool struct {
	store *PlanStore
}

func (t *planWriteTool) Name() string { return "plan_write" }

func (t *planWriteTool) Description() string {
	return "Draft or revise the session PLAN: an ordered, user-reviewable list " +
		"of the steps you intend to take before you start editing files. Pass the " +
		"COMPLETE plan every call (it replaces the previous draft). Use this when " +
		"asked to plan, or before a non-trivial multi-step change, so the user can " +
		"amend and approve it first. This tool IS how you present a plan — it's " +
		"displayed to the user inline for review and approval; do NOT write the plan " +
		"to a file (e.g. PLAN.md) unless the user explicitly asks for a file. The plan " +
		"is a DRAFT until the user approves it; do not begin executing it until then. " +
		"Each step: {title, detail?}."
}

func (t *planWriteTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "description": "A short title for the overall plan."},
			"steps": {
				"type": "array",
				"description": "The full ordered list of steps, replacing the previous draft.",
				"items": {
					"type": "object",
					"properties": {
						"title":  {"type": "string", "description": "Short imperative description of the step."},
						"detail": {"type": "string", "description": "Optional clarifying detail for the step."}
					},
					"required": ["title"]
				}
			}
		},
		"required": ["steps"]
	}`)
}

func (t *planWriteTool) Mutates() bool { return false }

func (t *planWriteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in Plan
	if err := decode(input, &in); err != nil {
		return "", err
	}
	steps := make([]PlanStep, 0, len(in.Steps))
	for i := range in.Steps {
		if strings.TrimSpace(in.Steps[i].Title) == "" {
			return "", fmt.Errorf("step %d: title is required", i+1)
		}
		steps = append(steps, in.Steps[i])
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("a plan must have at least one step")
	}
	in.Steps = steps
	t.store.Set(&in)

	var b strings.Builder
	if strings.TrimSpace(in.Title) != "" {
		fmt.Fprintf(&b, "Drafted plan: %s\n", strings.TrimSpace(in.Title))
	} else {
		fmt.Fprintf(&b, "Drafted plan (%d steps):\n", len(steps))
	}
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s.Title)
		if d := strings.TrimSpace(s.Detail); d != "" {
			fmt.Fprintf(&b, "   %s\n", d)
		}
	}
	b.WriteString("\nThis is a DRAFT awaiting the user's approval. Do not start executing it until the user approves (they can amend it first).")
	return strings.TrimRight(b.String(), "\n"), nil
}
