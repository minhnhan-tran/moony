package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/bridge"
)

func TestMirrorServesIndex(t *testing.T) {
	h := bridge.New()
	rec := postJSON(t, NewMirrorServer(h).Handler(), "GET", "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	// The UI is styled with locally-vendored Tailwind, renders answers as
	// markdown via locally-vendored marked, and draws icons with Apple's SF
	// Symbols web component. Non-Apple CDNs must NOT be referenced (the Apple
	// network blocks them).
	for _, want := range []string{"/static/tailwind.css", "/static/marked.min.js", "/static/app.css", "/static/app-state.js", "/static/app-core.js", `id="transcript"`, `id="stats"`, `id="hdrMeta"`, "sf-saas.cdn-apple.com", "<sf-symbol", "/static/icon/favicon"} {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
	for _, bad := range []string{"cdn.tailwindcss.com", "cdn.jsdelivr.net"} {
		if strings.Contains(body, bad) {
			t.Errorf("index page must not reference blocked CDN %q", bad)
		}
	}
}

func TestMirrorServesVendoredAssets(t *testing.T) {
	srv := NewMirrorServer(bridge.New()).Handler()
	for _, tc := range []struct{ path, want string }{
		{"/static/tailwind.css", ".md"},
		{"/static/marked.min.js", "marked"},
		// The UI's own CSS/JS, split out of index.html into dedicated files.
		{"/static/app.css", ".user-body"},
		{"/static/app-state.js", "confirmBox"},
		{"/static/app-core.js", "function openManage"},
	} {
		rec := postJSON(t, srv, "GET", tc.path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s code = %d, want 200", tc.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s body missing %q", tc.path, tc.want)
		}
	}
}

func TestMirrorServesIcons(t *testing.T) {
	srv := NewMirrorServer(bridge.New()).Handler()
	for _, p := range []string{"/static/icon/favicon.ico", "/static/icon/favicon-32.png", "/static/icon/apple-touch-icon.png"} {
		rec := postJSON(t, srv, "GET", p, "")
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: code=%d len=%d, want 200 with content", p, rec.Code, rec.Body.Len())
		}
	}
}

func TestMirrorListsSingleSession(t *testing.T) {
	h := bridge.New()
	rec := postJSON(t, NewMirrorServer(h).Handler(), "GET", "/api/sessions", "")
	var infos []sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(infos) != 1 || infos[0].ID != mirrorSessionID {
		t.Fatalf("sessions = %+v, want one with id %q", infos, mirrorSessionID)
	}
	if infos[0].State != bridge.StateIdle {
		t.Errorf("state = %q, want idle", infos[0].State)
	}
}

func TestMirrorMessageForwardsToController(t *testing.T) {
	h := bridge.New()
	var got string
	h.SetController(func(s string) { got = s }, nil, nil, nil)
	srv := NewMirrorServer(h).Handler()

	rec := postJSON(t, srv, "POST", "/api/sessions/main/message", `{"text":"hello"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
	if got != "hello" {
		t.Errorf("controller got %q, want hello", got)
	}

	// Empty text is a bad request.
	if r := postJSON(t, srv, "POST", "/api/sessions/main/message", `{"text":""}`); r.Code != http.StatusBadRequest {
		t.Errorf("empty text code = %d, want 400", r.Code)
	}
}

func TestMirrorMessageRejectedWhenViewOnly(t *testing.T) {
	h := bridge.New() // no controller => view-only
	srv := NewMirrorServer(h).Handler()
	rec := postJSON(t, srv, "POST", "/api/sessions/main/message", `{"text":"hi"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("view-only message code = %d, want 409", rec.Code)
	}
}

// TestMirrorEnqueuesWhileBusy: a plain message sent while the terminal is busy
// is forwarded to the queue (QueueControl "add"), not the idle Submit path, so
// the browser's type-ahead is held for after the turn instead of being rejected.
func TestMirrorEnqueuesWhileBusy(t *testing.T) {
	h := bridge.New()
	var submitted string
	var qAction, qText string
	h.SetController(func(s string) { submitted = s }, nil, nil, nil)
	h.SetQueueController(func(action string, _ int, text string) { qAction, qText = action, text })
	h.PublishState(bridge.StateBusy)
	srv := NewMirrorServer(h).Handler()

	rec := postJSON(t, srv, "POST", "/api/sessions/main/message", `{"text":"later"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("busy message code = %d, want 202", rec.Code)
	}
	if qAction != "add" || qText != "later" {
		t.Errorf("queue ctl = (%q,%q), want (add,later)", qAction, qText)
	}
	if submitted != "" {
		t.Errorf("busy message should not Submit, got %q", submitted)
	}
}

// TestMirrorBtwForwardsWhileBusy: a /btw side-question is forwarded to the owning
// UI even while busy (SubmitSide), never queued, so it can run concurrently.
func TestMirrorBtwForwardsWhileBusy(t *testing.T) {
	h := bridge.New()
	var submitted string
	queued := false
	h.SetController(func(s string) { submitted = s }, nil, nil, nil)
	h.SetQueueController(func(string, int, string) { queued = true })
	h.PublishState(bridge.StateBusy)
	srv := NewMirrorServer(h).Handler()

	rec := postJSON(t, srv, "POST", "/api/sessions/main/message", `{"text":"/btw what time is it"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("busy /btw code = %d, want 202", rec.Code)
	}
	if submitted != "/btw what time is it" {
		t.Errorf("submit got %q, want the /btw text", submitted)
	}
	if queued {
		t.Error("/btw must run concurrently, not be queued")
	}
}

// TestMirrorQueueActions: pop/remove/clear are forwarded to the owning UI, and
// pop answers synchronously with the last queued text (from the hub snapshot).
func TestMirrorQueueActions(t *testing.T) {
	h := bridge.New()
	var gotAction, gotText string
	var gotIndex int
	h.SetQueueController(func(action string, index int, text string) {
		gotAction, gotIndex, gotText = action, index, text
	})
	h.PublishQueue([]string{"first", "second"})
	srv := NewMirrorServer(h).Handler()

	rec := postJSON(t, srv, "POST", "/api/sessions/main/queue", `{"action":"pop","stash":"draft"}`)
	var pop struct {
		Text string `json:"text"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pop); err != nil {
		t.Fatalf("decode pop: %v (%s)", err, rec.Body)
	}
	if pop.Text != "second" || !pop.OK {
		t.Errorf("pop = %+v, want {second true}", pop)
	}
	if gotAction != "pop" || gotText != "draft" {
		t.Errorf("pop ctl = (%q,%q), want (pop,draft)", gotAction, gotText)
	}

	postJSON(t, srv, "POST", "/api/sessions/main/queue", `{"action":"remove","index":1}`)
	if gotAction != "remove" || gotIndex != 1 {
		t.Errorf("remove ctl = (%q,%d), want (remove,1)", gotAction, gotIndex)
	}
	postJSON(t, srv, "POST", "/api/sessions/main/queue", `{"action":"clear"}`)
	if gotAction != "clear" {
		t.Errorf("clear ctl action = %q, want clear", gotAction)
	}
}

// TestMirrorWireCanQueue: every mirrored state event carries CanQueue so the
// browser keeps the composer enabled while busy; queue events map to "queue".
func TestMirrorWireCanQueue(t *testing.T) {
	st := mirrorWire(bridge.Msg{Kind: bridge.KindState, State: bridge.StateBusy})
	if st.Type != "state" || st.State != bridge.StateBusy || !st.CanQueue {
		t.Errorf("state wire = %+v, want type=state state=busy canQueue=true", st)
	}
	q := mirrorWire(bridge.Msg{Kind: bridge.KindQueue, Queue: []string{"a", "b"}})
	if q.Type != "queue" || len(q.Queue) != 2 {
		t.Errorf("queue wire = %+v, want type=queue with 2 items", q)
	}
}

func TestMirrorApproveForwarding(t *testing.T) {
	h := bridge.New()
	var got agent.Decision = -1
	h.SetController(nil, func(d agent.Decision) { got = d }, nil, nil)
	srv := NewMirrorServer(h).Handler()

	// No approval pending yet -> 409.
	if r := postJSON(t, srv, "POST", "/api/sessions/main/approve", `{"decision":"approve"}`); r.Code != http.StatusConflict {
		t.Fatalf("approve with nothing pending = %d, want 409", r.Code)
	}

	// Move to awaiting-approval, then approve.
	h.PublishState(bridge.StateApproval)
	rec := postJSON(t, srv, "POST", "/api/sessions/main/approve", `{"decision":"always"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("approve code = %d, want 204", rec.Code)
	}
	if got != agent.ApproveAlways {
		t.Errorf("decision = %v, want ApproveAlways", got)
	}
}

func TestMirrorInterrupt(t *testing.T) {
	h := bridge.New()
	var interrupted bool
	h.SetController(nil, nil, func() { interrupted = true }, nil)
	rec := postJSON(t, NewMirrorServer(h).Handler(), "POST", "/api/sessions/main/interrupt", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("interrupt code = %d, want 204", rec.Code)
	}
	if !interrupted {
		t.Error("interrupt not forwarded")
	}
}

func TestMirrorSSEReplaysHistory(t *testing.T) {
	h := bridge.New()
	h.PublishUser("question")
	h.PublishEvent(agent.TextDelta{Text: "answer"})
	srv := NewMirrorServer(h).Handler()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/main/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { srv.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "data: ") || !strings.Contains(body, "question") || !strings.Contains(body, "answer") {
		t.Errorf("SSE body missing replayed events:\n%s", body)
	}
}

func TestMirrorWireConversions(t *testing.T) {
	cases := []struct {
		m   bridge.Msg
		typ string
	}{
		{bridge.Msg{Kind: bridge.KindUser, Text: "u"}, "user"},
		{bridge.Msg{Kind: bridge.KindState, State: bridge.StateBusy}, "state"},
		{bridge.Msg{Kind: bridge.KindError, Text: "e"}, "error"},
		{bridge.Msg{Kind: bridge.KindReset}, "reset"},
		{bridge.Msg{Kind: bridge.KindAgent, Event: agent.TextDelta{Text: "hi"}}, "text"},
	}
	for _, c := range cases {
		if w := mirrorWire(c.m); w.Type != c.typ {
			t.Errorf("mirrorWire(%+v).Type = %q, want %q", c.m, w.Type, c.typ)
		}
	}
	// Seq is carried through for agent events.
	if w := mirrorWire(bridge.Msg{Seq: 7, Kind: bridge.KindAgent, Event: agent.Info{Text: "x"}}); w.Seq != 7 {
		t.Errorf("agent seq not carried: %+v", w)
	}
}

func TestMirrorSessionsWire(t *testing.T) {
	w := mirrorWire(bridge.Msg{Seq: 4, Kind: bridge.KindSessions, Sessions: []bridge.SessionInfo{
		{ID: "20060102-x", Title: "build a thing", Detail: "Jan 2 · id", Active: true},
		{ID: "20060102-y", Title: "another"},
	}})
	if w.Type != "sessions" || len(w.Sessions) != 2 {
		t.Fatalf("sessions wire = %+v, want 2 sessions", w)
	}
	if w.Sessions[0].ID != "20060102-x" || !w.Sessions[0].Active || w.Sessions[0].Title != "build a thing" {
		t.Errorf("first session item wrong: %+v", w.Sessions[0])
	}
}

func TestMirrorCommandsWire(t *testing.T) {
	w := mirrorWire(bridge.Msg{Seq: 5, Kind: bridge.KindCommands, Commands: []bridge.Command{
		{Name: "/help", Args: "", Desc: "show help"},
		{Name: "/model", Args: "[id]", Desc: "set model"},
	}, Mentions: []bridge.Mention{
		{Name: "explore", Kind: "agent", Desc: "read-only explorer"},
		{Name: "claude", Kind: "worker", Desc: "worker · claude"},
	}})
	if w.Type != "commands" || len(w.Commands) != 2 {
		t.Fatalf("commands wire = %+v, want 2 commands", w)
	}
	if w.Commands[1].Name != "/model" || w.Commands[1].Args != "[id]" {
		t.Errorf("command payload wrong: %+v", w.Commands)
	}
	if len(w.Mentions) != 2 || w.Mentions[0].Name != "explore" || w.Mentions[0].Kind != "agent" {
		t.Fatalf("mentions wire = %+v, want explore(agent)+claude(worker)", w.Mentions)
	}
	if w.Mentions[1].Name != "claude" || w.Mentions[1].Kind != "worker" {
		t.Errorf("worker mention payload wrong: %+v", w.Mentions)
	}
}

func TestMirrorMenuWire(t *testing.T) {
	w := mirrorWire(bridge.Msg{Seq: 9, Kind: bridge.KindMenu, Menu: &bridge.Menu{
		Title:   "Select a model",
		Command: "/model",
		Items: []bridge.MenuItem{
			{Label: "Claude Sonnet 4.6", Value: "anthropic.claude-sonnet-4-6", Current: true},
			{Label: "Gemini 2.5 Pro", Value: "gcp:gemini-2.5-pro"},
		},
	}})
	if w.Type != "menu" || w.Menu == nil {
		t.Fatalf("menu wire = %+v, want type=menu with payload", w)
	}
	if w.Menu.Command != "/model" || len(w.Menu.Items) != 2 {
		t.Fatalf("menu payload wrong: %+v", w.Menu)
	}
	if w.Menu.Items[0].Value != "anthropic.claude-sonnet-4-6" || !w.Menu.Items[0].Current {
		t.Errorf("first item wrong: %+v", w.Menu.Items[0])
	}
}

func TestMirrorStatusWire(t *testing.T) {
	w := mirrorWire(bridge.Msg{Seq: 3, Kind: bridge.KindStatus, Status: &bridge.Status{
		Version: "0.3.0", Model: "opus", Provider: "floodgate anthropic",
		WorkingDir: "~/proj", Approval: "plan", Sandbox: "sandbox", TokensIn: 12, TokensOut: 4,
		ContextPercent: 33, ActiveTool: "bash", ToolsInFlight: 1,
		GitBranch: "main", GitDirty: 2, Relay: "connected",
		Host:  "Johns-MacBook",
		MCP:   []bridge.MCPServer{{Name: "fs", Tools: 3, Resources: 1, Prompts: 2}},
		Files: []bridge.ModFile{{Path: "main.go", Created: true}, {Path: "a.go"}},
	}})
	if w.Type != "status" || w.Status == nil {
		t.Fatalf("status wire = %+v, want type=status with payload", w)
	}
	s := w.Status
	if s.Model != "opus" || s.TokensIn != 12 || s.ContextPercent != 33 || s.ActiveTool != "bash" {
		t.Errorf("status payload wrong: %+v", s)
	}
	if s.Version != "0.3.0" || s.WorkingDir != "~/proj" || s.Approval != "plan" {
		t.Errorf("status header fields wrong: %+v", s)
	}
	if s.Sandbox != "sandbox" {
		t.Errorf("status sandbox = %q, want sandbox", s.Sandbox)
	}
	if s.GitBranch != "main" || s.GitDirty != 2 {
		t.Errorf("status git = %q/%d, want main/2", s.GitBranch, s.GitDirty)
	}
	if s.Relay != "connected" {
		t.Errorf("status relay = %q, want connected", s.Relay)
	}
	if s.Host != "Johns-MacBook" {
		t.Errorf("status host = %q, want Johns-MacBook", s.Host)
	}
	if len(s.MCP) != 1 || s.MCP[0].Name != "fs" || s.MCP[0].Tools != 3 {
		t.Errorf("status MCP wrong: %+v", s.MCP)
	}
	if len(s.Files) != 2 || s.Files[0].Path != "main.go" || !s.Files[0].Created {
		t.Errorf("status Files wrong: %+v", s.Files)
	}
}

func TestMirrorSSEStreamsStatus(t *testing.T) {
	h := bridge.New()
	h.PublishStatus(bridge.Status{Model: "sonnet-test", TokensIn: 21, ContextPercent: 9})
	srv := NewMirrorServer(h).Handler()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/main/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { srv.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"status"`) || !strings.Contains(body, "sonnet-test") {
		t.Errorf("SSE body missing replayed status snapshot:\n%s", body)
	}
}
