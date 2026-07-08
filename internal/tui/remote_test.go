package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// endTurnStreamer streams one text delta and ends the turn — enough to exercise
// the run loop deterministically without touching the network.
type endTurnStreamer struct{}

func (endTurnStreamer) StreamMessages(_ context.Context, _ string, _ []llm.Message, _ []llm.Tool, h llm.Handler) (*llm.Response, error) {
	if h.OnTextDelta != nil {
		h.OnTextDelta("hi there")
	}
	return &llm.Response{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hi there")}}, nil
}
func (endTurnStreamer) ListModels(context.Context) ([]config.ModelInfo, error) { return nil, nil }
func (endTurnStreamer) Usage(context.Context) (*llm.UsageResponse, error)      { return nil, nil }
func (endTurnStreamer) CheckAuth(context.Context) error                        { return nil }

// mirrorModel builds a sized model with a sandboxed agent and an attached hub.
func mirrorModel(t *testing.T) (Model, *bridge.Hub) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()

	cfg := config.Load()
	cfg.WorkingDir = dir
	reg := tools.Default(dir, cfg.MaxToolOutputBytes)
	ag := agent.New(cfg, endTurnStreamer{}, reg, nil, nil)
	m := New(cfg, ag, nil)
	hub := bridge.New()
	m.SetHub(hub)
	return sized(t, m, 100, 30), hub
}

func recvMsg(t *testing.T, ch <-chan bridge.Msg) bridge.Msg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a mirror message")
		return bridge.Msg{}
	}
}

// TestRemoteSubmitDrivesAgentAndMirrors checks that a browser-originated prompt
// starts a turn locally and is echoed to web viewers (user prompt + busy state).
func TestRemoteSubmitDrivesAgentAndMirrors(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.Update(remoteSubmitMsg{text: "hello"})
	m = next.(Model)

	if m.state != stateBusy {
		t.Fatalf("state = %v, want busy after remote submit", m.state)
	}
	if last := m.trans.entries[len(m.trans.entries)-1]; last.kind != entryUser || last.text != "hello" {
		t.Fatalf("last transcript entry = {kind:%v text:%q}, want user/hello", last.kind, last.text)
	}

	// The hub mirrors the user echo, then the busy state (in that order).
	if msg := recvMsg(t, ch); msg.Kind != bridge.KindUser || msg.Text != "hello" {
		t.Fatalf("first mirror msg = %+v, want user/hello", msg)
	}
	if msg := recvMsg(t, ch); msg.Kind != bridge.KindState || msg.State != bridge.StateBusy {
		t.Fatalf("second mirror msg = %+v, want state/busy", msg)
	}
}

// TestRemoteSubmitIgnoredWhileBusy ensures a remote prompt can't start a second
// concurrent turn.
func TestRemoteSubmitIgnoredWhileBusy(t *testing.T) {
	m, _ := mirrorModel(t)
	m.state = stateBusy
	before := len(m.trans.entries)

	next, _ := m.Update(remoteSubmitMsg{text: "nope"})
	m = next.(Model)
	if len(m.trans.entries) != before {
		t.Error("remote submit while busy should be ignored")
	}
}

// TestRemoteDecisionAnswersApproval checks a browser approval unblocks the
// agent and flips the session back to busy (mirrored to viewers).
func TestRemoteDecisionAnswersApproval(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()
	reply := make(chan agent.Decision, 1)
	m.state = stateAwaitingApproval
	m.pending = &agent.ApprovalRequest{Tool: "write_file", Reply: reply}

	next, _ := m.Update(remoteDecisionMsg{d: agent.ApproveAlways})
	m = next.(Model)

	select {
	case d := <-reply:
		if d != agent.ApproveAlways {
			t.Fatalf("decision = %v, want ApproveAlways", d)
		}
	case <-time.After(time.Second):
		t.Fatal("approval reply not delivered to the agent")
	}
	if m.state != stateBusy {
		t.Errorf("state = %v, want busy after decision", m.state)
	}
	if msg := recvMsg(t, ch); msg.Kind != bridge.KindState || msg.State != bridge.StateBusy {
		t.Errorf("mirror msg = %+v, want state/busy", msg)
	}
}

// TestRemoteSlashCommandRunsAndMirrors checks a browser-sent slash command is
// executed via the command handler and its output is mirrored to viewers.
func TestRemoteSlashCommandRunsAndMirrors(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.Update(remoteSubmitMsg{text: "/help"})
	m = next.(Model)

	// /help stays idle (no model turn) and prints help locally...
	if m.state != stateIdle {
		t.Fatalf("state = %v, want idle after /help", m.state)
	}
	if last := m.trans.entries[len(m.trans.entries)-1]; last.kind != entryInfo {
		t.Fatalf("expected a local info entry from /help, got kind=%v", last.kind)
	}
	// ...and mirrors that help text to viewers as an info event.
	var sawInfo bool
	for drained := false; !drained; {
		select {
		case msg := <-ch:
			if msg.Kind == bridge.KindAgent {
				if _, ok := msg.Event.(agent.Info); ok {
					sawInfo = true
				}
			}
		case <-time.After(200 * time.Millisecond):
			drained = true
		}
	}
	if !sawInfo {
		t.Error("/help output was not mirrored to web viewers")
	}
}

// TestRemoteQuitIsBlocked ensures a browser cannot quit the whole app.
func TestRemoteQuitIsBlocked(t *testing.T) {
	m, _ := mirrorModel(t)
	next, cmd := m.Update(remoteSubmitMsg{text: "/exit"})
	m = next.(Model)
	if cmd != nil {
		t.Error("/exit from web must not return a command (would quit the app)")
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want idle (exit blocked)", m.state)
	}
}

// sessionModel builds a model backed by a real session store containing one
// extra saved session for the working dir, and returns its id.
func sessionModel(t *testing.T) (Model, *bridge.Hub, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()

	cfg := config.Load()
	cfg.WorkingDir = dir
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := session.New(cfg.Model, dir)
	other.Messages = []llm.Message{llm.UserText("previous question")}
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}
	reg := tools.Default(dir, cfg.MaxToolOutputBytes)
	ag := agent.New(cfg, endTurnStreamer{}, reg, store, nil)
	m := New(cfg, ag, store)
	hub := bridge.New()
	m.SetHub(hub)
	return sized(t, m, 100, 30), hub, other.ID
}

// TestSwitchSessionReplaysToWeb checks switching sessions rebuilds the local
// transcript and resets+replays it to web viewers.
func TestSwitchSessionReplaysToWeb(t *testing.T) {
	m, hub, otherID := sessionModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.switchSession(otherID)
	m = next.(Model)

	if m.agent.SessionID() != otherID {
		t.Fatalf("agent on session %q, want %q", m.agent.SessionID(), otherID)
	}
	foundUser := false
	for _, e := range m.trans.entries {
		if e.kind == entryUser && e.text == "previous question" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Error("transcript was not rebuilt from the switched session's history")
	}

	var sawReset, sawUser bool
	for drained := false; !drained; {
		select {
		case msg := <-ch:
			switch {
			case msg.Kind == bridge.KindReset:
				sawReset = true
			case msg.Kind == bridge.KindUser && msg.Text == "previous question":
				sawUser = true
			}
		case <-time.After(200 * time.Millisecond):
			drained = true
		}
	}
	if !sawReset || !sawUser {
		t.Errorf("web replay incomplete: reset=%v user=%v", sawReset, sawUser)
	}
}

// TestRemoteSwitchSwitchesSession checks a browser-driven remoteSwitchMsg moves
// the terminal to the chosen session and updates the hub's active id (so the
// multi-project web server routes subsequent requests there). SetHub seeds the
// initial active id, and switching must update it.
func TestRemoteSwitchSwitchesSession(t *testing.T) {
	m, hub, otherID := sessionModel(t)

	if got := hub.ActiveSessionID(); got != m.agent.SessionID() {
		t.Fatalf("SetHub did not seed the active id: hub=%q agent=%q", got, m.agent.SessionID())
	}

	next, _ := m.Update(remoteSwitchMsg{id: otherID})
	m = next.(Model)

	if m.agent.SessionID() != otherID {
		t.Fatalf("remote switch left agent on %q, want %q", m.agent.SessionID(), otherID)
	}
	if got := hub.ActiveSessionID(); got != otherID {
		t.Fatalf("remote switch did not update the hub active id: got %q want %q", got, otherID)
	}
}

// TestRemoteSwitchIgnoredWhileBusy checks a switch request is dropped mid-turn
// (the terminal can only host one session at a time and won't switch while a
// turn is running).
func TestRemoteSwitchIgnoredWhileBusy(t *testing.T) {
	m, _, otherID := sessionModel(t)
	startID := m.agent.SessionID()
	m.state = stateBusy

	next, _ := m.Update(remoteSwitchMsg{id: otherID})
	m = next.(Model)

	if m.agent.SessionID() != startID {
		t.Fatalf("switch should be ignored while busy: agent on %q, want %q", m.agent.SessionID(), startID)
	}
}

// TestPublishSessionsMirrorsProjectSessions checks the project's saved sessions
// are mirrored to web viewers' sidebar.
func TestPublishSessionsMirrorsProjectSessions(t *testing.T) {
	m, hub, otherID := sessionModel(t)
	_, ch, _, _ := hub.Subscribe()

	m.publishSessions()

	for {
		select {
		case msg := <-ch:
			if msg.Kind == bridge.KindSessions {
				for _, s := range msg.Sessions {
					if s.ID == otherID {
						return // found the saved session in the sidebar snapshot
					}
				}
				t.Fatalf("sessions snapshot missing %s: %+v", otherID, msg.Sessions)
			}
		case <-time.After(time.Second):
			t.Fatal("no sessions snapshot was published")
		}
	}
}

// TestRemoteSessionsMirrorsMenu checks /sessions from the web mirrors a
// clickable session picker.
func TestRemoteSessionsMirrorsMenu(t *testing.T) {
	m, hub, otherID := sessionModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.Update(remoteSubmitMsg{text: "/sessions"})
	m = next.(Model)
	if m.state != stateIdle || m.menu != nil {
		t.Fatalf("remote /sessions must stay idle without a terminal menu; state=%v menu=%v", m.state, m.menu != nil)
	}
	menu := menuFromHub(t, ch)
	if menu == nil || menu.Command != "/sessions" || !hasItem(menu, otherID) {
		t.Errorf("expected a clickable /sessions menu containing %s, got %+v", otherID, menu)
	}
}

func TestRemoteInterruptCancels(t *testing.T) {
	m, _ := mirrorModel(t)
	canceled := false
	m.state = stateBusy
	m.cancel = func() { canceled = true }
	before := len(m.trans.entries)

	next, _ := m.Update(remoteInterruptMsg{})
	m = next.(Model)
	if !canceled {
		t.Error("remote interrupt did not call cancel")
	}
	if len(m.trans.entries) != before+1 {
		t.Errorf("expected an info entry noting the interrupt, entries %d -> %d", before, len(m.trans.entries))
	}
}

// menuFromHub drains the hub channel and returns the first mirrored menu.
func menuFromHub(t *testing.T, ch <-chan bridge.Msg) *bridge.Menu {
	t.Helper()
	for {
		select {
		case msg := <-ch:
			if msg.Kind == bridge.KindMenu && msg.Menu != nil {
				return msg.Menu
			}
		case <-time.After(300 * time.Millisecond):
			return nil
		}
	}
}

func hasItem(menu *bridge.Menu, value string) bool {
	if menu == nil {
		return false
	}
	for _, it := range menu.Items {
		if it.Value == value {
			return true
		}
	}
	return false
}

// TestRemoteModelsMirrorsClickableMenu checks a web-driven /models result mirrors
// a clickable picker (and stays idle) rather than opening the terminal menu.
func TestRemoteModelsMirrorsClickableMenu(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()

	m.modelsRemote = true
	next, _ := m.Update(modelsLoadedMsg{
		models: []config.ModelInfo{{ID: "model-x"}, {ID: "model-y", Name: "Model Y"}},
	})
	m = next.(Model)

	if m.menu != nil || m.state == stateMenu {
		t.Fatal("remote /models must not open the terminal picker menu")
	}
	if m.state != stateIdle {
		t.Fatalf("state = %v, want idle so the picked /model <id> works", m.state)
	}
	menu := menuFromHub(t, ch)
	if menu == nil || menu.Command != "/model" || !hasItem(menu, "model-x") {
		t.Errorf("expected a clickable /model menu containing model-x, got %+v", menu)
	}
}

// TestRemoteReasoningMirrorsMenu checks /reasoning with no argument mirrors a
// clickable level picker.
func TestRemoteReasoningMirrorsMenu(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.Update(remoteSubmitMsg{text: "/reasoning"})
	m = next.(Model)
	if m.state != stateIdle || m.menu != nil {
		t.Fatalf("remote /reasoning must stay idle without a terminal menu; state=%v menu=%v", m.state, m.menu != nil)
	}
	menu := menuFromHub(t, ch)
	if menu == nil || menu.Command != "/reasoning" || !hasItem(menu, config.ReasoningHigh) {
		t.Errorf("expected a clickable /reasoning menu, got %+v", menu)
	}
}

// TestInitReplaysResumedHistoryToWeb verifies that resuming a saved session at
// startup mirrors its prior conversation into the hub — so a browser connecting
// to the just-resumed terminal (before any new turn) sees the full history, not
// an empty transcript. Regression test for the Init() publishTranscript seed.
func TestInitReplaysResumedHistoryToWeb(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()

	cfg := config.Load()
	cfg.WorkingDir = dir
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(cfg.Model, dir)
	sess.Messages = []llm.Message{llm.UserText("previous question")}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	reg := tools.Default(dir, cfg.MaxToolOutputBytes)
	ag := agent.New(cfg, endTurnStreamer{}, reg, store, sess) // resume the saved session
	m := New(cfg, ag, store)
	hub := bridge.New()
	m.SetHub(hub)
	m = sized(t, m, 100, 30)

	m.Init() // startup publishes — must seed the resumed transcript

	// A viewer connecting now (before any turn) must see the prior message.
	_, _, hist, _ := hub.Subscribe()
	var sawUser bool
	for _, msg := range hist {
		if msg.Kind == bridge.KindUser && strings.Contains(msg.Text, "previous question") {
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatal("resumed history was not mirrored to the hub at Init — a web viewer would see an empty conversation")
	}
}

// TestCompactResultMirroredToWeb checks that when /compact finishes (async), its
// completion line is published to web viewers — otherwise a browser mirror sits
// on "Compacting older history…" forever even though the terminal finished.
func TestCompactResultMirroredToWeb(t *testing.T) {
	m, hub, _ := sessionModel(t)
	_, ch, _, _ := hub.Subscribe()

	next, _ := m.Update(compactDoneMsg{dropped: 3})
	if _, ok := next.(Model); !ok {
		t.Fatal("Update(compactDoneMsg) did not return a Model")
	}

	if !hubSawInfo(t, ch, "Compacted 3") {
		t.Fatal("compact completion was not mirrored to web viewers — a browser would stay stuck on 'Compacting…'")
	}
}

// hubSawInfo drains the hub channel briefly looking for an Info event containing want.
func hubSawInfo(t *testing.T, ch <-chan bridge.Msg, want string) bool {
	t.Helper()
	for {
		select {
		case msg := <-ch:
			if msg.Kind == bridge.KindAgent {
				if info, ok := msg.Event.(agent.Info); ok && strings.Contains(info.Text, want) {
					return true
				}
			}
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
}
