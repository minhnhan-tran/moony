package web

import (
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
)

// Register icon-font MIME so the embedded /static file server serves the vendored
// Material Symbols font with the correct Content-Type (Go's default table lacks
// .woff2 on some platforms, otherwise falling back to application/octet-stream).
func init() { _ = mime.AddExtensionType(".woff2", "font/woff2") }

// mirrorSessionID is the fixed id of the single shared session a MirrorServer
// exposes. The browser UI is session-oriented, so the mirror presents the live
// terminal conversation as one well-known session.
const mirrorSessionID = "main"

// MirrorServer serves a single shared session that mirrors the terminal UI's
// live conversation, backed by a bridge.Hub. Browser viewers follow along (with
// full history replay) and can also drive the agent: messages, approvals, and
// interrupts are forwarded to the controlling terminal. Unlike Server it never
// mints independent sessions — there is exactly one, and it is the terminal's.
type MirrorServer struct {
	hub   *bridge.Hub
	title string
}

// NewMirrorServer returns a server that mirrors the conversation carried by h.
func NewMirrorServer(h *bridge.Hub) *MirrorServer {
	return &MirrorServer{hub: h, title: "terminal session"}
}

// Handler builds the HTTP routes. They mirror Server's API so the same embedded
// browser UI works against either backend.
func (s *MirrorServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /api/sessions", s.handleList)
	mux.HandleFunc("POST /api/sessions", s.handleList) // "new" just returns the shared session
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/sessions/{id}/message", s.handleMessage)
	mux.HandleFunc("POST /api/sessions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/sessions/{id}/answer", s.handleAnswer)
	mux.HandleFunc("POST /api/sessions/{id}/interrupt", s.handleInterrupt)
	mux.HandleFunc("POST /api/sessions/{id}/queue", s.handleQueue)
	return mux
}

func (s *MirrorServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func (s *MirrorServer) info() sessionInfo {
	return sessionInfo{ID: mirrorSessionID, Title: s.title, State: s.hub.State()}
}

func (s *MirrorServer) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, []sessionInfo{s.info()})
}

func (s *MirrorServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id, ch, history, state := s.hub.Subscribe()
	defer s.hub.Unsubscribe(id)

	for _, m := range history {
		writeSSE(w, mirrorWire(m))
	}
	// Authoritative trailing state on connect (carrying CanQueue) so a viewer that
	// connects mid-turn keeps the composer enabled for type-ahead queueing, even
	// if no state transition happens to be buffered in history yet.
	writeSSE(w, wireEvent{Type: "history_end"}) // history/live boundary (client renders replayed thinking blocks collapsed)
	writeSSE(w, wireEvent{Type: "state", State: state, CanQueue: true})
	flusher.Flush()

	ka := time.NewTicker(sseKeepalive)
	defer ka.Stop()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, mirrorWire(m))
			flusher.Flush()
		case <-ka.C:
			writeSSEKeepalive(w)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *MirrorServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		http.Error(w, "missing text", http.StatusBadRequest)
		return
	}
	text := body.Text
	// /btw is a concurrent side-question: forward it even while busy so it runs
	// alongside the active turn (the terminal's remoteSubmit honors only /btw
	// while busy). Detected on the trimmed text so leading whitespace is ignored.
	if fields := strings.Fields(text); len(fields) > 0 && fields[0] == "/btw" {
		if !s.hub.SubmitSide(text) {
			http.Error(w, "the terminal is not accepting input", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// While the terminal is busy, enqueue type-ahead into its FIFO queue (drained
	// after the turn) instead of rejecting it — the web mirror of the terminal's
	// Enter-while-busy queueing. Submit() is the idle fast-path; if it races the
	// turn to busy and returns false, fall back to enqueueing so nothing is lost.
	// A view-only hub (no controller wired) accepts neither, so report 409.
	if s.hub.State() != bridge.StateIdle {
		if !s.hub.QueueControl("add", 0, text) {
			http.Error(w, "the terminal is not accepting input", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !s.hub.Submit(text) && !s.hub.QueueControl("add", 0, text) {
		http.Error(w, "the terminal is busy or not accepting input", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleQueue manages the terminal's busy-time message queue from the browser.
// The terminal owns the authoritative queue (m.queued); mutations are forwarded
// via the hub and republished to every viewer. "pop" answers synchronously with
// the last queued text (read from the hub's published snapshot) so the browser
// can load it into the composer for editing, mirroring the web-native session.
func (s *MirrorServer) handleQueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Index  int    `json:"index"`
		Stash  string `json:"stash"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch body.Action {
	case "clear":
		s.hub.QueueControl("clear", 0, "")
		writeJSON(w, map[string]any{"ok": true})
	case "remove":
		s.hub.QueueControl("remove", body.Index, "")
		writeJSON(w, map[string]any{"ok": true})
	case "pop":
		q := s.hub.Queue()
		text := ""
		if n := len(q); n > 0 {
			text = q[n-1]
		}
		s.hub.QueueControl("pop", 0, body.Stash)
		writeJSON(w, map[string]any{"text": text, "ok": text != ""})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (s *MirrorServer) handleApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision string `json:"decision"`
		Feedback string `json:"feedback"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var d agent.Decision
	switch body.Decision {
	case "approve":
		d = agent.Approve
	case "always":
		d = agent.ApproveAlways
	default:
		d = agent.Decision{Type: agent.DenyType, Feedback: body.Feedback}
	}
	if !s.hub.Approve(d) {
		http.Error(w, "no approval pending", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *MirrorServer) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	s.hub.Interrupt()
	w.WriteHeader(http.StatusNoContent)
}

func (s *MirrorServer) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Labels []string `json:"labels"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !s.hub.Answer(body.Labels) {
		http.Error(w, "no question pending", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mirrorWire converts a bridge.Msg into the browser wire form, reusing toWire
// for passthrough agent events and mapping the synthetic kinds onto the same
// flattened shape Server already emits.
func mirrorWire(m bridge.Msg) wireEvent {
	switch m.Kind {
	case bridge.KindAgent:
		w := toWire(m.Event)
		w.Seq = m.Seq
		return w
	case bridge.KindUser:
		return wireEvent{Seq: m.Seq, Type: "user", Text: m.Text}
	case bridge.KindState:
		// CanQueue marks the mirror as queue-capable so the browser keeps the
		// composer enabled while busy (type-ahead enqueues; see handleMessage).
		return wireEvent{Seq: m.Seq, Type: "state", State: m.State, CanQueue: true}
	case bridge.KindError:
		return wireEvent{Seq: m.Seq, Type: "error", Text: m.Text}
	case bridge.KindReset:
		return wireEvent{Seq: m.Seq, Type: "reset"}
	case bridge.KindQueue:
		return wireEvent{Seq: m.Seq, Type: "queue", Queue: m.Queue}
	case bridge.KindStatus:
		w := wireEvent{Seq: m.Seq, Type: "status"}
		if m.Status != nil {
			s := m.Status
			w.Status = &statusWire{
				Version: s.Version, Model: s.Model, Provider: s.Provider, LLMProvider: s.LLMProvider, Reasoning: s.Reasoning,
				WorkingDir: s.WorkingDir, Approval: s.Approval, Sandbox: s.Sandbox,
				GitBranch: s.GitBranch, GitDirty: s.GitDirty, GitAhead: s.GitAhead, GitBehind: s.GitBehind, GitUpstream: s.GitUpstream, Host: s.Host, Relay: s.Relay,
				TokensIn: s.TokensIn, TokensOut: s.TokensOut, CostUSD: s.CostUSD,
				QuotaSpend: s.QuotaSpend, QuotaBudget: s.QuotaBudget, QuotaKnown: s.QuotaKnown, QuotaName: s.QuotaName,
				QuotaTokens: s.QuotaTokens, QuotaTokensMax: s.QuotaTokensMax, QuotaTokenRate: s.QuotaTokenRate, QuotaTokenRatePeriod: s.QuotaTokenRatePeriod,
				ContextPercent: s.ContextPercent, ActiveTool: s.ActiveTool, ToolsInFlight: s.ToolsInFlight,
				Agent: s.Agent,
			}
			for _, mc := range s.MCP {
				w.Status.MCP = append(w.Status.MCP, mcpWire{Name: mc.Name, Tools: mc.Tools, Resources: mc.Resources, Prompts: mc.Prompts})
			}
			for _, f := range s.Files {
				w.Status.Files = append(w.Status.Files, fileWire{Path: f.Path, Created: f.Created})
			}
			// Per-model reasoning levels for the web picker (the bridge status only
			// carries the model name, so derive them here — same as statusEvent).
			for _, lvl := range config.ReasoningLevelsFor(s.Model) {
				w.Status.ReasoningLevels = append(w.Status.ReasoningLevels, levelWire{ID: lvl, Desc: config.ReasoningDescription(lvl)})
			}
		}
		return w
	case bridge.KindCommands:
		cmds := make([]cmdWire, 0, len(m.Commands))
		for _, c := range m.Commands {
			cmds = append(cmds, cmdWire{Name: c.Name, Args: c.Args, Desc: c.Desc})
		}
		mentions := make([]mentionWire, 0, len(m.Mentions))
		for _, mn := range m.Mentions {
			mentions = append(mentions, mentionWire{Name: mn.Name, Kind: mn.Kind, Desc: mn.Desc})
		}
		return wireEvent{Seq: m.Seq, Type: "commands", Commands: cmds, Mentions: mentions}
	case bridge.KindMenu:
		w := wireEvent{Seq: m.Seq, Type: "menu"}
		if m.Menu != nil {
			items := make([]menuItemWire, 0, len(m.Menu.Items))
			for _, it := range m.Menu.Items {
				items = append(items, menuItemWire{Label: it.Label, Desc: it.Desc, Value: it.Value, Current: it.Current})
			}
			w.Menu = &menuWire{Title: m.Menu.Title, Command: m.Menu.Command, Items: items}
		}
		return w
	case bridge.KindSessions:
		items := make([]sessionWireItem, 0, len(m.Sessions))
		for _, s := range m.Sessions {
			items = append(items, sessionWireItem{ID: s.ID, Title: s.Title, Detail: s.Detail, Active: s.Active})
		}
		return wireEvent{Seq: m.Seq, Type: "sessions", Sessions: items}
	}
	return wireEvent{Seq: m.Seq, Type: "info"}
}
