package taskboard

// worktree.go — self-contained git plumbing for the Task Board. Each task runs
// in a throwaway worktree checked out off HEAD under <repo>/.moony/worktrees, on
// its own branch, so parallel tasks never collide. We capture the task's work
// as a diff against the base commit (covering both committed and uncommitted
// changes) and, on Apply, replay that diff onto the real working tree.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

const worktreeSubdir = config.ProjectDir + "/worktrees"

// Branch prefix for taskboard worktrees (renamed from the legacy "sqe-board/").
const boardBranchPrefix = "moony-board/"

var (
	wtSeq atomic.Int64
	// opMu serializes worktree add/remove so concurrent tasks don't race on
	// git's shared worktree metadata / the info/exclude file.
	opMu sync.Mutex
)

type worktree struct {
	path   string
	branch string
	base   string // commit the worktree branched from; diffs are taken against it
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// createWorktree adds a fresh worktree of repoRoot on a new branch, off HEAD.
func createWorktree(ctx context.Context, repoRoot, label string) (*worktree, error) {
	base, err := git(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("not a git repository (or no commits yet): %s", collapse(base))
	}
	base = strings.TrimSpace(base)

	n := wtSeq.Add(1)
	name := fmt.Sprintf("board-%s-%d-%d", sanitize(label), time.Now().UnixNano(), n)
	path := filepath.Join(repoRoot, worktreeSubdir, name)
	branch := boardBranchPrefix + name
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	opMu.Lock()
	out, err := git(ctx, repoRoot, "worktree", "add", "-b", branch, path, "HEAD")
	if err == nil {
		excludeFromStatus(ctx, repoRoot)
	}
	opMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("git worktree add: %s", collapse(out))
	}
	return &worktree{path: path, branch: branch, base: base}, nil
}

// removeWorktree tears down a worktree and deletes its branch (best effort).
func removeWorktree(ctx context.Context, repoRoot, path, branch string) {
	if path == "" {
		return
	}
	opMu.Lock()
	defer opMu.Unlock()
	_, _ = git(ctx, repoRoot, "worktree", "remove", "--force", path)
	if branch != "" {
		_, _ = git(ctx, repoRoot, "branch", "-D", branch)
	}
}

// gitDiff returns the change from base to the worktree's current state,
// including any commits the task made plus uncommitted edits AND newly created
// (untracked) files — we stage everything first so `git diff` sees new files,
// which it otherwise omits.
func gitDiff(ctx context.Context, worktreePath, base string) (string, error) {
	_, _ = git(ctx, worktreePath, "add", "-A")
	// --binary so a task that touches a binary file still produces an
	// applyable patch (otherwise `git apply` rejects the whole diff).
	out, err := git(ctx, worktreePath, "diff", "--no-color", "--binary", "--cached", base)
	if err != nil {
		return "", fmt.Errorf("git diff: %s", collapse(out))
	}
	return out, nil
}

// applyPatch replays a captured diff onto repoRoot's working tree.
func applyPatch(ctx context.Context, repoRoot, diff string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = repoRoot
	cmd.Stdin = strings.NewReader(diff)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", collapse(string(out)))
	}
	return nil
}

// excludeFromStatus keeps the worktrees dir out of `git status` for the repo,
// so the isolated checkouts never show up as untracked noise.
func excludeFromStatus(ctx context.Context, repoRoot string) {
	commonDir, err := git(ctx, repoRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoRoot, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	line := "/" + worktreeSubdir + "/"
	if b, _ := os.ReadFile(excludePath); strings.Contains(string(b), line) {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n" + line + "\n")
}

// sanitize reduces a label to a short filesystem/branch-safe slug.
func sanitize(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "task"
	}
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	return out
}
