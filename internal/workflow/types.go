// Package workflow defines the schema and persistent store for the visual
// agent-workflow graphs built in the web UI's Agent Builder. A Workflow is a
// graph of typed Nodes connected by Edges, persisted as one JSON file each.
//
// This is Phase 1 (the builder): it models and stores graph definitions only.
// Compiling a graph and executing it through the agent runtime — node executors,
// conditional routing, bounded loops, human-in-the-loop pauses — is a later
// phase that will live alongside these types.
package workflow

// Node type identifiers. These classify a node for the editor; execution
// semantics arrive in a later phase.
const (
	NodeStart      = "start"
	NodeAgent      = "agent"
	NodeTool       = "tool"
	NodeMCP        = "mcp"
	NodeIfElse     = "if-else"
	NodeWhile      = "while"
	NodeApproval   = "user-approval"
	NodeGuardrails = "guardrails"
	NodeTransform  = "transform"
	NodeWorker     = "worker"
	NodeSkill      = "skill"
	NodeEnd        = "end"
	NodeNote       = "note"
	NodeUserInput  = "user-input"
	NodeParallel   = "parallel" // fan-out: runs its branches concurrently, reconverging at a join
	NodeJoin       = "join"     // barrier: waits for all branches of its parallel, then merges their outputs
)

// Position is a node's location on the canvas, in world coordinates.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// NodeData holds per-node configuration. All fields are optional and depend on
// the node Type; unset fields are omitted so a saved graph stays compact.
type NodeData struct {
	Label string `json:"label,omitempty"`

	// Agent node: an inline system prompt (Instructions), an optional model id,
	// an optional tool allowlist, or the name of a saved subagent persona to
	// delegate to (Agent — when set, the node runs that persona).
	Instructions string   `json:"instructions,omitempty"`
	Model        string   `json:"model,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Skills       []string `json:"skills,omitempty"` // Agent Skills the node may load (the `skill` tool)
	Agent        string   `json:"agent,omitempty"`
	// OutputSchema, when set on an agent node, is a JSON Schema the node's output
	// must satisfy (structured outputs): the prompt asks for JSON and the run
	// validates it, failing the node on a mismatch.
	OutputSchema string `json:"outputSchema,omitempty"`

	// Tool / MCP node: the tool (or MCP tool, e.g. "mcp.firecrawl.scrape") the
	// node invokes, plus its JSON arguments (a template, interpolated against the
	// variable bus). When ToolArgs is empty, the upstream input is used as the
	// arguments if it parses as JSON, else an empty object.
	Tool     string `json:"tool,omitempty"`
	ToolArgs string `json:"toolArgs,omitempty"`

	// Worker node: the name of a worker engine (delegate target) to run the
	// upstream input as a task.
	Worker string `json:"worker,omitempty"`

	// Skill node: a single Agent Skill to apply — the node runs an agent with that
	// skill loaded and instructed to use it on the upstream input.
	Skill string `json:"skill,omitempty"`

	// Logic nodes: a condition expression (if-else / while) and a loop bound.
	Condition     string `json:"condition,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`

	// Parallel node: when a branch errors, FailFast (default true) cancels the
	// sibling branches and fails the run; false collects the per-branch errors and
	// lets the join run on whichever branches succeeded. The zero value is true —
	// callers set NoFailFast to opt into collect mode (so an omitted field in saved
	// JSON keeps the safe fail-fast default).
	NoFailFast bool `json:"noFailFast,omitempty"`

	// Join node: how the branch outputs combine into the join's output text.
	//   "concat" (default) — branch texts joined by JoinSeparator
	//   "array"            — a JSON array of the branch texts (for a downstream transform/agent)
	//   "first"            — the first branch's text (first-wins / race)
	// JoinSeparator overrides the concat separator (default "\n\n---\n\n").
	JoinMode      string `json:"joinMode,omitempty"`
	JoinSeparator string `json:"joinSeparator,omitempty"`

	// User-approval node: the prompt shown to the human reviewer.
	Message string `json:"message,omitempty"`

	// RequireApproval, on an agent node, pauses the run for human approval BEFORE
	// the node executes (a per-node gate, distinct from the standalone approval
	// node). Independent of any tool-level approval the agent asks for mid-run.
	RequireApproval bool `json:"requireApproval,omitempty"`

	// Guardrails node: which checks to run and whether to add an LLM-judge pass.
	// ActionOnViolation is "block" (fail the run, default) or "warn" (continue).
	GuardPII          bool   `json:"guardPii,omitempty"`
	GuardJailbreak    bool   `json:"guardJailbreak,omitempty"`
	GuardModeration   bool   `json:"guardModeration,omitempty"`
	GuardLLMJudge     bool   `json:"guardLlmJudge,omitempty"`
	ActionOnViolation string `json:"actionOnViolation,omitempty"`

	// Transform node: a data-transform expression.
	Transform string `json:"transform,omitempty"`

	// Note node: free text (visual only, not executed).
	Text string `json:"text,omitempty"`

	// Retries is how many extra times to re-run this node if it fails (0 = no
	// retry). Bounded; the engine emits a node_retry trace event per attempt.
	Retries int `json:"retries,omitempty"`
}

// Node is a single step in the graph.
type Node struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Position Position `json:"position"`
	Data     NodeData `json:"data"`
}

// Edge connects one node's output port to another node's input port.
// SourceHandle names the originating port on multi-output nodes (e.g. "if"/"else"
// on an if-else, "continue"/"break" on a while); Label is an optional display label.
// SourceSide/TargetSide record which physical handle (right/bottom on the source,
// left/top on the target) the edge attaches to, so the canvas redraws it to the
// same handles and curves toward them. They're display-only — routing uses
// SourceHandle.
type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"sourceHandle,omitempty"`
	SourceSide   string `json:"sourceSide,omitempty"`
	TargetSide   string `json:"targetSide,omitempty"`
	Label        string `json:"label,omitempty"`
}

// Workflow is a complete graph definition.
type Workflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
	// EvalDataset is the saved set of eval cases for this workflow (persisted with
	// the graph, so an eval suite travels with the workflow and is shareable).
	EvalDataset []EvalCase `json:"evalDataset,omitempty"`
	CreatedAt   string     `json:"createdAt,omitempty"`
	UpdatedAt   string     `json:"updatedAt,omitempty"`
}
