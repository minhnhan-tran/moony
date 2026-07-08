package web

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/mcp"
)

// session state values.
const (
	stateIdle     = "idle"
	stateBusy     = "busy"
	stateApproval = "awaiting_approval"
	stateQuestion = "awaiting_question"
	subBufferSize = 1024 // per-viewer event buffer; full → that viewer drops live events (recoverable via reload)
)

// session is one conversation: an agent plus a broadcast hub. Every emitted
// event is appended to history (so a newly-connected, possibly remote, viewer
// can replay the whole conversation) and fanned out to all live subscribers.
type session struct {
	id      string
	title   string
	agent   *agent.Agent
	created time.Time

	mu      sync.Mutex
	seq     int
	history []wireEvent
	subs    map[int]chan wireEvent
	nextSub int
	state   string
	cancel  context.CancelFunc
	reply   chan agent.Decision // set while an approval is pending
	replyQ  chan []string       // set while an ask_user question is pending
	replyQG chan [][]string     // set while a grouped ask_user (questions[]) is pending

	// queued holds raw user inputs received while a turn was running. They are
	// drained one at a time (FIFO) after each turn returns to idle, via the
	// onIdle hook, mirroring the terminal's message queue. Guarded by mu.
	queued []queuedMsg

	// onIdle, if set, runs after a turn completes and the session returns to
	// idle (the server sets it to drain the message queue).
	onIdle func()

	// btwBusy guards a /btw side-question so only one runs at a time per session
	// (mirroring the terminal). A side-question is read-only and runs CONCURRENTLY
	// with any active turn, so it does NOT change s.state (the composer stays
	// free). Guarded by mu.
	btwBusy bool

	// usage accumulated across this session's turns (from TurnDone events).
	tokensIn             int
	tokensOut            int
	cachedIn             int     // of tokensIn, the portion read from the prompt cache (cheap)
	cacheCreateIn        int     // of tokensIn, the portion that wrote new cache entries
	lastTurnIn           int     // input tokens of the most recent turn (for context %)
	quotaSpend           float64 // authoritative provider 24h usage, when fetched
	quotaBudget          float64
	quotaKnown           bool
	quotaName            string // label for the quota source: a provider project name, or "personal"
	quotaTokens          int    // tokens used in the quota period (24h), when fetched
	quotaTokensMax       int    // per-day token cap, when the provider budget carries one (often 0)
	quotaTokenRate       int    // token rate limit (tokens per quotaTokenRatePeriod), e.g. 1e9
	quotaTokenRatePeriod string // the rate-limit window, e.g. "4m"

	// onTurnDone, if set, runs after each completed turn (server uses it to
	// re-emit an updated status snapshot with the new usage numbers).
	onTurnDone func()

	// refreshQuota, if set, clears the stale quota fields and kicks off a
	// fresh background fetch for the current provider. Call it whenever the
	// provider changes so the old provider's quota doesn't bleed into the new
	// provider's status bar.
	refreshQuota func()

	// mcp holds this session's connected MCP servers (connected lazily after
	// open; one manager per session). Guarded by mu. mcpMu serializes the
	// connect/reload operations (which can block on subprocess spawn) so two
	// don't race on the manager.
	mcp   *mcp.Manager
	mcpMu sync.Mutex

	// mcpReady is closed once this session's initial MCP connect attempt
	// completes (tools registered + system prompt rebuilt). The first turn waits
	// on it (bounded) so a freshly opened web/daemon session sees the MCP tools
	// and their system-prompt guidance before it runs — matching the terminal,
	// which connects MCP before accepting any input. Later turns find it already
	// closed and don't wait.
	mcpReady     chan struct{}
	mcpReadyOnce sync.Once

	// lspMu serializes /lsp reloads (which restart language servers, blocking on
	// subprocess spawn) so two callers don't race the agent's LSP manager.
	lspMu sync.Mutex
}

func newSession(id, title string, ag *agent.Agent) *session {
	return &session{
		id:       id,
		title:    title,
		agent:    ag,
		created:  time.Now(),
		subs:     map[int]chan wireEvent{},
		state:    stateIdle,
		mcpReady: make(chan struct{}),
	}
}

// snapshot returns lightweight metadata for the sessions list.
func (s *session) snapshot() sessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionInfo{ID: s.id, Title: s.title, State: s.state}
}

// seedHistory pre-loads replayed events (from a saved session's messages) into
// history so a connecting viewer sees the prior conversation. No subscribers
// exist yet at open time, so it only records history.
func (s *session) seedHistory(events []wireEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range events {
		s.seq++
		w.Seq = s.seq
		s.history = append(s.history, w)
	}
}

// publish appends a fully-formed wire event to history and fans it out. Caller
// must hold s.mu.
func (s *session) publishLocked(w wireEvent) {
	s.seq++
	w.Seq = s.seq
	s.history = append(s.history, w)
	s.fanoutLocked(w)
}

// publishTransientLocked fans out an ephemeral event WITHOUT recording it in
// history, so a viewer that connects or switches to this session doesn't replay
// it. Used for clickable pickers ("menu"): they're momentary UI from a slash
// command, and a stale one re-appearing on a session switch is a bug (mirrors
// the bridge hub's transient PublishMenu). Caller must hold s.mu.
func (s *session) publishTransientLocked(w wireEvent) {
	s.seq++
	w.Seq = s.seq
	s.fanoutLocked(w)
}

// fanoutLocked sends w to every subscriber. A viewer whose buffer is full can't
// keep up; rather than SILENTLY dropping the event (which leaves a permanent gap
// in that viewer's transcript — the client has no seq-gap detection, and a silent
// drop isn't a connection error, so EventSource never reconnects), we drop the
// SUBSCRIBER: close its channel and remove it. handleEvents then returns, the SSE
// closes, and the browser's EventSource auto-reconnects → a fresh subscribe
// replays the full history, so the view self-heals instead of corrupting. Caller
// must hold s.mu. (unsubscribe's presence check makes the eventual defer a no-op.)
func (s *session) fanoutLocked(w wireEvent) {
	for id, ch := range s.subs {
		select {
		case ch <- w:
		default:
			delete(s.subs, id)
			close(ch)
		}
	}
}

// isCoalescibleDelta reports whether a wire event is a streaming text/thinking
// delta whose history entries should be merged into one accumulating event per
// run — so a viewer switching to a mid-answer session replays a few small events
// instead of thousands of per-token ones (which would slow the snapshot write and
// overflow live subscribers' buffers). The live fanout stays per-delta for smooth
// streaming; only the recorded history is coalesced.
func isCoalescibleDelta(t string) bool { return t == "text" || t == "thinking" }

// recordDeltaLocked fans out a streaming delta live (incremental) but merges it
// into the trailing history entry of the same run (see isCoalescibleDelta). A
// non-delta event between runs breaks the run, matching how the client closes and
// reopens the assistant/thinking bubble, so a coalesced run replays identically
// to the per-token stream. Caller must hold s.mu.
func (s *session) recordDeltaLocked(w wireEvent) {
	s.seq++
	w.Seq = s.seq
	s.fanoutLocked(w) // current viewers get the incremental delta
	if n := len(s.history); n > 0 && s.history[n-1].Type == w.Type {
		s.history[n-1].Text += w.Text // coalesce into the current run's entry
		return
	}
	s.history = append(s.history, w) // start a new run
}

// emit is the agent's event sink. It runs on the agent's goroutine(s) — possibly
// concurrently during parallel tool execution — so it locks.
func (s *session) emit(ev agent.Event) {
	w := toWire(ev)
	s.mu.Lock()
	switch e := ev.(type) {
	case agent.ApprovalRequest:
		s.reply = e.Reply
		s.state = stateApproval
		s.publishLocked(stateWire(stateApproval))
	case agent.QuestionRequest:
		s.replyQ = e.Reply
		s.state = stateQuestion
		s.publishLocked(stateWire(stateQuestion))
	case agent.QuestionGroupRequest:
		s.replyQG = e.Reply
		s.state = stateQuestion
		s.publishLocked(stateWire(stateQuestion))
	case agent.TurnDone:
		// Accumulate the whole turn's usage (every model call), not just the final
		// call, so the session totals + cost + prompt-cache split reflect reality.
		// Some TurnDone paths (e.g. goal-budget stop) don't set TurnUsage — fall
		// back to the final call's usage there.
		u := e.TurnUsage
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			u = e.Usage
		}
		s.tokensIn += u.InputTokens
		s.tokensOut += u.OutputTokens
		s.cachedIn += u.CacheReadInputTokens
		s.cacheCreateIn += u.CacheCreationInputTokens
		if e.Usage.InputTokens > 0 {
			s.lastTurnIn = e.Usage.InputTokens // context fill = the last call's prompt size
		}
	case agent.TitleUpdate:
		s.title = e.Title // keep the live session's name in sync with the persisted one
	}
	if isCoalescibleDelta(w.Type) {
		s.recordDeltaLocked(w) // streaming delta: fan out live, coalesce in history
	} else {
		s.publishLocked(w)
	}
	hook := s.onTurnDone
	s.mu.Unlock()

	if _, ok := ev.(agent.TurnDone); ok && hook != nil {
		hook() // re-emit status with the updated usage (locks internally)
	}
}

// usageSnapshot returns the session's accumulated usage for the status bar.
func (s *session) usageSnapshot() (in, out, lastIn int, spend, budget float64, quotaKnown bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokensIn, s.tokensOut, s.lastTurnIn, s.quotaSpend, s.quotaBudget, s.quotaKnown
}

// cacheSnapshot returns the session's prompt-cache token totals: tokens read from
// the cache (billed cheap when the route discounts them) and tokens that wrote
// new cache entries. Both are subsets of the accumulated input tokens.
func (s *session) cacheSnapshot() (read, create int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cachedIn, s.cacheCreateIn
}

// setQuota records an authoritative usage snapshot. tokens is the 24h
// tokens used; tokensMax is the per-day token cap (0 when the budget has none);
// rate/ratePeriod are the token rate limit and its window (e.g. 1e9 per "4m").
func (s *session) setQuota(spend, budget float64, name string, tokens, tokensMax, rate int, ratePeriod string) {
	s.mu.Lock()
	s.quotaSpend, s.quotaBudget, s.quotaKnown, s.quotaName = spend, budget, true, name
	s.quotaTokens, s.quotaTokensMax = tokens, tokensMax
	s.quotaTokenRate, s.quotaTokenRatePeriod = rate, ratePeriod
	s.mu.Unlock()
}

// clearQuota resets all quota fields so the status bar shows no stale data.
// Call this whenever the provider changes — the old provider's quota belongs to
// a different endpoint/account and must not bleed into the new provider's view.
// A fresh fetch is triggered in the background after clearing.
func (s *session) clearQuota() {
	s.mu.Lock()
	s.quotaSpend, s.quotaBudget, s.quotaKnown, s.quotaName = 0, 0, false, ""
	s.quotaTokens, s.quotaTokensMax = 0, 0
	s.quotaTokenRate, s.quotaTokenRatePeriod = 0, ""
	s.mu.Unlock()
}

// quotaTokenSnapshot returns the 24h tokens used, the per-day token cap, and the
// token rate limit + its window (all zero/"" until a quota is fetched).
func (s *session) quotaTokenSnapshot() (tokens, tokensMax, rate int, ratePeriod string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaTokens, s.quotaTokensMax, s.quotaTokenRate, s.quotaTokenRatePeriod
}

// quotaNameSnapshot returns the quota source label ("personal" / a provider
// project name), or "" if not yet fetched.
func (s *session) quotaNameSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaName
}

// publishEvent appends a fully-formed wire event to history and fans it out to
// subscribers. Used by the server to push status snapshots between agent events.
func (s *session) publishEvent(w wireEvent) {
	s.mu.Lock()
	s.publishLocked(w)
	s.mu.Unlock()
}

// publishTransient fans out an ephemeral event without recording it in history
// (see publishTransientLocked) — for clickable pickers ("menu") that must not be
// replayed on connect / session switch.
func (s *session) publishTransient(w wireEvent) {
	s.mu.Lock()
	s.publishTransientLocked(w)
	s.mu.Unlock()
}

// clearHistory drops the recorded transcript so a /clear or /reset takes effect
// for newly-connecting viewers too. Live subscribers get a separate reset event.
func (s *session) clearHistory() {
	s.mu.Lock()
	s.history = nil
	s.mu.Unlock()
}

func (s *session) mcpManager() *mcp.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mcp
}

func (s *session) setMCP(m *mcp.Manager) {
	s.mu.Lock()
	s.mcp = m
	s.mu.Unlock()
}

// markMCPReady signals that this session's initial MCP connect attempt has
// finished (tools registered, system prompt rebuilt). Idempotent — only the
// first call (the open-time connect) has an effect; later reloads are no-ops.
func (s *session) markMCPReady() { s.mcpReadyOnce.Do(func() { close(s.mcpReady) }) }

// waitMCPReady blocks until the session's initial MCP connect completes or d
// elapses, whichever comes first. d <= 0 means don't wait.
func (s *session) waitMCPReady(d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-s.mcpReady:
	case <-time.After(d):
	}
}

// mcpReadyTimeout bounds how long a session's first turn waits for MCP servers
// to finish connecting. Default 10s; override with MOONY_MCP_READY_WAIT (a Go
// duration such as "20s", or "0" to disable waiting).
func mcpReadyTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("MOONY_MCP_READY_WAIT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return 10 * time.Second
}

// subscribe registers a viewer, returning its id, channel, and a snapshot of the
// history so far. Registration and snapshot happen under one lock so no event is
// missed or duplicated across the handoff.
func (s *session) subscribe() (int, chan wireEvent, []wireEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan wireEvent, subBufferSize)
	s.subs[id] = ch
	hist := make([]wireEvent, len(s.history))
	copy(hist, s.history)
	return id, ch, hist
}

func (s *session) unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(ch)
	}
}

// start begins a turn for the given prompt. It returns false if a turn is
// already running (one turn at a time per session).
func (s *session) start(prompt string, blocks []llm.ContentBlock, atts []attachWire) bool {
	s.mu.Lock()
	if s.state != stateIdle {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.state = stateBusy
	s.publishLocked(wireEvent{Type: "user", Text: prompt, Attachments: atts})
	s.publishLocked(stateWire(stateBusy))
	s.mu.Unlock()

	go func() {
		// Give a freshly opened session's MCP servers a bounded chance to finish
		// connecting (and the system prompt to be rebuilt) before the first turn,
		// so MCP tools and their system-prompt guidance are present — mirroring
		// the terminal, which connects MCP before it accepts input. The channel is
		// closed after the initial connect, so subsequent turns return instantly.
		s.waitMCPReady(mcpReadyTimeout())
		err := s.agent.RunWith(ctx, prompt, blocks, s.emit)
		s.mu.Lock()
		s.cancel = nil
		s.reply = nil
		s.replyQ = nil
		s.replyQG = nil
		s.state = stateIdle
		if err != nil && ctx.Err() == nil {
			s.publishLocked(wireEvent{Type: "error", Text: config.TurnErrorNote(err)})
		}
		s.publishLocked(stateWire(stateIdle))
		clean := err == nil && ctx.Err() == nil
		onIdle := s.onIdle
		s.mu.Unlock()

		// Drain the next queued message (FIFO) on a clean completion. Skipped on
		// cancel/error so the user decides what to do with what's left queued.
		if clean && onIdle != nil {
			onIdle()
		}
	}()
	return true
}

// approve answers a pending approval. Returns false when nothing is pending.
func (s *session) approve(d agent.Decision) bool {
	s.mu.Lock()
	reply := s.reply
	if reply == nil {
		s.mu.Unlock()
		return false
	}
	s.reply = nil
	s.state = stateBusy
	s.publishLocked(stateWire(stateBusy))
	s.mu.Unlock()

	reply <- d // unblocks the agent's requestApproval
	return true
}

// answer fulfils a pending single ask_user question with the chosen option
// label(s) (empty = dismissed). Returns false when no single question is pending.
func (s *session) answer(labels []string) bool {
	return s.answerAny(labels, nil)
}

// answerAny fulfils whichever kind of ask_user prompt is pending: a grouped panel
// (groups, one label-slice per question — the questions[] tabs UI) or a single
// question (labels). It prefers the group when one is pending. Returns false when
// nothing is pending. A nil groups with a group pending counts as dismiss-all.
func (s *session) answerAny(labels []string, groups [][]string) bool {
	s.mu.Lock()
	if replyG := s.replyQG; replyG != nil {
		s.replyQG = nil
		s.state = stateBusy
		s.publishLocked(stateWire(stateBusy))
		s.mu.Unlock()
		if groups == nil {
			groups = [][]string{}
		}
		replyG <- groups // unblocks the agent's requestQuestionGroup
		return true
	}
	reply := s.replyQ
	if reply == nil {
		s.mu.Unlock()
		return false
	}
	s.replyQ = nil
	s.state = stateBusy
	s.publishLocked(stateWire(stateBusy))
	s.mu.Unlock()

	reply <- labels // unblocks the agent's requestQuestion
	return true
}

// interrupt cancels the running turn, if any.
func (s *session) interrupt() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// askSide runs a /btw side-question: a one-shot, read-only LLM call (Agent.AskSide)
// that uses the conversation as context but doesn't touch history or turn state,
// so it runs CONCURRENTLY with any active turn — the composer stays usable. Only
// one side-question runs at a time per session (s.btwBusy), mirroring the
// terminal. The question and answer are published as info lines (there's no
// "side" concept on the web wire) prefixed with "↳ /btw", matching the live
// mirror. A blank question prints usage. Returns immediately; the answer arrives
// asynchronously on the goroutine.
func (s *session) askSide(question string) {
	q := strings.TrimSpace(question)
	if q == "" {
		s.publishEvent(wireEvent{Type: "info", Text: "/btw <question> — ask a quick side question without polluting the main thread"})
		return
	}
	s.mu.Lock()
	if s.btwBusy {
		s.mu.Unlock()
		s.publishEvent(wireEvent{Type: "info", Text: "A /btw side-question is already running — try again in a moment."})
		return
	}
	s.btwBusy = true
	s.mu.Unlock()

	s.publishEvent(wireEvent{Type: "info", Text: "↳ /btw " + q}) // echo the side question
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ans, err := s.agent.AskSide(ctx, q)
		s.mu.Lock()
		s.btwBusy = false
		s.mu.Unlock()
		if err != nil {
			s.publishEvent(wireEvent{Type: "info", Text: "/btw failed: " + err.Error()})
			return
		}
		s.publishEvent(wireEvent{Type: "info", Text: "↳ /btw: " + ans})
	}()
}

// stateWire builds a "state" wire event tagged with CanQueue=true, marking this
// (web-native) session as one that supports queuing messages typed while busy.
func stateWire(state string) wireEvent {
	return wireEvent{Type: "state", State: state, CanQueue: true}
}

// queuedMsg is a user message deferred while a turn was running: its text plus
// any resolved attachment content blocks (delivered to the model on drain) and
// display chips (for the browser). Carrying blocks here is what keeps pasted
// images/docs from being dropped when a message is sent mid-turn.
type queuedMsg struct {
	text   string
	blocks []llm.ContentBlock
	wires  []attachWire
}

// publishQueueLocked fans out the current queue contents as a "queue" event.
// Caller must hold s.mu.
func (s *session) publishQueueLocked() {
	s.publishLocked(wireEvent{Type: "queue", Queue: s.queuedTextsLocked()})
}

// queuedTextsLocked returns the queued message texts (for the queue preview).
// Caller must hold s.mu.
func (s *session) queuedTextsLocked() []string {
	q := make([]string, len(s.queued))
	for i := range s.queued {
		q[i] = s.queued[i].text
	}
	return q
}

// enqueueIfBusy appends text to the message queue iff a turn is currently
// running, returning true when it was queued. When the session is idle it
// returns false and queues nothing, so the caller can dispatch the message
// immediately (this check is atomic with the running-turn state, closing the
// race where a turn finishes between the caller's state check and the enqueue).
func (s *session) enqueueIfBusy(text string, blocks []llm.ContentBlock, wires []attachWire) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateIdle {
		return false
	}
	s.queued = append(s.queued, queuedMsg{text: text, blocks: blocks, wires: wires})
	s.publishQueueLocked()
	return true
}

// dequeueIfIdle pops the front queued message iff the session is idle, returning
// it and true. Returns ("", false) when busy or empty — so draining stops while
// a turn is running and resumes (via onIdle) when it completes.
func (s *session) dequeueIfIdle() (queuedMsg, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateIdle || len(s.queued) == 0 {
		return queuedMsg{}, false
	}
	m := s.queued[0]
	s.queued = s.queued[1:]
	s.publishQueueLocked()
	return m, true
}

// queuedList returns a copy of the pending queued message texts.
func (s *session) queuedList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queuedTextsLocked()
}

// clearQueue empties the queue, returning how many messages were dropped.
func (s *session) clearQueue() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.queued)
	if n > 0 {
		s.queued = nil
		s.publishQueueLocked()
	}
	return n
}

// popQueued removes the most recent queued message and returns it (for Esc-edit
// in the composer). When stash is non-empty it is pushed onto the FRONT of the
// queue first, preserving an in-progress draft the user was composing — matching
// the terminal's Esc-pop behavior. Returns ("", false) when the queue is empty.
func (s *session) popQueued(stash string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queued) == 0 {
		return "", false
	}
	if strings.TrimSpace(stash) != "" {
		s.queued = append([]queuedMsg{{text: stash}}, s.queued...)
	}
	last := s.queued[len(s.queued)-1]
	s.queued = s.queued[:len(s.queued)-1]
	s.publishQueueLocked()
	return last.text, true
}

// removeQueuedAt drops the queued message at index i (0-based), returning true
// when removed. Out-of-range indices are a no-op.
func (s *session) removeQueuedAt(i int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.queued) {
		return false
	}
	s.queued = append(s.queued[:i], s.queued[i+1:]...)
	s.publishQueueLocked()
	return true
}
