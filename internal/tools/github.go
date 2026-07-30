package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// runGH runs `gh <args>` in dir with a hard timeout and a non-interactive
// environment, so missing/expired GitHub-CLI auth fails fast instead of blocking
// on a credential prompt (this agent has no TTY — see the GIT_TERMINAL_PROMPT=0
// rule in AGENTS.md). Combined output is capped to max bytes. gh talks to
// whichever host it is configured for (github.com or an Enterprise host such as
// github.pie.apple.com) using its own stored auth; we never pass tokens.
//
// It returns the captured output even on a non-zero exit (many gh subcommands —
// e.g. `gh pr checks` — signal state through the exit code while still printing
// useful output), leaving callers to decide whether the non-zero exit is fatal.
func runGH(ctx context.Context, dir string, timeout time.Duration, max int, args ...string) (out string, exitErr error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("the GitHub CLI `gh` is not installed or not on PATH; install it and run `gh auth login` to use this tool")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1",    // never drop into an interactive prompt
		"GH_NO_UPDATE_NOTIFIER=1", // no background "update available" noise
		"GIT_TERMINAL_PROMPT=0",   // gh shells out to git for some ops
		"NO_COLOR=1", "CLICOLOR=0",
	)
	raw, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(raw), "\n")
	if max > 0 {
		text = truncate(text, max)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("gh %s timed out after %s", firstArg(args), timeout)
	}
	if err != nil {
		return text, fmt.Errorf("gh %s exited with error: %v", firstArg(args), err)
	}
	return text, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// currentBranch returns the checked-out branch name (best-effort; "" on detached
// HEAD or error), used as the default PR/CI target when the caller omits a ref.
func currentBranch(ctx context.Context, dir string) string {
	b, err := runGit(ctx, dir, 0, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	b = strings.TrimSpace(b)
	if b == "HEAD" { // detached
		return ""
	}
	return b
}

// ─── pull_request ────────────────────────────────────────────────────────────

// pullRequestTool wraps `gh pr` for the create / view / comment actions. It
// mutates remote state (create, comment) so it is approval-gated; it implements
// Previewer to show what will be opened/posted before the agent runs it.
type pullRequestTool struct {
	r   *Registry
	max int
}

func (t *pullRequestTool) Name() string { return "pull_request" }
func (t *pullRequestTool) Description() string {
	return "Manage GitHub pull requests via the `gh` CLI. action=create opens a PR for the current branch (title required; optional body, base branch, draft); action=comment posts a comment (body required; optional number, else the current branch's PR); action=view shows a PR with its comments (optional number, else the current branch's PR). Requires `gh` installed and authenticated (`gh auth login`); works against github.com or an Enterprise host. The branch must already be pushed for create."
}
func (t *pullRequestTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["create", "view", "comment"], "description": "Which PR operation to perform."},
			"title":  {"type": "string", "description": "PR title (create)."},
			"body":   {"type": "string", "description": "PR body (create) or comment text (comment)."},
			"base":   {"type": "string", "description": "Base branch to merge into (create); defaults to the repo default branch."},
			"draft":  {"type": "boolean", "description": "Open as a draft PR (create)."},
			"number": {"type": "integer", "description": "PR number (view/comment); defaults to the current branch's PR."}
		},
		"required": ["action"]
	}`)
}

// Mutates reports true so create/comment are approval-gated. view is read-only
// but the interface's approval flag is per-tool, not per-action, so it is gated
// too (a harmless confirm for a read).
func (t *pullRequestTool) Mutates() bool { return true }

type prInput struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Base   string `json:"base"`
	Draft  bool   `json:"draft"`
	Number int    `json:"number"`
}

func (t *pullRequestTool) parse(input json.RawMessage) (prInput, error) {
	var in prInput
	if err := decode(input, &in); err != nil {
		return in, err
	}
	in.Action = strings.TrimSpace(strings.ToLower(in.Action))
	switch in.Action {
	case "create":
		if strings.TrimSpace(in.Title) == "" {
			return in, fmt.Errorf("title is required for action=create")
		}
	case "comment":
		if strings.TrimSpace(in.Body) == "" {
			return in, fmt.Errorf("body is required for action=comment")
		}
	case "view":
	default:
		return in, fmt.Errorf("action must be one of create, view, comment (got %q)", in.Action)
	}
	return in, nil
}

func (t *pullRequestTool) Preview(ctx context.Context, input json.RawMessage) (Preview, error) {
	in, err := t.parse(input)
	if err != nil {
		return Preview{}, err
	}
	switch in.Action {
	case "create":
		base := in.Base
		if base == "" {
			base = "(default branch)"
		}
		kind := "pull request"
		if in.Draft {
			kind = "draft pull request"
		}
		// Show the commits that would ship in the PR for context.
		commits, _ := runGit(ctx, t.r.root, t.max, "log", "--oneline", "-20", "@{upstream}..HEAD")
		diff := in.Body
		if strings.TrimSpace(commits) != "" {
			diff = "commits:\n" + commits + "\n\n" + in.Body
		}
		return Preview{Summary: fmt.Sprintf("open %s into %s: %s", kind, base, firstLine(in.Title)), Diff: diff}, nil
	case "comment":
		target := "the current branch's PR"
		if in.Number > 0 {
			target = "PR #" + strconv.Itoa(in.Number)
		}
		return Preview{Summary: "comment on " + target, Diff: in.Body}, nil
	default: // view
		target := "the current branch's PR"
		if in.Number > 0 {
			target = "PR #" + strconv.Itoa(in.Number)
		}
		return Preview{Summary: "view " + target + " (read-only)"}, nil
	}
}

func (t *pullRequestTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	in, err := t.parse(input)
	if err != nil {
		return "", err
	}
	switch in.Action {
	case "create":
		args := []string{"pr", "create", "--title", in.Title, "--body", in.Body}
		if in.Base != "" {
			args = append(args, "--base", in.Base)
		}
		if in.Draft {
			args = append(args, "--draft")
		}
		out, err := runGH(ctx, t.r.root, 60*time.Second, t.max, args...)
		if err != nil {
			return "", fmt.Errorf("%w\n%s", err, out)
		}
		return "Opened pull request:\n" + out, nil
	case "comment":
		args := []string{"pr", "comment"}
		if in.Number > 0 {
			args = append(args, strconv.Itoa(in.Number))
		}
		args = append(args, "--body", in.Body)
		out, err := runGH(ctx, t.r.root, 45*time.Second, t.max, args...)
		if err != nil {
			return "", fmt.Errorf("%w\n%s", err, out)
		}
		return "Posted comment:\n" + out, nil
	default: // view
		args := []string{"pr", "view"}
		if in.Number > 0 {
			args = append(args, strconv.Itoa(in.Number))
		}
		args = append(args, "--comments")
		out, err := runGH(ctx, t.r.root, 45*time.Second, t.max, args...)
		if err != nil {
			return "", fmt.Errorf("%w\n%s", err, out)
		}
		return out, nil
	}
}

// ─── ci_status ───────────────────────────────────────────────────────────────

// ciStatusTool reports the CI check status for a branch or PR via `gh pr checks`
// (falling back to `gh run list` when the ref has no PR), and — for failed
// checks — appends the failing logs (`gh run view --log-failed`), so the agent
// can close the loop after a push: see what broke and fix it, mirroring the
// local build/test verify loop but for remote CI. Read-only (no approval).
type ciStatusTool struct {
	r   *Registry
	max int
}

func (t *ciStatusTool) Name() string { return "ci_status" }
func (t *ciStatusTool) Description() string {
	return "Report CI / GitHub Actions check status for a branch or PR via the `gh` CLI, and include the logs of any FAILED checks so you can diagnose and fix them (the remote counterpart of running build/tests locally). ref is a branch name or PR number (defaults to the current branch). Set failed_logs=false to skip fetching failure logs. Takes a single snapshot — call again to poll while checks are still pending. Requires `gh` installed and authenticated."
}
func (t *ciStatusTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"ref":         {"type": "string", "description": "Branch name or PR number to inspect. Defaults to the current branch."},
			"failed_logs": {"type": "boolean", "description": "Fetch and include logs for failed checks (default true)."}
		}
	}`)
}
func (t *ciStatusTool) Mutates() bool { return false }

func (t *ciStatusTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Ref        string `json:"ref"`
		FailedLogs *bool  `json:"failed_logs"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	withLogs := in.FailedLogs == nil || *in.FailedLogs

	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		ref = currentBranch(ctx, t.r.root)
		if ref == "" {
			return "", fmt.Errorf("no ref given and the current HEAD is detached; pass ref (a branch name or PR number)")
		}
	}
	// `gh run list`/failedRunLogs need an actual branch name. When ref is a PR
	// number, resolve its head branch instead of reusing the number as a bogus
	// branch name (which would silently match nothing).
	branch := ref
	if _, numErr := strconv.Atoi(ref); numErr == nil {
		if head, herr := runGH(ctx, t.r.root, 20*time.Second, 256, "pr", "view", ref, "--json", "headRefName", "--jq", ".headRefName"); herr == nil {
			if h := strings.TrimSpace(head); h != "" {
				branch = h
			}
		}
	}

	var b strings.Builder
	// `gh pr checks` exits non-zero when checks are failing/pending but still
	// prints the useful table, so keep its output regardless of the exit code.
	checks, err := runGH(ctx, t.r.root, 45*time.Second, t.max, "pr", "checks", ref)
	failed := looksFailed(checks)
	noPR := err != nil && strings.Contains(strings.ToLower(checks), "no pull requests found")
	switch {
	case noPR:
		// No PR for this ref — fall back to the branch's recent workflow runs.
		runs, rerr := runGH(ctx, t.r.root, 45*time.Second, t.max, "run", "list", "--branch", branch, "--limit", "10")
		if rerr != nil && strings.TrimSpace(runs) == "" {
			return "", fmt.Errorf("no PR for %q and could not list workflow runs: %w", ref, rerr)
		}
		fmt.Fprintf(&b, "No pull request for %q; recent workflow runs on the branch:\n%s\n", ref, runs)
		failed = looksFailed(runs) // the earlier `checks` var here is just the "no PR" error text, not run state
	case strings.TrimSpace(checks) == "" && err != nil:
		return "", err
	default:
		fmt.Fprintf(&b, "CI checks for %s:\n%s\n", ref, checks)
	}

	if withLogs && failed {
		if logs := t.failedRunLogs(ctx, branch); logs != "" {
			fmt.Fprintf(&b, "\n─── logs from the latest failed run ───\n%s\n", logs)
		}
	}
	return b.String(), nil
}

// looksFailed reports whether a `gh pr checks` table shows any failing/errored
// checks (its output labels them "fail"/"failing"/"error"/"X").
func looksFailed(checks string) bool {
	l := strings.ToLower(checks)
	return strings.Contains(l, "fail") || strings.Contains(l, "error")
}

// failedRunLogs fetches the failing-step logs of the most recent failed Actions
// run on branch (best-effort; "" if none or unavailable).
func (t *ciStatusTool) failedRunLogs(ctx context.Context, branch string) string {
	id, err := runGH(ctx, t.r.root, 30*time.Second, 256,
		"run", "list", "--branch", branch, "--status", "failure", "--limit", "1", "--json", "databaseId", "--jq", ".[0].databaseId")
	id = strings.TrimSpace(id)
	if err != nil || id == "" {
		return ""
	}
	logs, err := runGH(ctx, t.r.root, 60*time.Second, t.max, "run", "view", id, "--log-failed")
	if err != nil && strings.TrimSpace(logs) == "" {
		return ""
	}
	return logs
}
