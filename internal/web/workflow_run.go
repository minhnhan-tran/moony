// workflow_run.go — Agent Builder Phase 2: execute a saved workflow and stream a
// per-node trace over SSE (POST /api/workflows/{wid}/run). Laptop-scoped, like
// the rest of /api/workflows, so it runs on the connector where the agent runtime
// + tools live; the relay tunnels it. Each "agent" node runs through
// agent.RunScoped (the headless scoped-agent helper); other node types are simple
// passthroughs for now (executors arrive in later phases).

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
	"github.com/pysty1995/moony/internal/workflow"
)

// handleRunWorkflow: POST /api/workflows/{wid}/run  { "input": "..." } → SSE trace.
func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf, err := st.Load(r.PathValue("wid"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reject a malformed parallel/join region up front (no-op for graphs without
	// one), so a structural mistake surfaces as one clear message rather than a
	// mid-run failure. Streamed as a workflow_failed event below would also work,
	// but the run hasn't started yet, so a 400 is cleaner.
	if verr := workflow.Validate(wf); verr != nil {
		http.Error(w, verr.Error(), http.StatusBadRequest)
		return
	}

	var body struct {
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// One LLM client + base config for this run (the connector's own config).
	// The engine serializes all emit calls (parallel branches emit concurrently),
	// so this sink doesn't need its own lock.
	send := func(ev workflow.RunEvent) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	client := llm.NewClient(s.cfg)
	runner := s.workflowNodeRunner(client)
	defer runner.Close() // tear down MCP servers + worker sessions on EVERY exit (incl. panic/cancel)
	runner.runID = "run_" + hex.EncodeToString(randBytes(8))
	runner.emit = send
	runner.done = r.Context().Done()
	// Tell the browser this run's id up front so it can POST /resume on a pause.
	send(workflow.RunEvent{Type: "run_id", Output: runner.runID})

	// Persist the run for history + resume. The recorder updates a RunRecord from
	// the trace as it streams and saves at terminal events; the runner also exposes
	// the live variable bus so a pause can be resumed later.
	rec := &workflow.RunRecord{ID: runner.runID, WorkflowID: wf.ID, WorkflowName: wf.Name, Input: body.Input, Status: workflow.RunRunning}
	runStore, _ := workflow.OpenRuns()
	recEmit := s.recordingEmit(send, rec, runner, runStore)

	ctx := r.Context()
	now := func() int64 { return time.Now().UnixMilli() }
	_ = workflow.RunWithOptions(ctx, wf, workflow.RunOptions{Input: body.Input}, runner.run, recEmit, now)
	// Errors are already surfaced as workflow_failed trace events; the recorder
	// has persisted the final state. Cleanup runs via the deferred runner.Close().
}

// recordingEmit wraps the SSE sink with run-record bookkeeping: it forwards every
// event to the browser, accumulates a compact trace + cost + final vars on the
// RunRecord, and persists at terminal events (completed/failed/paused). The
// runner's live variable bus is snapshotted so a paused run is resumable.
func (s *Server) recordingEmit(send func(workflow.RunEvent), rec *workflow.RunRecord, runner *workflowRunner, store *workflow.RunStore) func(workflow.RunEvent) {
	// The engine serializes every emit call (incl. those from parallel branch
	// goroutines), so this recorder's rec mutations + store save don't need a lock.
	save := func() {
		if store != nil {
			rec.Vars = runner.snapVars()
			_ = store.Save(rec)
		}
	}
	return func(ev workflow.RunEvent) {
		send(ev)
		switch ev.Type {
		case "node_completed":
			te := workflow.TraceEntry{NodeID: ev.NodeID, NodeType: ev.NodeType, Label: ev.Label, Status: "completed", Output: ev.Output, Model: ev.Model, DurationMS: ev.DurationMS}
			if ev.Usage != nil {
				te.Usage = *ev.Usage
				rec.CostUSD += ev.Usage.CostUSD
			}
			rec.Trace = append(rec.Trace, te)
			// CurrentNode tracks the resume point. A parallel region is atomic for
			// resume (re-run from the parallel node), so DON'T advance it to a node
			// completed inside a branch — only to top-level completions. The branch
			// nodes carry a GroupID; the parallel node's own completion (NodeType
			// "parallel") is top-level and is the correct resume anchor for the region.
			if ev.GroupID == "" {
				rec.CurrentNode = ev.NodeID
			}
		case "node_failed":
			rec.Trace = append(rec.Trace, workflow.TraceEntry{NodeID: ev.NodeID, NodeType: ev.NodeType, Label: ev.Label, Status: "failed", Error: ev.Error, DurationMS: ev.DurationMS})
		case "node_retry":
			rec.Trace = append(rec.Trace, workflow.TraceEntry{NodeID: ev.NodeID, NodeType: ev.NodeType, Label: ev.Label, Status: "retry", Error: ev.Error})
		case "workflow_paused":
			rec.Status = workflow.RunPaused
			rec.CurrentNode = ev.NodeID // resume re-runs the approval node
			rec.Trace = append(rec.Trace, workflow.TraceEntry{NodeID: ev.NodeID, Label: ev.Label, Status: "paused", Output: ev.Prompt})
			save()
		case "workflow_completed":
			rec.Status = workflow.RunCompleted
			rec.Output = ev.Output
			save()
		case "workflow_failed":
			if rec.Status != workflow.RunPaused { // a pause emits failed on reject; keep the real cause
				rec.Status = workflow.RunFailed
			}
			rec.Error = ev.Error
			save()
		}
	}
}

// workflowRunner executes workflow nodes for one run. It owns a lazily-built
// tools registry (with this session's enabled MCP servers connected) shared by
// every tool/mcp node in the run, torn down by Close when the run ends. For
// human-in-the-loop, it also carries this run's id, an emit sink (to surface a
// workflow_paused event), and the request's done channel (to unblock a pending
// approval if the SSE connection drops).
type workflowRunner struct {
	cfg       *config.Config
	client    llm.Streamer
	reg       *tools.Registry
	regOnce   sync.Once // guards the lazy registry build (branches may first-touch concurrently)
	mcpMgr    *mcp.Manager
	workers   *worker.Manager
	workersMu sync.Mutex // guards the lazy worker-manager build (branches may first-touch concurrently)

	runID string
	emit  func(workflow.RunEvent)
	done  <-chan struct{}

	pauseSeq int64 // monotonic per-run counter for unique pause ids (atomic; branches may pause)

	varsMu   sync.Mutex     // guards lastVars (snapshotted from concurrent branch nodes)
	lastVars map[string]any // snapshot of the live variable bus (for resume persistence)
}

// workflowNodeRunner builds a runner for one workflow run.
func (s *Server) workflowNodeRunner(client llm.Streamer) *workflowRunner {
	return &workflowRunner{cfg: s.cfg, client: client}
}

// snapVars returns the last-snapshotted variable bus under lock (for the recorder).
func (wr *workflowRunner) snapVars() map[string]any {
	wr.varsMu.Lock()
	defer wr.varsMu.Unlock()
	return wr.lastVars
}

// Close tears down any MCP servers and worker sessions connected during the run.
func (wr *workflowRunner) Close() {
	if wr.mcpMgr != nil {
		wr.mcpMgr.Close()
		wr.mcpMgr = nil
	}
	if wr.workers != nil {
		wr.workers.CloseAll()
		wr.workers = nil
	}
}

// registry lazily builds (once) a default tools registry rooted at the working
// dir, with the session's enabled MCP servers connected and registered, so both
// "tool" and "mcp" nodes can resolve names via Registry.Execute. The build is
// guarded by sync.Once so concurrent tool/mcp nodes in a parallel region don't
// race to build it twice.
func (wr *workflowRunner) registry(ctx context.Context) *tools.Registry {
	wr.regOnce.Do(func() {
		wr.reg = tools.Default(wr.cfg.WorkingDir, wr.cfg.MaxToolOutputBytes)
		if mcp.Enabled() {
			mgmt := manage.Load(wr.cfg.WorkingDir)
			cfgs, _ := mcp.LoadConfigs(wr.cfg.WorkingDir)
			cfgs = manage.Filter(cfgs, func(c mcp.Config) string { return c.Name }, mgmt.MCPEnabled)
			if len(cfgs) > 0 {
				wr.mcpMgr = mcp.ConnectAll(ctx, cfgs, func(string) {})
				wr.mcpMgr.RegisterTools(wr.reg)
			}
		}
	})
	return wr.reg
}

// Run is the engine NodeFunc: it dispatches each node to its executor.
func (wr *workflowRunner) run(ctx context.Context, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	if !in.InBranch {
		// Snapshot the live top-level bus so a pause/fail can be resumed. Branch
		// nodes run against a forked copy, and a region is atomic for resume, so
		// they don't snapshot (it would persist a partial branch bus). Copy rather
		// than alias the live map, so the recorder can marshal lastVars without
		// racing the engine's ongoing bus writes.
		snap := make(map[string]any, len(in.Vars))
		for k, v := range in.Vars {
			snap[k] = v
		}
		wr.varsMu.Lock()
		wr.lastVars = snap
		wr.varsMu.Unlock()
	}
	baseCfg, client := wr.cfg, wr.client
	d := in.Node.Data
	switch in.Node.Type {
	case workflow.NodeStart, workflow.NodeEnd, workflow.NodeNote:
		return workflow.NodeOutput{Text: in.Input}, nil

	case workflow.NodeParallel:
		// Marker only: the engine fans out the branches and runs the join. If reached
		// as a normal node (it shouldn't be), pass the input through.
		return workflow.NodeOutput{Text: in.Input}, nil

	case workflow.NodeJoin:
		// Barrier: combine the branch outputs the engine collected for us.
		return workflow.NodeOutput{Text: combineBranches(d, in.BranchInputs, in.Input)}, nil

	case workflow.NodeAgent:
		// Per-node gate: pause for approval BEFORE the agent runs, if configured.
		if d.RequireApproval && wr.hitlActiveIn(in) {
			msg := workflow.Interpolate(strings.TrimSpace(d.Message), in.Vars)
			if msg == "" {
				label := strings.TrimSpace(d.Label)
				if label == "" {
					label = "this agent step"
				}
				msg = "Run " + label + "?"
			}
			res := wr.pause(workflow.RunEvent{NodeID: in.Node.ID, Label: d.Label, Prompt: msg, PauseMode: "approval"})
			if !res.approved {
				return workflow.NodeOutput{}, fmt.Errorf("approval rejected")
			}
		}
		prompt := buildAgentPrompt(d, in.Input)
		// Interpolate {{input}} / {{lastOutput}} / {{nodeId}} refs in the system
		// prompt against the variable bus (the "Insert variable" picker writes
		// these), so an agent node can pull in specific upstream outputs — not just
		// the immediate previous one that arrives as the prompt.
		system := workflow.Interpolate(strings.TrimSpace(d.Instructions), in.Vars)
		// Structured output: ask for JSON matching the schema, then validate.
		if strings.TrimSpace(d.OutputSchema) != "" {
			system = strings.TrimSpace(system + "\n\nRespond with ONLY a single JSON object that conforms to this JSON Schema. No prose, no code fences.\n" + d.OutputSchema)
		}
		scoped := agent.ScopedOptions{
			System:     system,
			Tools:      d.Tools,
			Skills:     d.Skills,
			Model:      strings.TrimSpace(d.Model),
			WorkingDir: baseCfg.WorkingDir,
		}
		// When HITL is active, let the agent pause the run for mid-loop tool
		// approvals and ask_user questions (surfaced in the trace, resumed by the
		// browser) instead of silently auto-approving / dropping them. Require
		// "prompt" approval so mutating tools actually trigger the gate.
		if wr.hitlActiveIn(in) {
			node := in.Node
			scoped.ApprovalPolicy = "prompt"
			scoped.OnApproval = func(tool, summary, diff string) bool {
				p := strings.TrimSpace(summary)
				if p == "" {
					p = "Allow the " + tool + " tool to run?"
				}
				res := wr.pause(workflow.RunEvent{NodeID: node.ID, Label: node.Data.Label, Prompt: p, PauseMode: "approval", PauseTool: tool, PauseDiff: diff})
				return res.approved
			}
			scoped.OnQuestion = func(question string, options []agent.QuestionOption, multiple bool) []string {
				opts := make([]workflow.PauseOption, 0, len(options))
				for _, o := range options {
					opts = append(opts, workflow.PauseOption{Label: o.Label, Description: o.Description})
				}
				res := wr.pause(workflow.RunEvent{NodeID: node.ID, Label: node.Data.Label, Prompt: question, PauseMode: "question", PauseOptions: opts, PauseMultiple: multiple})
				if !res.approved || strings.TrimSpace(res.text) == "" {
					return nil
				}
				return []string{res.text}
			}
		}
		res, err := agent.RunScoped(ctx, client, baseCfg, scoped, prompt, onDelta)
		if err != nil {
			return workflow.NodeOutput{}, err
		}
		if strings.TrimSpace(d.OutputSchema) != "" {
			if verr := workflow.ValidateStructured(res.Text, d.OutputSchema); verr != nil {
				return workflow.NodeOutput{}, fmt.Errorf("structured output invalid: %v", verr)
			}
		}
		model := strings.TrimSpace(d.Model)
		if model == "" {
			model = baseCfg.Model // node inherits the connector's model
		}
		return workflow.NodeOutput{
			Text:  res.Text,
			Usage: workflow.NodeUsage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CostUSD: res.CostUSD},
			Model: model,
		}, nil

	case workflow.NodeGuardrails:
		cfg := workflow.GuardrailConfig{PII: d.GuardPII, Jailbreak: d.GuardJailbreak, Moderation: d.GuardModeration, LLMJudge: d.GuardLLMJudge}
		gr := workflow.CheckGuardrails(ctx, client, baseCfg, cfg, in.Input)
		if !gr.Passed {
			msg := "guardrail violations: " + strings.Join(gr.Violations, "; ")
			if onDelta != nil {
				onDelta(msg)
			}
			if strings.EqualFold(strings.TrimSpace(d.ActionOnViolation), "warn") {
				return workflow.NodeOutput{Text: in.Input}, nil // warn: continue
			}
			return workflow.NodeOutput{}, errors.New(msg) // block (default)
		}
		return workflow.NodeOutput{Text: in.Input}, nil

	case workflow.NodeTool, workflow.NodeMCP:
		return wr.runTool(ctx, d, in, onDelta)

	case workflow.NodeTransform:
		// Interpolate the transform template against the variable bus. With no
		// template, pass the input through (a relabeling/no-op node).
		tpl := d.Transform
		if strings.TrimSpace(tpl) == "" {
			return workflow.NodeOutput{Text: in.Input}, nil
		}
		out := workflow.Interpolate(tpl, in.Vars)
		return workflow.NodeOutput{Text: out}, nil

	case workflow.NodeIfElse:
		ok := workflow.EvalCondition(d.Condition, in.Vars)
		branch := "else"
		if ok {
			branch = "if"
		}
		if onDelta != nil {
			onDelta("condition → " + branch)
		}
		return workflow.NodeOutput{Text: in.Input, Branch: branch}, nil

	case workflow.NodeWhile:
		return wr.runWhile(d, in, onDelta)

	case workflow.NodeApproval:
		return wr.runApproval(d, in, onDelta)

	case workflow.NodeUserInput:
		return wr.runUserInput(d, in, onDelta)

	case workflow.NodeWorker:
		return wr.runWorker(ctx, d, in, onDelta)

	case workflow.NodeSkill:
		return wr.runSkill(ctx, d, in, onDelta)

	default:
		// Unknown node type: pass through so the run still completes.
		if onDelta != nil {
			onDelta(fmt.Sprintf("[%s passed through]", in.Node.Type))
		}
		return workflow.NodeOutput{Text: in.Input}, nil
	}
}

// runWorker delegates the upstream input as a task to a named out-of-process
// worker engine (Claude Code / Optimus / Gemini), the same path as the agent's
// `delegate` tool. The worker manager is built lazily and reused for the run.
func (wr *workflowRunner) runWorker(ctx context.Context, d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	name := strings.TrimSpace(d.Worker)
	if name == "" {
		return workflow.NodeOutput{}, errors.New("no worker selected for this node")
	}
	wr.workersMu.Lock()
	if wr.workers == nil {
		cfgs, _ := worker.Load(wr.cfg.WorkingDir)
		wr.workers = worker.New(wr.cfg.WorkingDir, cfgs)
	}
	workers := wr.workers
	wr.workersMu.Unlock()
	task := workflow.Interpolate(strings.TrimSpace(d.Instructions), in.Vars)
	if task == "" {
		task = in.Input // no template → the upstream output is the task
	}
	if onDelta != nil {
		onDelta("delegating to " + name + "…")
	}
	res, err := workers.Dispatch(ctx, name, task, func(ev worker.Event) {
		if onDelta != nil && ev.Text != "" {
			onDelta(ev.Text)
		}
	})
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("worker %q failed: %v", name, err)
	}
	text := strings.TrimSpace(res.Text)
	if !res.OK {
		msg := res.Err
		if msg == "" {
			msg = "unknown error"
		}
		return workflow.NodeOutput{}, fmt.Errorf("worker %q: %s", name, msg)
	}
	return workflow.NodeOutput{Text: text}, nil
}

// runSkill runs an agent with a single Agent Skill loaded and instructs it to use
// that skill on the upstream input — a one-node "apply this skill" step.
func (wr *workflowRunner) runSkill(ctx context.Context, d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	name := strings.TrimSpace(d.Skill)
	if name == "" {
		return workflow.NodeOutput{}, errors.New("no skill selected for this node")
	}
	system := "Use the \"" + name + "\" skill (load it with the skill tool) to handle the input."
	if extra := strings.TrimSpace(d.Instructions); extra != "" {
		system += "\n\n" + workflow.Interpolate(extra, in.Vars) // resolve {{...}} refs in the extra guidance
	}
	input := strings.TrimSpace(in.Input)
	if input == "" {
		input = "Apply the skill."
	}
	res, err := agent.RunScoped(ctx, wr.client, wr.cfg, agent.ScopedOptions{
		System:     system,
		Skills:     []string{name},
		WorkingDir: wr.cfg.WorkingDir,
	}, input, onDelta)
	if err != nil {
		return workflow.NodeOutput{}, err
	}
	return workflow.NodeOutput{
		Text:  res.Text,
		Usage: workflow.NodeUsage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CostUSD: res.CostUSD},
		Model: wr.cfg.Model,
	}, nil
}

// hitlActive reports whether this run can surface human-in-the-loop pauses: it
// needs an emit sink (the SSE stream) and a run id (so the browser can resume).
// Evals and other unattended runs leave these empty, so HITL auto-resolves.
func (wr *workflowRunner) hitlActive() bool { return wr.emit != nil && wr.runID != "" }

// hitlActiveIn is hitlActive, additionally disabled INSIDE a parallel branch:
// concurrent live pauses don't compose with the single-valued resume model in
// v1, so an agent node within a branch auto-resolves its approvals/questions
// rather than pausing. Pauses outside parallel regions are unaffected.
func (wr *workflowRunner) hitlActiveIn(in workflow.NodeInput) bool {
	return wr.hitlActive() && !in.InBranch
}

// combineBranches merges a join node's branch outputs per its JoinMode:
//   - "array": a JSON array of the branch texts
//   - "first": the first branch's text (first-wins)
//   - "concat" (default): the branch texts joined by JoinSeparator ("\n\n---\n\n")
//
// With no branch inputs (e.g. a join reached outside a region) it returns the
// upstream input unchanged.
func combineBranches(d workflow.NodeData, branches []string, input string) string {
	if len(branches) == 0 {
		return input
	}
	switch strings.ToLower(strings.TrimSpace(d.JoinMode)) {
	case "array":
		b, err := json.Marshal(branches)
		if err != nil {
			return strings.Join(branches, "\n")
		}
		return string(b)
	case "first":
		return branches[0]
	default: // "concat"
		sep := d.JoinSeparator
		if sep == "" {
			sep = "\n\n---\n\n"
		}
		return strings.Join(branches, sep)
	}
}

// pause emits a workflow_paused event and blocks until the matching resume
// arrives (or the SSE connection drops). It returns the user's resolution. The
// caller supplies the mode-specific event fields via ev; pause fills in the
// run/pause ids. When HITL isn't active it returns a sensible default
// (approved, empty text) so unattended runs continue.
func (wr *workflowRunner) pause(ev workflow.RunEvent) pauseResolution {
	if !wr.hitlActive() {
		return pauseResolution{approved: true}
	}
	pauseID := fmt.Sprintf("p%d", atomic.AddInt64(&wr.pauseSeq, 1))
	ev.Type = "workflow_paused"
	ev.PauseID = pauseID
	wr.emit(ev)
	return workflowApprovals.wait(wr.runID, pauseID, wr.done)
}

// runApproval pauses the run for human approval (the standalone user-approval
// node). Approve → pass the input through; reject → fail.
func (wr *workflowRunner) runApproval(d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	if !wr.hitlActive() {
		// No HITL channel (e.g. during evals): auto-approve so the suite runs.
		return workflow.NodeOutput{Text: in.Input}, nil
	}
	msg := strings.TrimSpace(d.Message)
	if msg == "" {
		msg = "Approve to continue."
	}
	msg = workflow.Interpolate(msg, in.Vars)
	if onDelta != nil {
		onDelta("awaiting approval…")
	}
	res := wr.pause(workflow.RunEvent{NodeID: in.Node.ID, Label: in.Node.Data.Label, Prompt: msg, PauseMode: "approval"})
	if !res.approved {
		note := strings.TrimSpace(res.text)
		if note == "" {
			note = "rejected"
		}
		return workflow.NodeOutput{}, fmt.Errorf("approval %s", note)
	}
	return workflow.NodeOutput{Text: in.Input}, nil
}

// runUserInput pauses the run to collect free-text input from the user; the typed
// value becomes the node's output (threaded downstream as {{nodeId}} / the next
// node's input). With no HITL channel (evals), it passes the upstream input
// through so unattended runs still complete.
func (wr *workflowRunner) runUserInput(d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	if !wr.hitlActive() {
		return workflow.NodeOutput{Text: in.Input}, nil
	}
	msg := strings.TrimSpace(d.Message)
	if msg == "" {
		msg = "Enter input to continue."
	}
	msg = workflow.Interpolate(msg, in.Vars)
	if onDelta != nil {
		onDelta("awaiting input…")
	}
	res := wr.pause(workflow.RunEvent{NodeID: in.Node.ID, Label: in.Node.Data.Label, Prompt: msg, PauseMode: "input"})
	if !res.approved {
		return workflow.NodeOutput{}, fmt.Errorf("input cancelled")
	}
	return workflow.NodeOutput{Text: res.text}, nil
}

// randBytes returns n cryptographically-random bytes (for run ids).
func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// runTool executes a tool or MCP node by resolving the named tool in the run's
// registry and calling it with interpolated JSON arguments.
func (wr *workflowRunner) runTool(ctx context.Context, d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	name := strings.TrimSpace(d.Tool)
	if name == "" {
		return workflow.NodeOutput{}, errors.New("no tool selected for this node")
	}
	reg := wr.registry(ctx)
	if !reg.Has(name) {
		return workflow.NodeOutput{}, fmt.Errorf("tool %q is not available (built-ins: read_file/grep/glob/list_dir/bash/…; MCP tools are named mcp.<server>.<tool>)", name)
	}
	// Build the arguments JSON: the interpolated ToolArgs template, else the
	// upstream input if it's already JSON, else an empty object.
	argStr := strings.TrimSpace(d.ToolArgs)
	if argStr != "" {
		argStr = workflow.Interpolate(argStr, in.Vars)
	} else if json.Valid([]byte(strings.TrimSpace(in.Input))) {
		argStr = strings.TrimSpace(in.Input)
	} else {
		argStr = "{}"
	}
	if !json.Valid([]byte(argStr)) {
		return workflow.NodeOutput{}, fmt.Errorf("tool arguments are not valid JSON: %s", truncateStr(argStr, 120))
	}
	if onDelta != nil {
		onDelta("calling " + name + "…")
	}
	out, err := reg.Execute(ctx, name, json.RawMessage(argStr))
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("tool %q failed: %v", name, err)
	}
	return workflow.NodeOutput{Text: out}, nil
}

// runWhile evaluates the loop condition and routes "continue" (loop body) or
// "break" (exit). The engine's global step cap plus the node's MaxIterations
// bound the loop; the per-node counter lives in the variable bus.
func (wr *workflowRunner) runWhile(d workflow.NodeData, in workflow.NodeInput, onDelta func(string)) (workflow.NodeOutput, error) {
	key := in.Node.ID + "__iter"
	iter := 0
	if v, ok := in.Vars[key].(float64); ok {
		iter = int(v)
	} else if v, ok := in.Vars[key].(int); ok {
		iter = v
	}
	max := d.MaxIterations
	if max <= 0 {
		max = 10
	}
	cond := workflow.EvalCondition(d.Condition, in.Vars)
	branch := "break"
	if cond && iter < max {
		branch = "continue"
		in.Vars[key] = iter + 1 // persist the counter in the bus for the next pass
	}
	if onDelta != nil {
		onDelta(fmt.Sprintf("iteration %d/%d → %s", iter, max, branch))
	}
	return workflow.NodeOutput{Text: in.Input, Branch: branch}, nil
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// handleEvalWorkflow: POST /api/workflows/{wid}/eval  { "cases":[{input,expect}] }
// streams {"type":"eval_case","payload":{...}} per case, then a final
// {"type":"eval_summary","payload":{...}}.
func (s *Server) handleEvalWorkflow(w http.ResponseWriter, r *http.Request) {
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf, err := st.Load(r.PathValue("wid"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var body struct {
		Cases []workflow.EvalCase `json:"cases"`
		Save  bool                `json:"save"` // persist Cases as the workflow's saved dataset
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cases := body.Cases
	if len(cases) == 0 {
		cases = wf.EvalDataset // fall back to the dataset saved with the workflow
	}
	if len(cases) == 0 {
		http.Error(w, "no eval cases provided and none saved with this workflow", http.StatusBadRequest)
		return
	}
	if body.Save {
		wf.EvalDataset = body.Cases
		if err := st.Save(wf); err != nil {
			http.Error(w, "couldn't save dataset: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	send := func(typ string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: {\"type\":%q,\"payload\":%s}\n\n", typ, b)
		flusher.Flush()
	}

	client := llm.NewClient(s.cfg)
	runner := s.workflowNodeRunner(client)
	summary := workflow.RunEvals(r.Context(), wf, cases, runner.run, client, s.cfg, func(cr workflow.EvalCaseResult) {
		send("eval_case", cr)
	})
	runner.Close()
	send("eval_summary", summary)
}

// buildAgentPrompt composes an agent node's prompt: its instructions act as the
// system prompt (passed separately), so the user turn is the upstream input. When
// the node delegates to a saved subagent persona, prefix that as a hint.
func buildAgentPrompt(d workflow.NodeData, input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		input = "Begin."
	}
	if a := strings.TrimSpace(d.Agent); a != "" {
		return "Acting as the \"" + a + "\" subagent.\n\n" + input
	}
	return input
}
