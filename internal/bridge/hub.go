// Package bridge mirrors a single live agent conversation to any number of
// viewers and lets them drive the controlling UI remotely. It is deliberately
// UI-agnostic: it imports only the agent package, so both the terminal UI and
// the web server can depend on it without creating a cycle.
//
// The terminal UI owns the agent and remains the single turn-runner. It
// publishes everything it renders into the hub (agent events plus a few
// synthetic markers — the user echo, state changes, errors, resets). The web
// server subscribes to that stream and replays history to late/remote viewers.
//
// Control flows the other way through the controller callbacks: a browser's
// Submit/Approve/Interrupt is forwarded into the controlling UI, which applies
// it through its normal input paths. That keeps a single owner of the agent and
// avoids races over who runs a turn.
package bridge

import (
	"sync"

	"github.com/pysty1995/moony/internal/agent"
)

// Session state values, mirrored verbatim onto the wire for browsers.
const (
	StateIdle     = "idle"
	StateBusy     = "busy"
	StateApproval = "awaiting_approval"
	StateQuestion = "awaiting_question"
)

// Msg kinds. KindAgent wraps a passthrough agent.Event; the rest are synthetic
// markers produced by the controlling UI so viewers can reconstruct the
// conversation exactly as the terminal shows it.
const (
	KindAgent    = "agent"
	KindUser     = "user"
	KindState    = "state"
	KindError    = "error"
	KindReset    = "reset"
	KindStatus   = "status"
	KindCommands = "commands"
	KindMenu     = "menu"
	KindSessions = "sessions"
	KindQueue    = "queue"
)

// Command describes one slash command for the viewer's command palette.
type Command struct {
	Name string
	Args string
	Desc string
}

// Mention describes one @-mention candidate (an enabled subagent persona or
// worker) for the viewer's @-mention suggestion dropdown.
type Mention struct {
	Name string
	Kind string // "agent" | "worker"
	Desc string
}

// SessionInfo describes one saved session of the current project (working dir)
// for the viewer's session list. Selecting it switches the terminal to it.
type SessionInfo struct {
	ID     string
	Title  string
	Detail string // e.g. "Jan 2 15:04 · <id>"
	Active bool
}

// Menu is a clickable picker mirrored to viewers in place of a terminal-only
// selection menu (e.g. /models, /api, /reasoning). Selecting an item sends
// "Command Value" back as input.
type Menu struct {
	Title   string
	Command string // slash command to send with the chosen item's Value, e.g. "/model"
	Items   []MenuItem
}

// MenuItem is one selectable option in a Menu.
type MenuItem struct {
	Label   string
	Desc    string
	Value   string
	Current bool
}

// Status is a snapshot of the controlling UI's header and status bar — the app
// version, model/route, working directory, approval mode, token usage,
// cost/quota, context fill, current activity, plus the left-panel sections
// (MCP servers and modified files). Unlike transcript events it isn't part of
// history; only the latest snapshot is retained and replayed to new viewers.
type Status struct {
	Version     string
	Model       string
	Provider    string
	LLMProvider string // /provider backend (anthropic/gemini/ollama); distinct from Provider (the route)
	Reasoning   string
	WorkingDir  string
	Approval    string
	Sandbox     string
	GitBranch   string
	GitDirty    int
	GitAhead    int    // commits ahead of upstream
	GitBehind   int    // commits behind upstream
	GitUpstream string // upstream ref (e.g. origin/main)
	Host        string // the terminal machine's local name (e.g. macOS Computer Name)
	Relay       string // relay connection state: "", "connecting", "connected", "retrying"
	TokensIn    int
	TokensOut   int
	CostUSD     float64
	QuotaSpend  float64
	QuotaBudget float64
	QuotaKnown  bool
	QuotaName   string // "personal" or a gateway project name (status-bar tooltip)
	// QuotaBalance is prepaid credit REMAINING, for providers that report that instead
	// of an amount spent. QuotaBalanceSet separates a real $0.00 from "not reported".
	QuotaBalance         float64
	QuotaBalanceSet      bool
	QuotaTokens          int    // tokens used in the quota period (24h)
	QuotaTokensMax       int    // per-day token cap, when the budget carries one
	QuotaTokenRate       int    // token rate limit (tokens per period)
	QuotaTokenRatePeriod string // rate-limit window, e.g. "4m"
	// QuotaFallbackFrom names the provider an in-force quota fallback moved this
	// session away from, and QuotaFallbackResetAt (RFC3339) when it reverts. Empty
	// unless a fallback is active; lets the mirrored status explain why the provider
	// isn't the one that was picked.
	QuotaFallbackFrom    string
	QuotaFallbackResetAt string
	ContextPercent       int // -1 when unknown (no completed turn yet)
	ActiveTool           string
	ToolsInFlight        int
	Agent                string // active primary-agent/persona name (build/plan/custom), for the composer badge
	MCP                  []MCPServer
	Files                []ModFile
}

// MCPServer mirrors a connected MCP server for the left panel.
type MCPServer struct {
	Name      string
	Tools     int
	Resources int
	Prompts   int
}

// ModFile mirrors a file the agent created or edited this session.
type ModFile struct {
	Path    string
	Created bool
}

// subBuffer bounds each viewer's backlog. A viewer that can't keep up drops
// live messages (it can reconnect for a full history replay) rather than
// stalling the agent.
const subBuffer = 1024

// Msg is one item in the shared conversation stream.
type Msg struct {
	Seq      int
	Kind     string
	Event    agent.Event   // set when Kind == KindAgent
	Text     string        // user text (KindUser) or error text (KindError)
	State    string        // set when Kind == KindState
	Status   *Status       // set when Kind == KindStatus
	Commands []Command     // set when Kind == KindCommands
	Mentions []Mention     // set when Kind == KindCommands (@-mention candidates)
	Menu     *Menu         // set when Kind == KindMenu
	Sessions []SessionInfo // set when Kind == KindSessions
	Queue    []string      // set when Kind == KindQueue (pending busy-time messages)
}

// Hub fans out one conversation to many viewers and routes their control
// actions back to the owning UI. All methods are safe for concurrent use.
type Hub struct {
	mu       sync.Mutex
	seq      int
	history  []Msg
	subs     map[int]chan Msg
	nextSub  int
	state    string
	activeID string // id of the session the controlling UI is currently on

	// lastStatus is the most recent status snapshot. It's kept separately from
	// history (which would otherwise grow with every token update) and replayed
	// to each new viewer so the status bar is populated immediately.
	lastStatus *Msg
	// lastCommands is the slash-command list (static per session) retained for
	// replay so a new viewer's command palette is populated immediately.
	lastCommands *Msg
	// lastSessions is the project's saved-session list, retained for replay so
	// a new viewer's session sidebar is populated immediately.
	lastSessions *Msg
	// lastQueue is the most recent busy-time message queue, retained (like
	// status) and replayed to each new viewer so the composer's queue strip is
	// populated immediately on connect.
	lastQueue *Msg

	// Controller callbacks, wired by the owning UI via SetController. When nil
	// (the default) the hub is view-only and control actions are rejected.
	submit    func(string)
	approve   func(agent.Decision)
	interrupt func()
	// answer fulfils a pending ask_user question with the chosen option
	// label(s). Wired separately via SetAnswerController so SetController's
	// signature (and its many call sites) stays stable. Nil leaves questions
	// answerable only from the owning UI.
	answer func([]string)
	// switchTo asks the owning UI to switch its active session. When nil the
	// hub can't switch (view-only or a UI that doesn't support switching).
	switchTo func(string)
	// queueCtl forwards a busy-time queue mutation (add/remove/clear/pop) to the
	// owning UI, which owns the authoritative queue. Nil leaves the queue
	// manageable only from the owning UI.
	queueCtl func(action string, index int, text string)

	// onActive, when set, is called (off the lock) whenever the active session
	// id changes — used by the multi-project server to evict a store-backed
	// instance of a session that has just become the terminal's live one, so the
	// same session never runs in two places at once.
	onActive func(string)
}

// New returns an idle, view-only hub. Call SetController to allow viewers to
// drive the agent.
func New() *Hub {
	return &Hub{subs: map[int]chan Msg{}, state: StateIdle}
}

// SetController wires the callbacks used to drive the owning UI. Pass nil
// callbacks to leave the hub view-only. switchTo (which may be nil) lets a
// viewer move the owning UI to another session of the same project.
func (h *Hub) SetController(submit func(string), approve func(agent.Decision), interrupt func(), switchTo func(string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.submit, h.approve, h.interrupt, h.switchTo = submit, approve, interrupt, switchTo
}

// SetAnswerController wires the callback that fulfils a pending ask_user question
// with the chosen option label(s). Pass nil to leave questions answerable only
// from the owning UI. Kept separate from SetController so that method's
// signature — and its many call sites — stays stable.
func (h *Hub) SetAnswerController(answer func([]string)) {
	h.mu.Lock()
	h.answer = answer
	h.mu.Unlock()
}

// SetQueueController wires the callback that applies a busy-time queue mutation
// (add/remove/clear/pop) to the owning UI's authoritative queue. Pass nil to
// leave the queue manageable only from the owning UI. Kept separate from
// SetController so that method's signature stays stable.
func (h *Hub) SetQueueController(fn func(action string, index int, text string)) {
	h.mu.Lock()
	h.queueCtl = fn
	h.mu.Unlock()
}

// SetActiveSession records the id of the session the owning UI is currently on.
// The owning UI calls this on startup and after every switch/reset so viewers
// (and the multi-project web server's routing) know which session the hub
// currently mirrors.
func (h *Hub) SetActiveSession(id string) {
	h.mu.Lock()
	changed := h.activeID != id
	h.activeID = id
	fn := h.onActive
	h.mu.Unlock()
	if changed && fn != nil {
		fn(id) // off the lock: the callback may take the server's own lock
	}
}

// OnActiveChange registers a callback invoked (off the lock) whenever the active
// session id changes. Used by the multi-project server to drop a store-backed
// session that has just become the terminal's live one.
func (h *Hub) OnActiveChange(fn func(string)) {
	h.mu.Lock()
	h.onActive = fn
	h.mu.Unlock()
}

// ActiveSessionID returns the id of the session the owning UI is currently on,
// or "" if none has been recorded yet.
func (h *Hub) ActiveSessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activeID
}

// publishLocked stamps a sequence number, records the message in history, and
// fans it out. Caller must hold h.mu.
func (h *Hub) publishLocked(m Msg) {
	h.seq++
	m.Seq = h.seq
	if m.Kind == KindState {
		h.state = m.State
	}
	h.history = append(h.history, m)
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
			// Slow viewer: drop the live message; a reconnect replays history.
		}
	}
}

// PublishEvent mirrors a passthrough agent event.
func (h *Hub) PublishEvent(ev agent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishLocked(Msg{Kind: KindAgent, Event: ev})
}

// PublishUser echoes a user prompt (shown verbatim to viewers).
func (h *Hub) PublishUser(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishLocked(Msg{Kind: KindUser, Text: text})
}

// PublishState records a session-state transition (idle/busy/awaiting_approval).
func (h *Hub) PublishState(state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishLocked(Msg{Kind: KindState, State: state})
}

// PublishError records a turn-ending error.
func (h *Hub) PublishError(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishLocked(Msg{Kind: KindError, Text: text})
}

// PublishStatus fans out a status-bar snapshot. The snapshot is not appended to
// history (it changes too often); only the latest is retained for replay.
func (h *Hub) PublishStatus(st Status) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	m := Msg{Seq: h.seq, Kind: KindStatus, Status: &st}
	h.lastStatus = &m
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// PublishCommands fans out the slash-command list (and the @-mention candidate
// list) for viewers' command palettes and @-mention dropdowns. Like status it
// isn't stored in history; only the latest set is retained.
func (h *Hub) PublishCommands(cmds []Command, mentions []Mention) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	m := Msg{Seq: h.seq, Kind: KindCommands, Commands: cmds, Mentions: mentions}
	h.lastCommands = &m
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// PublishSessions fans out the project's saved-session list for the viewer's
// sidebar. Like status/commands it's retained (not in history) for replay.
func (h *Hub) PublishSessions(sessions []SessionInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	m := Msg{Seq: h.seq, Kind: KindSessions, Sessions: sessions}
	h.lastSessions = &m
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// PublishMenu fans out a clickable picker to viewers. It is transient — neither
// stored in history nor retained — since a picker is ephemeral; a viewer that
// reconnects simply re-runs the command.
func (h *Hub) PublishMenu(menu Menu) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	m := Msg{Seq: h.seq, Kind: KindMenu, Menu: &menu}
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// PublishQueue fans out the busy-time message queue (the texts pending after the
// running turn). Like status it isn't stored in history; only the latest queue
// is retained and replayed so a new viewer's composer queue strip is populated
// immediately. A nil/empty slice clears the strip.
func (h *Hub) PublishQueue(items []string) {
	q := append([]string(nil), items...)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	m := Msg{Seq: h.seq, Kind: KindQueue, Queue: q}
	h.lastQueue = &m
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// Reset clears the conversation: it drops history (so new viewers start clean)
// and fans a reset marker to current viewers so they clear too.
func (h *Hub) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	h.history = nil
	m := Msg{Seq: h.seq, Kind: KindReset}
	for _, ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// Subscribe registers a viewer, returning its id, channel, a snapshot of the
// history so far, and the current state. Registration and snapshot happen under
// one lock so no message is missed or duplicated across the handoff.
func (h *Hub) Subscribe() (int, <-chan Msg, []Msg, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextSub
	h.nextSub++
	ch := make(chan Msg, subBuffer)
	h.subs[id] = ch
	// Replay the latest status snapshot (if any) FIRST, ahead of the buffered
	// transcript, so the viewer's status bar populates immediately and — crucially
	// — so the model/reasoning are known before any replayed turn boundary. The
	// web UI renders a turn's footer (model · think:level) off the latest status
	// when it replays the turn's "idle" transition; if the status arrived after
	// that transition (as it did when appended last), a refreshed view dropped the
	// model name and think level from every replayed turn's footer.
	hist := make([]Msg, 0, len(h.history)+3)
	if h.lastStatus != nil {
		hist = append(hist, *h.lastStatus)
	}
	hist = append(hist, h.history...)
	if h.lastCommands != nil {
		hist = append(hist, *h.lastCommands)
	}
	if h.lastSessions != nil {
		hist = append(hist, *h.lastSessions)
	}
	if h.lastQueue != nil {
		hist = append(hist, *h.lastQueue)
	}
	return id, ch, hist, h.state
}

// Unsubscribe removes a viewer and closes its channel.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

// State returns the current session state.
func (h *Hub) State() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Submit forwards a prompt to the owning UI. It returns false when the hub is
// view-only or not idle. The owning UI re-checks its own state, so this is a
// fast-path guard for nicer client feedback, not the authoritative gate.
func (h *Hub) Submit(text string) bool {
	h.mu.Lock()
	submit, state := h.submit, h.state
	h.mu.Unlock()
	if submit == nil || state != StateIdle {
		return false
	}
	submit(text)
	return true
}

// SubmitSide forwards a concurrent side-input (e.g. /btw) to the owning UI
// regardless of state — unlike Submit it is NOT gated on idle, because a side
// question is designed to run alongside the active turn. The owning UI decides
// what to honor (the terminal runs only /btw concurrently and ignores other
// input while busy). Returns false only when the hub is view-only.
func (h *Hub) SubmitSide(text string) bool {
	h.mu.Lock()
	submit := h.submit
	h.mu.Unlock()
	if submit == nil {
		return false
	}
	submit(text)
	return true
}

// Queue returns a snapshot of the busy-time message queue last published by the
// owning UI (nil when none). Lets a queue-capable mirror answer a pop request
// synchronously (returning the text to edit) while the mutation itself is
// applied asynchronously by the owning UI through QueueControl.
func (h *Hub) Queue() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastQueue == nil {
		return nil
	}
	return append([]string(nil), h.lastQueue.Queue...)
}

// QueueControl forwards a busy-time queue mutation to the owning UI, which owns
// the authoritative queue and republishes it via PublishQueue. action is one of
// "add" (append text), "remove" (drop index), "clear", or "pop" (remove the last
// item; text carries the viewer's in-progress draft to stash onto the front).
// Returns false when the hub is view-only (no queue controller wired).
func (h *Hub) QueueControl(action string, index int, text string) bool {
	h.mu.Lock()
	fn := h.queueCtl
	h.mu.Unlock()
	if fn == nil {
		return false
	}
	fn(action, index, text)
	return true
}

// Approve forwards an approval decision. It returns false when the hub is
// view-only or no approval is pending.
func (h *Hub) Approve(d agent.Decision) bool {
	h.mu.Lock()
	approve, state := h.approve, h.state
	h.mu.Unlock()
	if approve == nil || state != StateApproval {
		return false
	}
	approve(d)
	return true
}

// Answer fulfils a pending ask_user question with the chosen option label(s). It
// returns false when the hub is view-only or no question is pending. An empty
// slice means the user dismissed the question without choosing.
func (h *Hub) Answer(labels []string) bool {
	h.mu.Lock()
	answer, state := h.answer, h.state
	h.mu.Unlock()
	if answer == nil || state != StateQuestion {
		return false
	}
	answer(labels)
	return true
}

// Interrupt forwards a cancel request for the running turn, if any.
func (h *Hub) Interrupt() {
	h.mu.Lock()
	interrupt := h.interrupt
	h.mu.Unlock()
	if interrupt != nil {
		interrupt()
	}
}

// Switch asks the owning UI to move to another session of the same project. It
// returns false when the hub can't switch (view-only), the UI is mid-turn (only
// an idle UI can switch, mirroring the single-session terminal), or the target
// is already active. The owning UI re-checks its own state, so this is a
// fast-path guard, not the authoritative gate.
func (h *Hub) Switch(id string) bool {
	h.mu.Lock()
	switchTo, state, active := h.switchTo, h.state, h.activeID
	h.mu.Unlock()
	if switchTo == nil || state != StateIdle || id == "" || id == active {
		return false
	}
	switchTo(id)
	return true
}
