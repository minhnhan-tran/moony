package tools

// Read-only viewing surface for background jobs.
//
// The cursor discipline is the whole point here. `bash_output` consumes output: it
// returns the bytes since the model last read and ADVANCES j.read. A UI that tailed
// through the same cursor would silently swallow output the model never sees — having
// a log panel open would change what the agent knows, which is a correctness bug, not
// a cosmetic one. So everything below reads by absolute offset and never touches
// j.read.

import (
	"fmt"
	"time"
)

// JobInfo is a point-in-time, JSON-serializable view of one background job.
type JobInfo struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Started time.Time `json:"started"`
	State   string    `json:"state"`
	Exit    int       `json:"exit"`
	Err     string    `json:"err,omitempty"`

	// Output is the slice requested by JobTail; empty for list views, which are
	// status-only so a long-running job's whole log isn't re-sent on every poll.
	Output string `json:"output,omitempty"`

	// Cursor is the absolute offset one past the end of Output. Pass it back to
	// JobTail to fetch only what has arrived since. It is INDEPENDENT of the model's
	// read cursor.
	Cursor int `json:"cursor"`

	// Running is derived rather than left to the client to infer from State, so the
	// UI and the store can't disagree about what "done" means.
	Running bool `json:"running"`

	// OutputLen is the total bytes buffered so far, letting a viewer show progress
	// (and detect that it is behind) without fetching the body.
	OutputLen int `json:"outputLen"`

	// TTY reports that the job runs under a pseudo-terminal, which is also what makes
	// it able to accept input: the panel only offers a keyboard for these.
	TTY bool `json:"tty"`
}

// info builds a status-only view (no output body). Caller must not hold j.mu.
func (j *job) info() JobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.infoLocked()
}

// infoLocked builds a status-only view. Caller holds j.mu.
func (j *job) infoLocked() JobInfo {
	return JobInfo{
		ID:      j.id,
		Command: j.command,
		Started: j.started,
		State:   string(j.state),
		Exit:    j.exit,
		Err:     j.errMsg,
		Cursor:  j.dropped + j.buf.Len(),
		Running: j.state == jobRunning,
		// Total bytes the job has produced, not just what is still buffered: a viewer
		// showing "12 KB" for a job that emitted 40 MB would be misleading.
		OutputLen: j.dropped + j.buf.Len(),
		TTY:       j.tty,
	}
}

// tail returns the job's status plus the output after absolute offset from, without
// advancing the model's read cursor.
func (j *job) tail(from int) JobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := j.infoLocked()
	all := j.buf.Bytes()
	// Cursors are absolute over the job's whole lifetime, but the buffer only keeps the
	// tail (trimLocked drops the front once the cap is hit). Rebase into buffer space,
	// so a cursor from before a trim resumes at the oldest KEPT byte instead of slicing
	// out of range or silently re-sending everything.
	rel := from - j.dropped
	if rel < 0 {
		rel = 0
	}
	// A cursor past the end means the caller is ahead of us — legitimate after a restart.
	// Return no output rather than panicking on a bad slice bound, and let the reported
	// Cursor pull the client back into sync.
	if rel > len(all) {
		return out
	}
	out.Output = string(all[rel:])
	out.Cursor = j.dropped + len(all)
	return out
}

// Jobs returns a status-only snapshot of every job, oldest id first. Safe on a nil
// store so callers don't have to special-case sessions without a local registry.
func (s *JobStore) Jobs() []JobInfo {
	if s == nil {
		return nil
	}
	jobs := s.list()
	out := make([]JobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.info())
	}
	return out
}

// Job returns one job's status, or false when the id is unknown.
func (s *JobStore) Job(id string) (JobInfo, bool) {
	if s == nil {
		return JobInfo{}, false
	}
	j, ok := s.get(id)
	if !ok {
		return JobInfo{}, false
	}
	return j.info(), true
}

// JobTail returns one job's status plus the output after absolute offset from.
//
// This is the browser's read path: it never advances the cursor bash_output reads
// from, so watching a job cannot starve the model of its output.
func (s *JobStore) JobTail(id string, from int) (JobInfo, bool) {
	if s == nil {
		return JobInfo{}, false
	}
	j, ok := s.get(id)
	if !ok {
		return JobInfo{}, false
	}
	return j.tail(from), true
}

// KillJob terminates one running job, reporting whether the id existed. Killing an
// already-finished job is a no-op that still reports true: the caller asked for it to
// be stopped, and it is stopped.
func (s *JobStore) KillJob(id string) bool {
	if s == nil {
		return false
	}
	j, ok := s.get(id)
	if !ok {
		return false
	}
	j.mu.Lock()
	running := j.state == jobRunning
	cancel := j.cancel
	j.mu.Unlock()
	if running && cancel != nil {
		cancel()
	}
	return true
}

// StartBackgroundJob launches a command as a background job and returns its id, or ""
// if no store is available. tty runs it under a pseudo-terminal (see jobtty.go).
//
// Deliberately reuses the registry's OWN sandbox setting and roots — the same values
// the bash tool passes — so a UI-started job is confined exactly like a model-issued
// one. Resolving them here rather than accepting them from the caller means a web
// handler cannot widen a job's authority by passing different ones.
func (r *Registry) StartBackgroundJob(command string, tty bool) string {
	if r == nil || r.jobs == nil {
		return ""
	}
	j := r.jobs.start(r.sandboxConfig(), r.allRoots(), command, jobOpts{tty: tty})
	if j == nil {
		return ""
	}
	return j.id
}

// SendJobInput types data into a running TTY job (see JobStore.SendInput). check
// vets the assembled input line; the agent passes its destructive-command refusal.
func (r *Registry) SendJobInput(id, data string, enter bool, check func(line string) error) error {
	if r == nil || r.jobs == nil {
		return fmt.Errorf("no such background job %q", id)
	}
	return r.jobs.SendInput(id, data, enter, check)
}
