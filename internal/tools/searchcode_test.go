package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func searchCodeReg(t *testing.T, files map[string]string) *searchCodeTool {
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
	return &searchCodeTool{r: NewRegistry(dir)}
}

func runSearchCode(t *testing.T, s *searchCodeTool, args map[string]any) string {
	t.Helper()
	in, _ := json.Marshal(args)
	out, err := s.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("search_code: %v", err)
	}
	return out
}

func TestSplitCamel(t *testing.T) {
	cases := map[string][]string{
		"parseConfig":  {"parse", "Config"},
		"parse_config": {"parse_config"}, // splitCamel doesn't handle underscores
		"HTTPServer":   {"HTTP", "Server"},
		"v2":           {"v", "2"},
		"ConfigPath":   {"Config", "Path"},
		"lower":        {"lower"},
	}
	for in, want := range cases {
		if got := splitCamel(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCamel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTokenizeCodeSplitsIdentifiers(t *testing.T) {
	toks := tokenizeCode("func parseConfig(path string) {}")
	set := map[string]bool{}
	for _, tk := range toks {
		set[tk] = true
	}
	// whole identifier plus camelCase subtokens, all lowercased; 1-char dropped.
	for _, want := range []string{"func", "parseconfig", "parse", "config", "path", "string"} {
		if !set[want] {
			t.Errorf("expected token %q in %v", want, toks)
		}
	}
	if set["s"] {
		t.Errorf("single-character tokens should be dropped, got %v", toks)
	}
}

// A camelCase query term must match a snake_case definition (and vice versa) —
// the whole point of splitting identifiers before ranking.
func TestSearchCodeMatchesAcrossNamingConventions(t *testing.T) {
	s := searchCodeReg(t, map[string]string{
		"a.go": "package a\n\nfunc parse_config(path string) error {\n\treturn nil\n}\n",
		"b.go": "package b\n\n// unrelated helper\nfunc totallyDifferent() {}\n",
	})
	out := runSearchCode(t, s, map[string]any{"query": "parseConfig"})
	if !strings.Contains(out, "a.go:") {
		t.Fatalf("expected a.go chunk for query parseConfig, got:\n%s", out)
	}
	if strings.Contains(out, "b.go:") {
		t.Errorf("unrelated b.go should not appear, got:\n%s", out)
	}
}

// The most relevant chunk (more query-term hits) should rank first.
func TestSearchCodeRanksMostRelevantFirst(t *testing.T) {
	s := searchCodeReg(t, map[string]string{
		"strong.go": "package p\nfunc handleRequest(req Request) Response {\n\treq.validate()\n\treturn buildResponse(req)\n}\n",
		"weak.go":   "package p\n// request mentioned once in a comment\nvar x = 1\n",
	})
	out := runSearchCode(t, s, map[string]any{"query": "request response"})
	iStrong := strings.Index(out, "strong.go")
	iWeak := strings.Index(out, "weak.go")
	if iStrong < 0 {
		t.Fatalf("strong.go should appear, got:\n%s", out)
	}
	if iWeak >= 0 && iStrong > iWeak {
		t.Errorf("strong.go should rank before weak.go, got:\n%s", out)
	}
}

func TestSearchCodeLimitAndFormat(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".go"] = "package p\nfunc widget() { widget() }\n"
	}
	s := searchCodeReg(t, files)
	out := runSearchCode(t, s, map[string]any{"query": "widget", "limit": 2})
	// Header + line-numbered snippet with file:start-end headers.
	if !strings.HasPrefix(out, "search_code:") {
		t.Errorf("missing header, got:\n%s", out)
	}
	headers := strings.Count(out, ".go:")
	if headers != 2 {
		t.Errorf("limit=2 should yield 2 chunk headers, got %d:\n%s", headers, out)
	}
}

func TestSearchCodeInclude(t *testing.T) {
	s := searchCodeReg(t, map[string]string{
		"code.go":  "package p\nfunc target() {}\n",
		"notes.md": "target is documented here in markdown\n",
	})
	out := runSearchCode(t, s, map[string]any{"query": "target", "include": "*.go"})
	if !strings.Contains(out, "code.go") {
		t.Fatalf("expected code.go, got:\n%s", out)
	}
	if strings.Contains(out, "notes.md") {
		t.Errorf("include=*.go should exclude notes.md, got:\n%s", out)
	}
}

// A definition of the query term should rank above chunks that merely mention
// it, even when the mentions are more frequent.
func TestSearchCodeBoostsDefinitions(t *testing.T) {
	s := searchCodeReg(t, map[string]string{
		"def.go": "package p\n\nfunc HandlePayment(amount int) error {\n\treturn nil\n}\n",
		"use.go": "package p\n\nfunc caller() {\n\tHandlePayment(1)\n\tHandlePayment(2)\n\tHandlePayment(3)\n\tHandlePayment(4)\n}\n",
	})
	out := runSearchCode(t, s, map[string]any{"query": "HandlePayment"})
	iDef, iUse := strings.Index(out, "def.go"), strings.Index(out, "use.go")
	if iDef < 0 {
		t.Fatalf("defining chunk should appear, got:\n%s", out)
	}
	if iUse >= 0 && iDef > iUse {
		t.Errorf("the chunk that DEFINES the term should rank first despite fewer mentions, got:\n%s", out)
	}
}

func TestSearchCodeNoMatch(t *testing.T) {
	s := searchCodeReg(t, map[string]string{"a.go": "package p\nfunc foo() {}\n"})
	out := runSearchCode(t, s, map[string]any{"query": "nonexistentzzz"})
	if !strings.Contains(out, "no relevant code found") {
		t.Errorf("expected no-match message, got:\n%s", out)
	}
}

func TestSearchCodeEmptyQuery(t *testing.T) {
	s := searchCodeReg(t, map[string]string{"a.go": "package p\n"})
	in, _ := json.Marshal(map[string]any{"query": "  "})
	if _, err := s.Execute(context.Background(), in); err == nil {
		t.Error("expected error for empty query")
	}
}
