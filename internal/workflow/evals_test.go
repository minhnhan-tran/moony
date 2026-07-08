package workflow

import (
	"context"
	"strings"
	"testing"
)

// linearAgentWF is a Start→Agent→End graph for eval tests.
func linearAgentWF() *Workflow {
	return &Workflow{
		ID: "wfe", Nodes: []Node{
			{ID: "start", Type: NodeStart},
			{ID: "a", Type: NodeAgent, Data: NodeData{Label: "A"}},
			{ID: "end", Type: NodeEnd},
		},
		Edges: []Edge{{ID: "e1", Source: "start", Target: "a"}, {ID: "e2", Source: "a", Target: "end"}},
	}
}

func TestRunEvalsSmokeAndHeuristic(t *testing.T) {
	wf := linearAgentWF()
	cases := []EvalCase{
		{Input: "hello"},                  // smoke: passes on clean run
		{Input: "world", Expect: "WORLD"}, // heuristic: echoUpper → "WORLD" contains "WORLD"
		{Input: "foo", Expect: "NOPE"},    // heuristic: "FOO" doesn't contain "NOPE" → fail
	}
	// judgeClient nil → heuristic grading.
	sum := RunEvals(context.Background(), wf, cases, echoUpper, nil, nil, nil)
	if sum.Total != 3 {
		t.Fatalf("total = %d, want 3", sum.Total)
	}
	if sum.Passed != 2 {
		t.Fatalf("passed = %d, want 2 (smoke + WORLD match); cases=%+v", sum.Passed, sum.Cases)
	}
	// The failing case carries the actual output for debugging.
	var foo *EvalCaseResult
	for i := range sum.Cases {
		if sum.Cases[i].Input == "foo" {
			foo = &sum.Cases[i]
		}
	}
	if foo == nil || foo.Passed || foo.Output != "FOO" {
		t.Fatalf("foo case = %+v, want failed with output FOO", foo)
	}
}

func TestRunEvalsCapturesRunError(t *testing.T) {
	wf := linearAgentWF()
	boom := func(ctx context.Context, in NodeInput, onDelta func(string)) (NodeOutput, error) {
		if in.Node.ID == "a" {
			return NodeOutput{}, context.Canceled
		}
		return NodeOutput{Text: in.Input}, nil
	}
	sum := RunEvals(context.Background(), wf, []EvalCase{{Input: "x", Expect: "y"}}, boom, nil, nil, nil)
	if sum.Passed != 0 || len(sum.Cases) != 1 || sum.Cases[0].Error == "" {
		t.Fatalf("expected 1 failed case with an error, got %+v", sum)
	}
}

func TestRunEvalsEmitsProgress(t *testing.T) {
	wf := linearAgentWF()
	var seen int
	RunEvals(context.Background(), wf, []EvalCase{{Input: "a"}, {Input: "b"}}, echoUpper, nil, nil,
		func(EvalCaseResult) { seen++ })
	if seen != 2 {
		t.Fatalf("emitCase called %d times, want 2", seen)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                         `{"a":1}`,
		"prefix {\"a\":1} suffix":         `{"a":1}`,
		"```json\n{\"a\":{\"b\":2}}\n```": `{"a":{"b":2}}`,
		`{"s":"has } brace"}`:             `{"s":"has } brace"}`,
		"no json here":                    "",
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
	_ = strings.TrimSpace
}
