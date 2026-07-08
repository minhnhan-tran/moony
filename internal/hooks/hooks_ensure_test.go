package hooks

import (
	"path/filepath"
	"testing"
)

func TestEnsureGlobalCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "moony", "hooks.json")

	const cmd = `echo refresh`
	if err := EnsureGlobalCommand(PostTool, cmd); err != nil {
		t.Fatalf("EnsureGlobalCommand: %v", err)
	}
	// Idempotent: a second identical call must not duplicate the command.
	if err := EnsureGlobalCommand(PostTool, cmd); err != nil {
		t.Fatalf("EnsureGlobalCommand (2nd): %v", err)
	}
	got, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if n := len(got[PostTool]); n != 1 {
		t.Fatalf("expected exactly 1 post_tool hook, got %d: %v", n, got[PostTool])
	}
	if got[PostTool][0] != cmd {
		t.Errorf("hook command = %q, want %q", got[PostTool][0], cmd)
	}

	// Adding a different command (and a user's pre-existing hook) is preserved.
	if err := EnsureGlobalCommand(PreTurn, "echo turn"); err != nil {
		t.Fatalf("EnsureGlobalCommand PreTurn: %v", err)
	}
	got, _ = loadFile(path)
	if len(got[PostTool]) != 1 || len(got[PreTurn]) != 1 {
		t.Errorf("both events should be present and intact: %+v", got)
	}

	// Unknown events are rejected.
	if err := EnsureGlobalCommand(Event("bogus"), "x"); err == nil {
		t.Error("EnsureGlobalCommand should reject an unknown event")
	}
}
