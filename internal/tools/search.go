package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// lsTool lists the entries of a directory.
type lsTool struct{ r *Registry }

func (t *lsTool) Name() string { return "list_dir" }

func (t *lsTool) Description() string {
	return "List the files and subdirectories of a directory (non-recursive). " +
		"Directories are suffixed with '/'. Defaults to the working directory. " +
		"Entries listed in .gitignore/.sqeignore are skipped."
}

func (t *lsTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory to list. Optional; defaults to '.'."}
		}
	}`)
}

func (t *lsTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		in.Path = "."
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	out, err := t.r.dirListing(abs)
	if err != nil {
		return "", fmt.Errorf("list %s: %v", in.Path, err)
	}
	t.r.searchCacheStore(t.Name(), input, out)
	return out, nil
}

// dirListing returns the directory's entries (one per line, trailing "/" on
// subdirectories), filtered by the active ignore rules with a hidden-count
// footer. Shared by list_dir and read_file (so pointing read_file at a
// directory yields a useful listing instead of an opaque "is a directory"
// error).
func (r *Registry) dirListing(abs string) (string, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	var visible []string
	skipped := 0
	for _, e := range entries {
		full := filepath.Join(abs, e.Name())
		rel, _ := filepath.Rel(r.root, full)
		if r.ignore.Match(rel, e.IsDir()) {
			skipped++
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		visible = append(visible, name)
	}
	if len(visible) == 0 && skipped == 0 {
		return "(empty directory)", nil
	}
	out := strings.Join(visible, "\n")
	if skipped > 0 {
		out += fmt.Sprintf("\n(%d entries hidden by ignore rules)", skipped)
	}
	return out, nil
}

// globTool finds files matching a glob pattern (supports ** for any depth).
type globTool struct{ r *Registry }

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return "Find files whose path matches a glob pattern. Supports '*' (any " +
		"chars except '/'), '?' (single char), and '**' (any depth). " +
		"Examples: '*.go', 'internal/**/*.go'. Returns up to 200 paths."
}

func (t *globTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob pattern to match against paths relative to the working directory."}
		},
		"required": ["pattern"]
	}`)
}

func (t *globTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	re, err := globToRegexp(in.Pattern)
	if err != nil {
		return "", err
	}

	const max = 200
	var matches []string
	walkErr := filepath.WalkDir(t.r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(t.r.root, path)
		rel = filepath.ToSlash(rel) // glob matching + tool output use '/' on every OS
		if d.IsDir() {
			if path != t.r.root && t.r.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.r.ignore.Match(rel, false) {
			return nil
		}
		if re.MatchString(rel) {
			matches = append(matches, rel)
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if len(matches) == 0 {
		return "(no files matched)", nil
	}
	sort.Strings(matches)
	total := len(matches)
	truncated := false
	if len(matches) > max {
		matches = matches[:max]
		truncated = true
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n... showing %d of %d matches (truncated); use a more specific pattern to see the rest", max, total)
	}
	t.r.searchCacheStore(t.Name(), input, out)
	return out, nil
}

// grepTool searches file contents using a regular expression.
type grepTool struct{ r *Registry }

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return "Search file contents across the project. Returns matches as " +
		"'path:line: text'. By default results are RANKED by relevance — files " +
		"whose match is a definition (func/type/class/…) and sparse, non-test, " +
		"non-vendor files come first — so the most likely target isn't buried or " +
		"truncated; pass sort:'path' for lexical order. Pattern is a Go (RE2) " +
		"regular expression unless literal is set. Options: ignore_case, literal " +
		"(fixed string, no regex escaping), word (whole words), files_only (just " +
		"the paths that match, listed by path), context (N lines of surrounding " +
		"code), sort ('relevance'|'path'), and include (comma-separated file globs " +
		"like '*.go', '*.{go,ts}', or 'internal/**/*.go'). Skips ignored and binary " +
		"files. Returns up to 200 matches."
}

func (t *grepTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"pattern":     {"type": "string", "description": "Pattern to search for: a Go RE2 regular expression, or a literal string when literal is true."},
			"path":        {"type": "string", "description": "Directory or file to search under. Optional; defaults to '.'."},
			"include":     {"type": "string", "description": "Comma-separated file globs to include, e.g. '*.go', '*.{go,ts}', or 'internal/**/*.go'. A glob without '/' matches the file name at any depth. Optional."},
			"ignore_case": {"type": "boolean", "description": "Case-insensitive matching. Optional."},
			"literal":     {"type": "boolean", "description": "Treat pattern as a literal string, not a regexp. Optional."},
			"word":        {"type": "boolean", "description": "Match whole words only. Optional."},
			"files_only":  {"type": "boolean", "description": "Return only the paths of files that contain a match (listed by path), not the matching lines. Optional."},
			"context":     {"type": "integer", "description": "Lines of context to show before and after each match (0-10). Optional."},
			"sort":        {"type": "string", "enum": ["relevance", "path"], "description": "Result ordering: 'relevance' (default — definitions and sparse, non-test/vendor files first) or 'path' (lexical). Optional."}
		},
		"required": ["pattern"]
	}`)
}

const (
	maxGrepMatches        = 200
	maxGrepMatchesPerFile = 20      // collapse the rest of a single file's matches past this
	maxGrepFileSize       = 5 << 20 // skip files larger than 5 MB
	maxGrepLineLen        = 240
)

// grepInput is the decoded grep request.
type grepInput struct {
	Pattern    string  `json:"pattern"`
	Path       string  `json:"path"`
	Include    string  `json:"include"`
	IgnoreCase bool    `json:"ignore_case"`
	Literal    bool    `json:"literal"`
	Word       bool    `json:"word"`
	FilesOnly  bool    `json:"files_only"`
	Context    flexInt `json:"context"`
	Sort       string  `json:"sort"` // "relevance" (default) | "path"
}

// fileResult holds one file's scan output: its emitted lines (hits + context)
// and whether it matched (used by files_only).
type fileResult struct {
	hits    []hitLine
	matched bool
}

func (t *grepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in grepInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}
	res, err := t.searchWithFallback(ctx, in)
	if err != nil {
		return "", err
	}
	t.r.searchCacheStore(t.Name(), input, res)
	return res, nil
}

// grepNoMatch is the sentinel a zero-result scan returns (see formatGrepResults),
// also used by searchWithFallback to detect a miss.
const grepNoMatch = "(no matches)"

// searchWithFallback runs the search and, when a STRICT search finds nothing —
// or the pattern doesn't even compile as a regex — auto-relaxes the most common
// over-specifications so the model gets useful results in a single call instead
// of retrying itself. On a zero-match it retries case-insensitively (the top
// reason a should-match search misses), then — when the pattern carries regex
// metacharacters that probably weren't meant as a regex (e.g. "Foo()") — as a
// literal string. A pattern that fails to COMPILE (e.g. "foo(", "arr[") is almost
// always an unescaped literal, so it's retried as a literal before the regex error
// is surfaced. The first variant that hits is returned with a clear label; when
// everything still misses, a diagnostic names the active filters and the scan's
// skip rules. The relaxed scans run ONLY on a miss/error (the matching common case
// short-circuits at the first call).
func (t *grepTool) searchWithFallback(ctx context.Context, in grepInput) (string, error) {
	res, err := t.search(ctx, in)
	if err != nil {
		// The pattern didn't compile as a regex. Unbalanced brackets/parens are the
		// commonest "literal that looks like a regex", so retry it as a literal
		// (case-insensitive) before surfacing the cryptic compile error.
		if !in.Literal && hasRegexMeta(in.Pattern) {
			lit := in
			lit.Literal = true
			lit.IgnoreCase = true
			if r, e := t.search(ctx, lit); e == nil {
				if r != grepNoMatch {
					return "(pattern isn't a valid regex — retried as a literal, case-insensitive string)\n" + r, nil
				}
				return noGrepMatchDiagnostic(lit), nil // literal compiled fine but matched nothing
			}
		}
		return res, err
	}
	if res != grepNoMatch {
		return res, nil
	}
	if !in.IgnoreCase {
		ci := in
		ci.IgnoreCase = true
		if r, e := t.search(ctx, ci); e == nil && r != grepNoMatch {
			return "(no case-sensitive matches — showing case-insensitive results; the search was case-sensitive)\n" + r, nil
		}
	}
	if !in.Literal && hasRegexMeta(in.Pattern) {
		lit := in
		lit.Literal = true
		lit.IgnoreCase = true
		if r, e := t.search(ctx, lit); e == nil && r != grepNoMatch {
			return "(no regex matches — the pattern has regex metacharacters, so it was retried as a literal, case-insensitive string)\n" + r, nil
		}
	}
	return noGrepMatchDiagnostic(in), nil
}

// hasRegexMeta reports whether a pattern contains RE2 metacharacters — i.e. it
// would behave differently as a regex than as a literal string.
func hasRegexMeta(pattern string) bool {
	return strings.ContainsAny(pattern, `.^$*+?()[]{}|\`)
}

// noGrepMatchDiagnostic explains a genuine zero-result search (after the case/
// literal fallbacks also missed): which scoping/filters were active and what the
// scan skips, so the model adjusts the right knob instead of re-grepping blind.
func noGrepMatchDiagnostic(in grepInput) string {
	var relaxed []string
	if !in.IgnoreCase {
		relaxed = append(relaxed, "case-insensitive")
	}
	if !in.Literal && hasRegexMeta(in.Pattern) {
		relaxed = append(relaxed, "literal")
	}
	msg := grepNoMatch
	if len(relaxed) > 0 {
		msg += " (also tried " + strings.Join(relaxed, " and ") + ")"
	}
	var filters []string
	if in.Include != "" {
		filters = append(filters, fmt.Sprintf("include=%q", in.Include))
	}
	if in.Path != "" && in.Path != "." {
		filters = append(filters, fmt.Sprintf("path=%q", in.Path))
	}
	if in.Word {
		filters = append(filters, "word=true")
	}
	if len(filters) > 0 {
		msg += " — active filters that could be hiding it: " + strings.Join(filters, ", ") + " (widen or drop them)."
	} else {
		msg += " —"
	}
	return msg + " The scan skips .gitignore/.sqeignore entries, binary files, and files >5MB, so the term may be in a skipped/generated file — otherwise it's likely absent or spelled differently."
}

// search runs a prepared grepInput through the ripgrep fast path (with a pure-Go
// fallback) and formats the results. Shared by grep and find_symbol.
func (t *grepTool) search(ctx context.Context, in grepInput) (string, error) {
	re, err := buildGrepRegexp(in.Pattern, in.Literal, in.Word, in.IgnoreCase)
	if err != nil {
		return "", err
	}
	includes, err := parseIncludes(in.Include)
	if err != nil {
		return "", err
	}
	ctxN := int(in.Context)
	if ctxN < 0 {
		ctxN = 0
	} else if ctxN > 10 {
		ctxN = 10
	}
	if in.Path == "" {
		in.Path = "."
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	searchTarget, _ := filepath.Rel(t.r.root, abs)
	if searchTarget == "" {
		searchTarget = "."
	}

	// Fast path: ripgrep when it's installed and not disabled (SQE_RG=0). It's
	// far faster on large trees; on any failure we fall back to the pure-Go scan
	// below, which is the correctness reference. Both feed the same formatter.
	if rgEnabled() {
		if rgPath, lerr := exec.LookPath("rg"); lerr == nil {
			if files, results, ok := t.scanWithRipgrep(ctx, rgPath, in, ctxN, rawIncludeGlobs(in.Include), searchTarget); ok {
				files, results = rankGrepResults(files, results, in.Sort, in.FilesOnly)
				return formatGrepResults(files, results, ctxN, in.FilesOnly), nil
			}
		}
	}

	files, results, err := t.scanWithGo(ctx, in, re, includes, ctxN, abs)
	if err != nil {
		return "", err
	}
	files, results = rankGrepResults(files, results, in.Sort, in.FilesOnly)
	return formatGrepResults(files, results, ctxN, in.FilesOnly), nil
}

// scanWithGo is the dependency-free reference scanner: it walks the tree
// (honoring ignore rules, include globs, and the size guard) and scans the
// candidate files concurrently. Each worker writes only its own slot, so the
// results stay in sorted-by-path order without locking.
func (t *grepTool) scanWithGo(ctx context.Context, in grepInput, re *regexp.Regexp, includes []includeGlob, ctxN int, abs string) ([]string, []fileResult, error) {
	var files []string // relative to the registry root
	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(t.r.root, path)
		rel = filepath.ToSlash(rel) // gitignore/include globs and tool output use '/' on every OS
		if d.IsDir() {
			if path != abs && t.r.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.r.ignore.Match(rel, false) {
			return nil
		}
		if len(includes) > 0 && !includeMatch(includes, rel) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > maxGrepFileSize {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	sort.Strings(files)

	results := make([]fileResult, len(files))
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				data, rerr := os.ReadFile(filepath.Join(t.r.root, files[i]))
				if rerr != nil || isBinary(data) {
					continue
				}
				if in.FilesOnly {
					results[i].matched = re.Match(data)
					continue
				}
				results[i].hits = scanFileForHits(data, re, ctxN, maxGrepMatches)
				results[i].matched = len(results[i].hits) > 0
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return files, results, nil
}

// formatGrepResults renders the scan output identically for either backend.
// files_only yields the sorted matching paths; otherwise matches are
// 'path:line: text' and context lines 'path:line- text', with '--' between
// non-adjacent regions (grep -C style). Capped at maxGrepMatches.
func formatGrepResults(files []string, results []fileResult, ctxN int, filesOnly bool) string {
	if filesOnly {
		var out []string
		for i, r := range results {
			if r.matched {
				out = append(out, files[i])
			}
		}
		if len(out) == 0 {
			return grepNoMatch
		}
		total := len(out)
		truncated := total > maxGrepMatches
		if truncated {
			out = out[:maxGrepMatches]
		}
		res := strings.Join(out, "\n")
		if truncated {
			res += fmt.Sprintf("\n... showing %d of %d matching files (truncated); refine the pattern or narrow with include/path to see the rest", maxGrepMatches, total)
		}
		return res
	}

	// totalMatches is the true number of matching lines across all files, used
	// to tell the model how much was truncated (the rendered output stops at
	// maxGrepMatches, but the count keeps going).
	totalMatches := 0
	for i := range files {
		for _, h := range results[i].hits {
			if h.match {
				totalMatches++
			}
		}
	}

	var b strings.Builder
	matches, prevNum := 0, 0
	prevFile := ""
	capped := false
	for i := range files {
		if capped {
			break
		}
		fileMatches := 0
		for hi, h := range results[i].hits {
			if h.match && matches >= maxGrepMatches {
				capped = true
				break
			}
			// Collapse a single noisy file: once it has contributed many matches,
			// summarize the rest instead of flooding the result with near-identical
			// lines from one file (keeps coverage broad and the output token-lean).
			if h.match && fileMatches >= maxGrepMatchesPerFile {
				rem := 0
				for _, hh := range results[i].hits[hi:] {
					if hh.match {
						rem++
					}
				}
				fmt.Fprintf(&b, "... (%d more match(es) in %s; narrow the pattern to see them)\n", rem, files[i])
				break
			}
			if ctxN > 0 && b.Len() > 0 && (files[i] != prevFile || h.num-prevNum > 1) {
				b.WriteString("--\n")
			}
			sep := "-"
			if h.match {
				sep = ":"
				matches++
				fileMatches++
			}
			fmt.Fprintf(&b, "%s:%d%s %s\n", files[i], h.num, sep, h.text)
			prevFile, prevNum = files[i], h.num
		}
	}
	if b.Len() == 0 {
		return grepNoMatch
	}
	res := strings.TrimRight(b.String(), "\n")
	if capped {
		res += fmt.Sprintf("\n... showing %d of at least %d matches (truncated); refine the pattern or narrow with include/path to see the rest", maxGrepMatches, totalMatches)
	}
	return res
}

// rankGrepResults reorders (files,results) so the most relevant matches come
// first: a file whose match is a DEFINITION (func/type/class/…) ranks highest,
// sparse files (few matches — likelier the definition than a heavy-use site) above
// dense ones, and test/vendor/generated files last. Within a file the hit order is
// untouched. Because formatGrepResults applies the 200-match cap in iteration
// order, this also decides WHICH matches survive the cap — so the definition is no
// longer truncated away by an alphabetically-late path. files_only keeps lexical
// (path) order — it's a set, and a predictable order is more useful there.
// sort=="path" forces lexical order. The reorder is stable on (-score, path) so the
// output is deterministic and identical across the ripgrep and Go backends.
func rankGrepResults(files []string, results []fileResult, sortMode string, filesOnly bool) ([]string, []fileResult) {
	if filesOnly || sortMode == "path" || len(files) <= 1 {
		return files, results
	}
	order := make([]int, len(files))
	scores := make([]int, len(files))
	for i := range files {
		order[i] = i
		scores[i] = grepRelevance(files[i], results[i])
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if scores[ia] != scores[ib] {
			return scores[ia] > scores[ib]
		}
		return files[ia] < files[ib]
	})
	rf := make([]string, len(files))
	rr := make([]fileResult, len(files))
	for n, i := range order {
		rf[n], rr[n] = files[i], results[i]
	}
	return rf, rr
}

// grepRelevance scores one file's match for ranking (higher surfaces first).
func grepRelevance(path string, r fileResult) int {
	score, matches, hasDef := 0, 0, false
	for _, h := range r.hits {
		if !h.match {
			continue
		}
		matches++
		if defLineRe.MatchString(h.text) {
			hasDef = true
		}
	}
	if matches == 0 && r.matched {
		matches = 1 // a result with no recorded hit lines still matched once
	}
	if hasDef {
		// The file has a match on a declaration line — likeliest "the
		// definition". Credited once per file (not per hit) so a heavy-use file
		// that happens to contain several declaration-shaped lines can't pile up
		// an unbounded bonus and swamp the location penalties below.
		score += 12
	}
	switch { // sparse files (the def) rank above dense ones (heavy use); bounded
	case matches <= 1:
		score += 5
	case matches <= 3:
		score += 3
	case matches >= 12:
		score -= 4
	}
	if isTestPath(path) {
		score -= 8
	}
	if isVendorPath(path) {
		score -= 10
	}
	if isGeneratedPath(path) {
		score -= 6
	}
	return score
}

// defLineRe matches a line whose LEADING token is a definition keyword — after
// optional indentation, and (since modifiers like public/static/export are
// themselves in symbolDefKeywords) any leading modifier chain. So it credits
// real declarations — "func Foo", "  public class X", "export const y" — but NOT
// prose or usages that merely contain a keyword somewhere on the line (e.g. a
// comment "// the type of Foo", or a call). This trades a little recall
// (keyword-less declarations like JS methods or Go struct fields don't get the
// bonus) for precision (comments/usages no longer masquerade as definitions),
// which is the right call for ranking. Reuses symbolDefKeywords; the trailing
// \b stops "func" from matching "funcs"/"funcall". Language-agnostic, ASCII.
var defLineRe = regexp.MustCompile(`^\s*(?:` + symbolDefKeywords + `)\b`)

// isTestPath / isVendorPath / isGeneratedPath classify a relative path so ranking
// can de-prioritize it. They only LOWER a score; a result is never excluded.
func isTestPath(p string) bool {
	base := filepath.Base(p)
	if strings.Contains(base, "_test.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") {
		return true
	}
	for _, seg := range []string{"/test/", "/tests/", "/__tests__/", "/testdata/"} {
		if strings.Contains("/"+p, seg) {
			return true
		}
	}
	return false
}

func isVendorPath(p string) bool {
	for _, seg := range []string{"vendor/", "node_modules/", "third_party/", "third-party/", "site-packages/", ".venv/", "/external/"} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	return false
}

func isGeneratedPath(p string) bool {
	base := filepath.Base(p)
	if strings.Contains(base, ".min.") || strings.Contains(base, "-min.") || strings.Contains(base, ".generated.") || strings.Contains(base, "_gen.") || strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".g.dart") {
		return true
	}
	return strings.Contains(p, "/dist/") || strings.Contains(p, "/build/")
}

// maxRgOutputBytes caps how much ripgrep output we buffer (plenty for the
// 200-match cap; bounds memory on a match-everything query).
const maxRgOutputBytes = 16 << 20

// rgEnabled reports whether the ripgrep fast path is allowed (default yes;
// SQE_RG=0/off/false/no forces the pure-Go scanner).
func rgEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_RG"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// scanWithRipgrep runs ripgrep and parses its output into the same (files,
// results) shape as the Go scanner. It returns ok=false on any problem (rg
// missing/errored, bad pattern for rg's regex engine, etc.) so the caller falls
// back. Results are post-filtered through the project's ignore set so rg never
// surfaces a path the Go path would hide (.sqeignore parity).
func (t *grepTool) scanWithRipgrep(ctx context.Context, rgPath string, in grepInput, ctxN int, rawGlobs []string, searchTarget string) ([]string, []fileResult, bool) {
	rgCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(rgCtx, rgPath, buildRgArgs(in, ctxN, rawGlobs, t.sqeignorePath(), searchTarget)...)
	cmd.Dir = t.r.root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, false
	}
	data, _ := io.ReadAll(io.LimitReader(stdout, maxRgOutputBytes))
	overflowed := len(data) >= maxRgOutputBytes
	cancel()                           // stop rg if it's still producing
	_, _ = io.Copy(io.Discard, stdout) // drain so Wait doesn't block on a full pipe
	waitErr := cmd.Wait()
	if !overflowed && waitErr != nil {
		// rg exits 1 when there are simply no matches (fine); anything else
		// (exit 2 bad pattern, missing binary) means fall back to the Go path.
		if ee, isExit := waitErr.(*exec.ExitError); !isExit || ee.ExitCode() != 1 {
			return nil, nil, false
		}
	}

	ignore := t.ignored
	if in.FilesOnly {
		files := parseRgFileList(data, ignore)
		results := make([]fileResult, len(files))
		for i := range results {
			results[i].matched = true
		}
		return files, results, true
	}
	files, results := parseRgJSON(data, ignore)
	return files, results, true
}

// ignored reports whether a relative path is hidden by the project's ignore
// rules — either directly, or because one of its ancestor directories is
// ignored. The Go walk gets this for free (it SkipDir's ignored directories);
// ripgrep doesn't, so we replicate it when filtering rg's results for parity.
func (t *grepTool) ignored(rel string) bool {
	if t.r.ignore.Match(rel, false) {
		return true
	}
	for dir := filepath.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if t.r.ignore.Match(dir, true) {
			return true
		}
	}
	return false
}

// sqeignorePath returns the project's .sqeignore path if it exists, so it can be
// passed to ripgrep (which honors .gitignore but not .sqeignore).
func (t *grepTool) sqeignorePath() string {
	p := filepath.Join(t.r.root, ".sqeignore")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// buildRgArgs maps the grep options to ripgrep flags. --sort path makes rg's
// output deterministic and path-sorted, matching the Go scanner.
func buildRgArgs(in grepInput, ctxN int, rawGlobs []string, sqeignore, searchTarget string) []string {
	args := []string{"--sort", "path", "--max-filesize", strconv.Itoa(maxGrepFileSize)}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Literal {
		args = append(args, "-F")
	}
	if in.Word {
		args = append(args, "-w")
	}
	for _, g := range rawGlobs {
		args = append(args, "-g", g)
	}
	if sqeignore != "" {
		args = append(args, "--ignore-file", sqeignore)
	}
	if in.FilesOnly {
		args = append(args, "-l")
	} else {
		if ctxN > 0 {
			args = append(args, "-C", strconv.Itoa(ctxN))
		}
		args = append(args, "--json")
	}
	// -e guards patterns starting with '-'; -- ends flag parsing before the path.
	return append(args, "-e", in.Pattern, "--", searchTarget)
}

// rgEvent is one ripgrep --json record (we only use match/context).
type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       rgText `json:"path"`
		Lines      rgText `json:"lines"`
		LineNumber int    `json:"line_number"`
	} `json:"data"`
}

type rgText struct {
	Text string `json:"text"`
}

// parseRgJSON turns ripgrep --json output into (sorted files, aligned results),
// dropping any path the ignore predicate hides.
func parseRgJSON(data []byte, ignore func(string) bool) ([]string, []fileResult) {
	hits := map[string][]hitLine{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var ev rgEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type != "match" && ev.Type != "context" {
			continue
		}
		p := filepath.ToSlash(filepath.Clean(ev.Data.Path.Text))
		if p == "" || p == "." || ignore(p) {
			continue
		}
		hits[p] = append(hits[p], hitLine{num: ev.Data.LineNumber, text: clipLine(ev.Data.Lines.Text), match: ev.Type == "match"})
	}
	return mapToSortedResults(hits)
}

// parseRgFileList turns ripgrep -l output (one path per line) into sorted,
// ignore-filtered, de-duplicated paths.
func parseRgFileList(data []byte, ignore func(string) bool) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		p := filepath.ToSlash(filepath.Clean(strings.TrimSpace(line)))
		if p == "" || p == "." || ignore(p) || seen[p] {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	sort.Strings(files)
	return files
}

func mapToSortedResults(hits map[string][]hitLine) ([]string, []fileResult) {
	files := make([]string, 0, len(hits))
	for p := range hits {
		files = append(files, p)
	}
	sort.Strings(files)
	results := make([]fileResult, len(files))
	for i, p := range files {
		results[i] = fileResult{hits: hits[p], matched: true}
	}
	return files, results
}

// rawIncludeGlobs expands the include string into the raw glob list passed to
// ripgrep's -g (the Go path uses the compiled form from parseIncludes).
func rawIncludeGlobs(include string) []string {
	include = strings.TrimSpace(include)
	if include == "" {
		return nil
	}
	var out []string
	for _, part := range splitOutsideBraces(include, ',') {
		for _, g := range expandBraces(strings.TrimSpace(part)) {
			if g != "" {
				out = append(out, g)
			}
		}
	}
	return out
}

// hitLine is one emitted line from a file scan: a match or a context line.
type hitLine struct {
	num   int // 1-based line number
	text  string
	match bool
}

// scanFileForHits finds matching lines (capped at max) plus ctxN lines of
// context around each, returned in line order. Context lines that overlap are
// emitted once; a line that is both a match and another match's context is a
// match.
func scanFileForHits(data []byte, re *regexp.Regexp, ctxN, max int) []hitLine {
	lines := strings.Split(string(data), "\n")
	// A trailing newline yields a phantom empty final element; drop it so line
	// counting matches grep/ripgrep (which don't count a line after the last \n).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	isMatch := map[int]bool{}
	for i, line := range lines {
		if re.MatchString(line) {
			isMatch[i] = true
			if len(isMatch) >= max {
				break
			}
		}
	}
	if len(isMatch) == 0 {
		return nil
	}
	include := map[int]struct{}{}
	for i := range isMatch {
		for j := i - ctxN; j <= i+ctxN; j++ {
			if j >= 0 && j < len(lines) {
				include[j] = struct{}{}
			}
		}
	}
	idxs := make([]int, 0, len(include))
	for i := range include {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]hitLine, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, hitLine{num: i + 1, text: clipLine(lines[i]), match: isMatch[i]})
	}
	return out
}

// clipLine trims surrounding whitespace and caps a line's length for display.
func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxGrepLineLen {
		s = s[:maxGrepLineLen] + "…"
	}
	return s
}

// buildGrepRegexp assembles the search regexp from the literal/word/ignore_case
// options.
func buildGrepRegexp(pattern string, literal, word, ignoreCase bool) (*regexp.Regexp, error) {
	expr := pattern
	if literal {
		expr = regexp.QuoteMeta(expr)
	}
	if word {
		expr = `\b(?:` + expr + `)\b`
	}
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %v", err)
	}
	return re, nil
}

// includeGlob is one parsed include pattern. pathful globs (containing '/') are
// matched against the relative path; bare globs match the file name at any depth.
type includeGlob struct {
	re      *regexp.Regexp
	pathful bool
}

// parseIncludes splits a comma-separated include string, expands brace sets
// ('*.{go,ts}'), and compiles each glob.
func parseIncludes(include string) ([]includeGlob, error) {
	include = strings.TrimSpace(include)
	if include == "" {
		return nil, nil
	}
	var globs []includeGlob
	for _, part := range splitOutsideBraces(include, ',') {
		for _, g := range expandBraces(strings.TrimSpace(part)) {
			if g == "" {
				continue
			}
			re, err := globToRegexp(g)
			if err != nil {
				return nil, err
			}
			globs = append(globs, includeGlob{re: re, pathful: strings.Contains(g, "/")})
		}
	}
	return globs, nil
}

// splitOutsideBraces splits s on sep, ignoring separators inside '{...}', so a
// comma-separated include list and a brace set ('*.{go,ts}') both parse.
func splitOutsideBraces(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// includeMatch reports whether rel passes any of the include globs.
func includeMatch(globs []includeGlob, rel string) bool {
	base := filepath.Base(rel)
	for _, g := range globs {
		target := base
		if g.pathful {
			target = rel
		}
		if g.re.MatchString(target) {
			return true
		}
	}
	return false
}

// expandBraces expands brace sets: "*.{go,ts}" -> ["*.go", "*.ts"]. Handles
// nested/multiple groups by recursing.
func expandBraces(p string) []string {
	open := strings.IndexByte(p, '{')
	if open < 0 {
		return []string{p}
	}
	closeIdx := strings.IndexByte(p[open:], '}')
	if closeIdx < 0 {
		return []string{p}
	}
	closeIdx += open
	prefix, inner, suffix := p[:open], p[open+1:closeIdx], p[closeIdx+1:]
	var out []string
	for _, opt := range strings.Split(inner, ",") {
		out = append(out, expandBraces(prefix+opt+suffix)...)
	}
	return out
}

func (t *lsTool) Mutates() bool   { return false }
func (t *globTool) Mutates() bool { return false }
func (t *grepTool) Mutates() bool { return false }

// findSymbolTool locates where a symbol (function, type, class, method,
// constant, …) is DEFINED, as opposed to merely mentioned. It builds a
// language-aware "declaration" regexp for the given name and runs it through the
// same ripgrep/Go machinery as grep, so the model gets the definition site(s) in
// one call instead of grepping the name and wading through every usage.
type findSymbolTool struct{ r *Registry }

func (t *findSymbolTool) Name() string { return "find_symbol" }

func (t *findSymbolTool) Description() string {
	return "Find where a symbol is DEFINED (function, method, type, class, " +
		"interface, struct, enum, trait, const, var, …) across the project. Pass " +
		"the bare name (e.g. 'parseConfig', 'UserService'); returns the " +
		"declaration sites as 'path:line: signature', not every usage. Language-" +
		"aware across Go/Python/JS/TS/Java/C#/Rust/Swift/Kotlin/Ruby/PHP and more. " +
		"Prefer this over grep when you want to jump to a definition. For call " +
		"sites / usages, or for plain-text matches, use grep instead. Options: " +
		"path, include (file globs), ignore_case, context."
}

func (t *findSymbolTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"name":        {"type": "string", "description": "The symbol name to find the definition of (bare identifier, no parentheses)."},
			"path":        {"type": "string", "description": "Directory or file to search under. Optional; defaults to '.'."},
			"include":     {"type": "string", "description": "Comma-separated file globs to include, e.g. '*.go', '*.{ts,tsx}'. Optional."},
			"ignore_case": {"type": "boolean", "description": "Case-insensitive matching. Optional; symbols are case-sensitive by default."},
			"context":     {"type": "integer", "description": "Lines of surrounding code to show with each definition (0-10). Optional."}
		},
		"required": ["name"]
	}`)
}

func (t *findSymbolTool) Mutates() bool { return false }

// findSymbolInput is the decoded find_symbol request.
type findSymbolInput struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Include    string  `json:"include"`
	IgnoreCase bool    `json:"ignore_case"`
	Context    flexInt `json:"context"`
}

func (t *findSymbolTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in findSymbolInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	// Reuse grep's ripgrep/Go scan with a declaration-shaped regexp.
	g := &grepTool{r: t.r}
	out, err := g.search(ctx, grepInput{
		Pattern:    symbolDefRegexp(name),
		Path:       in.Path,
		Include:    in.Include,
		IgnoreCase: in.IgnoreCase,
		Context:    in.Context,
	})
	if err != nil {
		return "", err
	}
	if out == "(no matches)" {
		return fmt.Sprintf("No definition of %q found. It may be declared without a recognized keyword (e.g. a C-style function), defined dynamically, or only used elsewhere — try grep %q for usages, or check the spelling.", name, name), nil
	}
	return fmt.Sprintf("Definitions of %q (declaration sites; use grep for usages):\n%s", name, out), nil
}

// symbolDefKeywords are the declaration-introducing keywords that, when they
// precede a name on a line, mark it as a definition across most languages.
const symbolDefKeywords = `func|def|fn|sub|function|class|interface|struct|enum|trait|protocol|extension|impl|module|namespace|package|type|typealias|typedef|var|const|let|val|public|private|protected|internal|static|final|abstract|override|export`

// symbolDefRegexp builds an RE2 pattern that matches a DEFINITION of name:
// either a declaration keyword followed (after optional modifiers/return types)
// by the name before any '=', ';' or '{' on the line, OR the name bound to a
// function/arrow expression (JS/TS `name = function`, `name = (…) =>`). The name
// is regexp-escaped and word-bounded so it won't match substrings.
func symbolDefRegexp(name string) string {
	q := regexp.QuoteMeta(name)
	// Keyword-led: e.g. "func Name", "def Name", "class Name", "public void Name"
	keywordLed := `(?:\b(?:` + symbolDefKeywords + `)\b[^\n;{=]*\b` + q + `\b)`

	// Assigned functions: JS/TS "const Name = () =>" or "Name = function"
	assigned := `(?:\b` + q + `\b\s*[:=]\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>))`

	// Class/struct/interface/object methods with curly braces (JS/TS, Java, C#, C++, Rust, etc. class methods without keywords):
	// e.g. "myMethod(args) {", "myMethod(args): void {"
	classMethod := `(?:\b` + q + `\b\s*(?:<[^>]+>)?\s*\([^)]*\)\s*(?:[^\n;{=]*)\{)`

	// Go interface methods or abstract/C-style declarations ending a line:
	// e.g. "MyMethod(ctx) (Result, error)"
	declLine := `(?:^\s*\b` + q + `\b\s*\([^)]*\)\s*(?:[a-zA-Z0-9_*\[\]\s,.]*(?:\([^)]*\))?\s*)?$)`

	// Go struct field definitions:
	// e.g. "FieldName string", "FieldName *string", "FieldName map[string]int `json:\"field\"`"
	structField := `(?:^\s*\b` + q + `\b\s+[a-zA-Z0-9_*\[\]]+(?:\[[^\]]+\])?[a-zA-Z0-9_*]*(?:\s+\x60[^\x60]+\x60)?\s*$)`

	return keywordLed + `|` + assigned + `|` + classMethod + `|` + declLine + `|` + structField
}

// isBinary reports whether data looks like a binary file (has a NUL byte in the
// first chunk).
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// globToRegexp converts a glob pattern into an anchored regular expression.
// Supports '*' (non-separator), '?' (single non-separator), and '**' (any).
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				// consume an optional following '/'
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %v", pattern, err)
	}
	return re, nil
}
