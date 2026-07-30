package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/diff"
)

// Smart self-review gating: a turn can produce a real textual diff that still has
// nothing a bug-reviewer can act on — bumping const Version, writing the
// CHANGELOG entry for a release, editing docs. Reviewing those costs a model call
// and turnaround for a guaranteed "no issues found". These helpers classify the
// turn's changed files so maybeSelfReview can skip when EVERY changed file is
// non-reviewable, while still reviewing the moment one real source file is mixed in.

// selfReviewSkipGlobsEnv lets a repo tune the non-reviewable set without a code
// change: a comma-separated list of globs matched against each changed file's
// repo-relative slash path (e.g. "docs/**,*.md,third_party/**").
const selfReviewSkipGlobsEnv = "SC_SELFREVIEW_SKIP_GLOBS"

// defaultSelfReviewSkipGlobs are the paths that never carry reviewable logic.
// Deliberately conservative: prose, release notes, lockfiles, and generated or
// vendored trees. Anything not listed here is treated as reviewable source.
var defaultSelfReviewSkipGlobs = []string{
	"*.md",
	"*.markdown",
	"*.txt",
	"*.lock",
	"go.sum",
	"**/testdata/**",
	"**/vendor/**",
	"**/node_modules/**",
}

// changedRelPaths returns the repo-relative slash paths of files whose contents
// NET changed this turn. It mirrors combinedTurnDiff's per-file emptiness check so
// the two agree on what "changed" means: a file edited and then reverted within
// the turn produces no diff and is not reported here.
func (a *Agent) changedRelPaths() []string {
	if a.snapshots == nil {
		return nil
	}
	var out []string
	for _, c := range a.snapshots.currentTurnChanges() {
		old := ""
		if !c.missing {
			old = string(c.prior)
		}
		newText := ""
		if data, err := os.ReadFile(c.path); err == nil {
			newText = string(data)
		}
		d, _ := diff.Unified(old, newText)
		if strings.TrimSpace(d) == "" { // net no-op (edited then reverted)
			continue // net no-op
		}
		out = append(out, filepath.ToSlash(mustRel(a.cfg.WorkingDir, c.path)))
	}
	return out
}

// selfReviewSkipGlobs returns the active non-reviewable globs: the defaults, plus
// anything in SC_SELFREVIEW_SKIP_GLOBS. Setting the env var ADDS to the defaults
// rather than replacing them, so a repo can extend the set without having to
// restate the built-ins.
func selfReviewSkipGlobs() []string {
	globs := defaultSelfReviewSkipGlobs
	extra := strings.TrimSpace(os.Getenv(selfReviewSkipGlobsEnv))
	if extra == "" {
		return globs
	}
	out := append([]string(nil), globs...)
	for _, g := range strings.Split(extra, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, filepath.ToSlash(g))
		}
	}
	return out
}

// matchesSkipGlob reports whether a repo-relative slash path matches a glob.
// Supports a leading "**/" (match at any depth) and a trailing "/**" (match
// everything under a directory) on top of filepath.Match's syntax, which alone
// can't cross separators.
func matchesSkipGlob(rel, glob string) bool {
	if glob == "" {
		return false
	}
	// "**/x/**" — anywhere in the path.
	if inner := strings.TrimSuffix(strings.TrimPrefix(glob, "**/"), "/**"); inner != glob && inner != "" && !strings.ContainsAny(inner, "*?") {
		for _, seg := range strings.Split(rel, "/") {
			if seg == inner {
				return true
			}
		}
		return false
	}
	if rest := strings.TrimPrefix(glob, "**/"); rest != glob {
		// Match the basename, or any suffix of the path starting at a segment.
		if ok, _ := filepath.Match(rest, filepath.Base(rel)); ok {
			return true
		}
		segs := strings.Split(rel, "/")
		for i := range segs {
			if ok, _ := filepath.Match(rest, strings.Join(segs[i:], "/")); ok {
				return true
			}
		}
		return false
	}
	if dir := strings.TrimSuffix(glob, "/**"); dir != glob {
		return rel == dir || strings.HasPrefix(rel, dir+"/")
	}
	if ok, _ := filepath.Match(glob, rel); ok {
		return true
	}
	// A bare pattern with no separator also matches on basename, so "*.md" hits
	// nested docs without the caller writing "**/*.md".
	if !strings.Contains(glob, "/") {
		if ok, _ := filepath.Match(glob, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}

// versionBumpOnly reports whether a Go file's net change is confined to the
// canonical `const Version = "x.y.z"` line. That single line is release metadata,
// not logic — a bump alone has nothing to review — but the same file also holds
// real configuration code, so we must NOT skip it wholesale: any other changed
// line in it makes the file reviewable again.
func versionBumpOnly(prior, current string) bool {
	if prior == "" || current == "" {
		return false // creation or deletion is never "just a bump"
	}
	d, _ := diff.Unified(prior, current)
	if strings.TrimSpace(d) == "" {
		return false
	}
	var changed int
	for _, line := range strings.Split(d, "\n") {
		if len(line) == 0 {
			continue
		}
		// Skip unified-diff framing; only +/- content lines matter.
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
			continue
		}
		if line[0] != '+' && line[0] != '-' {
			continue
		}
		body := strings.TrimSpace(line[1:])
		if body == "" {
			continue
		}
		if !strings.HasPrefix(body, "const Version") {
			return false // a non-version line changed — review it
		}
		changed++
	}
	return changed > 0
}

// turnIsNonReviewable reports whether EVERY file that net-changed this turn is
// release metadata or otherwise non-reviewable, so self-review would burn a model
// call to confirm nothing. Returns the reason to show the user. False (with "")
// as soon as one real source change is present — mixing a version bump into a
// code turn still gets reviewed.
func (a *Agent) turnIsNonReviewable() (bool, string) {
	rels := a.changedRelPaths()
	if len(rels) == 0 {
		return false, "" // nothing changed: the empty-diff gate handles this
	}
	globs := selfReviewSkipGlobs()
	sawBump := false
	for _, rel := range rels {
		skipped := false
		for _, g := range globs {
			if matchesSkipGlob(rel, g) {
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		if strings.HasSuffix(rel, ".go") && a.fileChangeIsVersionBump(rel) {
			sawBump = true
			continue
		}
		return false, "" // a genuinely reviewable file changed
	}
	if sawBump {
		return true, "only a version bump and release metadata changed"
	}
	return true, "only non-code files changed"
}

// fileChangeIsVersionBump re-reads the turn's snapshot for a repo-relative path
// and reports whether its net change is just the const Version line.
func (a *Agent) fileChangeIsVersionBump(rel string) bool {
	if a.snapshots == nil {
		return false
	}
	for _, c := range a.snapshots.currentTurnChanges() {
		if filepath.ToSlash(mustRel(a.cfg.WorkingDir, c.path)) != rel {
			continue
		}
		if c.missing {
			return false // newly created file
		}
		data, err := os.ReadFile(c.path)
		if err != nil {
			return false
		}
		return versionBumpOnly(string(c.prior), string(data))
	}
	return false
}
