package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// TestQueueCarriesAttachments is the regression test for the fix: a message
// queued while busy must keep its attachment content blocks (previously dropped),
// so a pasted image sent mid-turn still reaches the model when the queue drains.
func TestQueueCarriesAttachments(t *testing.T) {
	sess := newSession("id", "t", nil)
	sess.state = stateBusy
	img := llm.ImageBlock("image/png", "aGVsbG8=")
	wires := []attachWire{{Name: "paste.png", Kind: "image"}}
	if !sess.enqueueIfBusy("convert this to svg", []llm.ContentBlock{img}, wires) {
		t.Fatal("should enqueue while busy")
	}
	sess.state = stateIdle
	m, ok := sess.dequeueIfIdle()
	if !ok {
		t.Fatal("dequeueIfIdle should return the queued message")
	}
	if m.text != "convert this to svg" {
		t.Fatalf("text = %q", m.text)
	}
	if len(m.blocks) != 1 || m.blocks[0].Type != llm.BlockImage {
		t.Fatalf("attachment blocks were dropped from the queue: %+v", m.blocks)
	}
	if len(m.wires) != 1 || m.wires[0].Name != "paste.png" {
		t.Fatalf("display wires were dropped from the queue: %+v", m.wires)
	}
}

// TestSessionQueueMethods covers the busy-time message queue primitives on the
// session: enqueue only while busy, dequeue only while idle (FIFO), and the
// pop/clear/remove operations — without touching the agent.
func TestSessionQueueMethods(t *testing.T) {
	sess := newSession("id", "t", nil)

	// Idle: enqueueIfBusy refuses (so the caller dispatches immediately).
	if sess.enqueueIfBusy("a", nil, nil) {
		t.Fatal("enqueueIfBusy must return false while idle")
	}
	if len(sess.queuedList()) != 0 {
		t.Fatal("nothing should be queued while idle")
	}

	// Busy: messages queue FIFO.
	sess.state = stateBusy
	if !sess.enqueueIfBusy("a", nil, nil) || !sess.enqueueIfBusy("b", nil, nil) || !sess.enqueueIfBusy("c", nil, nil) {
		t.Fatal("enqueueIfBusy must queue while busy")
	}
	if got := sess.queuedList(); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("queue = %v, want [a b c]", got)
	}

	// dequeue refuses while busy.
	if _, ok := sess.dequeueIfIdle(); ok {
		t.Fatal("dequeueIfIdle must refuse while busy")
	}

	// Idle: drains FIFO.
	sess.state = stateIdle
	if m, ok := sess.dequeueIfIdle(); !ok || m.text != "a" {
		t.Fatalf("first dequeue = %q,%v want a,true", m.text, ok)
	}
	if got := sess.queuedList(); len(got) != 2 || got[0] != "b" {
		t.Fatalf("after dequeue, queue = %v want [b c]", got)
	}
}

// TestSessionPopQueued covers Esc-pop: remove the most recent item and return
// it, stashing an in-progress draft onto the front first.
func TestSessionPopQueued(t *testing.T) {
	sess := newSession("id", "t", nil)
	sess.state = stateBusy
	sess.enqueueIfBusy("a", nil, nil)
	sess.enqueueIfBusy("b", nil, nil)

	// No stash: pops the last item.
	txt, ok := sess.popQueued("")
	if !ok || txt != "b" {
		t.Fatalf("pop = %q,%v want b,true", txt, ok)
	}
	if got := sess.queuedList(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("after pop, queue = %v want [a]", got)
	}

	// With a stash (an in-progress draft): it goes to the front, then the last
	// item ("a") pops out.
	txt, ok = sess.popQueued("draft")
	if !ok || txt != "a" {
		t.Fatalf("pop = %q,%v want a,true", txt, ok)
	}
	if got := sess.queuedList(); len(got) != 1 || got[0] != "draft" {
		t.Fatalf("draft should be stashed on the front, queue = %v want [draft]", got)
	}

	// Empty queue: pop is a no-op.
	sess.queued = nil
	if _, ok := sess.popQueued("x"); ok {
		t.Fatal("pop on an empty queue must return false")
	}
}

// TestSessionClearAndRemove covers clearQueue and removeQueuedAt.
func TestSessionClearAndRemove(t *testing.T) {
	sess := newSession("id", "t", nil)
	sess.state = stateBusy
	sess.enqueueIfBusy("a", nil, nil)
	sess.enqueueIfBusy("b", nil, nil)
	sess.enqueueIfBusy("c", nil, nil)

	if !sess.removeQueuedAt(1) { // drop "b"
		t.Fatal("removeQueuedAt(1) should succeed")
	}
	if got := sess.queuedList(); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("after remove, queue = %v want [a c]", got)
	}
	if sess.removeQueuedAt(9) {
		t.Fatal("out-of-range remove must be a no-op")
	}
	if n := sess.clearQueue(); n != 2 {
		t.Fatalf("clearQueue = %d want 2", n)
	}
	if len(sess.queuedList()) != 0 {
		t.Fatal("queue should be empty after clear")
	}
}

// TestQueueEnqueueAndDrainHTTP is the end-to-end test: a message POSTed while a
// turn is running is queued (202), surfaces in a "queue" wire event, and then
// runs as its own turn once the in-flight turn completes.
func TestQueueEnqueueAndDrainHTTP(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeStreamer{
		turns: []llm.Response{
			{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("first done")}},
			{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("second done")}},
		},
		gate: gate,
	}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}

	// Start turn 1 — it blocks in the streamer on the gate, so the session stays
	// busy while we enqueue.
	if r := postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"first"}`); r.Code != http.StatusAccepted {
		t.Fatalf("first message code = %d want 202", r.Code)
	}
	waitState(t, srv, created.ID, stateBusy)

	// A message sent while busy is queued (202), not rejected.
	if r := postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"second"}`); r.Code != http.StatusAccepted {
		t.Fatalf("queued message code = %d want 202", r.Code)
	}

	srv.mu.Lock()
	sess := srv.sessions[created.ID]
	srv.mu.Unlock()
	if got := sess.queuedList(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("queue = %v want [second]", got)
	}

	// Release turn 1; the drain should dequeue "second" and start turn 2 (which
	// blocks on the gate again). Poll until the queue drains to empty.
	gate <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(sess.queuedList()) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue should drain to empty, got %v", sess.queuedList())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Release turn 2 and let the session settle idle.
	gate <- struct{}{}
	waitState(t, srv, created.ID, stateIdle)

	// The drained message ran as its own turn (a "user" event with its text).
	_, _, hist := sess.subscribe()
	var sawSecondUser bool
	for _, e := range hist {
		if e.Type == "user" && e.Text == "second" {
			sawSecondUser = true
		}
	}
	if !sawSecondUser {
		t.Error("the queued message should have run as its own turn")
	}
}
