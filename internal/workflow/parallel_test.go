package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fanWF builds a canonical fan-out/reduce graph:
//
//	start → fork(parallel) → [b0, b1, b2] → join → reduce → end
//
// Each branch and the reduce node are NodeAgent so the fake runner drives them.
func fanWF(joinMode string) *Workflow {
	return &Workflow{
		ID: "wffan", Name: "fan",
		Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "fork", Type: NodeParallel, Data: NodeData{Label: "Fan out"}},
			{ID: "b0", Type: NodeAgent},
			{ID: "b1", Type: NodeAgent},
			{ID: "b2", Type: NodeAgent},
			{ID: "join", Type: NodeJoin, Data: NodeData{JoinMode: joinMode}},
			{ID: "reduce", Type: NodeAgent},
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "b0"},
			{ID: "e3", Source: "fork", Target: "b1"},
			{ID: "e4", Source: "fork", Target: "b2"},
			{ID: "e5", Source: "b0", Target: "join"},
			{ID: "e6", Source: "b1", Target: "join"},
			{ID: "e7", Source: "b2", Target: "join"},
			{ID: "e8", Source: "join", Target: "reduce"},
			{ID: "e9", Source: "reduce", Target: "end"},
		},
	}
}

// fanRunner: each branch node outputs its own id; the join concatenates the
// branch inputs the engine hands it; reduce passes the join's text through. It
// records which branch nodes ran (concurrency-safe).
func fanRunner(ran *sync.Map) NodeFunc {
	return func(_ context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
		switch in.Node.Type {
		case NodeStart, NodeEnd:
			return NodeOutput{Text: in.Input}, nil
		case NodeJoin:
			return NodeOutput{Text: strings.Join(in.BranchInputs, "|")}, nil
		default:
			ran.Store(in.Node.ID, true)
			if in.Node.ID == "reduce" {
				return NodeOutput{Text: "R(" + in.Input + ")"}, nil
			}
			return NodeOutput{Text: in.Node.ID, Usage: NodeUsage{OutputTokens: 1, CostUSD: 0.001}}, nil
		}
	}
}

func TestRunParallelAllBranchesRun(t *testing.T) {
	var ran sync.Map
	evs, err := collectEvents(t, fanWF(""), "seed", fanRunner(&ran))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, id := range []string{"b0", "b1", "b2", "reduce"} {
		if _, ok := ran.Load(id); !ok {
			t.Errorf("branch/reduce node %q did not run", id)
		}
	}
	// The join feeds reduce the concatenation of all three branch outputs in
	// branch (declaration) order; reduce wraps it.
	last := evs[len(evs)-1]
	if last.Type != "workflow_completed" || last.Output != "R(b0|b1|b2)" {
		t.Fatalf("final = %+v, want workflow_completed R(b0|b1|b2)", last)
	}
}

func TestRunParallelEmitsGrouping(t *testing.T) {
	var ran sync.Map
	evs, err := collectEvents(t, fanWF(""), "seed", fanRunner(&ran))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The fork's node_started carries BranchCount; branch events carry GroupID.
	var sawForkStart, sawGroupedBranch bool
	for _, e := range evs {
		if e.Type == "node_started" && e.NodeID == "fork" {
			if e.BranchCount != 3 {
				t.Errorf("fork node_started BranchCount = %d, want 3", e.BranchCount)
			}
			sawForkStart = true
		}
		if e.NodeID == "b2" && e.GroupID == "fork" {
			sawGroupedBranch = true
		}
	}
	if !sawForkStart {
		t.Error("no node_started for the parallel (fork) node")
	}
	if !sawGroupedBranch {
		t.Error("branch node b2 did not carry GroupID=fork")
	}
	// Branch index is recorded on each branch's events.
	idxByNode := map[string]int{}
	for _, e := range evs {
		if e.GroupID == "fork" && (e.NodeID == "b0" || e.NodeID == "b1" || e.NodeID == "b2") && e.Type == "node_completed" {
			idxByNode[e.NodeID] = e.Branch
		}
	}
	if idxByNode["b0"] != 0 || idxByNode["b1"] != 1 || idxByNode["b2"] != 2 {
		t.Errorf("branch indices = %v, want b0:0 b1:1 b2:2", idxByNode)
	}
}

func TestRunParallelFailFastCancelsSiblings(t *testing.T) {
	// b1 fails immediately; under fail-fast the run fails and the slow siblings are
	// cancelled. We don't assert they didn't *start* (they may), only that the run
	// surfaces the failure and doesn't complete.
	wf := fanWF("")
	var started int32
	run := func(ctx context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		switch in.Node.Type {
		case NodeStart, NodeEnd:
			return NodeOutput{Text: in.Input}, nil
		case NodeJoin:
			return NodeOutput{Text: strings.Join(in.BranchInputs, "|")}, nil
		}
		if in.Node.ID == "b1" {
			return NodeOutput{}, errors.New("branch boom")
		}
		atomic.AddInt32(&started, 1)
		// Slow branches: block until cancelled so fail-fast has something to cancel.
		if in.Node.ID == "b0" || in.Node.ID == "b2" {
			<-ctx.Done()
			return NodeOutput{}, ctx.Err()
		}
		return NodeOutput{Text: in.Node.ID}, nil
	}
	evs, err := collectEvents(t, wf, "x", run)
	if err == nil {
		t.Fatal("expected the region to fail when a branch errors under fail-fast")
	}
	got := strings.Join(types(evs), ",")
	if !strings.Contains(got, "workflow_failed") {
		t.Fatalf("trace %q should contain workflow_failed", got)
	}
	if strings.Contains(got, "workflow_completed") {
		t.Fatalf("trace %q must not complete after a fail-fast branch error", got)
	}
	// reduce must never run.
	for _, e := range evs {
		if e.NodeID == "reduce" && e.Type == "node_completed" {
			t.Fatal("reduce ran despite a failed branch under fail-fast")
		}
	}
}

func TestRunParallelCollectModeRunsJoin(t *testing.T) {
	// With NoFailFast, a single branch error doesn't sink the run: the join sees
	// the successful branches and reduce still runs.
	wf := fanWF("")
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "fork" {
			wf.Nodes[i].Data.NoFailFast = true
		}
	}
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		switch in.Node.Type {
		case NodeStart, NodeEnd:
			return NodeOutput{Text: in.Input}, nil
		case NodeJoin:
			return NodeOutput{Text: strings.Join(in.BranchInputs, "|")}, nil
		}
		if in.Node.ID == "b1" {
			return NodeOutput{}, errors.New("branch boom")
		}
		if in.Node.ID == "reduce" {
			return NodeOutput{Text: "R(" + in.Input + ")"}, nil
		}
		return NodeOutput{Text: in.Node.ID}, nil
	}
	evs, err := collectEvents(t, wf, "x", run)
	if err != nil {
		t.Fatalf("collect mode should not fail the run: %v", err)
	}
	last := evs[len(evs)-1]
	// b1 dropped; join concatenates the two survivors in order.
	if last.Type != "workflow_completed" || last.Output != "R(b0|b2)" {
		t.Fatalf("final = %+v, want workflow_completed R(b0|b2)", last)
	}
}

// In collect mode, if EVERY branch fails the region must report the branch
// failures — not a misleading "no reachable join" structural error.
func TestRunParallelCollectModeAllFail(t *testing.T) {
	wf := fanWF("")
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "fork" {
			wf.Nodes[i].Data.NoFailFast = true
		}
	}
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		switch in.Node.Type {
		case NodeStart, NodeEnd:
			return NodeOutput{Text: in.Input}, nil
		case NodeJoin:
			return NodeOutput{Text: strings.Join(in.BranchInputs, "|")}, nil
		}
		if in.Node.ID == "reduce" {
			return NodeOutput{Text: in.Input}, nil
		}
		return NodeOutput{}, errors.New("branch boom") // every branch fails
	}
	_, err := collectEvents(t, wf, "x", run)
	if err == nil {
		t.Fatal("expected an error when all branches fail in collect mode")
	}
	if !strings.Contains(err.Error(), "all 3 branches") || strings.Contains(err.Error(), "no reachable join") {
		t.Fatalf("error should name the branch failures, got: %v", err)
	}
}

// A join reached directly on the top-level walk (a corrupt/old resume pointing at
// the join, bypassing the parallel) must fail loudly, not silently mis-combine
// with empty branch inputs.
func TestRunJoinDirectlyIsAnError(t *testing.T) {
	wf := &Workflow{
		ID: "jd", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "join", Type: NodeJoin}, {ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "join"}, {ID: "e2", Source: "join", Target: "end"}},
	}
	_, err := collectEvents(t, wf, "x", echoUpper)
	if err == nil || !strings.Contains(err.Error(), "cannot run join") {
		t.Fatalf("expected a 'cannot run join directly' error, got %v", err)
	}
}

func TestRunParallelExposesBranchVars(t *testing.T) {
	// After the region, each branch output is addressable as {{fork.bN}} in the
	// bus, and the reduce node can read them.
	wf := fanWF("")
	var seenB0, seenB2 string
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		switch in.Node.Type {
		case NodeStart, NodeEnd:
			return NodeOutput{Text: in.Input}, nil
		case NodeJoin:
			return NodeOutput{Text: strings.Join(in.BranchInputs, "|")}, nil
		}
		if in.Node.ID == "reduce" {
			seenB0 = asString(in.Vars["fork.b0"])
			seenB2 = asString(in.Vars["fork.b2"])
			return NodeOutput{Text: in.Input}, nil
		}
		return NodeOutput{Text: in.Node.ID}, nil
	}
	if _, err := collectEvents(t, wf, "x", run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seenB0 != "b0" || seenB2 != "b2" {
		t.Errorf("branch vars = (%q,%q), want (b0,b2)", seenB0, seenB2)
	}
}

func TestRunParallelSharedStepCap(t *testing.T) {
	// A branch that loops forever must trip the SHARED step cap (not run unbounded
	// just because it's inside a region).
	wf := &Workflow{
		ID: "wfloop", Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "fork", Type: NodeParallel},
			{ID: "b0", Type: NodeAgent},
			{ID: "spin", Type: NodeAgent},
			{ID: "join", Type: NodeJoin},
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "b0"},
			{ID: "e3", Source: "fork", Target: "spin"},
			{ID: "e4", Source: "b0", Target: "join"},
			{ID: "e5", Source: "spin", Target: "spin"}, // self-loop: never reaches the join
			{ID: "e6", Source: "join", Target: "end"},
		},
	}
	_, err := collectEvents(t, wf, "x", echoUpper)
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("expected a step-limit error from the looping branch, got %v", err)
	}
}

func TestRunJoinModes(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"concat", "b0\n\n---\n\nb1\n\n---\n\nb2"},
		{"first", "b0"},
		{"array", `["b0","b1","b2"]`},
	} {
		// Use the real join executor via a runner that mimics workflow_run's
		// combineBranches for the join, so we test the engine's BranchInputs wiring.
		run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
			switch in.Node.Type {
			case NodeStart, NodeEnd:
				return NodeOutput{Text: in.Input}, nil
			case NodeJoin:
				return NodeOutput{Text: combineForTest(in.Node.Data, in.BranchInputs)}, nil
			}
			return NodeOutput{Text: in.Node.ID}, nil
		}
		wf := fanWF(tc.mode)
		// Drop reduce/end so the join's output is the final output.
		wf.Edges = wf.Edges[:len(wf.Edges)-2] // remove join→reduce, reduce→end
		evs, err := collectEvents(t, wf, "x", run)
		if err != nil {
			t.Fatalf("mode %s: %v", tc.mode, err)
		}
		if last := evs[len(evs)-1]; last.Output != tc.want {
			t.Errorf("join mode %q output = %q, want %q", tc.mode, last.Output, tc.want)
		}
	}
}

// combineForTest mirrors web.combineBranches (kept here so the engine package is
// self-contained for the join-mode assertions).
func combineForTest(d NodeData, branches []string) string {
	switch d.JoinMode {
	case "first":
		if len(branches) > 0 {
			return branches[0]
		}
		return ""
	case "array":
		var b strings.Builder
		b.WriteByte('[')
		for i, s := range branches {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('"')
			b.WriteString(s)
			b.WriteByte('"')
		}
		b.WriteByte(']')
		return b.String()
	default:
		return strings.Join(branches, "\n\n---\n\n")
	}
}

// --- Validate ---------------------------------------------------------------

func TestValidateNoParallelIsNoop(t *testing.T) {
	wf := &Workflow{
		ID: "lin", Nodes: []Node{{ID: "start", Type: NodeStart}, {ID: "a", Type: NodeAgent}, {ID: "end", Type: NodeEnd}},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}, {ID: "e2", Source: "a", Target: "end"}},
	}
	if err := Validate(wf); err != nil {
		t.Fatalf("a graph with no parallel must validate, got %v", err)
	}
}

func TestValidateWellFormedRegion(t *testing.T) {
	if err := Validate(fanWF("")); err != nil {
		t.Fatalf("the canonical fan-out/reduce graph should validate, got %v", err)
	}
}

func TestValidateRejectsOneBranch(t *testing.T) {
	wf := &Workflow{
		ID: "one", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "fork", Type: NodeParallel},
			{ID: "b0", Type: NodeAgent}, {ID: "join", Type: NodeJoin}, {ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "b0"},
			{ID: "e3", Source: "b0", Target: "join"},
			{ID: "e4", Source: "join", Target: "end"},
		},
	}
	if err := Validate(wf); err == nil || !strings.Contains(err.Error(), "at least 2 branches") {
		t.Fatalf("expected a <2-branches error, got %v", err)
	}
}

func TestValidateRejectsNoJoin(t *testing.T) {
	wf := &Workflow{
		ID: "nojoin", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "fork", Type: NodeParallel},
			{ID: "b0", Type: NodeAgent}, {ID: "b1", Type: NodeAgent}, {ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "b0"},
			{ID: "e3", Source: "fork", Target: "b1"},
			{ID: "e4", Source: "b0", Target: "end"},
			{ID: "e5", Source: "b1", Target: "end"},
		},
	}
	if err := Validate(wf); err == nil || !strings.Contains(err.Error(), "reconverge") {
		t.Fatalf("expected a no-join error, got %v", err)
	}
}

func TestValidateRejectsDifferentJoins(t *testing.T) {
	// Two branches reconverge on two different joins.
	wf := &Workflow{
		ID: "twojoins", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "fork", Type: NodeParallel},
			{ID: "b0", Type: NodeAgent}, {ID: "b1", Type: NodeAgent},
			{ID: "j0", Type: NodeJoin}, {ID: "j1", Type: NodeJoin}, {ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "fork"},
			{ID: "e2", Source: "fork", Target: "b0"},
			{ID: "e3", Source: "fork", Target: "b1"},
			{ID: "e4", Source: "b0", Target: "j0"},
			{ID: "e5", Source: "b1", Target: "j1"},
			{ID: "e6", Source: "j0", Target: "end"},
			{ID: "e7", Source: "j1", Target: "end"},
		},
	}
	if err := Validate(wf); err == nil || !strings.Contains(err.Error(), "different joins") {
		t.Fatalf("expected a different-joins error, got %v", err)
	}
}

func TestValidateRejectsMultiOutJoin(t *testing.T) {
	wf := fanWF("")
	// Add a second outgoing edge from the join.
	wf.Nodes = append(wf.Nodes, Node{ID: "extra", Type: NodeAgent})
	wf.Edges = append(wf.Edges, Edge{ID: "x", Source: "join", Target: "extra"})
	if err := Validate(wf); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("expected a join-fan-out error, got %v", err)
	}
}

func TestValidateRejectsOrphanJoin(t *testing.T) {
	wf := &Workflow{
		ID: "orphan", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "a", Type: NodeAgent},
			{ID: "join", Type: NodeJoin}, {ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "a"},
			{ID: "e2", Source: "a", Target: "join"},
			{ID: "e3", Source: "join", Target: "end"},
		},
	}
	if err := Validate(wf); err == nil || !strings.Contains(err.Error(), "not the convergence") {
		t.Fatalf("expected an orphan-join error, got %v", err)
	}
}

// Every built-in template must be structurally valid (so the parallel-reduce
// starter — and any future region template — can't ship malformed).
func TestBuiltinTemplatesValidate(t *testing.T) {
	for _, tpl := range Templates() {
		wf := &Workflow{ID: tpl.ID, Name: tpl.Name, Nodes: tpl.Nodes, Edges: tpl.Edges}
		if err := Validate(wf); err != nil {
			t.Errorf("template %q does not validate: %v", tpl.ID, err)
		}
	}
}
