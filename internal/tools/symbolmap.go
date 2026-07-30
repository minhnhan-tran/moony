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
	symbolMapMaxFiles     = 10000            // Codebuff DEFAULT_MAX_PARSE_FILES
	symbolMapMaxFileBytes = 1_000_000        // per-file cap (skip larger — likely generated/minified)
	symbolMapMaxTotal     = 500_000_000      // total bytes budget across all files
	symbolMaxCallers      = 25               // Codebuff MAX_CALLERS
	symbolMinTokenLen     = 3                // ignore 1-2 char names (i, id, x…) as noise
	symbolMapMaxLines     = 1500             // cap on total defs retained
	symbolDigestMaxLines  = 40               // defs surfaced in the system-prompt digest
	symbolMapRecheck      = 15 * time.Second // min interval between change-detection walks (hot-path throttle)
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
	".swift": true, ".scala": true,
}

// defKeywordRe matches a definition: a declaration keyword IMMEDIATELY followed
// by the defined identifier (optionally after a few modifier words for
// JVM/C-family, e.g. "public static Foo"). Anchoring to the token right after
// the keyword (rather than the last word before a brace) avoids capturing stray
// words from comments and multi-word lines. RE2, no backrefs.
var defKeywordRe = regexp.MustCompile(
	`\b(?:func|def|fn|class|interface|struct|type|enum|trait|impl|module|namespace|package)\s+(?:\([^)]*\)\s*)?([A-Z_][A-Za-z0-9_]*|[a-z_][A-Za-z0-9_]*)\b`)

// assignFuncRe matches JS/TS `const name = (…) =>` / `name = function` bindings
// and exported const/var declarations that name a value.
var assignFuncRe = regexp.MustCompile(
	`\b(?:const|let|var|export(?:\s+default)?)\s+([A-Za-z_][A-Za-z0-9_]*)\s*[:=]`)

// callRe matches a call site: an identifier immediately followed by '('.
var callRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\(`)

// commentPrefix quickly rejects obvious comment/blank lines before regexing.
func skippableLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") ||
		strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
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
	_ = filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, e := filepath.Rel(r.root, path)
		if e != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if r.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !symbolLangExt[strings.ToLower(filepath.Ext(rel))] || r.ignore.Match(rel, false) {
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

	_ = filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, e := filepath.Rel(r.root, path)
		if e != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if r.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !symbolLangExt[strings.ToLower(filepath.Ext(rel))] || r.ignore.Match(rel, false) {
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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		fsStat.lines++
		if skippableLine(line) {
			continue
		}
		// Definitions: keyword-led, or JS/TS binding.
		for _, m := range defKeywordRe.FindAllStringSubmatch(line, -1) {
			if addDef(defSet, m[1]) {
				recordSite(fsStat.sites, m[1], declKeyword(m[0]), fsStat.lines)
			}
		}
		for _, m := range assignFuncRe.FindAllStringSubmatch(line, -1) {
			if addDef(defSet, m[1]) {
				recordSite(fsStat.sites, m[1], declKeyword(m[0]), fsStat.lines)
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
func recordSite(sites map[string]defSite, name, kind string, line int) {
	if _, seen := sites[name]; !seen {
		sites[name] = defSite{kind: kind, line: line}
	}
}

// declKeyword pulls the leading declaration keyword out of a matched def, so
// "func (a *Agent) Run" -> "func" and "export const x =" -> "const".
func declKeyword(match string) string {
	for _, f := range strings.Fields(match) {
		switch f {
		case "func", "fn", "def":
			return "func"
		case "type", "struct", "interface", "enum", "trait", "class", "impl",
			"module", "namespace", "const", "let", "var", "package":
			return f
		case "export", "default", "public", "private", "protected", "static", "final":
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
	if len(entries) > symbolMapMaxLines {
		entries = entries[:symbolMapMaxLines]
	}

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
