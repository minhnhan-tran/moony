package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symsFor(t *testing.T, dir string) map[string]SymbolEntry {
	t.Helper()
	sm := NewRegistry(dir).SymbolMap()
	out := map[string]SymbolEntry{}
	for _, e := range sm.Entries {
		out[e.Name] = e
	}
	return out
}

// JS `function foo()` must be indexed: defKeywordRe lists Go's `func`, which does
// NOT match `function`, so the web UI's JS was previously described only by whatever
// `const` locals happened to be swept up.
func TestSymbolMapIndexesJSFunctions(t *testing.T) {
	dir := t.TempDir()
	js := `function topLevel(a) { return a; }
const arrowFn = (x) => x + 1;
const asyncArrow = async () => 1;
const exprFn = function () { return 2; };
export function exportedFn() {}
export const exportedConst = 5;
async function asyncTop() {}
// Locals that are NOT functions must not be indexed as definitions:
const cancelBtn = el("button", "btn");
const nowStamp = Date.now();
let counterVal = 0;
`
	if err := os.WriteFile(filepath.Join(dir, "ui.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)

	for _, want := range []string{"topLevel", "arrowFn", "asyncArrow", "exprFn", "exportedFn", "exportedConst", "asyncTop"} {
		if _, ok := got[want]; !ok {
			t.Errorf("definition %q missing from the symbol map", want)
		}
	}
	// The point of narrowing assignFuncRe: value bindings are noise, not definitions.
	for _, unwanted := range []string{"cancelBtn", "nowStamp", "counterVal"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("non-function local %q was indexed as a definition", unwanted)
		}
	}
	// A JS function must carry a usable kind, or display and filtering break.
	if k := got["topLevel"].Kind; k != "func" {
		t.Errorf("kind for a JS function = %q, want \"func\"", k)
	}
	if k := got["asyncTop"].Kind; k != "func" {
		t.Errorf("kind for an async JS function = %q, want \"func\"", k)
	}
}

// CSS files need their own pass: they have no declaration keywords, so a stylesheet
// previously contributed zero symbols and was invisible to find_symbol/find_files.
func TestSymbolMapIndexesCSS(t *testing.T) {
	dir := t.TempDir()
	css := `:root {
  --c-brand: 96 165 250;
}
.job-log {
  color: rgb(var(--c-brand));
}
#stats {
  display: flex;
}
  .nested-indented { color: red; }
`
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)

	if e, ok := got["job-log"]; !ok {
		t.Error("class .job-log missing from the symbol map")
	} else if e.Kind != "class" {
		t.Errorf("kind for .job-log = %q, want \"class\"", e.Kind)
	}
	// '#' is a comment lead-in for shell/Python but an ID SELECTOR in CSS; treating it
	// as a comment silently dropped every id rule.
	if e, ok := got["stats"]; !ok {
		t.Error("id #stats missing — '#' was treated as a comment in CSS")
	} else if e.Kind != "id" {
		t.Errorf("kind for #stats = %q, want \"id\"", e.Kind)
	}
	if e, ok := got["--c-brand"]; !ok {
		t.Error("custom property --c-brand missing from the symbol map")
	} else if e.Kind != "var" {
		t.Errorf("kind for --c-brand = %q, want \"var\"", e.Kind)
	}
	// Only column-0 rules are landmarks; indented/nested ones would flood the map.
	if _, ok := got["nested-indented"]; ok {
		t.Error("indented selector was indexed; only top-level rules should be")
	}
}

// A JS/HTML file naming a CSS class REFERENCES it, so "what uses .job-log?" works
// the same way it does for functions.
func TestSymbolMapLinksCSSClassUsage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"),
		[]byte(".job-log { color: red; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui.js"),
		[]byte("function render() { const p = el(\"pre\", \"job-log\"); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)
	e, ok := got["job-log"]
	if !ok {
		t.Fatal("class .job-log missing from the symbol map")
	}
	if e.RefCount < 1 {
		t.Errorf("RefCount for .job-log = %d, want >=1 (referenced from ui.js)", e.RefCount)
	}
	if len(e.Callers) == 0 || !strings.Contains(strings.Join(e.Callers, ","), "ui.js") {
		t.Errorf("callers for .job-log = %v, want to include ui.js", e.Callers)
	}
}

// The global cap must not erase whole files. Flat files (stylesheets, big UI
// modules) hold many individually low-scoring names, so a plain score-ordered
// entries[:max] drops all of them and the map claims the file is empty.
func TestTruncateKeepingFileFloorNeverErasesAFile(t *testing.T) {
	var entries []SymbolEntry
	// hot.go scores high; cold.css is flat and scores ~0 across many names.
	for i := 0; i < 40; i++ {
		entries = append(entries, SymbolEntry{Name: "hot", File: "hot.go", Score: 100})
	}
	for i := 0; i < 40; i++ {
		entries = append(entries, SymbolEntry{Name: "cold", File: "cold.css", Score: 0.1})
	}
	out := truncateKeepingFileFloor(entries, 20)
	if len(out) != 20 {
		t.Fatalf("kept %d entries, want 20", len(out))
	}
	per := map[string]int{}
	for _, e := range out {
		per[e.File]++
	}
	if per["cold.css"] == 0 {
		t.Error("the low-scoring file was erased entirely; the per-file floor did not apply")
	}
	if per["hot.go"] == 0 {
		t.Error("the high-scoring file lost every entry")
	}
}

// The floor must shrink to fit the budget: a fixed floor inverts the function's
// purpose once files × floor exceeds max, spending everything in path order so files
// sorting late lose even their best definitions.
func TestTruncateKeepingFileFloorShrinksToFitBudget(t *testing.T) {
	var entries []SymbolEntry
	for f := 0; f < 50; f++ {
		for i := 0; i < 10; i++ {
			entries = append(entries, SymbolEntry{
				Name: "n", File: string(rune('a'+f%26)) + "-file", Score: float64(50 - f),
			})
		}
	}
	const max = 60
	out := truncateKeepingFileFloor(entries, max)
	if len(out) != max {
		t.Fatalf("kept %d, want %d", len(out), max)
	}
	// With 26 distinct files and a budget of 60, a fixed floor of 12 would consume the
	// whole budget on the first ~5 files. Every file should still appear.
	per := map[string]int{}
	for _, e := range out {
		per[e.File]++
	}
	if len(per) < 20 {
		t.Errorf("only %d of 26 files survived; the floor did not shrink to fit", len(per))
	}
}

// A JS module's map entry must describe its module-level API, not the local closures
// inside its functions. Those helpers are invisible outside their enclosing function,
// yet they scored like top-level definitions and dominated what the codebase map showed.
func TestSymbolMapPrunesInnerJSClosures(t *testing.T) {
	dir := t.TempDir()
	js := `function publicOne() {
  const flush = () => { pendingW = 0; };
  const tick = function () { return 1; };
  function innerHelper() { return 2; }
  if (x) {
    const deeplyNested = () => 3;
  }
}
const publicTwo = () => 4;
function publicThree() {}
`
	if err := os.WriteFile(filepath.Join(dir, "mod.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)

	for _, want := range []string{"publicOne", "publicTwo", "publicThree"} {
		if _, ok := got[want]; !ok {
			t.Errorf("module-level %q missing from the symbol map", want)
		}
	}
	for _, unwanted := range []string{"flush", "tick", "innerHelper", "deeplyNested"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("inner closure %q was indexed as a module-level definition", unwanted)
		}
	}
}

// The base level is DETECTED, not assumed to be column 0: an IIFE-wrapped module keeps
// its entire public surface at one indent, so a hardcoded column-0 rule would erase
// such a file from the map entirely.
func TestSymbolMapKeepsIIFEWrappedModuleSurface(t *testing.T) {
	dir := t.TempDir()
	js := `(function () {
  function wrappedPublic() {
    const innerLocal = () => 1;
    return innerLocal;
  }
  const alsoPublic = () => 2;
})();
`
	if err := os.WriteFile(filepath.Join(dir, "iife.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)

	for _, want := range []string{"wrappedPublic", "alsoPublic"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q missing: an IIFE-wrapped module was erased by depth pruning", want)
		}
	}
	if _, ok := got["innerLocal"]; ok {
		t.Error("a closure nested inside the IIFE's function was indexed")
	}
}

// Go nests functions too (closures, methods in blocks) but its top-level declarations
// dominate, so pruning must not apply and strip legitimate Go symbols.
func TestSymbolMapDoesNotPruneNestedGoDecls(t *testing.T) {
	dir := t.TempDir()
	code := `package p

func Outer() {
	inner := func() {}
	_ = inner
}

type Thing struct{}
`
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	got := symsFor(t, dir)
	// (Names under 3 chars are filtered as noise by symbolMinTokenLen, so use real ones.)
	for _, want := range []string{"Outer", "Thing"} {
		if _, ok := got[want]; !ok {
			t.Errorf("Go declaration %q was pruned; depth pruning must be JS/TS-only", want)
		}
	}
}
