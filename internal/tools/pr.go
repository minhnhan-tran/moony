package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/githost"
)

// resolveGitHostClient resolves the working dir's `origin` remote into a
// githost.Repo + Client, erroring clearly at whichever step fails: no
// supported remote, or no token configured for that host.
func resolveGitHostClient(ctx context.Context, dir string) (*githost.Repo, githost.Client, error) {
	remoteURL, err := runGit(ctx, dir, 4096, "remote", "get-url", "origin")
	if err != nil {
		return nil, nil, fmt.Errorf("no git remote 'origin' configured: %w", err)
	}
	repo, err := githost.Detect(strings.TrimSpace(remoteURL))
	if err != nil {
		return nil, nil, err
	}
	token, err := githost.Token(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	client, err := githost.New(repo, token)
	if err != nil {
		return nil, nil, err
	}
	return repo, client, nil
}

// detectDefaultBase guesses the repo's default branch from the local
// origin/HEAD symref (set by a normal clone or `git remote set-head origin
// -a`); falls back to "main" when that's not available.
func detectDefaultBase(ctx context.Context, dir string) string {
	if out, err := runGit(ctx, dir, 256, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return strings.TrimPrefix(ref, "origin/")
		}
	}
	return "main"
}

// ---- pr_create ----------------------------------------------------------

// prCreateTool resolves its githost.Client lazily via resolveGitHostClient,
// unless client is set (tests inject a fake here to avoid real network/git
// remote dependencies).
type prCreateTool struct {
	r      *Registry
	client githost.Client
}

func (t *prCreateTool) resolve(ctx context.Context) (githost.Client, error) {
	if t.client != nil {
		return t.client, nil
	}
	_, c, err := resolveGitHostClient(ctx, t.r.root)
	return c, err
}

func (t *prCreateTool) Name() string { return "pr_create" }
func (t *prCreateTool) Description() string {
	return "Open a pull request (GitHub) or merge request (GitLab) from the current branch. head defaults to " +
		"the current branch; base defaults to the repo's default branch. Resolves the host from `git remote " +
		"get-url origin`; errors clearly if there's no supported remote or no token configured " +
		"(MOONY_GITHUB_TOKEN/MOONY_GITLAB_TOKEN, gh/glab auth, or ~/.netrc)."
}
func (t *prCreateTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"title": {"type": "string", "description": "PR/MR title."},
		"body": {"type": "string", "description": "PR/MR description."},
		"head": {"type": "string", "description": "Source branch. Defaults to the current branch."},
		"base": {"type": "string", "description": "Target branch. Defaults to the repo's default branch."},
		"draft": {"type": "boolean", "description": "Open as a draft PR/MR."}
	}, "required": ["title"]}`)
}
func (t *prCreateTool) Mutates() bool { return true }

func (t *prCreateTool) parseInput(ctx context.Context, input json.RawMessage) (githost.CreatePROpts, error) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}
	if err := decode(input, &in); err != nil {
		return githost.CreatePROpts{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return githost.CreatePROpts{}, fmt.Errorf("title is required")
	}
	head := strings.TrimSpace(in.Head)
	if head == "" {
		branch, _, isRepo := GitInfo(t.r.root)
		if !isRepo || branch == "" {
			return githost.CreatePROpts{}, fmt.Errorf("head is required (couldn't detect the current branch)")
		}
		head = branch
	}
	base := strings.TrimSpace(in.Base)
	if base == "" {
		base = detectDefaultBase(ctx, t.r.root)
	}
	return githost.CreatePROpts{Title: in.Title, Body: in.Body, Head: head, Base: base, Draft: in.Draft}, nil
}

func (t *prCreateTool) Preview(ctx context.Context, input json.RawMessage) (Preview, error) {
	opts, err := t.parseInput(ctx, input)
	if err != nil {
		return Preview{}, err
	}
	summary := fmt.Sprintf("Open PR: %s -> %s: %q", opts.Head, opts.Base, opts.Title)
	if opts.Draft {
		summary = "Open draft " + summary[len("Open "):]
	}
	return Preview{Summary: summary, Diff: opts.Body}, nil
}

func (t *prCreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	opts, err := t.parseInput(ctx, input)
	if err != nil {
		return "", err
	}
	client, err := t.resolve(ctx)
	if err != nil {
		return "", err
	}
	pr, err := client.CreatePR(ctx, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Opened #%d: %s\n%s -> %s%s\n%s", pr.Number, pr.Title, pr.HeadRef, pr.BaseRef, draftSuffix(pr.Draft), pr.URL), nil
}

func draftSuffix(draft bool) string {
	if draft {
		return " (draft)"
	}
	return ""
}

// ---- pr_list --------------------------------------------------------------

type prListTool struct {
	r      *Registry
	client githost.Client
}

func (t *prListTool) resolve(ctx context.Context) (githost.Client, error) {
	if t.client != nil {
		return t.client, nil
	}
	_, c, err := resolveGitHostClient(ctx, t.r.root)
	return c, err
}

func (t *prListTool) Name() string { return "pr_list" }
func (t *prListTool) Description() string {
	return "List open pull/merge requests for this repo, or view one in detail (description, branches, " +
		"mergeable state) when a number is given. Read-only."
}
func (t *prListTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"number": {"type": "integer", "description": "View this PR/MR number in detail instead of listing."},
		"state": {"type": "string", "enum": ["open", "closed", "merged", "all"], "description": "List filter (default open); ignored when number is given."}
	}}`)
}
func (t *prListTool) Mutates() bool { return false }

func (t *prListTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Number flexInt `json:"number"`
		State  string  `json:"state"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	client, err := t.resolve(ctx)
	if err != nil {
		return "", err
	}

	if in.Number != 0 {
		pr, err := client.GetPR(ctx, int(in.Number))
		if err != nil {
			return "", err
		}
		return formatPRDetail(pr), nil
	}

	prs, err := client.ListPRs(ctx, githost.ListPROpts{State: in.State})
	if err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "No matching pull/merge requests.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pull/merge request(s):\n", len(prs))
	for _, pr := range prs {
		draft := ""
		if pr.Draft {
			draft = " [draft]"
		}
		fmt.Fprintf(&b, "\n#%d%s [%s] %s\n%s -> %s  %s", pr.Number, draft, pr.State, pr.Title, pr.HeadRef, pr.BaseRef, pr.URL)
	}
	return b.String(), nil
}

func formatPRDetail(pr *githost.PR) string {
	draft := ""
	if pr.Draft {
		draft = " [draft]"
	}
	mergeable := "unknown"
	if pr.Mergeable != nil {
		if *pr.Mergeable {
			mergeable = "yes"
		} else {
			mergeable = "no (conflicts)"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d%s [%s] %s\nby %s: %s -> %s\nmergeable: %s\n%s\n", pr.Number, draft, pr.State, pr.Title, pr.Author, pr.HeadRef, pr.BaseRef, mergeable, pr.URL)
	if strings.TrimSpace(pr.Body) != "" {
		fmt.Fprintf(&b, "\n%s", pr.Body)
	}
	return b.String()
}

// ---- pr_comments -----------------------------------------------------------

type prCommentsTool struct {
	r      *Registry
	client githost.Client
}

func (t *prCommentsTool) resolve(ctx context.Context) (githost.Client, error) {
	if t.client != nil {
		return t.client, nil
	}
	_, c, err := resolveGitHostClient(ctx, t.r.root)
	return c, err
}

func (t *prCommentsTool) Name() string { return "pr_comments" }
func (t *prCommentsTool) Description() string {
	return "List review comment threads on a PR/MR, or post a reply (thread_id) / a new general comment (no " +
		"thread_id) when body is given. Read-only unless body is set."
}
func (t *prCommentsTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"number": {"type": "integer", "description": "PR/MR number."},
		"body": {"type": "string", "description": "Post this as a reply (with thread_id) or a new comment (without). Omit to just list."},
		"thread_id": {"type": "string", "description": "Reply within this thread's id (from a prior pr_comments listing). Requires body."}
	}, "required": ["number"]}`)
}
func (t *prCommentsTool) Mutates() bool { return true }

func (t *prCommentsTool) Preview(ctx context.Context, input json.RawMessage) (Preview, error) {
	var in struct {
		Number   flexInt `json:"number"`
		Body     string  `json:"body"`
		ThreadID string  `json:"thread_id"`
	}
	if err := decode(input, &in); err != nil {
		return Preview{}, err
	}
	if strings.TrimSpace(in.Body) == "" {
		return Preview{}, nil // listing — nothing to preview
	}
	summary := fmt.Sprintf("Post comment on #%d", int(in.Number))
	if strings.TrimSpace(in.ThreadID) != "" {
		summary = fmt.Sprintf("Reply in thread %s on #%d", in.ThreadID, int(in.Number))
	}
	return Preview{Summary: summary, Diff: in.Body}, nil
}

func (t *prCommentsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Number   flexInt `json:"number"`
		Body     string  `json:"body"`
		ThreadID string  `json:"thread_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Number == 0 {
		return "", fmt.Errorf("number is required")
	}
	client, err := t.resolve(ctx)
	if err != nil {
		return "", err
	}
	number := int(in.Number)

	if strings.TrimSpace(in.Body) != "" {
		if strings.TrimSpace(in.ThreadID) != "" {
			if err := client.ReplyToComment(ctx, number, strings.TrimSpace(in.ThreadID), in.Body); err != nil {
				return "", err
			}
			return fmt.Sprintf("Replied in thread %s on #%d.", in.ThreadID, number), nil
		}
		if err := client.PostComment(ctx, number, in.Body); err != nil {
			return "", err
		}
		return fmt.Sprintf("Posted comment on #%d.", number), nil
	}

	comments, err := client.ListReviewComments(ctx, number)
	if err != nil {
		return "", err
	}
	if len(comments) == 0 {
		return fmt.Sprintf("No comments on #%d.", number), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d comment(s) on #%d:\n", len(comments), number)
	for _, c := range comments {
		loc := ""
		if c.Path != "" {
			loc = fmt.Sprintf(" (%s:%d)", c.Path, c.Line)
		}
		resolved := ""
		if c.Resolved {
			resolved = " [resolved]"
		}
		thread := ""
		if c.ThreadID != "" {
			thread = fmt.Sprintf(" thread=%s", c.ThreadID)
		}
		fmt.Fprintf(&b, "\n[id=%s%s]%s%s %s: %s", c.ID, thread, loc, resolved, c.Author, c.Body)
	}
	return b.String(), nil
}

// ---- pr_checks --------------------------------------------------------------

type prChecksTool struct {
	r      *Registry
	client githost.Client
}

func (t *prChecksTool) resolve(ctx context.Context) (githost.Client, error) {
	if t.client != nil {
		return t.client, nil
	}
	_, c, err := resolveGitHostClient(ctx, t.r.root)
	return c, err
}

func (t *prChecksTool) Name() string { return "pr_checks" }
func (t *prChecksTool) Description() string {
	return "Report CI check-run/pipeline status for a PR/MR number or a ref (branch/commit SHA). Read-only."
}
func (t *prChecksTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"number": {"type": "integer", "description": "PR/MR number \u2014 checks its head ref."},
		"ref": {"type": "string", "description": "A branch or commit SHA, instead of a PR/MR number."}
	}}`)
}
func (t *prChecksTool) Mutates() bool { return false }

func (t *prChecksTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Number flexInt `json:"number"`
		Ref    string  `json:"ref"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" && in.Number == 0 {
		return "", fmt.Errorf("either number or ref is required")
	}
	client, err := t.resolve(ctx)
	if err != nil {
		return "", err
	}
	if ref == "" {
		pr, err := client.GetPR(ctx, int(in.Number))
		if err != nil {
			return "", err
		}
		ref = pr.HeadRef
	}
	checks, err := client.CheckStatus(ctx, ref)
	if err != nil {
		return "", err
	}
	if len(checks) == 0 {
		return fmt.Sprintf("No checks reported for %s.", ref), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d check(s) for %s:\n", len(checks), ref)
	for _, c := range checks {
		state := c.Status
		if c.Conclusion != "" {
			state = c.Conclusion
		}
		fmt.Fprintf(&b, "\n[%s] %s  %s", state, c.Name, c.URL)
	}
	return b.String(), nil
}
