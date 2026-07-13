// Package githost abstracts GitHub and GitLab's REST APIs behind one
// interface, normalizing "pull request" vs "merge request" terminology (and
// the different auth/URL shapes) so the pr_* tools don't need to know which
// host a repo lives on.
package githost

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// HostKind identifies which REST API a Repo talks to.
type HostKind string

const (
	GitHub HostKind = "github"
	GitLab HostKind = "gitlab"
)

// Label is the human-readable host name, for error messages.
func (k HostKind) Label() string {
	switch k {
	case GitHub:
		return "GitHub"
	case GitLab:
		return "GitLab"
	default:
		return string(k)
	}
}

// Repo identifies a git host repository resolved from `git remote get-url
// origin`, plus everything a Client needs to call its REST API.
type Repo struct {
	Kind HostKind
	// RemoteHost is the git remote's hostname (e.g. "github.com",
	// "gitlab.mycompany.com") — used for netrc credential lookup.
	RemoteHost string
	// APIBase is the resolved REST API base URL, e.g. "https://api.github.com"
	// or "https://gitlab.mycompany.com/api/v4".
	APIBase string
	// Owner/Name are for display only. GitHub: the two path segments.
	// GitLab: the first and last segments of a (possibly nested-group) path.
	Owner string
	Name  string
	// ProjectPath is what's actually used in API URLs: "owner/name" for
	// GitHub, or the full (possibly multi-segment) path for GitLab.
	ProjectPath string
}

// CreatePROpts are the inputs to Client.CreatePR.
type CreatePROpts struct {
	Title string
	Body  string
	Head  string // source branch
	Base  string // target branch
	Draft bool
}

// ListPROpts filter Client.ListPRs.
type ListPROpts struct {
	State string // "open" (default), "closed", "merged", "all"
}

// PR is a normalized pull/merge request, regardless of host.
type PR struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed", "merged"
	Draft     bool
	HeadRef   string
	BaseRef   string
	URL       string
	Mergeable *bool // nil = unknown/not yet computed
	Author    string
}

// Comment is a normalized PR/MR comment — either a general conversation
// comment (Path == "") or an inline review comment tied to a diff position.
type Comment struct {
	ID        string
	ThreadID  string // groups replies into the same discussion; "" if not threaded
	Author    string
	Body      string
	Path      string
	Line      int
	CreatedAt string
	Resolved  bool
	URL       string
}

// CheckRun is a normalized CI status entry (a GitHub check-run or a GitLab
// commit/pipeline status).
type CheckRun struct {
	Name       string
	Status     string // "queued"/"in_progress"/"completed" (GitHub-style)
	Conclusion string // "success"/"failure"/"cancelled"/... ("" if not completed)
	URL        string
}

// Client is the normalized operation set the pr_* tools drive.
type Client interface {
	CreatePR(ctx context.Context, opts CreatePROpts) (*PR, error)
	ListPRs(ctx context.Context, opts ListPROpts) ([]PR, error)
	GetPR(ctx context.Context, number int) (*PR, error)
	ListReviewComments(ctx context.Context, number int) ([]Comment, error)
	ReplyToComment(ctx context.Context, number int, threadID, body string) error
	PostComment(ctx context.Context, number int, body string) error
	CheckStatus(ctx context.Context, ref string) ([]CheckRun, error)
}

// New builds the right Client for repo.Kind.
func New(repo *Repo, token string) (Client, error) {
	switch repo.Kind {
	case GitHub:
		return newGitHubClient(repo, token), nil
	case GitLab:
		return newGitLabClient(repo, token), nil
	default:
		return nil, fmt.Errorf("unsupported git host %q", repo.Kind)
	}
}

var (
	sshRemoteRe  = regexp.MustCompile(`^(?:ssh://)?git@([^:/]+)[:/](.+)$`)
	httpRemoteRe = regexp.MustCompile(`^https?://(?:[^@/]+@)?([^/]+)/(.+)$`)
)

// Detect parses a `git remote get-url origin` value into a Repo. It
// recognizes github.com and gitlab.com by hostname, and any other host whose
// hostname matches the MOONY_GITHUB_BASE_URL or MOONY_GITLAB_BASE_URL
// override (for GitHub Enterprise / self-hosted GitLab).
func Detect(remoteURL string) (*Repo, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return nil, fmt.Errorf("no git remote URL given")
	}

	var host, path string
	if m := sshRemoteRe.FindStringSubmatch(remoteURL); m != nil {
		host, path = m[1], m[2]
	} else if m := httpRemoteRe.FindStringSubmatch(remoteURL); m != nil {
		host, path = m[1], m[2]
	} else {
		return nil, fmt.Errorf("remote URL %q is not a recognized git host URL", remoteURL)
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return nil, fmt.Errorf("remote URL %q has no repository path", remoteURL)
	}

	kind, apiBase, err := classifyHost(host)
	if err != nil {
		return nil, err
	}

	segs := strings.Split(path, "/")
	owner, name := segs[0], segs[len(segs)-1]
	projectPath := path
	if kind == GitHub && len(segs) != 2 {
		return nil, fmt.Errorf("GitHub remote path %q should be owner/repo", path)
	}
	if kind == GitHub {
		projectPath = owner + "/" + name
	}

	return &Repo{
		Kind:        kind,
		RemoteHost:  host,
		APIBase:     apiBase,
		Owner:       owner,
		Name:        name,
		ProjectPath: projectPath,
	}, nil
}

// classifyHost decides which host kind a remote hostname is, and its REST API
// base URL. github.com/gitlab.com are recognized outright; anything else must
// match a MOONY_GITHUB_BASE_URL/MOONY_GITLAB_BASE_URL override's hostname
// (Enterprise/self-hosted convention: API lives at {host}/api/v3 or
// {host}/api/v4 unless the override gives a full different path).
func classifyHost(host string) (HostKind, string, error) {
	host = strings.ToLower(host)
	switch host {
	case "github.com", "www.github.com":
		return GitHub, "https://api.github.com", nil
	case "gitlab.com", "www.gitlab.com":
		return GitLab, "https://gitlab.com/api/v4", nil
	}

	if base := strings.TrimSpace(envGithubBaseURL()); base != "" {
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), host) {
			return GitHub, strings.TrimSuffix(base, "/"), nil
		}
	}
	if base := strings.TrimSpace(envGitlabBaseURL()); base != "" {
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), host) {
			return GitLab, strings.TrimSuffix(base, "/"), nil
		}
	}

	return "", "", fmt.Errorf(
		"%q isn't github.com or gitlab.com, and doesn't match MOONY_GITHUB_BASE_URL or MOONY_GITLAB_BASE_URL — "+
			"set one of those to the self-hosted API base URL (e.g. https://%s/api/v3 for GitHub Enterprise, "+
			"https://%s/api/v4 for self-hosted GitLab)", host, host, host)
}
