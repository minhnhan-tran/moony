package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// newGateAgent builds an Agent with a snapshot store rooted at a temp dir, so
// the gate helpers can be exercised against real files on disk.
func newGateAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	a := &Agent{cfg: &config.Config{WorkingDir: dir}, snapshots: &snapshotStore{}}
	return a, dir
}

// writeAndSnapshot seeds a file with prior content, records the snapshot the way
// a mutating tool would, then writes the new content.
func writeAndSnapshot(t *testing.T, a *Agent, dir, rel, prior, current string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if prior != "" {
		if err := os.WriteFile(p, []byte(prior), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.SnapshotPath(p)
	if err := os.WriteFile(p, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGateSkipsReleaseMetadataTurn is the case that prompted this: a ship-it turn
// that only bumps const Version and writes the CHANGELOG must NOT burn a
// self-review model call.
func TestGateSkipsReleaseMetadataTurn(t *testing.T) {
	a, dir := newGateAgent(t)
	writeAndSnapshot(t, a, dir, "CHANGELOG.md",
		"## [1.0.0]\n", "## [1.0.1] - today\n\n### Fixed\n- thing\n\n## [1.0.0]\n")
	writeAndSnapshot(t, a, dir, "internal/config/config.go",
		"package config\n\nconst Version = \"1.0.0\"\n\nfunc F() int { return 1 }\n",
		"package config\n\nconst Version = \"1.0.1\"\n\nfunc F() int { return 1 }\n")

	skip, why := a.turnIsNonReviewable()
	if !skip {
		t.Fatal("a version bump + CHANGELOG turn should skip self-review")
	}
	if why == "" {
		t.Error("skip reason should be user-facing, got empty")
	}
}

// TestGateReviewsWhenRealCodeMixedIn pins the conservative half: a bump that
// rides along with an actual source change must still be reviewed.
func TestGateReviewsWhenRealCodeMixedIn(t *testing.T) {
	a, dir := newGateAgent(t)
	writeAndSnapshot(t, a, dir, "CHANGELOG.md", "## [1.0.0]\n", "## [1.0.1]\n\n## [1.0.0]\n")
	writeAndSnapshot(t, a, dir, "internal/config/config.go",
		"package config\n\nconst Version = \"1.0.0\"\n", "package config\n\nconst Version = \"1.0.1\"\n")
	writeAndSnapshot(t, a, dir, "internal/agent/thing.go",
		"package agent\n\nfunc F() int { return 1 }\n",
		"package agent\n\nfunc F() int { return 2 }\n")

	if skip, _ := a.turnIsNonReviewable(); skip {
		t.Fatal("a turn touching real source must still be reviewed")
	}
}

// TestGateReviewsNonVersionChangeInConfig guards the narrowest edge: config.go is
// only exempt for the const Version line. Any other edit to it is real code.
func TestGateReviewsNonVersionChangeInConfig(t *testing.T) {
	a, dir := newGateAgent(t)
	writeAndSnapshot(t, a, dir, "internal/config/config.go",
		"package config\n\nconst Version = \"1.0.0\"\n\nfunc F() int { return 1 }\n",
		"package config\n\nconst Version = \"1.0.1\"\n\nfunc F() int { return 42 }\n")

	if skip, _ := a.turnIsNonReviewable(); skip {
		t.Fatal("a logic change alongside the bump in config.go must be reviewed")
	}
}

// TestGateSkipsDocsOnlyTurn covers the broadened non-reviewable set.
func TestGateSkipsDocsOnlyTurn(t *testing.T) {
	a, dir := newGateAgent(t)
	writeAndSnapshot(t, a, dir, "docs/FEATURES.md", "old\n", "new\n")
	writeAndSnapshot(t, a, dir, "README.md", "a\n", "b\n")

	if skip, _ := a.turnIsNonReviewable(); !skip {
		t.Fatal("a docs-only turn should skip self-review")
	}
}

// TestGateRevertedEditIsNotAChange ensures the gate agrees with combinedTurnDiff:
// a file edited then restored has no net diff and must not count as changed (and
// so must not, by itself, make a turn look reviewable).
func TestGateRevertedEditIsNotAChange(t *testing.T) {
	a, dir := newGateAgent(t)
	const same = "package agent\n\nfunc F() int { return 1 }\n"
	writeAndSnapshot(t, a, dir, "internal/agent/thing.go", same, same)

	if got := a.changedRelPaths(); len(got) != 0 {
		t.Fatalf("net no-op edit should report no changed paths, got %v", got)
	}
}

// TestGateEmptyTurnDefersToDiffGate: with nothing changed the helper reports
// "don't skip", leaving the pre-existing empty-diff check to end the turn. This
// keeps the two gates from disagreeing.
func TestGateEmptyTurnDefersToDiffGate(t *testing.T) {
	a, _ := newGateAgent(t)
	if skip, _ := a.turnIsNonReviewable(); skip {
		t.Fatal("an empty turn must defer to the empty-diff gate, not claim a skip")
	}
}

func TestMatchesSkipGlob(t *testing.T) {
	cases := []struct {
		rel, glob string
		want      bool
	}{
		{"README.md", "*.md", true},
		{"docs/FEATURES.md", "*.md", true}, // bare pattern also matches basename
		{"internal/agent/a.go", "*.md", false},
		{"go.sum", "go.sum", true},
		{"internal/tools/testdata/x.json", "**/testdata/**", true},
		{"internal/tools/x.json", "**/testdata/**", false},
		{"vendor/foo/bar.go", "**/vendor/**", true},
		{"docs/a/b.md", "docs/**", true},
		{"docs", "docs/**", true},
		{"docsx/a.md", "docs/**", false},
		{"a.go", "", false},
	}
	for _, c := range cases {
		if got := matchesSkipGlob(c.rel, c.glob); got != c.want {
			t.Errorf("matchesSkipGlob(%q, %q) = %v, want %v", c.rel, c.glob, got, c.want)
		}
	}
}

func TestVersionBumpOnly(t *testing.T) {
	base := "package config\n\nconst Version = \"1.0.0\"\n\nfunc F() int { return 1 }\n"
	bumped := "package config\n\nconst Version = \"1.0.1\"\n\nfunc F() int { return 1 }\n"
	if !versionBumpOnly(base, bumped) {
		t.Error("a lone version bump should be recognized")
	}
	logic := "package config\n\nconst Version = \"1.0.1\"\n\nfunc F() int { return 2 }\n"
	if versionBumpOnly(base, logic) {
		t.Error("bump + logic change must not be treated as a lone bump")
	}
	if versionBumpOnly(base, base) {
		t.Error("no change is not a bump")
	}
	if versionBumpOnly("", bumped) {
		t.Error("file creation is not a bump")
	}
}

// TestSelfReviewSkipGlobsEnvExtends verifies the env knob ADDS to the defaults
// rather than replacing them, so a repo needn't restate the built-ins.
func TestSelfReviewSkipGlobsEnvExtends(t *testing.T) {
	t.Setenv(selfReviewSkipGlobsEnv, "third_party/**, *.pb.go")
	globs := selfReviewSkipGlobs()

	var hasDefault, hasCustom, hasSecond bool
	for _, g := range globs {
		switch g {
		case "*.md":
			hasDefault = true
		case "third_party/**":
			hasCustom = true
		case "*.pb.go":
			hasSecond = true
		}
	}
	if !hasDefault {
		t.Error("env knob must extend the defaults, not replace them")
	}
	if !hasCustom || !hasSecond {
		t.Errorf("both comma-separated globs should be parsed and trimmed, got %v", globs)
	}
}
