package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWorktreeTool(dir string) *gitWorktreeTool {
	return &gitWorktreeTool{r: NewRegistry(dir), max: 1 << 20}
}

func TestGitWorktreeLifecycle(t *testing.T) {
	dir := gitRepo(t)
	tool := newWorktreeTool(dir)
	ctx := context.Background()

	// add
	out, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"feature"}`))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "sqe/feature") {
		t.Errorf("add output missing branch name: %q", out)
	}
	wtPath := filepath.Join(dir, worktreeDir, "feature")
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	// list shows it
	out, err = tool.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "feature") {
		t.Errorf("list missing worktree: %q", out)
	}

	// adding the same name again fails
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"feature"}`)); err == nil {
		t.Error("expected error re-adding existing worktree")
	}

	// remove (with branch deletion)
	out, err = tool.Execute(ctx, json.RawMessage(`{"action":"remove","name":"feature","delete_branch":true}`))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, stat err=%v", err)
	}
	if !strings.Contains(out, "Deleted branch") {
		t.Errorf("remove should report branch deletion: %q", out)
	}
}

func TestGitWorktreeMerge(t *testing.T) {
	dir := gitRepo(t)
	tool := newWorktreeTool(dir)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"work"}`)); err != nil {
		t.Fatalf("add: %v", err)
	}
	wtPath := filepath.Join(dir, worktreeDir, "work")

	// Make a commit inside the worktree.
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, wtPath, "add", "new.txt")
	gitExec(t, wtPath, "commit", "-q", "-m", "add new.txt")

	// Merge it back.
	out, err := tool.Execute(ctx, json.RawMessage(`{"action":"merge","name":"work"}`))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(out, "Merged branch") {
		t.Errorf("merge output unexpected: %q", out)
	}
	// File should now exist in the main tree, and the worktree removed.
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Errorf("merged file missing from main tree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed after merge, stat err=%v", err)
	}
}

func TestGitWorktreeMergeDirtyRefused(t *testing.T) {
	dir := gitRepo(t)
	tool := newWorktreeTool(dir)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"wip"}`)); err != nil {
		t.Fatalf("add: %v", err)
	}
	wtPath := filepath.Join(dir, worktreeDir, "wip")
	// Uncommitted change.
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, wtPath, "add", "dirty.txt")

	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"merge","name":"wip"}`)); err == nil {
		t.Error("expected merge to refuse a dirty worktree")
	}
}

func TestGitWorktreeBadName(t *testing.T) {
	tool := newWorktreeTool(t.TempDir())
	ctx := context.Background()
	for _, name := range []string{"", "../escape", "a/b"} {
		in := json.RawMessage(`{"action":"add","name":"` + name + `"}`)
		if _, err := tool.Execute(ctx, in); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

// TestGitWorktreeExcludedFromParentStatus checks that creating a worktree under
// .sqe/worktrees/ doesn't leave the parent repo dirty — the worktree base dir
// is added to .git/info/exclude so it never shows as untracked (which would
// inflate the status-bar dirty count and clutter git_status).
func TestGitWorktreeExcludedFromParentStatus(t *testing.T) {
	dir := gitRepo(t)
	tool := newWorktreeTool(dir)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"iso"}`)); err != nil {
		t.Fatalf("add: %v", err)
	}
	st, err := runGit(ctx, dir, 1<<16, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.TrimSpace(st) != "" {
		t.Errorf("parent repo should be clean after add (worktree must be git-excluded), got:\n%s", st)
	}

	// The exclude entry must be the precise worktree dir, not all of .sqe/, so
	// tracked project config under .sqe/ (commands, agents, mcp.json, …) stays
	// visible to git.
	excl, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if !strings.Contains(string(excl), worktreeDir+"/") {
		t.Errorf("exclude file missing %q entry:\n%s", worktreeDir+"/", excl)
	}
	if strings.Contains(string(excl), "\n.sqe/\n") {
		t.Errorf("exclude should not blanket-ignore all of .sqe/:\n%s", excl)
	}

	// Idempotent: a second add must not duplicate the exclude entry.
	if _, err := tool.Execute(ctx, json.RawMessage(`{"action":"add","name":"iso2"}`)); err != nil {
		t.Fatalf("second add: %v", err)
	}
	excl2, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if n := strings.Count(string(excl2), worktreeDir+"/"); n != 1 {
		t.Errorf("exclude entry should appear exactly once, found %d:\n%s", n, excl2)
	}
}
