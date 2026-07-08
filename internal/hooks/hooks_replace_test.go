package hooks

import (
	"path/filepath"
	"testing"
)

func TestReplaceGlobalCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "moony", "hooks.json")

	// A user's own hook and a prior (stale) variant of the built-in hook.
	const userHook = `echo my-own-hook`
	const oldBuiltin = `case "$MOONY_TOOL_NAME" in write_file) reindex_old ;; esac # idx_marker`
	if err := EnsureGlobalCommand(PostTool, userHook); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGlobalCommand(PostTool, oldBuiltin); err != nil {
		t.Fatal(err)
	}

	const marker = "idx_marker"
	const newBuiltin = `case "$MOONY_TOOL_NAME" in write_file|bash) reindex_new ;; esac # idx_marker`
	if err := ReplaceGlobalCommand(PostTool, marker, newBuiltin); err != nil {
		t.Fatalf("ReplaceGlobalCommand: %v", err)
	}

	got, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The stale built-in is gone; the user hook and the new built-in remain.
	if len(got[PostTool]) != 2 {
		t.Fatalf("expected 2 hooks (user + new built-in), got %v", got[PostTool])
	}
	var haveUser, haveNew, haveOld bool
	for _, c := range got[PostTool] {
		switch c {
		case userHook:
			haveUser = true
		case newBuiltin:
			haveNew = true
		case oldBuiltin:
			haveOld = true
		}
	}
	if !haveUser {
		t.Error("the user's own hook must be preserved")
	}
	if !haveNew {
		t.Error("the new built-in hook must be present")
	}
	if haveOld {
		t.Error("the stale built-in variant must be removed")
	}

	// Idempotent: re-running changes nothing.
	if err := ReplaceGlobalCommand(PostTool, marker, newBuiltin); err != nil {
		t.Fatal(err)
	}
	got2, _ := loadFile(path)
	if len(got2[PostTool]) != 2 {
		t.Errorf("second call should be a no-op, got %v", got2[PostTool])
	}

	// Unknown events are rejected.
	if err := ReplaceGlobalCommand(Event("bogus"), marker, "x"); err == nil {
		t.Error("ReplaceGlobalCommand should reject an unknown event")
	}
}
