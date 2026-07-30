package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewChangesTool(t *testing.T) {
	r := NewRegistry(".")
	tool := &reviewChangesTool{r: r, max: 16000}

	if tool.Mutates() {
		t.Error("review_changes must be read-only (Mutates=false)")
	}

	// No provider wired => graceful "not available".
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not available") {
		t.Errorf("no provider: got %q, want 'not available'", out)
	}

	// Empty diff => "no files changed".
	r.SetChangeReviewer(func(int) string { return "" })
	out, _ = tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !strings.Contains(out, "No files") {
		t.Errorf("empty provider: got %q, want 'No files'", out)
	}

	// Provider returns a diff => passed through (trimmed).
	r.SetChangeReviewer(func(max int) string {
		if max != 16000 {
			t.Errorf("provider got max=%d, want 16000", max)
		}
		return "\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n\n"
	})
	out, _ = tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !strings.Contains(out, "x.go") || !strings.Contains(out, "+new") {
		t.Errorf("diff passthrough: got %q", out)
	}
	if out != strings.TrimSpace(out) {
		t.Errorf("output should be trimmed, got %q", out)
	}
}

// TestReviewChangesToolUnboundedMax covers the package convention (see bash.go's
// truncate): max<=0 means "no cap", not "fall back to some default size".
func TestReviewChangesToolUnboundedMax(t *testing.T) {
	r := NewRegistry(".")
	tool := &reviewChangesTool{r: r, max: 0}
	var gotMax int
	r.SetChangeReviewer(func(max int) string {
		gotMax = max
		return "diff"
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if gotMax < 1<<20 {
		t.Errorf("max=0 (unbounded) resolved to a small cap %d, want effectively unbounded", gotMax)
	}
}
