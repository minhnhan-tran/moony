package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func grepReg(t *testing.T, files map[string]string) *grepTool {
	t.Helper()
	t.Setenv("MOONY_RG", "0") // force the pure-Go scanner so these tests are deterministic
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
	return &grepTool{r: NewRegistry(dir)}
}

func runGrep(t *testing.T, g *grepTool, args map[string]any) string {
	t.Helper()
	in, _ := json.Marshal(args)
	out, err := g.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	return out
}

// TestGrepRanksDefinitionsFirst: results are relevance-ranked by default, so the
// file whose match is a DEFINITION (b.go's `func Foo`) outranks a mere mention
// (a.go's comment) even though it sorts later by path. sort:"path" restores lexical
// order. Also checks the 'path:line: text' format.
func TestGrepRanksDefinitionsFirst(t *testing.T) {
	g := grepReg(t, map[string]string{
		"b.go": "package b\nfunc Foo() {}\n",
		"a.go": "package a\n// Foo here\n",
	})
	// Relevance (default): the definition in b.go comes first.
	out := runGrep(t, g, map[string]any{"pattern": "Foo"})
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "b.go:2: ") || !strings.HasPrefix(lines[1], "a.go:2: ") {
		t.Errorf("relevance order should put the definition (b.go) first, got:\n%s", out)
	}
	// sort:"path" restores lexical order.
	out = runGrep(t, g, map[string]any{"pattern": "Foo", "sort": "path"})
	lines = strings.Split(out, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "a.go:2: ") || !strings.HasPrefix(lines[1], "b.go:2: ") {
		t.Errorf("sort:path should be lexical (a.go then b.go), got:\n%s", out)
	}
}

// TestGrepDeprioritizesTestAndVendor: identical definitions rank by location —
// a normal source file beats a *_test.go and a vendor/ copy.
func TestGrepDeprioritizesTestAndVendor(t *testing.T) {
	g := grepReg(t, map[string]string{
		"vendor/v.go": "func Handler() {}\n",
		"a_test.go":   "func Handler() {}\n",
		"server.go":   "func Handler() {}\n",
	})
	out := runGrep(t, g, map[string]any{"pattern": "Handler"})
	lines := strings.Split(out, "\n")
	if len(lines) < 1 || !strings.HasPrefix(lines[0], "server.go:1: ") {
		t.Errorf("normal source file should rank first over test/vendor, got:\n%s", out)
	}
}

// TestGrepDefBonusIsNotPerHit: the definition bonus is credited once per file,
// so a vendor file with several declaration-shaped lines can't out-score a
// normal source file holding the single real definition (regression for the
// per-hit stacking that let a heavy vendor copy dominate).
func TestGrepDefBonusIsNotPerHit(t *testing.T) {
	g := grepReg(t, map[string]string{
		"vendor/lib.go": "func HandlerA() {}\nfunc HandlerB() {}\nfunc HandlerC() {}\n",
		"server.go":     "func Handler() {}\n",
	})
	out := runGrep(t, g, map[string]any{"pattern": "Handler"})
	lines := strings.Split(out, "\n")
	if len(lines) < 1 || !strings.HasPrefix(lines[0], "server.go:1: ") {
		t.Errorf("normal source should outrank a vendor file with several decls (def bonus must be per-file, not per-hit), got:\n%s", out)
	}
}

// TestGrepDefLineNotFooledByComment: a comment that merely contains a definition
// keyword ("type") must NOT be treated as a definition — the real `func Foo`
// outranks it (regression for defLineRe matching a keyword anywhere on the line).
func TestGrepDefLineNotFooledByComment(t *testing.T) {
	g := grepReg(t, map[string]string{
		"b.go": "package b\nfunc Foo() {}\n",      // the real definition
		"a.go": "package a\n// the type of Foo\n", // a comment containing the keyword "type"
	})
	out := runGrep(t, g, map[string]any{"pattern": "Foo"})
	lines := strings.Split(out, "\n")
	if len(lines) < 1 || !strings.HasPrefix(lines[0], "b.go:2: ") {
		t.Errorf("the real definition (b.go) should outrank a comment that merely contains a keyword, got:\n%s", out)
	}
}

func TestGrepIgnoreCase(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "Hello WORLD\n"})
	// A case-sensitive miss now auto-falls-back to case-insensitive (labeled), so the
	// model still gets the WORLD hit in one call instead of a bare no-match + retry.
	out := runGrep(t, g, map[string]any{"pattern": "world"})
	if !strings.Contains(out, "case-sensitive") || !strings.Contains(out, "x.go:1: ") {
		t.Errorf("case-sensitive miss should surface the case-insensitive hit with a label, got %q", out)
	}
	// An explicit ignore_case search matches directly (no fallback).
	if out := runGrep(t, g, map[string]any{"pattern": "world", "ignore_case": true}); !strings.Contains(out, "x.go:1: ") {
		t.Errorf("ignore_case should match WORLD, got %q", out)
	}
}

// TestGrepAutoCaseFallback: a case-sensitive miss auto-retries case-insensitively
// and returns the hit with a clear label, saving the model a retry.
func TestGrepAutoCaseFallback(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "package x\nfunc Handler() {}\n"})
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"handler"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "case-sensitive") || !strings.Contains(out, "x.go:") {
		t.Errorf("case-sensitive miss should auto-retry case-insensitively with a label, got:\n%s", out)
	}
}

// TestGrepAutoLiteralFallback: a pattern with regex metacharacters that finds no
// regex match auto-retries as a literal string (e.g. "foo(bar)", whose regex form
// means "foobar" and won't match the literal text).
func TestGrepAutoLiteralFallback(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "call foo(bar) here\n"})
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"foo(bar)"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "literal") || !strings.Contains(out, "x.go:") {
		t.Errorf("regex-metachar miss should auto-retry as a literal with a label, got:\n%s", out)
	}
}

// TestGrepInvalidRegexLiteralFallback: a pattern that doesn't compile as a regex
// (unbalanced paren) is retried as a literal instead of surfacing a compile error
// — the commonest "literal mistaken for a regex" case.
func TestGrepInvalidRegexLiteralFallback(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "call foo( here\nand bar\n"})
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"foo("}`))
	if err != nil {
		t.Fatalf("an invalid-regex pattern should auto-retry as a literal, not error: %v", err)
	}
	if !strings.Contains(out, "valid regex") || !strings.Contains(out, "x.go:") {
		t.Errorf("invalid-regex miss should retry literally with a label, got:\n%s", out)
	}
}

// TestGrepInvalidRegexNoMatch: an invalid-regex pattern with no literal match
// returns a diagnostic, not the cryptic compile error.
func TestGrepInvalidRegexNoMatch(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "nothing relevant here\n"})
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"zzz("}`))
	if err != nil {
		t.Fatalf("should not surface a compile error when a literal retry is possible: %v", err)
	}
	if !strings.HasPrefix(out, "(no matches)") {
		t.Errorf("invalid-regex with no literal match should return the no-match diagnostic, got:\n%s", out)
	}
}

// miss) returns a diagnostic noting the skip rules, so the model doesn't re-grep
// blindly.
func TestGrepNoMatchDiagnostic(t *testing.T) {
	g := grepReg(t, map[string]string{"x.go": "package x\n"})
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"zzznotpresenthere","include":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "(no matches)") || !strings.Contains(out, "skips .gitignore") {
		t.Errorf("a true miss should return a diagnostic, got:\n%s", out)
	}
	if !strings.Contains(out, "include=") {
		t.Errorf("the diagnostic should surface the active include filter, got:\n%s", out)
	}
}

func TestGrepLiteral(t *testing.T) {
	g := grepReg(t, map[string]string{"x.txt": "a.b.c\naXbXc\n"})
	out := runGrep(t, g, map[string]any{"pattern": "a.b.c", "literal": true})
	if !strings.Contains(out, "x.txt:1: ") || strings.Contains(out, "x.txt:2:") {
		t.Errorf("literal 'a.b.c' should match only the dotted line, got:\n%s", out)
	}
}

func TestGrepWord(t *testing.T) {
	g := grepReg(t, map[string]string{"x.txt": "foo\nfoobar\n"})
	out := runGrep(t, g, map[string]any{"pattern": "foo", "word": true})
	if !strings.Contains(out, "x.txt:1: ") || strings.Contains(out, "x.txt:2:") {
		t.Errorf("word search should match only the standalone 'foo', got:\n%s", out)
	}
}

func TestGrepFilesOnly(t *testing.T) {
	g := grepReg(t, map[string]string{
		"a.go": "match here\nmatch again\n",
		"b.go": "nope\n",
		"c.go": "match\n",
	})
	out := runGrep(t, g, map[string]any{"pattern": "match", "files_only": true})
	if out != "a.go\nc.go" {
		t.Errorf("files_only = %q, want \"a.go\\nc.go\"", out)
	}
}

func TestGrepContext(t *testing.T) {
	g := grepReg(t, map[string]string{"x.txt": "one\ntwo\nTARGET\nfour\nfive\n"})
	out := runGrep(t, g, map[string]any{"pattern": "TARGET", "context": 1})
	for _, want := range []string{"x.txt:2- two", "x.txt:3: TARGET", "x.txt:4- four"} {
		if !strings.Contains(out, want) {
			t.Errorf("context output missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "one") || strings.Contains(out, "five") {
		t.Errorf("context should not reach beyond ±1, got:\n%s", out)
	}
}

func TestGrepIncludePathAwareAndBraces(t *testing.T) {
	g := grepReg(t, map[string]string{
		"a.go":        "needle\n",
		"a.ts":        "needle\n",
		"a.md":        "needle\n",
		"sub/deep.go": "needle\n",
	})
	out := runGrep(t, g, map[string]any{"pattern": "needle", "include": "*.{go,ts}", "files_only": true})
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "a.ts") || !strings.Contains(out, "sub/deep.go") || strings.Contains(out, "a.md") {
		t.Errorf("brace include should match .go/.ts at any depth, not .md, got:\n%s", out)
	}
	out = runGrep(t, g, map[string]any{"pattern": "needle", "include": "sub/*.go", "files_only": true})
	if out != "sub/deep.go" {
		t.Errorf("pathful include 'sub/*.go' = %q, want sub/deep.go", out)
	}
}

func TestExpandBraces(t *testing.T) {
	got := expandBraces("*.{go,ts}")
	if len(got) != 2 || got[0] != "*.go" || got[1] != "*.ts" {
		t.Errorf("expandBraces(*.{go,ts}) = %v", got)
	}
	if got := expandBraces("*.go"); len(got) != 1 || got[0] != "*.go" {
		t.Errorf("expandBraces with no brace = %v", got)
	}
}

func TestRgEnabled(t *testing.T) {
	t.Setenv("MOONY_RG", "")
	if !rgEnabled() {
		t.Error("rg should be enabled by default")
	}
	for _, v := range []string{"0", "off", "false", "no"} {
		t.Setenv("MOONY_RG", v)
		if rgEnabled() {
			t.Errorf("MOONY_RG=%q should disable rg", v)
		}
	}
}

func TestBuildRgArgs(t *testing.T) {
	args := buildRgArgs(grepInput{
		Pattern: "foo", IgnoreCase: true, Literal: true, Word: true, Context: 2,
	}, 2, []string{"*.go", "*.ts"}, "/repo/.sqeignore", "internal")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--sort path", "-i", "-F", "-w", "-g *.go", "-g *.ts", "--ignore-file /repo/.sqeignore", "-C 2", "--json", "-e foo -- internal"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildRgArgs missing %q in: %s", want, joined)
		}
	}
	// files_only uses -l and omits --json.
	fl := strings.Join(buildRgArgs(grepInput{Pattern: "x", FilesOnly: true}, 0, nil, "", "."), " ")
	if !strings.Contains(fl, "-l") || strings.Contains(fl, "--json") {
		t.Errorf("files_only args wrong: %s", fl)
	}
}

func TestParseRgJSON(t *testing.T) {
	// A trimmed ripgrep --json stream: a context line then a match in a.go, and
	// a match in an ignored vendor/x.go that must be filtered out.
	data := `{"type":"begin","data":{"path":{"text":"a.go"}}}
{"type":"context","data":{"path":{"text":"a.go"},"lines":{"text":"package a\n"},"line_number":1}}
{"type":"match","data":{"path":{"text":"a.go"},"lines":{"text":"func Foo() {}\n"},"line_number":2}}
{"type":"end","data":{"path":{"text":"a.go"}}}
{"type":"match","data":{"path":{"text":"vendor/x.go"},"lines":{"text":"Foo\n"},"line_number":9}}
{"type":"summary","data":{}}`
	ignore := func(p string) bool { return strings.HasPrefix(p, "vendor/") }
	files, results := parseRgJSON([]byte(data), ignore)
	if len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("files = %v, want [a.go] (vendor filtered)", files)
	}
	hits := results[0].hits
	if len(hits) != 2 || hits[0].num != 1 || hits[0].match || hits[1].num != 2 || !hits[1].match {
		t.Errorf("hits = %+v, want context line 1 then match line 2", hits)
	}
	if hits[1].text != "func Foo() {}" {
		t.Errorf("match text = %q, want trimmed 'func Foo() {}'", hits[1].text)
	}
}

func TestParseRgFileList(t *testing.T) {
	data := "a.go\n./b.go\nvendor/x.go\na.go\n"
	files := parseRgFileList([]byte(data), func(p string) bool { return strings.HasPrefix(p, "vendor/") })
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Errorf("parseRgFileList = %v, want [a.go b.go] (deduped, cleaned, vendor filtered)", files)
	}
}

// TestRipgrepParityIfPresent runs the same search via ripgrep (MOONY_RG unset) and
// the Go scanner (MOONY_RG=0) and asserts identical output. When rg isn't
// installed (or can't be spawned), the rg path falls back to Go, so this still
// passes — it just doesn't exercise rg there.
func TestRipgrepParityIfPresent(t *testing.T) {
	files := map[string]string{
		"a.go":        "package a\nfunc Foo() {}\n// foo again\n",
		"sub/b.go":    "package b\nvar Foo = 1\n",
		"notes.txt":   "Foo in text\n",
		"vendor/v.go": "func Foo() {}\n",
	}
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
	g := &grepTool{r: NewRegistry(dir)}
	run := func(rg string, args map[string]any) string {
		t.Setenv("MOONY_RG", rg)
		in, _ := json.Marshal(args)
		out, err := g.Execute(context.Background(), in)
		if err != nil {
			t.Fatalf("grep (MOONY_RG=%q): %v", rg, err)
		}
		return out
	}
	for _, args := range []map[string]any{
		{"pattern": "Foo"},
		{"pattern": "foo", "ignore_case": true},
		{"pattern": "Foo", "include": "*.go"},
		{"pattern": "Foo", "files_only": true},
		{"pattern": "Foo", "context": 1},
	} {
		goOut := run("0", args) // Go scanner
		rgOut := run("", args)  // ripgrep when present, else falls back to Go
		if goOut != rgOut {
			t.Errorf("ripgrep/Go output differ for %v:\n--- rg ---\n%s\n--- go ---\n%s", args, rgOut, goOut)
		}
	}
}

// TestGrepTruncationFeedback: when results exceed the cap, the output must tell
// the model it was truncated and how many matched, so it can refine rather than
// assume it saw everything.
func TestGrepTruncationFeedback(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < maxGrepMatches+50; i++ {
		files["f"+strconv.Itoa(i)+".txt"] = "needle here\n"
	}
	g := grepReg(t, files)
	out := runGrep(t, g, map[string]any{"pattern": "needle"})
	tail := out
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation notice, got tail:\n%s", tail)
	}
	if !strings.Contains(out, "refine") {
		t.Errorf("truncation notice should advise refining, got tail:\n%s", tail)
	}
}

// TestSearchDedupCache verifies the cross-turn search cache: an identical repeat
// grep returns a compact stub, while a write through the registry (and an
// explicit ClearSearchCache, as compaction does) invalidate it so the search
// re-runs. grep, glob, and list_dir share the same lookup/store/invalidate path.
func TestSearchDedupCache(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 60; i++ {
		body.WriteString("needle on this line\n") // big enough to exceed searchCacheMinBytes
	}
	g := grepReg(t, map[string]string{"big.txt": body.String()})
	reg := g.r
	args := json.RawMessage(`{"pattern":"needle"}`)

	first, err := g.Execute(context.Background(), args)
	if err != nil || !strings.Contains(first, "big.txt:1:") {
		t.Fatalf("first grep = %q (err=%v); want matches", first, err)
	}
	if len(first) < searchCacheMinBytes {
		t.Fatalf("result too small (%d B) to exercise the cache", len(first))
	}

	// Identical repeat → dedup stub, not the body.
	second, _ := g.Execute(context.Background(), args)
	if strings.Contains(second, "big.txt:1:") || !strings.Contains(second, "identical") {
		t.Errorf("repeat grep should be a dedup stub, got:\n%s", second)
	}

	// A write through the same registry invalidates the search cache → re-runs.
	if _, err := (&writeTool{r: reg}).Execute(context.Background(), json.RawMessage(`{"path":"new.txt","content":"x\n"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, _ := g.Execute(context.Background(), args); !strings.Contains(out, "big.txt:1:") {
		t.Errorf("grep after a write should re-run with fresh results, got:\n%s", out)
	}

	// ClearSearchCache (what compaction calls) also forces a re-run.
	if out, _ := g.Execute(context.Background(), args); !strings.Contains(out, "identical") {
		t.Fatalf("expected the cache to repopulate after the write")
	}
	reg.ClearSearchCache()
	if out, _ := g.Execute(context.Background(), args); !strings.Contains(out, "big.txt:1:") {
		t.Errorf("grep after ClearSearchCache should re-run, got:\n%s", out)
	}
}

// TestBashInvalidatesSearchCache guards the bash ⇄ search-cache staleness gap: a
// bash command can change the tree in ways the structured write tools don't, so
// any bash run drops the cross-turn search cache — otherwise a repeat grep would
// return a stale "nothing changed" stub.
func TestBashInvalidatesSearchCache(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 60; i++ {
		body.WriteString("needle on this line\n")
	}
	g := grepReg(t, map[string]string{"big.txt": body.String()})
	reg := g.r
	args := json.RawMessage(`{"pattern":"needle"}`)

	if _, err := g.Execute(context.Background(), args); err != nil {
		t.Fatalf("prime grep: %v", err)
	}
	if out, _ := g.Execute(context.Background(), args); !strings.Contains(out, "identical") {
		t.Fatalf("grep should be cached before bash, got:\n%s", out)
	}

	// Any bash run drops the search cache (the result is irrelevant; the deferred
	// invalidation fires once a command is dispatched).
	bash := &bashTool{r: reg, maxOutputBytes: 1 << 20}
	_, _ = bash.Execute(context.Background(), json.RawMessage(`{"command":"echo hi"}`))

	if out, _ := g.Execute(context.Background(), args); !strings.Contains(out, "big.txt:1:") {
		t.Errorf("grep after bash should re-run (bash may have changed the tree), got:\n%s", out)
	}
}

// pruneStaleToolResults elides a search's result from history: the dropped
// search re-runs (its "already above" stub would now point at an elision
// marker), while an unrelated cached search is untouched. It also pins the key
// consistency with searchCacheStore — a byte-identical input must drop.
func TestDropSearchSurgical(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 60; i++ {
		body.WriteString("needle and haystack on this line\n")
	}
	g := grepReg(t, map[string]string{"big.txt": body.String()})
	reg := g.r
	needle := json.RawMessage(`{"pattern":"needle"}`)
	hay := json.RawMessage(`{"pattern":"haystack"}`)

	// Prime both searches into the cache (first call returns the body + stores).
	for _, a := range []json.RawMessage{needle, hay} {
		if out, err := g.Execute(context.Background(), a); err != nil || !strings.Contains(out, "big.txt:1:") {
			t.Fatalf("prime grep %s = %q (err=%v)", a, out, err)
		}
	}
	// Both are now cached → identical repeats are stubs.
	if out, _ := g.Execute(context.Background(), needle); !strings.Contains(out, "identical") {
		t.Fatalf("needle should be cached before drop, got:\n%s", out)
	}

	// Drop ONLY the needle search, exactly as prune does on elision.
	reg.DropSearch("grep", needle)

	if out, _ := g.Execute(context.Background(), needle); !strings.Contains(out, "big.txt:1:") {
		t.Errorf("dropped search should re-run with fresh results, got:\n%s", out)
	}
	if out, _ := g.Execute(context.Background(), hay); !strings.Contains(out, "identical") {
		t.Errorf("undropped search should still be a dedup stub, got:\n%s", out)
	}
}
