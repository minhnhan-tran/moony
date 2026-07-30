package tools

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Codebase-map generator. Reuses the pure-Go symbol map (buildSymbolMap +
// cross-file caller graph) to emit a deterministic, readable structural view of
// the project — a directory listing with per-file top symbols (kind, line, and
// inbound reference counts) — that is written into the AUTO:STRUCTURE section of
// CODEBASE_MAP.md and refreshed whenever the codebase changes (see the symbol-map
// background rebuild). No LLM is involved, so it is cheap and never stale.
//
// Design constraints, in priority order:
//  1. Signal density — the section shares a bounded prompt budget with the
//     hand-written narrative, so every byte must earn its place. Test files,
//     build output and interface boilerplate are filtered out.
//  2. Determinism — the file is committed and auto-refreshed on every codebase
//     change, so output must be stable under unrelated edits (alphabetical
//     ordering, never score order) to avoid churning git diffs.
//  3. Graceful degradation — output is fitted to a byte budget by progressively
//     shedding detail, so it is always coherent rather than truncated mid-line.

const (
	codebaseMapMaxFilesPerDir = 40 // cap files listed per directory
	codebaseMapMaxSymsPerFile = 10 // cap symbols listed per file
	codebaseMapMaxDirs        = 400
	// codebaseMapKeyFileRefs is the inbound-reference count at/above which a file
	// is flagged as load-bearing.
	codebaseMapKeyFileRefs = 25
	// codebaseMapBudgetBytes is the target size for the FULL rendered section on
	// disk. It no longer competes with the system prompt (which gets the compact
	// digest instead), so it is generous — it only guards against pathological
	// repos. Keep it at/above what the most degraded rendering produces, or the
	// fitting loop bottoms out and silently ships minimum detail.
	codebaseMapBudgetBytes = 60000
)

// codebaseMapSkipDirSegs are path segments that hold generated/compiled output
// rather than source. They may not be covered by ignore rules (e.g. a committed
// editors/vscode/out/), but listing minified build artifacts as "key symbols" is
// pure noise.
var codebaseMapSkipDirSegs = map[string]bool{
	"out": true, "dist": true, "build": true, "bin": true, "obj": true,
	"target": true, "vendor": true, "node_modules": true, "coverage": true,
	"__pycache__": true, ".next": true, ".nuxt": true, "gen": true,
	"generated": true, "migrations": true, "testdata": true, "fixtures": true,
	"__tests__": true, "__snapshots__": true,
}

// skipInCodebaseMap reports whether a file should be omitted from the rendered
// map. It is a RENDER-time filter only: these files stay in the symbol map so
// find_symbol/find_references still resolve them.
func skipInCodebaseMap(rel string) bool {
	base := path.Base(rel)
	lower := strings.ToLower(base)

	// Tests and specs: high volume, low orientation value.
	for _, marker := range []string{"_test.", "_tests.", ".test.", ".spec.", "_spec."} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.HasPrefix(lower, "test_") || strings.HasPrefix(lower, "conftest.") {
		return true
	}
	// Minified / generated bundles.
	for _, marker := range []string{".min.", ".bundle.", ".pb.", "_pb2.", ".g.", ".generated."} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, seg := range strings.Split(path.Dir(rel), "/") {
		if codebaseMapSkipDirSegs[seg] {
			return true
		}
	}
	return false
}

// boilerplateNames are names so widely implemented (interface methods, ctors,
// stringers) that listing them tells the reader little about a specific file.
// They are DEMOTED, not dropped: a name here still wins its slot when it is the
// most-referenced thing in its file (see chooseSyms), because for an entry point
// like main.go's Set or agent.go's New the common name really is the headline.
var boilerplateNames = map[string]bool{
	"Name": true, "Execute": true, "Schema": true, "Mutates": true,
	"Description": true, "String": true, "Error": true,
	"Len": true, "Less": true, "Swap": true,
	"Init": true, "ServeHTTP": true, "MarshalJSON": true, "UnmarshalJSON": true,
	"Equal": true, "Clone": true, "Copy": true,
}

// cbmFile is a file's rendered payload.
type cbmFile struct {
	rel     string
	syms    []SymbolEntry // candidate pool, then the chosen display set
	total   int           // total symbols defined here
	topRefs int           // highest inbound reference count in this file
}

// CodebaseMapStructural renders the always-fresh structural section of the
// codebase map from the memoized symbol map: a project summary, then a
// directory-grouped listing where each significant source file shows its
// highest-signal definitions with kind, line and inbound reference count.
// Returns "" when the map isn't built yet.
//
// This is the FULL tier — written to CODEBASE_MAP.md for humans, @map and the
// codebase_map tool. The system prompt gets the compact digest instead (see
// CodebaseMapDigest), so detail on disk is no longer limited by prompt budget.
func (r *Registry) CodebaseMapStructural() string {
	dirs, byDir, meta, ok := r.codebaseMapModel()
	if !ok {
		return ""
	}
	for _, symCap := range []int{codebaseMapMaxSymsPerFile, 6, 4, 2} {
		out := renderCodebaseMap(dirs, byDir, meta, symCap)
		if len(out) <= codebaseMapBudgetBytes || symCap == 2 {
			return out
		}
	}
	return ""
}

// CodebaseMapDigest renders the COMPACT tier: one line per directory listing its
// file count and its load-bearing files, with no per-file symbol detail. This is
// what goes in the system prompt — it orients the model (which package holds
// what) in a few KB, and it can call the codebase_map tool for a directory's full
// symbol listing when it actually needs it.
func (r *Registry) CodebaseMapDigest() string {
	dirs, byDir, meta, ok := r.codebaseMapModel()
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("Compact index of the codebase, generated and refreshed on change. ")
	b.WriteString("Use it to pick the right directory, then call the `codebase_map` tool with that directory for its full per-file symbol listing (kinds, lines, reference counts).\n\n")
	writeCodebaseMapHeader(&b, meta)

	for i, d := range dirs {
		if i >= codebaseMapMaxDirs {
			fmt.Fprintf(&b, "- _… %d more directories_\n", len(dirs)-i)
			break
		}
		fs := byDir[d]
		label := d
		if label == "" {
			label = "(project root)"
		} else {
			label += "/"
		}
		fmt.Fprintf(&b, "- `%s` (%s)", label, plural(len(fs), "file"))

		// Name the load-bearing files: that is what makes the digest actionable.
		ranked := make([]*cbmFile, len(fs))
		copy(ranked, fs)
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].topRefs != ranked[j].topRefs {
				return ranked[i].topRefs > ranked[j].topRefs
			}
			return ranked[i].rel < ranked[j].rel
		})
		var key []string
		for _, f := range ranked {
			if f.topRefs < codebaseMapKeyFileRefs || len(key) >= 5 {
				break
			}
			key = append(key, path.Base(f.rel))
		}
		if len(key) > 0 {
			b.WriteString(" — " + strings.Join(key, ", "))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// CodebaseMapForDir renders the full per-file symbol listing for ONE directory
// (exact repo-relative path, "" for the project root), for the codebase_map tool.
// Returns "" when the directory has no mapped source files.
func (r *Registry) CodebaseMapForDir(dir string) string {
	dir = strings.Trim(strings.TrimSpace(filepath.ToSlash(dir)), "/")
	_, byDir, meta, ok := r.codebaseMapModel()
	if !ok {
		return ""
	}
	fs := byDir[dir]
	if len(fs) == 0 {
		return ""
	}
	// Full detail: this tier is fetched deliberately, so don't degrade it.
	return renderCodebaseMap([]string{dir}, map[string][]*cbmFile{dir: fs}, meta, codebaseMapMaxSymsPerFile)
}

// CodebaseMapDirs lists the mapped directories, so the codebase_map tool can
// report valid choices when given an unknown one.
func (r *Registry) CodebaseMapDirs() []string {
	dirs, _, _, ok := r.codebaseMapModel()
	if !ok {
		return nil
	}
	return dirs
}

// cbmMeta carries the project-summary facts shared by both tiers.
type cbmMeta struct {
	root      string
	counts    map[string]int
	rootFiles map[string]bool
	total     int
	truncated bool
	indexed   int
}

// codebaseMapModel builds the shared, filtered, deterministically-ordered view
// that every tier renders from.
func (r *Registry) codebaseMapModel() (dirs []string, byDir map[string][]*cbmFile, meta cbmMeta, ok bool) {
	m := r.SymbolMap()
	if m == nil || len(m.Entries) == 0 {
		return nil, nil, meta, false
	}
	counts, _, rootFiles, total, truncated := r.walkTreeCounts()

	byFile := map[string]*cbmFile{}
	for _, e := range m.Entries {
		if skipInCodebaseMap(e.File) {
			continue
		}
		f := byFile[e.File]
		if f == nil {
			f = &cbmFile{rel: e.File}
			byFile[e.File] = f
		}
		f.total++
		if e.RefCount > f.topRefs {
			f.topRefs = e.RefCount
		}
		if len(f.syms) < codebaseMapMaxSymsPerFile*3 { // keep a pool to choose from
			f.syms = append(f.syms, e)
		}
	}
	if len(byFile) == 0 {
		return nil, nil, meta, false
	}
	for _, f := range byFile {
		f.syms = chooseSyms(f.syms, codebaseMapMaxSymsPerFile)
	}

	byDir = map[string][]*cbmFile{}
	for _, f := range byFile {
		d := path.Dir(f.rel)
		if d == "." {
			d = ""
		}
		byDir[d] = append(byDir[d], f)
	}
	for d, fs := range byDir {
		dirs = append(dirs, d)
		sort.Slice(fs, func(i, j int) bool { return fs[i].rel < fs[j].rel })
	}
	sort.Strings(dirs)

	return dirs, byDir, cbmMeta{
		root: r.root, counts: counts, rootFiles: rootFiles,
		total: total, truncated: truncated, indexed: len(byFile),
	}, true
}

// writeCodebaseMapHeader emits the shared project-summary lines.
func writeCodebaseMapHeader(b *strings.Builder, meta cbmMeta) {
	approx := ""
	if meta.truncated {
		approx = "+"
	}
	b.WriteString("**Project:** `" + filepath.Base(meta.root) + "`")
	if stack := detectStack(meta.rootFiles); stack != "" {
		b.WriteString(" · " + stack)
	}
	if meta.total > 0 {
		fmt.Fprintf(b, " · %d%s files", meta.total, approx)
	}
	fmt.Fprintf(b, " · %d source files mapped\n", meta.indexed)
	if markers := presentMarkers(meta.rootFiles); markers != "" {
		b.WriteString("**Key files:** " + markers + "\n")
	}
	b.WriteByte('\n')
}

func renderCodebaseMap(dirs []string, byDir map[string][]*cbmFile, meta cbmMeta, symCap int) string {
	var b strings.Builder
	b.WriteString("Generated from the codebase and refreshed on change. Per directory: the significant source files and each file's key definitions. ")
	b.WriteString("Tests and build output are omitted. Consult this before exploring; it is ground truth for structure.\n\n")
	writeCodebaseMapHeader(&b, meta)
	b.WriteString("_`kind:line` = where it's declared · `N refs` = files referencing it (`+` = more) · ⭐ = load-bearing file._\n\n")

	for i, d := range dirs {
		if i >= codebaseMapMaxDirs {
			fmt.Fprintf(&b, "_… %d more directories omitted._\n", len(dirs)-i)
			break
		}
		fs := byDir[d]
		label := d
		if label == "" {
			label = "(project root)"
		} else {
			label += "/"
		}
		if c := meta.counts[d]; c > 0 && d != "" {
			fmt.Fprintf(&b, "### %s — %s\n", label, plural(c, "file"))
		} else {
			fmt.Fprintf(&b, "### %s\n", label)
		}
		if hint := dirRoleHint(fs); hint != "" {
			b.WriteString("_" + hint + "_\n")
		}
		for j, f := range fs {
			if j >= codebaseMapMaxFilesPerDir {
				fmt.Fprintf(&b, "- _… %d more files_\n", len(fs)-j)
				break
			}
			b.WriteString("- `" + path.Base(f.rel) + "`")
			if f.topRefs >= codebaseMapKeyFileRefs {
				b.WriteString(" ⭐")
			}
			syms := f.syms
			if len(syms) > symCap {
				syms = syms[:symCap]
			}
			if len(syms) > 0 {
				parts := make([]string, 0, len(syms)+1)
				for _, s := range syms {
					parts = append(parts, renderSym(s))
				}
				if f.total > len(syms) {
					parts = append(parts, fmt.Sprintf("+%d more", f.total-len(syms)))
				}
				b.WriteString(" — " + strings.Join(parts, ", "))
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderSym formats one symbol as "Name (func:412, 25+ refs)", omitting parts
// that aren't known so it degrades to a bare name.
func renderSym(s SymbolEntry) string {
	var meta []string
	switch {
	case s.Kind != "" && s.Line > 0:
		meta = append(meta, fmt.Sprintf("%s:%d", s.Kind, s.Line))
	case s.Kind != "":
		meta = append(meta, s.Kind)
	case s.Line > 0:
		meta = append(meta, fmt.Sprintf(":%d", s.Line))
	}
	if s.RefCount > 1 {
		plus := ""
		if s.CallersTruncated() {
			plus = "+"
		}
		meta = append(meta, fmt.Sprintf("%d%s refs", s.RefCount, plus))
	}
	if len(meta) == 0 {
		return s.Name
	}
	return s.Name + " (" + strings.Join(meta, ", ") + ")"
}

// dirRoleHint summarizes a directory from its file names, so the reader can pick
// the right package without scanning every line. Returns "" when it would just
// restate the listing.
func dirRoleHint(fs []*cbmFile) string {
	if len(fs) < 4 {
		return ""
	}
	// Surface the most-referenced files as the directory's entry points.
	ranked := make([]*cbmFile, len(fs))
	copy(ranked, fs)
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].topRefs != ranked[j].topRefs {
			return ranked[i].topRefs > ranked[j].topRefs
		}
		return ranked[i].rel < ranked[j].rel
	})
	var names []string
	for _, f := range ranked {
		if f.topRefs < codebaseMapKeyFileRefs {
			break
		}
		names = append(names, strings.TrimSuffix(path.Base(f.rel), path.Ext(f.rel)))
		if len(names) == 3 {
			break
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "entry points: " + strings.Join(names, ", ")
}

// chooseSyms picks up to n symbols to display for a file. Boilerplate names are
// demoted by ranking them below distinctive ones of comparable reference count,
// rather than excluded outright — dropping them entirely hid the real headline of
// entry-point files. Ties break on name for deterministic output.
func chooseSyms(pool []SymbolEntry, n int) []SymbolEntry {
	ranked := make([]SymbolEntry, len(pool))
	copy(ranked, pool)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		ai, bi := boilerplateNames[a.Name], boilerplateNames[b.Name]
		if ai != bi {
			// A demoted name only outranks a distinctive one if it is
			// substantially more referenced.
			if ai {
				return a.RefCount > b.RefCount*3
			}
			return !(b.RefCount > a.RefCount*3)
		}
		if a.RefCount != b.RefCount {
			return a.RefCount > b.RefCount
		}
		return a.Name < b.Name
	})
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	return ranked
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
