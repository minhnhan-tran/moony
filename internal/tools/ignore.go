package tools

// .sqeignore: a small subset of gitignore semantics for the agent's read-side
// tools (glob, grep, list_dir). Reads `.sqeignore` and `.gitignore` from the
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
)

// defaultIgnorePatterns are always applied. They mirror what a sensible
// .gitignore in any repo would carry.
var defaultIgnorePatterns = []string{
	".git/", ".hg/", ".svn/",
	".sqe/",
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
}

// IgnoreSet holds the rules from one ignore root.
type IgnoreSet struct {
	rules []ignoreRule
	root  string
}

// LoadIgnore reads `.sqeignore` and `.gitignore` (in that order) from root and
// returns a combined set, preceded by defaultIgnorePatterns. A missing file is
// not an error.
func LoadIgnore(root string) *IgnoreSet {
	set := &IgnoreSet{root: root}
	for _, p := range defaultIgnorePatterns {
		set.add(p)
	}
	for _, name := range []string{".gitignore", ".sqeignore"} {
		_ = set.appendFile(filepath.Join(root, name))
	}
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

// add canonicalizes one raw pattern and appends a rule.
func (s *IgnoreSet) add(raw string) {
	r := ignoreRule{}
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
		if r.dirOnly && !isDir {
			continue
		}
		if !ruleMatches(r, relPath) {
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
