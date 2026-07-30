package taskboard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

// waitFor polls cond until true or the deadline; fails the test otherwise.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func statusOf(b *Board, id string) Status {
	t, _ := b.Get(id)
	return t.Status
}

// A task's file edits are captured as a diff, then Apply replays them onto the
// real working tree and Discard/Close cleans up.
func TestBoardRunAndApply(t *testing.T) {
	repo := newTestRepo(t)
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte(prompt), 0o644); err != nil {
			return "", err
		}
		return "created new.txt", nil
	}
	b := newBoard(repo, 2, runner)
	defer b.Close()

	task, err := b.Enqueue("make a file", "hello world", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", func() bool { return statusOf(b, task.ID) == Done })

	got, _ := b.Get(task.ID)
	if !strings.Contains(got.Diff, "new.txt") {
		t.Errorf("diff should mention new.txt, got:\n%s", got.Diff)
	}
	if got.Response != "created new.txt" {
		t.Errorf("response = %q", got.Response)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt must NOT be in the real tree before Apply")
	}

	if _, err := b.Apply(task.ID); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if statusOf(b, task.ID) != Applied {
		t.Errorf("status = %s, want applied", statusOf(b, task.ID))
	}
	data, err := os.ReadFile(filepath.Join(repo, "new.txt"))
	if err != nil {
		t.Fatalf("new.txt should exist after Apply: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("new.txt = %q", data)
	}
	// Worktree cleaned up.
	if got, _ := b.Get(task.ID); got.Worktree != "" {
		t.Errorf("worktree should be cleared after Apply, got %q", got.Worktree)
	}
}

// dependsOn delays a task until its prerequisite finishes.
func TestBoardDependencyOrdering(t *testing.T) {
	repo := newTestRepo(t)
	release := make(chan struct{})
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		if prompt == "first" {
			<-release // hold task A until the test lets go
		}
		return "ok", nil
	}
	b := newBoard(repo, 4, runner) // ample parallelism; ordering must come from deps
	defer b.Close()

	a, _ := b.Enqueue("A", "first", nil)
	c, _ := b.Enqueue("C", "second", []string{a.ID})

	waitFor(t, "A running", func() bool { return statusOf(b, a.ID) == Running })
	if s := statusOf(b, c.ID); s != Blocked && s != Queued {
		t.Fatalf("C should be waiting on A, got %s", s)
	}
	// C must not have started while A is still running.
	if cc, _ := b.Get(c.ID); !cc.StartedAt.IsZero() {
		t.Fatal("C started before its dependency finished")
	}
	close(release)
	waitFor(t, "both done", func() bool { return statusOf(b, a.ID) == Done && statusOf(b, c.ID) == Done })

	aa, _ := b.Get(a.ID)
	cc, _ := b.Get(c.ID)
	if cc.StartedAt.Before(aa.EndedAt) {
		t.Errorf("C started at %v, before A ended at %v", cc.StartedAt, aa.EndedAt)
	}
}

// A failed dependency fails its dependents instead of running them.
func TestBoardFailureCascade(t *testing.T) {
	repo := newTestRepo(t)
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		if prompt == "boom" {
			return "", context.DeadlineExceeded
		}
		return "ok", nil
	}
	b := newBoard(repo, 2, runner)
	defer b.Close()

	a, _ := b.Enqueue("A", "boom", nil)
	c, _ := b.Enqueue("C", "depends", []string{a.ID})

	waitFor(t, "A failed", func() bool { return statusOf(b, a.ID) == Failed })
	waitFor(t, "C failed", func() bool { return statusOf(b, c.ID) == Failed })
	cc, _ := b.Get(c.ID)
	if !strings.Contains(cc.Error, "dependency") {
		t.Errorf("C error = %q, want it to mention the failed dependency", cc.Error)
	}
}

func TestEnqueueValidation(t *testing.T) {
	b := newBoard(t.TempDir(), 2, func(context.Context, string, string) (string, error) { return "", nil })
	defer b.Close()
	if _, err := b.Enqueue("", "   ", nil); err == nil {
		t.Error("empty prompt should error")
	}
	if _, err := b.Enqueue("", "real prompt", []string{"nope"}); err == nil {
		t.Error("unknown dependency should error")
	}
}

func TestApplyRequiresDone(t *testing.T) {
	b := newBoard(t.TempDir(), 2, func(context.Context, string, string) (string, error) { return "", nil })
	defer b.Close()
	if _, err := b.Apply("missing"); err == nil {
		t.Error("applying a missing task should error")
	}
}

// Discard of a finished task removes its worktree from disk.
func TestBoardDiscardCleansWorktree(t *testing.T) {
	repo := newTestRepo(t)
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		return "ok", os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	}
	b := newBoard(repo, 2, runner)
	defer b.Close()
	task, _ := b.Enqueue("", "do it", nil)
	waitFor(t, "done", func() bool { return statusOf(b, task.ID) == Done })

	got, _ := b.Get(task.ID)
	if got.Worktree == "" {
		t.Fatal("expected a worktree path on the done task")
	}
	wtPath := got.Worktree
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree should exist on disk: %v", err)
	}
	if err := b.Discard(task.ID); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if statusOf(b, task.ID) != Discarded {
		t.Errorf("status = %s, want discarded", statusOf(b, task.ID))
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after Discard, stat err = %v", err)
	}
}

// The scheduler never runs more than maxParallel tasks at once.
func TestBoardRespectsMaxParallel(t *testing.T) {
	repo := newTestRepo(t)
	var mu sync.Mutex
	cur, peak := 0, 0
	gate := make(chan struct{})
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		<-gate // hold every task until released so they pile up
		mu.Lock()
		cur--
		mu.Unlock()
		return "ok", nil
	}
	b := newBoard(repo, 2, runner)
	defer b.Close()
	for i := 0; i < 5; i++ {
		b.Enqueue("", "task", nil)
	}
	// Give the scheduler time to saturate its slots.
	waitFor(t, "two running", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return cur == 2
	})
	time.Sleep(100 * time.Millisecond) // ensure it doesn't exceed the cap
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	close(gate)
	if gotPeak > 2 {
		t.Errorf("peak concurrency = %d, want <= 2", gotPeak)
	}
}

// Closing the board while a task runs must not leak that task's worktree.
func TestBoardCloseCleansRunningWorktree(t *testing.T) {
	repo := newTestRepo(t)
	started := make(chan string, 1)
	release := make(chan struct{})
	runner := func(ctx context.Context, prompt, dir string) (string, error) {
		if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
			return "", err
		}
		started <- dir
		<-release
		return "ok", nil
	}
	b := newBoard(repo, 2, runner)
	b.Enqueue("", "do it", nil)

	dir := <-started // task is now running inside its worktree
	b.Close()        // closes while the task is still in flight
	close(release)   // let it finish; finish() must clean up because closed

	waitFor(t, "worktree removed", func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
}

func TestTruncateAndCollapse(t *testing.T) {
	if got := collapse("a\n b\tc"); got != "a b c" {
		t.Errorf("collapse = %q", got)
	}
	if got := truncate("one two three", 7); got != "one two…" {
		t.Errorf("truncate = %q", got)
	}
}
