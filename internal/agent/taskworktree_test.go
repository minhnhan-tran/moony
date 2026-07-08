package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a temp git repo with one committed file ("a.txt") and
// returns its path. Skips the test when git isn't available. Uses a minimal,
// isolated env (no global/system config) so the commit can't be tripped up by a
// developer's gpgsign/hooks settings.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir, // no ~/.gitconfig here → fully isolated
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

// TestIsolatedWorktree covers the full lifecycle: create off HEAD, edit inside it
// without touching the main tree, capture the change set as a diff, and clean up.
func TestIsolatedWorktree(t *testing.T) {
	repo := initGitRepo(t)
	ctx := context.Background()
	if !isGitWorktreeRepo(ctx, repo) {
		t.Fatal("expected repo to be a git work tree")
	}

	w, err := newIsolatedWorktree(ctx, repo, "Builder #1")
	if err != nil {
		t.Fatalf("newIsolatedWorktree: %v", err)
	}
	// The worktree base is git-excluded so it won't show as untracked in the repo.
	if ex, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); !strings.Contains(string(ex), ".sqe/worktrees/") {
		t.Errorf("expected .sqe/worktrees/ in the repo's git exclude, got:\n%s", ex)
	}
	// Edit an existing file and add a new one INSIDE the worktree.
	if err := os.WriteFile(filepath.Join(w.path, "a.txt"), []byte("changed in worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.path, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := w.diff(ctx, 1<<20)
	for _, want := range []string{"a.txt", "new.go", "changed in worktree", "package x"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	// The MAIN tree is untouched while the isolated work happens.
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "original\n" {
		t.Errorf("main tree should be untouched, a.txt=%q", got)
	}

	w.cleanup()
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be removed after cleanup, stat err=%v", err)
	}
}

// TestIsolatedWorktreeNonGit: a plain dir isn't a work tree, so isolation reports
// so and the caller falls back to running in the main tree.
func TestIsolatedWorktreeNonGit(t *testing.T) {
	if isGitWorktreeRepo(context.Background(), t.TempDir()) {
		t.Error("a plain temp dir should not be reported as a git work tree")
	}
}

func TestSanitizeWorktreeLabel(t *testing.T) {
	cases := map[string]string{
		"explore":       "explore",
		"Builder #1":    "builder-1",
		"  weird/name ": "weird-name",
		"":              "agent",
		"!!!":           "agent",
	}
	for in, want := range cases {
		if got := sanitizeWorktreeLabel(in); got != want {
			t.Errorf("sanitizeWorktreeLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeWorktreeLabel("a/b c..d"); strings.ContainsAny(got, " /\\") || strings.Contains(got, "..") {
		t.Errorf("token must be path/branch-safe, got %q", got)
	}
}
