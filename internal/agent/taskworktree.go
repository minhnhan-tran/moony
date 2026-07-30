package agent

// taskworktree.go gives delegated sub-agents optional git-worktree isolation: a
// task run with isolate=true executes in a throwaway worktree checked out off
// HEAD, so several editing sub-agents can run in PARALLEL over independent files
// without clobbering each other (or the user's working tree). Its changes are
// returned to the orchestrator as a diff to apply — sub-agents can't commit (the
// hard-gate blocks that), and we never auto-mutate the main tree from a
// concurrent sub-agent, so synthesis stays in the parent's single-threaded hands.

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

	"github.com/pysty1995/moony/internal/tools"
)

// taskWorktreeDir mirrors the git_worktree tool's location so isolated task
// worktrees live alongside (and are ignored like) the manual ones.
const taskWorktreeDir = ".sqe/worktrees"

// worktreeSeq makes concurrent isolated-task worktree names unique.
var worktreeSeq atomic.Int64

// worktreeOpMu serializes the quick git worktree-metadata mutations (add/remove),
// which share the repo's worktree lock; the sub-agent's actual work and per-
// worktree diff run concurrently outside it.
var worktreeOpMu sync.Mutex

// gitText runs git in dir with the secret-scrubbed env (parity with bash/verify)
// and returns trimmed combined output. A short timeout keeps a wedged git from
// hanging a turn.
func gitText(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = tools.ScrubbedEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// isGitWorktreeRepo reports whether dir is inside a git working tree (worktree
// isolation needs one).
func isGitWorktreeRepo(ctx context.Context, dir string) bool {
	out, err := gitText(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// isolatedWorktree is a throwaway git worktree for one delegated sub-agent.
type isolatedWorktree struct {
	path   string // absolute worktree dir (under repo/.sqe/worktrees)
	branch string
	repo   string // the parent repo root
}

// newIsolatedWorktree creates a throwaway worktree off HEAD for an isolated
// sub-agent; label (the persona name) is sanitized into the dir/branch name.
func newIsolatedWorktree(ctx context.Context, repoRoot, label string) (*isolatedWorktree, error) {
	seq := worktreeSeq.Add(1)
	name := fmt.Sprintf("task-%s-%d-%d-%d", sanitizeWorktreeLabel(label), time.Now().UnixNano(), os.Getpid(), seq)
	path := filepath.Join(repoRoot, taskWorktreeDir, name)
	branch := "sqe-task/" + name
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	worktreeOpMu.Lock()
	// Sweep leftovers from prior runs that were killed before their cleanup()
	// defer could fire (crash, SIGKILL, power loss): `git worktree prune` drops
	// admin entries whose dirs vanished, and pruneStaleTaskWorktrees removes stale
	// on-disk task worktrees + their branches. Without this, a killed agent leaves
	// a locked worktree that blocks future adds and inflates the repo. Best-effort.
	pruneStaleTaskWorktrees(ctx, repoRoot)
	out, err := gitText(ctx, repoRoot, "worktree", "add", "-b", branch, path)
	if err == nil {
		// Mark the tree as in-use so a concurrent sweep (pruneStaleTaskWorktrees)
		// can't reap it just for being old — a genuinely long sub-agent run would
		// otherwise have its worktree deleted underneath it. cleanup() unlocks before
		// removing; a crashed run leaves the lock, and the sweep's age check plus
		// `git worktree prune` still reclaim it once the dir is gone.
		_, _ = gitText(ctx, repoRoot, "worktree", "lock", path)
		// Keep isolated worktrees out of the parent repo's git status (a leaked or
		// in-flight one would otherwise inflate the dirty count / clutter git_status).
		// Must run within the lock to prevent concurrent write race on info/exclude.
		excludeFromGitStatus(ctx, repoRoot)
	}
	worktreeOpMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("git worktree add: %v: %s", err, out)
	}
	return &isolatedWorktree{path: path, branch: branch, repo: repoRoot}, nil
}

// excludeFromGitStatus adds the worktree base dir to the repo's LOCAL git exclude
// (<git-common-dir>/info/exclude) so isolated worktrees never appear as untracked
// in the parent repo. Local, uncommitted, idempotent, best-effort — it never
// touches the user's tracked .gitignore. Mirrors the git_worktree tool.
func excludeFromGitStatus(ctx context.Context, repoRoot string) {
	commonDir, err := gitText(ctx, repoRoot, "rev-parse", "--git-common-dir")
	if err != nil || commonDir == "" {
		return
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoRoot, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	entry := taskWorktreeDir + "/" // ".sqe/worktrees/"
	data, _ := os.ReadFile(excludePath)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return // already excluded
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	body := string(data)
	if len(body) > 0 && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	_ = os.WriteFile(excludePath, []byte(body+entry+"\n"), 0o644)
}

// diff stages and returns the worktree's full change set vs HEAD (modified, new,
// and deleted files), or "" when nothing changed. Capped to maxBytes. Per-
// worktree index, so it's safe to run concurrently across worktrees.
func (w *isolatedWorktree) diff(ctx context.Context, maxBytes int) string {
	if _, err := gitText(ctx, w.path, "add", "-A"); err != nil {
		return ""
	}
	out, err := gitText(ctx, w.path, "diff", "--cached", "HEAD")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[:maxBytes]
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		}
		out += "… [diff truncated]"
	}
	return out
}

// cleanup removes the worktree and deletes its throwaway branch. Best-effort, and
// it uses a FRESH context (not the turn's) on purpose: cleanup runs from a defer,
// so it must still fire when the turn was cancelled/interrupted — otherwise the
// worktree dir and branch would leak on disk. gitText caps it with its own timeout.
func (w *isolatedWorktree) cleanup() {
	worktreeOpMu.Lock()
	defer worktreeOpMu.Unlock()
	ctx := context.Background()
	// Unlock first: `worktree remove` refuses a locked tree.
	_, _ = gitText(ctx, w.repo, "worktree", "unlock", w.path)
	_, _ = gitText(ctx, w.repo, "worktree", "remove", "--force", w.path)
	_, _ = gitText(ctx, w.repo, "branch", "-D", w.branch)
}

// staleTaskWorktreeAge is how old an on-disk task worktree must be before the
// sweep will force-remove it. Well beyond any realistic sub-agent turn, so a
// live concurrent worktree is never touched; it only reaps genuine leaks from
// killed prior runs.
const staleTaskWorktreeAge = 6 * time.Hour

// staleTaskWorktreeLockedAge is the (much longer) age after which even a LOCKED
// worktree is reaped. A crashed run leaves its lock behind forever, so the lock
// alone can't mean "never touch"; this bound keeps such leaks from accumulating
// while still being far past any real sub-agent run.
const staleTaskWorktreeLockedAge = 7 * 24 * time.Hour

// pruneStaleTaskWorktrees clears leaked isolation worktrees left by agents that
// died before cleanup(): (1) `git worktree prune` drops registry entries + locks
// whose working dirs are already gone; (2) any .sqe/worktrees/task-* dir older
// than staleTaskWorktreeAge is force-removed and its sqe-task/* branch deleted.
// Called under worktreeOpMu. Entirely best-effort — a failure just leaves the
// leftover for a later sweep and never blocks a new worktree.
func pruneStaleTaskWorktrees(ctx context.Context, repoRoot string) {
	// Drop admin entries/locks for worktrees whose dirs no longer exist.
	_, _ = gitText(ctx, repoRoot, "worktree", "prune")

	base := filepath.Join(repoRoot, taskWorktreeDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return // no worktree dir yet — nothing to sweep
	}
	// Age alone is not proof a worktree is abandoned: a legitimately long sub-agent
	// run (or one whose dir mtime simply stopped changing) would be destroyed under
	// it. Only reap trees git still lists but that are ALSO old, and skip anything
	// git reports as locked — `git worktree lock` is how a live run marks itself.
	locked := lockedWorktreePaths(ctx, repoRoot)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "task-") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleTaskWorktreeAge {
			continue // fresh: could be a live concurrent worktree — leave it
		}
		stale := filepath.Join(base, e.Name())
		if locked[stale] && time.Since(info.ModTime()) < staleTaskWorktreeLockedAge {
			continue // locked and not ancient: assume a live run still owns it
		}
		// Remove via git first so its admin entry goes too; then rm the dir and
		// branch in case git had already forgotten it. Unlock first — `remove` refuses
		// a locked tree even with --force, which is what makes a crashed run's
		// leftover lock otherwise un-reapable.
		_, _ = gitText(ctx, repoRoot, "worktree", "unlock", stale)
		_, _ = gitText(ctx, repoRoot, "worktree", "remove", "--force", stale)
		_ = os.RemoveAll(stale)
		_, _ = gitText(ctx, repoRoot, "branch", "-D", "sqe-task/"+e.Name())
	}
	_, _ = gitText(ctx, repoRoot, "worktree", "prune")
}

// lockedWorktreePaths returns the set of worktree paths git reports as LOCKED,
// which is how a live isolated run marks its tree as in-use. Best-effort: on any
// error the set is empty, and callers then fall back to the age check alone.
func lockedWorktreePaths(ctx context.Context, repoRoot string) map[string]bool {
	out := map[string]bool{}
	txt, err := gitText(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return out
	}
	cur := ""
	for _, line := range strings.Split(txt, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "locked"):
			if cur != "" {
				out[cur] = true
			}
		}
	}
	return out
}

// sanitizeWorktreeLabel reduces a persona name to a filesystem/branch-safe token
// (lowercase alphanumerics; runs of other chars collapse to a single '-').
func sanitizeWorktreeLabel(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
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
		out = "agent"
	}
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	return out
}
