package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
)

// TestTurnChangesDiffAndRevert covers #3's user-facing pieces: a combined
// unified diff of everything the current turn touched (across multiple files),
// and an atomic revert (Undo) that restores them all — an edited file back to
// its prior content and a newly-created file removed.
func TestTurnChangesDiffAndRevert(t *testing.T) {
	dir := t.TempDir()
	edited := filepath.Join(dir, "edited.go")
	created := filepath.Join(dir, "created.go")
	if err := os.WriteFile(edited, []byte("package p\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
		sess:      &session.Session{},
	}

	// One turn that mutates two files: edit `edited`, create `created`.
	a.snapshots.startTurn()
	a.SnapshotPath(edited)
	os.WriteFile(edited, []byte("package p\n\nfunc A() int { return 2 }\n"), 0o644)
	a.SnapshotPath(created)
	os.WriteFile(created, []byte("package p\n\nfunc B() {}\n"), 0o644)

	diff := a.TurnChangesDiff(0)
	for _, want := range []string{"edited.go", "created.go", "return 1", "return 2"} {
		if !strings.Contains(diff, want) {
			t.Errorf("combined diff missing %q; got:\n%s", want, diff)
		}
	}

	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(edited); string(got) != "package p\n\nfunc A() int { return 1 }\n" {
		t.Errorf("edited.go not restored: %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("created.go should have been removed by revert, stat err = %v", err)
	}

	// After revert there are no current-turn changes to show.
	if d := a.TurnChangesDiff(0); strings.TrimSpace(d) != "" {
		t.Errorf("expected empty diff after revert, got:\n%s", d)
	}
}
