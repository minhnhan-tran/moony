package web

// Glue that lets a chat turn run a saved Agent Builder workflow via the agent's
// run_workflow tool (the @workflow path). The agent package can't import web
// (web imports agent), so the runner is injected: attachWorkflowTool wires the
// session's agent to runWorkflowForTool + listWorkflowNames here.
//
// A workflow invoked from a chat turn is UNATTENDED — there's no SSE stream to
// answer a human-in-the-loop pause on — so it runs with no HITL emit/runID, and
// the runner's pause() auto-resolves (same as evals). Node-level progress is
// streamed back to the chat via the tool's onEvent callback.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/workflow"
)

// WorkflowToolFuncs returns the run + list funcs that back the agent's
// run_workflow tool, bound to cfg. Exported so the terminal/TUI agent (built
// outside the web Server, in main.go) can wire @workflow + run_workflow exactly
// like the web sessions do — both reuse the same node executors and engine.
func WorkflowToolFuncs(cfg *config.Config) (agent.WorkflowRunFunc, agent.WorkflowLister) {
	run := func(ctx context.Context, ref, input string, onEvent func(kind, text string)) (string, error) {
		return runSavedWorkflow(ctx, cfg, ref, input, onEvent)
	}
	return run, listWorkflowNames
}

// attachWorkflowTool registers the run_workflow tool on a session's agent,
// backed by this server's config-bound runner + workflow lister.
func (s *Server) attachWorkflowTool(sess *session) {
	if sess == nil || sess.agent == nil {
		return
	}
	run, list := WorkflowToolFuncs(s.cfg)
	sess.agent.AttachWorkflows(run, list)
}

// listWorkflowNames returns the saved workflows' names (for the run_workflow tool
// description/enum and @workflow mention resolution). Best-effort: a store error
// yields no names (the tool simply advertises none).
func listWorkflowNames() []string {
	st, err := workflow.Open()
	if err != nil {
		return nil
	}
	wfs, err := st.List()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(wfs))
	for _, wf := range wfs {
		if n := strings.TrimSpace(wf.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// runSavedWorkflow runs the workflow identified by ref (its name or id) with the
// given input, streaming node progress through onEvent, and returns the final
// output. It reuses the same node executors (workflowRunner) + engine the builder
// uses, but headless: no runID/emit, so any HITL node auto-resolves.
func runSavedWorkflow(ctx context.Context, cfg *config.Config, ref, input string, onEvent func(kind, text string)) (string, error) {
	st, err := workflow.Open()
	if err != nil {
		return "", err
	}
	wf, err := resolveWorkflowRef(st, ref)
	if err != nil {
		return "", err
	}

	client := llm.NewClient(cfg)
	runner := &workflowRunner{cfg: cfg, client: client}
	defer runner.Close()
	if ctx != nil {
		runner.done = ctx.Done()
	}

	// Index nodes by id so a progress line can name the concrete target a node runs
	// — its agent persona / skill / worker / tool / MCP tool — not just the node's
	// label (the RunEvent itself doesn't carry the node's Data).
	byID := make(map[string]workflow.Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}
	// label combines the node's display label with its target ("Summarize · ↳ explore",
	// "Scrape · mcp.firecrawl.scrape"), so the chat worker-row shows what each block runs.
	label := func(ev workflow.RunEvent) string {
		base := nodeLabel(ev)
		if tgt := nodeTarget(byID[ev.NodeID]); tgt != "" {
			return base + " · " + tgt
		}
		return base
	}

	// Persist this run to history, just like the builder's Run button — so a
	// workflow invoked from a chat (@workflow / run_workflow) also shows up in the
	// Agent Builder's run history with its trace, output, and cost. The engine
	// serializes emit (incl. parallel branches), so the record mutations below need
	// no lock. Run history is global, so this covers both web sessions and the TUI.
	rec := &workflow.RunRecord{ID: "run_" + hex.EncodeToString(randBytes(8)), WorkflowID: wf.ID, WorkflowName: wf.Name, Input: input, Status: workflow.RunRunning}
	runStore, _ := workflow.OpenRuns()
	saveRec := func() {
		if runStore != nil {
			rec.Vars = runner.snapVars()
			_ = runStore.Save(rec)
		}
	}

	// Translate the engine's RunEvents into chat-facing progress lines + capture
	// the final output, AND accumulate the persisted run record. node_started/
	// completed → "tool" lines (so the chat shows each node), node_delta → streamed
	// "text", failures → "info".
	var final string
	emit := func(ev workflow.RunEvent) {
		switch ev.Type {
		case "node_started":
			onEventSafe(onEvent, "tool", "▶ "+label(ev))
		case "node_completed":
			te := workflow.TraceEntry{NodeID: ev.NodeID, NodeType: ev.NodeType, Label: ev.Label, Status: "completed", Output: ev.Output, Model: ev.Model, DurationMS: ev.DurationMS}
			if ev.Usage != nil {
				te.Usage = *ev.Usage
				rec.CostUSD += ev.Usage.CostUSD
			}
			rec.Trace = append(rec.Trace, te)
			onEventSafe(onEvent, "info", "✓ "+label(ev)+nodeMetaSuffix(ev))
			// Surface the node's actual model response in the chat worker-row, not
			// just the ✓ status + metrics — so you can see what each block produced.
			// (Logic/passthrough nodes like start/end/if-else carry no output.)
			if out := strings.TrimSpace(ev.Output); out != "" {
				onEventSafe(onEvent, "output", label(ev)+": "+out)
				final = ev.Output
			}
		case "node_delta":
			onEventSafe(onEvent, "text", ev.Delta)
		case "node_failed":
			rec.Trace = append(rec.Trace, workflow.TraceEntry{NodeID: ev.NodeID, NodeType: ev.NodeType, Label: ev.Label, Status: "failed", Error: ev.Error, DurationMS: ev.DurationMS})
			onEventSafe(onEvent, "info", "✕ "+label(ev)+": "+ev.Error)
		case "workflow_completed":
			if strings.TrimSpace(ev.Output) != "" {
				final = ev.Output
			}
			rec.Status = workflow.RunCompleted
			rec.Output = final
			saveRec()
		case "workflow_failed":
			rec.Status = workflow.RunFailed
			rec.Error = ev.Error
			saveRec()
		}
	}

	now := func() int64 { return 0 }
	if err := workflow.Run(ctx, wf, input, runner.run, emit, now); err != nil {
		// The run surfaced the failure via emit already; persist it if a terminal
		// event didn't (defensive), then return so the tool reports a clear error.
		if rec.Status == workflow.RunRunning {
			rec.Status = workflow.RunFailed
			rec.Error = err.Error()
			saveRec()
		}
		return final, err
	}
	if strings.TrimSpace(final) == "" {
		return "", errors.New("workflow produced no output")
	}
	return final, nil
}

// onEventSafe forwards a progress line to onEvent when it's set (it may be nil for
// fully-headless callers); the run still records regardless.
func onEventSafe(onEvent func(kind, text string), kind, text string) {
	if onEvent != nil {
		onEvent(kind, text)
	}
}

// nodeLabel is a short human label for a workflow node event.
func nodeLabel(ev workflow.RunEvent) string {
	if l := strings.TrimSpace(ev.Label); l != "" {
		return l
	}
	if ev.NodeType != "" {
		return ev.NodeType
	}
	return ev.NodeID
}

// nodeTarget names the concrete thing a node runs — its agent persona, skill,
// worker, tool, or MCP tool — for the chat progress line, or "" for a node with
// no named target (start/end/if-else/while/transform/guardrails). Mirrors the
// canvas's node subtitles (↳ persona, ✦ skill, ⇲ worker).
func nodeTarget(n workflow.Node) string {
	d := n.Data
	switch n.Type {
	case workflow.NodeAgent:
		if a := strings.TrimSpace(d.Agent); a != "" {
			return "↳ " + a // a saved subagent persona
		}
	case workflow.NodeSkill:
		if s := strings.TrimSpace(d.Skill); s != "" {
			return "✦ " + s
		}
	case workflow.NodeWorker:
		if w := strings.TrimSpace(d.Worker); w != "" {
			return "⇲ " + w
		}
	case workflow.NodeTool, workflow.NodeMCP:
		if t := strings.TrimSpace(d.Tool); t != "" {
			return t // e.g. read_file, mcp.firecrawl.scrape
		}
	}
	return ""
}

// nodeMetaSuffix renders a node_completed's accounting — model · in→out tok ·
// $cost · Nms — as a trailing " · …" string for the chat worker-row, or "" when
// the node carried no metrics (a passthrough like start/end/if-else).
func nodeMetaSuffix(ev workflow.RunEvent) string {
	var parts []string
	if m := strings.TrimSpace(ev.Model); m != "" {
		parts = append(parts, shortModel(m))
	}
	if ev.Usage != nil && (ev.Usage.InputTokens > 0 || ev.Usage.OutputTokens > 0) {
		parts = append(parts, fmt.Sprintf("%d→%d tok", ev.Usage.InputTokens, ev.Usage.OutputTokens))
	}
	if ev.Usage != nil && ev.Usage.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", ev.Usage.CostUSD))
	}
	if ev.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("%dms", ev.DurationMS))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  ·  " + strings.Join(parts, " · ")
}

// resolveWorkflowRef finds a workflow by id first, then by exact name, then by a
// case-insensitive name match.
func resolveWorkflowRef(st *workflow.Store, ref string) (*workflow.Workflow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("workflow is required")
	}
	if wf, err := st.Load(ref); err == nil {
		return wf, nil
	}
	wfs, err := st.List()
	if err != nil {
		return nil, err
	}
	var ci *workflow.Workflow
	for i := range wfs {
		if wfs[i].Name == ref {
			return st.Load(wfs[i].ID)
		}
		if ci == nil && strings.EqualFold(wfs[i].Name, ref) {
			id := wfs[i].ID
			if w, err := st.Load(id); err == nil {
				ci = w
			}
		}
	}
	if ci != nil {
		return ci, nil
	}
	return nil, fmt.Errorf("no workflow named %q", ref)
}
