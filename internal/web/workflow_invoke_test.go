package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/workflow"
)

// A completed node's chat line carries model · tokens · cost · time; a node with
// no metrics (passthrough) adds nothing.
func TestNodeMetaSuffix(t *testing.T) {
	rich := workflow.RunEvent{
		Type:       "node_completed",
		Model:      "anthropic.claude-opus-4-8",
		Usage:      &workflow.NodeUsage{InputTokens: 1200, OutputTokens: 340, CostUSD: 0.0123},
		DurationMS: 1500,
	}
	got := nodeMetaSuffix(rich)
	for _, want := range []string{"1200→340 tok", "$0.0123", "1500ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("suffix %q missing %q", got, want)
		}
	}
	if !strings.Contains(got, "opus") { // shortModel trims the vendor prefix
		t.Errorf("suffix %q should name the model", got)
	}

	// Passthrough node (no model/usage/duration) → empty suffix.
	if s := nodeMetaSuffix(workflow.RunEvent{Type: "node_completed", Usage: &workflow.NodeUsage{}}); s != "" {
		t.Errorf("a metric-less node should add no suffix, got %q", s)
	}
}

// nodeTarget names the concrete agent/skill/worker/tool a node runs, for the chat
// progress line; passthrough/logic nodes have none.
func TestNodeTarget(t *testing.T) {
	cases := []struct {
		node workflow.Node
		want string
	}{
		{workflow.Node{Type: workflow.NodeAgent, Data: workflow.NodeData{Agent: "explore"}}, "↳ explore"},
		{workflow.Node{Type: workflow.NodeSkill, Data: workflow.NodeData{Skill: "pdf"}}, "✦ pdf"},
		{workflow.Node{Type: workflow.NodeWorker, Data: workflow.NodeData{Worker: "claude"}}, "⇲ claude"},
		{workflow.Node{Type: workflow.NodeTool, Data: workflow.NodeData{Tool: "read_file"}}, "read_file"},
		{workflow.Node{Type: workflow.NodeMCP, Data: workflow.NodeData{Tool: "mcp.firecrawl.scrape"}}, "mcp.firecrawl.scrape"},
		{workflow.Node{Type: workflow.NodeAgent}, ""},     // inline agent (no persona) → no target
		{workflow.Node{Type: workflow.NodeTransform}, ""}, // passthrough → none
		{workflow.Node{Type: workflow.NodeIfElse}, ""},    // logic → none
	}
	for _, c := range cases {
		if got := nodeTarget(c.node); got != c.want {
			t.Errorf("nodeTarget(%s/%+v) = %q, want %q", c.node.Type, c.node.Data, got, c.want)
		}
	}
}

// runSavedWorkflow must stream each node's actual model response to the chat as an
// "output" event (not just the ✓ status line), so the worker-row shows what each
// block produced. A transform node produces output with no LLM, so this is
// deterministic.
func TestRunSavedWorkflowEmitsNodeOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	st, err := workflow.Open()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	wf := &workflow.Workflow{
		ID:   "wf-out",
		Name: "Echoer",
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeStart},
			{ID: "t", Type: workflow.NodeTransform, Data: workflow.NodeData{Label: "Shout", Transform: "RESULT: {{input}}"}},
			{ID: "end", Type: workflow.NodeEnd},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "start", Target: "t"},
			{ID: "e2", Source: "t", Target: "end"},
		},
	}
	if err := st.Save(wf); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := &config.Config{Model: "test", WorkingDir: t.TempDir()}
	var outputs, tools []string
	final, err := runSavedWorkflow(context.Background(), cfg, "Echoer", "hello", func(kind, text string) {
		switch kind {
		case "output":
			outputs = append(outputs, text)
		case "tool":
			tools = append(tools, text)
		}
	})
	if err != nil {
		t.Fatalf("runSavedWorkflow: %v", err)
	}
	if !strings.Contains(final, "RESULT: hello") {
		t.Errorf("final output = %q, want it to contain the transform result", final)
	}
	// The transform node's response is surfaced as an "output" event, labeled.
	var sawResponse bool
	for _, o := range outputs {
		if strings.Contains(o, "RESULT: hello") && strings.Contains(o, "Shout") {
			sawResponse = true
		}
	}
	if !sawResponse {
		t.Errorf("expected an 'output' event with the node label + response, got %v", outputs)
	}
	if len(tools) == 0 {
		t.Error("expected at least one 'tool' (node_started) progress line")
	}

	// The run must be RECORDED in history (a chat-invoked workflow shows up in the
	// Agent Builder's run history with its output + trace), not just streamed.
	runStore, err := workflow.OpenRuns()
	if err != nil {
		t.Fatalf("open runs: %v", err)
	}
	runs, err := runStore.List(wf.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want exactly 1 recorded run for the workflow, got %d", len(runs))
	}
	r := runs[0]
	if r.Status != workflow.RunCompleted {
		t.Errorf("recorded run status = %q, want completed", r.Status)
	}
	if !strings.Contains(r.Output, "RESULT: hello") {
		t.Errorf("recorded run output = %q, want the transform result", r.Output)
	}
	if len(r.Trace) == 0 {
		t.Error("recorded run has no trace entries")
	}
}
