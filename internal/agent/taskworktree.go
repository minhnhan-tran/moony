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
	out, err := gitText(ctx, repoRoot, "worktree", "add", "-b", branch, path)
	if err == nil {
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
	_, _ = gitText(ctx, w.repo, "worktree", "remove", "--force", w.path)
	_, _ = gitText(ctx, w.repo, "branch", "-D", w.branch)
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
