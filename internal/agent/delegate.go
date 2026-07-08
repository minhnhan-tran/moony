package agent

// delegateTool dispatches a focused task to an out-of-process worker — another
// coding agent (Claude Code or Optimus) driven through internal/worker. Unlike
// the in-process `task` sub-agents, a worker has its own context, model, and
// tools; dispatching to the same worker again continues its session. Workers are
// read-only by default, so this is aimed at exploration, review, and planning.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
)

type delegateTool struct {
	mgr *worker.Manager
}

// workerActivityCtxKey carries the per-call worker-activity emitter through the
// context into a streaming tool's Execute (the Tool interface has no emit).
type workerActivityCtxKey struct{}

// workerActivityFn reports one progress line from a worker: kind is
// "tool"|"info"|"text".
type workerActivityFn func(worker, kind, text string)

func withWorkerActivity(ctx context.Context, fn workerActivityFn) context.Context {
	return context.WithValue(ctx, workerActivityCtxKey{}, fn)
}

func workerActivityFrom(ctx context.Context) workerActivityFn {
	if fn, ok := ctx.Value(workerActivityCtxKey{}).(workerActivityFn); ok {
		return fn
	}
	return nil
}

func (t *delegateTool) Name() string { return "delegate" }

func (t *delegateTool) Description() string {
	cfgs := t.mgr.Configs()
	var b strings.Builder
	b.WriteString("Delegate a self-contained task to an external worker agent (Claude Code or Optimus), ")
	b.WriteString("which runs in its own context and returns a final answer. Dispatching to the same worker ")
	b.WriteString("again continues its session, so you can have a multi-turn conversation. Workers are read-only ")
	b.WriteString("by default — use them for exploration, code review, research, or a second opinion. ")
	b.WriteString("Give complete instructions; the worker does not see your conversation. Available workers:\n")
	for _, c := range cfgs {
		desc := c.Desc
		if desc == "" {
			desc = c.Backend + " worker"
		}
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *delegateTool) Schema() json.RawMessage {
	names := make([]string, 0)
	for _, c := range t.mgr.Configs() {
		names = append(names, fmt.Sprintf("%q", c.Name))
	}
	enum := ""
	if len(names) > 0 {
		enum = `, "enum": [` + strings.Join(names, ", ") + `]`
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"worker": {"type": "string", "description": "The worker to dispatch to."` + enum + `},
			"task":   {"type": "string", "description": "Complete instructions for the worker. It does not see your history, so include all needed context."}
		},
		"required": ["worker", "task"]
	}`)
}

// Mutates is true so a dispatch goes through the same approval gate as task/bash:
// it launches an autonomous agent, and the user should opt in.
func (t *delegateTool) Mutates() bool { return true }

func (t *delegateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Worker string `json:"worker"`
		Task   string `json:"task"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid delegate input: %v", err)
	}
	if strings.TrimSpace(in.Task) == "" {
		return "", fmt.Errorf("task is required")
	}

	toolCalls := 0
	activity := workerActivityFrom(ctx) // may be nil (e.g. headless/tests)
	onEvent := func(ev worker.Event) {
		if ev.Kind == "tool" {
			toolCalls++
		}
		// Stream the worker's progress to the UI as it happens.
		if activity != nil {
			activity(in.Worker, ev.Kind, ev.Text)
		}
	}
	res, err := t.mgr.Dispatch(ctx, in.Worker, in.Task, onEvent)
	if err != nil {
		return "", fmt.Errorf("worker %q failed: %v", in.Worker, err)
	}

	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "(worker returned no text)"
	}
	footer := ""
	if toolCalls > 0 {
		footer = fmt.Sprintf("\n\n(worker ran %d tool call(s))", toolCalls)
	}
	if !res.OK && res.Err != "" {
		return fmt.Sprintf("[%s] %s\n%s%s", in.Worker, res.Err, text, footer), nil
	}
	return fmt.Sprintf("[%s]\n%s%s", in.Worker, text, footer), nil
}

// workerResetTool drops a worker's session so the next delegate starts fresh.
type workerResetTool struct {
	mgr *worker.Manager
}

func (t *workerResetTool) Name() string { return "worker_reset" }

func (t *workerResetTool) Description() string {
	return "Reset an external worker's session, discarding its accumulated context so the next delegate to it starts fresh."
}

func (t *workerResetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"worker": {"type": "string", "description": "The worker whose session to reset."}},
		"required": ["worker"]
	}`)
}

func (t *workerResetTool) Mutates() bool { return false }

func (t *workerResetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Worker string `json:"worker"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	t.mgr.Reset(in.Worker)
	return fmt.Sprintf("Reset worker %q.", in.Worker), nil
}

// AttachWorkers registers the delegate + worker_reset tools when any workers are
// configured. A no-op otherwise (no point exposing a tool with nothing to call).
func (a *Agent) AttachWorkers(mgr *worker.Manager) {
	if mgr.Empty() {
		return
	}
	a.workers = mgr
	a.tools.Register(&delegateTool{mgr: mgr})
	a.tools.Register(&workerResetTool{mgr: mgr})
}

// Workers returns the agent's worker manager (may be nil), so hosts can list and
// manage workers (the /workers command).
func (a *Agent) Workers() *worker.Manager { return a.workers }

var (
	_ tools.Tool = (*delegateTool)(nil)
	_ tools.Tool = (*workerResetTool)(nil)
)
