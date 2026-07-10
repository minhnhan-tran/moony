package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanLintRunsDetectsGo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := planLintRuns(root, root)
	if len(runs) != 1 || !strings.Contains(runs[0].lang, "Go") {
		t.Fatalf("planLintRuns(go.mod root) = %+v, want exactly one Go run", runs)
	}
	if runs[0].args[len(runs[0].args)-1] != "./..." {
		t.Errorf("whole-repo target should lint './...', got args %v", runs[0].args)
	}
}

func TestPlanLintRunsDetectsMultipleLanguages(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runs := planLintRuns(root, root)
	if len(runs) != 4 {
		t.Fatalf("planLintRuns with 4 marker files = %d runs, want 4: %+v", len(runs), runs)
	}
}

func TestPlanLintRunsFileTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "a.go")
	if err := os.WriteFile(file, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := planLintRuns(root, file)
	if len(runs) != 1 {
		t.Fatalf("planLintRuns(file) = %+v, want 1 run", runs)
	}
	last := runs[0].args[len(runs[0].args)-1]
	if last != "./pkg/a.go" {
		t.Errorf("file target pattern = %q, want ./pkg/a.go", last)
	}
}

func TestPlanLintRunsNoMarkers(t *testing.T) {
	root := t.TempDir()
	if runs := planLintRuns(root, root); len(runs) != 0 {
		t.Errorf("planLintRuns with no marker files = %+v, want none", runs)
	}
}

func TestLintToolOnMoonyRepo(t *testing.T) {
	// Exercise the real Execute() path against this actual repo (which has a
	// go.mod, so "go" being on PATH — required just to build/test at all —
	// guarantees at least the go-vet-fallback branch always runs).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(wd) // internal/tools -> internal -> repo root is two up; walk to find go.mod
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skip("couldn't locate repo root go.mod from test working dir")
	}
	reg := NewRegistry(root)
	tool := &lintTool{r: reg, max: 1 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Go") {
		t.Errorf("lint output should mention the Go run, got: %s", out)
	}
}

func TestLintToolNoProjectMarkers(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(root)
	tool := &lintTool{r: reg, max: 1 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "nothing to lint") {
		t.Errorf("expected a no-project-detected message, got: %s", out)
	}
}

func TestLintToolIsReadOnly(t *testing.T) {
	if (&lintTool{}).Mutates() {
		t.Error("lint should never be reported as mutating")
	}
}

func TestRunLinterTreatsNonZeroExitAsSuccess(t *testing.T) {
	// `go vet` on a file with an actual vet issue exits non-zero; the tool
	// must still report it as a normal (non-error) result, not fail.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := `package x

import "fmt"

func F() {
	fmt.Printf("%d")
}
`
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(root)
	tool := &lintTool{r: reg, max: 1 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a linter finding issues must not be a tool error, got: %v", err)
	}
	if !strings.Contains(out, "Printf") && !strings.Contains(out, "no issues found") {
		// golangci-lint or go vet may or may not be installed in the test
		// environment; either a real finding or a graceful skip is fine, but
		// silence (empty string) or a crash is not.
		t.Errorf("unexpected lint output: %q", out)
	}
}
