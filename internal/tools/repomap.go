package tools

// repomap.go builds a compact, ignore-aware structural overview of the repository
// for the system prompt — so the agent can orient (where packages and entrypoints
// live) without globbing the tree first. It's static: a single ignore-aware walk,
// no model call, always current. Bounded in both the walk (repoMapMaxFiles) and
// the rendered output (repoMapMaxLines).

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

const (
	repoMapMaxFiles = 40000 // walk guard: past this the map is marked approximate
	repoMapMaxLines = 45    // rendered directory-line budget
	repoMapMaxSub   = 8     // max depth-2 subdirs shown per top-level dir
)

// rootMarkerFiles are project-defining files surfaced at the top of the map, in
// display-priority order (manifests / build / entrypoints / docs).
var rootMarkerFiles = []string{
	"go.mod", "go.work", "package.json", "tsconfig.json", "Cargo.toml",
	"pyproject.toml", "setup.py", "requirements.txt", "pom.xml", "build.gradle",
	"Gemfile", "composer.json", "Makefile", "Dockerfile", "docker-compose.yml",
	"main.go", "index.ts", "index.js", "README.md", "CHANGELOG.md",
	"AGENTS.md", "CLAUDE.md",
}

// RepoMap returns a compact structural overview of the repository: the detected
// stack, key root files, and a depth-2 directory tree with per-directory
// (recursive) file counts. Honors the same ignore rules as the search tools
// (.gitignore/.moonyignore + defaults), is static, and is bounded. Returns "" for
// an empty tree. Memoized per registry (computeRepoMap's whole-tree walk runs at
// most once) so repeated system-prompt rebuilds don't re-walk the tree.
func (r *Registry) RepoMap() string {
	r.repoMapMu.Lock()
	defer r.repoMapMu.Unlock()
	if !r.repoMapDone {
		r.repoMapStr = r.computeRepoMap()
		r.repoMapDone = true
	}
	return r.repoMapStr
}

// computeRepoMap performs the ignore-aware walk and renders the map. See RepoMap.
func (r *Registry) computeRepoMap() string {
	counts, children, rootFiles, total, truncated := r.walkTreeCounts()

	if total == 0 {
		return ""
	}

	var b strings.Builder
	approx := ""
	if truncated {
		approx = "+"
	}
	fmt.Fprintf(&b, "Repository map — %s", filepath.Base(r.Root()))
	if stack := detectStack(rootFiles); stack != "" {
		fmt.Fprintf(&b, " (%s)", stack)
	}
	fmt.Fprintf(&b, ", %d%s files. Structure (recursive file counts, ignore-aware):\n", total, approx)

	if markers := presentMarkers(rootFiles); markers != "" {
		fmt.Fprintf(&b, "  key files: %s\n", markers)
	}

	lines := 0
	top := sortedByCount(children[""], counts)
	for ti, dir := range top {
		if lines >= repoMapMaxLines {
			fmt.Fprintf(&b, "  … (+%d more top-level dirs)\n", len(top)-ti)
			break
		}
		fmt.Fprintf(&b, "  %s/ (%d)\n", dir, counts[dir])
		lines++
		subs := sortedByCount(children[dir], counts)
		for i, sub := range subs {
			if lines >= repoMapMaxLines {
				break
			}
			if i >= repoMapMaxSub {
				fmt.Fprintf(&b, "    … (+%d more)\n", len(subs)-repoMapMaxSub)
				lines++
				break
			}
			fmt.Fprintf(&b, "    %s/ (%d)\n", lastSeg(sub), counts[sub])
			lines++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// walkTreeCounts does one ignore-aware walk of the repo and returns recursive
// per-directory file counts, the immediate-children map for tree rendering, the
// set of root-level file names, the total file count, and whether the walk was
// truncated. Shared by RepoMap and the codebase-map generator.
func (r *Registry) walkTreeCounts() (counts map[string]int, children map[string]map[string]bool, rootFiles map[string]bool, total int, truncated bool) {
	counts = map[string]int{}
	children = map[string]map[string]bool{}
	rootFiles = map[string]bool{}

	// One guarded read of the root + ignore set for the whole walk rather than one
	// per visited file: cheaper, and every path in this walk is resolved against
	// the SAME root even if a concurrent /cd re-roots the registry midway.
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
		if ig.Match(rel, false) {
			return nil
		}
		total++
		if total > repoMapMaxFiles {
			truncated = true
			return filepath.SkipAll
		}
		dir := slashDir(rel)
		if dir == "" {
			rootFiles[rel] = true
		}
		for cur := dir; ; {
			counts[cur]++
			if cur == "" {
				break
			}
			parent := slashDir(cur)
			if children[parent] == nil {
				children[parent] = map[string]bool{}
			}
			children[parent][cur] = true
			cur = parent
		}
		return nil
	})
	return counts, children, rootFiles, total, truncated
}

// slashDir returns the parent directory of a slash path, or "" at the root.
func slashDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// lastSeg returns the final segment of a slash path.
func lastSeg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// sortedByCount returns the keys of set ordered by their file count (desc), then
// path (asc) — so the biggest, most relevant directories lead, deterministically.
func sortedByCount(set map[string]bool, counts map[string]int) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// presentMarkers lists the rootMarkerFiles present at the repo root, in priority
// order, comma-joined.
func presentMarkers(rootFiles map[string]bool) string {
	var m []string
	for _, f := range rootMarkerFiles {
		if rootFiles[f] {
			m = append(m, f)
		}
	}
	return strings.Join(m, ", ")
}

// detectStack infers the primary language/ecosystem from the root marker files.
func detectStack(rf map[string]bool) string {
	switch {
	case rf["go.mod"] || rf["go.work"]:
		return "Go"
	case rf["Cargo.toml"]:
		return "Rust"
	case rf["pyproject.toml"] || rf["setup.py"] || rf["requirements.txt"]:
		return "Python"
	case rf["package.json"]:
		return "Node/TypeScript"
	case rf["pom.xml"] || rf["build.gradle"]:
		return "Java/JVM"
	case rf["Gemfile"]:
		return "Ruby"
	case rf["composer.json"]:
		return "PHP"
	}
	return ""
}
