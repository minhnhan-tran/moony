package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNewSessionDir(t *testing.T) {
	home := t.TempDir()

	// Existing directory → returned as-is.
	existing := filepath.Join(home, "proj")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNewSessionDir(existing, home); err != nil || got != existing {
		t.Errorf("existing dir = (%q, %v), want (%q, nil)", got, err, existing)
	}

	// Non-existent dir under an existing parent → CREATED.
	fresh := filepath.Join(home, "IOSApp")
	got, err := resolveNewSessionDir(fresh, home)
	if err != nil {
		t.Fatalf("auto-create: %v", err)
	}
	if got != fresh {
		t.Errorf("got %q, want %q", got, fresh)
	}
	if info, statErr := os.Stat(fresh); statErr != nil || !info.IsDir() {
		t.Errorf("folder was not created: %v", statErr)
	}

	// Deep path whose PARENT doesn't exist → error, and nothing created (no
	// silent MkdirAll of a typo'd tree).
	deep := filepath.Join(home, "nope", "child")
	if _, err := resolveNewSessionDir(deep, home); err == nil {
		t.Error("deep path with a missing parent should error")
	}
	if _, statErr := os.Stat(filepath.Join(home, "nope")); !os.IsNotExist(statErr) {
		t.Error("the missing parent must NOT have been created")
	}

	// Existing regular file (not a dir) → rejected.
	f := filepath.Join(home, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveNewSessionDir(f, home); err == nil {
		t.Error("a regular file path should be rejected")
	}

	// Empty → error.
	if _, err := resolveNewSessionDir("  ", home); err == nil {
		t.Error("empty dir should error")
	}

	// "~" expands to home (which exists) → returned.
	if got, err := resolveNewSessionDir("~", home); err != nil || got != home {
		t.Errorf("~ = (%q, %v), want (%q, nil)", got, err, home)
	}
}
