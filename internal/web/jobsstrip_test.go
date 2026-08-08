package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// An EMPTY job list must survive JSON encoding. This is the whole reason wireEvent.Jobs is
// a *[]jobWire: with a plain slice, `omitempty` drops the empty list, and the empty list
// is precisely the update that clears the strip when the last job is reaped — so a
// finished job's row would stay on screen forever.
func TestJobsWireKeepsEmptyList(t *testing.T) {
	empty := []jobWire{}
	b, err := json.Marshal(wireEvent{Type: "jobs", Jobs: &empty})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"jobs":[]`) {
		t.Errorf("empty list was dropped from the wire: %s", b)
	}

	// And a nil Jobs (any other event type) must NOT emit a jobs key at all.
	b2, err := json.Marshal(wireEvent{Type: "token", Text: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b2), `"jobs"`) {
		t.Errorf("non-jobs event should omit the jobs key: %s", b2)
	}
}

func TestJobsDigestDetectsChanges(t *testing.T) {
	base := []jobWire{
		{ID: "1", State: "running", OutputLen: 100},
		{ID: "2", State: "done", OutputLen: 40},
	}
	d := jobsDigest(base)

	if d == "" {
		t.Fatal("a non-empty list must produce a non-empty digest")
	}
	// Stable across identical snapshots, or the watcher would republish every tick.
	if jobsDigest(base) != d {
		t.Error("digest is not stable for an unchanged list")
	}

	tests := []struct {
		name string
		list []jobWire
	}{
		{"state change", []jobWire{{ID: "1", State: "done", OutputLen: 100}, {ID: "2", State: "done", OutputLen: 40}}},
		// Output growth must refresh: the strip shows a live byte count, and a job can
		// stream output for minutes without changing state.
		{"output growth", []jobWire{{ID: "1", State: "running", OutputLen: 101}, {ID: "2", State: "done", OutputLen: 40}}},
		{"job added", append(append([]jobWire{}, base...), jobWire{ID: "3", State: "running"})},
		{"job removed", base[:1]},
		{"id change", []jobWire{{ID: "9", State: "running", OutputLen: 100}, {ID: "2", State: "done", OutputLen: 40}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if jobsDigest(tc.list) == d {
				t.Errorf("%s was not detected as a change", tc.name)
			}
		})
	}

	// An empty list has the empty digest, which is also the watcher's "nothing published
	// yet" sentinel — so a session that never runs a job stays completely silent.
	if jobsDigest(nil) != "" || jobsDigest([]jobWire{}) != "" {
		t.Error("an empty list must produce the empty digest")
	}
}

// Two different lists must not collide into the same digest just because their fields
// concatenate to the same bytes.
func TestJobsDigestNoFieldRunTogether(t *testing.T) {
	a := jobsDigest([]jobWire{{ID: "a", State: "b", OutputLen: 1}})
	b := jobsDigest([]jobWire{{ID: "ab", State: "", OutputLen: 1}})
	if a == b {
		t.Errorf("digests collided: %q vs %q", a, b)
	}
}

// sessionJobWires must never return nil (the JSON must be [], not null) and must not panic
// on a session with no agent/registry/store — the hub-routed and mirror cases.
func TestSessionJobWiresHandlesMissingStore(t *testing.T) {
	if got := sessionJobWires(nil); got == nil || len(got) != 0 {
		t.Errorf("nil session: got %v, want an empty non-nil slice", got)
	}
	if got := sessionJobWires(&session{}); got == nil || len(got) != 0 {
		t.Errorf("session with no agent: got %v, want an empty non-nil slice", got)
	}
}

// The watcher is reference-counted against SSE viewers: several viewers share ONE poll
// goroutine, and it must stop when the last one disconnects, or every session ever opened
// would keep ticking forever.
func TestJobWatchRefcount(t *testing.T) {
	s := &session{}

	s.startJobWatch()
	s.mu.Lock()
	viewers, stop1 := s.jobViewers, s.jobWatchStop
	s.mu.Unlock()
	if viewers != 1 || stop1 == nil {
		t.Fatalf("after one viewer: viewers=%d stop=%v", viewers, stop1 != nil)
	}

	// A second viewer must NOT start a second goroutine.
	s.startJobWatch()
	s.mu.Lock()
	viewers, stop2 := s.jobViewers, s.jobWatchStop
	s.mu.Unlock()
	if viewers != 2 {
		t.Errorf("viewers = %d, want 2", viewers)
	}
	if stop2 != stop1 {
		t.Error("a second viewer started a second watcher goroutine")
	}

	// Dropping one viewer keeps it running for the other.
	s.releaseJobWatch()
	s.mu.Lock()
	viewers, still := s.jobViewers, s.jobWatchStop
	s.mu.Unlock()
	if viewers != 1 || still == nil {
		t.Errorf("after one release: viewers=%d running=%v, want 1/true", viewers, still != nil)
	}

	// The last release stops it and closes the channel.
	s.releaseJobWatch()
	s.mu.Lock()
	viewers, gone := s.jobViewers, s.jobWatchStop
	s.mu.Unlock()
	if viewers != 0 || gone != nil {
		t.Errorf("after the last release: viewers=%d stop=%v, want 0/nil", viewers, gone != nil)
	}
	select {
	case <-stop1:
	default:
		t.Error("the stop channel was not closed, so the goroutine leaks")
	}
}

// An unbalanced release (a disconnect path running twice) must not drive the count
// negative or panic by closing an already-closed channel.
func TestJobWatchReleaseWithoutStart(t *testing.T) {
	s := &session{}
	s.releaseJobWatch()
	s.releaseJobWatch()
	s.mu.Lock()
	viewers := s.jobViewers
	s.mu.Unlock()
	if viewers != 0 {
		t.Errorf("viewers = %d, want 0", viewers)
	}
	// A start after the spurious releases must still work.
	s.startJobWatch()
	s.mu.Lock()
	ok := s.jobWatchStop != nil
	s.mu.Unlock()
	if !ok {
		t.Error("start after an unbalanced release did not begin watching")
	}
	s.releaseJobWatch()
}

// End-to-end: start a REAL background job and confirm the strip's data path (the same
// sessionJobWires the SSE watcher publishes) reports it, then reports its completion.
func TestJobStripSeesRealJob(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs",
		`{"command":"echo strip-probe; sleep 0.4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start job: status %d body %s", rec.Code, rec.Body.String())
	}
	jobID, _ := out["id"].(string)
	if jobID == "" {
		t.Fatalf("no job id in %s", rec.Body.String())
	}

	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatal("session not live")
	}

	// The watcher's snapshot must include the new job while it runs.
	var sawRunning bool
	for i := 0; i < 50; i++ {
		for _, j := range sessionJobWires(sess) {
			if j.ID == jobID && j.Running {
				sawRunning = true
			}
		}
		if sawRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawRunning {
		t.Error("a running job never appeared in the strip's snapshot")
	}

	// ...and must report it finished once it exits, or the row would show "running" forever.
	var sawDone bool
	for i := 0; i < 100; i++ {
		for _, j := range sessionJobWires(sess) {
			if j.ID == jobID && !j.Running {
				sawDone = true
			}
		}
		if sawDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawDone {
		t.Error("the job never showed as finished in the strip's snapshot")
	}

	// The digest must have changed between the two states, otherwise the watcher would
	// never republish and the row would stay stale on screen.
	running := jobsDigest([]jobWire{{ID: jobID, State: "running", OutputLen: 0}})
	done := jobsDigest([]jobWire{{ID: jobID, State: "done", OutputLen: 13}})
	if running == done {
		t.Error("digest did not change between running and done")
	}
}
