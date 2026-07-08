package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyEnter returns a synthetic Enter key press the Bubble Tea Update loop
// recognises (Type == KeyEnter, no runes).
func keyEnter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

// keyEsc returns a synthetic Escape key press.
func keyEsc() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

// TestEnterWhileBusyEnqueues verifies that pressing Enter while a turn is in
// flight pushes the current input onto the queue (FIFO) and clears the
// composer, instead of dropping the keystroke. Slash commands (except /btw,
// which dispatches concurrently — see TestBtwWhileBusyDispatchesNotQueued) and
// `#` memory shortcuts are queued verbatim — they execute when the queue flushes.
func TestEnterWhileBusyEnqueues(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy // simulate an in-flight turn

	m.input.SetValue("first message")
	next, _ := m.Update(keyEnter())
	m = next.(Model)

	if got, want := m.queued, []string{"first message"}; !equalStrings(got, want) {
		t.Fatalf("queued = %v, want %v", got, want)
	}
	if v := m.input.Value(); v != "" {
		t.Fatalf("input not reset after enqueue, got %q", v)
	}

	// Second enqueue: appends to the back of the FIFO.
	m.input.SetValue("/queue list")
	next, _ = m.Update(keyEnter())
	m = next.(Model)
	if got, want := m.queued, []string{"first message", "/queue list"}; !equalStrings(got, want) {
		t.Fatalf("queued = %v, want %v", got, want)
	}

	// Empty/whitespace Enter while busy is a no-op (doesn't enqueue blanks).
	m.input.SetValue("   ")
	next, _ = m.Update(keyEnter())
	m = next.(Model)
	if len(m.queued) != 2 {
		t.Fatalf("blank enqueue should be a no-op, queued = %v", m.queued)
	}
}

// TestEscWhileBusyPopsQueueBeforeCancelling verifies that Esc with a non-empty
// queue pops the most recent queued message back into the composer for editing
// or discard, and only cancels the running turn once the queue is empty.
func TestEscWhileBusyPopsQueueBeforeCancelling(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy
	m.queued = []string{"first", "second"}

	next, _ := m.Update(keyEsc())
	m = next.(Model)

	if got, want := m.queued, []string{"first"}; !equalStrings(got, want) {
		t.Fatalf("after pop, queued = %v, want %v", got, want)
	}
	if v := m.input.Value(); v != "second" {
		t.Fatalf("popped message should be in input, got %q", v)
	}

	// Pop again — the input holds the just-popped "second" (an untouched
	// popped item, not a user draft), so it is DISCARDED rather than
	// re-stashed, and "first" comes out into the composer. (Re-stashing it
	// would make Esc cycle the last items forever and never drain the queue.)
	next, _ = m.Update(keyEsc())
	m = next.(Model)
	if got, want := m.queued, []string{}; !equalStrings(got, want) {
		t.Fatalf("untouched popped item should be discarded, not re-queued, got %v", got)
	}
	if v := m.input.Value(); v != "first" {
		t.Fatalf("input should hold popped item 'first', got %q", v)
	}

	// One more Esc drains the last popped item from the composer.
	next, _ = m.Update(keyEsc())
	m = next.(Model)
	if v := m.input.Value(); v != "" {
		t.Fatalf("final Esc should clear the last popped item, got %q", v)
	}
}

// TestEscRepeatedDrainsAll guards the >2-queued regression: pressing Esc
// repeatedly must drain the entire queue (popped items get discarded once
// untouched) instead of cycling the last item between input and queue forever.
func TestEscRepeatedDrainsAll(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy
	m.queued = []string{"A", "B", "C"}

	for i := 0; i < 8 && (len(m.queued) > 0 || m.input.Value() != ""); i++ {
		next, _ := m.Update(keyEsc())
		m = next.(Model)
	}
	if len(m.queued) != 0 || m.input.Value() != "" {
		t.Fatalf("could not drain via Esc: queued=%v input=%q", m.queued, m.input.Value())
	}
}

// TestEscPopPreservesDraft verifies that an in-progress draft isn't clobbered
// when Esc pops a queued item: it's stashed at the front of the queue so the
// next Esc returns it.
func TestEscPopPreservesDraft(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy
	m.queued = []string{"queued-one"}
	m.input.SetValue("draft-in-progress")

	next, _ := m.Update(keyEsc())
	m = next.(Model)

	if v := m.input.Value(); v != "queued-one" {
		t.Fatalf("Esc should pop queued item into input, got %q", v)
	}
	if got, want := m.queued, []string{"draft-in-progress"}; !equalStrings(got, want) {
		t.Fatalf("draft should be stashed on queue front, got %v", got)
	}
}

// TestQueueClearCommand verifies that `/queue clear` empties the queue.
func TestQueueClearCommand(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.queued = []string{"a", "b", "c"}

	next, _ := m.command("/queue clear")
	m = next.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("queue should be empty after /queue clear, got %v", m.queued)
	}
}

// TestQueueListCommand verifies that `/queue` (no args) renders a transcript
// info line listing the pending items.
func TestQueueListCommand(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.queued = []string{"hello", "world"}

	next, _ := m.command("/queue")
	m = next.(Model)

	// Look for an info entry mentioning both items.
	var found string
	for _, e := range m.trans.entries {
		if e.kind == entryInfo && strings.Contains(e.text, "hello") && strings.Contains(e.text, "world") {
			found = e.text
			break
		}
	}
	if found == "" {
		t.Fatalf("expected /queue to list pending items, got entries: %+v", m.trans.entries)
	}
}

// TestQueuedHintsRendersInView verifies the queued-messages preview appears in
// the rendered View when the queue is non-empty.
func TestQueuedHintsRendersInView(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.queued = []string{"do the thing", "then run tests"}

	out := m.View()
	if !strings.Contains(out, "queued (2)") {
		t.Fatalf("expected 'queued (2)' header in View, got:\n%s", out)
	}
	if !strings.Contains(out, "do the thing") {
		t.Fatalf("expected first queued item in View, got:\n%s", out)
	}
}

// TestQueueDrainsNonTurnItemsWithoutStalling is the regression test for the
// drain loop: queued slash commands (and other non-turn items) return to idle
// without starting a turn, so a single turn-completion must keep dispatching
// them until the queue empties — not pop just one and strand the rest.
func TestQueueDrainsNonTurnItemsWithoutStalling(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy
	// All three are slash commands that stay idle (no turn starts).
	m.queued = []string{"/queue list", "/queue list", "/queue clear"}

	// Simulate the in-flight turn completing cleanly.
	next, _ := m.handleStream(streamMsg{done: true})
	m = next.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("all non-turn items should drain in one completion, got %v", m.queued)
	}
	if m.state != stateIdle {
		t.Fatalf("state should settle to idle after draining non-turn items, got %v", m.state)
	}
}

// TestQueueDrainPreservesInProgressDraft verifies that a draft typed while the
// turn was running survives the drain (non-turn items are dispatched, then the
// draft is restored into the composer rather than sent or clobbered).
func TestQueueDrainPreservesInProgressDraft(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.state = stateBusy
	m.queued = []string{"/queue list"}
	m.input.SetValue("half-typed draft")

	next, _ := m.handleStream(streamMsg{done: true})
	m = next.(Model)

	if len(m.queued) != 0 {
		t.Fatalf("queued slash command should have drained, got %v", m.queued)
	}
	if v := m.input.Value(); v != "half-typed draft" {
		t.Fatalf("in-progress draft should be restored after drain, got %q", v)
	}
}

// TestTypeWhileBusyThenEnqueue guards the focus fix: the composer stays focused
// while a turn runs (runUserMessage no longer blurs it), so typed characters
// reach the input and Enter then queues them. A regression here means the
// composer is blurred while busy and the queue can't be filled by typing.
func TestTypeWhileBusyThenEnqueue(t *testing.T) {
	m := newTestModel(t)
	m = sized(t, m, 100, 30)
	m.input.Focus() // mirrors runUserMessage keeping the composer focused while busy
	m.state = stateBusy

	for _, r := range "hello" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("typing while busy should fill the composer, got %q", got)
	}

	next, _ := m.Update(keyEnter())
	m = next.(Model)
	if !equalStrings(m.queued, []string{"hello"}) {
		t.Fatalf("queued = %v, want [hello]", m.queued)
	}
	if m.input.Value() != "" {
		t.Fatalf("composer should reset after enqueue, got %q", m.input.Value())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
