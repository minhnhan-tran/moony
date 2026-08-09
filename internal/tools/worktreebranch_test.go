package tools

import (
	"context"
	"os/exec"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// A worktree created by an older build sits on a "sqe/<name>" branch. After the
// rename, merge and remove must still find it without the user passing --branch,
// or those worktrees become unmergeable.
func TestDefaultBranchFallsBackToLegacyPrefix(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := gitRepo(t) // shared helper: an initialised repo with one commit
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	tool := newWorktreeTool(dir)
	ctx := context.Background()

	// Nothing exists yet: assume the current prefix.
	if got := tool.defaultBranch(ctx, "feature"); got != branchPrefix+"feature" {
		t.Errorf("with no branches: got %q, want %q", got, branchPrefix+"feature")
	}

	// A legacy branch exists and the new one does not: use the legacy name.
	git("branch", legacyBranchPrefix+"feature")
	if got := tool.defaultBranch(ctx, "feature"); got != legacyBranchPrefix+"feature" {
		t.Errorf("legacy branch present: got %q, want %q", got, legacyBranchPrefix+"feature")
	}

	// Both exist: the current prefix wins.
	git("branch", branchPrefix+"feature")
	if got := tool.defaultBranch(ctx, "feature"); got != branchPrefix+"feature" {
		t.Errorf("both present: got %q, want the current prefix %q", got, branchPrefix+"feature")
	}
}

// The worktree directory constant must track the project config dir.
func TestWorktreeDirUsesProjectDir(t *testing.T) {
	if want := config.ProjectDir + "/worktrees"; worktreeDir != want {
		t.Errorf("worktreeDir = %q, want %q", worktreeDir, want)
	}
}
