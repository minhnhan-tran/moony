package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/provider"
	sessionpkg "github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
)

// fakeStreamer replays scripted model turns (no network), streaming any text
// through the handler the way the real client does.
type fakeStreamer struct {
	turns []llm.Response
	calls int
	// gate, when non-nil, blocks each StreamMessages call until a value is
	// received — letting a test hold a turn in the "busy" state deterministically.
	gate chan struct{}
	// errs, when set, is returned (by call index) instead of a turn response — so
	// a test can make a specific turn fail (e.g. an SQE-Proxy 403). A nil entry (or
	// past the end) means no injected error for that call.
	errs []error
}

func (f *fakeStreamer) StreamMessages(_ context.Context, _ string, _ []llm.Message, _ []llm.Tool, h llm.Handler) (*llm.Response, error) {
	if f.gate != nil {
		<-f.gate
	}
	if f.calls < len(f.errs) && f.errs[f.calls] != nil {
		err := f.errs[f.calls]
		f.calls++
		return nil, err
	}
	if f.calls >= len(f.turns) {
		return &llm.Response{StopReason: "end_turn"}, nil
	}
	r := f.turns[f.calls]
	f.calls++
	for _, b := range r.Blocks {
		if b.Type == llm.BlockText && b.Text != "" && h.OnTextDelta != nil {
			h.OnTextDelta(b.Text)
		}
	}
	return &r, nil
}
func (f *fakeStreamer) ListModels(context.Context) ([]config.ModelInfo, error) { return nil, nil }
func (f *fakeStreamer) Usage(context.Context) (*llm.UsageResponse, error)      { return nil, nil }
func (f *fakeStreamer) CheckAuth(context.Context) error                        { return nil }

// writerTool is a mutating tool used to exercise the approval flow.
type writerTool struct{}

func (writerTool) Name() string            { return "write_file" }
func (writerTool) Description() string     { return "writes" }
func (writerTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (writerTool) Mutates() bool           { return true }
func (writerTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "wrote it", nil
}

// newTestServer builds a store-backed multi-project server with a scripted
// agent, returning the server and the project key for its working dir.
func newTestServer(t *testing.T, fake *fakeStreamer, policy string, withTool bool) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MOONY_MCP", "0")        // don't spawn real MCP subprocesses in unit tests
	t.Setenv("MOONY_AUTO_TITLE", "0") // deterministic: no extra title model-call per turn (opt in per-test)
	dir := t.TempDir()
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := &config.Config{Model: "test", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: policy, WorkingDir: dir}
	factory := func(workingDir string, sess *sessionpkg.Session) *agent.Agent {
		c := *base
		c.WorkingDir = workingDir
		reg := tools.NewRegistry(workingDir)
		if withTool {
			reg.Register(writerTool{})
		}
		return agent.New(&c, fake, reg, store, sess)
	}
	return NewServer(store, dir, base, factory), projectKey(dir)
}

func TestToWire(t *testing.T) {
	cases := []struct {
		ev   agent.Event
		typ  string
		text string
	}{
		{agent.TextDelta{Text: "hi"}, "text", "hi"},
		{agent.ThinkingDelta{Text: "hmm"}, "thinking", "hmm"},
		{agent.Info{Text: "note"}, "info", "note"},
		{agent.TurnDone{}, "turn_done", ""},
		{agent.TitleUpdate{Title: "Add Dark Mode"}, "title", "Add Dark Mode"},
		{agent.ApprovalRequest{Tool: "bash"}, "approval", ""},
	}
	for _, c := range cases {
		w := toWire(c.ev)
		if w.Type != c.typ || w.Text != c.text {
			t.Errorf("toWire(%T) = {%s,%q}, want {%s,%q}", c.ev, w.Type, w.Text, c.typ, c.text)
		}
	}
	ts := toWire(agent.ToolStart{ID: "t1", Name: "grep", Summary: "x"})
	if ts.Type != "tool_start" || ts.ToolID != "t1" || ts.Tool != "grep" {
		t.Errorf("tool_start mapping wrong: %+v", ts)
	}
	if sw := toWire(agent.StepUpdate{Step: 7, Max: 150}); sw.Type != "step" || sw.Step != 7 || sw.MaxSteps != 150 {
		t.Errorf("step mapping wrong: %+v", sw)
	}
	qw := toWire(agent.QuestionRequest{
		Question: "Which?",
		Multiple: true,
		Options:  []agent.QuestionOption{{Label: "A", Description: "first"}, {Label: "B"}},
	})
	if qw.Type != "question" || qw.Question == nil {
		t.Fatalf("question mapping wrong: %+v", qw)
	}
	if qw.Question.Question != "Which?" || !qw.Question.Multiple || len(qw.Question.Options) != 2 {
		t.Fatalf("question payload wrong: %+v", qw.Question)
	}
	if qw.Question.Options[0].Label != "A" || qw.Question.Options[0].Description != "first" {
		t.Fatalf("question option wrong: %+v", qw.Question.Options[0])
	}

	// ContextStats flattens into a "context" event carrying the segments, total,
	// and window so the browser can draw its own stacked bar.
	cw := toWire(agent.ContextStats{
		Segments: []agent.ContextSegment{{Label: "System", Tokens: 300}, {Label: "Tools", Tokens: 4000}},
		Total:    4300,
		Window:   200_000,
	})
	if cw.Type != "context" || cw.Context == nil {
		t.Fatalf("context mapping wrong: %+v", cw)
	}
	if cw.Context.Total != 4300 || cw.Context.Window != 200_000 || len(cw.Context.Segments) != 2 {
		t.Fatalf("context payload wrong: %+v", cw.Context)
	}
	if cw.Context.Segments[0].Label != "System" || cw.Context.Segments[0].Tokens != 300 {
		t.Fatalf("context segment wrong: %+v", cw.Context.Segments[0])
	}
}

// TestSessionAnswerRoundTrip checks a pending ask_user question moves the session
// to awaiting_question and that answer() delivers the chosen labels to the
// agent's reply channel and flips the session back to busy.
func TestSessionAnswerRoundTrip(t *testing.T) {
	sess := newSession("id", "t", nil)

	// answer with nothing pending is a no-op.
	if sess.answer([]string{"x"}) {
		t.Fatal("answer should fail when no question is pending")
	}

	reply := make(chan []string, 1)
	sess.emit(agent.QuestionRequest{
		Question: "Which?",
		Options:  []agent.QuestionOption{{Label: "A"}, {Label: "B"}},
		Reply:    reply,
	})
	if got := sess.snapshot().State; got != stateQuestion {
		t.Fatalf("state = %q, want %q", got, stateQuestion)
	}

	if !sess.answer([]string{"B"}) {
		t.Fatal("answer should succeed with a pending question")
	}
	select {
	case got := <-reply:
		if len(got) != 1 || got[0] != "B" {
			t.Fatalf("reply labels = %v", got)
		}
	default:
		t.Fatal("answer did not deliver to the reply channel")
	}
	if got := sess.snapshot().State; got != stateBusy {
		t.Fatalf("state after answer = %q, want %q", got, stateBusy)
	}
}

func TestSessionHistoryReplayAndFanout(t *testing.T) {
	sess := newSession("id", "t", nil)
	sess.emit(agent.Info{Text: "first"})

	// A subscriber gets the history so far...
	id1, ch1, hist := sess.subscribe()
	if len(hist) != 1 || hist[0].Text != "first" {
		t.Fatalf("history replay wrong: %+v", hist)
	}
	// ...and a second subscriber too.
	_, ch2, hist2 := sess.subscribe()
	if len(hist2) != 1 {
		t.Fatalf("second subscriber history = %d, want 1", len(hist2))
	}
	// New events fan out to both, with increasing Seq.
	sess.emit(agent.TextDelta{Text: "second"})
	for _, ch := range []chan wireEvent{ch1, ch2} {
		select {
		case w := <-ch:
			if w.Text != "second" || w.Seq != 2 {
				t.Errorf("live event = %+v, want text=second seq=2", w)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive the live event")
		}
	}
	sess.unsubscribe(id1)
	if _, ok := <-ch1; ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

// TestProxyDeniedTurnPrunedFromHistory: a turn that fails with the SQE-Proxy
// group-gate 403 must NOT leave its "user" echo or the error in history — the
// client transparently resends, so a persisted echo/error would duplicate the
// question (and re-fire a refresh) on every reload/reconnect. A non-retried error
// is kept so the user still sees what went wrong.
func TestProxyDeniedTurnPrunedFromHistory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantPruned bool
	}{
		{"proxy denied is pruned", errors.New("floodgate returned 403 Forbidden: sqe-proxy: access denied"), true},
		{"other error is kept", errors.New("some other failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("MOONY_MCP", "0")
			t.Setenv("MOONY_AUTO_TITLE", "0")
			t.Setenv("MOONY_MCP_READY_WAIT", "0") // don't block the first turn waiting on MCP
			dir := t.TempDir()
			store, err := sessionpkg.NewStoreAt(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Model: "test", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, WorkingDir: dir}
			fake := &fakeStreamer{errs: []error{tc.err}} // the first (only) turn fails
			psess := sessionpkg.New(cfg.Model, dir)
			ag := agent.New(cfg, fake, tools.NewRegistry(dir), store, psess)
			sess := newSession("s1", "t", ag)

			if !sess.start("my question", nil, nil) {
				t.Fatal("start returned false")
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && sess.snapshot().State != stateIdle {
				time.Sleep(5 * time.Millisecond)
			}
			if sess.snapshot().State != stateIdle {
				t.Fatal("turn never returned to idle")
			}

			_, _, hist := sess.subscribe()
			var sawUser, sawError bool
			for _, e := range hist {
				switch e.Type {
				case "user":
					sawUser = true
				case "error":
					sawError = true
				}
			}
			if tc.wantPruned {
				if sawUser {
					t.Errorf("proxy-denied turn left a 'user' echo in history: %+v", hist)
				}
				if sawError {
					t.Errorf("proxy-denied turn left an 'error' in history (would re-fire a refresh on replay): %+v", hist)
				}
			} else {
				if !sawUser {
					t.Errorf("a normal turn should keep its 'user' echo in history: %+v", hist)
				}
				if !sawError {
					t.Errorf("a non-retried error should be recorded in history: %+v", hist)
				}
			}
		})
	}
}

func postJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// waitState polls until the session reaches want or the deadline passes.
func waitState(t *testing.T, s *Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		sess := s.sessions[id]
		s.mu.Unlock()
		if sess != nil && sess.snapshot().State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session never reached state %q", want)
}

func TestMessageFlow(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hello world")}}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}

	rec = postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("message accepted code = %d, want 202", rec.Code)
	}
	// A second message while busy is rejected.
	if r2 := postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"again"}`); r2.Code != http.StatusConflict && r2.Code != http.StatusAccepted {
		// (race: the first turn may already be done; 202 is also acceptable)
		t.Logf("second message code = %d", r2.Code)
	}

	waitState(t, srv, created.ID, stateIdle)

	srv.mu.Lock()
	sess := srv.sessions[created.ID]
	srv.mu.Unlock()
	_, _, hist := sess.subscribe()

	var text strings.Builder
	var sawUser, sawDone bool
	for _, e := range hist {
		switch e.Type {
		case "user":
			// The fake streamer finishes a turn instantly, so the "again"
			// message above may start a second turn and add another user event.
			// Use OR so a later user message can't clear the flag.
			sawUser = sawUser || e.Text == "hi"
		case "text":
			text.WriteString(e.Text)
		case "turn_done":
			sawDone = true
		}
	}
	if !sawUser {
		t.Error("history missing the user message")
	}
	if !strings.Contains(text.String(), "hello world") {
		t.Errorf("assistant text = %q", text.String())
	}
	if !sawDone {
		t.Error("history missing turn_done")
	}
}

func TestApprovalFlow(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	srv, key := newTestServer(t, fake, config.ApprovalPrompt, true)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"write something"}`)

	// The agent blocks on approval.
	waitState(t, srv, created.ID, stateApproval)

	rec = postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/approve", `{"decision":"approve"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("approve code = %d, want 204", rec.Code)
	}

	waitState(t, srv, created.ID, stateIdle)

	srv.mu.Lock()
	sess := srv.sessions[created.ID]
	srv.mu.Unlock()
	_, _, hist := sess.subscribe()

	var sawApproval, sawToolEnd bool
	for _, e := range hist {
		switch e.Type {
		case "approval":
			sawApproval = e.Tool == "write_file"
		case "tool_end":
			sawToolEnd = e.Tool == "write_file"
		}
	}
	if !sawApproval {
		t.Error("history missing the approval request")
	}
	if !sawToolEnd {
		t.Error("tool did not run after approval")
	}
}

func TestSSEStreamsHistory(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("streamed")}}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"go"}`)
	waitState(t, srv, created.ID, stateIdle)

	// Open the SSE stream; it replays history then blocks. Cancel after a beat.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/"+created.ID+"/events", nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "data: ") || !strings.Contains(body, "streamed") {
		t.Errorf("SSE body missing replayed events:\n%s", body)
	}
}

// TestReplayMessages checks that a saved transcript becomes the right wire
// events, pairing each tool_use with its tool_result by id.
func TestReplayMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("read main.go")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockText, Text: "sure"},
			{Type: llm.BlockToolUse, ID: "t1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: "t1", Content: "package main", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
	got := replayMessages(msgs, nil, nil, nil, nil)

	want := []struct {
		typ, text, tool, output string
	}{
		{"user", "read main.go", "", ""},
		{"text", "sure", "", ""},
		{"tool_start", "", "read_file", ""},
		{"tool_end", "", "read_file", "package main"},
		{"text", "done", "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("replay produced %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Type != w.typ || g.Text != w.text || g.Tool != w.tool || g.Output != w.output {
			t.Errorf("event %d = {%s,%q,%s,%q}, want {%s,%q,%s,%q}",
				i, g.Type, g.Text, g.Tool, g.Output, w.typ, w.text, w.tool, w.output)
		}
	}
	if got[2].Input != `{"path":"main.go"}` {
		t.Errorf("tool_start input = %q, want the tool's JSON input", got[2].Input)
	}
}

// TestReplayMessagesTurnMeta confirms a saved session's per-turn footer records
// are replayed as "turn_meta" events anchored right after the turn they belong
// to, so a reopened session shows each turn's datetime · duration · model.
func TestReplayMessagesTurnMeta(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("hi")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("hello")}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("more")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")}},
	}
	now := time.Now()
	metas := []sessionpkg.TurnMeta{
		{Time: now, DurationMS: 1500, CostUSD: 0.012, Model: "anthropic.claude-opus-4-8", Reasoning: "high", MsgCount: 2},
		{Time: now, DurationMS: 800, Model: "anthropic.claude-opus-4-8", MsgCount: 4},
	}
	got := replayMessages(msgs, metas, nil, nil, nil)

	wantTypes := []string{"user", "text", "turn_meta", "user", "text", "turn_meta"}
	if len(got) != len(wantTypes) {
		t.Fatalf("replay produced %d events, want %d: %+v", len(got), len(wantTypes), got)
	}
	for i, wt := range wantTypes {
		if got[i].Type != wt {
			t.Fatalf("event %d type = %q, want %q (got %+v)", i, got[i].Type, wt, got)
		}
	}
	if got[2].Meta == nil || got[2].Meta.DurationMS != 1500 || got[2].Meta.Model != "anthropic.claude-opus-4-8" || got[2].Meta.Reasoning != "high" {
		t.Errorf("first turn_meta = %+v, want duration 1500, model set, reasoning high", got[2].Meta)
	}
	if got[2].Meta.CostUSD != 0.012 {
		t.Errorf("first turn_meta cost = %v, want 0.012 (the answering cost should round-trip)", got[2].Meta.CostUSD)
	}
	if got[2].Meta.Time == "" {
		t.Errorf("first turn_meta missing time")
	}
	if got[5].Meta == nil || got[5].Meta.DurationMS != 800 {
		t.Errorf("second turn_meta = %+v, want duration 800", got[5].Meta)
	}
	// Each user message must be stamped with its turn's time for the footer.
	if got[0].Time == "" || got[3].Time == "" {
		t.Errorf("user events not stamped with turn time: %q, %q", got[0].Time, got[3].Time)
	}
}

// TestReplayMessagesBoardMessages confirms a saved session's shared message-board
// posts are replayed as "board_message" events anchored after the turn that
// produced them, so a reopened session restores the multi-agent conversation
// (including sub-agent posts that never entered the transcript).
func TestReplayMessagesBoardMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("coordinate")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
	board := []sessionpkg.BoardMessage{
		{Seq: 1, From: "build", To: "", Text: "kick off", MsgCount: 2},
		{Seq: 2, From: "explore", To: "build", Text: "found 3 callers", MsgCount: 2},
	}
	got := replayMessages(msgs, nil, nil, board, nil)

	var posts []wireEvent
	for _, e := range got {
		if e.Type == "board_message" {
			posts = append(posts, e)
		}
	}
	if len(posts) != 2 {
		t.Fatalf("got %d board_message events, want 2: %+v", len(posts), got)
	}
	if posts[0].Board == nil || posts[0].Board.From != "build" || posts[0].Board.To != "" || posts[0].Board.Text != "kick off" {
		t.Errorf("first board_message = %+v, want build broadcast 'kick off'", posts[0].Board)
	}
	if posts[1].Board == nil || posts[1].Board.From != "explore" || posts[1].Board.To != "build" {
		t.Errorf("second board_message = %+v, want explore→build", posts[1].Board)
	}
}

// TestReplayMessagesInfoNotes confirms a saved session's persisted status
// notices (self-review's) are replayed as plain "info" events anchored right
// after the message they followed live, and BEFORE that position's turn_meta
// — matching the live ordering (notice, then TurnDone).
func TestReplayMessagesInfoNotes(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("fix the bug")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
	metas := []sessionpkg.TurnMeta{{MsgCount: 2, DurationMS: 500}}
	notes := []sessionpkg.InfoNote{
		{Text: "Self-reviewing the diff for introduced bugs… (claude-sonnet-5)", MsgCount: 2},
		{Text: "Self-review: no issues found. (claude-sonnet-5 · 1.2k↑ 218↓ · $0.0078)", MsgCount: 2},
	}
	got := replayMessages(msgs, metas, nil, nil, notes)

	infoIdx, metaIdx := -1, -1
	var infos []wireEvent
	for i, e := range got {
		if e.Type == "info" {
			infos = append(infos, e)
			if infoIdx == -1 {
				infoIdx = i
			}
		}
		if e.Type == "turn_meta" {
			metaIdx = i
		}
	}
	if len(infos) != 2 {
		t.Fatalf("got %d info events, want 2: %+v", len(infos), got)
	}
	if infos[0].Text != notes[0].Text || infos[1].Text != notes[1].Text {
		t.Errorf("info texts = %q, %q; want %q, %q", infos[0].Text, infos[1].Text, notes[0].Text, notes[1].Text)
	}
	if metaIdx == -1 || infoIdx >= metaIdx {
		t.Errorf("info events (first at %d) must come before turn_meta (at %d)", infoIdx, metaIdx)
	}
}

// TestReplayMessagesTaskMeta confirms a saved session's per-delegation footer
// records are replayed as "task_meta" events anchored right after their task
// call's tool_end, so a reopened session restores each task card's footer
// (duration · cost · model · calls) — mirroring the live run.
func TestReplayMessagesTaskMeta(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("delegate it")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "task1", Name: "task", Input: []byte(`{"agent":"explore","prompt":"look"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: "task1", Content: "found it"},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
	taskMetas := []sessionpkg.TaskMeta{
		{ParentID: "task1", Agent: "explore", Model: "anthropic.claude-haiku", CostUSD: 0.003, DurationMS: 4200, Calls: 5, InTokens: 1200, OutTokens: 340},
	}
	got := replayMessages(msgs, nil, taskMetas, nil, nil)

	wantTypes := []string{"user", "tool_start", "tool_end", "task_meta", "text"}
	if len(got) != len(wantTypes) {
		t.Fatalf("replay produced %d events, want %d: %+v", len(got), len(wantTypes), got)
	}
	for i, wt := range wantTypes {
		if got[i].Type != wt {
			t.Fatalf("event %d type = %q, want %q (got %+v)", i, got[i].Type, wt, got)
		}
	}
	tm := got[3]
	if tm.ParentID != "task1" {
		t.Errorf("task_meta parentId = %q, want task1 (must anchor to the task card)", tm.ParentID)
	}
	if tm.Agent != "explore" {
		t.Errorf("task_meta agent = %q, want explore", tm.Agent)
	}
	if tm.Meta == nil || tm.Meta.DurationMS != 4200 || tm.Meta.CostUSD != 0.003 ||
		tm.Meta.Model != "anthropic.claude-haiku" || tm.Meta.Calls != 5 ||
		tm.Meta.InTokens != 1200 || tm.Meta.OutTokens != 340 {
		t.Errorf("task_meta = %+v, want the persisted footer to round-trip", tm.Meta)
	}
}

// TestReplayMessagesReconstructsTodos confirms a reopened session restores its
// todo panel: the last successful todo_write call becomes a trailing "todos"
// wire event even though per-call todos events aren't part of the transcript.
// TestReplayMessagesNoSyntheticTodos confirms that reopening a session does NOT
// synthesize a trailing "todos" event, even when the transcript contains
// todo_write calls. Per-turn todos aren't persisted, so a restored panel/phase-
// block could only be a detached, mis-anchored block at the end of the transcript;
// we'd rather show nothing on reopen and let the todo_write tool calls replay as
// ordinary tool cards. Live sessions still emit per-turn todos events as before.
func TestReplayMessagesNoSyntheticTodos(t *testing.T) {
	first, _ := json.Marshal(map[string]any{"todos": []map[string]any{
		{"content": "step one", "status": "in_progress"},
	}})
	final, _ := json.Marshal(map[string]any{"todos": []map[string]any{
		{"content": "step one", "status": "completed"},
		{"content": "step two", "status": "in_progress"},
	}})
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("do work")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t1", Name: "todo_write", Input: first},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: "t1", Content: "ok", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t2", Name: "todo_write", Input: final},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: "t2", Content: "ok", IsError: false},
		}},
	}

	got := replayMessages(msgs, nil, nil, nil, nil)
	if len(got) == 0 {
		t.Fatal("no events produced")
	}
	for _, e := range got {
		if e.Type == "todos" {
			t.Fatalf("replay should not synthesize a todos event on reopen, got %+v", e)
		}
	}
	// The todo_write calls themselves should still replay as tool cards.
	var toolEnds int
	for _, e := range got {
		if e.Type == "tool_end" {
			toolEnds++
		}
	}
	if toolEnds == 0 {
		t.Fatal("expected the todo_write tool calls to replay as tool_end events")
	}
}

// TestReplayMessagesNoTodos confirms a transcript without todo_write produces no
// trailing todos event.
func TestReplayMessagesNoTodos(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("hi")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("hello")}},
	}
	for _, e := range replayMessages(msgs, nil, nil, nil, nil) {
		if e.Type == "todos" {
			t.Fatal("did not expect a todos event without todo_write")
		}
	}
}

// TestListProjects verifies the launch dir is surfaced first and projects with
// saved sessions are listed with their session counts, most-recent first.
func TestListProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch := t.TempDir()
	other := t.TempDir()
	// Two saved sessions in `other`, none yet in `launch`.
	for i := 0; i < 2; i++ {
		if err := store.Save(sessionpkg.New("m", other)); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(store, launch, &config.Config{Model: "m"}, func(string, *sessionpkg.Session) *agent.Agent { return nil })

	rec := postJSON(t, srv.Handler(), "GET", "/api/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list projects code = %d", rec.Code)
	}
	var projects []projectInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v (%s)", err, rec.Body)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(projects), projects)
	}
	if projects[0].Dir != launch {
		t.Errorf("first project = %q, want the launch dir %q", projects[0].Dir, launch)
	}
	if projects[0].Key != projectKey(launch) {
		t.Errorf("launch key = %q, want %q", projects[0].Key, projectKey(launch))
	}
	if projects[1].Dir != other || projects[1].Sessions != 2 {
		t.Errorf("second project = %+v, want dir=%q sessions=2", projects[1], other)
	}
}

// TestProjectSessions lists the saved sessions for one project by its key.
func TestProjectSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	saved := sessionpkg.New("m", dir)
	saved.Title = "my work"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, dir, &config.Config{Model: "m"}, func(string, *sessionpkg.Session) *agent.Agent { return nil })

	rec := postJSON(t, srv.Handler(), "GET", "/api/projects/"+projectKey(dir)+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("project sessions code = %d", rec.Code)
	}
	var sessions []savedSessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(sessions) != 1 || sessions[0].ID != saved.ID || sessions[0].Title != "my work" {
		t.Fatalf("sessions = %+v, want one titled 'my work'", sessions)
	}
	if sessions[0].Live {
		t.Error("a never-opened session should not be reported live")
	}

	// A bad key is a 400.
	if bad := postJSON(t, srv.Handler(), "GET", "/api/projects/!!notbase64!!/sessions", ""); bad.Code != http.StatusBadRequest {
		t.Errorf("bad project key code = %d, want 400", bad.Code)
	}
}

// TestOpenPersistedSessionReplaysHistory opens a session that already has a
// saved transcript and confirms its SSE history replays the prior turn — i.e.
// the server lazily loads + seeds disk sessions on first access.
func TestOpenPersistedSessionReplaysHistory(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)

	// Pre-seed a session on disk with a completed exchange.
	dir := t.TempDir()
	saved := sessionpkg.New("m", dir)
	saved.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("earlier question")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("earlier answer")}},
	}
	if err := srv.store.Save(saved); err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/"+saved.ID+"/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "earlier question") || !strings.Contains(body, "earlier answer") {
		t.Errorf("opening a persisted session did not replay its transcript:\n%s", body)
	}
	// The replay must include the session's current state, so a connecting viewer
	// knows it's idle (askable). Without it, switching here from a busy session
	// would leave the single-pane composer disabled — "can't ask while another
	// session is working".
	if !strings.Contains(body, `"type":"state"`) || !strings.Contains(body, `"state":"idle"`) {
		t.Errorf("replay did not seed an idle state event (composer would stay disabled after switching from a busy session):\n%s", body)
	}
}

// TestMessagePersistsToStore drives a full turn over the API and confirms the
// agent wrote the conversation back to disk (so it survives a restart).
func TestMessagePersistsToStore(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("persisted reply")}}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"remember this"}`)
	waitState(t, srv, created.ID, stateIdle)

	// Give the post-turn persist a moment, then reload from the store.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reloaded, err := srv.store.Load(created.ID)
		if err == nil && len(reloaded.Messages) >= 2 {
			return // user + assistant message persisted
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("turn was not persisted back to the store")
}

// TestDeleteSession deletes an idle session and confirms it's gone from disk and
// from the project listing, and that deleting an unknown id is idempotent.
func TestDeleteSession(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hi")}}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}

	del := func(id string) int {
		req := httptest.NewRequest("DELETE", "/api/sessions/"+id, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := del(created.ID); code != http.StatusNoContent {
		t.Fatalf("delete code = %d, want 204", code)
	}
	if _, err := srv.store.Load(created.ID); err == nil {
		t.Error("session still in the store after delete")
	}
	rec = postJSON(t, h, "GET", "/api/projects/"+key+"/sessions", "")
	var list []savedSessionInfo
	json.Unmarshal(rec.Body.Bytes(), &list)
	for _, s := range list {
		if s.ID == created.ID {
			t.Error("deleted session still appears in the project listing")
		}
	}
	// Idempotent: deleting a now-missing (or never-existing) id is still 204.
	if code := del("does-not-exist"); code != http.StatusNoContent {
		t.Errorf("delete unknown code = %d, want 204", code)
	}
}

// TestDeleteSessionBusyRejected confirms a session with a turn in progress can't
// be deleted (otherwise the agent could re-persist it after deletion).
func TestDeleteSessionBusyRejected(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	srv, key := newTestServer(t, fake, config.ApprovalPrompt, true)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"write"}`)
	waitState(t, srv, created.ID, stateApproval) // blocked on approval == not idle

	req := httptest.NewRequest("DELETE", "/api/sessions/"+created.ID, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete-while-busy code = %d, want 409", rec.Code)
	}
	if _, err := srv.store.Load(created.ID); err != nil {
		t.Errorf("session should survive a rejected delete: %v", err)
	}
	// Finish the turn so the agent goroutine drains before t.TempDir() cleanup.
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/approve", `{"decision":"approve"}`)
	waitState(t, srv, created.ID, stateIdle)
}

// TestSessionListReportsAttentionState confirms the project session list carries
// each session's live state, so the sidebar can show a "needs approval" badge and
// the web UI can notify across parallel sessions.
func TestSessionListReportsAttentionState(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	srv, key := newTestServer(t, fake, config.ApprovalPrompt, true)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"write"}`)
	waitState(t, srv, created.ID, stateApproval)

	find := func() *savedSessionInfo {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", "/api/projects/"+key+"/sessions", nil))
		var list []savedSessionInfo
		json.Unmarshal(r.Body.Bytes(), &list)
		for i := range list {
			if list[i].ID == created.ID {
				return &list[i]
			}
		}
		return nil
	}

	// While blocked on approval, the list must report it so the sidebar badges it.
	si := find()
	if si == nil {
		t.Fatalf("session %s not in the project list", created.ID)
	}
	if si.State != stateApproval {
		t.Errorf("State = %q, want %q (the sidebar needs-approval badge reads this)", si.State, stateApproval)
	}
	if !si.Running {
		t.Error("Running should be true while awaiting approval")
	}

	// After the turn finishes, the list reports idle (no attention).
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/approve", `{"decision":"approve"}`)
	waitState(t, srv, created.ID, stateIdle)
	if si = find(); si == nil || si.State != "" || si.Running {
		t.Errorf("after finishing: %+v, want idle (State \"\", Running false)", si)
	}
}

// TestClearProjectSessions deletes every saved session in a project in one call.
func TestClearProjectSessions(t *testing.T) {
	fake := &fakeStreamer{}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	// Seed three saved sessions in the project's working dir.
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := srv.store.Save(sessionpkg.New("m", dir)); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest("DELETE", "/api/projects/"+key+"/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear code = %d, want 200", rec.Code)
	}
	var res map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if res["deleted"] != 3 || res["skipped"] != 0 {
		t.Errorf("result = %+v, want deleted=3 skipped=0", res)
	}

	rec = postJSON(t, h, "GET", "/api/projects/"+key+"/sessions", "")
	var list []savedSessionInfo
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("project still has %d sessions after clear", len(list))
	}
}

// TestClearProjectSessionsSkipsBusy confirms a session mid-turn survives a clear
// (it's skipped) while the idle ones are deleted.
func TestClearProjectSessionsSkipsBusy(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	srv, key := newTestServer(t, fake, config.ApprovalPrompt, true)
	h := srv.Handler()
	dir, _ := decodeProjectKey(key)

	// One idle saved session, and one we'll drive into an approval (busy) state.
	if err := srv.store.Save(sessionpkg.New("m", dir)); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var busy sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &busy)
	postJSON(t, h, "POST", "/api/sessions/"+busy.ID+"/message", `{"text":"write"}`)
	waitState(t, srv, busy.ID, stateApproval)

	req := httptest.NewRequest("DELETE", "/api/projects/"+key+"/sessions", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var res map[string]int
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res["deleted"] != 1 || res["skipped"] != 1 {
		t.Errorf("result = %+v, want deleted=1 skipped=1", res)
	}
	if _, err := srv.store.Load(busy.ID); err != nil {
		t.Errorf("busy session should survive the clear: %v", err)
	}
	// Drain the busy turn before cleanup.
	postJSON(t, h, "POST", "/api/sessions/"+busy.ID+"/approve", `{"decision":"approve"}`)
	waitState(t, srv, busy.ID, stateIdle)
}

// TestSessionEmitsStatus confirms the standalone server seeds a status snapshot
// (so the browser header/status bar populate) — it previously sent none.
func TestSessionEmitsStatus(t *testing.T) {
	fake := &fakeStreamer{}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/"+created.ID+"/events", nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"status"`) {
		t.Errorf("event stream carried no status event:\n%s", body)
	}
	if !strings.Contains(body, `"model":"test"`) {
		t.Errorf("status snapshot missing the model:\n%s", body)
	}
}

// TestStatusReflectsUsage confirms a completed turn's token usage is accumulated
// and surfaced in a re-emitted status snapshot (token counts in the status bar).
func TestStatusReflectsUsage(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{{
		StopReason: "end_turn",
		Blocks:     []llm.ContentBlock{llm.TextBlock("hi")},
		Usage:      llm.Usage{InputTokens: 1000, OutputTokens: 50},
	}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"go"}`)
	waitState(t, srv, created.ID, stateIdle)

	srv.mu.Lock()
	sess := srv.sessions[created.ID]
	srv.mu.Unlock()

	// Wait for the post-turn usage to settle.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if in, out, _, _, _, _ := sess.usageSnapshot(); in == 1000 && out == 50 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if in, out, _, _, _, _ := sess.usageSnapshot(); in != 1000 || out != 50 {
		t.Fatalf("accumulated usage = in:%d out:%d, want 1000/50", in, out)
	}

	// A status event in history reflects the accumulated tokens.
	_, _, hist := sess.subscribe()
	var maxIn, maxOut int
	for _, e := range hist {
		if e.Type == "status" && e.Status != nil {
			if e.Status.TokensIn > maxIn {
				maxIn = e.Status.TokensIn
			}
			if e.Status.TokensOut > maxOut {
				maxOut = e.Status.TokensOut
			}
		}
	}
	if maxIn != 1000 || maxOut != 50 {
		t.Errorf("status usage = in:%d out:%d, want 1000/50", maxIn, maxOut)
	}
}

// --- slash commands ---------------------------------------------------------

// openLive creates a session and returns its live *session (white-box).
func openLive(t *testing.T, srv *Server, key string) *session {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}
	sess := srv.openSession(created.ID)
	if sess == nil {
		t.Fatal("openSession returned nil")
	}
	// Attach a live subscriber so transient events (menus, which aren't recorded
	// in history) can be observed by lastMenu. runCommand is synchronous and fans
	// out to current subscribers, so anything emitted after this lands in the
	// channel buffer for lastMenu to drain.
	id, ch, _ := sess.subscribe()
	menuChans[sess] = ch
	t.Cleanup(func() { sess.unsubscribe(id); delete(menuChans, sess) })
	return sess
}

// menuChans holds each openLive session's live event channel so lastMenu can read
// transient "menu" events (single-threaded test use; no locking needed).
var menuChans = map[*session]chan wireEvent{}

// waitMenu polls for a transient menu with the given command (some menus, e.g.
// /models, are emitted from a background goroutine after an async fetch). Fails
// the test if none arrives before the deadline.
func waitMenu(t *testing.T, sess *session, command string) *menuWire {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m := lastMenu(sess, command); m != nil {
			return m
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %q menu emitted within the deadline", command)
	return nil
}

func history(sess *session) []wireEvent {
	_, _, h := sess.subscribe()
	return h
}

func hasInfo(sess *session, substr string) bool {
	for _, e := range history(sess) {
		if e.Type == "info" && strings.Contains(e.Text, substr) {
			return true
		}
	}
	return false
}

// waitFor polls the session history until pred is satisfied or it times out.
func waitFor(t *testing.T, sess *session, what string, pred func(wireEvent) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range history(sess) {
			if pred(e) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPaletteSeededAndHelp(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// The command palette is seeded on open.
	seeded := false
	for _, e := range history(sess) {
		if e.Type == "commands" && len(e.Commands) > 0 {
			seeded = true
		}
	}
	if !seeded {
		t.Error("command palette was not seeded into the session history")
	}

	srv.runCommand(sess, "/help")
	if !hasInfo(sess, "/reset") {
		t.Error("/help should list the available commands")
	}
}

func TestSlashPlanAndBuild(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/plan")
	if !sess.agent.PlanMode() {
		t.Fatal("/plan should enable plan mode")
	}
	// The refreshed status badge reads "plan".
	planned := false
	for _, e := range history(sess) {
		if e.Type == "status" && e.Status != nil && e.Status.Approval == "plan" {
			planned = true
		}
	}
	if !planned {
		t.Error("status after /plan should show approval=plan")
	}

	srv.runCommand(sess, "/build")
	if sess.agent.PlanMode() {
		t.Error("/build should disable plan mode")
	}
}

// TestSlashPlanApproveEmitsPlanEvent covers the web /plan approve path: approving
// a draft plan must publish a "plan" event carrying the now-approved status (not
// just a status event), so the browser's plan bar flips to "approved" and the
// Tasks-bar gate — which keeps the todo list hidden until the user confirms the
// plan — opens. (The exit_plan tool path emits this via PlanUpdated; the slash
// command must publish it explicitly.)
func TestSlashPlanApproveEmitsPlanEvent(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// Seed a draft plan awaiting the user's confirmation.
	sess.agent.SetPlan(&sessionpkg.Plan{
		Title:  "Refactor",
		Steps:  []sessionpkg.PlanStep{{Title: "step one"}},
		Status: sessionpkg.PlanDraft,
	})

	srv.runCommand(sess, "/plan approve")

	if got := sess.agent.Plan(); got == nil || got.Status != sessionpkg.PlanApproved {
		t.Fatalf("plan status after approve = %+v, want approved", got)
	}
	// A "plan" event carrying the approved status must have been published so the
	// browser opens the Tasks-bar gate (and flips the plan bar to "approved").
	found := false
	for _, e := range history(sess) {
		if e.Type == "plan" && e.Plan != nil && e.Plan.Status == string(sessionpkg.PlanApproved) {
			found = true
		}
	}
	if !found {
		t.Error("approving a plan should publish a plan event with status=approved")
	}
}

func TestSlashModelSetsConfig(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/model some-other-model")
	if got := sess.agent.Config().Model; got != "some-other-model" {
		t.Fatalf("model = %q, want some-other-model", got)
	}
	// A refreshed status reflects the new model.
	found := false
	for _, e := range history(sess) {
		if e.Type == "status" && e.Status != nil && e.Status.Model == "some-other-model" {
			found = true
		}
	}
	if !found {
		t.Error("status after /model should show the new model")
	}
}

// TestSlashVerifyConfigures covers the /verify command: a command sets (and
// persists) the verify command, and "off" clears it.
func TestSlashVerifyConfigures(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/verify go build ./...")
	if got := sess.agent.Config().VerifyCommand; got != "go build ./..." {
		t.Fatalf("VerifyCommand = %q, want 'go build ./...'", got)
	}

	srv.runCommand(sess, "/verify off")
	if got := sess.agent.Config().VerifyCommand; got != "" {
		t.Errorf("VerifyCommand after /verify off = %q, want empty", got)
	}
}

// TestSlashSelfReviewConfigures covers the /selfreview command: "on" enables (and
// persists) the self-review gate and "off" disables it.
func TestSlashSelfReviewConfigures(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/selfreview on")
	if !sess.agent.Config().SelfReview {
		t.Fatal("SelfReview after /selfreview on = false, want true")
	}

	srv.runCommand(sess, "/selfreview off")
	if sess.agent.Config().SelfReview {
		t.Error("SelfReview after /selfreview off = true, want false")
	}
}

// TestSlashAgentSwitchesProfile covers the web primary-agent switcher: bare
// /agent emits a picker menu (current marked), /agent <name> switches the active
// profile and refreshes status, and an unknown name leaves the profile unchanged.
func TestSlashAgentSwitchesProfile(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// Bare /agent → a picker menu carrying the selectable profiles.
	srv.runCommand(sess, "/agent")
	m := lastMenu(sess, "/agent")
	if m == nil {
		t.Fatal("/agent should emit a menu")
	}
	build, ok := menuItem(m, "build")
	if !ok {
		t.Fatal("/agent menu should include build")
	}
	if _, ok := menuItem(m, "plan"); !ok {
		t.Fatal("/agent menu should include plan")
	}
	if !build.Current {
		t.Error("build should be the current profile by default")
	}

	// /agent plan switches to plan mode.
	srv.runCommand(sess, "/agent plan")
	if !sess.agent.PlanMode() {
		t.Fatal("/agent plan should enable plan mode")
	}
	agentBadge := ""
	for _, e := range history(sess) {
		if e.Type == "status" && e.Status != nil && e.Status.Agent != "" {
			agentBadge = e.Status.Agent
		}
	}
	if agentBadge != "plan" {
		t.Errorf("status after /agent plan should show agent=plan, got %q", agentBadge)
	}

	// /agent build switches back.
	srv.runCommand(sess, "/agent build")
	if sess.agent.PlanMode() {
		t.Error("/agent build should leave plan mode")
	}

	// An unknown profile is rejected without changing the active one.
	srv.runCommand(sess, "/agent nope")
	if got := sess.agent.ActiveAgent().Name; got != config.AgentBuild {
		t.Errorf("after /agent nope the profile should stay build, got %q", got)
	}
}

// TestAutoTitleEmitsTitleEvent verifies that, with auto-titling enabled, the
// first completed turn generates a model title, persists it, updates the live
// session, and pushes a "title" event so the browser can refresh the sidebar.
func TestAutoTitleEmitsTitleEvent(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Done — added the toggle.")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Add Dark Mode Toggle")}}, // the title call
	}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	t.Setenv("MOONY_AUTO_TITLE", "1") // override the harness default for this test
	sess := openLive(t, srv, key)

	sess.start("please add a dark mode toggle", nil, nil)
	waitFor(t, sess, "title event", func(e wireEvent) bool {
		return e.Type == "title" && e.Text == "Add Dark Mode Toggle"
	})
	if got := sess.snapshot().Title; got != "Add Dark Mode Toggle" {
		t.Errorf("live session title = %q, want %q", got, "Add Dark Mode Toggle")
	}
}

func TestSlashModelsEmitsMenu(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)
	srv.runCommand(sess, "/models")
	// The menu is transient (not stored in history) and emitted from a background
	// goroutine after an async model fetch; wait for it on the live stream.
	waitMenu(t, sess, "/model")
}

func TestSlashReasoningEmitsMenu(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)
	srv.runCommand(sess, "/reasoning")
	if lastMenu(sess, "/reasoning") == nil {
		t.Error("/reasoning with no arg should emit a reasoning menu")
	}
}

// TestSlashProviderEmitsMenu verifies /provider with no arg emits a clickable
// provider picker, and /provider <id> sets the backend directly. Ollama is the
// sole provider.
func TestSlashProviderEmitsMenu(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// No arg → a picker listing the providers, with the active one marked.
	srv.runCommand(sess, "/provider")
	m := lastMenu(sess, "/provider")
	if m == nil {
		t.Fatal("/provider with no arg should emit a provider menu")
	}
	if it, ok := menuItem(m, "Ollama"); !ok || it.Value != "ollama" || !it.Current {
		t.Errorf("provider menu should offer Ollama as the current provider: %+v ok=%v", it, ok)
	}

	// With an arg it sets directly (no menu) and confirms.
	srv.runCommand(sess, "/provider ollama")
	if !hasInfo(sess, "Provider set to Ollama") {
		t.Error("/provider ollama should switch and confirm")
	}
	if got := provider.Normalize(sess.agent.Config().LLMProvider); got != "ollama" {
		t.Errorf("LLMProvider = %q, want ollama", got)
	}
}

func TestSlashTokensAndUnknown(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/tokens")
	if !hasInfo(sess, "Tokens this session") {
		t.Error("/tokens should report usage")
	}
	srv.runCommand(sess, "/bogus")
	if !hasInfo(sess, "Unknown command") {
		t.Error("an unknown command should say so")
	}
}

// TestSlashUpdateHeadless covers the /update command on a daemon-hosted session:
// it is offered only when a restart hook is wired, and when the build is already
// current it reports so without applying an update or restarting.
func TestSlashUpdateHeadless(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// Without a restart hook (terminal-hosted server) /update stays hidden.
	if commandsHave(srv.commandsEvent(sess).Commands, "/update") {
		t.Error("/update should be hidden when no restart hook is wired")
	}

	restarted := make(chan struct{}, 1)
	srv.SetUpdateRestart(func() { restarted <- struct{}{} })
	if !commandsHave(srv.commandsEvent(sess).Commands, "/update") {
		t.Error("/update should be offered once a restart hook is wired (headless host)")
	}

	// Relay reports our own version → already current: no apply, no restart.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/relay" {
			fmt.Fprintf(w, `{"version":%q}`, config.Version)
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()
	t.Setenv("MOONY_UPDATE_URL", stub.URL)

	srv.runSelfUpdate(sess)
	if !hasInfo(sess, "Already on the latest") {
		t.Error("expected an 'already on the latest' notice")
	}
	select {
	case <-restarted:
		t.Error("restart must not fire when already current")
	default:
	}
}

// TestSlashUpdateBehindFailsCleanly checks the behind-but-undownloadable path:
// it reports a failure and does NOT restart (so a failed update never bounces
// the daemon).
func TestSlashUpdateBehindFailsCleanly(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)
	restarted := make(chan struct{}, 1)
	srv.SetUpdateRestart(func() { restarted <- struct{}{} })

	// Newer version advertised, but no binary served → Apply fails.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/relay" {
			fmt.Fprint(w, `{"version":"999.0.0"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()
	t.Setenv("MOONY_UPDATE_URL", stub.URL)

	srv.runSelfUpdate(sess)
	if !hasInfo(sess, "update failed") {
		t.Error("expected an update-failure notice when the download is unavailable")
	}
	select {
	case <-restarted:
		t.Error("restart must not fire when the update did not apply")
	default:
	}
}

func commandsHave(cmds []cmdWire, name string) bool {
	for _, c := range cmds {
		if c.Name == name {
			return true
		}
	}
	return false
}

// TestMCPReadyGate covers the first-turn MCP-readiness signal: waitMCPReady
// blocks until the session is marked ready (or the timeout elapses), markMCPReady
// is idempotent, and a non-positive timeout never blocks.
func TestMCPReadyGate(t *testing.T) {
	sess := newSession("id", "t", nil)

	// Not ready yet → waitMCPReady blocks until its timeout.
	start := time.Now()
	sess.waitMCPReady(60 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("waitMCPReady returned too early (%v); expected to wait ~60ms", elapsed)
	}

	// Once ready, it returns immediately; markMCPReady is safe to call twice.
	sess.markMCPReady()
	sess.markMCPReady() // must not panic (sync.Once guards the close)
	start = time.Now()
	sess.waitMCPReady(2 * time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("waitMCPReady should return immediately once ready; took %v", elapsed)
	}

	// A non-positive timeout disables waiting even when not ready.
	fresh := newSession("id2", "t", nil)
	start = time.Now()
	fresh.waitMCPReady(0)
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("waitMCPReady(0) should not block; took %v", elapsed)
	}
}

// TestConnectMCPMarksReady verifies that opening a session (which connects MCP in
// the background) closes the readiness channel, so the first turn doesn't stall
// for the full timeout.
func TestConnectMCPMarksReady(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key) // fires `go connectMCP` (no servers in the temp project)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-sess.mcpReady:
			return // closed → ready
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("connectMCP did not mark the session MCP-ready within 3s")
}

// TestSlashCommandViaMessageEndpoint confirms a "/"-message is intercepted and
// not sent to the model as a turn.
func TestSlashCommandViaMessageEndpoint(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()
	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	// Open the live session first so the command's events land in its history.
	sess := srv.openSession(created.ID)
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"/help"}`)

	waitFor(t, sess, "help info", func(e wireEvent) bool {
		return e.Type == "info" && strings.Contains(e.Text, "/reset")
	})
	if sess.snapshot().State != stateIdle {
		t.Error("a slash command must not start a turn (session should stay idle)")
	}
}

// TestSlashSkillsAndMcps confirms /skills emits a clickable menu of discovered
// skills and /mcps a menu of MCP servers from config (incl. Claude-discovered).
func TestSlashSkillsAndMcps(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	home := os.Getenv("HOME")
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: A demo skill\n---\nbody")
	mustWrite(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"fsserver":{"command":"npx"}}}`)

	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/skills")
	if _, ok := menuItem(lastMenu(sess, "/skills"), "demo"); !ok {
		t.Error("/skills should emit a menu listing the demo skill")
	}
	srv.runCommand(sess, "/mcps")
	if _, ok := menuItem(lastMenu(sess, "/mcps"), "fsserver"); !ok {
		t.Error("/mcps should emit a menu listing the configured fsserver")
	}
}

// TestSlashAgents confirms /agents emits a clickable menu of subagent personas
// and that disabling one via the command is reflected in the next menu.
func TestSlashAgents(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".sqe", "agents", "explore.md"),
		"---\ndescription: read-only explorer\ntools: read_file, grep\n---\nYou explore.")
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/agents")
	if _, ok := menuItem(lastMenu(sess, "/agents"), "explore"); !ok {
		t.Error("/agents should emit a menu listing the explore persona")
	}
	srv.runCommand(sess, "/agents disable explore")
	srv.runCommand(sess, "/agents")
	if it, ok := menuItem(lastMenu(sess, "/agents"), "explore"); !ok || it.Current {
		t.Errorf("disabled persona should show off in the menu: %+v ok=%v", it, ok)
	}
}

// TestSlashLSP confirms /lsp emits a clickable toggle menu of configured language
// servers and that disabling one via the command is reflected in the next menu.
func TestSlashLSP(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".sqe", "lsp.json"),
		`{"servers":{"gopls":{"command":"gopls","extensions":[".go"]}}}`)
	sess := openLive(t, srv, key)

	srv.runCommand(sess, "/lsp")
	if it, ok := menuItem(lastMenu(sess, "/lsp"), "gopls"); !ok || !it.Current {
		t.Errorf("/lsp should list gopls as enabled: %+v ok=%v", it, ok)
	}
	srv.runCommand(sess, "/lsp disable gopls")
	srv.runCommand(sess, "/lsp")
	if it, ok := menuItem(lastMenu(sess, "/lsp"), "gopls"); !ok || it.Current {
		t.Errorf("disabled language server should show off in the menu: %+v ok=%v", it, ok)
	}
}

// lspCatalog GETs the marketplace and returns the parsed rows.
func lspCatalog(t *testing.T, srv *Server, sessID string) []lspCatalogItem {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sessID+"/lsp/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog GET = %d: %s", rec.Code, rec.Body)
	}
	var items []lspCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return items
}

func findCatalogItem(items []lspCatalogItem, name string) (lspCatalogItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return lspCatalogItem{}, false
}

// --- skills marketplace ---

func skillsCatalog(t *testing.T, srv *Server, sessID string) []skillCatalogItem {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sessID+"/skills/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("skills catalog GET = %d: %s", rec.Code, rec.Body)
	}
	var items []skillCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode skills catalog: %v", err)
	}
	return items
}

func findSkill(items []skillCatalogItem, name string) (skillCatalogItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return skillCatalogItem{}, false
}

func TestSkillsCatalogListsCatalogAndCustom(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, _ := decodeProjectKey(key)
	mustWrite(t, filepath.Join(dir, ".claude", "skills", "myskill", "SKILL.md"),
		"---\nname: myskill\ndescription: a custom skill\n---\nDo the thing.")
	sess := openLive(t, srv, key)

	items := skillsCatalog(t, srv, sess.id)
	if c, ok := findSkill(items, "conventional-commits"); !ok || c.Installed {
		t.Errorf("a catalog skill should be offered but not installed: %+v ok=%v", c, ok)
	}
	if m, ok := findSkill(items, "myskill"); !ok || !m.Installed || !m.Custom {
		t.Errorf("custom discovered skill should show installed+custom: %+v ok=%v", m, ok)
	}
}

func TestSkillInstallToggleRemove(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/skills/install", `{"name":"conventional-commits"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	if s, ok := findSkill(skillsCatalog(t, srv, sess.id), "conventional-commits"); !ok || !s.Installed || !s.Enabled {
		t.Fatalf("skill should be installed + enabled: %+v", s)
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/skills/toggle", `{"name":"conventional-commits","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", rec.Code, rec.Body)
	}
	if s, _ := findSkill(skillsCatalog(t, srv, sess.id), "conventional-commits"); s.Enabled {
		t.Error("skill should be disabled after toggle off")
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/skills/remove", `{"name":"conventional-commits"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if s, _ := findSkill(skillsCatalog(t, srv, sess.id), "conventional-commits"); s.Installed {
		t.Error("skill should be uninstalled after remove")
	}
}

// --- mcp marketplace ---

func mcpsCatalog(t *testing.T, srv *Server, sessID string) []mcpCatalogItem {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sessID+"/mcps/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("mcps catalog GET = %d: %s", rec.Code, rec.Body)
	}
	var items []mcpCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode mcps catalog: %v", err)
	}
	return items
}

func findMcp(items []mcpCatalogItem, name string) (mcpCatalogItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return mcpCatalogItem{}, false
}

func TestMcpsCatalogListsCatalogAndCustom(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, _ := decodeProjectKey(key)
	mustWrite(t, filepath.Join(dir, ".sqe", "mcp.json"),
		`{"servers":{"mymcp":{"command":"mymcp"}}}`)
	sess := openLive(t, srv, key)

	items := mcpsCatalog(t, srv, sess.id)
	if c, ok := findMcp(items, "filesystem"); !ok || c.Installed {
		t.Errorf("a catalog server should be offered but not installed: %+v ok=%v", c, ok)
	}
	if m, ok := findMcp(items, "mymcp"); !ok || !m.Installed || !m.Custom {
		t.Errorf("custom configured server should show installed+custom: %+v ok=%v", m, ok)
	}
}

func TestMcpInstallToggleRemove(t *testing.T) {
	// newTestServer sets MOONY_MCP=0, so connect is a no-op — install just writes
	// the config, which is what we assert here (no npx spawn in tests).
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/mcps/install", `{"name":"memory"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	if m, ok := findMcp(mcpsCatalog(t, srv, sess.id), "memory"); !ok || !m.Installed || !m.Enabled {
		t.Fatalf("memory should be installed + enabled: %+v", m)
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/mcps/toggle", `{"name":"memory","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", rec.Code, rec.Body)
	}
	if m, _ := findMcp(mcpsCatalog(t, srv, sess.id), "memory"); m.Enabled {
		t.Error("memory should be disabled after toggle off")
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/mcps/remove", `{"name":"memory"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if m, _ := findMcp(mcpsCatalog(t, srv, sess.id), "memory"); m.Installed {
		t.Error("memory should be uninstalled after remove")
	}
}

// --- agents marketplace ---

func agentsCatalog(t *testing.T, srv *Server, sessID string) []agentCatalogItem {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sessID+"/agents/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("agents catalog GET = %d: %s", rec.Code, rec.Body)
	}
	var items []agentCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode agents catalog: %v", err)
	}
	return items
}

func findAgent(items []agentCatalogItem, name string) (agentCatalogItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return agentCatalogItem{}, false
}

func TestAgentsCatalogListsCatalogAndCustom(t *testing.T) {
	t.Setenv("MOONY_IMPORT_EXTERNAL_AGENTS", "0") // keep discovery to sqe dirs only
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, _ := decodeProjectKey(key)
	mustWrite(t, filepath.Join(dir, ".sqe", "agents", "myagent.md"),
		"---\ndescription: a custom agent\n---\nDo the thing.")
	sess := openLive(t, srv, key)

	items := agentsCatalog(t, srv, sess.id)
	// test-author writes files, so it is NOT a built-in: offered, not yet installed.
	if c, ok := findAgent(items, "test-author"); !ok || c.Installed {
		t.Errorf("a catalog agent should be offered but not installed: %+v ok=%v", c, ok)
	}
	// explore IS an always-on built-in: shown installed and non-removable.
	if c, ok := findAgent(items, "explore"); !ok || !c.Installed || c.Removable {
		t.Errorf("built-in explore should show installed + non-removable: %+v ok=%v", c, ok)
	}
	if m, ok := findAgent(items, "myagent"); !ok || !m.Installed || !m.Custom {
		t.Errorf("custom discovered agent should show installed+custom: %+v ok=%v", m, ok)
	}
}

func TestAgentInstallToggleRemove(t *testing.T) {
	t.Setenv("MOONY_IMPORT_EXTERNAL_AGENTS", "0")
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// test-author is a catalog entry that is NOT a built-in, so the full
	// install → toggle → remove lifecycle applies (a built-in like explore can't be
	// removed — it always falls back to the in-memory default).
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/install", `{"name":"test-author"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	if a, ok := findAgent(agentsCatalog(t, srv, sess.id), "test-author"); !ok || !a.Installed || !a.Enabled {
		t.Fatalf("test-author should be installed + enabled: %+v", a)
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/toggle", `{"name":"test-author","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, sess.id), "test-author"); a.Enabled {
		t.Error("test-author should be disabled after toggle off")
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/remove", `{"name":"test-author"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, sess.id), "test-author"); a.Installed {
		t.Error("test-author should be uninstalled after remove")
	}
}

func TestAgentSetModel(t *testing.T) {
	t.Setenv("MOONY_IMPORT_EXTERNAL_AGENTS", "0")
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/install", `{"name":"explore"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	// Pin a model — the catalog then reports it.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/model", `{"name":"explore","model":"anthropic.claude-haiku-4-5"}`); rec.Code != http.StatusOK {
		t.Fatalf("set model = %d: %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, sess.id), "explore"); a.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("explore model = %q after pin, want the pinned id", a.Model)
	}
	// Clearing it returns the persona to inherit.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/model", `{"name":"explore","model":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear model = %d: %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, sess.id), "explore"); a.Model != "" {
		t.Errorf("explore model = %q after clear, want empty", a.Model)
	}
	// A name is required.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/model", `{"model":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("set model without a name = %d, want 400", rec.Code)
	}
}

// The models endpoint falls back to the curated list when the route fetch
// returns nothing (the fake streamer's ListModels yields nil), so the picker is
// never empty.
func TestAgentModelsEndpoint(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sess.id+"/agents/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("models GET = %d: %s", rec.Code, rec.Body)
	}
	var opts []agentModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &opts); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("models endpoint should fall back to a non-empty curated list")
	}
}

// A persona discovered from another coding agent (Claude Code here) is copied
// into the sqe config when the Agents catalog loads, so it becomes editable —
// removable and model-settable — rather than a read-only import.
func TestAgentsCatalogMaterializesExternals(t *testing.T) {
	t.Setenv("MOONY_IMPORT_EXTERNAL_AGENTS", "1")
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	// newTestServer points HOME at a tempdir; drop the Claude agent there.
	mustWrite(t, filepath.Join(os.Getenv("HOME"), ".claude", "agents", "fromclaude.md"),
		"---\ndescription: a claude agent\nmodel: claude-only\n---\nDo it.")
	sess := openLive(t, srv, key)

	a, ok := findAgent(agentsCatalog(t, srv, sess.id), "fromclaude")
	if !ok {
		t.Fatal("the discovered claude agent should appear in the catalog")
	}
	if a.Source != "global" || !a.Removable {
		t.Errorf("after materialize it should be an editable global persona, got source=%q removable=%v", a.Source, a.Removable)
	}
	if a.Model != "" {
		t.Errorf("the external model id should be dropped on copy, got %q", a.Model)
	}
	// Its model can now be set (it couldn't as a read-only import).
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/agents/model", `{"name":"fromclaude","model":"anthropic.claude-haiku-4-5"}`); rec.Code != http.StatusOK {
		t.Fatalf("set model on materialized agent = %d: %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, sess.id), "fromclaude"); a.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("model = %q after pin, want the pinned id", a.Model)
	}
}

func TestLspCatalogListsCatalogAndCustom(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, _ := decodeProjectKey(key)
	mustWrite(t, filepath.Join(dir, ".sqe", "lsp.json"),
		`{"servers":{"mylsp":{"command":"mylsp","extensions":[".x"]}}}`)
	sess := openLive(t, srv, key)

	items := lspCatalog(t, srv, sess.id)
	if g, ok := findCatalogItem(items, "gopls"); !ok || g.Installed {
		t.Errorf("gopls should be offered but not installed: %+v ok=%v", g, ok)
	}
	if c, ok := findCatalogItem(items, "mylsp"); !ok || !c.Installed || !c.Custom {
		t.Errorf("custom configured server should show installed+custom: %+v ok=%v", c, ok)
	}
}

func TestLspInstallToggleRemove(t *testing.T) {
	// Pretend the binary is already on PATH so install registers synchronously
	// without running a package manager.
	orig := lsp.BinaryInstalled
	lsp.BinaryInstalled = func(string) bool { return true }
	t.Cleanup(func() { lsp.BinaryInstalled = orig })

	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/lsp/install", `{"name":"gopls"}`); rec.Code != http.StatusOK {
		t.Fatalf("install gopls = %d: %s", rec.Code, rec.Body)
	}
	if g, ok := findCatalogItem(lspCatalog(t, srv, sess.id), "gopls"); !ok || !g.Installed || !g.Enabled {
		t.Fatalf("gopls should be installed + enabled after install: %+v", g)
	}

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/lsp/toggle", `{"name":"gopls","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", rec.Code, rec.Body)
	}
	if g, _ := findCatalogItem(lspCatalog(t, srv, sess.id), "gopls"); g.Enabled {
		t.Error("gopls should be disabled after toggle off")
	}

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/lsp/remove", `{"name":"gopls"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if g, _ := findCatalogItem(lspCatalog(t, srv, sess.id), "gopls"); g.Installed {
		t.Error("gopls should be uninstalled after remove")
	}
}

func TestWorkerInstall(t *testing.T) {
	// Stub the install runner so no real curl|bash/npm runs; capture the command.
	ran := make(chan string, 1)
	orig := workerInstallRun
	workerInstallRun = func(_ context.Context, cmd string) (string, error) { ran <- cmd; return "ok", nil }
	t.Cleanup(func() { workerInstallRun = orig })

	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// Attach a worker whose engine command is deliberately absent from PATH, so
	// the handler always takes the install path regardless of this machine.
	mgr := worker.New(t.TempDir(), []worker.Config{
		{Name: "rev", Backend: worker.BackendClaude, Command: "sqe-test-absent-engine"},
	})
	sess.agent.AttachWorkers(mgr)

	rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/workers/install", `{"name":"rev"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	if !hasInfo(sess, "Installing") {
		t.Error("expected an 'Installing …' transcript event")
	}
	select {
	case got := <-ran:
		if got != worker.InstallCommand(worker.BackendClaude) {
			t.Errorf("ran %q, want the claude install one-liner", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("install runner was not invoked")
	}

	// Unknown worker → 400.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/workers/install", `{"name":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown worker install = %d, want 400", rec.Code)
	}
}

func TestPluginInstallRemove(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// review-suite bundles skills + an agent + a command (no MCP), so install is
	// pure file writes — no npx/connect to stub.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/plugins/install", `{"name":"review-suite"}`); rec.Code != http.StatusOK {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	if p, ok := findPlugin(pluginCatalog(t, srv, sess.id), "review-suite"); !ok || !p.Installed {
		t.Fatalf("review-suite should be installed: %+v", p)
	}

	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/plugins/remove", `{"name":"review-suite"}`); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if p, _ := findPlugin(pluginCatalog(t, srv, sess.id), "review-suite"); p.Installed {
		t.Error("review-suite should be uninstalled after remove")
	}

	// Unknown plugin → 400.
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/plugins/install", `{"name":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown plugin install = %d, want 400", rec.Code)
	}
}

func TestPluginMarketplaceEndpoints(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// No marketplaces added yet → empty list, 200.
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sess.id+"/plugins/marketplaces", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("marketplaces GET = %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Marketplaces []any `json:"marketplaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(resp.Marketplaces) != 0 {
		t.Errorf("expected no marketplaces, got %d", len(resp.Marketplaces))
	}

	// Adding with no URL is a fast 400 (no git/network needed).
	if rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+sess.id+"/plugins/marketplace/add", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("add with empty url = %d, want 400", rec.Code)
	}
}

func pluginCatalog(t *testing.T, srv *Server, sessID string) []pluginCatalogItem {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/sessions/"+sessID+"/plugins/catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("plugins catalog GET = %d: %s", rec.Code, rec.Body)
	}
	var items []pluginCatalogItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode plugins catalog: %v", err)
	}
	return items
}

func findPlugin(items []pluginCatalogItem, name string) (pluginCatalogItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return pluginCatalogItem{}, false
}

// lastMenu returns the most recent menu event published for command, or nil.
func lastMenu(sess *session, command string) *menuWire {
	var m *menuWire
	// Transient menus aren't kept in history; drain the live subscriber channel
	// (set up by openLive) for the latest one matching command.
	if ch := menuChans[sess]; ch != nil {
		for {
			select {
			case e := <-ch:
				if e.Type == "menu" && e.Menu != nil && e.Menu.Command == command {
					m = e.Menu
				}
				continue
			default:
			}
			break
		}
	}
	if m != nil {
		return m
	}
	// Fall back to history (covers sessions opened without openLive).
	for _, e := range history(sess) {
		if e.Type == "menu" && e.Menu != nil && e.Menu.Command == command {
			m = e.Menu
		}
	}
	return m
}

// menuItem finds a menu item by its label (nil-safe).
func menuItem(m *menuWire, label string) (menuItemWire, bool) {
	if m == nil {
		return menuItemWire{}, false
	}
	for _, it := range m.Items {
		if it.Label == label {
			return it, true
		}
	}
	return menuItemWire{}, false
}

// TestMcpsListingShowsConnectionStatus checks the /mcps listing annotates each
// server with its live connection state: a failed server shows the error, and an
// enabled server with no manager yet shows "not connected".
func TestMcpsListingShowsConnectionStatus(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"broken":{"command":"does-not-exist"}}}`)
	sess := openLive(t, srv, key)

	// No manager connected yet → enabled server reads as "not connected".
	if out := srv.mcpsCommand(sess, ""); !strings.Contains(out, "not connected") {
		t.Errorf("expected 'not connected' before connect, got:\n%s", out)
	}

	// Inject a manager whose only server failed to connect → listing shows why.
	sess.setMCP(mcp.ConnectAll(context.Background(), []mcp.Config{{Name: "broken"}}, nil))
	if out := srv.mcpsCommand(sess, ""); !strings.Contains(out, "failed:") {
		t.Errorf("expected 'failed:' after a failed connect, got:\n%s", out)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestManageSkillsAndMcps exercises enable/disable via /skills and /mcps.
func TestManageSkillsAndMcps(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	home := os.Getenv("HOME")
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: A demo skill\n---\nbody")
	mustWrite(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"fsserver":{"command":"npx"}}}`)
	sess := openLive(t, srv, key)

	// Disable, confirm, and see it marked off in the toggle menu.
	srv.runCommand(sess, "/skills disable demo")
	if !hasInfo(sess, `Skill "demo" disabled`) {
		t.Error("/skills disable should confirm")
	}
	srv.runCommand(sess, "/skills")
	if it, ok := menuItem(lastMenu(sess, "/skills"), "demo"); !ok || it.Current || it.Value != "enable demo" {
		t.Errorf("disabled skill should show off (click re-enables) in the menu: %+v ok=%v", it, ok)
	}
	// Re-enable and see it back on.
	srv.runCommand(sess, "/skills enable demo")
	srv.runCommand(sess, "/skills")
	if it, ok := menuItem(lastMenu(sess, "/skills"), "demo"); !ok || !it.Current || it.Value != "disable demo" {
		t.Errorf("re-enabled skill should show on (click disables) in the menu: %+v ok=%v", it, ok)
	}

	// Same for MCP servers.
	srv.runCommand(sess, "/mcps disable fsserver")
	if !hasInfo(sess, `MCP server "fsserver" disabled`) {
		t.Error("/mcps disable should confirm")
	}
	srv.runCommand(sess, "/mcps")
	if it, ok := menuItem(lastMenu(sess, "/mcps"), "fsserver"); !ok || it.Current {
		t.Errorf("disabled MCP server should show off in the menu: %+v ok=%v", it, ok)
	}
}

// TestManageAddRemove exercises /skills new|remove and /mcps add|remove.
func TestManageAddRemove(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	sess := openLive(t, srv, key)

	// Scaffold a project skill, see it listed, then remove it.
	srv.runCommand(sess, "/skills new mytool")
	if !hasInfo(sess, "Created skill at") {
		t.Error("/skills new should confirm")
	}
	srv.runCommand(sess, "/skills")
	if it, ok := menuItem(lastMenu(sess, "/skills"), "mytool"); !ok || !it.Current {
		t.Errorf("scaffolded skill should appear (on) in the menu: %+v ok=%v", it, ok)
	}
	srv.runCommand(sess, "/skills remove mytool")
	if !hasInfo(sess, "Removed skill at") {
		t.Error("/skills remove should confirm")
	}

	// Add an MCP server, see it listed, then remove it.
	srv.runCommand(sess, "/mcps add fs -- npx -y server-fs")
	if !hasInfo(sess, `Added MCP server "fs"`) {
		t.Error("/mcps add should confirm")
	}
	srv.runCommand(sess, "/mcps")
	if it, ok := menuItem(lastMenu(sess, "/mcps"), "fs"); !ok || !it.Current {
		t.Errorf("added MCP server should appear (on) in the menu: %+v ok=%v", it, ok)
	}
	srv.runCommand(sess, "/mcps remove fs")
	if !hasInfo(sess, "Removed MCP server from") {
		t.Error("/mcps remove should confirm")
	}
}

// TestMcpsReload drives the /mcps reload command path (no servers configured, so
// it connects zero — but exercises the reconnect plumbing end to end).
func TestMcpsReload(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	t.Setenv("MOONY_MCP", "1") // re-enable connect for this test (no configs to spawn)
	sess := openLive(t, srv, key)
	srv.runCommand(sess, "/mcps reload")
	waitFor(t, sess, "reload info", func(e wireEvent) bool {
		return e.Type == "info" && strings.Contains(e.Text, "MCP reloaded")
	})
}

// TestManagePanelEndpoints covers the GET list endpoints the manage panel reads,
// and that a toggle via the message endpoint (the panel's mutation path) is
// reflected on the next list.
func TestManagePanelEndpoints(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()
	home := os.Getenv("HOME")
	dir, err := decodeProjectKey(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: A demo\n---\nbody")
	mustWrite(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"fs":{"command":"npx"}}}`)

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	rec = postJSON(t, h, "GET", "/api/sessions/"+created.ID+"/skills", "")
	var sk []skillItem
	if err := json.Unmarshal(rec.Body.Bytes(), &sk); err != nil {
		t.Fatalf("skills: %v (%s)", err, rec.Body)
	}
	if len(sk) != 1 || sk[0].Name != "demo" || !sk[0].Enabled {
		t.Fatalf("skills list = %+v", sk)
	}

	rec = postJSON(t, h, "GET", "/api/sessions/"+created.ID+"/mcps", "")
	var ms []mcpItem
	json.Unmarshal(rec.Body.Bytes(), &ms)
	if len(ms) != 1 || ms[0].Name != "fs" || ms[0].Target != "npx" {
		t.Fatalf("mcps list = %+v", ms)
	}

	// Toggle via the message endpoint (how the panel mutates), then re-list.
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"/skills disable demo"}`)
	rec = postJSON(t, h, "GET", "/api/sessions/"+created.ID+"/skills", "")
	json.Unmarshal(rec.Body.Bytes(), &sk)
	if sk[0].Enabled {
		t.Error("demo should report disabled after the toggle command")
	}
}

func TestListDirAndOpenSession(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)

	root := t.TempDir()
	for _, d := range []string{"alpha", "beta", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "afile.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// List: directories only, dotdirs + files skipped, sorted, parent set.
	rec := postJSON(t, srv.Handler(), "GET", "/api/fs?dir="+root, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fs list = %d: %s", rec.Code, rec.Body)
	}
	var listing dirListing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range listing.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "alpha,beta" {
		t.Errorf("entries = %v, want [alpha beta]", names)
	}
	if listing.Dir != root || listing.Parent == "" {
		t.Errorf("dir/parent wrong: %+v", listing)
	}

	// A missing directory is a 400.
	if rec := postJSON(t, srv.Handler(), "GET", "/api/fs?dir="+filepath.Join(root, "nope"), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("fs list of missing dir = %d, want 400", rec.Code)
	}

	// Open a session in a subfolder → returns id + matching project key.
	target := filepath.Join(root, "alpha")
	body, _ := json.Marshal(map[string]string{"dir": target})
	rec = postJSON(t, srv.Handler(), "POST", "/api/projects/open", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("open = %d: %s", rec.Code, rec.Body)
	}
	var res struct{ ID, Key, Dir string }
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.ID == "" || res.Dir != target || res.Key != projectKey(target) {
		t.Errorf("open result = %+v, want dir=%s key=%s", res, target, projectKey(target))
	}

	// Opening a non-directory is a 400.
	fbody, _ := json.Marshal(map[string]string{"dir": filepath.Join(root, "afile.txt")})
	if rec := postJSON(t, srv.Handler(), "POST", "/api/projects/open", string(fbody)); rec.Code != http.StatusBadRequest {
		t.Errorf("open of a file = %d, want 400", rec.Code)
	}
}

func TestEventStreamKeepalive(t *testing.T) {
	// Shrink the keepalive so the test sees several ticks quickly.
	old := sseKeepalive
	sseKeepalive = 5 * time.Millisecond
	t.Cleanup(func() { sseKeepalive = old })

	srv, key := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/sessions", "")
	var info struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.ID == "" {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}

	// Open the (idle) event stream briefly; it must emit keepalive comments so a
	// proxy/LB won't reap it. ServeHTTP blocks until the context times out.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/sessions/"+info.ID+"/events", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), ": keepalive") {
		t.Errorf("expected a keepalive comment in the idle event stream, got: %q", rr.Body.String())
	}
}

func TestHandleEncryption(t *testing.T) {
	for _, k := range []string{"MOONY_SESSION_ENCRYPT", "MOONY_SESSION_KEY", "MOONY_SESSION_KEY_CMD", "MOONY_SESSION_KEY_FILE", "MOONY_SESSION_PASSPHRASE"} {
		os.Unsetenv(k)
	}
	// Default (auto) → encrypted true.
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, t.TempDir(), &config.Config{Model: "m"}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/encryption", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		Encrypted bool   `json:"encrypted"`
		Mode      string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Encrypted || got.Mode != "auto" {
		t.Errorf("got %+v, want encrypted=true mode=auto", got)
	}

	// off → encrypted false.
	t.Setenv("MOONY_SESSION_ENCRYPT", "off")
	store2, _ := sessionpkg.NewStoreAt(t.TempDir())
	srv2 := NewServer(store2, t.TempDir(), &config.Config{Model: "m"}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	rec2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/api/encryption", nil))
	json.Unmarshal(rec2.Body.Bytes(), &got)
	if got.Encrypted {
		t.Errorf("off mode should report encrypted=false")
	}
}
