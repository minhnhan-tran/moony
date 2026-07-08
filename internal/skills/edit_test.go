package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()

	path, err := Scaffold("mytool", false, wd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SKILL.md not created: %v", err)
	}
	found := false
	for _, s := range Load(wd) {
		if s.Name == "mytool" {
			found = true
		}
	}
	if !found {
		t.Error("scaffolded skill was not discovered")
	}
	if _, err := Scaffold("mytool", false, wd); err == nil {
		t.Error("scaffolding an existing skill should error")
	}

	if _, err := Remove("mytool", wd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Error("skill directory should be gone after remove")
	}
	if _, err := Remove("nope", wd); err == nil {
		t.Error("removing an unknown skill should error")
	}
}

func TestRemoveRefusesAgentsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	dir := filepath.Join(home, ".agents", "skills", "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: shared\ndescription: x\n---\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove("shared", wd); err == nil {
		t.Error("Remove should refuse skills under ~/.agents")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the ~/.agents skill must not be deleted")
	}
}
