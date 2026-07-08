package web

import (
	"encoding/base64"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/attach"
	sess "github.com/pysty1995/moony/internal/session"
)

// wireEvent is the JSON-friendly, flattened form of an agent.Event sent to
// browsers over SSE. Each carries a monotonic Seq so late/remote viewers can
// reason about ordering after a history replay. Synthetic types ("state",
// "error") are produced by the session, not the agent.
type wireEvent struct {
	Seq      int    `json:"seq"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Tool     string `json:"tool,omitempty"`
	ToolID   string `json:"toolId,omitempty"`
	Input    string `json:"input,omitempty"`
	Diff     string `json:"diff,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Output   string `json:"output,omitempty"`
	IsError  bool   `json:"isError,omitempty"`
	Mutating bool   `json:"mutating,omitempty"`
	// ParentID and Agent are set on a "tool_start"/"tool_end" for a delegated
	// sub-agent's tool call, so the browser nests it under the parent `task`
	// card (ParentID is that task call's toolId) and attributes it to the
	// persona (Agent).
	ParentID string `json:"parentId,omitempty"`
	Agent    string `json:"agent,omitempty"`
	// Model is set on a `task` "tool_start" with the delegated sub-agent's model
	// id, so the browser can show which model the delegation runs on.
	Model string `json:"model,omitempty"`
	State string `json:"state,omitempty"`
	// CanQueue is set on a "state" event emitted by a web-native session to tell
	// the browser this session supports queuing messages while busy (the relay
	// terminal-mirror path doesn't emit it, so that view keeps its old
	// composer-locked-while-busy behavior).
	CanQueue bool `json:"canQueue,omitempty"`
	// Queue carries the session's pending queued-message texts (FIFO) on a
	// "queue" event, so every viewer sees what's pending and the count.
	Queue    []string          `json:"queue,omitempty"`
	Todos    []todoWire        `json:"todos,omitempty"`
	Plan     *planWire         `json:"plan,omitempty"`
	Question *questionWire     `json:"question,omitempty"`
	Status   *statusWire       `json:"status,omitempty"`
	Commands []cmdWire         `json:"commands,omitempty"`
	Mentions []mentionWire     `json:"mentions,omitempty"`
	Menu     *menuWire         `json:"menu,omitempty"`
	Step     int               `json:"step,omitempty"`
	MaxSteps int               `json:"maxSteps,omitempty"`
	Sessions []sessionWireItem `json:"sessions,omitempty"`
	Context  *contextWire      `json:"context,omitempty"`
	// Attachments decorate a "user" event with the images/files sent that turn,
	// so the browser renders thumbnails (images carry a data URI) and chips.
	Attachments []attachWire `json:"attachments,omitempty"`
	// Images decorate a "tool_end" event with any images the tool returned (e.g.
	// a browser screenshot), each as a data URI the browser renders inline.
	Images []string `json:"images,omitempty"`
	// Meta carries a completed turn's footer (datetime · duration · model ·
	// think:level) for a "turn_meta" event, replayed when a saved session reopens.
	Meta *turnMetaWire `json:"meta,omitempty"`
	// Board carries one shared-message-board post for a "board_message" event, so
	// the browser can render the multi-agent conversation (live + on replay).
	Board *boardMsgWire `json:"board,omitempty"`
	// Time stamps a "user" event with when it was sent (RFC3339), for the
	// per-message footer. Set on replay from the answering turn's time.
	Time string `json:"time,omitempty"`
	// CostUSD carries the just-finished turn's estimated cost on a "turn_done"
	// event, so the live per-turn footer shows the answering cost (not the
	// cumulative session total).
	CostUSD float64 `json:"costUSD,omitempty"`
	// TurnIn/TurnOut/TurnCacheRead carry the just-finished turn's total token
	// usage on a "turn_done" event (summed across all model calls), so the live
	// footer can show tokens and the prompt-cache hit rate. TurnCacheRead is the
	// cached-input subset of TurnIn.
	TurnIn        int `json:"turnIn,omitempty"`
	TurnOut       int `json:"turnOut,omitempty"`
	TurnCacheRead int `json:"turnCacheRead,omitempty"`
}

// turnMetaWire is one completed turn's footer for the browser: the wall-clock
// time the turn finished (RFC3339, rendered locally), its elapsed duration in
// milliseconds, its estimated cost in USD, the model, the reasoning level, the
// number of model calls (loop steps) the turn made, and the turn's total
// input/output tokens with the cached-input subset (for the cache-hit rate).
type turnMetaWire struct {
	Time            string  `json:"time"`
	DurationMS      int64   `json:"durationMs,omitempty"`
	CostUSD         float64 `json:"costUSD,omitempty"`
	Model           string  `json:"model,omitempty"`
	Reasoning       string  `json:"reasoning,omitempty"`
	Calls           int     `json:"calls,omitempty"`
	InTokens        int     `json:"inTokens,omitempty"`
	OutTokens       int     `json:"outTokens,omitempty"`
	CacheReadTokens int     `json:"cacheReadTokens,omitempty"`
}

// boardMsgWire is one shared-message-board post for the browser's Agent Messages
// panel: the sender, the addressee ("" = broadcast), the text, and the per-turn
// sequence number.
type boardMsgWire struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	Text string `json:"text"`
}

// attachWire describes one attachment on a user turn for the browser. DataURI is
// set only for images (for an inline thumbnail); other kinds render as a chip.
type attachWire struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // image | document | file
	DataURI string `json:"dataUri,omitempty"`
}

// contextWire is the estimated context-usage breakdown for the browser to
// render as a color-coded stacked bar plus a legend. Total and Window are in
// tokens; each segment carries its own token estimate.
type contextWire struct {
	Segments []contextSegmentWire `json:"segments"`
	Total    int                  `json:"total"`
	Window   int                  `json:"window"`
}

type contextSegmentWire struct {
	Label  string               `json:"label"`
	Tokens int                  `json:"tokens"`
	Items  []contextSegmentWire `json:"items,omitempty"`
}

// contextSegmentsWire converts agent context segments (with optional per-item
// detail) to their wire form for the browser's stacked bar + itemized legend.
func contextSegmentsWire(segs []agent.ContextSegment) []contextSegmentWire {
	out := make([]contextSegmentWire, 0, len(segs))
	for _, s := range segs {
		w := contextSegmentWire{Label: s.Label, Tokens: s.Tokens}
		if len(s.Items) > 0 {
			w.Items = contextSegmentsWire(s.Items)
		}
		out = append(out, w)
	}
	return out
}

// sessionWireItem is one saved session of the current project, for the sidebar.
type sessionWireItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Active bool   `json:"active,omitempty"`
}

// cmdWire is one slash command for the browser's command palette.
type cmdWire struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
	Desc string `json:"desc,omitempty"`
}

// mentionWire is one @-mention candidate (an enabled persona or worker) for the
// browser's @-mention suggestion dropdown.
type mentionWire struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // agent | worker | skill
	Desc string `json:"desc,omitempty"`
}

// menuWire is a clickable picker (e.g. model/route selection) for the browser.
// Selecting an item sends "Command Value" back as a message.
type menuWire struct {
	Title   string         `json:"title,omitempty"`
	Command string         `json:"command"`
	Items   []menuItemWire `json:"items"`
}

type menuItemWire struct {
	Label   string `json:"label"`
	Desc    string `json:"desc,omitempty"`
	Value   string `json:"value"`
	Current bool   `json:"current,omitempty"`
}

// statusWire mirrors the terminal header + status bar: app version, model/route,
// working directory, approval mode, token + cost/quota usage, context fill, and
// current activity.
type statusWire struct {
	Version  string `json:"version,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// LLMProvider is the /provider backend selection (ollama / openai / anthropic),
	// distinct from Provider (the derived API route). The composer shows it before
	// the model name. Normalized id; the client maps it to a friendly label.
	LLMProvider string `json:"llmProvider,omitempty"`
	Reasoning   string `json:"reasoning,omitempty"`
	// ReasoningLevels is the set of reasoning levels VALID for the current model,
	// so the web reasoning picker offers the right options per family (Gemini and
	// Opus differ — see config.ReasoningLevelsFor). Each is {id, desc}.
	ReasoningLevels []levelWire `json:"reasoningLevels,omitempty"`
	// MemoryCount is how many project AGENTS.md memory rules are stored, so the
	// composer's "memory" pill can show a count without a separate fetch.
	MemoryCount int    `json:"memoryCount,omitempty"`
	WorkingDir  string `json:"workingDir,omitempty"`
	// Agent is the active primary profile's name (build / plan / a custom
	// persona), so the browser can show it as a badge and offer a switcher
	// (parity with the TUI's /agent and Tab).
	Agent                string     `json:"agent,omitempty"`
	Approval             string     `json:"approval,omitempty"`
	Sandbox              string     `json:"sandbox,omitempty"`
	GitBranch            string     `json:"gitBranch,omitempty"`
	GitDirty             int        `json:"gitDirty,omitempty"`
	GitAhead             int        `json:"gitAhead,omitempty"`    // commits ahead of upstream
	GitBehind            int        `json:"gitBehind,omitempty"`   // commits behind upstream
	GitUpstream          string     `json:"gitUpstream,omitempty"` // upstream ref, e.g. origin/main
	Host                 string     `json:"host,omitempty"`
	Relay                string     `json:"relay,omitempty"`
	TokensIn             int        `json:"tokensIn"`
	TokensOut            int        `json:"tokensOut"`
	CostUSD              float64    `json:"costUSD,omitempty"`
	QuotaSpend           float64    `json:"quotaSpend,omitempty"`
	QuotaBudget          float64    `json:"quotaBudget,omitempty"`
	QuotaKnown           bool       `json:"quotaKnown,omitempty"`
	QuotaName            string     `json:"quotaName,omitempty"`            // tooltip: the gateway's project name, or "personal"
	QuotaTokens          int        `json:"quotaTokens,omitempty"`          // tokens used in the quota period (24h)
	QuotaTokensMax       int        `json:"quotaTokensMax,omitempty"`       // per-day token cap, when the budget carries one
	QuotaTokenRate       int        `json:"quotaTokenRate,omitempty"`       // token rate limit (tokens per period)
	QuotaTokenRatePeriod string     `json:"quotaTokenRatePeriod,omitempty"` // rate-limit window, e.g. "4m"
	ContextPercent       int        `json:"contextPercent"`
	ContextTokens        int        `json:"contextTokens,omitempty"` // most recent turn's input-token size
	ContextWindow        int        `json:"contextWindow,omitempty"` // model's context window, for the same calc as ContextPercent
	ActiveTool           string     `json:"activeTool,omitempty"`
	ToolsInFlight        int        `json:"toolsInFlight,omitempty"`
	MCP                  []mcpWire  `json:"mcp,omitempty"`
	Files                []fileWire `json:"files,omitempty"`
}

// levelWire is one selectable reasoning level (id + short description) for the
// web reasoning picker.
type levelWire struct {
	ID   string `json:"id"`
	Desc string `json:"desc,omitempty"`
}

// mcpWire mirrors a connected MCP server for the left panel.
type mcpWire struct {
	Name      string `json:"name"`
	Tools     int    `json:"tools"`
	Resources int    `json:"resources"`
	Prompts   int    `json:"prompts"`
}

// fileWire mirrors a file the agent created or edited this session.
type fileWire struct {
	Path    string `json:"path"`
	Created bool   `json:"created,omitempty"`
}

type todoWire struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// planWire mirrors the session's plan artifact for the browser.
type planWire struct {
	Title  string         `json:"title,omitempty"`
	Status string         `json:"status"`
	Steps  []planStepWire `json:"steps"`
}

type planStepWire struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// questionWire mirrors a pending ask_user question for the browser. A grouped
// ask (the questions[] form) instead carries Questions — one entry per question,
// rendered as switchable tabs — with the scalar fields left empty.
type questionWire struct {
	Question  string               `json:"question"`
	Multiple  bool                 `json:"multiple,omitempty"`
	AllowText bool                 `json:"allowText,omitempty"`
	Options   []questionOptionWire `json:"options"`
	Questions []*questionWire      `json:"questions,omitempty"`
}

type questionOptionWire struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// toWire flattens an agent.Event into its wire form. The ApprovalRequest's Reply
// channel is intentionally dropped — the session holds it and bridges the reply
// from the browser's /approve call.
func toWire(ev agent.Event) wireEvent {
	switch e := ev.(type) {
	case agent.TextDelta:
		return wireEvent{Type: "text", Text: e.Text}
	case agent.ThinkingDelta:
		return wireEvent{Type: "thinking", Text: e.Text}
	case agent.StreamReset:
		return wireEvent{Type: "stream_reset"}
	case agent.ToolStart:
		return wireEvent{Type: "tool_start", Tool: e.Name, ToolID: e.ID, Input: e.Input, Diff: e.Diff, Summary: e.Summary, Mutating: e.Mutating, ParentID: e.ParentID, Agent: e.Agent, Model: e.Model}
	case agent.ToolEnd:
		var imgs []string
		for _, im := range e.Images {
			imgs = append(imgs, dataURI(im.MediaType, im.Data))
		}
		return wireEvent{Type: "tool_end", Tool: e.Name, ToolID: e.ID, Output: e.Output, IsError: e.IsError, Images: imgs, ParentID: e.ParentID, Agent: e.Agent}
	case agent.TurnDone:
		return wireEvent{Type: "turn_done", CostUSD: e.CostUSD, Model: e.Model,
			TurnIn: e.TurnUsage.InputTokens, TurnOut: e.TurnUsage.OutputTokens,
			TurnCacheRead: e.TurnUsage.CacheReadInputTokens}
	case agent.TaskMeta:
		// A delegated sub-agent finished: carry its footer (duration · cost ·
		// model · calls) so the browser can render a turn-meta line under the
		// parent task card. Reuses turnMetaWire as the number carrier.
		return wireEvent{Type: "task_meta", ParentID: e.ParentID, Agent: e.Agent,
			Meta: &turnMetaWire{
				Time:       time.Now().Format(time.RFC3339),
				DurationMS: e.DurationMS,
				CostUSD:    e.CostUSD,
				Model:      e.Model,
				Calls:      e.Calls,
				InTokens:   e.InTokens,
				OutTokens:  e.OutTokens,
			}}
	case agent.BoardMessagePosted:
		// An agent posted to the shared message board; the browser renders the
		// multi-agent conversation in its Agent Messages panel.
		return wireEvent{Type: "board_message", Board: &boardMsgWire{
			Seq:  e.Seq,
			From: e.From,
			To:   e.To,
			Text: e.Text,
		}}
	case agent.StepUpdate:
		return wireEvent{Type: "step", Step: e.Step, MaxSteps: e.Max}
	case agent.TitleUpdate:
		return wireEvent{Type: "title", Text: e.Title}
	case agent.Info:
		return wireEvent{Type: "info", Text: e.Text}
	case agent.ContextStats:
		return wireEvent{Type: "context", Context: &contextWire{Segments: contextSegmentsWire(e.Segments), Total: e.Total, Window: e.Window}}
	case agent.ApprovalRequest:
		return wireEvent{Type: "approval", Tool: e.Tool, Input: e.Input, Summary: e.Summary, Diff: e.Diff}
	case agent.TodosUpdated:
		ts := make([]todoWire, 0, len(e.Items))
		for _, it := range e.Items {
			ts = append(ts, todoWire{Content: it.Content, Status: string(it.Status)})
		}
		return wireEvent{Type: "todos", Todos: ts}
	case agent.PlanUpdated:
		return wireEvent{Type: "plan", Plan: planWireFrom(e.Plan)}
	case agent.QuestionRequest:
		return wireEvent{Type: "question", Question: questionWireFrom(e)}
	case agent.QuestionGroupRequest:
		return wireEvent{Type: "question", Question: questionGroupWireFrom(e)}
	case agent.WorkerActivity:
		// Live worker progress; the browser nests it under the delegate call.
		return wireEvent{Type: "worker", Tool: e.Worker, ToolID: e.ID, Summary: e.Kind, Text: e.Text}
	default:
		return wireEvent{Type: "info"}
	}
}

// planWireFrom renders the session's plan artifact for the browser, or nil when
// the plan was cleared.
func planWireFrom(p *sess.Plan) *planWire {
	if p == nil || len(p.Steps) == 0 {
		return nil
	}
	out := &planWire{Title: p.Title, Status: string(p.Status)}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, planStepWire{Title: s.Title, Detail: s.Detail})
	}
	return out
}

// questionWireFrom renders a pending ask_user question for the browser.
func questionWireFrom(q agent.QuestionRequest) *questionWire {
	out := &questionWire{Question: q.Question, Multiple: q.Multiple, AllowText: q.AllowText}
	for _, o := range q.Options {
		out.Options = append(out.Options, questionOptionWire{Label: o.Label, Description: o.Description})
	}
	return out
}

// questionGroupWireFrom renders a pending grouped ask_user question (the
// questions[] form) for the browser — one questionWire per question, which the
// client shows as switchable tabs and answers all at once.
func questionGroupWireFrom(g agent.QuestionGroupRequest) *questionWire {
	out := &questionWire{}
	for _, q := range g.Questions {
		out.Questions = append(out.Questions, questionWireFrom(q))
	}
	return out
}

// attachWireFromAttachment renders a freshly-posted attachment for the browser:
// images carry an inline data URI; PDFs and Office files show as labeled chips.
func attachWireFromAttachment(a attach.Attachment) attachWire {
	switch a.Kind {
	case attach.KindImage:
		return attachWire{Name: a.Name, Kind: "image", DataURI: dataURI(a.MediaType, a.Data)}
	case attach.KindDocument:
		return attachWire{Name: a.Name, Kind: "document"}
	default:
		return attachWire{Name: a.Name, Kind: "file"}
	}
}

func dataURI(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
