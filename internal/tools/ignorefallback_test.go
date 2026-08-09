package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// .moonyignore is the current name, but .sqeignore must keep working: it is a file
// users hand-author and commit to their own repositories, so there is nothing for us
// to migrate and silently ignoring it would break existing checkouts.
func TestBothAgentIgnoreFilesAreHonoured(t *testing.T) {
	cases := []struct{ name, file string }{
		{"current", ".moonyignore"},
		{"legacy", ".sqeignore"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte("secret.txt\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"secret.txt", "public.txt"} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			set := LoadIgnore(dir)
			if !set.Match("secret.txt", false) {
				t.Errorf("%s was not honoured: secret.txt should be ignored", tc.file)
			}
			if set.Match("public.txt", false) {
				t.Error("public.txt should not be ignored")
			}
		})
	}
}

// The project config dir is ignored under both names, so an un-migrated checkout
// does not start exposing its config to file listings and searches.
func TestProjectConfigDirsAreIgnored(t *testing.T) {
	set := LoadIgnore(t.TempDir())
	for _, d := range []string{".moony", ".sqe"} {
		if !set.Match(d, true) {
			t.Errorf("%s/ should be ignored by default", d)
		}
	}
}
