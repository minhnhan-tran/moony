package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func findSymbolReg(t *testing.T, files map[string]string) *findSymbolTool {
	t.Helper()
	t.Setenv("MOONY_RG", "0") // force the pure-Go scanner for determinism
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
	return &findSymbolTool{r: NewRegistry(dir)}
}

func runFindSymbol(t *testing.T, ft *findSymbolTool, args map[string]any) string {
	t.Helper()
	in, _ := json.Marshal(args)
	out, err := ft.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("find_symbol: %v", err)
	}
	return out
}

// TestFindSymbolFindsDefinitionNotUsage verifies find_symbol returns the
// declaration site and skips a plain call site that grep would surface.
func TestFindSymbolFindsDefinitionNotUsage(t *testing.T) {
	ft := findSymbolReg(t, map[string]string{
		"def.go": "package p\n\nfunc Parse(s string) error { return nil }\n",
		"use.go": "package p\n\nfunc run() { _ = Parse(\"x\") }\n",
	})
	out := runFindSymbol(t, ft, map[string]any{"name": "Parse"})
	if !strings.Contains(out, "def.go:3:") {
		t.Errorf("should report the func definition at def.go:3, got:\n%s", out)
	}
	if strings.Contains(out, "use.go") {
		t.Errorf("should NOT report the call site in use.go, got:\n%s", out)
	}
}

// TestFindSymbolLanguages spot-checks a few declaration shapes across languages.
func TestFindSymbolLanguages(t *testing.T) {
	cases := []struct {
		file, content, name, wantLine string
	}{
		{"t.go", "type UserService struct{}\n", "UserService", "t.go:1:"},
		{"c.py", "class Widget:\n    pass\n", "Widget", "c.py:1:"},
		{"f.py", "def compute_total(x):\n    return x\n", "compute_total", "f.py:1:"},
		{"a.ts", "export const makeClient = (opts) => ({});\n", "makeClient", "a.ts:1:"},
		{"r.rs", "pub fn handle_request() {}\n", "handle_request", "r.rs:1:"},
		{"i.ts", "interface Options { a: number }\n", "Options", "i.ts:1:"},
		{"m.ts", "class Foo {\n  myMethod() {\n    return 1;\n  }\n}\n", "myMethod", "m.ts:2:"},
		{"i.go", "package p\ntype Tool interface {\n\tExecute(ctx context.Context) (string, error)\n}\n", "Execute", "i.go:3:"},
		{"s.go", "package p\ntype MyStruct struct {\n\tFieldName string\n}\n", "FieldName", "s.go:3:"},
		{"m.java", "class Foo {\n    void myMethod(String x) {\n    }\n}\n", "myMethod", "m.java:2:"},
	}
	for _, c := range cases {
		ft := findSymbolReg(t, map[string]string{c.file: c.content})
		out := runFindSymbol(t, ft, map[string]any{"name": c.name})
		if !strings.Contains(out, c.wantLine) {
			t.Errorf("find_symbol %q in %s: want a hit at %s, got:\n%s", c.name, c.file, c.wantLine, out)
		}
	}
}

// TestFindSymbolNoMatchMessage verifies the helpful fallback message when no
// declaration is found (e.g. the name only appears as a usage).
func TestFindSymbolNoMatchMessage(t *testing.T) {
	ft := findSymbolReg(t, map[string]string{
		"use.go": "package p\nfunc run() { doThing() }\n",
	})
	out := runFindSymbol(t, ft, map[string]any{"name": "doThing"})
	if !strings.Contains(out, "No definition of") || !strings.Contains(out, "grep") {
		t.Errorf("expected a helpful no-definition message pointing at grep, got:\n%s", out)
	}
}

// TestFindSymbolRequiresName verifies a missing name is rejected.
func TestFindSymbolRequiresName(t *testing.T) {
	ft := findSymbolReg(t, map[string]string{"x.go": "package p\n"})
	in, _ := json.Marshal(map[string]any{"name": "  "})
	if _, err := ft.Execute(context.Background(), in); err == nil {
		t.Error("blank name should be rejected")
	}
}

// TestFindSymbolWordBoundary verifies it doesn't match a name that is only a
// substring of a different identifier's declaration.
func TestFindSymbolWordBoundary(t *testing.T) {
	ft := findSymbolReg(t, map[string]string{
		"x.go": "package p\nfunc ParseConfig() {}\n",
	})
	out := runFindSymbol(t, ft, map[string]any{"name": "Parse"})
	if !strings.Contains(out, "No definition of") {
		t.Errorf("'Parse' should not match the declaration of 'ParseConfig', got:\n%s", out)
	}
}

// TestSymbolDefRegexpCompiles guards that the generated pattern is valid RE2.
func TestSymbolDefRegexpCompiles(t *testing.T) {
	g := &grepTool{r: NewRegistry(t.TempDir())}
	if _, err := g.search(context.Background(), grepInput{Pattern: symbolDefRegexp("Foo.bar"), Path: "."}); err != nil {
		t.Fatalf("symbolDefRegexp must produce a valid pattern: %v", err)
	}
}
