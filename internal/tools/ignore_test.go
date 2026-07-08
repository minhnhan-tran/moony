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
