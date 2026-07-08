package web

import (
	"testing"

	"github.com/pysty1995/moony/internal/agent"
)

// textRuns returns the coalesced "text" history entries (for assertions).
func textRuns(s *session) []wireEvent {
	var out []wireEvent
	for _, w := range s.history {
		if w.Type == "text" {
			out = append(out, w)
		}
	}
	return out
}

// Consecutive streaming text deltas coalesce into ONE history entry per run; a
// non-text event breaks the run so the next text starts a fresh entry. This
// keeps a mid-answer session's replay snapshot small.
func TestEmitCoalescesTextDeltas(t *testing.T) {
	s := &session{subs: map[int]chan wireEvent{}}

	s.emit(agent.TextDelta{Text: "Hello "})
	s.emit(agent.TextDelta{Text: "world"})
	if runs := textRuns(s); len(runs) != 1 || runs[0].Text != "Hello world" {
		t.Fatalf("expected one coalesced run %q, got %+v", "Hello world", runs)
	}

	// A tool event breaks the run; the following text is a separate entry.
	s.emit(agent.ToolStart{Name: "read_file", ID: "t1"})
	s.emit(agent.TextDelta{Text: "again"})
	runs := textRuns(s)
	if len(runs) != 2 || runs[1].Text != "again" {
		t.Fatalf("expected a new text run after the tool event, got %+v", runs)
	}
}

// A subscriber whose buffer fills is dropped (channel closed + removed) rather
// than silently losing events, so handleEvents returns and the browser's
// EventSource reconnects for a full, correct replay — the self-heal.
func TestFanoutClosesFullSubscriber(t *testing.T) {
	s := &session{subs: map[int]chan wireEvent{}}
	ch := make(chan wireEvent, 1)
	s.subs[7] = ch
	ch <- wireEvent{Type: "text", Text: "prefill"} // fill the (size-1) buffer

	s.mu.Lock()
	s.fanoutLocked(wireEvent{Type: "text", Text: "overflow"})
	s.mu.Unlock()

	if _, ok := s.subs[7]; ok {
		t.Error("a full subscriber should be removed from subs")
	}
	if got := <-ch; got.Text != "prefill" { // the buffered event is still deliverable
		t.Errorf("buffered event = %q, want prefill", got.Text)
	}
	if _, open := <-ch; open {
		t.Error("subscriber channel should be closed after overflow (drives an EventSource reconnect)")
	}
}

// A subscriber that keeps up receives fanned-out events normally (no false drop).
func TestFanoutDeliversToLiveSubscriber(t *testing.T) {
	s := &session{subs: map[int]chan wireEvent{}}
	id, ch, _ := s.subscribe()

	s.emit(agent.TextDelta{Text: "hi"})
	select {
	case got := <-ch:
		if got.Text != "hi" {
			t.Errorf("got %q, want hi", got.Text)
		}
	default:
		t.Fatal("expected the delta to be delivered to a keeping-up subscriber")
	}
	if _, ok := s.subs[id]; !ok {
		t.Error("a keeping-up subscriber should NOT be dropped")
	}
}
