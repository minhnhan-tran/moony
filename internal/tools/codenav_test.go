package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/lsp"
)

func TestLocateSymbol(t *testing.T) {
	src := "package main\n\nfunc Foo() int { return foo }\nvar foo = Foo()\n"
	// Line 3 (index 2): "func Foo() int { return foo }"
	// Line 4 (index 3): "var foo = Foo()"

	cases := []struct {
		name           string
		symbol         string
		occ, hint      int
		wantLine, want int // wantLine 0-based, want = utf16 char
		ok             bool
	}{
		{"first Foo", "Foo", 0, 0, 2, 5, true},                  // func Foo
		{"second Foo via occurrence", "Foo", 2, 0, 3, 10, true}, // = Foo()
		{"foo whole word not inside Foo", "foo", 0, 0, 2, 24, true},
		{"line hint picks line 4", "foo", 0, 4, 3, 4, true},
		{"missing", "Bar", 0, 0, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, char, ok := locateSymbol(src, c.symbol, c.occ, c.hint)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if line != c.wantLine || char != c.want {
				t.Errorf("got line=%d char=%d, want line=%d char=%d", line, char, c.wantLine, c.want)
			}
		})
	}
}

func TestWholeWordCols(t *testing.T) {
	// "foobar" must NOT match "foo"; standalone "foo" must.
	got := wholeWordCols("foo foobar _foo foo", "foo")
	// positions: 0 ("foo"), 16 ("foo" at end). "foobar" (4) and "_foo" (11) excluded.
	if len(got) != 2 || got[0] != 0 || got[1] != 16 {
		t.Fatalf("wholeWordCols = %v, want [0 16]", got)
	}
}

func TestUTF16Len(t *testing.T) {
	if n := utf16Len("héllo"); n != 5 { // é is 1 UTF-16 unit
		t.Errorf("utf16Len(héllo) = %d, want 5", n)
	}
	if n := utf16Len("a𝄞b"); n != 4 { // 𝄞 is a surrogate pair (2 units)
		t.Errorf("utf16Len(a𝄞b) = %d, want 4", n)
	}
}

func TestRelOrAbs(t *testing.T) {
	root := "/work/proj"
	if got := relOrAbs("/work/proj/internal/x.go", root); got != "internal/x.go" {
		t.Errorf("relOrAbs rel = %q, want internal/x.go", got)
	}
	// Outside root → keep absolute.
	if got := relOrAbs("/etc/passwd", root); got != "/etc/passwd" {
		t.Errorf("relOrAbs outside = %q, want /etc/passwd", got)
	}
}

// fakeLSP is a stub lspManager for exercising code_nav without a real server.
type fakeLSP struct {
	has  bool
	defs []lsp.Location
	refs []lsp.Location
	hov  string
	err  error
}

func (f *fakeLSP) Empty() bool         { return false }
func (f *fakeLSP) Handles(string) bool { return f.has }
func (f *fakeLSP) Definition(context.Context, string, int, int) ([]lsp.Location, string, error) {
	return f.defs, "gopls", f.err
}
func (f *fakeLSP) References(context.Context, string, int, int, bool) ([]lsp.Location, string, error) {
	return f.refs, "gopls", f.err
}
func (f *fakeLSP) Hover(context.Context, string, int, int) (string, string, error) {
	return f.hov, "gopls", f.err
}

func loc(path string, line, char int) lsp.Location {
	var l lsp.Location
	l.Path = strings.TrimPrefix(path, "file://")
	l.Range.Start.Line = line
	l.Range.Start.Character = char
	return l
}

func codeNavFixture(t *testing.T) (*codeNavTool, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"),
		[]byte("package p\n\nfunc Target() {}\n\nfunc use() { Target() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(dir)
	return &codeNavTool{r: reg}, dir
}

func runNav(t *testing.T, tool *codeNavTool, in map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(in)
	out, err := tool.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	return out
}

func TestCodeNavNoServer(t *testing.T) {
	tool, _ := codeNavFixture(t)
	// No SetLSP at all → nil manager.
	out := runNav(t, tool, map[string]any{"action": "definition", "path": "sample.go", "symbol": "Target"})
	if !strings.Contains(out, "No language server is running") {
		t.Fatalf("want graceful no-server message, got: %s", out)
	}
}

func TestCodeNavUnsupportedFileType(t *testing.T) {
	tool, _ := codeNavFixture(t)
	tool.r.SetLSP(&fakeLSP{has: false})
	out := runNav(t, tool, map[string]any{"action": "hover", "path": "sample.go", "symbol": "Target"})
	if !strings.Contains(out, "No running language server handles") {
		t.Fatalf("want unsupported-type message, got: %s", out)
	}
}

func TestCodeNavDefinition(t *testing.T) {
	tool, dir := codeNavFixture(t)
	tool.r.SetLSP(&fakeLSP{has: true, defs: []lsp.Location{
		loc("file://"+filepath.Join(dir, "sample.go"), 2, 5),
	}})
	out := runNav(t, tool, map[string]any{"action": "definition", "path": "sample.go", "symbol": "Target"})
	// 0-based line 2/char 5 → displayed 1-based 3:6, path relative to root.
	if !strings.Contains(out, "sample.go:3:6") {
		t.Fatalf("want sample.go:3:6 in output, got: %s", out)
	}
	if !strings.Contains(out, "definition of \"Target\"") {
		t.Fatalf("want definition header, got: %s", out)
	}
}

func TestCodeNavReferencesDedupeAndSort(t *testing.T) {
	tool, dir := codeNavFixture(t)
	abs := "file://" + filepath.Join(dir, "sample.go")
	tool.r.SetLSP(&fakeLSP{has: true, refs: []lsp.Location{
		loc(abs, 4, 12),
		loc(abs, 2, 5),
		loc(abs, 4, 12), // duplicate
	}})
	out := runNav(t, tool, map[string]any{"action": "references", "path": "sample.go", "symbol": "Target"})
	if strings.Count(out, "sample.go:") != 2 {
		t.Fatalf("want 2 deduped rows, got: %s", out)
	}
	// sorted: line 3 before line 5
	i3 := strings.Index(out, "sample.go:3:6")
	i5 := strings.Index(out, "sample.go:5:13")
	if i3 < 0 || i5 < 0 || i3 > i5 {
		t.Fatalf("want sorted 3:6 before 5:13, got: %s", out)
	}
}

func TestCodeNavHover(t *testing.T) {
	tool, _ := codeNavFixture(t)
	tool.r.SetLSP(&fakeLSP{has: true, hov: "func Target()"})
	out := runNav(t, tool, map[string]any{"action": "hover", "path": "sample.go", "symbol": "Target"})
	if !strings.Contains(out, "func Target()") {
		t.Fatalf("want hover text, got: %s", out)
	}
}

func TestCodeNavSymbolNotFound(t *testing.T) {
	tool, _ := codeNavFixture(t)
	tool.r.SetLSP(&fakeLSP{has: true})
	b, _ := json.Marshal(map[string]any{"action": "definition", "path": "sample.go", "symbol": "Nope"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Fatal("expected error for missing symbol")
	}
}

func TestCodeNavBadAction(t *testing.T) {
	tool, _ := codeNavFixture(t)
	tool.r.SetLSP(&fakeLSP{has: true})
	b, _ := json.Marshal(map[string]any{"action": "rename", "path": "sample.go", "symbol": "Target"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Fatal("expected error for unknown action")
	}
}
