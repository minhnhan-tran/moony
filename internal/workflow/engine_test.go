package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// collectEvents runs wf with a fake node runner and returns the trace types + a
// map of node outputs, so tests can assert ordering and threading without an LLM.
func collectEvents(t *testing.T, wf *Workflow, input string, run NodeFunc) ([]RunEvent, error) {
	t.Helper()
	var evs []RunEvent
	err := Run(context.Background(), wf, input, run, func(e RunEvent) { evs = append(evs, e) }, nil)
	return evs, err
}

func types(evs []RunEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

// echoUpper is a trivial node runner: it uppercases the input (for agent-ish
// nodes) and passes through for start/end, so we can trace data flow.
func echoUpper(_ context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
	switch in.Node.Type {
	case NodeStart:
		return NodeOutput{Text: in.Input}, nil
	case NodeEnd:
		return NodeOutput{Text: in.Input}, nil
	default:
		up := strings.ToUpper(in.Input)
		if onDelta != nil {
			onDelta(up) // exercise the delta path
		}
		return NodeOutput{Text: up, Usage: NodeUsage{InputTokens: 1, OutputTokens: 1, CostUSD: 0.001}}, nil
	}
}

func TestRunLinearThreadsOutput(t *testing.T) {
	wf := &Workflow{
		ID: "wf1", Name: "linear",
		Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "a", Type: NodeAgent, Data: NodeData{Label: "Agent A"}},
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}, {ID: "e2", Source: "a", Target: "end"}},
	}
	evs, err := collectEvents(t, wf, "hello", echoUpper)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Join(types(evs), ",")
	// start → a (delta + completed) → end → workflow_completed
	for _, want := range []string{"workflow_started", "node_delta", "node_completed", "workflow_completed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("trace %q missing %q", got, want)
		}
	}
	// The End node's output should be the uppercased text threaded through "a".
	last := evs[len(evs)-1]
	if last.Type != "workflow_completed" || last.Output != "HELLO" {
		t.Fatalf("final event = %+v, want workflow_completed HELLO", last)
	}
	// node_completed for "a" should carry usage.
	var sawUsage bool
	for _, e := range evs {
		if e.Type == "node_completed" && e.NodeID == "a" {
			if e.Usage == nil || e.Usage.OutputTokens != 1 {
				t.Fatalf("node a usage = %+v", e.Usage)
			}
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatal("no node_completed for a")
	}
}

func TestRunNodeFailureStops(t *testing.T) {
	wf := &Workflow{
		ID: "wf2", Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "boom", Type: NodeAgent},
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "boom"}, {ID: "e2", Source: "boom", Target: "end"}},
	}
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		if in.Node.ID == "boom" {
			return NodeOutput{}, errors.New("kaboom")
		}
		return NodeOutput{Text: in.Input}, nil
	}
	evs, err := collectEvents(t, wf, "x", run)
	if err == nil {
		t.Fatal("expected error from failing node")
	}
	got := strings.Join(types(evs), ",")
	if !strings.Contains(got, "node_failed") || !strings.Contains(got, "workflow_failed") {
		t.Fatalf("trace %q should contain node_failed + workflow_failed", got)
	}
	if strings.Contains(got, "workflow_completed") {
		t.Fatalf("trace %q should not complete after a failure", got)
	}
}

func TestRunMissingStart(t *testing.T) {
	wf := &Workflow{ID: "wf3", Nodes: []Node{{ID: "a", Type: NodeAgent}}}
	if _, err := collectEvents(t, wf, "x", echoUpper); err == nil {
		t.Fatal("expected error when no start node")
	}
}

func TestRunCycleCapped(t *testing.T) {
	// start → a → a (self-loop) never reaches an end; the step cap must stop it.
	wf := &Workflow{
		ID: "wf4", Nodes: []Node{{ID: "start", Type: NodeStart}, {ID: "a", Type: NodeAgent}},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}, {ID: "e2", Source: "a", Target: "a"}},
	}
	_, err := collectEvents(t, wf, "x", echoUpper)
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("expected step-limit error, got %v", err)
	}
}

func TestRunNodeRetry(t *testing.T) {
	wf := &Workflow{
		ID: "wfr", Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "flaky", Type: NodeAgent, Data: NodeData{Retries: 2}}, // up to 3 attempts
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "flaky"}, {ID: "e2", Source: "flaky", Target: "end"}},
	}
	var attempts int
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		if in.Node.ID != "flaky" {
			return NodeOutput{Text: in.Input}, nil
		}
		attempts++
		if attempts < 3 { // fail twice, succeed on the 3rd
			return NodeOutput{}, errors.New("transient")
		}
		return NodeOutput{Text: "ok"}, nil
	}
	evs, err := collectEvents(t, wf, "go", run)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	var retries int
	for _, e := range evs {
		if e.Type == "node_retry" {
			retries++
		}
	}
	if retries != 2 {
		t.Fatalf("node_retry events = %d, want 2", retries)
	}
}

func TestRunNodeRetryExhausted(t *testing.T) {
	wf := &Workflow{
		ID: "wfr2", Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "bad", Type: NodeAgent, Data: NodeData{Retries: 1}}, // 2 attempts, both fail
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "bad"}, {ID: "e2", Source: "bad", Target: "end"}},
	}
	var attempts int
	run := func(_ context.Context, in NodeInput, _ func(string)) (NodeOutput, error) {
		if in.Node.ID == "bad" {
			attempts++
			return NodeOutput{}, errors.New("always fails")
		}
		return NodeOutput{Text: in.Input}, nil
	}
	if _, err := collectEvents(t, wf, "x", run); err == nil {
		t.Fatal("expected failure after retries exhausted")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (1 + 1 retry)", attempts)
	}
}

func TestRunDeadEndCompletes(t *testing.T) {
	// start → a with no further edge: a clean finish even without an End node.
	wf := &Workflow{
		ID: "wf5", Nodes: []Node{{ID: "start", Type: NodeStart}, {ID: "a", Type: NodeAgent}},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}},
	}
	evs, err := collectEvents(t, wf, "hi", echoUpper)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if last := evs[len(evs)-1]; last.Type != "workflow_completed" {
		t.Fatalf("expected workflow_completed at a dead end, got %+v", last)
	}
}

// branchRunner routes an if-else node down whichever branch its label names
// ("if"/"else"), so we can assert the engine follows the matching edge.
func branchRunner(branch string) NodeFunc {
	return func(_ context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
		if in.Node.Type == NodeIfElse {
			return NodeOutput{Text: in.Input, Branch: branch}, nil
		}
		return NodeOutput{Text: in.Node.ID}, nil // each node outputs its id, so we can see which ran
	}
}

func TestRunBranchTakesMatchingEdge(t *testing.T) {
	// start → gate, gate --if--> A, gate --else--> B. Routing "else" must reach B.
	wf := &Workflow{
		ID: "wfb", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "gate", Type: NodeIfElse},
			{ID: "A", Type: NodeAgent}, {ID: "B", Type: NodeAgent},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "gate"},
			{ID: "e2", Source: "gate", Target: "A", SourceHandle: "if"},
			{ID: "e3", Source: "gate", Target: "B", SourceHandle: "else"},
		},
	}
	for _, tc := range []struct{ branch, want string }{{"if", "A"}, {"else", "B"}} {
		evs, err := collectEvents(t, wf, "x", branchRunner(tc.branch))
		if err != nil {
			t.Fatalf("Run(%s): %v", tc.branch, err)
		}
		last := evs[len(evs)-1]
		if last.Output != tc.want {
			t.Errorf("branch %q reached %q, want %q", tc.branch, last.Output, tc.want)
		}
	}
}

// An unwired branch must NOT fall back to a different labeled edge (which for a
// while loop's missing "break" would loop forever); it ends the run instead.
func TestRunUnwiredBranchDoesNotMisroute(t *testing.T) {
	// gate has ONLY an "if" edge; routing "else" has no match and no unlabeled
	// fallback, so the run ends rather than wrongly taking the "if" edge to A.
	wf := &Workflow{
		ID: "wfb2", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "gate", Type: NodeIfElse}, {ID: "A", Type: NodeAgent},
		},
		Edges: []Edge{
			{ID: "e1", Source: "start", Target: "gate"},
			{ID: "e2", Source: "gate", Target: "A", SourceHandle: "if"},
		},
	}
	evs, err := collectEvents(t, wf, "x", branchRunner("else"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range evs {
		if e.Type == "node_started" && e.NodeID == "A" {
			t.Fatal("router took the wrong (if) branch when else was unwired")
		}
	}
	if last := evs[len(evs)-1]; last.Type != "workflow_completed" {
		t.Fatalf("expected a clean completion at the unwired branch, got %+v", last)
	}
}

// The variable bus exposes each node's output as {{nodeId}} and {{lastOutput}};
// confirm a downstream node's input is the upstream output (threading) and that
// per-node outputs are addressable in the bus.
func TestRunVariableBusThreadsByID(t *testing.T) {
	var seenInput string
	wf := &Workflow{
		ID: "wfv", Nodes: []Node{
			{ID: "start", Type: NodeStart}, {ID: "a", Type: NodeAgent}, {ID: "b", Type: NodeAgent},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}, {ID: "e2", Source: "a", Target: "b"}},
	}
	run := func(_ context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
		switch in.Node.ID {
		case "a":
			return NodeOutput{Text: "A-OUT"}, nil
		case "b":
			seenInput = in.Input // should be a's output, threaded via lastOutput
			if got := asString(in.Vars["a"]); got != "A-OUT" {
				t.Errorf("vars[a] = %q, want A-OUT (per-node output not addressable)", got)
			}
			return NodeOutput{Text: in.Input}, nil
		}
		return NodeOutput{Text: in.Input}, nil
	}
	if _, err := collectEvents(t, wf, "seed", run); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seenInput != "A-OUT" {
		t.Errorf("node b input = %q, want A-OUT (output not threaded)", seenInput)
	}
}
