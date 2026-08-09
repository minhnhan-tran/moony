package tools

// .moonyignore: a small subset of gitignore semantics for the agent's read-side
// tools (glob, grep, list_dir). Reads `.moonyignore` (and the legacy
// `.sqeignore`) plus `.gitignore` from the
// working directory and applies a sensible built-in default so noisy folders
// like `.git`, `node_modules`, `dist`, and build caches don't blow up
// every search.
//
// Supported patterns:
//
//   foo            match basename or any path segment named "foo"
//   foo/           directory-only match (entire subtree)
//   /foo           anchored at the working directory
//   *.go           glob (any depth)
//   !pattern       negation; later rules override earlier ones
//   #comment       comments and blank lines ignored
//
// The match function returns (matched, isDir). When matched && isDir, callers
// should prune the entire subtree.

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// nestedIgnoreMaxDepth bounds how deep LoadIgnore looks for nested .gitignore /
// agent ignore files. Real projects keep them near the top, and this scan runs at
// startup, so a shallow bound keeps it cheap on a big tree.
const nestedIgnoreMaxDepth = 4

// ignoreFileNames are the per-directory ignore files that are honoured, in order.
//
// Both the current and the legacy agent ignore file are read: unlike the config
// DIRECTORY, this is a file users hand-author and commit to their repositories, so
// dropping the old name would silently stop honouring a file that had been working
// — with nothing to migrate, since it lives in the user's own git history.
var ignoreFileNames = []string{".gitignore", ignoreFileName, legacyIgnoreFileName}

// The agent's own ignore file, named after the project dir it accompanies.
const (
	ignoreFileName       = ".moonyignore"
	legacyIgnoreFileName = ".sqeignore"
)

// defaultIgnorePatterns are always applied. They mirror what a sensible
// .gitignore in any repo would carry.
var defaultIgnorePatterns = []string{
	".git/", ".hg/", ".svn/",
	config.ProjectDir + "/", config.LegacyProjectDir + "/",
	"node_modules/", "vendor/", ".venv/", "__pycache__/",
	"dist/", "build/", "target/",
	".next/", ".nuxt/", ".cache/", ".parcel-cache/",
	".idea/", ".vscode/",
	".DS_Store",
	".env", // local secrets file — keep it out of agent searches
	"*.pyc", "*.class", "*.o", "*.obj",
}

// ignoreRule is one parsed pattern.
type ignoreRule struct {
	pattern  string // canonicalized: leading slash trimmed; trailing slash trimmed
	anchored bool   // pattern began with `/` (match relative to root only)
	dirOnly  bool   // pattern ended with `/` (only matches directories)
	negate   bool   // pattern began with `!`
	hasSlash bool   // pattern contains a `/` after canonicalization
	// scope confines the rule to one subtree (repo-relative dir, no trailing slash)
	// when it came from a nested ignore file; empty means repo-wide.
	scope string
}

// IgnoreSet holds the rules from one ignore root.
type IgnoreSet struct {
	rules []ignoreRule
	root  string
}

// LoadIgnore reads `.gitignore` and the agent ignore files (see ignoreFileNames) from root and
// returns a combined set, preceded by defaultIgnorePatterns. A missing file is
// not an error.
func LoadIgnore(root string) *IgnoreSet {
	set := &IgnoreSet{root: root}
	for _, p := range defaultIgnorePatterns {
		set.add(p)
	}
	for _, name := range ignoreFileNames {
		_ = set.appendFile(filepath.Join(root, name))
	}
	set.appendNested(root)
	return set
}

// appendFile parses a gitignore-style file.
func (s *IgnoreSet) appendFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.add(line)
	}
	return sc.Err()
}

// add canonicalizes one raw pattern and appends a repo-wide rule.
func (s *IgnoreSet) add(raw string) { s.addScoped(raw, "") }

// appendNested finds ignore files BELOW root and adds their rules scoped to the
// file's own directory, so a nested `.gitignore`'s `dist/` governs only its own
// subtree and cannot match a same-named directory elsewhere in the repo.
//
// Reading only the root ignore file meant a vendored subtree excluded by a nested
// .gitignore was still walked and indexed as project source, crowding real files out
// of the symbol map's budget.
//
// Bounded deliberately: it descends only through directories the rules so far don't
// already exclude, and stops at nestedIgnoreMaxDepth, so it stays a cheap startup
// scan rather than a full walk.
func (s *IgnoreSet) appendNested(root string) {
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if depth > nestedIgnoreMaxDepth {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			// Don't descend into anything already excluded: its ignore files are
			// irrelevant and the tree may be huge (node_modules).
			if s.Match(childRel, true) {
				continue
			}
			for _, n := range ignoreFileNames {
				s.appendFileScoped(filepath.Join(dir, name, n), childRel)
			}
			walk(filepath.Join(dir, name), childRel, depth+1)
		}
	}
	walk(root, "", 0)
}

// appendFileScoped parses an ignore file whose rules apply only within scope (a
// repo-relative directory), mirroring how git scopes a nested .gitignore.
func (s *IgnoreSet) appendFileScoped(path, scope string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.addScoped(line, scope)
	}
}

// addScoped canonicalizes one raw pattern and appends a rule. A non-empty scope
// confines the pattern to that subtree: an unanchored name like `dist` would
// otherwise match a same-named directory anywhere in the repo.
func (s *IgnoreSet) addScoped(raw, scope string) {
	r := ignoreRule{scope: scope}
	if strings.HasPrefix(raw, "!") {
		r.negate = true
		raw = raw[1:]
	}
	if strings.HasPrefix(raw, "/") {
		r.anchored = true
		raw = raw[1:]
	}
	if strings.HasSuffix(raw, "/") {
		r.dirOnly = true
		raw = strings.TrimSuffix(raw, "/")
	}
	r.pattern = raw
	r.hasSlash = strings.Contains(raw, "/")
	s.rules = append(s.rules, r)
}

// Match reports whether relPath (relative to the ignore root, forward-slash
// separated) is ignored. isDir lets directory-only rules apply.
func (s *IgnoreSet) Match(relPath string, isDir bool) bool {
	if s == nil || len(s.rules) == 0 {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	matched := false
	for _, r := range s.rules {
		// A scoped rule (from a nested ignore file) governs the CONTENTS of its
		// directory, never the directory itself — matching the scope dir against its own
		// rules lets a catch-all like `*` hide the very subtree the file lives in, so a
		// walker prunes it and the nested rules become self-defeating.
		if r.scope != "" && !strings.HasPrefix(relPath, r.scope+"/") {
			continue
		}
		// Patterns in a nested ignore file are written relative to that file's own
		// directory, so match against the path with the scope prefix removed.
		cand := relPath
		if r.scope != "" {
			cand = strings.TrimPrefix(relPath, r.scope+"/")
		}
		if !ruleMatches(r, cand) {
			continue
		}
		// A dir-only rule (`build/`) excludes the directory AND everything under it.
		// Skipping it for non-dirs only works when the caller walks top-down and
		// prunes; a direct question about a file path (find_files, or a tool checking a
		// single path) would otherwise report a file inside an ignored directory as
		// visible.
		if r.dirOnly && !isDir && !ruleMatchesAncestor(r, cand) {
			continue
		}
		if r.negate {
			matched = false
		} else {
			matched = true
		}
	}
	return matched
}

// ruleMatches reports whether the rule's pattern matches relPath.
func ruleMatches(r ignoreRule, relPath string) bool {
	if r.anchored || r.hasSlash {
		return globMatch(r.pattern, relPath)
	}
	// Unanchored, no slash: match basename and any path segment.
	if globMatch(r.pattern, filepath.Base(relPath)) {
		return true
	}
	for _, seg := range strings.Split(relPath, "/") {
		if globMatch(r.pattern, seg) {
			return true
		}
	}
	return false
}

// ruleMatchesAncestor reports whether the rule matches any ANCESTOR directory of
// relPath. It's what makes a dir-only rule (`build/`) also hide the files inside it
// when a caller asks about one path directly rather than walking and pruning.
func ruleMatchesAncestor(r ignoreRule, relPath string) bool {
	segs := strings.Split(relPath, "/")
	for i := 1; i < len(segs); i++ {
		if globMatch(r.pattern, strings.Join(segs[:i], "/")) {
			return true
		}
		// Unanchored patterns match any single segment along the way.
		if !r.anchored && !r.hasSlash && globMatch(r.pattern, segs[i-1]) {
			return true
		}
	}
	return false
}

// globMatch is a small `**`-aware matcher; falls back to filepath.Match.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, name)
		return ok
	}
	parts := strings.Split(pattern, "**")
	rest := name
	for i, p := range parts {
		switch i {
		case 0:
			if p != "" && !strings.HasPrefix(rest, strings.TrimPrefix(p, "/")) {
				// Try treating ** as "any path"; match by relaxed prefix.
				if !strings.Contains(rest, p) {
					return false
				}
			}
			if p != "" {
				idx := strings.Index(rest, p)
				if idx < 0 {
					return false
				}
				rest = rest[idx+len(p):]
			}
		default:
			if p == "" {
				return true
			}
			idx := strings.Index(rest, strings.TrimSuffix(p, "/"))
			if idx < 0 {
				return false
			}
			rest = rest[idx+len(p):]
		}
	}
	return true
}
