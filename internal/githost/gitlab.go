package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type gitlabClient struct {
	repo  *Repo
	token string
	http  *http.Client
}

func newGitLabClient(repo *Repo, token string) *gitlabClient {
	return &gitlabClient{repo: repo, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *gitlabClient) projectID() string { return url.QueryEscape(c.repo.ProjectPath) }

func (c *gitlabClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.repo.APIBase+path, reader)
	if err != nil {
		return nil, err
	}
	// Set both auth header conventions: PRIVATE-TOKEN covers personal/project
	// access tokens (the common case for MOONY_GITLAB_TOKEN/GITLAB_TOKEN/netrc),
	// Bearer covers OAuth2 tokens (e.g. from `glab auth token`). Harmless to
	// send both; GitLab uses whichever it recognizes.
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *gitlabClient) decode(resp *http.Response, out any) error {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("gitlab %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type glMR struct {
	IID                 int    `json:"iid"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	State               string `json:"state"` // "opened", "closed", "merged", "locked"
	Draft               bool   `json:"draft"`
	WebURL              string `json:"web_url"`
	SourceBranch        string `json:"source_branch"`
	TargetBranch        string `json:"target_branch"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
	Author              struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (g glMR) normalize() PR {
	state := g.State
	if state == "opened" {
		state = "open"
	}
	var mergeable *bool
	if g.DetailedMergeStatus != "" {
		v := g.DetailedMergeStatus == "mergeable"
		mergeable = &v
	}
	return PR{
		Number: g.IID, Title: g.Title, Body: g.Description, State: state, Draft: g.Draft,
		HeadRef: g.SourceBranch, BaseRef: g.TargetBranch, URL: g.WebURL,
		Mergeable: mergeable, Author: g.Author.Username,
	}
}

func (c *gitlabClient) CreatePR(ctx context.Context, opts CreatePROpts) (*PR, error) {
	title := opts.Title
	if opts.Draft && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "draft:") {
		title = "Draft: " + title
	}
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/merge_requests", c.projectID()), map[string]any{
		"source_branch": opts.Head, "target_branch": opts.Base, "title": title, "description": opts.Body,
	})
	if err != nil {
		return nil, err
	}
	var raw glMR
	if err := c.decode(resp, &raw); err != nil {
		return nil, err
	}
	pr := raw.normalize()
	return &pr, nil
}

func (c *gitlabClient) ListPRs(ctx context.Context, opts ListPROpts) ([]PR, error) {
	state := opts.State
	if state == "" {
		state = "open"
	}
	apiState := state
	if apiState == "open" {
		apiState = "opened"
	}
	resp, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/projects/%s/merge_requests?state=%s&per_page=30", c.projectID(), url.QueryEscape(apiState)), nil)
	if err != nil {
		return nil, err
	}
	var raws []glMR
	if err := c.decode(resp, &raws); err != nil {
		return nil, err
	}
	out := make([]PR, len(raws))
	for i, r := range raws {
		out[i] = r.normalize()
	}
	return out, nil
}

func (c *gitlabClient) GetPR(ctx context.Context, number int) (*PR, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests/%d", c.projectID(), number), nil)
	if err != nil {
		return nil, err
	}
	var raw glMR
	if err := c.decode(resp, &raw); err != nil {
		return nil, err
	}
	pr := raw.normalize()
	return &pr, nil
}

type glNote struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	System   bool   `json:"system"`
	Resolved bool   `json:"resolved"`
	Author   struct {
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt string `json:"created_at"`
	Position  *struct {
		NewPath string `json:"new_path"`
		NewLine int    `json:"new_line"`
	} `json:"position"`
}

type glDiscussion struct {
	ID    string   `json:"id"`
	Notes []glNote `json:"notes"`
}

func (c *gitlabClient) ListReviewComments(ctx context.Context, number int) ([]Comment, error) {
	resp, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/projects/%s/merge_requests/%d/discussions?per_page=100", c.projectID(), number), nil)
	if err != nil {
		return nil, err
	}
	var discussions []glDiscussion
	if err := c.decode(resp, &discussions); err != nil {
		return nil, err
	}
	var out []Comment
	for _, d := range discussions {
		for _, n := range d.Notes {
			if n.System {
				continue // skip GitLab's auto-generated activity notes (e.g. "approved this merge request")
			}
			path, line := "", 0
			if n.Position != nil {
				path, line = n.Position.NewPath, n.Position.NewLine
			}
			out = append(out, Comment{
				ID: strconv.FormatInt(n.ID, 10), ThreadID: d.ID, Author: n.Author.Username, Body: n.Body,
				Path: path, Line: line, CreatedAt: n.CreatedAt, Resolved: n.Resolved,
			})
		}
	}
	return out, nil
}

func (c *gitlabClient) ReplyToComment(ctx context.Context, number int, threadID, body string) error {
	resp, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/projects/%s/merge_requests/%d/discussions/%s/notes", c.projectID(), number, threadID),
		map[string]any{"body": body})
	if err != nil {
		return err
	}
	return c.decode(resp, nil)
}

func (c *gitlabClient) PostComment(ctx context.Context, number int, body string) error {
	resp, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", c.projectID(), number),
		map[string]any{"body": body})
	if err != nil {
		return err
	}
	return c.decode(resp, nil)
}

type glStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "pending","running","success","failed","canceled","skipped"
	TargetURL string `json:"target_url"`
}

func (c *gitlabClient) CheckStatus(ctx context.Context, ref string) ([]CheckRun, error) {
	resp, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/projects/%s/repository/commits/%s/statuses?per_page=100", c.projectID(), url.PathEscape(ref)), nil)
	if err != nil {
		return nil, err
	}
	var raws []glStatus
	if err := c.decode(resp, &raws); err != nil {
		return nil, err
	}
	out := make([]CheckRun, len(raws))
	for i, r := range raws {
		status, conclusion := "in_progress", ""
		switch r.Status {
		case "success", "failed", "canceled", "skipped":
			status, conclusion = "completed", r.Status
		case "pending":
			status = "queued"
		}
		out[i] = CheckRun{Name: r.Name, Status: status, Conclusion: conclusion, URL: r.TargetURL}
	}
	return out, nil
}
