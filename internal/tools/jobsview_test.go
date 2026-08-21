package tools

import (
	"strings"
	"testing"
	"time"
)

// The invariant this whole file exists for: a UI tailing a job must NOT consume the
// output the model reads through bash_output.
//
// If the viewer shared the model's cursor, then merely having the jobs panel open would
// change what the agent sees — output would vanish before bash_output ever returned it.
// That is a correctness bug (the model silently loses information), not a cosmetic one.
func TestJobTailDoesNotStealModelOutput(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "echo hi", state: jobRunning}
	s.add(j)
	j.Write([]byte("first chunk\n"))

	// The viewer tails everything.
	info, ok := s.JobTail("1", 0)
	if !ok {
		t.Fatal("JobTail did not find the job")
	}
	if info.Output != "first chunk\n" {
		t.Fatalf("viewer output = %q, want the whole buffer", info.Output)
	}
	if info.Cursor != len("first chunk\n") {
		t.Errorf("cursor = %d, want %d", info.Cursor, len("first chunk\n"))
	}

	// The model must still receive that same output: the viewer read must not have
	// advanced j.read.
	got, _, _, _ := j.snapshot()
	if got != "first chunk\n" {
		t.Errorf("bash_output got %q, want the full output — the viewer consumed it", got)
	}
}

// And the converse: the model reading must not disturb the viewer's absolute offsets.
func TestModelReadDoesNotBreakViewerCursor(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "x", state: jobRunning}
	s.add(j)

	j.Write([]byte("aaa"))
	j.snapshot() // model consumes "aaa"
	j.Write([]byte("bbb"))

	// A viewer starting fresh still sees the whole history, because it reads the buffer
	// by absolute offset rather than "what's unread".
	info, _ := s.JobTail("1", 0)
	if info.Output != "aaabbb" {
		t.Errorf("viewer output = %q, want the full history %q", info.Output, "aaabbb")
	}

	// A viewer that had already seen "aaa" gets only the new bytes.
	info2, _ := s.JobTail("1", 3)
	if info2.Output != "bbb" {
		t.Errorf("incremental tail = %q, want %q", info2.Output, "bbb")
	}
}

// Repeated tails at the returned cursor must yield nothing new — otherwise a polling
// panel would re-append the same text forever.
func TestJobTailIsIdempotentAtCursor(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "x", state: jobRunning}
	s.add(j)
	j.Write([]byte("hello"))

	info, _ := s.JobTail("1", 0)
	for i := 0; i < 3; i++ {
		again, _ := s.JobTail("1", info.Cursor)
		if again.Output != "" {
			t.Fatalf("re-tail at the cursor returned %q, want nothing new", again.Output)
		}
		if again.Cursor != info.Cursor {
			t.Fatalf("cursor moved without new output: %d -> %d", info.Cursor, again.Cursor)
		}
	}
}

// A malformed or stale cursor must not panic on a slice bound. A client should never be
// able to crash the server with a bad query string.
func TestJobTailToleratesOutOfRangeCursor(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "x", state: jobRunning}
	s.add(j)
	j.Write([]byte("abc"))

	for _, from := range []int{-100, -1, 4, 999999} {
		info, ok := s.JobTail("1", from)
		if !ok {
			t.Fatalf("from=%d: job not found", from)
		}
		// Negative clamps to the start; past-the-end yields nothing but must report the
		// real cursor so the client can resynchronize.
		if from < 0 && info.Output != "abc" {
			t.Errorf("from=%d: output = %q, want the whole buffer", from, info.Output)
		}
		if from > 3 && info.Output != "" {
			t.Errorf("from=%d: output = %q, want empty", from, info.Output)
		}
		if info.Cursor != 3 {
			t.Errorf("from=%d: cursor = %d, want 3", from, info.Cursor)
		}
	}
}

// List views must be status-only: including every job's whole log would re-send
// megabytes on each poll of a long-running server.
func TestJobsListOmitsOutput(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "x", state: jobRunning}
	s.add(j)
	j.Write([]byte(strings.Repeat("x", 10000)))

	list := s.Jobs()
	if len(list) != 1 {
		t.Fatalf("got %d jobs, want 1", len(list))
	}
	if list[0].Output != "" {
		t.Errorf("list view must not carry output, got %d bytes", len(list[0].Output))
	}
	// But it must report the size, so a viewer can tell there IS output to fetch.
	if list[0].OutputLen != 10000 {
		t.Errorf("OutputLen = %d, want 10000", list[0].OutputLen)
	}
	if !list[0].Running {
		t.Error("Running must be true for a running job")
	}
}

func TestJobsListIsOrderedAndComplete(t *testing.T) {
	s := newJobStore()
	for _, id := range []string{"3", "1", "2"} {
		s.add(&job{id: id, command: "c" + id, state: jobDone})
	}
	list := s.Jobs()
	if len(list) != 3 {
		t.Fatalf("got %d jobs, want 3", len(list))
	}
	// Stable order, or rows would jump around between polls.
	for i, want := range []string{"1", "2", "3"} {
		if list[i].ID != want {
			t.Errorf("position %d = %q, want %q", i, list[i].ID, want)
		}
	}
}

// State must be reported in a form the UI can style, and Running must agree with it —
// two sources of truth would let a dead job render as live.
func TestJobInfoStateAndRunningAgree(t *testing.T) {
	for _, tc := range []struct {
		state   jobState
		running bool
	}{
		{jobRunning, true},
		{jobDone, false},
		{jobFailed, false},
		{jobKilled, false},
	} {
		s := newJobStore()
		s.add(&job{id: "1", command: "x", state: tc.state})
		got, ok := s.Job("1")
		if !ok {
			t.Fatal("job not found")
		}
		if got.State != string(tc.state) {
			t.Errorf("State = %q, want %q", got.State, tc.state)
		}
		if got.Running != tc.running {
			t.Errorf("state %q: Running = %v, want %v", tc.state, got.Running, tc.running)
		}
	}
}

func TestJobViewUnknownID(t *testing.T) {
	s := newJobStore()
	if _, ok := s.Job("nope"); ok {
		t.Error("Job must report false for an unknown id")
	}
	if _, ok := s.JobTail("nope", 0); ok {
		t.Error("JobTail must report false for an unknown id")
	}
	if s.KillJob("nope") {
		t.Error("KillJob must report false for an unknown id")
	}
}

// Every accessor must be nil-safe: sessions without a local registry (hub-routed, or
// the read-only mirror) have no store, and the web layer calls these unconditionally.
func TestJobViewNilStore(t *testing.T) {
	var s *JobStore
	if got := s.Jobs(); got != nil {
		t.Errorf("Jobs() on a nil store = %v, want nil", got)
	}
	if _, ok := s.Job("1"); ok {
		t.Error("Job on a nil store must report false")
	}
	if _, ok := s.JobTail("1", 0); ok {
		t.Error("JobTail on a nil store must report false")
	}
	if s.KillJob("1") {
		t.Error("KillJob on a nil store must report false")
	}
}

// Killing a job that already exited is not an error: the caller asked for it stopped,
// and it is stopped. Reporting false would make the UI show a spurious failure.
func TestKillJobOnFinishedJobSucceeds(t *testing.T) {
	s := newJobStore()
	s.add(&job{id: "1", command: "x", state: jobDone})
	if !s.KillJob("1") {
		t.Error("killing an already-finished job must report success")
	}
}

func TestKillJobCancelsRunning(t *testing.T) {
	s := newJobStore()
	cancelled := false
	s.add(&job{id: "1", command: "x", state: jobRunning, cancel: func() { cancelled = true }})
	if !s.KillJob("1") {
		t.Fatal("KillJob reported failure")
	}
	if !cancelled {
		t.Error("the job's cancel func was not called")
	}
}

// A runaway job must not be able to exhaust host memory. The buffer lives for the
// process's whole life, so `yes` backgrounded once would OOM-kill the agent.
func TestJobOutputIsBounded(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "yes", state: jobRunning}
	s.add(j)

	chunk := []byte(strings.Repeat("x", 1<<20)) // 1 MB at a time
	for i := 0; i < 20; i++ {                   // 20 MB total, well past the cap
		j.Write(chunk)
	}
	j.mu.Lock()
	buffered := j.buf.Len()
	dropped := j.dropped
	j.mu.Unlock()

	if buffered > maxJobOutputBytes {
		t.Errorf("buffered %d bytes, want <= the %d cap", buffered, maxJobOutputBytes)
	}
	if dropped == 0 {
		t.Error("nothing was dropped, so the cap is not being enforced")
	}
	// The TAIL is what's kept: a runaway job's interesting part is where it is now.
	info, _ := s.JobTail("1", 0)
	if len(info.Output) > maxJobOutputBytes {
		t.Errorf("tail returned %d bytes, want <= the cap", len(info.Output))
	}
	// OutputLen reports the true total, or a viewer would think the job emitted only 8 MB.
	if info.OutputLen != 20<<20 {
		t.Errorf("OutputLen = %d, want the full %d produced", info.OutputLen, 20<<20)
	}
}

// After a trim, a cursor from BEFORE the drop must resume at the oldest kept byte —
// not slice out of range, and not silently replay the whole buffer on every poll.
func TestJobTailRebasesCursorAfterTrim(t *testing.T) {
	s := newJobStore()
	j := &job{id: "1", command: "x", state: jobRunning}
	s.add(j)

	j.Write([]byte("early-output"))
	stale, _ := s.JobTail("1", 0)
	cursor := stale.Cursor // a cursor captured before any trimming

	// Overflow the cap so the early bytes are discarded.
	chunk := []byte(strings.Repeat("y", 1<<20))
	for i := 0; i < 12; i++ {
		j.Write(chunk)
	}

	info, ok := s.JobTail("1", cursor)
	if !ok {
		t.Fatal("job vanished")
	}
	// Must not exceed the cap, and must not be empty (there IS newer output).
	if len(info.Output) == 0 {
		t.Error("a pre-trim cursor returned nothing; the viewer would appear frozen")
	}
	if len(info.Output) > maxJobOutputBytes {
		t.Errorf("returned %d bytes, want <= the cap", len(info.Output))
	}
	// Polling again at the new cursor must yield nothing — no infinite replay.
	again, _ := s.JobTail("1", info.Cursor)
	if again.Output != "" {
		t.Errorf("re-poll returned %d bytes, want none", len(again.Output))
	}
}

// The model's cursor must be rebased too, or bash_output would slice into freed bytes.
func TestModelCursorSurvivesTrim(t *testing.T) {
	j := &job{id: "1", command: "x", state: jobRunning}
	j.Write([]byte("first"))
	j.snapshot() // model is caught up

	chunk := []byte(strings.Repeat("z", 1<<20))
	for i := 0; i < 12; i++ {
		j.Write(chunk)
	}
	// Must not panic, and must return the kept tail rather than nothing.
	out, _, _, _ := j.snapshot()
	if out == "" {
		t.Error("the model got no output after a trim; its cursor was not rebased")
	}
	if len(out) > maxJobOutputBytes {
		t.Errorf("model got %d bytes, want <= the cap", len(out))
	}
}

func TestJobInfoIncludesEndedWhenStopped(t *testing.T) {
	s := newJobStore()
	started := time.Now().Add(-5 * time.Second)
	ended := time.Now().Add(-2 * time.Second)
	j := &job{
		id:      "bg_1",
		command: "echo test",
		state:   jobKilled,
		started: started,
		ended:   ended,
		store:   s,
	}
	s.add(j)

	info, ok := s.Job("bg_1")
	if !ok {
		t.Fatalf("job not found")
	}
	if info.Ended.IsZero() {
		t.Errorf("expected Ended to be set, got zero time")
	}
	if !info.Ended.Equal(ended) {
		t.Errorf("Ended = %v, want %v", info.Ended, ended)
	}

	list := s.Jobs()
	if len(list) != 1 || list[0].Ended.IsZero() {
		t.Errorf("Jobs() list item should have Ended set")
	}
}
