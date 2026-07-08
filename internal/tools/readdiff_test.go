package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #1: re-reading a file that changed since it was last shown returns a unified
// diff of the window instead of the whole thing.
func TestReadFileDiffOnReread(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "f.go")
	var buf strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&buf, "line %d content\n", i)
	}
	if err := os.WriteFile(p, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &readTool{r: NewRegistry(root)}
	run := func() string {
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.go"}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := run()
	if !strings.Contains(first, "line 30 content") {
		t.Fatalf("first read should be full contents, got:\n%s", first)
	}

	// Change one line in a large file — the diff should be far smaller than a full re-read.
	changed := strings.Replace(buf.String(), "line 30 content", "line 30 CHANGED", 1)
	if err := os.WriteFile(p, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	second := run()
	if !strings.Contains(second, "changed since you last read it") {
		t.Fatalf("re-read of changed file should return a diff, got:\n%s", second)
	}
	if !strings.Contains(second, "line 30 CHANGED") {
		t.Fatalf("diff should show the new line, got:\n%s", second)
	}
	if len(second) >= len(first) {
		t.Errorf("diff (%d) should be smaller than a full re-read (%d)", len(second), len(first))
	}
}

// After an agent edit (invalidateRead marks the window dirty), a re-read whose
// window content is unchanged returns the compact "unchanged" stub.
func TestReadFileStubWhenWindowUnchanged(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "f.go")
	if err := os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(root)
	tool := &readTool{r: reg}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.go"}`)); err != nil {
		t.Fatal(err)
	}
	// Simulate an agent edit that dirties the cache but leaves this file identical.
	abs, _ := reg.resolve("f.go")
	reg.invalidateRead(abs)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(out), "already") {
		t.Fatalf("unchanged window after edit should return a stub, got:\n%s", out)
	}
}
