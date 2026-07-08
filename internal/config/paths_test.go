package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalConfigDirHonorsXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err := GlobalConfigDir()
	if err != nil {
		t.Fatalf("GlobalConfigDir: %v", err)
	}
	want := filepath.Join(xdg, "moony")
	if got != want {
		t.Fatalf("GlobalConfigDir() = %q, want %q", got, want)
	}
}

func TestGlobalConfigDirFallsBackToDotConfig(t *testing.T) {
	// With XDG unset, the dir must be ~/.config/moony on every
	// platform (NOT ~/Library/Application Support on macOS).
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := GlobalConfigDir()
	if err != nil {
		t.Fatalf("GlobalConfigDir: %v", err)
	}
	want := filepath.Join(home, ".config", "moony")
	if got != want {
		t.Fatalf("GlobalConfigDir() = %q, want %q", got, want)
	}
}

func TestGlobalConfigPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err := GlobalConfigPath("mcp.json")
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	want := filepath.Join(xdg, "moony", "mcp.json")
	if got != want {
		t.Fatalf("GlobalConfigPath() = %q, want %q", got, want)
	}
}

func TestMigrateConfigDir(t *testing.T) {
	root := t.TempDir()

	// Seed a legacy dir with a top-level file and a nested file.
	legacy := filepath.Join(root, "legacy")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "sessions", "s1.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Migrate into a (currently absent) destination.
	dst := filepath.Join(root, "new", "moony")
	if err := migrateConfigDir(legacy, dst); err != nil {
		t.Fatalf("migrateConfigDir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "config.json")); err != nil || string(b) != `{"x":1}` {
		t.Fatalf("config.json not migrated: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sessions", "s1.json")); err != nil {
		t.Fatalf("nested session not migrated: %v", err)
	}

	// No-ops: equal paths, and an absent legacy dir (must not create dst).
	if err := migrateConfigDir(dst, dst); err != nil {
		t.Fatalf("equal-path migrate should be a no-op: %v", err)
	}
	noop := filepath.Join(root, "new2")
	if err := migrateConfigDir(filepath.Join(root, "missing"), noop); err != nil {
		t.Fatalf("absent-legacy migrate should be a no-op: %v", err)
	}
	if _, err := os.Stat(noop); !os.IsNotExist(err) {
		t.Fatalf("no-op migration must not create dst")
	}

	// Never clobber an existing destination.
	legacy2 := filepath.Join(root, "legacy2")
	dst2 := filepath.Join(root, "dst2")
	if err := os.MkdirAll(legacy2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy2, "a"), []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst2, "a"), []byte("NEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateConfigDir(legacy2, dst2); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst2, "a")); string(b) != "NEW" {
		t.Fatalf("migration clobbered an existing destination: %q", b)
	}
}

// TestMigrateRenamedConfigDir: the sqe-code-agent -> moony
// product rename relocates ~/.config/sqe-code-agent (honoring XDG_CONFIG_HOME)
// into GlobalConfigDir()'s current (renamed) location.
func TestMigrateRenamedConfigDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	legacy := filepath.Join(xdg, "sqe-code-agent")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "mcp.json"), []byte(`{"servers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateRenamedConfigDir(); err != nil {
		t.Fatalf("MigrateRenamedConfigDir: %v", err)
	}

	newDir, err := GlobalConfigDir()
	if err != nil {
		t.Fatalf("GlobalConfigDir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "mcp.json")); err != nil || string(b) != `{"servers":{}}` {
		t.Fatalf("mcp.json not migrated into %s: %q %v", newDir, b, err)
	}

	// Idempotent: calling again (legacy dir untouched by migrateConfigDir, which
	// doesn't delete the source) must not error and must not clobber the dest.
	if err := os.WriteFile(filepath.Join(newDir, "mcp.json"), []byte("NEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateRenamedConfigDir(); err != nil {
		t.Fatalf("MigrateRenamedConfigDir (2nd): %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(newDir, "mcp.json")); string(b) != "NEW" {
		t.Fatalf("2nd migration clobbered the destination: %q", b)
	}
}
