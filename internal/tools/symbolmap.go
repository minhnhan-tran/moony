package tools

// symbolmap.go builds a compact, scored symbol + caller map of the repository —
// a lightweight port of Codebuff's tree-sitter "code map": for every source file
// we extract only two things, definitions (function/type/class/method/exported
// const names) and call sites (foo(), obj.method(), new X()), then score each
// definition and link it to the files that call it. This gives the agent (and
// the file-picker) a cheap, whole-repo orientation without embeddings, an LLM
// call, or full parsing.
//
// The extractor is pure-Go/regex (no CGO tree-sitter): it keeps cross-compiles
// (darwin arm64/amd64 + the linux relay) and the private GOPROXY clean, and
// reuses the same definition vocabulary as find_symbol. A tree-sitter backend
// could later drop in behind buildSymbolMap without changing callers.
//
// Scoring mirrors Codebuff (parse.ts):
//   tokenBaseScore = 0.8^depth * sqrt(numLines / (numIdentifiers + 1))
//   score         *= 1 + ln(1 + timesCalledFromOtherFiles)   // popularity boost
// A token's defining file is the highest-scoring file that defines it; callers
// are the other files that reference the name (capped at symbolMaxCallers).

import (
	"bufio"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	symbolMapMaxFiles     = 10000       // Codebuff DEFAULT_MAX_PARSE_FILES
	symbolMapMaxFileBytes = 1_000_000   // per-file cap (skip larger — likely generated/minified)
	symbolMapMaxTotal     = 500_000_000 // total bytes budget across all files
	symbolMaxCallers      = 25          // Codebuff MAX_CALLERS
	symbolMinTokenLen     = 3           // ignore 1-2 char names (i, id, x…) as noise
	symbolMapMaxLines     = 6000        // cap on total defs retained (see note below)
	// symbolMapMaxLines must stay comfortably above (contributing files ×
	// symbolFileFloor) or the floor pass alone exhausts the budget and whole
	// directories get nothing: at the previous 1500, every file sorting late in path
	// order — including all of internal/web/static — was dropped from the map
	// entirely. Only CODEBASE_MAP.md on disk and the find_files ranking read the full
	// set; the system prompt takes just symbolDigestMaxLines from the top, so a larger
	// cap costs disk, not context.
	// symbolFileFloor is how many definitions each contributing file keeps before the
	// remaining budget is handed out by score, so no file is erased outright (see
	// truncateKeepingFileFloor). 12 rather than a handful because the floor is what
	// makes a *flat* file usable: the value of a stylesheet or a large UI module is the
	// breadth of names you can look up, and those score low individually (a CSS class
	// has no callers in the identifier sense), so score alone retains almost none.
	symbolFileFloor      = 12
	symbolDigestMaxLines = 40               // defs surfaced in the system-prompt digest
	symbolMapRecheck     = 15 * time.Second // min interval between change-detection walks (hot-path throttle)
)

// ignoreTokens are ubiquitous names whose "definition" carries no locating
// value (mirrors Codebuff's IGNORE_TOKENS), so they neither seed defs nor count
// as meaningful calls.
var ignoreTokens = map[string]bool{
	"__init__": true, "__post_init__": true, "__call__": true, "constructor": true,
	"main": true, "init": true, "String": true, "Error": true, "error": true,
	"new": true, "func": true, "return": true, "if": true, "for": true, "map": true,
}

// symbolLangExt maps a file extension to whether we extract symbols from it.
// Kept aligned with the languages Codebuff supports.
var symbolLangExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true,
	".py": true, ".java": true, ".rb": true, ".rs": true, ".c": true, ".h": true,
	".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".php": true, ".kt": true,
	".swift": true, ".scala": true, ".css": true,
}

// defKeywordRe matches a definition: a declaration keyword IMMEDIATELY followed
// by the defined identifier (optionally after a few modifier words for
// JVM/C-family, e.g. "public static Foo"). Anchoring to the token right after
// the keyword (rather than the last word before a brace) avoids capturing stray
// words from comments and multi-word lines. RE2, no backrefs.
// CSS has no declaration keywords, so it needs its own extraction pass: its
// "definitions" are the rules a stylesheet exposes (`.job-log`, `#stats`) and the
// custom properties it defines (`--c-brand`). Without this, a large stylesheet like
// internal/web/static/app.css contributed ZERO symbols and was invisible to
// find_files / find_symbol / the codebase map, even though it's where all the
// styling lives.

// cssSelectorRe matches a rule that starts at column 0 — i.e. a top-level component
// selector rather than a nested/indented one — and captures its first class or id
// name. Anchoring to column 0 keeps the output to the file's structural landmarks
// instead of every descendant combinator and pseudo-variant.
var cssSelectorRe = regexp.MustCompile(`^([.#][A-Za-z_][A-Za-z0-9_-]*)`)

// cssVarRe matches a custom-property definition (`--c-brand: 96 165 250;`). These are
// the theming contract, so they're worth finding by name.
var cssVarRe = regexp.MustCompile(`^\s*(--[A-Za-z][A-Za-z0-9_-]*)\s*:`)

// cssVarUseRe matches a custom-property REFERENCE (`var(--c-brand)`), so the caller
// graph can answer "what uses this token?" the way it does for functions.
var cssVarUseRe = regexp.MustCompile(`var\(\s*(--[A-Za-z][A-Za-z0-9_-]*)`)

type cssDef struct {
	name string
	kind string
}

// extractCSSDefs pulls definitions and references out of one CSS line. Returns the
// names defined on this line (with their kind) and the tokens it references.
func extractCSSDefs(line string) (defs []cssDef, uses []string) {
	if m := cssSelectorRe.FindStringSubmatch(line); m != nil {
		kind := "class"
		if strings.HasPrefix(m[1], "#") {
			kind = "id"
		}
		// Store the bare name so it reads like any other symbol; the kind carries
		// whether it was a class or an id.
		defs = append(defs, cssDef{name: strings.TrimLeft(m[1], ".#"), kind: kind})
	}
	if m := cssVarRe.FindStringSubmatch(line); m != nil {
		defs = append(defs, cssDef{name: m[1], kind: "var"})
	}
	for _, m := range cssVarUseRe.FindAllStringSubmatch(line, -1) {
		uses = append(uses, m[1])
	}
	return defs, uses
}

// isCSSRefCandidate reports whether a file is one that references CSS class names
// (JS/TS building DOM, or HTML markup).
func isCSSRefCandidate(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".js", ".jsx", ".mjs", ".ts", ".tsx", ".html":
		return true
	}
	return false
}

// cssClassAttrRe matches class names in markup: class="a b" / className="a b".
var cssClassAttrRe = regexp.MustCompile(`class(?:Name)?\s*=\s*["']([^"']+)["']`)

// cssClassCallRe matches the DOM APIs that take a bare class name or selector.
var cssClassCallRe = regexp.MustCompile(`(?:classList\.(?:add|remove|toggle|contains)|querySelector(?:All)?|closest|matches)\(\s*["']([^"']+)["']`)

// cssElHelperRe matches this codebase's DOM builder, el(tag, classes, text) — the way
// nearly every element in the web UI gets its classes, e.g. el("pre", "job-log").
var cssElHelperRe = regexp.MustCompile(`\bel\(\s*["'][A-Za-z][A-Za-z0-9]*["']\s*,\s*["']([^"']+)["']`)

// cssClassNameAssignRe matches `x.className = "a b"` / `.className += " a"`.
var cssClassNameAssignRe = regexp.MustCompile(`\.className\s*\+?=\s*["']([^"']+)["']`)

// cssClassRefs pulls candidate CSS class names out of one line of JS/HTML.
func cssClassRefs(line string) []string {
	var out []string
	add := func(blob string) {
		for _, tok := range strings.FieldsFunc(blob, func(r rune) bool {
			// Split a class list / selector into bare names: ".a.b > .c" -> a, b, c.
			return r == ' ' || r == '.' || r == '#' || r == '>' || r == ',' || r == ':' ||
				r == '[' || r == ']' || r == '+' || r == '~' || r == '\t'
		}) {
			if len(tok) >= symbolMinTokenLen && !ignoreTokens[tok] && !isKeyword(tok) {
				out = append(out, tok)
			}
		}
	}
	for _, re := range []*regexp.Regexp{cssClassAttrRe, cssClassCallRe, cssElHelperRe, cssClassNameAssignRe} {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
	}
	return out
}

// `function` is listed alongside `func`: `func` is Go's keyword and does NOT match
// JavaScript's `function foo() {}`, so without it every top-level function in the
// web UI's JS went unindexed and the map described those files purely by whatever
// `const` locals assignFuncRe happened to catch.
var defKeywordRe = regexp.MustCompile(
	`\b(?:func|function|def|fn|class|interface|struct|type|enum|trait|impl|module|namespace|package)\s+(?:\([^)]*\)\s*)?([A-Z_][A-Za-z0-9_]*|[a-z_][A-Za-z0-9_]*)\b`)

// assignFuncRe matches JS/TS bindings whose VALUE is a function: arrow functions
// (`const f = (a) =>`, `const f = a =>`, `const f = async () =>`) and function
// expressions (`const f = function`). It deliberately does NOT match arbitrary value
// bindings: it used to end at `\s*[:=]`, which indexed every local in the file
// (`const cancel = el("button")`, `const now = Date.now()`) as though it were a
// top-level definition, burying the real entry points under throwaway names.
var assignFuncRe = regexp.MustCompile(
	`\b(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s+)?(?:function\b|(?:\([^)]*\)|[A-Za-z_$][A-Za-z0-9_$]*)\s*=>)`)

// exportRe indexes anything a module explicitly exports by name, function or not:
// an export is part of the file's public surface regardless of the value's type.
var exportRe = regexp.MustCompile(
	`\bexport\s+(?:default\s+)?(?:async\s+)?(?:function|class|const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)

// callRe matches a call site: an identifier immediately followed by '('.
var callRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\(`)

// skippableLine reports whether a line is a comment or blank and can be ignored.
// isCSS matters: `#` starts a comment in Python/shell but an ID SELECTOR in CSS, so
// treating it as a comment silently dropped every `#stats`-style rule; likewise `*`
// begins a universal selector, not just a doc-comment continuation.
func skippableLine(s string, isCSS bool) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") {
		return true
	}
	if isCSS {
		return false
	}
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*")
}

// symDef is one definition occurrence in a file.
type symDef struct {
	name string
	file string // repo-relative slash path
}

// defSite records where and how a name was defined, so the map can report the
// symbol's kind (func/type/class/…) and line instead of a bare name.
type defSite struct {
	kind string // declaration keyword, normalized ("func", "type", "class", "const", …)
	line int    // 1-based line of the declaration
	// indent is the leading-whitespace width of the declaring line. Brace languages
	// nest freely, so this is how a module's public surface is told apart from the
	// closures inside its functions — see pruneNestedDefs.
	indent int
}

// fileStat holds per-file extraction results used for scoring.
type fileStat struct {
	path     string
	lines    int
	numIdent int
	defs     []string        // distinct definition names in this file
	calls    map[string]bool // distinct names this file calls
	sites    map[string]defSite
}

// SymbolEntry is a scored definition surfaced to consumers (RepoMap, find_files).
type SymbolEntry struct {
	Name    string
	File    string   // repo-relative slash path of the defining file
	Score   float64  // higher = more relevant/important
	Callers []string // up to symbolMaxCallers files that reference this symbol
	Kind    string   // declaration keyword ("func", "type", …); "" when unknown
	Line    int      // 1-based declaration line; 0 when unknown
	// RefCount is the TRUE number of cross-file references, which can exceed
	// len(Callers) because that list is capped at symbolMaxCallers. Use this for
	// ranking and for rendering "×N+" so a saturated count stays informative.
	RefCount int
}

// CallersTruncated reports whether the Callers list was capped, i.e. RefCount
// understates how many distinct files were seen.
func (e SymbolEntry) CallersTruncated() bool { return len(e.Callers) >= symbolMaxCallers }

// SymbolMap is the built map: entries sorted by score (desc), plus the
// generation it was built at (for mtime-based invalidation).
type SymbolMap struct {
	Entries    []SymbolEntry
	byFile     map[string][]int // repo-rel file -> indices into Entries defining a symbol there
	generation int64
}

// SymbolMap returns the memoized scored symbol map, rebuilding it synchronously
// only when it has never been built; otherwise it returns the cached map and
// refreshes it in the background when the repo has changed (see
// symbolMapLocked). Callers that must not block (the system-prompt builder)
// should use SymbolMapCached instead, which never triggers a synchronous build.
func (r *Registry) SymbolMap() *SymbolMap {
	r.symbolMapMu.Lock()
	if r.symbolMap != nil {
		m := r.symbolMap
		r.maybeRefreshLocked()
		r.symbolMapMu.Unlock()
		return m
	}
	// Cold: build synchronously (first explicit consumer, e.g. find_files).
	r.symbolMapMu.Unlock()
	return r.buildAndStore()
}

// SymbolMapCached returns the current cached map WITHOUT ever building
// synchronously. If the map hasn't been built yet, it kicks off a one-shot
// background build and returns nil, so startup / prompt assembly never blocks on
// the full-repo walk+parse. Subsequent calls return the map once it's ready.
func (r *Registry) SymbolMapCached() *SymbolMap {
	r.symbolMapMu.Lock()
	defer r.symbolMapMu.Unlock()
	if r.symbolMap != nil {
		r.maybeRefreshLocked()
		return r.symbolMap
	}
	r.startBuildLocked()
	return nil
}

// maybeRefreshLocked, called with symbolMapMu held, triggers a background
// rebuild if enough time has passed since the last generation check AND the repo
// has changed. The generation walk is throttled (symbolMapRecheck) so a hot map
// costs nothing on the common path. Never blocks the caller.
func (r *Registry) maybeRefreshLocked() {
	if r.symbolBuilding {
		return
	}
	if time.Since(r.symbolChecked) < symbolMapRecheck {
		return
	}
	r.symbolChecked = time.Now()
	// The generation walk itself is cheap relative to a full parse, but still do
	// it off the caller's goroutine to keep prompt assembly instant.
	cur := r.symbolMap
	r.symbolBuilding = true
	go func() {
		gen := r.repoGeneration()
		if cur != nil && cur.generation == gen {
			r.symbolMapMu.Lock()
			r.symbolBuilding = false
			r.symbolMapMu.Unlock()
			return
		}
		m := r.buildSymbolMap()
		m.generation = gen
		r.symbolMapMu.Lock()
		r.symbolMap = m
		r.symbolBuilding = false
		hook := r.onSymbolMapRebuilt
		r.symbolMapMu.Unlock()
		if hook != nil {
			hook(r)
		}
	}()
}

// startBuildLocked, called with symbolMapMu held, launches a single background
// build when none is cached and none is in flight.
func (r *Registry) startBuildLocked() {
	if r.symbolBuilding {
		return
	}
	r.symbolBuilding = true
	r.symbolChecked = time.Now()
	go func() {
		gen := r.repoGeneration()
		m := r.buildSymbolMap()
		m.generation = gen
		r.symbolMapMu.Lock()
		r.symbolMap = m
		r.symbolBuilding = false
		hook := r.onSymbolMapRebuilt
		r.symbolMapMu.Unlock()
		if hook != nil {
			hook(r)
		}
	}()
}

// buildAndStore builds synchronously and caches the result (used by the cold
// path of the blocking SymbolMap()).
func (r *Registry) buildAndStore() *SymbolMap {
	gen := r.repoGeneration()
	m := r.buildSymbolMap()
	m.generation = gen
	r.symbolMapMu.Lock()
	r.symbolMap = m
	r.symbolChecked = time.Now()
	r.symbolMapMu.Unlock()
	return m
}

// repoGeneration is a cheap change signal: the max mtime (unix nanos) of source
// files under root, XORed with the count. It changes when any file is added,
// removed, or edited, so a stale SymbolMap rebuilds without a filesystem watcher.
func (r *Registry) repoGeneration() int64 {
	var latest int64
	var n int64
	// One guarded read of the root + ignore set per walk rather than per file
	// (see repoMap): cheaper, and consistent if /cd re-roots midway.
	root, ig := r.Root(), r.ignoreSet()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if ig.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !symbolLangExt[strings.ToLower(filepath.Ext(rel))] || ig.Match(rel, false) {
			return nil
		}
		n++
		if info, ie := d.Info(); ie == nil {
			if mt := info.ModTime().UnixNano(); mt > latest {
				latest = mt
			}
		}
		return nil
	})
	return latest ^ (n * 2654435761) // knuth multiplicative mix so count changes register
}

// buildSymbolMap does the ignore-aware walk, extracts defs/calls, scores, and
// builds the caller graph. Pure local work; no model calls.
func (r *Registry) buildSymbolMap() *SymbolMap {
	var stats []fileStat
	var totalBytes int64
	files := 0

	// One guarded read of the root + ignore set per walk rather than per file
	// (see repoMap): cheaper, and consistent if /cd re-roots midway.
	root, ig := r.Root(), r.ignoreSet()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if ig.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !symbolLangExt[strings.ToLower(filepath.Ext(rel))] || ig.Match(rel, false) {
			return nil
		}
		if files >= symbolMapMaxFiles || totalBytes >= symbolMapMaxTotal {
			return filepath.SkipAll
		}
		info, ie := d.Info()
		if ie != nil || info.Size() > symbolMapMaxFileBytes {
			return nil
		}
		fsStat, ok := extractFile(path, rel)
		if !ok {
			return nil
		}
		files++
		totalBytes += info.Size()
		stats = append(stats, fsStat)
		return nil
	})

	return scoreAndLink(stats)
}

// extractFile reads a file and pulls out its definitions and call sites.
func extractFile(abs, rel string) (fileStat, bool) {
	f, err := os.Open(abs)
	if err != nil {
		return fileStat{}, false
	}
	defer f.Close()

	fsStat := fileStat{path: rel, calls: map[string]bool{}}
	defSet := map[string]bool{}
	fsStat.sites = map[string]defSite{}
	isCSS := strings.EqualFold(filepath.Ext(rel), ".css")
	cssRefs := isCSSRefCandidate(rel)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		fsStat.lines++
		if skippableLine(line, isCSS) {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if isCSS {
			// CSS has no declaration keywords; its definitions are rules and custom
			// properties, so it gets a dedicated pass instead of the keyword regexes.
			defs, uses := extractCSSDefs(line)
			for _, d := range defs {
				if addDef(defSet, d.name) {
					recordSite(fsStat.sites, d.name, d.kind, fsStat.lines, indent)
				}
			}
			for _, u := range uses {
				fsStat.calls[u] = true
			}
			continue
		}
		// Definitions: keyword-led, JS/TS function binding, or a named export.
		for _, m := range defKeywordRe.FindAllStringSubmatch(line, -1) {
			if addDef(defSet, m[1]) {
				recordSite(fsStat.sites, m[1], declKeyword(m[0]), fsStat.lines, indent)
			}
		}
		for _, m := range assignFuncRe.FindAllStringSubmatch(line, -1) {
			if addDef(defSet, m[1]) {
				recordSite(fsStat.sites, m[1], declKeyword(m[0]), fsStat.lines, indent)
			}
		}
		for _, m := range exportRe.FindAllStringSubmatch(line, -1) {
			if addDef(defSet, m[1]) {
				recordSite(fsStat.sites, m[1], declKeyword(m[0]), fsStat.lines, indent)
			}
		}
		// A JS/HTML file that names a CSS class REFERENCES it, so the caller graph
		// can answer "what uses .job-log?" the way it does for functions.
		if cssRefs {
			for _, c := range cssClassRefs(line) {
				fsStat.calls[c] = true
			}
		}
		// Call sites — only if the line actually contains '(' (cheap guard before
		// the regex), which most def-only lines don't.
		if strings.IndexByte(line, '(') >= 0 {
			for _, m := range callRe.FindAllStringSubmatch(line, -1) {
				name := m[1]
				if len(name) >= symbolMinTokenLen && !ignoreTokens[name] && !isKeyword(name) {
					fsStat.calls[name] = true
				}
			}
		}
	}
	if isNestingLang(rel) {
		pruneNestedDefs(defSet, fsStat.sites)
	}
	for name := range defSet {
		fsStat.defs = append(fsStat.defs, name)
	}
	fsStat.numIdent = len(defSet)
	sort.Strings(fsStat.defs) // deterministic
	return fsStat, true
}

func addDef(set map[string]bool, name string) bool {
	if len(name) >= symbolMinTokenLen && !ignoreTokens[name] && !isKeyword(name) {
		set[name] = true
		return true
	}
	return false
}

// recordSite remembers the FIRST declaration site seen for a name in a file
// (later re-declarations, e.g. methods on other types, don't overwrite it).
func recordSite(sites map[string]defSite, name, kind string, line, indent int) {
	if _, seen := sites[name]; !seen {
		sites[name] = defSite{kind: kind, line: line, indent: indent}
	}
}

// declKeyword pulls the leading declaration keyword out of a matched def, so
// "func (a *Agent) Run" -> "func" and "export const x =" -> "const".
func declKeyword(match string) string {
	for _, f := range strings.Fields(match) {
		switch f {
		case "func", "function", "fn", "def":
			// JS's `function` normalizes to "func" like Go/Rust/Python: callers filter
			// and display on this kind, and without the case every JS function got an
			// empty kind.
			return "func"
		case "type", "struct", "interface", "enum", "trait", "class", "impl",
			"module", "namespace", "const", "let", "var", "package":
			return f
		case "export", "default", "async", "public", "private", "protected", "static", "final":
			continue // modifier — keep scanning for the real keyword
		}
	}
	return ""
}

// langKeywords are control-flow / builtin words that syntactically look like a
// call ("if (", "return (") or a def token but never name a real symbol.
var langKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true,
	"else": true, "case": true, "catch": true, "defer": true, "go": true,
	"select": true, "range": true, "func": true, "def": true, "class": true,
	"struct": true, "interface": true, "type": true, "enum": true, "import": true,
	"package": true, "var": true, "const": true, "let": true, "async": true,
	"await": true, "throw": true, "try": true, "with": true, "print": true,
	"assert": true, "typeof": true, "sizeof": true, "and": true, "not": true,
	"the": true, "this": true, "self": true, "super": true, "true": true,
	"false": true, "null": true, "nil": true, "none": true, "void": true,
}

func isKeyword(name string) bool { return langKeywords[name] || goBuiltins[name] }

// goBuiltins are Go predeclared identifiers that appear as calls/conversions
// (string(x), len(x), make(...)) or builtin type names — never a locating symbol.
var goBuiltins = map[string]bool{
	"string": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true, "byte": true, "rune": true, "float32": true,
	"float64": true, "complex64": true, "complex128": true, "bool": true,
	"len": true, "cap": true, "make": true, "append": true, "copy": true,
	"delete": true, "close": true, "panic": true, "recover": true, "min": true,
	"max": true, "any": true, "comparable": true,
}

// scoreAndLink applies the Codebuff scoring + builds the caller graph, returning
// a SymbolMap sorted by score (desc), then name/file for determinism.
func scoreAndLink(stats []fileStat) *SymbolMap {
	// Per-file declaration sites (kind + line), so entries can report them.
	sitesOf := make(map[string]map[string]defSite, len(stats))
	for _, s := range stats {
		sitesOf[s.path] = s.sites
	}

	// Per-file base score (Codebuff tokenBaseScore, computed once per file).
	fileScore := make(map[string]float64, len(stats))
	for _, s := range stats {
		depth := strings.Count(s.path, "/")
		fileScore[s.path] = math.Pow(0.8, float64(depth)) *
			math.Sqrt(float64(s.lines)/float64(s.numIdent+1))
	}

	// A token's defining file = the highest-scoring file that defines it.
	type defPick struct {
		file  string
		score float64
	}
	defOf := map[string]defPick{}
	for _, s := range stats {
		for _, name := range s.defs {
			if cur, ok := defOf[name]; !ok || fileScore[s.path] > cur.score {
				defOf[name] = defPick{file: s.path, score: fileScore[s.path]}
			}
		}
	}

	// Caller graph: for each token, the other files that call it. Also count
	// external calls for the popularity boost.
	callers := map[string]map[string]bool{}
	extCalls := map[string]int{}
	for _, s := range stats {
		for name := range s.calls {
			def, ok := defOf[name]
			if !ok || def.file == s.path {
				continue // undefined-in-repo, or self-call — no cross-file signal
			}
			if callers[name] == nil {
				callers[name] = map[string]bool{}
			}
			if len(callers[name]) < symbolMaxCallers {
				callers[name][s.path] = true
			}
			extCalls[name]++
		}
	}

	entries := make([]SymbolEntry, 0, len(defOf))
	for name, dp := range defOf {
		score := dp.score * (1 + math.Log(1+float64(extCalls[name])))
		cl := make([]string, 0, len(callers[name]))
		for c := range callers[name] {
			cl = append(cl, c)
		}
		sort.Strings(cl)
		entries = append(entries, SymbolEntry{
			Name: name, File: dp.file, Score: score, Callers: cl,
			Kind: sitesOf[dp.file][name].kind, Line: sitesOf[dp.file][name].line,
			RefCount: extCalls[name],
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		if entries[i].File != entries[j].File {
			return entries[i].File < entries[j].File
		}
		return entries[i].Name < entries[j].Name
	})
	entries = truncateKeepingFileFloor(entries, symbolMapMaxLines)

	byFile := map[string][]int{}
	for i, e := range entries {
		byFile[e.File] = append(byFile[e.File], i)
	}
	return &SymbolMap{Entries: entries, byFile: byFile}
}

// SymbolDigest renders a compact, bounded "Key symbols" section for the system
// prompt: the highest-scored definitions across the repo, each with its file and
// (when notable) how many other files reference it. Returns "" when the map is
// empty or symbol surfacing is disabled. Bounded to symbolDigestMaxLines so it
// never bloats the prompt.
func (r *Registry) SymbolDigest() string {
	if !r.symbolDigestEnabled {
		return ""
	}
	m := r.SymbolMapCached()
	if m == nil || len(m.Entries) == 0 {
		return "" // not built yet — a background build is underway; fills in later
	}
	var b strings.Builder
	b.WriteString("\nKey symbols (highest-signal definitions; name — file [× callers]):\n")
	n := 0
	for _, e := range m.Entries {
		if n >= symbolDigestMaxLines {
			break
		}
		fmt.Fprintf(&b, "  %s — %s", e.Name, e.File)
		if len(e.Callers) > 1 {
			fmt.Fprintf(&b, " [×%d]", len(e.Callers))
		}
		b.WriteByte('\n')
		n++
	}
	b.WriteString("Use find_files/find_symbol to locate more; this is a ranked sample, not exhaustive.\n")
	return b.String()
}

// truncateKeepingFileFloor trims entries to max, but guarantees each contributing
// file keeps its best few definitions before the remainder is handed out by score.
//
// A plain entries[:max] is score-ordered, so it silently erases entire files: flat
// files (a stylesheet, a big UI module) have many individually low-scoring names — a
// CSS class has no "callers" in the identifier sense — so a global cut drops all of
// them and the map claims those files contain nothing.
func truncateKeepingFileFloor(entries []SymbolEntry, max int) []SymbolEntry {
	if len(entries) <= max {
		return entries
	}
	// The floor has to fit the budget. A fixed floor silently inverts this function's
	// purpose once (files × floor) exceeds max: the first pass spends everything in
	// path order and files sorting late lose even their best definitions. Derive it
	// from the actual file count, spending at most half the budget on the floor so the
	// rest still goes to genuinely high-scoring symbols.
	nFiles := 0
	seen := make(map[string]bool, 512)
	for _, e := range entries {
		if !seen[e.File] {
			seen[e.File] = true
			nFiles++
		}
	}
	floor := symbolFileFloor
	if nFiles > 0 {
		if budget := max / 2 / nFiles; budget < floor {
			floor = budget
		}
	}
	if floor < 1 {
		floor = 1 // never erase a file outright; one name is still a way in
	}
	keep := make([]bool, len(entries))
	perFile := map[string]int{}
	kept := 0
	// Pass 1: the floor. entries is already score-sorted, so the first N seen for a
	// file are its best.
	for i, e := range entries {
		if kept >= max {
			break
		}
		if perFile[e.File] < floor {
			keep[i] = true
			perFile[e.File]++
			kept++
		}
	}
	// Pass 2: spend what's left strictly by score.
	for i := range entries {
		if kept >= max {
			break
		}
		if !keep[i] {
			keep[i] = true
			kept++
		}
	}
	out := make([]SymbolEntry, 0, kept)
	for i, e := range entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

// isNestingLang reports whether a file's language routinely declares functions
// INSIDE other functions, making declaration depth meaningful. Go and Rust nest too,
// but their top-level declarations dominate, whereas a JS/TS module is mostly
// callbacks and closures — so the pruning below is limited to those.
func isNestingLang(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return true
	}
	return false
}

// pruneNestedDefs drops definitions nested inside other functions, keeping the file's
// module-level surface.
//
// Without it a large JS module is described by its inner closures: app-core.js has 211
// declarations at its base level, but also 40 nested ones with names like `flush`,
// `commit`, `mutate` and `tick`. Those are local helpers — invisible outside their
// enclosing function — yet they scored like top-level API and were what the codebase
// map showed for the file, so the map advertised private locals as entry points.
//
// The base level is DETECTED rather than assumed to be column 0: an IIFE-wrapped module
// (`(function () { … })()`, as app-workflow.js uses) has its entire public surface at
// one indent level, and hardcoding column 0 would erase such a file from the map
// completely. Everything at the shallowest observed depth is kept; anything deeper is
// dropped, unless that would leave nothing.
func pruneNestedDefs(defSet map[string]bool, sites map[string]defSite) {
	base := -1
	for name := range defSet {
		st, ok := sites[name]
		if !ok {
			continue // no recorded site (e.g. a call-only name); leave it alone
		}
		if base < 0 || st.indent < base {
			base = st.indent
		}
	}
	if base < 0 {
		return
	}
	for name := range defSet {
		st, ok := sites[name]
		if !ok {
			continue
		}
		if st.indent > base {
			delete(defSet, name)
			delete(sites, name)
		}
	}
}
