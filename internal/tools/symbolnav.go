package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file adds two symbol-navigation tools that reduce the read/grep churn
// the agent otherwise incurs:
//
//   read_symbol    — read ONLY the body of a named function/type/etc. instead
//                    of a whole file or a guessed line window.
//   find_references — list the USAGES (call sites / references) of a symbol,
//                    ranked and deduped, filtering out the definition itself.
//
// Both reuse the definition machinery from search.go (symbolDefRegexp,
// defLineRe) and the ignore/include plumbing on the Registry.

// ---------------------------------------------------------------------------
// shared scanning
// ---------------------------------------------------------------------------

// symbolNavMaxFiles caps how many files we scan so a huge tree can't blow up
// latency; the walk is ordered so results are deterministic.
const symbolNavMaxFiles = 20000

// walkSourceFiles invokes fn for every non-ignored, non-binary, size-bounded
// file under the registry root whose relative path matches the include globs
// (when non-empty). Paths passed to fn are relative to root.
func (r *Registry) walkSourceFiles(includes []includeGlob, fn func(rel string, data []byte)) error {
	root := r.root
	count := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if p != root && r.ignore != nil && r.ignore.Match(p, true) {
				return filepath.SkipDir
			}
			base := d.Name()
			if p != root && (base == ".git" || base == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= symbolNavMaxFiles {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if len(includes) > 0 && !includeMatch(includes, rel) {
			return nil
		}
		if r.ignore != nil && r.ignore.Match(p, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxGrepFileSize {
			return nil
		}
		data, derr := os.ReadFile(p)
		if derr != nil || isBinary(data) {
			return nil
		}
		count++
		fn(rel, data)
		return nil
	})
}

// ---------------------------------------------------------------------------
// read_symbol
// ---------------------------------------------------------------------------

type readSymbolTool struct{ r *Registry }

type readSymbolInput struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func (t *readSymbolTool) Name() string { return "read_symbol" }

func (t *readSymbolTool) Description() string {
	return "Read ONLY the body of a named symbol (function, method, type, class, interface, struct, const, etc.) instead of a whole file. Give the bare identifier (e.g. 'parseConfig', 'UserService'); returns the definition's source with 1-based line numbers, plus the file:line it came from. Much cheaper than read_file for editing one declaration in a large file. Optionally restrict to a path (file or directory). If several definitions match, the first (ranked: non-test/non-vendor first) is returned and the others are listed."
}

func (t *readSymbolTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Bare symbol name to read the definition of (no parentheses)."},
			"path": {"type": "string", "description": "Optional file or directory to restrict the search to."}
		},
		"required": ["name"]
	}`)
}

func (t *readSymbolTool) Mutates() bool { return false }

func (t *readSymbolTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in readSymbolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	defRe, err := regexp.Compile(symbolDefRegexp(name))
	if err != nil {
		return "", fmt.Errorf("bad symbol pattern: %w", err)
	}

	type defHit struct {
		rel   string
		line  int // 1-based
		lines []string
	}
	var hits []defHit

	scan := func(rel string, data []byte) {
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for i, ln := range lines {
			if defRe.MatchString(ln) {
				hits = append(hits, defHit{rel: rel, line: i + 1, lines: lines})
			}
		}
	}

	if in.Path != "" {
		abs, rerr := t.r.resolve(in.Path)
		if rerr != nil {
			return "", rerr
		}
		info, serr := os.Stat(abs)
		if serr != nil {
			return "", fmt.Errorf("cannot stat %s: %w", in.Path, serr)
		}
		if info.IsDir() {
			if err := t.walkUnder(abs, scan); err != nil {
				return "", err
			}
		} else {
			data, derr := os.ReadFile(abs)
			if derr != nil {
				return "", derr
			}
			rel, _ := filepath.Rel(t.r.root, abs)
			scan(filepath.ToSlash(rel), data)
		}
	} else {
		if err := t.r.walkSourceFiles(nil, scan); err != nil {
			return "", err
		}
	}

	if len(hits) == 0 {
		return fmt.Sprintf("No definition of %q found.", name), nil
	}

	// Rank: non-test/non-vendor/non-generated first, then by path.
	sort.SliceStable(hits, func(a, b int) bool {
		ra, rb := defRank(hits[a].rel), defRank(hits[b].rel)
		if ra != rb {
			return ra < rb
		}
		if hits[a].rel != hits[b].rel {
			return hits[a].rel < hits[b].rel
		}
		return hits[a].line < hits[b].line
	})

	best := hits[0]
	start, end := symbolBodyRange(best.lines, best.line-1)
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d (symbol %q)\n", best.rel, best.line, name)
	for i := start; i <= end && i < len(best.lines); i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, best.lines[i])
	}
	if len(hits) > 1 {
		b.WriteString("\nOther definitions:\n")
		for _, h := range hits[1:] {
			fmt.Fprintf(&b, "  %s:%d\n", h.rel, h.line)
		}
	}
	return b.String(), nil
}

func (t *readSymbolTool) walkUnder(absDir string, fn func(rel string, data []byte)) error {
	return filepath.WalkDir(absDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != absDir && t.r.ignore != nil && t.r.ignore.Match(p, true) {
				return filepath.SkipDir
			}
			if base := d.Name(); p != absDir && (base == ".git" || base == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if t.r.ignore != nil && t.r.ignore.Match(p, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxGrepFileSize {
			return nil
		}
		data, derr := os.ReadFile(p)
		if derr != nil || isBinary(data) {
			return nil
		}
		rel, _ := filepath.Rel(t.r.root, p)
		fn(filepath.ToSlash(rel), data)
		return nil
	})
}

// defRank orders definition sites: real source (0) before generated (1),
// test (2), vendor (3).
func defRank(rel string) int {
	switch {
	case isVendorPath(rel):
		return 3
	case isTestPath(rel):
		return 2
	case isGeneratedPath(rel):
		return 1
	default:
		return 0
	}
}

// symbolBodyRange returns [start,end] line indices (0-based, inclusive) that
// span the definition beginning at defIdx. For brace-delimited languages it
// tracks brace depth from the first '{' on/after the def line to its match.
// For indentation-based blocks (no brace on the def line, e.g. Python) it
// includes subsequent lines more-indented than the def line. Falls back to the
// single def line when neither applies.
func symbolBodyRange(lines []string, defIdx int) (int, int) {
	if defIdx < 0 || defIdx >= len(lines) {
		if defIdx < 0 {
			return 0, 0
		}
		return len(lines) - 1, len(lines) - 1
	}
	// Look for an opening brace on the def line or the next couple lines.
	braceLine := -1
	for i := defIdx; i < len(lines) && i <= defIdx+2; i++ {
		if strings.Contains(lines[i], "{") {
			braceLine = i
			break
		}
		// A line ending in ';' with no brace is a forward decl / one-liner.
		if strings.HasSuffix(strings.TrimSpace(lines[i]), ";") {
			return defIdx, i
		}
	}
	if braceLine >= 0 {
		depth := 0
		for i := braceLine; i < len(lines); i++ {
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			if depth <= 0 {
				return defIdx, i
			}
		}
		return defIdx, len(lines) - 1
	}
	// Indentation-based (Python-like): include following lines that are blank
	// or indented deeper than the def line.
	baseIndent := indentWidth(lines[defIdx])
	end := defIdx
	for i := defIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			end = i
			continue
		}
		if indentWidth(lines[i]) <= baseIndent {
			break
		}
		end = i
	}
	// Trim trailing blank lines.
	for end > defIdx && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	return defIdx, end
}

func indentWidth(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// find_references
// ---------------------------------------------------------------------------

type findReferencesTool struct{ r *Registry }

type findReferencesInput struct {
	Name    string `json:"name"`
	Include string `json:"include,omitempty"`
	Path    string `json:"path,omitempty"`
}

func (t *findReferencesTool) Name() string { return "find_references" }

func (t *findReferencesTool) Description() string {
	return "Find USAGES (call sites / references) of a symbol across the project — the complement of find_symbol (which returns only definitions). Pass the bare identifier; returns matching 'path:line: text' lines with the DEFINITION site(s) filtered out, ranked so non-test/non-vendor files come first and deduped per file. Prefer this over a broad grep when refactoring or tracing where something is used. Options: include (comma-separated file globs like '*.go'), path (restrict to a subdir)."
}

func (t *findReferencesTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Bare symbol name to find usages of."},
			"include": {"type": "string", "description": "Optional comma-separated file globs, e.g. '*.go' or '*.{ts,tsx}'."},
			"path": {"type": "string", "description": "Optional subdirectory to restrict the search to."}
		},
		"required": ["name"]
	}`)
}

func (t *findReferencesTool) Mutates() bool { return false }

const maxReferenceLines = 200

func (t *findReferencesTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in findReferencesInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	wordRe, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil {
		return "", fmt.Errorf("bad name: %w", err)
	}
	defRe, err := regexp.Compile(symbolDefRegexp(name))
	if err != nil {
		return "", fmt.Errorf("bad symbol pattern: %w", err)
	}

	includes, err := parseIncludes(in.Include)
	if err != nil {
		return "", err
	}

	type ref struct {
		rel  string
		line int
		text string
	}
	var refs []ref
	defs := 0

	scan := func(rel string, data []byte) {
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for i, ln := range lines {
			if !wordRe.MatchString(ln) {
				continue
			}
			if defRe.MatchString(ln) {
				defs++
				continue // skip the definition itself
			}
			text := strings.TrimSpace(ln)
			if len(text) > 240 {
				text = text[:240] + "…"
			}
			refs = append(refs, ref{rel: rel, line: i + 1, text: text})
		}
	}

	if in.Path != "" {
		abs, rerr := t.r.resolve(in.Path)
		if rerr != nil {
			return "", rerr
		}
		if err := (&readSymbolTool{r: t.r}).walkUnder(abs, func(rel string, data []byte) {
			if len(includes) > 0 && !includeMatch(includes, rel) {
				return
			}
			scan(rel, data)
		}); err != nil {
			return "", err
		}
	} else {
		if err := t.r.walkSourceFiles(includes, scan); err != nil {
			return "", err
		}
	}

	if len(refs) == 0 {
		if defs > 0 {
			return fmt.Sprintf("No references to %q found (only its definition).", name), nil
		}
		return fmt.Sprintf("No references to %q found.", name), nil
	}

	// Rank by file class then path/line.
	sort.SliceStable(refs, func(a, b int) bool {
		ra, rb := defRank(refs[a].rel), defRank(refs[b].rel)
		if ra != rb {
			return ra < rb
		}
		if refs[a].rel != refs[b].rel {
			return refs[a].rel < refs[b].rel
		}
		return refs[a].line < refs[b].line
	})

	total := len(refs)
	truncated := false
	if len(refs) > maxReferenceLines {
		refs = refs[:maxReferenceLines]
		truncated = true
	}

	var b strings.Builder
	files := map[string]bool{}
	for _, r := range refs {
		files[r.rel] = true
	}
	fmt.Fprintf(&b, "%d reference(s) to %q in %d file(s)", total, name, len(files))
	if defs > 0 {
		fmt.Fprintf(&b, " (%d definition site(s) excluded)", defs)
	}
	b.WriteString(":\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "%s:%d: %s\n", r.rel, r.line, r.text)
	}
	if truncated {
		fmt.Fprintf(&b, "… %d more (narrow with include/path).\n", total-maxReferenceLines)
	}
	return b.String(), nil
}
