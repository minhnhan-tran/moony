package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"
)

// jobState is the lifecycle status of a background bash job.
type jobState string

const (
	jobRunning jobState = "running"
	jobDone    jobState = "done"   // exited 0
	jobFailed  jobState = "failed" // exited non-zero
	jobKilled  jobState = "killed" // terminated by bash_kill or shutdown
)

// job is a single background shell command, its captured output, and status.
type job struct {
	id      string
	command string
	started time.Time

	mu     sync.Mutex
	buf    bytes.Buffer // combined stdout/stderr, grows as the process writes
	read   int          // bytes already returned by bash_output (for incremental reads)
	state  jobState
	exit   int    // exit code once finished
	errMsg string // non-empty if the process couldn't start / was killed

	cancel context.CancelFunc
}

// Write implements io.Writer so cmd can stream combined output into the buffer
// under the lock (stdout and stderr are both pointed here).
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

// snapshot returns any output not yet read plus the current status, advancing
// the read cursor so each bash_output call yields only new bytes.
func (j *job) snapshot() (newOutput string, state jobState, exit int, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	all := j.buf.Bytes()
	if j.read < len(all) {
		newOutput = string(all[j.read:])
		j.read = len(all)
	}
	return newOutput, j.state, j.exit, j.errMsg
}

// status returns the current state without consuming buffered output, so
// callers polling for completion don't steal bytes from the next snapshot.
func (j *job) status() jobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *job) setState(s jobState, exit int, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = s
	j.exit = exit
	if errMsg != "" {
		j.errMsg = errMsg
	}
}

// JobStore is a concurrency-safe registry of background bash jobs, owned by a
// tool Registry. Jobs are kept after they finish so the model can still read
// their final output; KillAll is called on shutdown to reap any survivors.
type JobStore struct {
	mu   sync.Mutex
	jobs map[string]*job
	next int
}

func newJobStore() *JobStore { return &JobStore{jobs: map[string]*job{}} }

func (s *JobStore) add(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.id] = j
}

func (s *JobStore) get(id string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) newID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return "bg_" + strconv.Itoa(s.next)
}

// list returns jobs sorted by id for stable display.
func (s *JobStore) list() []*job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].id < out[k].id })
	return out
}

// KillAll cancels every still-running job. Safe to call on a nil store.
func (s *JobStore) KillAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.mu.Lock()
		running := j.state == jobRunning
		cancel := j.cancel
		j.mu.Unlock()
		if running && cancel != nil {
			cancel()
		}
	}
}

// start launches command as a background job under the given sandbox config,
// returning the new job. The process runs detached from the caller's context
// (it uses a fresh context cancelled only by bash_kill or KillAll) so it
// survives the tool call that spawned it.
func (s *JobStore) start(set sandboxSetting, roots []string, command string) *job {
	jctx, cancel := context.WithCancel(context.Background())
	j := &job{
		id:      s.newID(),
		command: command,
		started: time.Now(),
		state:   jobRunning,
		cancel:  cancel,
	}

	name, args := sandboxCommand(set, roots, "sh", "-c", command)
	cmd := exec.CommandContext(jctx, name, args...)
	cmd.Dir = roots[0]
	cmd.Env = scrubbedEnvFor(roots[0]) // secrets stripped; $PWD pinned to the working dir
	cmd.Stdout = j
	cmd.Stderr = j

	s.add(j)

	if err := cmd.Start(); err != nil {
		j.setState(jobFailed, -1, fmt.Sprintf("failed to start: %v", err))
		cancel()
		return j
	}

	go func() {
		err := cmd.Wait()
		switch {
		case jctx.Err() == context.Canceled:
			j.setState(jobKilled, -1, "killed")
		case err == nil:
			j.setState(jobDone, 0, "")
		default:
			exit := -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			}
			j.setState(jobFailed, exit, err.Error())
		}
		cancel()
	}()

	return j
}
