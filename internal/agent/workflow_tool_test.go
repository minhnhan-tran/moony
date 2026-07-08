package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The run_workflow tool calls the injected runner with the parsed args and wraps
// its output; node progress streams via the worker-activity channel.
func TestWorkflowToolExecute(t *testing.T) {
	var gotRef, gotInput string
	run := func(ctx context.Context, ref, input string, onEvent func(kind, text string)) (string, error) {
		gotRef, gotInput = ref, input
		onEvent("tool", "node A")
		return "the result", nil
	}
	tool := &workflowTool{run: run, list: func() []string { return []string{"pipeline"} }}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"workflow":"pipeline","input":"go"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotRef != "pipeline" || gotInput != "go" {
		t.Errorf("runner got (%q,%q), want (pipeline,go)", gotRef, gotInput)
	}
	if !strings.Contains(out, "the result") {
		t.Errorf("output %q should contain the workflow result", out)
	}
}

func TestWorkflowToolValidates(t *testing.T) {
	tool := &workflowTool{run: func(context.Context, string, string, func(string, string)) (string, error) { return "x", nil }}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"input":"x"}`)); err == nil {
		t.Error("missing workflow should error")
	}
	// No runner injected → clear error, not a panic.
	none := &workflowTool{}
	if _, err := none.Execute(context.Background(), json.RawMessage(`{"workflow":"p"}`)); err == nil {
		t.Error("nil runner should error")
	}
}

func TestWorkflowToolSchemaEnumeratesNames(t *testing.T) {
	tool := &workflowTool{
		run:  func(context.Context, string, string, func(string, string)) (string, error) { return "", nil },
		list: func() []string { return []string{"alpha", "beta"} },
	}
	desc := tool.Description()
	if !strings.Contains(desc, "alpha") || !strings.Contains(desc, "beta") {
		t.Errorf("description should list workflow names, got:\n%s", desc)
	}
	if !strings.Contains(string(tool.Schema()), `"enum"`) {
		t.Error("schema should carry an enum of workflow names")
	}
}
