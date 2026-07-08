package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func TestSnapshotsRevertWritesAndDeleteCreated(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "created.txt") // does not exist yet

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
	}
	a.snapshots.startTurn()
	a.SnapshotPath(existing)
	a.SnapshotPath(created)

	// Simulate a tool overwriting/creating both files.
	if err := os.WriteFile(existing, []byte("clobbered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := a.Undo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "original" {
		t.Errorf("expected restore to original, got %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("expected created.txt to be removed after undo")
	}
	if out == "Nothing to undo." {
		t.Errorf("expected a non-empty summary, got %q", out)
	}
}

func TestSnapshotDedupesPerTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
	}
	a.snapshots.startTurn()
	a.SnapshotPath(path)
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.SnapshotPath(path) // should be a no-op since same turn
	if err := os.WriteFile(path, []byte("v3"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1" {
		t.Errorf("expected first snapshot (v1), got %q", got)
	}
}

func TestUndoEmpty(t *testing.T) {
	a := &Agent{snapshots: &snapshotStore{}}
	out, err := a.Undo(context.Background())
	if err != nil || out != "Nothing to undo." {
		t.Errorf("unexpected: out=%q err=%v", out, err)
	}
}

func TestModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "sub", "created.txt") // does not exist yet

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
	}

	// Empty before anything is touched.
	if got := a.ModifiedFiles(); len(got) != 0 {
		t.Fatalf("expected no modified files initially, got %v", got)
	}

	a.snapshots.startTurn()
	a.SnapshotPath(existing) // edited (exists)
	a.SnapshotPath(created)  // created (missing)

	files := a.ModifiedFiles()
	if len(files) != 2 {
		t.Fatalf("expected 2 modified files, got %d: %+v", len(files), files)
	}
	// Most-recently-touched first: created was recorded last.
	if files[0].Path != filepath.Join("sub", "created.txt") || !files[0].Created {
		t.Errorf("expected created file first with Created=true, got %+v", files[0])
	}
	if files[1].Path != "existing.txt" || files[1].Created {
		t.Errorf("expected existing file second with Created=false, got %+v", files[1])
	}

	// A later turn that touches existing again should bump it to the front and
	// must not duplicate it.
	a.snapshots.startTurn()
	a.SnapshotPath(existing)
	files = a.ModifiedFiles()
	if len(files) != 2 {
		t.Fatalf("re-touching a file must not duplicate it, got %d: %+v", len(files), files)
	}
	if files[0].Path != "existing.txt" {
		t.Errorf("re-touched file should move to the front, got %+v", files)
	}

	// Undo pops the most recent turn (turn 2). The turn-1 snapshot of
	// existing.txt remains, so it's still considered modified relative to its
	// session-start contents — both files stay in the list, created first.
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	files = a.ModifiedFiles()
	if len(files) != 2 {
		t.Fatalf("after undoing turn 2, both turn-1 files should remain, got %+v", files)
	}
	if files[0].Path != filepath.Join("sub", "created.txt") || files[1].Path != "existing.txt" {
		t.Errorf("unexpected order/paths after undo: %+v", files)
	}
}

func TestModifiedFilesNilStore(t *testing.T) {
	a := &Agent{}
	if got := a.ModifiedFiles(); got != nil {
		t.Errorf("nil snapshot store should yield nil, got %v", got)
	}
}

func TestPlanModeFiltersTools(t *testing.T) {
	a := &Agent{}
	a.SetPlanMode(true)
	if !a.PlanMode() {
		t.Fatal("plan mode should be on")
	}
	for tool := range planModeBlocked {
		if !planModeBlocked[tool] {
			t.Errorf("expected %s to be blocked", tool)
		}
	}
	a.SetPlanMode(false)
	if a.PlanMode() {
		t.Fatal("plan mode should be off")
	}
}
