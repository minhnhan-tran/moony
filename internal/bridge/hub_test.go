package bridge

import (
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
)

func TestHistoryReplayAndFanout(t *testing.T) {
	h := New()
	h.PublishUser("first")

	// A subscriber gets the history so far and the current state.
	id1, ch1, hist, state := h.Subscribe()
	if len(hist) != 1 || hist[0].Kind != KindUser || hist[0].Text != "first" {
		t.Fatalf("history replay wrong: %+v", hist)
	}
	if state != StateIdle {
		t.Fatalf("initial state = %q, want idle", state)
	}

	// A second subscriber sees the same history.
	_, ch2, hist2, _ := h.Subscribe()
	if len(hist2) != 1 {
		t.Fatalf("second subscriber history = %d, want 1", len(hist2))
	}

	// New events fan out to both with increasing Seq.
	h.PublishEvent(agent.TextDelta{Text: "second"})
	for _, ch := range []<-chan Msg{ch1, ch2} {
		select {
		case m := <-ch:
			if m.Kind != KindAgent || m.Seq != 2 {
				t.Errorf("live msg = %+v, want agent seq=2", m)
			}
			if td, ok := m.Event.(agent.TextDelta); !ok || td.Text != "second" {
				t.Errorf("live event = %#v, want TextDelta{second}", m.Event)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive the live event")
		}
	}

	h.Unsubscribe(id1)
	if _, ok := <-ch1; ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

func TestStateTracking(t *testing.T) {
	h := New()
	if h.State() != StateIdle {
		t.Fatalf("state = %q, want idle", h.State())
	}
	h.PublishState(StateBusy)
	if h.State() != StateBusy {
		t.Fatalf("state = %q, want busy", h.State())
	}
	// A late subscriber learns the current state at subscribe time.
	_, _, _, state := h.Subscribe()
	if state != StateBusy {
		t.Fatalf("late subscriber state = %q, want busy", state)
	}
}

func TestControllerViewOnly(t *testing.T) {
	h := New() // no controller wired
	if h.Submit("hi") {
		t.Error("Submit should fail without a controller")
	}
	h.PublishState(StateApproval)
	if h.Approve(agent.Approve) {
		t.Error("Approve should fail without a controller")
	}
	h.Interrupt() // must not panic
}

func TestControllerForwarding(t *testing.T) {
	h := New()
	var gotText string
	var gotDecision agent.Decision
	var interrupted bool
	h.SetController(
		func(s string) { gotText = s },
		func(d agent.Decision) { gotDecision = d },
		func() { interrupted = true },
		nil,
	)

	// Submit is allowed only when idle.
	if !h.Submit("build it") || gotText != "build it" {
		t.Fatalf("Submit not forwarded: ok text=%q", gotText)
	}

	// While busy, a submit is rejected (the owning UI is the authority, but the
	// hub guards for nicer client feedback).
	h.PublishState(StateBusy)
	if h.Submit("again") {
		t.Error("Submit should be rejected while busy")
	}

	// Approve is allowed only while awaiting approval.
	if h.Approve(agent.Approve) {
		t.Error("Approve should be rejected when not awaiting approval")
	}
	h.PublishState(StateApproval)
	if !h.Approve(agent.ApproveAlways) || gotDecision != agent.ApproveAlways {
		t.Fatalf("Approve not forwarded: decision=%v", gotDecision)
	}

	h.Interrupt()
	if !interrupted {
		t.Error("Interrupt not forwarded")
	}
}

func TestAnswerForwarding(t *testing.T) {
	h := New()
	var got []string
	h.SetAnswerController(func(labels []string) { got = labels })

	// Answer is rejected unless a question is pending.
	if h.Answer([]string{"x"}) {
		t.Error("Answer should be rejected when not awaiting a question")
	}
	h.PublishState(StateQuestion)
	if !h.Answer([]string{"A", "B"}) {
		t.Fatal("Answer should be forwarded while awaiting a question")
	}
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("Answer not forwarded: %v", got)
	}
}

func TestAnswerViewOnly(t *testing.T) {
	h := New() // no answer controller wired
	h.PublishState(StateQuestion)
	if h.Answer([]string{"x"}) {
		t.Error("Answer should fail without an answer controller")
	}
}

// TestSubmitSideRunsWhileBusy: unlike Submit, SubmitSide forwards regardless of
// state so a /btw side-question can run alongside the active turn.
func TestSubmitSideRunsWhileBusy(t *testing.T) {
	h := New()
	var got string
	h.SetController(func(s string) { got = s }, nil, nil, nil)
	h.PublishState(StateBusy)

	if h.Submit("queued?") {
		t.Error("Submit should be rejected while busy")
	}
	if !h.SubmitSide("/btw ping") || got != "/btw ping" {
		t.Fatalf("SubmitSide not forwarded while busy: got %q", got)
	}

	// View-only: SubmitSide reports false with no controller.
	if New().SubmitSide("/btw x") {
		t.Error("SubmitSide should fail without a controller")
	}
}

// TestQueuePublishReplayAndControl: PublishQueue retains the latest queue for
// replay, Queue() snapshots it, and QueueControl forwards mutations (or reports
// view-only when no queue controller is wired).
func TestQueuePublishReplayAndControl(t *testing.T) {
	h := New()
	if New().QueueControl("add", 0, "x") {
		t.Error("QueueControl should fail without a queue controller")
	}
	var act, txt string
	var idx int
	h.SetQueueController(func(a string, i int, s string) { act, idx, txt = a, i, s })

	h.PublishQueue([]string{"one", "two"})
	if q := h.Queue(); len(q) != 2 || q[0] != "one" || q[1] != "two" {
		t.Fatalf("Queue() = %v, want [one two]", q)
	}
	// A late subscriber replays the retained queue as a trailing KindQueue msg.
	_, _, hist, _ := h.Subscribe()
	var found *Msg
	for i := range hist {
		if hist[i].Kind == KindQueue {
			found = &hist[i]
		}
	}
	if found == nil || len(found.Queue) != 2 {
		t.Fatalf("replay missing KindQueue with 2 items: %+v", hist)
	}

	if !h.QueueControl("remove", 1, "draft") || act != "remove" || idx != 1 || txt != "draft" {
		t.Fatalf("QueueControl not forwarded: (%q,%d,%q)", act, idx, txt)
	}
}

func TestSwitchForwarding(t *testing.T) {
	h := New()
	var switched string
	h.SetController(nil, nil, nil, func(id string) { switched = id })
	h.SetActiveSession("aaa")

	if got := h.ActiveSessionID(); got != "aaa" {
		t.Fatalf("ActiveSessionID = %q, want aaa", got)
	}

	// Switching to a different idle session is forwarded.
	if !h.Switch("bbb") || switched != "bbb" {
		t.Fatalf("Switch not forwarded: ok target=%q", switched)
	}

	// Switching to the already-active session is a no-op (no churn).
	switched = ""
	if h.Switch("aaa") {
		t.Error("Switch to the active session should be rejected")
	}
	if switched != "" {
		t.Errorf("Switch to active session still forwarded: %q", switched)
	}

	// An empty target is rejected.
	if h.Switch("") {
		t.Error("Switch to empty id should be rejected")
	}

	// While busy, switching is rejected (mirrors the single-session terminal).
	switched = ""
	h.PublishState(StateBusy)
	if h.Switch("ccc") {
		t.Error("Switch should be rejected while busy")
	}
	if switched != "" {
		t.Errorf("Switch forwarded while busy: %q", switched)
	}

	// A view-only hub (no switch controller) can't switch.
	h2 := New()
	if h2.Switch("ddd") {
		t.Error("Switch should be rejected on a view-only hub")
	}
}

func TestStatusReplayedToNewViewers(t *testing.T) {
	h := New()
	// A live subscriber receives status updates as they happen.
	_, ch, _, _ := h.Subscribe()
	h.PublishStatus(Status{Model: "opus", TokensIn: 10, TokensOut: 3, ContextPercent: 42})
	select {
	case m := <-ch:
		if m.Kind != KindStatus || m.Status == nil || m.Status.Model != "opus" {
			t.Fatalf("live status msg = %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the status update")
	}

	// A late subscriber gets the latest status replayed — but only the latest:
	// status is not accumulated in history.
	h.PublishStatus(Status{Model: "sonnet", TokensIn: 99, ContextPercent: 7})
	_, _, hist, _ := h.Subscribe()
	statusCount, lastModel := 0, ""
	for _, m := range hist {
		if m.Kind == KindStatus && m.Status != nil {
			statusCount++
			lastModel = m.Status.Model
		}
	}
	if statusCount != 1 {
		t.Fatalf("late subscriber replayed %d status msgs, want exactly 1 (latest only)", statusCount)
	}
	if lastModel != "sonnet" {
		t.Errorf("replayed status model = %q, want latest (sonnet)", lastModel)
	}
}

// TestStatusReplayedBeforeTranscript guards the ordering the web UI relies on:
// the latest status snapshot must be replayed AHEAD of the buffered transcript,
// not appended after it. The browser renders a turn's footer (model · think
// level) off the latest status when it replays that turn's "idle" transition;
// if the status came after the transition, a refreshed view dropped the model
// name and think level from every replayed turn's footer.
func TestStatusReplayedBeforeTranscript(t *testing.T) {
	h := New()
	h.PublishStatus(Status{Model: "opus", Reasoning: "high"})
	h.PublishUser("hi")
	h.PublishEvent(agent.TextDelta{Text: "hello"})
	h.PublishState(StateIdle)

	_, _, hist, _ := h.Subscribe()
	statusIdx, idleIdx := -1, -1
	for i, m := range hist {
		switch {
		case m.Kind == KindStatus && statusIdx < 0:
			statusIdx = i
		case m.Kind == KindState && m.State == StateIdle:
			idleIdx = i
		}
	}
	if statusIdx < 0 {
		t.Fatal("status snapshot was not replayed to a new viewer")
	}
	if idleIdx < 0 {
		t.Fatal("idle transition was not in replayed history")
	}
	if statusIdx > idleIdx {
		t.Errorf("status replayed at index %d, after the idle transition at %d; the web UI needs model/reasoning before the turn footer renders", statusIdx, idleIdx)
	}
}

func TestSessionsReplayedToNewViewers(t *testing.T) {
	h := New()
	h.PublishSessions([]SessionInfo{{ID: "s1", Title: "first", Active: true}})
	_, _, hist, _ := h.Subscribe()
	var got []SessionInfo
	for _, m := range hist {
		if m.Kind == KindSessions {
			got = m.Sessions
		}
	}
	if len(got) != 1 || got[0].ID != "s1" || !got[0].Active {
		t.Fatalf("replayed sessions = %+v, want one active s1", got)
	}
}

func TestCommandsReplayedToNewViewers(t *testing.T) {
	h := New()
	h.PublishCommands([]Command{{Name: "/help", Desc: "show help"}, {Name: "/clear"}},
		[]Mention{{Name: "explore", Kind: "agent", Desc: "read-only explorer"}})

	// A late subscriber gets the command list replayed for its palette.
	_, _, hist, _ := h.Subscribe()
	var cmds []Command
	var mentions []Mention
	for _, m := range hist {
		if m.Kind == KindCommands {
			cmds = m.Commands
			mentions = m.Mentions
		}
	}
	if len(cmds) != 2 || cmds[0].Name != "/help" {
		t.Fatalf("replayed commands = %+v, want /help + /clear", cmds)
	}
	if len(mentions) != 1 || mentions[0].Name != "explore" || mentions[0].Kind != "agent" {
		t.Fatalf("replayed mentions = %+v, want [explore]", mentions)
	}
}

func TestMenuFannedOutNotRetained(t *testing.T) {
	h := New()
	// Published before anyone subscribes: a transient menu isn't retained, so a
	// later subscriber won't replay it.
	h.PublishMenu(Menu{Command: "/model", Items: []MenuItem{{Value: "m1"}}})

	id, ch, hist, _ := h.Subscribe()
	for _, m := range hist {
		if m.Kind == KindMenu {
			t.Fatal("menu should not be retained in history")
		}
	}

	// A menu published while subscribed is delivered live.
	h.PublishMenu(Menu{Title: "Pick", Command: "/api", Items: []MenuItem{{Value: "anthropic"}, {Value: "openai"}}})
	select {
	case m := <-ch:
		if m.Kind != KindMenu || m.Menu == nil || m.Menu.Command != "/api" || len(m.Menu.Items) != 2 {
			t.Fatalf("live menu msg = %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the menu")
	}
	h.Unsubscribe(id)
}

func TestResetClearsHistoryAndNotifies(t *testing.T) {
	h := New()
	h.PublishUser("old")
	id, ch, _, _ := h.Subscribe()

	h.Reset()

	// The live subscriber is told to reset.
	select {
	case m := <-ch:
		if m.Kind != KindReset {
			t.Errorf("expected reset msg, got %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive reset")
	}
	h.Unsubscribe(id)

	// A subscriber that connects after the reset starts from a clean slate.
	_, _, hist, _ := h.Subscribe()
	if len(hist) != 0 {
		t.Errorf("history after reset = %d entries, want 0: %+v", len(hist), hist)
	}
}
