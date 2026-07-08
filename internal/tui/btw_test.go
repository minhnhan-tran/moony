package tui

import (
	"strings"
	"testing"
)

// TestBtwEmptyShowsHelp verifies that "/btw" with no question shows usage
// help and does NOT enter the busy state or kick off a side-turn.
func TestBtwEmptyShowsHelp(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)

	next, cmd := m.command("/btw")
	m = next.(Model)
	if cmd != nil {
		// /btw with no arg should be inert (no spinner, no agent call).
		t.Fatalf("/btw with no arg should return nil cmd, got non-nil")
	}
	if m.btwBusy {
		t.Fatalf("/btw with no arg must not enter btwBusy state")
	}
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryInfo || !strings.Contains(last.text, "/btw") {
		t.Fatalf("expected info entry with usage hint, got kind=%v text=%q", last.kind, last.text)
	}
}

// TestBtwAddsSideUserEntry verifies that "/btw <q>" appends a user entry
// flagged as `side` (so the renderer dims it) and enters btwBusy.
func TestBtwAddsSideUserEntry(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)

	next, cmd := m.command("/btw is this thing on?")
	m = next.(Model)
	if cmd == nil {
		t.Fatalf("/btw <q> should return a non-nil cmd (spinner+side-turn)")
	}
	if !m.btwBusy {
		t.Fatalf("expected btwBusy=true after /btw <q>")
	}
	// Last entry should be the side user message, with the leading slash
	// stripped (only the question is shown).
	var found *entry
	for i := range m.trans.entries {
		e := &m.trans.entries[i]
		if e.kind == entryUser && e.side {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("expected a side=true user entry, none found")
	}
	if found.text != "is this thing on?" {
		t.Fatalf("side user entry text = %q, want %q", found.text, "is this thing on?")
	}
}

// TestBtwDoneAppendsSideAssistant verifies that a btwDoneMsg with a
// successful answer clears btwBusy and appends a `side`-flagged assistant
// entry — and never touches the agent's main message history.
func TestBtwDoneAppendsSideAssistant(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.btwBusy = true

	priorAgentMsgs := len(m.agent.Messages())

	next, _ := m.Update(btwDoneMsg{question: "q?", answer: "an answer"})
	m = next.(Model)

	if m.btwBusy {
		t.Fatalf("btwBusy should be cleared after btwDoneMsg")
	}
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryAssistant || !last.side {
		t.Fatalf("expected last entry to be side=true assistant, got kind=%v side=%v", last.kind, last.side)
	}
	if last.text != "an answer" {
		t.Fatalf("assistant side entry text = %q, want %q", last.text, "an answer")
	}
	// Crucial: a /btw exchange must NOT enter the main conversation history.
	if got := len(m.agent.Messages()); got != priorAgentMsgs {
		t.Fatalf("agent.Messages length changed after /btw: prior=%d after=%d", priorAgentMsgs, got)
	}
}

// TestBtwDoneError reports the error as an info entry and does NOT add a
// side-assistant entry.
func TestBtwDoneError(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.btwBusy = true

	next, _ := m.Update(btwDoneMsg{question: "q?", err: errBtwTest})
	m = next.(Model)

	if m.btwBusy {
		t.Fatalf("btwBusy should be cleared after error btwDoneMsg")
	}
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryInfo || !strings.Contains(last.text, "/btw failed") {
		t.Fatalf("expected info entry reporting failure, got kind=%v text=%q", last.kind, last.text)
	}
}

// TestBtwBusyKeepsSpinnerTicking guards the fix for the frozen-spinner bug: a
// spinner TickMsg while a /btw side-question is in flight must keep the spinner
// animating (return a non-nil re-tick cmd), like the busy/compacting states.
func TestBtwBusyKeepsSpinnerTicking(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.btwBusy = true

	tick := m.spinner.Tick() // a real TickMsg carrying the spinner's id/tag
	_, cmd := m.Update(tick)
	if cmd == nil {
		t.Fatal("spinner must keep ticking while a /btw side-question is in flight")
	}
}

// TestBtwBusyShowsStatusIndicator guards the fix for the missing status line:
// while a /btw side-question runs, the status bar must show a progress label
// rather than the idle hints.
func TestBtwBusyShowsStatusIndicator(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)

	if strings.Contains(m.statusView(), "answering side question") {
		t.Fatal("idle status should not show the side-question label")
	}
	m.btwBusy = true
	if !strings.Contains(m.statusView(), "answering side question") {
		t.Fatalf("status bar must indicate the in-flight /btw, got: %q", m.statusView())
	}
}

// errBtwTest is a sentinel error for the error-path test.
var errBtwTest = btwTestErr("boom")

type btwTestErr string

func (e btwTestErr) Error() string { return string(e) }

// TestBtwWhileBusyDispatchesNotQueued is the regression for the /btw queuing bug:
// unlike other slash commands (which queue while a turn runs), /btw is a
// concurrent side-question, so pressing Enter on it while busy must dispatch it
// immediately — clearing the composer and entering btwBusy — not push it onto the
// FIFO queue (which would defer it until the turn ends).
func TestBtwWhileBusyDispatchesNotQueued(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy // a turn is in flight

	m.input.SetValue("/btw what is the time complexity here?")
	next, cmd := m.Update(keyEnter())
	m = next.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("/btw must not be queued while busy, got queued=%v", m.queued)
	}
	if !m.btwBusy {
		t.Fatal("/btw should have dispatched immediately (btwBusy=true) while the turn runs")
	}
	if cmd == nil {
		t.Fatal("/btw dispatch should return a non-nil cmd (spinner + side-question)")
	}
	if m.input.Value() != "" {
		t.Fatalf("composer should reset after dispatching /btw, got %q", m.input.Value())
	}
	if m.state != stateBusy {
		t.Fatalf("the main turn must keep running (stateBusy), got %v", m.state)
	}
	var sideFound bool
	for _, e := range m.trans.entries {
		if e.side && strings.Contains(e.text, "time complexity") {
			sideFound = true
		}
	}
	if !sideFound {
		t.Fatal("/btw question should be shown as a side entry, not queued")
	}
}
