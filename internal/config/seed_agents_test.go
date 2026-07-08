package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedGlobalAGENTSWritesOnce: a fresh install gets the embedded default
// global AGENTS.md; a pre-existing one (a user's own rules) is never touched.
func TestSeedGlobalAGENTSWritesOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	wrote, err := SeedGlobalAGENTS()
	if err != nil {
		t.Fatalf("SeedGlobalAGENTS: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false on a fresh config dir, want true")
	}
	path, err := GlobalConfigPath("AGENTS.md")
	if err != nil {
		t.Fatalf("GlobalConfigPath: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "CHANGELOG.md") {
		t.Errorf("seeded content missing the CHANGELOG.md rule: %s", b)
	}

	// A second call (simulating a later run) must not overwrite user edits.
	if err := os.WriteFile(path, []byte("my own rules\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wrote2, err := SeedGlobalAGENTS()
	if err != nil {
		t.Fatalf("SeedGlobalAGENTS (2nd): %v", err)
	}
	if wrote2 {
		t.Fatal("wrote = true on an existing AGENTS.md, want false (must not clobber)")
	}
	b2, _ := os.ReadFile(path)
	if string(b2) != "my own rules\n" {
		t.Fatalf("existing AGENTS.md was overwritten: %s", b2)
	}
}

func TestSeedGlobalAGENTSCreatesConfigDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "not-yet-created"))

	if _, err := SeedGlobalAGENTS(); err != nil {
		t.Fatalf("SeedGlobalAGENTS: %v", err)
	}
	path, _ := GlobalConfigPath("AGENTS.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
}
