package agent

// run_workflow dispatches a saved Agent Builder workflow (a graph of agent/tool/
// skill/worker/logic nodes) as a single step from a chat turn — the @workflow
// analog of the `task`/`delegate` tools. The actual execution lives in the web
// layer (it owns the node executors), so it's injected here as a WorkflowRunFunc
// to avoid an import cycle (internal/web imports internal/agent, not vice versa).
// Node-level progress streams into the chat via the same worker-activity channel
// the delegate tool uses, so you see each node run live.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/tools"
)

// WorkflowRunFunc runs the saved workflow named (or id'd) by ref with the given
// input, calling onEvent for each node-level progress line (kind is
// "tool"|"info"|"text"), and returns the workflow's final output. The host (web)
// supplies this; runs invoked from a chat turn are unattended (any human-in-the
// -loop node auto-resolves, like evals), so it never blocks on a pause.
type WorkflowRunFunc func(ctx context.Context, ref, input string, onEvent func(kind, text string)) (string, error)

// WorkflowLister returns the names of the workflows available to run, for the
// tool description + schema enum. Cheap to call (used when advertising the tool).
type WorkflowLister func() []string

type workflowTool struct {
	run  WorkflowRunFunc
	list WorkflowLister
}

func (t *workflowTool) Name() string { return "run_workflow" }

func (t *workflowTool) Description() string {
	var b strings.Builder
	b.WriteString("Run a saved Agent Builder workflow (a multi-step graph of agent/tool/skill/worker/logic nodes) end to end, ")
	b.WriteString("passing it an input and getting back its final output. Use this to invoke a prepared pipeline rather than ")
	b.WriteString("re-deriving its steps. The workflow does not see this conversation, so put everything it needs in the input.")
	if names := t.names(); len(names) > 0 {
		b.WriteString(" Available workflows:\n")
		for _, n := range names {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return b.String()
}

func (t *workflowTool) names() []string {
	if t.list == nil {
		return nil
	}
	return t.list()
}

func (t *workflowTool) Schema() json.RawMessage {
	enum := ""
	if names := t.names(); len(names) > 0 {
		quoted := make([]string, 0, len(names))
		for _, n := range names {
			quoted = append(quoted, fmt.Sprintf("%q", n))
		}
		enum = `, "enum": [` + strings.Join(quoted, ", ") + `]`
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"workflow": {"type": "string", "description": "The workflow to run (its name or id)."` + enum + `},
			"input":    {"type": "string", "description": "The initial input for the workflow's Start node. It does not see your history, so include all needed context."}
		},
		"required": ["workflow"]
	}`)
}

// Mutates: a workflow can run agent/tool/worker nodes that change state, so it
// goes through the same approval gate as task/delegate.
func (t *workflowTool) Mutates() bool { return true }

func (t *workflowTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Workflow string `json:"workflow"`
		Input    string `json:"input"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid run_workflow input: %v", err)
	}
	ref := strings.TrimSpace(in.Workflow)
	if ref == "" {
		return "", fmt.Errorf("workflow is required")
	}
	if t.run == nil {
		return "", fmt.Errorf("workflows are not available in this session")
	}
	nodes := 0
	activity := workerActivityFrom(ctx) // may be nil (headless/tests)
	onEvent := func(kind, text string) {
		if kind == "tool" {
			nodes++
		}
		if activity != nil {
			activity(ref, kind, text)
		}
	}
	out, err := t.run(ctx, ref, in.Input, onEvent)
	if err != nil {
		return "", fmt.Errorf("workflow %q failed: %v", ref, err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(workflow returned no output)"
	}
	footer := ""
	if nodes > 0 {
		footer = fmt.Sprintf("\n\n(workflow ran %d node(s))", nodes)
	}
	return fmt.Sprintf("[workflow %s]\n%s%s", ref, out, footer), nil
}

// AttachWorkflows registers the run_workflow tool, backed by the host's runner +
// lister. A no-op when run is nil (nothing to execute). Safe to call repeatedly
// (re-registers with the latest funcs); call it whenever the workflow set may
// have changed so the tool's advertised list stays current.
func (a *Agent) AttachWorkflows(run WorkflowRunFunc, list WorkflowLister) {
	if run == nil {
		return
	}
	a.workflowNames = list
	a.tools.Register(&workflowTool{run: run, list: list})
}

var _ tools.Tool = (*workflowTool)(nil)
