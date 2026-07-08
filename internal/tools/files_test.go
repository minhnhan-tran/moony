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

func writeTemp(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestReadFileDefaultLineCap(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line\n")
	}
	writeTemp(t, root, "big.txt", sb.String())
	tool := &readTool{r: NewRegistry(root)}

	// Default is 2000 unless overridden; shrink it and confirm read_file honors it.
	old := defaultReadLines
	t.Cleanup(func() { SetDefaultReadLines(old) })
	SetDefaultReadLines(10)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(out, ": line"); got != 10 {
		t.Errorf("returned %d lines, want 10 (the configured cap)", got)
	}
	if !strings.Contains(out, "more lines; use offset=11") {
		t.Errorf("should page the rest with an offset footer, got:\n%s", out)
	}

	// An explicit limit still overrides the default.
	out, _ = tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt","limit":3}`))
	if got := strings.Count(out, ": line"); got != 3 {
		t.Errorf("explicit limit=3 returned %d lines, want 3", got)
	}
}

// TestReadFileAcceptsStringNumbers guards the fix for models that emit numeric
// args as JSON strings (the "cannot unmarshal string into .offset of type int"
// error). offset/limit must accept "3"/"2" (and a float-ish "5.0") the same as
// bare numbers.
func TestReadFileAcceptsStringNumbers(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	writeTemp(t, root, "f.txt", sb.String())
	tool := &readTool{r: NewRegistry(root)}

	// offset and limit as strings — must not error, and must page like ints.
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","offset":"3","limit":"2"}`))
	if err != nil {
		t.Fatalf("string offset/limit should be accepted, got error: %v", err)
	}
	if !strings.Contains(out, "3: line3") || !strings.Contains(out, "4: line4") {
		t.Errorf("offset=\"3\" limit=\"2\" should return lines 3-4, got:\n%s", out)
	}
	if strings.Contains(out, "5: line5") {
		t.Errorf("limit=\"2\" should stop after line 4, got:\n%s", out)
	}

	// A float-ish string is tolerated too (truncated).
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","offset":"5.0"}`)); err != nil {
		t.Errorf("float-ish string offset should be accepted, got: %v", err)
	}
}

// TestReadFileOnDirectoryListsContents verifies read_file on a directory returns
// a helpful listing + hint instead of the opaque "is a directory" error.
func TestReadFileOnDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, root, "sub/a.go", "package sub\n")
	writeTemp(t, root, "sub/b.txt", "hi\n")
	tool := &readTool{r: NewRegistry(root)}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"sub"}`))
	if err != nil {
		t.Fatalf("read_file on a directory should not error, got: %v", err)
	}
	if !strings.Contains(out, "is a directory") {
		t.Errorf("output should explain the path is a directory, got:\n%s", out)
	}
	for _, want := range []string{"a.go", "b.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing should include %q, got:\n%s", want, out)
		}
	}
}

func readTemp(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestMultiEditAppliesEditsInOrder(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "alpha beta gamma\n")
	tool := &multiEditTool{r: NewRegistry(root)}

	in, _ := json.Marshal(map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "ALPHA"},
			{"old_string": "gamma", "new_string": "GAMMA"},
		},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := readTemp(t, root, "f.txt"); got != "ALPHA beta GAMMA\n" {
		t.Errorf("content = %q", got)
	}
	if !strings.Contains(out, "2 edit(s)") {
		t.Errorf("summary = %q", out)
	}
}

func TestMultiEditSequentialDependency(t *testing.T) {
	// A later edit operates on the result of an earlier one.
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "one\n")
	tool := &multiEditTool{r: NewRegistry(root)}

	in, _ := json.Marshal(map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "one", "new_string": "two"},
			{"old_string": "two", "new_string": "three"},
		},
	})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := readTemp(t, root, "f.txt"); got != "three\n" {
		t.Errorf("content = %q", got)
	}
}

func TestMultiEditAtomicOnFailure(t *testing.T) {
	root := t.TempDir()
	const orig = "keep this\n"
	writeTemp(t, root, "f.txt", orig)
	tool := &multiEditTool{r: NewRegistry(root)}

	in, _ := json.Marshal(map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "keep", "new_string": "kept"},
			{"old_string": "nonexistent", "new_string": "x"},
		},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for unmatched edit")
	}
	if got := readTemp(t, root, "f.txt"); got != orig {
		t.Errorf("file should be unchanged on failure, got %q", got)
	}
}

func TestMultiEditAmbiguousRequiresReplaceAll(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "x x x\n")
	tool := &multiEditTool{r: NewRegistry(root)}

	in, _ := json.Marshal(map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{{"old_string": "x", "new_string": "y"}},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected ambiguity error without replace_all")
	}

	in, _ = json.Marshal(map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{{"old_string": "x", "new_string": "y", "replace_all": true}},
	})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatalf("replace_all should succeed: %v", err)
	}
	if got := readTemp(t, root, "f.txt"); got != "y y y\n" {
		t.Errorf("content = %q", got)
	}
}

func TestMultiEditEmptyEditsRejected(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "data\n")
	tool := &multiEditTool{r: NewRegistry(root)}
	in, _ := json.Marshal(map[string]any{"path": "f.txt", "edits": []map[string]any{}})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for empty edits")
	}
}

func TestMultiEditPreview(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "hello\n")
	tool := &multiEditTool{r: NewRegistry(root)}
	in, _ := json.Marshal(map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{{"old_string": "hello", "new_string": "world"}},
	})
	prev, err := tool.Preview(context.Background(), in)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.Path != "f.txt" || prev.Diff == "" || prev.Diff == "(no change)" {
		t.Errorf("preview = %+v", prev)
	}
	// Preview must not write to disk.
	if got := readTemp(t, root, "f.txt"); got != "hello\n" {
		t.Errorf("preview mutated file: %q", got)
	}
}

func TestEditFileEmptyOldStringRejected(t *testing.T) {
	root := t.TempDir()
	const orig = "data\n"
	writeTemp(t, root, "f.txt", orig)
	tool := &editTool{r: NewRegistry(root)}
	in, _ := json.Marshal(map[string]any{"path": "f.txt", "old_string": "", "new_string": "x"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for empty old_string")
	}
	if got := readTemp(t, root, "f.txt"); got != orig {
		t.Errorf("file should be unchanged: %q", got)
	}
}

// TestReadFileNotFoundSuggestsSibling: a missing file in an existing dir should
// surface a close-name match so the model can self-correct.
func TestReadFileNotFoundSuggestsSibling(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "ingest.py", "x\n")
	tool := &readTool{r: NewRegistry(root)}

	// Wrong extension, right stem.
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"ingest.go"}`))
	if err == nil {
		t.Fatal("expected a not-found error")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "ingest.py") {
		t.Errorf("error should suggest the sibling ingest.py, got: %v", err)
	}
}

// TestReadFileNotFoundMissingParent: when the parent directory doesn't exist,
// the error should name the nearest existing ancestor.
func TestReadFileNotFoundMissingParent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &readTool{r: NewRegistry(root)}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"app/processing/ingest.py"}`))
	if err == nil {
		t.Fatal("expected a not-found error")
	}
	// processing/ doesn't exist; nearest existing is app/.
	if !strings.Contains(err.Error(), "doesn't exist") || !strings.Contains(err.Error(), "app") {
		t.Errorf("error should point at the missing dir and nearest existing ancestor, got: %v", err)
	}
}

func TestMultiEditEmptyOldStringRejected(t *testing.T) {
	root := t.TempDir()
	const orig = "data\n"
	writeTemp(t, root, "f.txt", orig)
	tool := &multiEditTool{r: NewRegistry(root)}
	in, _ := json.Marshal(map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{{"old_string": "", "new_string": "x"}},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for empty old_string")
	}
	if got := readTemp(t, root, "f.txt"); got != orig {
		t.Errorf("file should be unchanged: %q", got)
	}
}

// TestReadDedupCache verifies the per-session read cache: an identical repeat
// read of an unchanged file returns a compact stub instead of the body, but a
// changed file (or a write/edit through the registry) re-sends fresh contents.
func TestReadDedupCache(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "alpha\nbeta\ngamma\n")
	reg := NewRegistry(root)
	rt := &readTool{r: reg}
	args := json.RawMessage(`{"path":"f.txt"}`)

	first, err := rt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !strings.Contains(first, "alpha") {
		t.Fatalf("first read should return body, got:\n%s", first)
	}

	second, err := rt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if strings.Contains(second, "alpha") {
		t.Errorf("repeat read of unchanged file should NOT re-send body, got:\n%s", second)
	}
	if !strings.Contains(second, "unchanged") {
		t.Errorf("repeat read should return an 'unchanged' stub, got:\n%s", second)
	}

	// A write through the registry invalidates the cache → body comes back.
	wt := &writeTool{r: reg}
	if _, err := wt.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","content":"delta\nepsilon\n"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	third, err := rt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("third read: %v", err)
	}
	if !strings.Contains(third, "delta") {
		t.Errorf("read after write should re-send fresh body, got:\n%s", third)
	}
}

// TestIdenticalEditRejectedWithGuidance verifies a no-op edit (old_string ==
// new_string) is rejected with a self-correcting message, for both edit_file and
// multi_edit (where the offending edit is named by its 1-based index).
func TestIdenticalEditRejectedWithGuidance(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "alpha\nbeta\ngamma\n")
	reg := NewRegistry(root)

	// edit_file: identical old/new.
	_, err := (&editTool{r: reg}).Execute(context.Background(),
		json.RawMessage(`{"path":"f.txt","old_string":"beta","new_string":"beta"}`))
	if err == nil || !strings.Contains(err.Error(), "changes nothing") {
		t.Errorf("edit_file no-op error = %v, want self-correcting 'changes nothing' message", err)
	}

	// multi_edit: edit 2 is the no-op; error must name it and stay self-correcting.
	_, err = (&multiEditTool{r: reg}).Execute(context.Background(), json.RawMessage(
		`{"path":"f.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"gamma","new_string":"gamma"}]}`))
	if err == nil || !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "changes nothing") {
		t.Errorf("multi_edit no-op error = %v, want 'edit 2 … changes nothing'", err)
	}
	// Atomic: the valid first edit must NOT have been written.
	if got, _ := os.ReadFile(filepath.Join(root, "f.txt")); strings.Contains(string(got), "ALPHA") {
		t.Errorf("multi_edit must be atomic — no edit should apply when one is rejected; file: %q", got)
	}
}
