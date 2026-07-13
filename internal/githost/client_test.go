package githost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestGitHubRepo(baseURL string) *Repo {
	return &Repo{Kind: GitHub, RemoteHost: "github.com", APIBase: baseURL, Owner: "acme", Name: "widget", ProjectPath: "acme/widget"}
}

func newTestGitLabRepo(baseURL string) *Repo {
	return &Repo{Kind: GitLab, RemoteHost: "gitlab.com", APIBase: baseURL, Owner: "acme", Name: "widget", ProjectPath: "acme/platform/widget"}
}

func TestGitHubClientRoundTrips(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &gotBody)
			}
		}
		switch {
		case r.URL.Path == "/repos/acme/widget/pulls" && r.Method == http.MethodPost:
			io.WriteString(w, `{"number":7,"title":"add widget","state":"open","draft":false,"html_url":"https://github.com/acme/widget/pull/7","head":{"ref":"feat"},"base":{"ref":"main"},"user":{"login":"alice"}}`)
		case r.URL.Path == "/repos/acme/widget/pulls" && r.Method == http.MethodGet:
			io.WriteString(w, `[{"number":7,"title":"add widget","state":"open","head":{"ref":"feat"},"base":{"ref":"main"}}]`)
		case r.URL.Path == "/repos/acme/widget/pulls/7":
			io.WriteString(w, `{"number":7,"title":"add widget","state":"open","mergeable":true,"head":{"ref":"feat"},"base":{"ref":"main"}}`)
		case r.URL.Path == "/repos/acme/widget/pulls/7/comments":
			io.WriteString(w, `[{"id":1,"body":"nit: typo","path":"a.go","line":3,"user":{"login":"bob"}}]`)
		case r.URL.Path == "/repos/acme/widget/issues/7/comments" && r.Method == http.MethodGet:
			io.WriteString(w, `[]`)
		case r.URL.Path == "/repos/acme/widget/issues/7/comments" && r.Method == http.MethodPost:
			io.WriteString(w, `{}`)
		case r.URL.Path == "/repos/acme/widget/pulls/7/comments/1/replies":
			io.WriteString(w, `{}`)
		case r.URL.Path == "/repos/acme/widget/commits/feat/check-runs":
			io.WriteString(w, `{"check_runs":[{"name":"build","status":"completed","conclusion":"success","html_url":"https://ci/1"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"not found: `+r.URL.Path+`"}`)
		}
	}))
	defer srv.Close()

	repo := newTestGitHubRepo(srv.URL)
	client := newGitHubClient(repo, "gh-tok")
	ctx := context.Background()

	pr, err := client.CreatePR(ctx, CreatePROpts{Title: "add widget", Head: "feat", Base: "main"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 7 || pr.HeadRef != "feat" || pr.Author != "alice" {
		t.Errorf("CreatePR result = %+v", pr)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer gh-tok" {
		t.Errorf("CreatePR request method=%s auth=%s", gotMethod, gotAuth)
	}
	if gotBody["title"] != "add widget" {
		t.Errorf("CreatePR body = %v", gotBody)
	}

	prs, err := client.ListPRs(ctx, ListPROpts{})
	if err != nil || len(prs) != 1 || prs[0].Number != 7 {
		t.Fatalf("ListPRs = %+v, %v", prs, err)
	}

	got, err := client.GetPR(ctx, 7)
	if err != nil || got.Mergeable == nil || !*got.Mergeable {
		t.Fatalf("GetPR = %+v, %v", got, err)
	}

	comments, err := client.ListReviewComments(ctx, 7)
	if err != nil || len(comments) != 1 || comments[0].Path != "a.go" || comments[0].Author != "bob" {
		t.Fatalf("ListReviewComments = %+v, %v", comments, err)
	}

	if err := client.ReplyToComment(ctx, 7, "1", "fixed"); err != nil {
		t.Fatalf("ReplyToComment: %v", err)
	}
	if gotPath != "/repos/acme/widget/pulls/7/comments/1/replies" {
		t.Errorf("ReplyToComment path = %s", gotPath)
	}

	if err := client.PostComment(ctx, 7, "lgtm"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}

	checks, err := client.CheckStatus(ctx, "feat")
	if err != nil || len(checks) != 1 || checks[0].Conclusion != "success" {
		t.Fatalf("CheckStatus = %+v, %v", checks, err)
	}
}

func TestGitLabClientRoundTrips(t *testing.T) {
	const projectPath = "acme/platform/widget"
	var gotPrivateToken, gotBearer, gotEscapedPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrivateToken, gotBearer = r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("Authorization")
		if strings.Contains(r.URL.EscapedPath(), "%2F") {
			gotEscapedPath = r.URL.EscapedPath()
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &gotBody)
			}
		}
		path := r.URL.Path
		switch {
		case path == "/projects/"+projectPath+"/merge_requests" && r.Method == http.MethodPost:
			io.WriteString(w, `{"iid":3,"title":"Draft: add widget","state":"opened","draft":true,"web_url":"https://gitlab.com/acme/platform/widget/-/merge_requests/3","source_branch":"feat","target_branch":"main","author":{"username":"alice"}}`)
		case path == "/projects/"+projectPath+"/merge_requests" && r.Method == http.MethodGet:
			io.WriteString(w, `[{"iid":3,"title":"add widget","state":"opened","source_branch":"feat","target_branch":"main"}]`)
		case path == "/projects/"+projectPath+"/merge_requests/3":
			io.WriteString(w, `{"iid":3,"title":"add widget","state":"opened","detailed_merge_status":"mergeable","source_branch":"feat","target_branch":"main"}`)
		case path == "/projects/"+projectPath+"/merge_requests/3/discussions":
			io.WriteString(w, `[{"id":"d1","notes":[{"id":10,"body":"nit","author":{"username":"bob"},"position":{"new_path":"a.go","new_line":3}},{"id":11,"body":"approved this merge request","system":true}]}]`)
		case path == "/projects/"+projectPath+"/merge_requests/3/discussions/d1/notes":
			io.WriteString(w, `{}`)
		case path == "/projects/"+projectPath+"/merge_requests/3/notes":
			io.WriteString(w, `{}`)
		case path == "/projects/"+projectPath+"/repository/commits/feat/statuses":
			io.WriteString(w, `[{"name":"build","status":"success","target_url":"https://ci/1"},{"name":"lint","status":"pending","target_url":"https://ci/2"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"not found: `+path+`"}`)
		}
	}))
	defer srv.Close()

	repo := newTestGitLabRepo(srv.URL)
	client := newGitLabClient(repo, "gl-tok")
	ctx := context.Background()

	pr, err := client.CreatePR(ctx, CreatePROpts{Title: "add widget", Head: "feat", Base: "main", Draft: true})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 3 || !pr.Draft || pr.Author != "alice" {
		t.Errorf("CreatePR result = %+v", pr)
	}
	if gotPrivateToken != "gl-tok" || gotBearer != "Bearer gl-tok" {
		t.Errorf("auth headers = PRIVATE-TOKEN=%q Authorization=%q", gotPrivateToken, gotBearer)
	}
	if gotBody["title"] != "Draft: add widget" {
		t.Errorf("CreatePR body title = %v (draft prefix expected)", gotBody["title"])
	}

	prs, err := client.ListPRs(ctx, ListPROpts{})
	if err != nil || len(prs) != 1 || prs[0].State != "open" {
		t.Fatalf("ListPRs = %+v, %v", prs, err)
	}

	got, err := client.GetPR(ctx, 3)
	if err != nil || got.Mergeable == nil || !*got.Mergeable {
		t.Fatalf("GetPR = %+v, %v", got, err)
	}

	comments, err := client.ListReviewComments(ctx, 3)
	if err != nil || len(comments) != 1 || comments[0].ThreadID != "d1" || comments[0].Path != "a.go" {
		t.Fatalf("ListReviewComments = %+v, %v (system note should be filtered)", comments, err)
	}

	if err := client.ReplyToComment(ctx, 3, "d1", "fixed"); err != nil {
		t.Fatalf("ReplyToComment: %v", err)
	}
	if err := client.PostComment(ctx, 3, "lgtm"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}

	checks, err := client.CheckStatus(ctx, "feat")
	if err != nil || len(checks) != 2 {
		t.Fatalf("CheckStatus = %+v, %v", checks, err)
	}
	if checks[0].Status != "completed" || checks[0].Conclusion != "success" {
		t.Errorf("checks[0] = %+v", checks[0])
	}
	if checks[1].Status != "queued" {
		t.Errorf("checks[1] = %+v", checks[1])
	}

	if gotEscapedPath == "" {
		t.Error("expected at least one request to URL-encode the nested project path (%2F)")
	}
}
