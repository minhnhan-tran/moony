package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func patchInput(t *testing.T, patch string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplyPatchAddUpdateDelete(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "update.txt", "alpha\nbeta\ngamma\n")
	writeTemp(t, root, "gone.txt", "remove me\n")
	tool := &applyPatchTool{r: NewRegistry(root)}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: new.txt",
		"+hello",
		"+world",
		"*** Update File: update.txt",
		"@@",
		" alpha",
		"-beta",
		"+BETA",
		" gamma",
		"*** Delete File: gone.txt",
		"*** End Patch",
	}, "\n")

	out, err := tool.Execute(context.Background(), patchInput(t, patch))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "1 added") || !strings.Contains(out, "1 updated") || !strings.Contains(out, "1 deleted") {
		t.Errorf("summary = %q", out)
	}
	if got := readTemp(t, root, "new.txt"); got != "hello\nworld" {
		t.Errorf("new.txt = %q", got)
	}
	if got := readTemp(t, root, "update.txt"); got != "alpha\nBETA\ngamma\n" {
		t.Errorf("update.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("gone.txt should have been deleted")
	}
}

func TestApplyPatchAddCreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := "*** Begin Patch\n*** Add File: a/b/c.txt\n+nested\n*** End Patch"
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := readTemp(t, root, filepath.Join("a", "b", "c.txt")); got != "nested" {
		t.Errorf("content = %q", got)
	}
}

func TestApplyPatchMultipleHunks(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "one\ntwo\nthree\nfour\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: f.txt",
		"@@",
		"-one",
		"+ONE",
		"@@",
		"-four",
		"+FOUR",
		"*** End Patch",
	}, "\n")
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := readTemp(t, root, "f.txt"); got != "ONE\ntwo\nthree\nFOUR\n" {
		t.Errorf("content = %q", got)
	}
}

func TestApplyPatchAtomicOnFailure(t *testing.T) {
	root := t.TempDir()
	const orig = "keep\n"
	writeTemp(t, root, "ok.txt", orig)
	tool := &applyPatchTool{r: NewRegistry(root)}

	// First op is valid, second references a missing file → whole patch fails
	// during planning, so ok.txt must be untouched and add.txt never created.
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: add.txt",
		"+data",
		"*** Update File: missing.txt",
		"@@",
		"-x",
		"+y",
		"*** End Patch",
	}, "\n")
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err == nil {
		t.Fatal("expected failure for the missing update target")
	}
	if got := readTemp(t, root, "ok.txt"); got != orig {
		t.Errorf("ok.txt changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "add.txt")); !os.IsNotExist(err) {
		t.Errorf("add.txt should not have been created on failed patch")
	}
}

func TestApplyPatchAmbiguousContext(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "x\nx\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := "*** Begin Patch\n*** Update File: f.txt\n@@\n-x\n+y\n*** End Patch"
	_, err := tool.Execute(context.Background(), patchInput(t, patch))
	if err == nil || !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
}

func TestApplyPatchContextNotFound(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "hello\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := "*** Begin Patch\n*** Update File: f.txt\n@@\n-nope\n+x\n*** End Patch"
	_, err := tool.Execute(context.Background(), patchInput(t, patch))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestApplyPatchAddExistingRejected(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "exists\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := "*** Begin Patch\n*** Add File: f.txt\n+new\n*** End Patch"
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err == nil {
		t.Fatal("expected error adding an existing file")
	}
}

func TestApplyPatchDuplicatePathRejected(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "a\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: f.txt",
		"@@",
		"-a",
		"+b",
		"*** Delete File: f.txt",
		"*** End Patch",
	}, "\n")
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err == nil {
		t.Fatal("expected error for duplicate path in one patch")
	}
}

func TestApplyPatchEscapeRejected(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(root)
	r.tempRoots = nil // isolate sandbox confinement from the temp scratch hatch
	tool := &applyPatchTool{r: r}
	patch := "*** Begin Patch\n*** Add File: ../escape.txt\n+x\n*** End Patch"
	if _, err := tool.Execute(context.Background(), patchInput(t, patch)); err == nil {
		t.Fatal("expected sandbox-escape error")
	}
}

func TestApplyPatchMalformedEnvelope(t *testing.T) {
	root := t.TempDir()
	tool := &applyPatchTool{r: NewRegistry(root)}
	cases := []string{
		"",                               // empty
		"*** Add File: x\n+y",            // no begin/end
		"*** Begin Patch\n+orphan",       // body line outside any section
		"*** Begin Patch\n*** End Patch", // no operations
	}
	for _, p := range cases {
		if _, err := tool.Execute(context.Background(), patchInput(t, p)); err == nil {
			t.Errorf("expected error for malformed patch %q", p)
		}
	}
}

func TestApplyPatchPreviewNoWrite(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "old\n")
	tool := &applyPatchTool{r: NewRegistry(root)}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: n.txt",
		"+new",
		"*** Update File: f.txt",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
	}, "\n")
	prev, err := tool.Preview(context.Background(), patchInput(t, patch))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(prev.Summary, "2 file(s)") {
		t.Errorf("summary = %q", prev.Summary)
	}
	if !strings.Contains(prev.Diff, "add n.txt") || !strings.Contains(prev.Diff, "update f.txt") {
		t.Errorf("diff should label both files:\n%s", prev.Diff)
	}
	// Preview must not touch disk.
	if got := readTemp(t, root, "f.txt"); got != "old\n" {
		t.Errorf("preview mutated f.txt: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "n.txt")); !os.IsNotExist(err) {
		t.Errorf("preview created n.txt")
	}
}

func TestApplyPatchPaths(t *testing.T) {
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: a.txt",
		"+x",
		"*** Update File: b.txt",
		"@@",
		"-1",
		"+2",
		"*** Delete File: c.txt",
		"*** End Patch",
	}, "\n")
	got := ApplyPatchPaths(patchInput(t, patch))
	want := []string{"a.txt", "b.txt", "c.txt"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Malformed input yields nil.
	if ApplyPatchPaths(patchInput(t, "garbage")) != nil {
		t.Error("expected nil paths for unparseable patch")
	}
}
