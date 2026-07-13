package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/githost"
)

// fakeGithostClient is an in-memory githost.Client for testing the pr_* tools
// without a real git remote or network access.
type fakeGithostClient struct {
	createErr error
	created   *githost.CreatePROpts

	prs    []githost.PR
	getErr error

	comments    []githost.Comment
	replied     []string // "threadID:body"
	posted      []string // body
	commentsErr error

	checks       []githost.CheckRun
	checksErr    error
	lastCheckRef string
}

func (f *fakeGithostClient) CreatePR(ctx context.Context, opts githost.CreatePROpts) (*githost.PR, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = &opts
	return &githost.PR{Number: 42, Title: opts.Title, HeadRef: opts.Head, BaseRef: opts.Base, Draft: opts.Draft, URL: "https://example/pr/42"}, nil
}

func (f *fakeGithostClient) ListPRs(ctx context.Context, opts githost.ListPROpts) ([]githost.PR, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.prs, nil
}

func (f *fakeGithostClient) GetPR(ctx context.Context, number int) (*githost.PR, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.prs {
		if f.prs[i].Number == number {
			return &f.prs[i], nil
		}
	}
	return nil, nil
}

func (f *fakeGithostClient) ListReviewComments(ctx context.Context, number int) ([]githost.Comment, error) {
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.comments, nil
}

func (f *fakeGithostClient) ReplyToComment(ctx context.Context, number int, threadID, body string) error {
	f.replied = append(f.replied, threadID+":"+body)
	return nil
}

func (f *fakeGithostClient) PostComment(ctx context.Context, number int, body string) error {
	f.posted = append(f.posted, body)
	return nil
}

func (f *fakeGithostClient) CheckStatus(ctx context.Context, ref string) ([]githost.CheckRun, error) {
	f.lastCheckRef = ref
	if f.checksErr != nil {
		return nil, f.checksErr
	}
	return f.checks, nil
}

func TestPRCreateTool(t *testing.T) {
	fake := &fakeGithostClient{}
	tool := &prCreateTool{r: &Registry{}, client: fake}

	if _, err := tool.Execute(context.Background(), mustJSON(map[string]any{})); err == nil {
		t.Error("empty title should error")
	}

	in := mustJSON(map[string]any{"title": "add widget", "head": "feat", "base": "main", "draft": true})
	preview, err := tool.Preview(context.Background(), in)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !strings.Contains(preview.Summary, "feat") || !strings.Contains(preview.Summary, "main") {
		t.Errorf("Preview.Summary = %q", preview.Summary)
	}

	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.created == nil || fake.created.Title != "add widget" || fake.created.Head != "feat" || !fake.created.Draft {
		t.Errorf("created = %+v", fake.created)
	}
	if !strings.Contains(out, "#42") {
		t.Errorf("output = %q", out)
	}
}

func TestPRListTool(t *testing.T) {
	fake := &fakeGithostClient{prs: []githost.PR{
		{Number: 1, Title: "one", State: "open", HeadRef: "a", BaseRef: "main"},
		{Number: 2, Title: "two", State: "open", Draft: true, HeadRef: "b", BaseRef: "main"},
	}}
	tool := &prListTool{r: &Registry{}, client: fake}

	out, err := tool.Execute(context.Background(), mustJSON(map[string]any{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "#1") || !strings.Contains(out, "#2") || !strings.Contains(out, "[draft]") {
		t.Errorf("list output = %q", out)
	}

	out, err = tool.Execute(context.Background(), mustJSON(map[string]any{"number": 2}))
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !strings.Contains(out, "two") || !strings.Contains(out, "[draft]") {
		t.Errorf("view output = %q", out)
	}
}

func TestPRCommentsTool(t *testing.T) {
	fake := &fakeGithostClient{comments: []githost.Comment{
		{ID: "1", ThreadID: "1", Author: "bob", Body: "nit: typo", Path: "a.go", Line: 3},
	}}
	tool := &prCommentsTool{r: &Registry{}, client: fake}

	// List mode: no preview, returns the comment.
	preview, err := tool.Preview(context.Background(), mustJSON(map[string]any{"number": 7}))
	if err != nil || preview.Summary != "" {
		t.Errorf("list-mode Preview = %+v, %v; want empty", preview, err)
	}
	out, err := tool.Execute(context.Background(), mustJSON(map[string]any{"number": 7}))
	if err != nil || !strings.Contains(out, "bob") || !strings.Contains(out, "a.go:3") {
		t.Fatalf("list output = %q, %v", out, err)
	}

	// Reply mode.
	preview, err = tool.Preview(context.Background(), mustJSON(map[string]any{"number": 7, "thread_id": "1", "body": "fixed"}))
	if err != nil || !strings.Contains(preview.Summary, "thread 1") {
		t.Errorf("reply Preview = %+v, %v", preview, err)
	}
	if _, err := tool.Execute(context.Background(), mustJSON(map[string]any{"number": 7, "thread_id": "1", "body": "fixed"})); err != nil {
		t.Fatalf("reply Execute: %v", err)
	}
	if len(fake.replied) != 1 || fake.replied[0] != "1:fixed" {
		t.Errorf("replied = %v", fake.replied)
	}

	// Post (no thread_id) mode.
	if _, err := tool.Execute(context.Background(), mustJSON(map[string]any{"number": 7, "body": "lgtm"})); err != nil {
		t.Fatalf("post Execute: %v", err)
	}
	if len(fake.posted) != 1 || fake.posted[0] != "lgtm" {
		t.Errorf("posted = %v", fake.posted)
	}

	// number is required.
	if _, err := tool.Execute(context.Background(), mustJSON(map[string]any{"body": "x"})); err == nil {
		t.Error("missing number should error")
	}
}

func TestPRChecksTool(t *testing.T) {
	fake := &fakeGithostClient{
		prs:    []githost.PR{{Number: 9, HeadRef: "feat"}},
		checks: []githost.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
	}
	tool := &prChecksTool{r: &Registry{}, client: fake}

	out, err := tool.Execute(context.Background(), mustJSON(map[string]any{"number": 9}))
	if err != nil {
		t.Fatalf("by number: %v", err)
	}
	if fake.lastCheckRef != "feat" {
		t.Errorf("resolved ref = %q, want feat (from PR head)", fake.lastCheckRef)
	}
	if !strings.Contains(out, "success") {
		t.Errorf("output = %q", out)
	}

	out, err = tool.Execute(context.Background(), mustJSON(map[string]any{"ref": "main"}))
	if err != nil {
		t.Fatalf("by ref: %v", err)
	}
	if fake.lastCheckRef != "main" {
		t.Errorf("resolved ref = %q, want main", fake.lastCheckRef)
	}

	if _, err := tool.Execute(context.Background(), mustJSON(map[string]any{})); err == nil {
		t.Error("missing number and ref should error")
	}
}

func TestPRToolsSchemasAreValidJSON(t *testing.T) {
	tools := []Tool{&prCreateTool{r: &Registry{}}, &prListTool{r: &Registry{}}, &prCommentsTool{r: &Registry{}}, &prChecksTool{r: &Registry{}}}
	for _, tl := range tools {
		var v any
		if err := json.Unmarshal(tl.Schema(), &v); err != nil {
			t.Errorf("%s schema is not valid JSON: %v", tl.Name(), err)
		}
		if tl.Description() == "" {
			t.Errorf("%s has no description", tl.Name())
		}
	}
}
