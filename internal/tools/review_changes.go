package tools

import (
	"context"
	"encoding/json"
	"strings"
)

// reviewChangesTool returns one combined unified diff of every file the agent has
// changed so far in the current turn — the cohesive multi-file picture a reviewer
// (human or the model itself) wants before finalizing a change, instead of the
// per-file edit results scattered through the turn. It's read-only: it renders
// the agent's snapshot store (wired via Registry.SetChangeReviewer) and never
// touches the filesystem. Use it to self-check a multi-file refactor as a whole;
// pair it with /undo to reject the whole turn's changes atomically.
type reviewChangesTool struct {
	r   *Registry
	max int
}

func (t *reviewChangesTool) Name() string { return "review_changes" }

func (t *reviewChangesTool) Description() string {
	return "Show a single combined unified diff of every file you've changed so far in the current turn " +
		"(a cohesive multi-file view, not per-file). Read-only. Useful to review a large multi-file change as " +
		"one diff before finishing. Takes no arguments."
}

func (t *reviewChangesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *reviewChangesTool) Mutates() bool { return false }

func (t *reviewChangesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if t.r == nil || t.r.changeReviewer == nil {
		return "Change review is not available in this context.", nil
	}
	// Matches the rest of the package's convention (see truncate in bash.go):
	// max<=0 means unbounded, not "fall back to some default cap".
	max := t.max
	if max <= 0 {
		max = 1 << 30 // effectively unbounded for a diff
	}
	diff := strings.TrimSpace(t.r.changeReviewer(max))
	if diff == "" {
		return "No files have been changed in the current turn.", nil
	}
	return diff, nil
}
