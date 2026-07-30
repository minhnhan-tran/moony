package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// TestTouchedFiles: TouchedFiles returns the distinct files this session edited,
// relative to WorkingDir, for cross-session commit attribution.
func TestTouchedFiles(t *testing.T) {
	dir := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: dir}, nil, tools.NewRegistry(dir), nil, nil)

	// Simulate the file tools snapshotting edits (record is what write/edit call).
	foo := filepath.Join(dir, "foo.go")
	sub := filepath.Join(dir, "pkg", "bar.go")
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(foo, []byte("x"), 0o644)
	os.WriteFile(sub, []byte("y"), 0o644)
	a.snapshots.record(foo)
	a.snapshots.record(sub)
	a.snapshots.record(foo) // duplicate across a later "turn" must dedupe
	a.snapshots.turn++
	a.snapshots.record(foo)

	got := a.TouchedFiles()
	want := map[string]bool{"foo.go": true, filepath.Join("pkg", "bar.go"): true}
	if len(got) != len(want) {
		t.Fatalf("TouchedFiles = %v, want 2 distinct relative paths", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected path %q in %v", g, got)
		}
	}
}
