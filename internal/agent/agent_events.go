// agent_events.go — streaming Event interface and the event types emitted during a turn.

package agent

import (
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// Event is a streaming update emitted while a turn is processed.
type Event interface{ isEvent() }

// TextDelta carries a chunk of assistant text as it streams in.
type TextDelta struct{ Text string }

// ThinkingDelta carries a chunk of extended-thinking text as it streams in.
type ThinkingDelta struct{ Text string }

// StreamReset tells the UI to discard the partial assistant/thinking output of
// the current step because a transient stream failure is being retried from
// scratch (the response will re-stream).
type StreamReset struct{}

// TodosUpdated carries the agent's current task list when todo_write is called.
type TodosUpdated struct{ Items []tools.Todo }

// PlanUpdated carries the session's plan artifact when it changes (the model
// drafting/revising it via plan_write, or the user approving/clearing it). Plan
// is nil when the plan was cleared.
type PlanUpdated struct{ Plan *session.Plan }

// ToolStart announces a tool call about to run, with an optional change preview.
type ToolStart struct {
	ID       string
	Name     string
	Input    string
	Diff     string
	Summary  string
	Mutating bool
	// Model, set only for the `task` tool, is the model the delegated sub-agent
	// will run (the persona's pinned model, the fast model for a read-only
	// persona, else the inherited parent model), so the UI can show it on the
	// task card. Empty for other tools or an unknown agent.
	Model string
	// ParentID and Agent are set when this is a delegated sub-agent's tool call,
	// forwarded so the UI can nest it under the parent `task` call (ParentID is
	// that task call's ID) and attribute it to the persona (Agent). Empty for a
	// normal top-level call.
	ParentID string
	Agent    string
}

// ToolEnd reports the result of a tool call.
type ToolEnd struct {
	ID      string
	Name    string
	Output  string
	IsError bool
	Denied  bool
	// Images are any images the tool returned (e.g. a browser screenshot), so
	// the UI can render them inline alongside the text output.
	Images []ToolImage
	// ParentID and Agent mirror ToolStart: set when this is a delegated
	// sub-agent's tool result, so the UI updates the nested card.
	ParentID string
	Agent    string
}

// ToolImage is an image a tool returned, carried to the UI for inline display.
type ToolImage struct {
	MediaType string // e.g. "image/png"
	Data      []byte // raw image bytes
}

// TaskMeta is a per-delegation footer for a `task` sub-agent run: the sub-agent
// finished, so the UI can show a turn-meta line (duration · cost · model ·
// calls) under that task's card — mirroring the parent's per-turn footer.
// ParentID is the `task` tool call's ID the footer belongs to; Agent is the
// persona. It is emitted only when sub-agent streaming is on (see task.go).
type TaskMeta struct {
	ParentID   string
	Agent      string
	Model      string
	CostUSD    float64
	DurationMS int64
	Calls      int // sub-agent tool calls observed
	InTokens   int
	OutTokens  int
}

// TurnDone signals the assistant finished its turn (no more tool calls). Usage
// is the final model call's usage (used for token/context accounting); CostUSD
// is the estimated dollar cost of the whole turn (all model calls) for the
// per-turn footer. TurnUsage is the summed token usage across every model call
// this turn (input/output + the cached-input portion), so the footer can show
// the turn's total tokens and prompt-cache hit rate.
type TurnDone struct {
	Usage     llm.Usage
	TurnUsage llm.Usage
	CostUSD   float64
	// Model is the turn's actual serving model (the last round's turnModel), which
	// an AdaptiveModel/CheapReads downshift can make differ from the session's
	// configured model — so the live footer labels the cost against what really ran.
	Model string
}

// Info is an out-of-band notice (e.g. compaction happened).
type Info struct{ Text string }

// ContextStats carries the estimated per-segment context breakdown for the web
// UI to render as a stacked bar (the TUI renders its own colored bar inline).
// Window is the active context-window size the segments are measured against.
type ContextStats struct {
	Segments []ContextSegment
	Total    int
	Window   int
}

// StepUpdate reports progress through a turn's tool-call loop: Step is the
// current 1-based step and Max is the hard ceiling for this turn.
type StepUpdate struct{ Step, Max int }

// TitleUpdate carries a freshly model-generated session title (see title.go),
// so the UI can refresh the session's displayed name without a reload.
type TitleUpdate struct{ Title string }

// ApprovalRequest asks the UI to confirm a mutating tool call. The agent
// blocks until a Decision is sent on Reply.
type ApprovalRequest struct {
	Tool    string
	Input   string
	Summary string
	Diff    string
	Reply   chan Decision
}

// WorkerActivity is a live progress line from an out-of-process worker (the
// `delegate` tool) while it runs: a tool it called, a status note, or a chunk of
// its streamed answer. ID is the delegate tool call's id so the UI can nest the
// activity under it.
type WorkerActivity struct {
	ID     string
	Worker string
	Kind   string // "tool" | "info" | "text"
	Text   string
}

// BoardMessagePosted is a message an agent posted to the shared message board
// (the post_message tool). The web UI renders the multi-agent conversation from
// these — live as they're posted, and on reopen via the persisted board (see
// session.BoardMessage). From is the posting agent (a persona or the
// orchestrator); To is the addressee, or "" for a broadcast; Seq is the
// message's 1-based id within the turn.
type BoardMessagePosted struct {
	Seq  int
	From string
	To   string
	Text string
}

// QuestionRequest asks the UI to put a multiple-choice question to the user and
// block until they answer (the ask_user tool). Like ApprovalRequest it carries a
// reply channel the host fulfils; Reply receives the chosen option label(s) (an
// empty slice means the user dismissed it without choosing).
type QuestionRequest struct {
	Question  string
	Options   []QuestionOption
	Multiple  bool
	AllowText bool // let the user type a free-text answer (options may be empty)
	Reply     chan []string
}

// QuestionOption is one selectable choice in a QuestionRequest.
type QuestionOption struct {
	Label       string
	Description string
}

// QuestionGroupRequest asks the UI to put SEVERAL questions to the user at once
// (the ask_user tool's questions[] form) as a single panel — e.g. switchable
// tabs — and block until they answer them all. Reply receives one answer-slice
// per question, in the same order (an empty inner slice = that question was
// dismissed; a short slice = the trailing questions were). Only surfaces that set
// the agent's grouped-questions capability receive this event; the others are
// asked one question at a time via QuestionRequest instead.
type QuestionGroupRequest struct {
	Questions []QuestionRequest // sub-questions; each carries its own Options/Multiple/AllowText (inner Reply unused)
	Reply     chan [][]string
}

func (TextDelta) isEvent() {}

func (ThinkingDelta) isEvent() {}

func (StreamReset) isEvent() {}

func (TodosUpdated) isEvent() {}

func (PlanUpdated) isEvent() {}

func (ToolStart) isEvent() {}

func (ToolEnd) isEvent() {}

func (TurnDone) isEvent() {}

func (TaskMeta) isEvent() {}

func (Info) isEvent() {}

func (ContextStats) isEvent() {}

func (StepUpdate) isEvent() {}

func (TitleUpdate) isEvent() {}

func (ApprovalRequest) isEvent() {}

func (WorkerActivity) isEvent() {}

func (QuestionRequest) isEvent() {}

func (QuestionGroupRequest) isEvent() {}

func (BoardMessagePosted) isEvent() {}
