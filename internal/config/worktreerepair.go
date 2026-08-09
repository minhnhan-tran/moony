// worktreerepair.go — restoring git's worktree bookkeeping after the project
// config dir is renamed.

package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RepairWorktrees re-points git's worktree bookkeeping at <ProjectDir>/worktrees.
//
// Necessary because git records ABSOLUTE paths in both directions: the worktree's
// .git file points at <repo>/.git/worktrees/<name>, and that dir's `gitdir` file
// points back at the worktree's .git file. Renaming the project config dir
// invalidates the second pointer, so `git worktree list` reports the OLD path and
// marks it `prunable` — a later `git worktree prune` would then drop the
// registration even though the checkout is still there.
//
// `git worktree repair` must be given the NEW path explicitly; called with no
// argument it does not find the moved worktrees.
//
// Best-effort by design: a repo without worktrees, without git, or with a git too
// old for `repair` (added in 2.30) simply gets no repair. Never fails the caller,
// since a stranded worktree is recoverable but refusing to start is not.
func RepairWorktrees(workingDir string) (repaired []string) {
	if workingDir == "" {
		return nil
	}
	root := filepath.Join(workingDir, ProjectDir, "worktrees")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // no worktrees to repair
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only real worktrees have a .git entry; skip unrelated leftovers.
		p := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			continue
		}
		// Absolute: cmd.Dir below is workingDir, so a RELATIVE path built from
		// workingDir would be resolved against it a second time
		// ("proj/.moony/..." becoming "proj/proj/.moony/...") and git would fail
		// to find any worktree.
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil
	}

	// Bounded: a hung git must not stall startup.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// One invocation with every path: repair is idempotent, and doing it in a
	// single call keeps the cost flat regardless of worktree count.
	args := append([]string{"worktree", "repair"}, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workingDir
	if err := cmd.Run(); err != nil {
		return nil
	}
	return paths
}
