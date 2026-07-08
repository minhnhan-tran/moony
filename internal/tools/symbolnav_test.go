package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symbolNavReg(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewRegistry(dir)
}

func run(t *testing.T, tool interface {
	Execute(context.Context, json.RawMessage) (string, error)
}, args map[string]any) string {
	t.Helper()
	in, _ := json.Marshal(args)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

func TestReadSymbolReturnsBody(t *testing.T) {
	r := symbolNavReg(t, map[string]string{
		"a.go": "package a\n\nfunc Foo(x int) int {\n\ty := x + 1\n\treturn y\n}\n\nfunc Bar() {}\n",
	})
	out := run(t, &readSymbolTool{r: r}, map[string]any{"name": "Foo"})
	if !strings.Contains(out, "func Foo(x int) int {") {
		t.Fatalf("missing signature:\n%s", out)
	}
	if !strings.Contains(out, "return y") {
		t.Fatalf("body not spanned to closing brace:\n%s", out)
	}
	if strings.Contains(out, "func Bar") {
		t.Fatalf("read past symbol body into Bar:\n%s", out)
	}
	if !strings.Contains(out, "a.go:3") {
		t.Fatalf("missing file:line header:\n%s", out)
	}
}

func TestReadSymbolPythonIndent(t *testing.T) {
	r := symbolNavReg(t, map[string]string{
		"m.py": "def foo():\n    x = 1\n    return x\n\ndef bar():\n    return 2\n",
	})
	out := run(t, &readSymbolTool{r: r}, map[string]any{"name": "foo"})
	if !strings.Contains(out, "return x") {
		t.Fatalf("indent body not captured:\n%s", out)
	}
	if strings.Contains(out, "def bar") {
		t.Fatalf("spilled into next def:\n%s", out)
	}
}

func TestReadSymbolNotFound(t *testing.T) {
	r := symbolNavReg(t, map[string]string{"a.go": "package a\n"})
	out := run(t, &readSymbolTool{r: r}, map[string]any{"name": "Nope"})
	if !strings.Contains(out, "No definition") {
		t.Fatalf("expected not-found, got:\n%s", out)
	}
}

func TestReadSymbolRanksNonTestFirst(t *testing.T) {
	r := symbolNavReg(t, map[string]string{
		"impl_test.go": "package a\n\nfunc Widget() {}\n",
		"impl.go":      "package a\n\nfunc Widget() int { return 1 }\n",
	})
	out := run(t, &readSymbolTool{r: r}, map[string]any{"name": "Widget"})
	// The primary (first) definition shown should be the non-test one.
	head := out
	if i := strings.Index(out, "Other definitions"); i >= 0 {
		head = out[:i]
	}
	if !strings.Contains(head, "impl.go") {
		t.Fatalf("expected impl.go ranked first:\n%s", out)
	}
	if !strings.Contains(out, "impl_test.go") {
		t.Fatalf("expected test def listed as other:\n%s", out)
	}
}

func TestFindReferencesExcludesDefinition(t *testing.T) {
	r := symbolNavReg(t, map[string]string{
		"def.go":  "package a\n\nfunc Compute() int { return 41 }\n",
		"use1.go": "package a\n\nfunc a() int { return Compute() + 1 }\n",
		"use2.go": "package a\n\nvar _ = Compute\n",
	})
	out := run(t, &findReferencesTool{r: r}, map[string]any{"name": "Compute"})
	if !strings.Contains(out, "use1.go") || !strings.Contains(out, "use2.go") {
		t.Fatalf("missing usages:\n%s", out)
	}
	if strings.Contains(out, "func Compute() int") {
		t.Fatalf("definition line should be excluded:\n%s", out)
	}
	if !strings.Contains(out, "definition site") {
		t.Fatalf("expected definition-excluded note:\n%s", out)
	}
}

func TestFindReferencesInclude(t *testing.T) {
	r := symbolNavReg(t, map[string]string{
		"x.go": "package a\nvar _ = Thing\n",
		"y.ts": "const z = Thing()\n",
	})
	out := run(t, &findReferencesTool{r: r}, map[string]any{"name": "Thing", "include": "*.ts"})
	if strings.Contains(out, "x.go") {
		t.Fatalf("include filter should exclude x.go:\n%s", out)
	}
	if !strings.Contains(out, "y.ts") {
		t.Fatalf("expected y.ts:\n%s", out)
	}
}

func TestFindReferencesNone(t *testing.T) {
	r := symbolNavReg(t, map[string]string{"a.go": "package a\nfunc Solo() {}\n"})
	out := run(t, &findReferencesTool{r: r}, map[string]any{"name": "Solo"})
	if !strings.Contains(out, "only its definition") {
		t.Fatalf("expected only-definition message:\n%s", out)
	}
}
