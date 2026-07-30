package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Companion tools for background/async sub-agents (task background:true). They
// read and control detached runs registered in the Agent's TaskStore.
//
//   task_output(handle)  — new streamed output since the last poll + status, and
//                          (once done) the final result plus a cost/time footer.
//   task_status(handle)  — current status only, without consuming output.
//   task_cancel(handle)  — stop a running background task.

type handleInput struct {
	Handle string `json:"handle"`
}

func decodeHandle(input json.RawMessage) (string, error) {
	var in handleInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	h := strings.TrimSpace(in.Handle)
	if h == "" {
		return "", fmt.Errorf("handle is required (e.g. task_bg_1)")
	}
	return h, nil
}

// unknownHandleErr builds a clear error listing the live handles.
func unknownHandleErr(store *TaskStore, h string) error {
	live := store.handles()
	if len(live) == 0 {
		return fmt.Errorf("no background task %q — there are no running background tasks (start one with task(..., background:true))", h)
	}
	return fmt.Errorf("no background task %q — running handles: %s", h, strings.Join(live, ", "))
}

func usageFooter(cost float64, in, out int, dur interface{ String() string }) string {
	return fmt.Sprintf("\n\n— %s · %d in / %d out tokens · $%.4f", dur.String(), in, out, cost)
}

// --- task_output ----------------------------------------------------------

type taskOutputTool struct{ store *TaskStore }

func (t *taskOutputTool) Name() string { return "task_output" }
func (t *taskOutputTool) Description() string {
	return "Return any new output produced by a background sub-agent task since your last poll, plus its status (running/done/failed/cancelled). When the task is done it also returns the final result (and, for an isolated task, the diff) followed by a token/cost footer. Use the handle returned by task(..., background:true). Poll again while it's still running to get more output. Read-only."
}
func (t *taskOutputTool) Mutates() bool { return false }
func (t *taskOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string","description":"The background task handle returned by task(..., background:true), e.g. task_bg_1."}},"required":["handle"]}`)
}
func (t *taskOutputTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	h, err := decodeHandle(input)
	if err != nil {
		return "", err
	}
	bt, ok := t.store.get(h)
	if !ok {
		return "", unknownHandleErr(t.store, h)
	}
	newOut, state := bt.snapshot()
	if state == taskRunning {
		if strings.TrimSpace(newOut) == "" {
			return fmt.Sprintf("[%s] still running (%s) — no new output since the last poll. Poll again later.", h, bt.persona), nil
		}
		return fmt.Sprintf("[%s] running (%s) — new output:\n%s", h, bt.persona, newOut), nil
	}
	// Terminal: deliver the final result + footer once finished.
	result, errMsg, cost, in, out, dur := bt.finalResult()
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s (%s).\n\n", h, state.String(), bt.persona)
	if state == taskFailed && errMsg != "" {
		fmt.Fprintf(&b, "error: %s\n\n", errMsg)
	}
	if strings.TrimSpace(result) != "" {
		b.WriteString(result)
	} else if strings.TrimSpace(newOut) != "" {
		b.WriteString(newOut)
	}
	b.WriteString(usageFooter(cost, in, out, dur))
	return b.String(), nil
}

// --- task_status ----------------------------------------------------------

type taskStatusTool struct{ store *TaskStore }

func (t *taskStatusTool) Name() string { return "task_status" }
func (t *taskStatusTool) Description() string {
	return "Report a background sub-agent task's current status (running/done/failed/cancelled) WITHOUT consuming its buffered output (unlike task_output). Pass the handle from task(..., background:true). With no handle, lists all background tasks. Read-only."
}
func (t *taskStatusTool) Mutates() bool { return false }
func (t *taskStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string","description":"The background task handle, e.g. task_bg_1. Omit to list all background tasks."}}}`)
}
func (t *taskStatusTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in handleInput
	_ = json.Unmarshal(input, &in)
	h := strings.TrimSpace(in.Handle)
	if h == "" {
		tasks := t.store.list()
		if len(tasks) == 0 {
			return "No background tasks.", nil
		}
		var b strings.Builder
		b.WriteString("Background tasks:\n")
		for _, bt := range tasks {
			fmt.Fprintf(&b, "  %s — %s — %s\n", bt.id, bt.persona, bt.status().String())
		}
		return b.String(), nil
	}
	bt, ok := t.store.get(h)
	if !ok {
		return "", unknownHandleErr(t.store, h)
	}
	return fmt.Sprintf("[%s] %s (%s).", h, bt.status().String(), bt.persona), nil
}

// --- task_cancel ----------------------------------------------------------

type taskCancelTool struct{ store *TaskStore }

func (t *taskCancelTool) Name() string { return "task_cancel" }
func (t *taskCancelTool) Description() string {
	return "Stop a running background sub-agent task started with task(..., background:true). Pass its handle. A cancelled task's partial output is still retrievable with task_output. No-op if it already finished. Only stops work — makes no other changes."
}
func (t *taskCancelTool) Mutates() bool { return false }
func (t *taskCancelTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string","description":"The background task handle to cancel, e.g. task_bg_1."}},"required":["handle"]}`)
}
func (t *taskCancelTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	h, err := decodeHandle(input)
	if err != nil {
		return "", err
	}
	bt, ok := t.store.get(h)
	if !ok {
		return "", unknownHandleErr(t.store, h)
	}
	if bt.status() != taskRunning {
		return fmt.Sprintf("[%s] already %s — nothing to cancel.", h, bt.status().String()), nil
	}
	if bt.cancel != nil {
		bt.cancel()
	}
	return fmt.Sprintf("[%s] cancel requested; it will stop shortly. Use task_output(%s) for its partial result.", h, h), nil
}
