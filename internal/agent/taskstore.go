package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Background / async sub-agents. A `task` call with background:true starts the
// sub-agent detached and returns a handle immediately, so the orchestrator can
// keep working while a long explore/review runs. The run's streamed text is
// buffered here with a read cursor (incremental polling, like a bash background
// job's snapshot), and its final result is stored on completion. The three
// companion tools — task_output, task_status, task_cancel — read/cancel via this
// store. Mirrors internal/tools JobStore.

type taskState int

const (
	taskRunning taskState = iota
	taskDone
	taskFailed
	taskCancelled
)

func (s taskState) String() string {
	switch s {
	case taskDone:
		return "done"
	case taskFailed:
		return "failed"
	case taskCancelled:
		return "cancelled"
	default:
		return "running"
	}
}

// bgTask is one detached sub-agent run.
type bgTask struct {
	id      string
	persona string
	prompt  string
	started time.Time
	cancel  context.CancelFunc

	mu     sync.Mutex
	buf    strings.Builder // all streamed text so far
	cursor int             // bytes of buf already handed out via snapshot
	state  taskState
	result string  // final result text (set once, on completion)
	errMsg string  // set when state == taskFailed
	cost   float64 // total USD across the sub-agent's turn(s)
	inTok  int
	outTok int
	ended  time.Time
}

// appendOutput records streamed text as it arrives.
func (t *bgTask) appendOutput(s string) {
	t.mu.Lock()
	t.buf.WriteString(s)
	t.mu.Unlock()
}

// finish records the terminal state, final result and usage totals. Safe to call
// once; later calls are ignored so a cancel racing completion can't clobber it.
func (t *bgTask) finish(state taskState, result, errMsg string, cost float64, in, out int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != taskRunning {
		return
	}
	t.state = state
	t.result = result
	t.errMsg = errMsg
	t.cost = cost
	t.inTok = in
	t.outTok = out
	t.ended = time.Now()
}

// snapshot returns text buffered since the last snapshot (advancing the cursor)
// plus the current status; done reports whether the task has finished.
func (t *bgTask) snapshot() (newOutput string, state taskState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	full := t.buf.String()
	if t.cursor < len(full) {
		newOutput = full[t.cursor:]
		t.cursor = len(full)
	}
	return newOutput, t.state
}

// status returns the current state without consuming buffered output.
func (t *bgTask) status() taskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// finalResult returns the final result text (empty until done) and usage footer
// fields.
func (t *bgTask) finalResult() (result, errMsg string, cost float64, in, out int, dur time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := time.Duration(0)
	if !t.ended.IsZero() {
		d = t.ended.Sub(t.started)
	}
	return t.result, t.errMsg, t.cost, t.inTok, t.outTok, d
}

// TaskStore is a concurrency-safe registry of background sub-agent runs, keyed by
// handle (task_bg_N). Owned by the Agent; shared with nested sub-agents so any
// handle is pollable session-wide.
type TaskStore struct {
	mu    sync.Mutex
	tasks map[string]*bgTask
	seq   int
}

func newTaskStore() *TaskStore { return &TaskStore{tasks: map[string]*bgTask{}} }

// newID allocates the next handle (task_bg_1, task_bg_2, …).
func (s *TaskStore) newID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("task_bg_%d", s.seq)
}

func (s *TaskStore) add(t *bgTask) {
	s.mu.Lock()
	s.tasks[t.id] = t
	s.mu.Unlock()
}

func (s *TaskStore) get(id string) (*bgTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	return t, ok
}

// list returns the tasks sorted by handle for stable listing in error messages.
func (s *TaskStore) list() []*bgTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*bgTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	// Insertion order ~ handle order; sort by started to be deterministic.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].started.After(out[j].started); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// handles returns the live (running) handles, for unknown-handle error messages.
func (s *TaskStore) handles() []string {
	var live []string
	for _, t := range s.list() {
		if t.status() == taskRunning {
			live = append(live, t.id)
		}
	}
	return live
}

// KillAll cancels every running background task; called on session shutdown
// alongside the bash JobStore's KillAll so no detached sub-agent outlives it.
func (s *TaskStore) KillAll() {
	s.mu.Lock()
	tasks := make([]*bgTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	s.mu.Unlock()
	for _, t := range tasks {
		if t.status() == taskRunning && t.cancel != nil {
			t.cancel()
		}
	}
}
