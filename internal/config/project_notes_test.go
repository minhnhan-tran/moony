package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// notesTestDir points the global config at a temp dir so tests never touch the
// developer's real ~/.config/moony/project_notes.json.
func notesTestDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir, err := GlobalConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadProjectNotesMissingFileIsEmptyNotError(t *testing.T) {
	notesTestDir(t)
	notes, err := LoadProjectNotes()
	if err != nil {
		t.Fatalf("a missing store must not be an error: %v", err)
	}
	if notes == nil {
		t.Fatal("want a usable empty map, got nil")
	}
	if len(notes) != 0 {
		t.Fatalf("want empty, got %v", notes)
	}
}

func TestProjectNotesRoundTrip(t *testing.T) {
	dir := notesTestDir(t)
	want := map[string]string{
		"/work/alpha": "- [ ] ship\n- [x] done",
		"/work/beta":  "just prose",
	}
	if err := SaveProjectNotes(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjectNotes()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, got[k])
		}
	}
	// Written where the rest of the global config lives, and as readable JSON.
	raw, err := os.ReadFile(filepath.Join(dir, "project_notes.json"))
	if err != nil {
		t.Fatalf("store not at the expected path: %v", err)
	}
	var check map[string]string
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("store is not valid JSON: %v", err)
	}
	if check["/work/alpha"] != want["/work/alpha"] {
		t.Error("on-disk content does not match what was saved")
	}
}

func TestSetProjectNotesStoresAndClears(t *testing.T) {
	notesTestDir(t)
	if err := SetProjectNotes("/p/one", "note one"); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectNotes("/p/two", "note two"); err != nil {
		t.Fatal(err)
	}
	all, _ := LoadProjectNotes()
	if all["/p/one"] != "note one" || all["/p/two"] != "note two" {
		t.Fatalf("both projects should be stored, got %v", all)
	}

	// Empty must DELETE the key, not store a blank string: a blank would keep the
	// project badged as "has notes" forever.
	if err := SetProjectNotes("/p/one", ""); err != nil {
		t.Fatal(err)
	}
	all, _ = LoadProjectNotes()
	if _, ok := all["/p/one"]; ok {
		t.Error("clearing notes must remove the key, not blank it")
	}
	if all["/p/two"] != "note two" {
		t.Error("clearing one project must not disturb another")
	}
}

// A corrupt store must surface as an error rather than being silently treated as
// "no notes", which would invite overwriting a file the user could still recover.
func TestLoadProjectNotesCorruptFileErrors(t *testing.T) {
	dir := notesTestDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project_notes.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectNotes(); err == nil {
		t.Error("a corrupt store should report an error")
	}
}

// A literal "null" is valid JSON but unmarshals to a nil map; callers index the
// result directly, so it must come back usable.
func TestLoadProjectNotesNullJSONYieldsUsableMap(t *testing.T) {
	dir := notesTestDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project_notes.json"), []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := LoadProjectNotes()
	if err != nil {
		t.Fatal(err)
	}
	if notes == nil {
		t.Fatal("want a usable map for a null store, got nil")
	}
	notes["x"] = "y" // must not panic
}

func TestSaveProjectNotesLeavesNoTempFile(t *testing.T) {
	dir := notesTestDir(t)
	if err := SaveProjectNotes(map[string]string{"/a": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "project_notes.json.tmp")); !os.IsNotExist(err) {
		t.Error("the temp file used for the atomic write should not survive")
	}
}

func TestProjectNotesForFallsBackToAbsolutePath(t *testing.T) {
	notesTestDir(t)
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProjectNotes(abs, "stored under the absolute path"); err != nil {
		t.Fatal(err)
	}
	// Looked up by a relative dir, it must still resolve.
	if got := ProjectNotesFor("."); got != "stored under the absolute path" {
		t.Errorf("relative lookup should fall back to abs, got %q", got)
	}
	if got := ProjectNotesFor(""); got != "" {
		t.Errorf("empty dir should yield no notes, got %q", got)
	}
	if got := ProjectNotesFor("/nope/not/here"); got != "" {
		t.Errorf("unknown dir should yield no notes, got %q", got)
	}
}

// Concurrent single-project edits must not lose one another. Load+mutate+Save in
// two steps would race; SetProjectNotes does it under one lock.
func TestSetProjectNotesConcurrent(t *testing.T) {
	notesTestDir(t)
	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := SetProjectNotes(filepath.Join("/p", string(rune('a'+i))), "v"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	all, err := LoadProjectNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Errorf("all %d concurrent writes should survive, got %d: %v", n, len(all), all)
	}
}
