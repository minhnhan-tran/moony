package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFixture lays out a small multi-language repo for the symbol-map tests.
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"parser/parse.go": `package parser

// ParseConfig reads config.
func ParseConfig(path string) error {
	return validate(path)
}

func validate(p string) error { return nil }
`,
		"app/main.go": `package main

import "x/parser"

func main() {
	_ = parser.ParseConfig("a")
	run()
}

func run() { ParseConfig("b") }
`,
		"web/handler.ts": `export function ParseConfig(x: string) { return handle(x); }
const handle = (x: string) => { return x; };
`,
		"util/py.py": `def parse_config(path):
    return helper(path)

def helper(p):
    return p
`,
		// A file that should be skipped (unsupported ext).
		"README.md": "# ParseConfig everywhere\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSymbolMapExtractsDefsAndCallers(t *testing.T) {
	r := Default(writeFixture(t), 100000)
	m := r.SymbolMap()
	if len(m.Entries) == 0 {
		t.Fatal("no symbols extracted")
	}

	find := func(name string) *SymbolEntry {
		for i := range m.Entries {
			if m.Entries[i].Name == name {
				return &m.Entries[i]
			}
		}
		return nil
	}

	pc := find("ParseConfig")
	if pc == nil {
		t.Fatal("ParseConfig not found in symbol map")
	}
	// ParseConfig is called from app/main.go (main + run) — expect a caller.
	if len(pc.Callers) == 0 {
		t.Errorf("ParseConfig should have callers, got none")
	}
	foundAppCaller := false
	for _, c := range pc.Callers {
		if strings.HasPrefix(c, "app/") {
			foundAppCaller = true
		}
	}
	if !foundAppCaller {
		t.Errorf("expected app/ file among ParseConfig callers, got %v", pc.Callers)
	}

	// README.md must be ignored (unsupported extension).
	for _, e := range m.Entries {
		if strings.HasSuffix(e.File, ".md") {
			t.Errorf("markdown file should not be in symbol map: %s", e.File)
		}
	}

	// Language keywords / builtins must not appear as definitions.
	for _, e := range m.Entries {
		if isKeyword(e.Name) {
			t.Errorf("keyword/builtin leaked as a symbol: %q", e.Name)
		}
	}
}

func TestSymbolMapScoringPrefersShallowAndPopular(t *testing.T) {
	// Two files defining a symbol at different depths; the shallower one should
	// win as the defining file (0.8^depth weighting).
	dir := t.TempDir()
	shallow := "top.go"
	deep := "a/b/c/deep.go"
	body := "package p\nfunc Widget() {}\n"
	for _, p := range []string{shallow, deep} {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := Default(dir, 100000)
	m := r.SymbolMap()
	var w *SymbolEntry
	for i := range m.Entries {
		if m.Entries[i].Name == "Widget" {
			w = &m.Entries[i]
		}
	}
	if w == nil {
		t.Fatal("Widget not found")
	}
	if w.File != shallow {
		t.Errorf("shallower file should define Widget, got %s", w.File)
	}
}

func TestSymbolDigestAsyncNonBlocking(t *testing.T) {
	r := Default(writeFixture(t), 100000)
	// First call must be non-blocking: it kicks off a background build and
	// returns immediately (empty), so startup / prompt assembly never stalls.
	start := time.Now()
	first := r.SymbolDigest()
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Errorf("SymbolDigest should return immediately on cold cache, took %s", el)
	}
	_ = first // may be "" on the cold path

	// The digest fills in once the background build completes.
	var d string
	for i := 0; i < 200; i++ {
		if d = r.SymbolDigest(); d != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d == "" {
		t.Fatal("expected a non-empty digest after the background build")
	}
	if lines := strings.Count(d, "\n"); lines > symbolDigestMaxLines+5 {
		t.Errorf("digest not bounded: %d lines", lines)
	}
	r.SetSymbolDigest(false)
	if r.SymbolDigest() != "" {
		t.Error("digest should be empty when disabled")
	}
}

func TestFindFilesRanksRelevant(t *testing.T) {
	r := Default(writeFixture(t), 100000)
	tool := &findFilesTool{r: r}
	in, _ := json.Marshal(map[string]any{"query": "parse config", "count": 5})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parser/parse.go") {
		t.Errorf("expected parser/parse.go in results, got:\n%s", out)
	}
}

func TestFindFilesRequiresQuery(t *testing.T) {
	r := Default(writeFixture(t), 100000)
	tool := &findFilesTool{r: r}
	in, _ := json.Marshal(map[string]any{"query": "  "})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Error("expected error for empty query")
	}
}

func TestQueryTermsSplitIdentifiers(t *testing.T) {
	got := queryTerms("parseConfig HTTP_client")
	want := map[string]bool{"parse": true, "config": true, "http": true, "client": true}
	for _, term := range got {
		delete(want, term)
	}
	if len(want) > 0 {
		t.Errorf("missing terms after split: %v (got %v)", want, got)
	}
}
