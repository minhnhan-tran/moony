package agent

// board.go implements Option C-1: a shared "message board" (blackboard) that
// lets the agents participating in ONE orchestration tree talk to each other.
//
// Today delegation is strictly one-shot and hierarchical — a parent spawns a
// `task` sub-agent with a prompt and gets a final string back; the child has no
// way to address the parent or a sibling mid-run (see task.go). The board adds a
// genuine peer channel: the orchestrator and every sub-agent it spawns (and
// their sub-agents, recursively) share a single mutex-guarded list of messages
// they can `post_message` to and `read_messages` from. Because contiguous `task`
// calls run in parallel (parallelSafe), the board is concurrency-safe, mirroring
// the ctx-threaded accumulator pattern used by subUsageAcct/taskMetaAcct.
//
// The board is scoped to a single parent turn: it's created once by the
// outermost RunWith and inherited (not replaced) by every descendant's RunWith,
// so the whole delegation tree shares one board, and a fresh turn starts clean.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/tools"
)

// boardMessage is one entry posted to the shared message board by an agent.
// Seq is a 1-based, monotonically increasing id assigned under the board lock,
// so readers can page with `since` and reason about ordering. To is the
// addressee agent's name, or "" for a broadcast visible to everyone.
type boardMessage struct {
	Seq  int
	From string
	To   string
	Text string
	Time time.Time
}

// Per-turn board limits, so a looping/runaway agent can't grow the board
// unboundedly. maxBoardMessageBytes truncates an over-long single message;
// maxBoardMessages caps how many messages one turn's board holds (further posts
// are refused). Both are generous for legitimate coordination.
const (
	maxBoardMessageBytes = 8 << 10 // 8 KiB per message
	maxBoardMessages     = 1000    // per turn
)

// messageBoard is the shared, concurrency-safe blackboard for one orchestration
// tree. Posts are append-only (never clobbered), so parallel sub-agents can post
// without coordinating; reads take a snapshot under the same lock.
type messageBoard struct {
	mu   sync.Mutex
	msgs []boardMessage
}

// post appends a message and returns its assigned sequence number, or 0 if the
// board is full (at maxBoardMessages) — callers surface that as an error. An
// over-long message is truncated to maxBoardMessageBytes. Nil-safe so callers
// needn't check whether a board was threaded onto ctx.
func (b *messageBoard) post(from, to, text string) int {
	if b == nil {
		return 0
	}
	if len(text) > maxBoardMessageBytes {
		// Trim to a valid UTF-8 boundary so the transcript never carries a split
		// rune, then mark it truncated.
		text = strings.ToValidUTF8(text[:maxBoardMessageBytes], "") + "… [truncated]"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) >= maxBoardMessages {
		return 0
	}
	seq := len(b.msgs) + 1
	b.msgs = append(b.msgs, boardMessage{Seq: seq, From: from, To: to, Text: text, Time: time.Now()})
	return seq
}

// read returns the messages with Seq > since (0 = all), optionally restricted to
// those posted by `from`. When `audience` is non-empty, only messages that agent
// can see are returned: broadcasts (To == "") plus messages addressed to it.
func (b *messageBoard) read(since int, from, audience string) []boardMessage {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]boardMessage, 0, len(b.msgs))
	for _, m := range b.msgs {
		if m.Seq <= since {
			continue
		}
		if from != "" && !strings.EqualFold(m.From, from) {
			continue
		}
		if audience != "" && m.To != "" && !strings.EqualFold(m.To, audience) {
			continue
		}
		out = append(out, m)
	}
	return out
}

type messageBoardKey struct{}

// withMessageBoard threads the shared board onto ctx so the board tools (and any
// descendant agent's RunWith) can find it.
func withMessageBoard(ctx context.Context, b *messageBoard) context.Context {
	return context.WithValue(ctx, messageBoardKey{}, b)
}

// messageBoardFrom returns the shared board on ctx, or nil when none was set.
func messageBoardFrom(ctx context.Context) *messageBoard {
	b, _ := ctx.Value(messageBoardKey{}).(*messageBoard)
	return b
}

type agentIdentityKey struct{}

// withAgentIdentity stamps ctx with the name the current agent posts under, so
// the board attributes its messages. The parent sets its active profile name;
// the task tool overrides it with the persona name before running a sub-agent.
func withAgentIdentity(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, agentIdentityKey{}, name)
}

// agentIdentityFrom returns the stored identity, or "" when unset (so RunWith
// can detect whether to stamp the orchestrator's name without clobbering a
// sub-agent's already-stamped persona name).
func agentIdentityFrom(ctx context.Context) string {
	name, _ := ctx.Value(agentIdentityKey{}).(string)
	return name
}

// boardIdentity is agentIdentityFrom with a sensible fallback for display/
// attribution when no name was stamped.
func boardIdentity(ctx context.Context) string {
	if name := agentIdentityFrom(ctx); name != "" {
		return name
	}
	return "orchestrator"
}

type boardAdminKey struct{}

// withBoardAdmin marks ctx as belonging to the top-level orchestrator (the run
// that created the board), or clears the mark for a sub-agent. An admin reader
// sees the WHOLE board — including directed messages between sub-agents — so the
// orchestrator can observe and coordinate all traffic, whereas a sub-agent only
// sees broadcasts plus messages addressed to it.
func withBoardAdmin(ctx context.Context, admin bool) context.Context {
	return context.WithValue(ctx, boardAdminKey{}, admin)
}

// boardAdminFrom reports whether ctx is the orchestrator's (full board read
// visibility). Defaults to false for sub-agents.
func boardAdminFrom(ctx context.Context) bool {
	admin, _ := ctx.Value(boardAdminKey{}).(bool)
	return admin
}

type boardEmitterKey struct{}

// withBoardEmitter threads a sink that surfaces each posted message live in the
// UI. It is set ONCE at the root run (pinned to that turn's UI event sink) and
// never overwritten, so even a sub-agent's post_message — which runs in an
// isolated loop whose per-agent emit closure forwards only tool events — still
// reaches the UI.
func withBoardEmitter(ctx context.Context, fn func(boardMessage)) context.Context {
	return context.WithValue(ctx, boardEmitterKey{}, fn)
}

// boardEmitterFrom returns the live board sink on ctx, or nil when none was set.
func boardEmitterFrom(ctx context.Context) func(boardMessage) {
	fn, _ := ctx.Value(boardEmitterKey{}).(func(boardMessage))
	return fn
}

// agentMessagingEnabled reports whether the shared message board (the
// post_message / read_messages tools) is exposed. Default on; set
// SQE_AGENT_MESSAGING=0/off/false/no to disable it.
func agentMessagingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_AGENT_MESSAGING"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// registerBoardTools adds the post_message + read_messages tools to a registry,
// so the orchestrator and sub-agents can use the shared board. A no-op when
// messaging is disabled. The tools read the board + caller identity from ctx at
// Execute time, so a single registration works for every agent.
//
// allow honors a persona's tool allowlist (least privilege): when non-nil, a
// board tool is added only if its name is present, so a persona declared with a
// restricted `tools:` list doesn't silently gain messaging unless it opts in. A
// nil allow registers both (the orchestrator and default-toolset sub-agents).
func registerBoardTools(reg *tools.Registry, allow map[string]bool) {
	if reg == nil || !agentMessagingEnabled() {
		return
	}
	for _, t := range []tools.Tool{postMessageTool{}, readMessagesTool{}} {
		if allow == nil || allow[t.Name()] {
			reg.Register(t)
		}
	}
}

// postMessageTool lets an agent post a message to the shared board for the other
// agents in this orchestration tree (the orchestrator and its sub-agents).
type postMessageTool struct{}

func (postMessageTool) Name() string { return "post_message" }

func (postMessageTool) Description() string {
	return "Post a message to the shared agent message board so the other agents working on this " +
		"request (the orchestrator and any sub-agents) can read it. Use it to coordinate: hand off " +
		"findings, ask a sibling agent a question, or report partial progress. Set \"to\" to an agent's " +
		"name to address it directly, or omit it to broadcast to everyone. Messages persist for the rest " +
		"of this turn; read them back with read_messages."
}

func (postMessageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"to":   {"type": "string", "description": "Optional addressee: the name of the agent this message is for. Omit to broadcast to all agents."},
			"text": {"type": "string", "description": "The message body."}
		},
		"required": ["text"]
	}`)
}

// Mutates is false: posting only appends to an in-memory, turn-scoped board with
// no external side effects, so it needs no approval and is safe to run in
// parallel (the board is mutex-guarded and posts are append-only).
func (postMessageTool) Mutates() bool { return false }

func (postMessageTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid post_message input: %v", err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return "", fmt.Errorf("text is required")
	}
	board := messageBoardFrom(ctx)
	if board == nil {
		return "", fmt.Errorf("the message board is only available while delegating to sub-agents")
	}
	from := boardIdentity(ctx)
	to := strings.TrimSpace(in.To)
	seq := board.post(from, to, in.Text)
	if seq == 0 {
		return "", fmt.Errorf("the message board is full (max %d messages this turn)", maxBoardMessages)
	}
	// Surface it live in the UI (also persisted at turn end for replay).
	if emit := boardEmitterFrom(ctx); emit != nil {
		emit(boardMessage{Seq: seq, From: from, To: to, Text: in.Text, Time: time.Now()})
	}
	if to != "" {
		return fmt.Sprintf("posted message #%d from %q to %q", seq, from, to), nil
	}
	return fmt.Sprintf("posted message #%d from %q (broadcast)", seq, from), nil
}

var _ tools.Tool = postMessageTool{}

// readMessagesTool lets an agent read messages other agents posted to the shared
// board this turn — broadcasts plus anything addressed to it.
type readMessagesTool struct{}

func (readMessagesTool) Name() string { return "read_messages" }

func (readMessagesTool) Description() string {
	return "Read messages from the shared agent message board posted by the other agents working on " +
		"this request. Returns broadcasts plus messages addressed to you, each tagged with a sequence " +
		"number (#N), sender, and addressee. Pass \"since\" with the highest # you've already seen to get " +
		"only new messages. Use it to pick up findings or answers other agents posted with post_message."
}

func (readMessagesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"since":     {"type": "integer", "description": "Only return messages after this sequence number (the highest # you've already seen). Omit or 0 for all messages."},
			"from":      {"type": "string", "description": "Optional: only messages posted by this agent name."},
			"mine_only": {"type": "boolean", "description": "If true, exclude broadcasts and return only messages addressed specifically to you. Default false (broadcasts + messages to you)."}
		}
	}`)
}

func (readMessagesTool) Mutates() bool { return false }

func (readMessagesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Since    int    `json:"since"`
		From     string `json:"from"`
		MineOnly bool   `json:"mine_only"`
	}
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("invalid read_messages input: %v", err)
		}
	}
	board := messageBoardFrom(ctx)
	if board == nil {
		return "", fmt.Errorf("the message board is only available while delegating to sub-agents")
	}
	me := boardIdentity(ctx)
	// The orchestrator (board admin) sees the whole board, including directed
	// messages between sub-agents, so it can observe and coordinate all traffic.
	// A sub-agent sees only broadcasts plus messages addressed to it.
	audience := me
	if boardAdminFrom(ctx) {
		audience = ""
	}
	msgs := board.read(in.Since, strings.TrimSpace(in.From), audience)
	if in.MineOnly {
		// Drop broadcasts, keeping only messages addressed specifically to me.
		filtered := msgs[:0]
		for _, m := range msgs {
			if strings.EqualFold(m.To, me) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	if len(msgs) == 0 {
		return "(no messages)", nil
	}
	var b strings.Builder
	for _, m := range msgs {
		to := m.To
		if to == "" {
			to = "all"
		}
		fmt.Fprintf(&b, "#%d %s → %s (%s): %s\n", m.Seq, m.From, to, m.Time.Format("15:04:05"), m.Text)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

var _ tools.Tool = readMessagesTool{}
