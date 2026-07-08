package announce

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAddListRemovePersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "announce.json")
	s := OpenAt(path)

	a1, err := s.Add("First", "body one", "info", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("Second", "", "warn", "alice"); err != nil {
		t.Fatal(err)
	}

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("want 2, got %d", len(list))
	}
	// Newest first.
	if list[0].Title != "Second" {
		t.Errorf("want Second first (newest), got %q", list[0].Title)
	}
	if list[1].Level != "info" || list[1].Author != "alice" {
		t.Errorf("unexpected first announcement: %+v", list[1])
	}

	// Reload from disk → persisted.
	s2 := OpenAt(path)
	if len(s2.List()) != 2 {
		t.Fatalf("reload: want 2, got %d", len(s2.List()))
	}

	// Remove.
	if err := s.Remove(a1.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("after remove: want 1, got %d", len(s.List()))
	}
	if OpenAt(path).List()[0].Title != "Second" {
		t.Errorf("remove not persisted")
	}
}

func TestStoreRejectsEmpty(t *testing.T) {
	s := OpenAt("")
	if _, err := s.Add("  ", "  ", "info", "x"); err == nil {
		t.Error("expected error for empty title+body")
	}
}

func TestStoreCap(t *testing.T) {
	s := OpenAt("")
	for i := 0; i < maxAnnouncements+10; i++ {
		if _, err := s.Add("t", "b", "info", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.List()) != maxAnnouncements {
		t.Errorf("want cap %d, got %d", maxAnnouncements, len(s.List()))
	}
}

func TestOpenAtMissingFile(t *testing.T) {
	s := OpenAt(filepath.Join(t.TempDir(), "nope.json"))
	if len(s.List()) != 0 {
		t.Error("missing file should yield empty store")
	}
	// nil store List is safe.
	var ns *Store
	if ns.List() != nil {
		t.Error("nil store List should be nil")
	}
	_ = os.Remove
}
