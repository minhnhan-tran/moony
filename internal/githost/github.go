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

type githubClient struct {
	repo  *Repo
	token string
	http  *http.Client
}

func newGitHubClient(repo *Repo, token string) *githubClient {
	return &githubClient{repo: repo, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *githubClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
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
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *githubClient) decode(resp *http.Response, out any) error {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	// Mergeable is nil while GitHub is still computing it, and boolean once known.
	Mergeable *bool  `json:"mergeable"`
	MergedAt  string `json:"merged_at"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g ghPR) normalize() PR {
	state := g.State
	if g.MergedAt != "" {
		state = "merged"
	}
	return PR{
		Number: g.Number, Title: g.Title, Body: g.Body, State: state, Draft: g.Draft,
		HeadRef: g.Head.Ref, BaseRef: g.Base.Ref, URL: g.HTMLURL,
		Mergeable: g.Mergeable, Author: g.User.Login,
	}
}

func (c *githubClient) CreatePR(ctx context.Context, opts CreatePROpts) (*PR, error) {
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", c.repo.ProjectPath), map[string]any{
		"title": opts.Title, "head": opts.Head, "base": opts.Base, "body": opts.Body, "draft": opts.Draft,
	})
	if err != nil {
		return nil, err
	}
	var raw ghPR
	if err := c.decode(resp, &raw); err != nil {
		return nil, err
	}
	pr := raw.normalize()
	return &pr, nil
}

func (c *githubClient) ListPRs(ctx context.Context, opts ListPROpts) ([]PR, error) {
	state := opts.State
	if state == "" {
		state = "open"
	}
	apiState := state
	if apiState == "merged" {
		apiState = "closed" // GitHub has no "merged" filter; filter client-side below.
	}
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?state=%s&per_page=30", c.repo.ProjectPath, url.QueryEscape(apiState)), nil)
	if err != nil {
		return nil, err
	}
	var raws []ghPR
	if err := c.decode(resp, &raws); err != nil {
		return nil, err
	}
	out := make([]PR, len(raws))
	for i, r := range raws {
		out[i] = r.normalize()
	}
	if state == "merged" {
		filtered := out[:0]
		for _, p := range out {
			if p.State == "merged" {
				filtered = append(filtered, p)
			}
		}
		out = filtered
	}
	return out, nil
}

func (c *githubClient) GetPR(ctx context.Context, number int) (*PR, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", c.repo.ProjectPath, number), nil)
	if err != nil {
		return nil, err
	}
	var raw ghPR
	if err := c.decode(resp, &raw); err != nil {
		return nil, err
	}
	pr := raw.normalize()
	return &pr, nil
}

type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
	InReplyTo *int64 `json:"in_reply_to_id"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// ListReviewComments returns inline diff-review comments plus general PR
// conversation comments (GitHub PRs are issues under the hood).
func (c *githubClient) ListReviewComments(ctx context.Context, number int) ([]Comment, error) {
	var out []Comment

	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", c.repo.ProjectPath, number), nil)
	if err != nil {
		return nil, err
	}
	var reviewRaw []ghComment
	if err := c.decode(resp, &reviewRaw); err != nil {
		return nil, err
	}
	for _, r := range reviewRaw {
		threadID := strconv.FormatInt(r.ID, 10)
		if r.InReplyTo != nil {
			threadID = strconv.FormatInt(*r.InReplyTo, 10)
		}
		out = append(out, Comment{
			ID: strconv.FormatInt(r.ID, 10), ThreadID: threadID, Author: r.User.Login, Body: r.Body,
			Path: r.Path, Line: r.Line, CreatedAt: r.CreatedAt, URL: r.HTMLURL,
		})
	}

	resp2, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", c.repo.ProjectPath, number), nil)
	if err != nil {
		return nil, err
	}
	var issueRaw []ghComment
	if err := c.decode(resp2, &issueRaw); err != nil {
		return nil, err
	}
	for _, r := range issueRaw {
		out = append(out, Comment{
			ID: strconv.FormatInt(r.ID, 10), Author: r.User.Login, Body: r.Body, CreatedAt: r.CreatedAt, URL: r.HTMLURL,
		})
	}
	return out, nil
}

// ReplyToComment replies within an inline review comment thread (threadID is
// the thread's root review-comment id).
func (c *githubClient) ReplyToComment(ctx context.Context, number int, threadID, body string) error {
	resp, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/pulls/%d/comments/%s/replies", c.repo.ProjectPath, number, threadID),
		map[string]any{"body": body})
	if err != nil {
		return err
	}
	return c.decode(resp, nil)
}

// PostComment posts a general (non-review) PR conversation comment.
func (c *githubClient) PostComment(ctx context.Context, number int, body string) error {
	resp, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/issues/%d/comments", c.repo.ProjectPath, number),
		map[string]any{"body": body})
	if err != nil {
		return err
	}
	return c.decode(resp, nil)
}

type ghCheckRunsResp struct {
	CheckRuns []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	} `json:"check_runs"`
}

func (c *githubClient) CheckStatus(ctx context.Context, ref string) ([]CheckRun, error) {
	resp, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", c.repo.ProjectPath, url.PathEscape(ref)), nil)
	if err != nil {
		return nil, err
	}
	var raw ghCheckRunsResp
	if err := c.decode(resp, &raw); err != nil {
		return nil, err
	}
	out := make([]CheckRun, len(raw.CheckRuns))
	for i, r := range raw.CheckRuns {
		out[i] = CheckRun{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL}
	}
	return out, nil
}
