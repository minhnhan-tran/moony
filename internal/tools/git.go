package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runGit runs `git <args>` in dir, returning combined output (capped to max
// bytes). It does not use the bash sandbox — these are constrained git
// invocations, not arbitrary shell.
func runGit(ctx context.Context, dir string, max int, args ...string) (string, error) {
	return runGitEnv(ctx, dir, 30*time.Second, nil, max, args...)
}

// runGitEnv is runGit with an explicit timeout and optional extra environment.
// Used for network ops (push), which get a longer timeout and GIT_TERMINAL_PROMPT=0
// so a missing credential helper fails fast instead of blocking on a prompt.
func runGitEnv(ctx context.Context, dir string, timeout time.Duration, extraEnv []string, max int, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if max > 0 {
		text = truncate(text, max)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("git %s timed out", args[0])
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

// GitInfo reports the current branch and the number of changed (dirty) entries
// for a working directory. isRepo is false when dir isn't inside a git repo.
func GitInfo(dir string) (branch string, dirty int, isRepo bool) {
	ctx := context.Background()
	b, err := runGit(ctx, dir, 0, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", 0, false
	}
	branch = strings.TrimSpace(b)
	if branch == "HEAD" {
		// Detached: use the short SHA instead.
		if sha, err := runGit(ctx, dir, 0, "rev-parse", "--short", "HEAD"); err == nil {
			branch = strings.TrimSpace(sha)
		}
	}
	if st, err := runGit(ctx, dir, 0, "status", "--porcelain"); err == nil {
		st = strings.TrimSpace(st)
		if st != "" {
			dirty = len(strings.Split(st, "\n"))
		}
	}
	return branch, dirty, true
}

// GitAheadBehind reports how many commits the current branch is ahead/behind its
// upstream, plus the upstream ref name. ok is false when there's no upstream (or
// not a repo). Used by the status token (↑/↓) and the git modal.
func GitAheadBehind(dir string) (ahead, behind int, upstream string, ok bool) {
	ctx := context.Background()
	up, err := runGit(ctx, dir, 0, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return 0, 0, "", false
	}
	upstream = strings.TrimSpace(up)
	counts, err := runGit(ctx, dir, 0, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	if err != nil {
		return 0, 0, upstream, false
	}
	f := strings.Fields(strings.TrimSpace(counts)) // "<behind>\t<ahead>" → behind ahead
	if len(f) == 2 {
		behind, _ = strconv.Atoi(f[0])
		ahead, _ = strconv.Atoi(f[1])
	}
	return ahead, behind, upstream, true
}

// GitChange is one entry from `git status --porcelain` for the git modal.
type GitChange struct {
	Path     string `json:"path"`
	Status   string `json:"status"`   // two-char XY code, e.g. " M", "A ", "??"
	Staged   bool   `json:"staged"`   // the index (X) column has a change
	Unstaged bool   `json:"unstaged"` // the worktree (Y) column has a change
	Label    string `json:"label"`    // human label: Modified/Added/Deleted/Renamed/Untracked/…
}

// GitCommit is one recent commit for the git modal.
type GitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	When    string `json:"when"` // relative, e.g. "2 hours ago"
	Author  string `json:"author"`
}

// GitOverview is the read-only snapshot the web git modal renders.
type GitOverview struct {
	IsRepo    bool        `json:"isRepo"`
	Branch    string      `json:"branch"`
	Upstream  string      `json:"upstream,omitempty"`
	Ahead     int         `json:"ahead"`
	Behind    int         `json:"behind"`
	HasRemote bool        `json:"hasRemote"` // a remote is configured (push is possible)
	Changes   []GitChange `json:"changes"`
	Commits   []GitCommit `json:"commits"`
}

// GitOverviewFor gathers a read-only git snapshot of dir: branch, upstream
// ahead/behind, the porcelain working-tree changes, and the most recent commits.
// All read-only git calls — safe to run on any request.
func GitOverviewFor(dir string, maxBytes int) GitOverview {
	branch, _, isRepo := GitInfo(dir)
	ov := GitOverview{IsRepo: isRepo, Branch: branch}
	if !isRepo {
		return ov
	}
	ov.Ahead, ov.Behind, ov.Upstream, _ = GitAheadBehind(dir)
	ov.HasRemote = gitHasRemote(dir)
	ov.Changes = gitChanges(dir)
	ov.Commits = gitRecentCommits(dir, 20)
	return ov
}

// gitHasRemote reports whether the repo has at least one configured remote.
func gitHasRemote(dir string) bool {
	out, err := runGit(context.Background(), dir, 0, "remote")
	return err == nil && strings.TrimSpace(out) != ""
}

// gitChanges parses `git status --porcelain` into structured entries.
func gitChanges(dir string) []GitChange {
	out, err := runGit(context.Background(), dir, 0, "status", "--porcelain")
	if err != nil {
		return nil
	}
	var changes []GitChange
	for _, ln := range strings.Split(out, "\n") {
		if len(ln) < 3 {
			continue
		}
		xy := ln[:2]
		path := strings.TrimSpace(ln[3:])
		// Renames are "old -> new"; show the new path.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		x, y := xy[0], xy[1]
		changes = append(changes, GitChange{
			Path:     path,
			Status:   xy,
			Staged:   x != ' ' && x != '?',
			Unstaged: y != ' ' && y != '?' || xy == "??",
			Label:    gitStatusLabel(xy),
		})
	}
	return changes
}

// gitStatusLabel turns a porcelain XY code into a short human label.
func gitStatusLabel(xy string) string {
	if xy == "??" {
		return "Untracked"
	}
	code := xy[0]
	if code == ' ' {
		code = xy[1]
	}
	switch code {
	case 'M':
		return "Modified"
	case 'A':
		return "Added"
	case 'D':
		return "Deleted"
	case 'R':
		return "Renamed"
	case 'C':
		return "Copied"
	case 'U':
		return "Conflicted"
	default:
		return "Changed"
	}
}

// gitRecentCommits returns up to n recent commits (hash, subject, relative time,
// author) for the git modal's history list.
func gitRecentCommits(dir string, n int) []GitCommit {
	// Unit-separator-delimited fields; one commit per line.
	out, err := runGit(context.Background(), dir, 0, "log", "-n", strconv.Itoa(n), "--pretty=format:%h\x1f%s\x1f%cr\x1f%an")
	if err != nil || out == "" {
		return nil
	}
	var commits []GitCommit
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Split(ln, "\x1f")
		if len(f) < 4 {
			continue
		}
		commits = append(commits, GitCommit{Hash: f[0], Subject: f[1], When: f[2], Author: f[3]})
	}
	return commits
}

// GitDiffFor returns the diff for the working tree (staged or unstaged), or a
// single file when path is set. Read-only. "" path means the whole tree.
func GitDiffFor(dir string, staged bool, path string, maxBytes int) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--staged")
	}
	if strings.TrimSpace(path) != "" {
		args = append(args, "--", path)
	}
	return runGit(context.Background(), dir, maxBytes, args...)
}

// GitCommitDiffFor returns the diff a commit introduced (read-only). hash must be
// a short/long SHA; it's validated as hex-ish to avoid arg injection.
func GitCommitDiffFor(dir, hash string, maxBytes int) (string, error) {
	hash = strings.TrimSpace(hash)
	if !isHexish(hash) {
		return "", fmt.Errorf("invalid commit hash")
	}
	return runGit(context.Background(), dir, maxBytes, "show", "--stat", "--patch", hash)
}

// isHexish reports whether s looks like a git short/long object id (hex, 4–40).
func isHexish(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// validRepoPath guards a client-supplied path for the write ops below: it must be
// a specific, repo-relative path — not empty, not "." (which would let a discard
// `git clean` wipe the whole tree), not absolute, and not escaping via "..".
// Combined with the "-- <path>" passed to git (which stops option injection and
// scopes to the work tree), this keeps stage/unstage/discard to one intended file.
func validRepoPath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return fmt.Errorf("a specific file path is required")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path must be relative to the repo")
	}
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return fmt.Errorf("path escapes the repo")
	}
	return nil
}

// GitStage stages one path (git add). Mutating (working-tree only, not a commit).
func GitStage(dir, path string) error {
	if err := validRepoPath(path); err != nil {
		return err
	}
	_, err := runGit(context.Background(), dir, 0, "add", "--", path)
	return err
}

// GitUnstage unstages one path (git restore --staged), reverting the index entry
// to HEAD without touching the working tree. Falls back to `reset` on older git.
func GitUnstage(dir, path string) error {
	if err := validRepoPath(path); err != nil {
		return err
	}
	if _, err := runGit(context.Background(), dir, 0, "restore", "--staged", "--", path); err != nil {
		_, err2 := runGit(context.Background(), dir, 0, "reset", "-q", "HEAD", "--", path)
		return err2
	}
	return nil
}

// GitDiscard discards one path's changes — DESTRUCTIVE (uncommitted work is lost):
// an untracked file is removed (git clean), a tracked file is reverted to HEAD
// in both the index and the working tree (git restore --staged --worktree).
func GitDiscard(dir, path string) error {
	if err := validRepoPath(path); err != nil {
		return err
	}
	st, _ := runGit(context.Background(), dir, 0, "status", "--porcelain", "--", path)
	if strings.HasPrefix(strings.TrimSpace(st), "??") {
		_, err := runGit(context.Background(), dir, 0, "clean", "-fd", "--", path)
		return err
	}
	_, err := runGit(context.Background(), dir, 0, "restore", "--staged", "--worktree", "--", path)
	return err
}

// GitCommitChanges records a commit from this user-initiated web action (distinct
// from the agent's hard-gated git_commit tool — here a human typed the message and
// confirmed in the modal). With all=true it stages tracked modifications first
// (git commit -a); otherwise only already-staged changes are committed. message
// is required and committing with nothing staged is reported as an error. Returns
// the short git summary line. Never pushes.
func GitCommitChanges(dir, message string, all bool) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("a commit message is required")
	}
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-m", message)
	out, err := runGit(context.Background(), dir, 1<<16, args...)
	if err != nil {
		return "", err
	}
	return firstLine(out), nil
}

// GitPush pushes the current branch to its upstream — a user-initiated web action
// (a human clicked Push and confirmed). It sets the upstream on the first push of
// a branch (push -u origin <branch>). GIT_TERMINAL_PROMPT=0 makes a missing
// credential helper fail fast rather than hang on an interactive prompt, and the
// network op gets a longer timeout. Returns git's summary output. Never force-pushes.
func GitPush(dir string) (string, error) {
	branch, _, isRepo := GitInfo(dir)
	if !isRepo {
		return "", fmt.Errorf("not a git repository")
	}
	if !gitHasRemote(dir) {
		return "", fmt.Errorf("no git remote is configured")
	}
	args := []string{"push"}
	if _, _, _, hasUpstream := GitAheadBehind(dir); !hasUpstream {
		// First push of this branch: create and track origin/<branch>.
		args = append(args, "-u", "origin", branch)
	}
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	out, err := runGitEnv(context.Background(), dir, 120*time.Second, env, 1<<16, args...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		out = "Pushed " + branch + "."
	}
	return out, nil
}

// --- read-only tools --------------------------------------------------------

type gitStatusTool struct {
	r   *Registry
	max int
}

func (t *gitStatusTool) Name() string { return "git_status" }
func (t *gitStatusTool) Description() string {
	return "Show the working tree status (current branch, ahead/behind, and changed files) via 'git status --short --branch'."
}
func (t *gitStatusTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{}}`)
}
func (t *gitStatusTool) Mutates() bool { return false }
func (t *gitStatusTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	out, err := runGit(ctx, t.r.root, t.max, "status", "--short", "--branch")
	if err != nil {
		return "", err
	}
	if out == "" {
		out = "(clean working tree)"
	}
	t.r.searchCacheStore(t.Name(), input, out)
	return out, nil
}

type gitDiffTool struct {
	r   *Registry
	max int
}

func (t *gitDiffTool) Name() string { return "git_diff" }
func (t *gitDiffTool) Description() string {
	return "Show a unified diff of changes. By default shows unstaged changes; set staged=true for the index. Optionally limit to a path."
}
func (t *gitDiffTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"staged": {"type": "boolean", "description": "Show staged (index) changes instead of unstaged."},
			"path":   {"type": "string", "description": "Limit the diff to this file or directory."}
		}
	}`)
}
func (t *gitDiffTool) Mutates() bool { return false }
func (t *gitDiffTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Staged bool   `json:"staged"`
		Path   string `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	args := []string{"diff"}
	if in.Staged {
		args = append(args, "--staged")
	}
	if strings.TrimSpace(in.Path) != "" {
		args = append(args, "--", in.Path)
	}
	out, err := runGit(ctx, t.r.root, t.max, args...)
	if err != nil {
		return "", err
	}
	if out == "" {
		out = "(no changes)"
	}
	t.r.searchCacheStore(t.Name(), input, out)
	return out, nil
}

type gitLogTool struct {
	r   *Registry
	max int
}

func (t *gitLogTool) Name() string { return "git_log" }
func (t *gitLogTool) Description() string {
	return "Show recent commits, one per line (hash + subject), via 'git log --oneline'."
}
func (t *gitLogTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"count": {"type": "integer", "description": "How many commits to show (default 15, max 100)."},
			"path":  {"type": "string", "description": "Limit to commits touching this file or directory."}
		}
	}`)
}
func (t *gitLogTool) Mutates() bool { return false }
func (t *gitLogTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Count flexInt `json:"count"`
		Path  string  `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	n := int(in.Count)
	if n <= 0 {
		n = 15
	}
	if n > 100 {
		n = 100
	}
	args := []string{"log", "--oneline", "-n", strconv.Itoa(n)}
	if strings.TrimSpace(in.Path) != "" {
		args = append(args, "--", in.Path)
	}
	out, err := runGit(ctx, t.r.root, t.max, args...)
	if err != nil {
		return "", err
	}
	if out == "" {
		out = "(no commits)"
	}
	t.r.searchCacheStore(t.Name(), input, out)
	return out, nil
}

// --- mutating tools ---------------------------------------------------------

type gitCommitTool struct {
	r   *Registry
	max int
}

func (t *gitCommitTool) Name() string { return "git_commit" }
func (t *gitCommitTool) Description() string {
	return "Create a git commit with the given message. Set all=true to stage all tracked modifications first (git commit -a); otherwise only already-staged changes are committed. Never pushes."
}
func (t *gitCommitTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"message": {"type": "string", "description": "The commit message."},
			"all":     {"type": "boolean", "description": "Stage all tracked modifications before committing (git commit -a)."}
		},
		"required": ["message"]
	}`)
}
func (t *gitCommitTool) Mutates() bool { return true }

func (t *gitCommitTool) commitInput(input json.RawMessage) (message string, all bool, err error) {
	var in struct {
		Message string `json:"message"`
		All     bool   `json:"all"`
	}
	if err = decode(input, &in); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(in.Message) == "" {
		return "", false, fmt.Errorf("message is required")
	}
	return in.Message, in.All, nil
}

// Preview shows what will be committed so the approval prompt has a diff.
func (t *gitCommitTool) Preview(ctx context.Context, input json.RawMessage) (Preview, error) {
	msg, all, err := t.commitInput(input)
	if err != nil {
		return Preview{}, err
	}
	diffArgs := []string{"diff", "--stat", "--staged"}
	summary := "commit staged changes"
	if all {
		diffArgs = []string{"diff", "--stat"} // working-tree changes that -a will stage
		summary = "commit all tracked changes"
	}
	diff, _ := runGit(ctx, t.r.root, t.max, diffArgs...)
	return Preview{Summary: summary + ": " + firstLine(msg), Diff: diff}, nil
}

func (t *gitCommitTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	defer t.r.invalidateSearches() // a commit changes git status/diff output
	msg, all, err := t.commitInput(input)
	if err != nil {
		return "", err
	}
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-m", msg)
	out, err := runGit(ctx, t.r.root, t.max, args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

type gitBranchTool struct {
	r *Registry
}

func (t *gitBranchTool) Name() string { return "git_branch" }
func (t *gitBranchTool) Description() string {
	return "Switch to a branch, or create and switch to a new one (set create=true). Use git_status to see the current branch."
}
func (t *gitBranchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"name":   {"type": "string", "description": "The branch to switch to (or create)."},
			"create": {"type": "boolean", "description": "Create the branch before switching (git checkout -b)."}
		},
		"required": ["name"]
	}`)
}
func (t *gitBranchTool) Mutates() bool { return true }
func (t *gitBranchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	defer t.r.invalidateSearches() // switching branches changes git status/diff output
	var in struct {
		Name   string `json:"name"`
		Create bool   `json:"create"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Name) == "" {
		return "", fmt.Errorf("name is required")
	}
	args := []string{"checkout"}
	if in.Create {
		args = append(args, "-b")
	}
	args = append(args, in.Name)
	out, err := runGit(ctx, t.r.root, 1<<16, args...)
	if err != nil {
		return "", err
	}
	if out == "" {
		out = "Switched to " + in.Name
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
