// Package taskboard implements the web UI's Task Board: a queue of prompts run
// as independent headless sub-processes of this same binary, each isolated in
// its own throwaway git worktree so several tasks execute in parallel without
// touching the real working tree or each other. A finished task holds its
// changes as a diff you review and then either Apply (patch it onto the real
// working tree) or Discard (throw the worktree away).
//
// "Depends on" is start-ordering only: a dependent task won't begin until its
// prerequisites finish, but it still branches from the same HEAD — it does NOT
// inherit a prerequisite's edits. Apply the prerequisite first if the dependent
// needs its changes.
//
// State is in-memory for the life of the session (not persisted across a full
// process restart); worktrees live under
// <repo>/.sqe/worktrees and are cleaned up on Apply, Discard, or Close.
//
// Isolation is by convention, not a sandbox: a task's git *worktree* is
// separate, but the sub-process has normal OS filesystem/network access (the
// same trust level as the existing schedule feature), so a tool call could
// still reach outside the worktree.
package taskboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Status is a task's lifecycle state.
type Status string

const (
	Queued     Status = "queued"     // eligible to run once a slot is free
	Blocked    Status = "blocked"    // waiting on unfinished dependencies
	Running    Status = "running"    // sub-process in flight
	Done       Status = "done"       // finished; diff awaiting Apply/Discard
	Failed     Status = "failed"     // run errored (or a dependency failed)
	Applying   Status = "applying"   // Apply in progress (claim; blocks a racing Apply/Discard)
	Applied    Status = "applied"    // diff patched onto the real working tree
	Discarding Status = "discarding" // Discard in progress (claim)
	Discarded  Status = "discarded"  // worktree thrown away
)

// Task is one queued unit of work.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	DependsOn []string  `json:"dependsOn,omitempty"`
	Status    Status    `json:"status"`
	Branch    string    `json:"branch,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Response  string    `json:"response,omitempty"` // model's final answer
	Diff      string    `json:"diff,omitempty"`     // changes vs the base commit
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
}

// terminal reports whether the task has reached a state the runner won't leave.
func (t *Task) terminal() bool {
	switch t.Status {
	case Done, Failed, Applied, Discarded:
		return true
	}
	return false
}

// runFunc executes one prompt to completion in dir and returns the model's
// final answer. The default spawns this binary headlessly; tests inject a fake.
type runFunc func(ctx context.Context, prompt, dir string) (string, error)

const defaultTaskTimeout = 20 * time.Minute

// Board is one repository's task queue. All methods are safe for concurrent use.
type Board struct {
	repoRoot    string
	maxParallel int
	run         runFunc

	mu      sync.Mutex
	tasks   map[string]*Task
	order   []string
	running int
	seq     int
	wake    chan struct{}
	closed  bool
}

// New returns a Board that runs each task by spawning exe headlessly against an
// isolated worktree of repoRoot. maxParallel <= 0 defaults to 2.
func New(repoRoot, exe string, maxParallel int) *Board {
	return newBoard(repoRoot, maxParallel, subprocessRunner(exe))
}

func newBoard(repoRoot string, maxParallel int, run runFunc) *Board {
	if maxParallel <= 0 {
		maxParallel = 2
	}
	b := &Board{
		repoRoot:    repoRoot,
		maxParallel: maxParallel,
		run:         run,
		tasks:       map[string]*Task{},
		wake:        make(chan struct{}, 1),
	}
	go b.loop()
	return b
}

// subprocessRunner runs `exe -p <prompt> --json -C <dir> --no-approval` and
// returns the final answer from the JSON result. The worktree makes
// --no-approval safe: edits land in a throwaway checkout, never the real tree.
func subprocessRunner(exe string) runFunc {
	return func(ctx context.Context, prompt, dir string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, defaultTaskTimeout)
		defer cancel()
		out, err := exec.CommandContext(cctx, exe, "-p", prompt, "--json", "-C", dir, "--no-approval").Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
				return "", fmt.Errorf("%v: %s", err, collapse(string(ee.Stderr)))
			}
			return "", err
		}
		var r struct {
			OK       bool   `json:"ok"`
			Response string `json:"response"`
			Error    string `json:"error"`
		}
		if err := json.Unmarshal(out, &r); err != nil {
			return "", fmt.Errorf("unreadable headless result: %v", err)
		}
		if !r.OK {
			if r.Error == "" {
				r.Error = "headless run failed"
			}
			return "", fmt.Errorf("%s", r.Error)
		}
		return r.Response, nil
	}
}

// Enqueue adds a task. dependsOn ids must already exist on the board.
func (b *Board) Enqueue(title, prompt string, dependsOn []string) (Task, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Task{}, fmt.Errorf("prompt is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Task{}, fmt.Errorf("task board is closed")
	}
	for _, d := range dependsOn {
		if _, ok := b.tasks[d]; !ok {
			return Task{}, fmt.Errorf("unknown dependency %q", d)
		}
	}
	b.seq++
	id := fmt.Sprintf("t%d", b.seq)
	title = strings.TrimSpace(title)
	if title == "" {
		title = truncate(prompt, 60)
	}
	t := &Task{ID: id, Title: title, Prompt: prompt, DependsOn: dependsOn, Status: Queued, CreatedAt: time.Now()}
	b.tasks[id] = t
	b.order = append(b.order, id)
	b.signal()
	return *t, nil
}

// List returns a snapshot of every task in insertion order.
func (b *Board) List() []Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Task, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, *b.tasks[id])
	}
	return out
}

// Get returns a snapshot of one task.
func (b *Board) Get(id string) (Task, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// Apply patches a finished task's diff onto the real working tree, then removes
// its worktree. The task must be Done. It claims the task (status Applying)
// under the lock so a racing Apply/Discard is rejected, and reverts to Done if
// the patch fails so the user can retry.
func (b *Board) Apply(id string) (string, error) {
	b.mu.Lock()
	t, ok := b.tasks[id]
	if !ok {
		b.mu.Unlock()
		return "", fmt.Errorf("task %s not found", id)
	}
	if t.Status != Done {
		st := t.Status
		b.mu.Unlock()
		return "", fmt.Errorf("task is %s, not done", st)
	}
	t.Status = Applying // claim it so a concurrent Apply/Discard is rejected
	diff, worktree, branch := t.Diff, t.Worktree, t.Branch
	b.mu.Unlock()

	msg := "applied"
	if strings.TrimSpace(diff) == "" {
		msg = "no file changes to apply"
	} else if err := applyPatch(context.Background(), b.repoRoot, diff); err != nil {
		b.mu.Lock()
		t.Status = Done // revert the claim so Apply can be retried
		b.mu.Unlock()
		return "", fmt.Errorf("patch did not apply cleanly (the working tree may have changed since): %w", err)
	}
	removeWorktree(context.Background(), b.repoRoot, worktree, branch)

	b.mu.Lock()
	t.Status = Applied
	t.Worktree, t.Branch = "", ""
	b.mu.Unlock()
	b.signal()
	return msg, nil
}

// Discard drops a task. A Running/Applying/Discarding task can't be discarded;
// wait for it to settle. Queued/Blocked tasks are simply removed from
// scheduling; finished ones have their worktree cleaned up.
func (b *Board) Discard(id string) error {
	b.mu.Lock()
	t, ok := b.tasks[id]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("task %s not found", id)
	}
	switch t.Status {
	case Running, Applying, Discarding:
		st := t.Status
		b.mu.Unlock()
		return fmt.Errorf("cannot discard a %s task", st)
	}
	worktree, branch := t.Worktree, t.Branch
	t.Status = Discarding // claim it
	b.mu.Unlock()

	removeWorktree(context.Background(), b.repoRoot, worktree, branch)

	b.mu.Lock()
	t.Status = Discarded
	t.Worktree, t.Branch = "", ""
	b.mu.Unlock()
	b.signal()
	return nil
}

// Close stops the scheduler and best-effort removes any leftover worktrees.
// Running tasks are left to finish their sub-process (their worktree is cleaned
// on the next Close/Discard); we don't kill them mid-write.
func (b *Board) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	type wt struct{ path, branch string }
	var leftover []wt
	for _, id := range b.order {
		t := b.tasks[id]
		if t.Worktree != "" {
			leftover = append(leftover, wt{t.Worktree, t.Branch})
		}
	}
	b.mu.Unlock()
	b.signal() // wake loop so it observes closed and returns
	for _, w := range leftover {
		removeWorktree(context.Background(), b.repoRoot, w.path, w.branch)
	}
}

// loop is the scheduler: it launches eligible tasks up to maxParallel.
func (b *Board) loop() {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		var next *Task
		for _, id := range b.order {
			t := b.tasks[id]
			if t.Status != Queued && t.Status != Blocked {
				continue
			}
			ready, failed := b.depsSatisfiedLocked(t)
			if failed {
				t.Status = Failed
				t.Error = "a dependency failed or was discarded"
				t.EndedAt = time.Now()
				continue
			}
			if !ready {
				t.Status = Blocked
				continue
			}
			t.Status = Queued
			if next == nil && b.running < b.maxParallel {
				next = t
			}
		}
		if next != nil {
			next.Status = Running
			next.StartedAt = time.Now()
			b.running++
		}
		b.mu.Unlock()

		if next == nil {
			select {
			case <-b.wake:
			case <-time.After(2 * time.Second): // safety net
			}
			continue
		}
		go b.execute(next)
	}
}

// depsSatisfiedLocked reports whether all of t's dependencies have completed
// (ready) and whether any of them failed/were discarded (failed). Caller holds mu.
func (b *Board) depsSatisfiedLocked(t *Task) (ready, failed bool) {
	ready = true
	for _, dep := range t.DependsOn {
		d, ok := b.tasks[dep]
		if !ok {
			continue
		}
		switch d.Status {
		case Done, Applied:
			// satisfied
		case Failed, Discarded:
			failed = true
		default:
			ready = false
		}
	}
	return ready, failed
}

// execute runs one task in its own worktree and records the outcome.
func (b *Board) execute(t *Task) {
	ctx := context.Background()
	wt, err := createWorktree(ctx, b.repoRoot, t.ID)
	if err != nil {
		b.finish(t, nil, func(tt *Task) {
			tt.Status = Failed
			tt.Error = fmt.Sprintf("could not create an isolated worktree: %v", err)
		})
		return
	}

	resp, runErr := b.run(ctx, t.Prompt, wt.path)
	if runErr != nil {
		b.finish(t, wt, func(tt *Task) {
			tt.Status = Failed
			tt.Response = resp
			tt.Error = collapse(runErr.Error())
		})
		return
	}
	diff, diffErr := gitDiff(ctx, wt.path, wt.base)
	if diffErr != nil {
		// Don't silently mark Done with an empty diff — that would look like a
		// successful no-op and lose the (about-to-be-removed) changes.
		b.finish(t, wt, func(tt *Task) {
			tt.Status = Failed
			tt.Response = resp
			tt.Error = "could not capture the task's diff: " + collapse(diffErr.Error())
		})
		return
	}
	b.finish(t, wt, func(tt *Task) {
		tt.Status = Done
		tt.Response = resp
		tt.Diff = diff
		tt.Worktree = wt.path
		tt.Branch = wt.branch
	})
}

// finish records a task's terminal state (mutate runs under the lock), frees
// the running slot, and wakes the scheduler. The worktree is torn down
// immediately unless the task ended Done on a still-open board — i.e. failed
// tasks and tasks whose board closed mid-run never leak a worktree. (A Done
// task on a closed board keeps its diff in Task.Diff, so it's still applicable,
// but its now-unreachable worktree is removed.)
func (b *Board) finish(t *Task, wt *worktree, mutate func(*Task)) {
	b.mu.Lock()
	mutate(t)
	t.EndedAt = time.Now()
	if b.running > 0 {
		b.running--
	}
	keep := wt != nil && t.Status == Done && !b.closed
	if !keep {
		t.Worktree, t.Branch = "", ""
	}
	b.mu.Unlock()

	if wt != nil && !keep {
		removeWorktree(context.Background(), b.repoRoot, wt.path, wt.branch)
	}
	b.signal()
}

// signal nudges the scheduler loop without blocking.
func (b *Board) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func truncate(s string, n int) string {
	s = collapse(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// collapse flattens whitespace so multi-line text renders as one tidy line.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
