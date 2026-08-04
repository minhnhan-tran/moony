package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreSetDefaultsAndCustomPatterns(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".sqeignore"), []byte(`
# comment
secrets/
*.tmp
!keep.tmp
`), 0o644); err != nil {
		t.Fatal(err)
	}

	set := LoadIgnore(dir)

	cases := []struct {
		path  string
		isDir bool
		want  bool
		desc  string
	}{
		{".git", true, true, "default .git directory is pruned"},
		{"node_modules", true, true, "default node_modules directory is pruned"},
		{"src/main.go", false, false, "regular source file"},
		{"src/secrets", true, true, "custom secrets/ pattern (dir)"},
		{"build.tmp", false, true, "*.tmp matches"},
		{"keep.tmp", false, false, "negation re-includes keep.tmp"},
	}
	for _, c := range cases {
		got := set.Match(c.path, c.isDir)
		if got != c.want {
			t.Errorf("%s: Match(%q, isDir=%v) = %v, want %v", c.desc, c.path, c.isDir, got, c.want)
		}
	}
}

func TestIgnoreSetReadsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("hidden.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := LoadIgnore(dir)
	if !set.Match("hidden.txt", false) {
		t.Error("expected .gitignore patterns to apply")
	}
}

// A nested .gitignore must be honoured, and scoped to its own subtree the way git
// scopes it. Reading only the root file meant a vendored subtree excluded by a
// nested ignore file was walked and indexed as if it were project source.
func TestNestedIgnoreScopedToItsOwnSubtree(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// editors/vscode/.gitignore excludes .vscode-test/ and out/ — but only there.
	mk("editors/vscode/.gitignore")
	if err := os.WriteFile(filepath.Join(root, "editors/vscode/.gitignore"),
		[]byte(".vscode-test/\nout/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk("editors/vscode/src/extension.ts")
	mk("editors/vscode/out/bundle.js")
	mk("internal/web/out/keep.js")

	ig := LoadIgnore(root)

	if !ig.Match("editors/vscode/out", true) {
		t.Error("nested .gitignore rule `out/` did not exclude editors/vscode/out")
	}
	// The critical scoping property: the nested rule must NOT leak repo-wide.
	if ig.Match("internal/web/out", true) {
		t.Error("nested rule leaked out of its subtree and matched internal/web/out")
	}
	// Real source in the same subtree stays visible.
	if ig.Match("editors/vscode/src/extension.ts", false) {
		t.Error("nested ignore file wrongly excluded real source in its own subtree")
	}
}

// A dir-only rule (`build/`) must also hide the FILES inside it. Callers that walk
// top-down prune the directory, but a caller asking about one path directly (a tool
// checking a single file) would otherwise be told the file is visible.
func TestDirOnlyRuleHidesFilesUnderneath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".sqeignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig := LoadIgnore(root)

	if !ig.Match("build", true) {
		t.Fatal("dir-only rule did not match the directory itself")
	}
	if !ig.Match("build/out.o", false) {
		t.Error("a file inside an ignored dir-only directory was reported as visible")
	}
	if !ig.Match("build/nested/deep/x.js", false) {
		t.Error("a deeply nested file inside an ignored directory was reported as visible")
	}
	// A same-named file (not directory) must still be unaffected.
	if ig.Match("buildfile.go", false) {
		t.Error("dir-only rule wrongly matched an unrelated file")
	}
}

// A nested ignore file governs the CONTENTS of its directory, never the directory
// itself. A catch-all like `*` must not make the scope dir ignore itself, or walkers
// prune the whole subtree and the nested rules become self-defeating.
func TestNestedIgnoreCatchAllDoesNotHideItsOwnDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "editors", "vscode")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real-world pattern: ignore everything, then re-include sources.
	if err := os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig := LoadIgnore(root)
	if ig.Match("editors/vscode", true) {
		t.Error("a nested `*` rule hid its OWN directory; the subtree gets pruned entirely")
	}
	if !ig.Match("editors/vscode/anything.txt", false) {
		t.Error("the nested `*` rule failed to apply to contents")
	}
}
