package tools

// exit_plan lets the model end read-only plan mode once it has drafted a plan
// and is ready to start making changes. Like ask_user it has no inline result —
// it round-trips to the UI and blocks for the user's yes/no confirmation. The
// Tool interface has no emit channel, so the host (agent) stashes a "confirmer"
// callback in the context (see WithExitPlanConfirmer), exactly as ask_user gets
// its asker via WithUserAsker. On approval the host marks the plan approved and
// leaves plan mode (re-enabling mutating tools) so the same turn continues into
// execution. When no confirmer is wired (headless runs, sub-agents, tests) the
// tool returns an error so the model falls back to asking the user to /build.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ExitPlanRequest carries the model's optional one-line summary of the plan it
// is ready to execute, shown to the user alongside the approval prompt.
type ExitPlanRequest struct {
	Summary string
}

// ExitPlanConfirmer asks the user to confirm leaving read-only plan mode and
// approving the drafted plan. It returns whether the user approved (so the turn
// should proceed in build mode). The host performs the actual plan-status and
// plan-mode side effects before returning. An error aborts the confirm (e.g.
// the context was cancelled).
type ExitPlanConfirmer func(ctx context.Context, req ExitPlanRequest) (bool, error)

// exitPlanCtxKey carries the per-turn confirmer callback through the context
// into the exit_plan tool's Execute (the Tool interface has no emit channel).
type exitPlanCtxKey struct{}

// WithExitPlanConfirmer returns ctx carrying the confirmer the exit_plan tool
// will use. The host (agent) wires this per tool call, like WithUserAsker.
func WithExitPlanConfirmer(ctx context.Context, fn ExitPlanConfirmer) context.Context {
	return context.WithValue(ctx, exitPlanCtxKey{}, fn)
}

// exitPlanConfirmerFrom returns the confirmer stashed in ctx, or nil when none
// is wired (headless, sub-agent, or test runs).
func exitPlanConfirmerFrom(ctx context.Context) ExitPlanConfirmer {
	if fn, ok := ctx.Value(exitPlanCtxKey{}).(ExitPlanConfirmer); ok {
		return fn
	}
	return nil
}

// exitPlanTool implements the exit_plan capability. It holds no state — the
// confirmer comes from the context per call.
type exitPlanTool struct{}

func (t *exitPlanTool) Name() string { return "exit_plan" }

func (t *exitPlanTool) Description() string {
	return "Signal that you have finished planning in read-only plan mode and are " +
		"ready to start making changes. Call this ONLY after you have drafted the " +
		"plan with plan_write and want to begin executing it. It asks the user to " +
		"approve leaving plan mode; on approval you switch to BUILD mode (mutating " +
		"tools are re-enabled) and should carry out the approved plan step by step. " +
		"If the user declines, stay read-only and keep refining the plan. Do not " +
		"call this to ask a general question — use ask_user for that."
}

func (t *exitPlanTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"summary": {
				"type": "string",
				"description": "Optional one-line summary of the plan you're ready to execute, shown to the user with the approval prompt."
			}
		}
	}`)
}

// Mutates is false: requesting to leave plan mode changes no files, so the tool
// stays available while plan mode hides the mutating tools.
func (t *exitPlanTool) Mutates() bool { return false }

func (t *exitPlanTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Summary string `json:"summary"`
	}
	if len(input) > 0 {
		if err := decode(input, &in); err != nil {
			return "", err
		}
	}

	confirm := exitPlanConfirmerFrom(ctx)
	if confirm == nil {
		return "", fmt.Errorf("exit_plan is unavailable in this environment; ask the user to run /build when they're ready for you to make changes")
	}

	approved, err := confirm(ctx, ExitPlanRequest{Summary: strings.TrimSpace(in.Summary)})
	if err != nil {
		return "", err
	}
	if approved {
		return "The user approved the plan. You are now in BUILD mode — the mutating tools " +
			"(write_file, edit_file, bash, …) are enabled again. Carry out the approved plan " +
			"step by step, mirroring progress with the todo list.", nil
	}
	return "The user chose to keep planning. You remain in read-only plan mode — refine the " +
		"plan with plan_write and call exit_plan again when it's ready. Do not attempt to " +
		"modify files yet.", nil
}
