package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #4: git read tools reuse the session search cache and return a dedup stub on
// an identical repeat, and a mutation (or a write) invalidates it.
func TestGitDiffSessionCache(t *testing.T) {
	dir := gitRepo(t)
	reg := NewRegistry(dir)

	// A tracked file with a large modification so the diff exceeds the cache's
	// minimum size and gets stored.
	f := filepath.Join(dir, "big.txt")
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString("original line of content here\n")
	}
	if err := os.WriteFile(f, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "big.txt")
	gitExec(t, dir, "commit", "-q", "-m", "add big")
	var appended strings.Builder
	appended.WriteString(sb.String())
	for i := 0; i < 40; i++ {
		appended.WriteString("a newly appended line of content\n")
	}
	if err := os.WriteFile(f, []byte(appended.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	diff := &gitDiffTool{r: reg, max: 1 << 20}
	first, err := diff.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first, "identical to an earlier") {
		t.Fatalf("first call should be a real diff, got stub:\n%s", first)
	}
	second, err := diff.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "identical to an earlier") {
		t.Fatalf("identical repeat should return dedup stub, got:\n%s", second)
	}

	// A commit invalidates the cache -> next diff runs fresh again.
	reg.invalidateSearches()
	third, err := diff.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(third, "identical to an earlier") {
		t.Fatalf("after invalidation diff should run fresh, got stub:\n%s", third)
	}
}
