package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadForMention returns numbered content for a real text file under the root,
// and refuses directories, binary files, missing files, and paths that escape
// the workspace.
func TestReadForMention(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{'a', 0x00, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Default(dir, 30000)

	got, ok := r.ReadForMention("hello.txt")
	if !ok {
		t.Fatal("expected ok for a real text file")
	}
	if !strings.Contains(got, "1: alpha") || !strings.Contains(got, "2: beta") {
		t.Errorf("expected read_file-style numbered lines, got %q", got)
	}

	for _, bad := range []string{"bin.dat", "sub", "nope.txt", "../escape.txt"} {
		if _, ok := r.ReadForMention(bad); ok {
			t.Errorf("ReadForMention(%q) = ok, want skipped", bad)
		}
	}
}
