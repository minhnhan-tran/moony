package workflow

// Workflow execution engine (Agent Builder Phase 2: Preview/Run). It walks the
// graph from the Start node, threads a variable bus between nodes, enforces a
// step cap (cycle guard) and context cancellation, and emits a per-node trace via
// a callback. The engine is deliberately independent of the agent/LLM runtime:
// each node is executed by an injected NodeFunc, so it's fully testable with a
// fake runner and the agent wiring lives in the web layer.
//
// Phase 2 routing is linear: from each node the engine follows its first
// outgoing edge (skipping note nodes). Multi-output branching for if-else/while
// is a later phase; the NodeOutput.Branch field is already carried so the router
// can honor it then.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultMaxSteps bounds how many nodes a single run may execute — a guard
// against cycles (an edge loop) running forever. Generous for real graphs. The
// cap is shared across the top-level walk AND all parallel branches (one atomic
// counter), so a fan-out can't multiply past it.
const defaultMaxSteps = 200

// maxNodeRetries caps a node's configured retry count, so a typo can't make one
// node hammer the model hundreds of times.
const maxNodeRetries = 5

// maxParallel caps how many branches of one parallel region run concurrently;
// excess branches queue on a semaphore and start as slots free.
const maxParallel = 8

// NodeInput is what the engine hands a node executor.
type NodeInput struct {
	Node  Node           // the node being executed
	Input string         // the upstream node's output text (the initial input at Start)
	Vars  map[string]any // the variable bus (input, lastOutput, per-node outputs)

	// BranchInputs is set only for a join node: each (successful) branch's output
	// text, in branch order, so the join can merge them.
	BranchInputs []string
	// InBranch is true while executing a node INSIDE a parallel branch. Executors
	// use it to disable human-in-the-loop pauses there (concurrent pauses don't
	// compose with single-valued resume in v1), and to skip per-node bus snapshots.
	InBranch bool
}

// NodeUsage is the optional token/cost accounting a node may report (agent nodes).
type NodeUsage struct {
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUSD,omitempty"`
}

// NodeOutput is what a node executor returns.
type NodeOutput struct {
	Text   string    // the node's output, threaded to the next node as its Input
	Usage  NodeUsage // optional accounting
	Model  string    // the model that ran this node (agent/skill/worker), for the trace
	Branch string    // optional source-handle to route on (if-else/while); "" = default/first edge
}

// NodeFunc executes a single node. onDelta, when non-nil, receives streamed text
// chunks (for live preview). Returning an error fails the run at that node.
type NodeFunc func(ctx context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error)

// RunEvent is one entry in the execution trace, delivered to the emit callback.
type RunEvent struct {
	Type        string     `json:"type"` // workflow_started|node_started|node_delta|node_retry|node_completed|node_failed|workflow_paused|workflow_completed|workflow_failed
	NodeID      string     `json:"nodeId,omitempty"`
	NodeType    string     `json:"nodeType,omitempty"`
	Label       string     `json:"label,omitempty"`
	Delta       string     `json:"delta,omitempty"`  // node_delta: a streamed text chunk
	Output      string     `json:"output,omitempty"` // node_completed: the node's full output
	Model       string     `json:"model,omitempty"`  // node_completed: the model that ran this node (agent/skill/worker)
	Error       string     `json:"error,omitempty"`
	Usage       *NodeUsage `json:"usage,omitempty"`
	DurationMS  int64      `json:"durationMs,omitempty"`
	Attempt     int        `json:"attempt,omitempty"`     // node_retry: which attempt just failed (1-based)
	MaxAttempts int        `json:"maxAttempts,omitempty"` // node_retry: total attempts allowed
	Prompt      string     `json:"prompt,omitempty"`      // workflow_paused: the approval/question/input message

	// Parallel-region grouping, so a streamed trace can nest concurrent rows.
	// GroupID is the parallel node's id, set on every event emitted inside its
	// region (branch nodes + the join's own events carry it). Branch is the 0-based
	// branch index a node ran under. BranchCount is set on the parallel node's
	// node_started: how many branches it fans out to.
	GroupID     string `json:"groupId,omitempty"`
	Branch      int    `json:"branch,omitempty"`
	BranchCount int    `json:"branchCount,omitempty"`

	// workflow_paused detail. PauseID uniquely identifies this pause within the run
	// (a run can pause more than once); the resume request echoes it back. PauseMode
	// is "approval" (Approve/Reject), "question" (pick an option), or "input" (free
	// text). PauseTool/PauseDiff describe an agent's mid-run tool-approval; PauseOptions
	// + PauseMultiple describe a question.
	PauseID       string        `json:"pauseId,omitempty"`
	PauseMode     string        `json:"pauseMode,omitempty"`
	PauseTool     string        `json:"pauseTool,omitempty"`
	PauseDiff     string        `json:"pauseDiff,omitempty"`
	PauseOptions  []PauseOption `json:"pauseOptions,omitempty"`
	PauseMultiple bool          `json:"pauseMultiple,omitempty"`
}

// PauseOption is one selectable choice in a question-mode pause.
type PauseOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Clock returns the current time in unix milliseconds. Injected so the engine
// stays deterministic in tests (scripts can't call time.Now in some contexts,
// and tests want fixed durations). Pass nil to Run for a zero clock.
type Clock func() int64

// Run executes wf from its Start node, calling run for each node and emit for
// each trace event. input seeds the variable bus (vars["input"]). It returns nil
// on a clean finish (reaching an End node or a dead end) and an error if no Start
// node exists, the step cap is hit, the context is cancelled, or a node fails.
func Run(ctx context.Context, wf *Workflow, input string, run NodeFunc, emit func(RunEvent), now Clock) error {
	return RunWithOptions(ctx, wf, RunOptions{Input: input}, run, emit, now)
}

// RunOptions configures a run, including resume: a non-empty StartNodeID begins
// at that node instead of Start, and Vars (when set) pre-seeds the variable bus
// (the saved bus from an earlier paused/failed run), so a durable run can pick up
// where it left off.
type RunOptions struct {
	Input       string
	Vars        map[string]any // pre-seeded bus; nil → {input, lastOutput:input}
	StartNodeID string         // resume from this node; "" → the Start node
}

// RunWithOptions is Run with resume support.
func RunWithOptions(ctx context.Context, wf *Workflow, opts RunOptions, run NodeFunc, emit func(RunEvent), now Clock) error {
	if now == nil {
		now = func() int64 { return 0 }
	}
	if emit == nil {
		emit = func(RunEvent) {}
	}
	// Serialize emit: parallel regions call it from concurrent branch goroutines,
	// so wrap the caller's sink in a mutex once here. This makes emit safe for ANY
	// downstream sink (SSE writer, run recorder) without each having to lock.
	{
		var emitMu sync.Mutex
		inner := emit
		emit = func(e RunEvent) {
			emitMu.Lock()
			defer emitMu.Unlock()
			inner(e)
		}
	}

	byID := make(map[string]*Node, len(wf.Nodes))
	for i := range wf.Nodes {
		byID[wf.Nodes[i].ID] = &wf.Nodes[i]
	}
	// Outgoing edges per source, in declaration order.
	out := make(map[string][]Edge)
	for _, e := range wf.Edges {
		out[e.Source] = append(out[e.Source], e)
	}

	// Pick the starting node: an explicit resume target, else the Start node.
	var cur *Node
	if opts.StartNodeID != "" {
		cur = byID[opts.StartNodeID]
		if cur == nil {
			return fmt.Errorf("resume node %q not found", opts.StartNodeID)
		}
	} else {
		cur = findStart(wf)
		if cur == nil {
			return errors.New("workflow has no start node")
		}
	}

	emit(RunEvent{Type: "workflow_started"})

	// Seed the variable bus: a resumed run carries its saved bus; a fresh run
	// starts from the input.
	vars := opts.Vars
	if vars == nil {
		vars = map[string]any{"input": opts.Input, "lastOutput": opts.Input}
	}
	// steps is the shared execution counter (cycle guard), incremented per node
	// across the top-level walk AND every parallel branch, so a fan-out can't
	// multiply node executions past the cap.
	var steps int32
	for {
		if err := ctx.Err(); err != nil {
			emit(RunEvent{Type: "workflow_failed", Error: "cancelled"})
			return err
		}
		if int(atomic.AddInt32(&steps, 1)) > defaultMaxSteps {
			emit(RunEvent{Type: "workflow_failed", Error: "step limit reached (possible cycle)"})
			return fmt.Errorf("workflow exceeded %d steps (possible cycle)", defaultMaxSteps)
		}

		// A join is only ever executed by runRegion (after its barrier). Reaching one
		// on the top-level walk means we started/resumed at it directly — which would
		// run the barrier's combine with no branch inputs and silently mis-merge. Fail
		// loudly instead: a region must be resumed from its parallel node (atomic).
		if cur.Type == NodeJoin {
			err := fmt.Errorf("cannot run join node %q directly; resume a parallel region from its parallel node", cur.ID)
			emit(RunEvent{Type: "workflow_failed", NodeID: cur.ID, Error: err.Error()})
			return err
		}

		// Parallel region: fan out to concurrent branches, reconverge at the join,
		// and continue from the join. The region is one logical step at this level.
		if cur.Type == NodeParallel {
			join, res, err := runRegion(ctx, cur, vars, out, byID, run, emit, now, &steps)
			if err != nil {
				emit(RunEvent{Type: "workflow_failed", NodeID: cur.ID, Error: err.Error()})
				return err
			}
			vars["lastOutput"] = res.Text
			vars[cur.ID] = res.Text
			vars[join.ID] = res.Text
			next := nextNode(out[join.ID], "", byID)
			if next == nil {
				emit(RunEvent{Type: "workflow_completed", Output: res.Text})
				return nil
			}
			cur = next
			continue
		}

		in := NodeInput{Node: *cur, Input: asString(vars["lastOutput"]), Vars: vars}
		res, err := execNode(ctx, in, run, emit, now, "", 0)
		if err != nil {
			emit(RunEvent{Type: "workflow_failed", NodeID: cur.ID, Error: err.Error()})
			return err
		}

		// Thread the output into the variable bus.
		vars["lastOutput"] = res.Text
		vars[cur.ID] = res.Text

		if cur.Type == NodeEnd {
			emit(RunEvent{Type: "workflow_completed", Output: res.Text})
			return nil
		}

		next := nextNode(out[cur.ID], res.Branch, byID)
		if next == nil {
			// Dead end (no further edge) — a clean finish even without an End node.
			emit(RunEvent{Type: "workflow_completed", Output: res.Text})
			return nil
		}
		cur = next
	}
}

// execNode runs a single node: emits node_started, runs the executor with the
// node's bounded retry, and emits node_completed (success) or node_failed
// (failure). It returns the executor's output and error WITHOUT threading the
// variable bus or following edges — the caller routes. grp/branch tag every
// emitted event for parallel-region grouping ("" / 0 at the top level). A failure
// caused purely by context cancellation (e.g. a sibling branch failed under
// fail-fast) is returned silently — no node_failed — so the trace isn't littered
// with "cancelled" rows for branches that were merely aborted.
func execNode(ctx context.Context, in NodeInput, run NodeFunc, emit func(RunEvent), now Clock, grp string, branch int) (NodeOutput, error) {
	node := in.Node
	label := node.Data.Label
	if label == "" {
		label = node.Type
	}
	emit(RunEvent{Type: "node_started", NodeID: node.ID, NodeType: node.Type, Label: label, GroupID: grp, Branch: branch})

	started := now()
	onDelta := func(d string) {
		emit(RunEvent{Type: "node_delta", NodeID: node.ID, Delta: d, GroupID: grp, Branch: branch})
	}

	retries := node.Data.Retries
	if retries < 0 {
		retries = 0
	} else if retries > maxNodeRetries {
		retries = maxNodeRetries
	}
	var res NodeOutput
	var err error
	for attempt := 0; ; attempt++ {
		res, err = run(ctx, in, onDelta)
		if err == nil || attempt >= retries || ctx.Err() != nil {
			break
		}
		emit(RunEvent{Type: "node_retry", NodeID: node.ID, NodeType: node.Type, Label: label, Error: err.Error(), Attempt: attempt + 1, MaxAttempts: retries + 1, GroupID: grp, Branch: branch})
	}
	dur := now() - started

	if err != nil {
		if ctx.Err() != nil {
			return NodeOutput{}, err // cancelled (e.g. sibling fail-fast) — stay quiet
		}
		emit(RunEvent{Type: "node_failed", NodeID: node.ID, NodeType: node.Type, Label: label, Error: err.Error(), DurationMS: dur, GroupID: grp, Branch: branch})
		return NodeOutput{}, fmt.Errorf("node %q (%s) failed: %w", node.ID, node.Type, err)
	}
	usage := res.Usage
	emit(RunEvent{Type: "node_completed", NodeID: node.ID, NodeType: node.Type, Label: label, Output: res.Text, Usage: &usage, Model: res.Model, DurationMS: dur, GroupID: grp, Branch: branch})
	return res, nil
}

// runRegion executes a parallel node's region: it runs each branch concurrently
// (capped by maxParallel), waits for all to finish (the barrier), merges their
// variable buses + outputs, and runs the single join node once. It emits a
// node_started for the parallel node (carrying BranchCount, opening a trace group)
// and a node_completed for it after the join, and returns the join node and its
// output so the caller can route onward. On a branch failure with fail-fast (the
// default), sibling branches are cancelled and the error is returned.
func runRegion(ctx context.Context, p *Node, vars map[string]any, out map[string][]Edge, byID map[string]*Node, run NodeFunc, emit func(RunEvent), now Clock, steps *int32) (*Node, NodeOutput, error) {
	label := nodeLabel(p)

	// Branch entry nodes = targets of the parallel node's out-edges (skip notes),
	// in declaration order.
	var entries []*Node
	for _, e := range out[p.ID] {
		tgt := byID[e.Target]
		if tgt == nil || tgt.Type == NodeNote {
			continue
		}
		entries = append(entries, tgt)
	}
	if len(entries) < 2 {
		err := fmt.Errorf("parallel node %q needs at least 2 branches", p.ID)
		emit(RunEvent{Type: "node_failed", NodeID: p.ID, NodeType: p.Type, Label: label, Error: err.Error()})
		return nil, NodeOutput{}, err
	}

	emit(RunEvent{Type: "node_started", NodeID: p.ID, NodeType: p.Type, Label: label, BranchCount: len(entries)})
	started := now()

	failFast := !p.Data.NoFailFast
	sem := make(chan struct{}, maxParallel)
	results := make([]string, len(entries))
	joinIDs := make([]string, len(entries))
	branchVars := make([]map[string]any, len(entries))
	errs := make([]error, len(entries))

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if rctx.Err() != nil {
				errs[i] = rctx.Err()
				return
			}
			bv := copyVars(vars)
			joinID, txt, err := walkBranch(rctx, entries[i], bv, out, byID, run, emit, now, steps, p.ID, i)
			branchVars[i] = bv
			results[i] = txt
			joinIDs[i] = joinID
			errs[i] = err
			if err != nil && failFast {
				cancel() // stop the siblings
			}
		}(i)
	}
	wg.Wait()

	// Under fail-fast, the first genuine branch error fails the whole region (the
	// failing node already emitted node_failed; cancelled siblings stayed quiet).
	if failFast {
		for _, e := range errs {
			if e != nil && !errors.Is(e, context.Canceled) {
				return nil, NodeOutput{}, e
			}
		}
		if err := ctx.Err(); err != nil { // the parent run itself was cancelled
			return nil, NodeOutput{}, err
		}
	}

	// Count survivors. In collect mode (NoFailFast) individual branch errors don't
	// fail the region, but if EVERY branch failed there's nothing to join — report
	// that as the real cause (the branch errors), not as a structural "no join"
	// error below (the graph is well-formed; the branches died).
	survivors := 0
	for _, e := range errs {
		if e == nil {
			survivors++
		}
	}
	if survivors == 0 {
		var msgs []string
		for i, e := range errs {
			if e != nil && !errors.Is(e, context.Canceled) {
				msgs = append(msgs, fmt.Sprintf("branch %d: %v", i, e))
			}
		}
		err := fmt.Errorf("all %d branches of parallel %q failed: %s", len(entries), p.ID, strings.Join(msgs, "; "))
		emit(RunEvent{Type: "node_failed", NodeID: p.ID, NodeType: p.Type, Label: label, Error: err.Error()})
		return nil, NodeOutput{}, err
	}

	// Identify the join: every successful branch must converge on the same join id.
	joinID := ""
	for i, j := range joinIDs {
		if errs[i] != nil || j == "" {
			continue
		}
		if joinID == "" {
			joinID = j
		} else if j != joinID {
			err := fmt.Errorf("parallel region from %q has branches converging on different joins (%q vs %q)", p.ID, joinID, j)
			emit(RunEvent{Type: "node_failed", NodeID: p.ID, NodeType: p.Type, Label: label, Error: err.Error()})
			return nil, NodeOutput{}, err
		}
	}
	if joinID == "" {
		// Validation guarantees a structural join exists, so reaching here means
		// every branch took an internal route (if-else/while) that escaped the
		// region without reaching the join — a runtime-routing dead-end, not a
		// missing node.
		err := fmt.Errorf("no branch of parallel %q reached its join (every branch routed to a dead-end)", p.ID)
		emit(RunEvent{Type: "node_failed", NodeID: p.ID, NodeType: p.Type, Label: label, Error: err.Error()})
		return nil, NodeOutput{}, err
	}
	join := byID[joinID]

	// Merge each successful branch's variable bus back into the parent, in branch
	// order (deterministic collision resolution), and expose each branch output as
	// "{parallelId}.b{i}". Collect the successful outputs for the join, in order.
	var branchInputs []string
	for i := range entries {
		if errs[i] != nil {
			continue
		}
		mergeVars(vars, branchVars[i])
		vars[fmt.Sprintf("%s.b%d", p.ID, i)] = results[i]
		branchInputs = append(branchInputs, results[i])
	}

	// Run the join once, with all branch outputs available. It's emitted
	// un-grouped (grp="") so the trace renders it as a flat row after the branch
	// group — it's the convergence, not part of any single branch.
	joinIn := NodeInput{Node: *join, Input: firstOr(branchInputs, ""), Vars: vars, BranchInputs: branchInputs}
	res, err := execNode(ctx, joinIn, run, emit, now, "", 0)
	if err != nil {
		return nil, NodeOutput{}, err
	}

	// Close the region: a node_completed for the parallel node itself, which the
	// trace UI uses to close the branch group and mark the canvas node done (it
	// carries NodeType "parallel", so the UI knows not to render it as a row).
	usage := res.Usage
	emit(RunEvent{Type: "node_completed", NodeID: p.ID, NodeType: p.Type, Label: label, Output: res.Text, Usage: &usage, DurationMS: now() - started})
	return join, res, nil
}

// walkBranch runs nodes from entry, following edges with a branch-local variable
// bus, until it reaches the region's join node (returned WITHOUT executing — the
// region runs the join once after the barrier) or an end/dead-end. It returns the
// join node id it stopped at (or "" if it ended before a join), the last output,
// and an error. A nested parallel inside a branch recurses through runRegion.
func walkBranch(ctx context.Context, entry *Node, bvars map[string]any, out map[string][]Edge, byID map[string]*Node, run NodeFunc, emit func(RunEvent), now Clock, steps *int32, grp string, branch int) (string, string, error) {
	cur := entry
	last := asString(bvars["lastOutput"])
	for {
		if err := ctx.Err(); err != nil {
			return "", last, err
		}
		if int(atomic.AddInt32(steps, 1)) > defaultMaxSteps {
			return "", last, fmt.Errorf("step limit reached (possible cycle)")
		}
		if cur.Type == NodeJoin {
			return cur.ID, last, nil // stop at the join; the region runs it
		}
		if cur.Type == NodeParallel { // nested region
			join, res, err := runRegion(ctx, cur, bvars, out, byID, run, emit, now, steps)
			if err != nil {
				return "", last, err
			}
			bvars["lastOutput"] = res.Text
			bvars[cur.ID] = res.Text
			bvars[join.ID] = res.Text
			last = res.Text
			next := nextNode(out[join.ID], "", byID)
			if next == nil {
				return "", last, nil
			}
			cur = next
			continue
		}
		in := NodeInput{Node: *cur, Input: asString(bvars["lastOutput"]), Vars: bvars, InBranch: true}
		res, err := execNode(ctx, in, run, emit, now, grp, branch)
		if err != nil {
			return "", last, err
		}
		bvars["lastOutput"] = res.Text
		bvars[cur.ID] = res.Text
		last = res.Text
		if cur.Type == NodeEnd {
			return "", last, nil
		}
		next := nextNode(out[cur.ID], res.Branch, byID)
		if next == nil {
			return "", last, nil
		}
		cur = next
	}
}

// copyVars returns a shallow copy of a variable bus (for forking into a branch).
func copyVars(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src)+4)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// mergeVars copies all keys from src into dst (later callers overwrite earlier on
// collision), used to fold a finished branch's bus back into the parent.
func mergeVars(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// nodeLabel returns a node's display label, falling back to its type.
func nodeLabel(n *Node) string {
	if n.Data.Label != "" {
		return n.Data.Label
	}
	return n.Type
}

// firstOr returns the first element of s, or def if s is empty.
func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// findStart returns the Start node (preferring an explicit type "start"), or nil.
func findStart(wf *Workflow) *Node {
	for i := range wf.Nodes {
		if wf.Nodes[i].Type == NodeStart {
			return &wf.Nodes[i]
		}
	}
	return nil
}

// nextNode picks the next node to run from a node's outgoing edges, skipping
// edges to note nodes. When branch is set (if-else "if"/"else", while
// "continue"/"break"), it takes the edge whose SourceHandle matches that branch.
// The fallback is ONLY an unlabeled edge (SourceHandle == "") — never a different
// labeled branch — so e.g. a while node that routes "break" but whose "break"
// port is unwired ends the run rather than wrongly looping down "continue".
func nextNode(edges []Edge, branch string, byID map[string]*Node) *Node {
	var fallback *Node
	for _, e := range edges {
		tgt := byID[e.Target]
		if tgt == nil || tgt.Type == NodeNote {
			continue
		}
		if branch != "" && e.SourceHandle == branch {
			return tgt // exact branch match wins
		}
		// Only an unlabeled edge is a safe fallback; a different labeled branch
		// (e.g. "continue" when we asked for "break") must not be taken.
		if fallback == nil && e.SourceHandle == "" {
			fallback = tgt
		}
	}
	return fallback
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
