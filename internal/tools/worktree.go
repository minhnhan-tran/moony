package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// worktreeDir is the path (relative to the repo root) under which the agent's
// isolated git worktrees live. Keeping them inside the working directory means
// the existing path sandbox and ignore rules apply to them unchanged.
const worktreeDir = ".sqe/worktrees"

// gitWorktreeTool lets the model create, list, remove, and merge back isolated
// git worktrees so a task's changes are confined to a throwaway branch +
// directory until they're explicitly merged into the current branch.
type gitWorktreeTool struct {
	r   *Registry
	max int
}

func (t *gitWorktreeTool) Name() string { return "git_worktree" }

func (t *gitWorktreeTool) Description() string {
	return "Manage isolated git worktrees so a task's edits stay on a throwaway branch and directory until you merge them. " +
		"Actions: 'add' creates a worktree on a new branch under " + worktreeDir + "/<name> (returns its path — do your work there with the file/bash tools); " +
		"'list' shows existing worktrees; 'remove' deletes a worktree (and optionally its branch); " +
		"'merge' merges a worktree's branch into the current branch, then removes the worktree. " +
		"Use this to try risky or large changes in isolation before integrating them."
}

func (t *gitWorktreeTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["add", "list", "remove", "merge"], "description": "The operation to perform."},
			"name":   {"type": "string", "description": "Worktree name (used for the directory and branch). Required for add/remove/merge."},
			"branch": {"type": "string", "description": "For 'add': the new branch name to create (defaults to sqe/<name>). For 'merge': informational only."},
			"base":   {"type": "string", "description": "For 'add': the commit/branch to base the new worktree on (defaults to the current HEAD)."},
			"delete_branch": {"type": "boolean", "description": "For 'remove': also delete the worktree's branch (git branch -D). Default false."},
			"force":  {"type": "boolean", "description": "For 'remove'/'merge': force removal even with uncommitted changes in the worktree."}
		},
		"required": ["action"]
	}`)
}

func (t *gitWorktreeTool) Mutates() bool { return true }

type worktreeInput struct {
	Action       string `json:"action"`
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	DeleteBranch bool   `json:"delete_branch"`
	Force        bool   `json:"force"`
}

// sanitizeName rejects names that would escape the worktree directory or
// contain path separators / traversal.
func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return "", fmt.Errorf("name %q must not contain path separators or '..'", name)
	}
	return name, nil
}

// worktreePath returns the absolute path of the named worktree, guaranteed to
// be inside the repo root.
func (t *gitWorktreeTool) worktreePath(name string) string {
	return filepath.Join(t.r.root, worktreeDir, name)
}

// excludeWorktreeDir adds the worktree base dir to the repo's LOCAL exclude
// file (<git-common-dir>/info/exclude) so the nested worktrees don't show up as
// untracked in the PARENT repo's status — which would otherwise inflate the
// dirty count (status bar / web header) and clutter git_status. It's local and
// uncommitted: it never touches the user's tracked .gitignore, and it excludes
// only worktreeDir, leaving the rest of .sqe/ (commands, agents, mcp.json, …)
// visible to git. Best-effort and idempotent — any failure is ignored since
// this is cosmetic cleanup, not correctness.
func (t *gitWorktreeTool) excludeWorktreeDir(ctx context.Context) {
	commonDir, err := runGit(ctx, t.r.root, 0, "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(t.r.root, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	entry := worktreeDir + "/" // ".sqe/worktrees/"

	data, _ := os.ReadFile(excludePath)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return // already excluded
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	out := string(data)
	if len(out) > 0 && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += entry + "\n"
	_ = os.WriteFile(excludePath, []byte(out), 0o644)
}

func (t *gitWorktreeTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in worktreeInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "add":
		return t.add(ctx, in)
	case "list", "":
		return t.list(ctx)
	case "remove", "rm":
		return t.remove(ctx, in)
	case "merge":
		return t.merge(ctx, in)
	default:
		return "", fmt.Errorf("unknown action %q (want add, list, remove, or merge)", in.Action)
	}
}

func (t *gitWorktreeTool) add(ctx context.Context, in worktreeInput) (string, error) {
	name, err := sanitizeName(in.Name)
	if err != nil {
		return "", err
	}
	abs := t.worktreePath(name)
	if _, statErr := os.Stat(abs); statErr == nil {
		return "", fmt.Errorf("worktree %q already exists at %s", name, filepath.Join(worktreeDir, name))
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "sqe/" + name
	}
	// `git worktree add -b <branch> <path> [<base>]` creates the directory,
	// a new branch, and checks it out there in one step.
	args := []string{"worktree", "add", "-b", branch, abs}
	if base := strings.TrimSpace(in.Base); base != "" {
		args = append(args, base)
	}
	out, err := runGit(ctx, t.r.root, t.max, args...)
	if err != nil {
		return "", err
	}
	// Keep the nested worktree out of the parent repo's status (dirty count +
	// git_status) without touching the user's tracked .gitignore.
	t.excludeWorktreeDir(ctx)
	rel := filepath.Join(worktreeDir, name)
	return fmt.Sprintf("Created worktree %q on branch %q at %s\nWork inside %s/ to keep changes isolated; use action \"merge\" to integrate, or \"remove\" to discard.\n%s",
		name, branch, rel, rel, out), nil
}

func (t *gitWorktreeTool) list(ctx context.Context) (string, error) {
	out, err := runGit(ctx, t.r.root, t.max, "worktree", "list")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(no worktrees)", nil
	}
	return out, nil
}

func (t *gitWorktreeTool) remove(ctx context.Context, in worktreeInput) (string, error) {
	name, err := sanitizeName(in.Name)
	if err != nil {
		return "", err
	}
	abs := t.worktreePath(name)
	args := []string{"worktree", "remove", abs}
	if in.Force {
		args = append(args, "--force")
	}
	if _, err := runGit(ctx, t.r.root, t.max, args...); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Removed worktree %q.", name)
	if in.DeleteBranch {
		branch := strings.TrimSpace(in.Branch)
		if branch == "" {
			branch = "sqe/" + name
		}
		flag := "-d"
		if in.Force {
			flag = "-D"
		}
		if out, err := runGit(ctx, t.r.root, t.max, "branch", flag, branch); err != nil {
			return msg + fmt.Sprintf("\nCould not delete branch %q: %v", branch, err), nil
		} else {
			msg += fmt.Sprintf("\nDeleted branch %q. %s", branch, strings.TrimSpace(out))
		}
	}
	return msg, nil
}

func (t *gitWorktreeTool) merge(ctx context.Context, in worktreeInput) (string, error) {
	name, err := sanitizeName(in.Name)
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "sqe/" + name
	}
	abs := t.worktreePath(name)
	if _, statErr := os.Stat(abs); statErr != nil {
		return "", fmt.Errorf("worktree %q not found at %s", name, filepath.Join(worktreeDir, name))
	}
	// Refuse to merge a dirty worktree unless forced — uncommitted work would
	// be left behind.
	if !in.Force {
		if st, _ := runGit(ctx, abs, t.max, "status", "--porcelain"); strings.TrimSpace(st) != "" {
			return "", fmt.Errorf("worktree %q has uncommitted changes; commit them first or pass force=true", name)
		}
	}
	out, err := runGit(ctx, t.r.root, t.max, "merge", "--no-ff", branch)
	if err != nil {
		return "", fmt.Errorf("merge of %q failed (resolve in the main tree, or abort with `git merge --abort`): %v", branch, err)
	}
	// Clean up the worktree now that its commits are merged. Leave the branch
	// in place so the history reference survives.
	rmArgs := []string{"worktree", "remove", abs}
	if in.Force {
		rmArgs = append(rmArgs, "--force")
	}
	rmNote := ""
	if _, rmErr := runGit(ctx, t.r.root, t.max, rmArgs...); rmErr != nil {
		rmNote = fmt.Sprintf("\n(merged, but could not remove worktree dir: %v)", rmErr)
	}
	return fmt.Sprintf("Merged branch %q into the current branch and removed worktree %q.%s\n%s", branch, name, rmNote, out), nil
}

// Preview describes the worktree action for the approval prompt.
func (t *gitWorktreeTool) Preview(ctx context.Context, input json.RawMessage) (Preview, error) {
	var in worktreeInput
	if err := decode(input, &in); err != nil {
		return Preview{}, err
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "add":
		branch := strings.TrimSpace(in.Branch)
		if branch == "" {
			branch = "sqe/" + strings.TrimSpace(in.Name)
		}
		return Preview{Summary: fmt.Sprintf("create isolated worktree %q on branch %q", in.Name, branch)}, nil
	case "remove", "rm":
		return Preview{Summary: fmt.Sprintf("remove worktree %q", in.Name)}, nil
	case "merge":
		branch := strings.TrimSpace(in.Branch)
		if branch == "" {
			branch = "sqe/" + strings.TrimSpace(in.Name)
		}
		// Show what the merge would bring in.
		diff, _ := runGit(ctx, t.r.root, t.max, "diff", "--stat", "HEAD.."+branch)
		return Preview{Summary: fmt.Sprintf("merge worktree branch %q into the current branch", branch), Diff: diff}, nil
	default:
		return Preview{Summary: "git worktree " + action}, nil
	}
}
