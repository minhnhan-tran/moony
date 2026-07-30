package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPullRequestParseValidation(t *testing.T) {
	tool := &pullRequestTool{r: NewRegistry(t.TempDir()), max: 1 << 20}
	cases := []struct {
		name    string
		input   string
		wantErr string // substring; "" = expect success
	}{
		{"create needs title", `{"action":"create"}`, "title is required"},
		{"create ok", `{"action":"create","title":"Fix bug"}`, ""},
		{"comment needs body", `{"action":"comment"}`, "body is required"},
		{"comment ok", `{"action":"comment","body":"LGTM"}`, ""},
		{"view ok", `{"action":"view"}`, ""},
		{"view by number ok", `{"action":"view","number":42}`, ""},
		{"unknown action", `{"action":"merge"}`, "action must be one of"},
		{"case-insensitive action", `{"action":"CREATE","title":"t"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.parse(json.RawMessage(c.input))
			if c.wantErr == "" && err != nil {
				t.Fatalf("parse(%s) unexpected error: %v", c.input, err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("parse(%s) error = %v, want substring %q", c.input, err, c.wantErr)
			}
		})
	}
}

// Preview for comment/view is pure (no gh/git call), so it's safe to assert.
func TestPullRequestPreviewSummaries(t *testing.T) {
	tool := &pullRequestTool{r: NewRegistry(t.TempDir()), max: 1 << 20}
	p, err := tool.Preview(context.Background(), json.RawMessage(`{"action":"comment","body":"ship it","number":7}`))
	if err != nil {
		t.Fatalf("Preview comment: %v", err)
	}
	if !strings.Contains(p.Summary, "PR #7") || p.Diff != "ship it" {
		t.Errorf("comment preview = %+v, want summary naming PR #7 and diff=body", p)
	}
	p, err = tool.Preview(context.Background(), json.RawMessage(`{"action":"view"}`))
	if err != nil {
		t.Fatalf("Preview view: %v", err)
	}
	if !strings.Contains(p.Summary, "read-only") {
		t.Errorf("view preview summary = %q, want it flagged read-only", p.Summary)
	}
}

func TestGitHubToolProperties(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	pr := &pullRequestTool{r: reg, max: 1 << 20}
	ci := &ciStatusTool{r: reg, max: 1 << 20}
	if pr.Name() != "pull_request" || !pr.Mutates() {
		t.Errorf("pull_request: name=%q mutates=%v, want pull_request/true", pr.Name(), pr.Mutates())
	}
	if ci.Name() != "ci_status" || ci.Mutates() {
		t.Errorf("ci_status: name=%q mutates=%v, want ci_status/false", ci.Name(), ci.Mutates())
	}
	// Schemas must be valid JSON objects (schema() would otherwise be malformed).
	for _, raw := range []json.RawMessage{pr.Schema(), ci.Schema()} {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("invalid schema JSON: %v", err)
		}
	}
	// Both tools are registered in the default set.
	def := NewRegistry(t.TempDir())
	def.RegisterDefaults(1 << 20)
	for _, name := range []string{"pull_request", "ci_status"} {
		if def.Get(name) == nil {
			t.Errorf("default registry missing %q", name)
		}
	}
}

func TestLooksFailed(t *testing.T) {
	fail := []string{"all checks failing", "build (X error)", "1 FAILING"}
	ok := []string{"all checks passing", "3 successful, 0 pending", ""}
	for _, s := range fail {
		if !looksFailed(s) {
			t.Errorf("looksFailed(%q) = false, want true", s)
		}
	}
	for _, s := range ok {
		if looksFailed(s) {
			t.Errorf("looksFailed(%q) = true, want false", s)
		}
	}
}
