package config

import (
	"strings"
	"testing"
)

func TestCodebaseMapSectionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file: both sections empty.
	if s := CodebaseMapStructure(dir); s != "" {
		t.Fatalf("expected empty structure for missing file, got %q", s)
	}
	if _, ok := ReadCodebaseMap(dir); ok {
		t.Fatal("expected ReadCodebaseMap false for missing file")
	}

	// Write structure only; file is created, narrative gets the stub.
	if err := WriteCodebaseMapStructure(dir, "### pkg/\n- `a.go` — Foo, Bar"); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapStructure(dir); !strings.Contains(got, "a.go") {
		t.Fatalf("structure not stored: %q", got)
	}
	if n := CodebaseMapNarrative(dir); strings.Contains(n, "a.go") || n == "" {
		// narrative should be the stub, not the structure
		if strings.Contains(n, "a.go") {
			t.Fatalf("narrative leaked structure: %q", n)
		}
	}

	// Write narrative; structure must be preserved.
	if err := WriteCodebaseMapNarrative(dir, "## Auth\nHandled by a.go."); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapNarrative(dir); !strings.Contains(got, "Handled by a.go") {
		t.Fatalf("narrative not stored: %q", got)
	}
	if got := CodebaseMapStructure(dir); !strings.Contains(got, "a.go") {
		t.Fatalf("structure clobbered by narrative write: %q", got)
	}

	// Rewrite structure; narrative must be preserved.
	if err := WriteCodebaseMapStructure(dir, "### pkg/\n- `b.go` — Baz"); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapNarrative(dir); !strings.Contains(got, "Handled by a.go") {
		t.Fatalf("narrative clobbered by structure rewrite: %q", got)
	}
	if got := CodebaseMapStructure(dir); !strings.Contains(got, "b.go") || strings.Contains(got, "a.go") {
		t.Fatalf("structure not replaced: %q", got)
	}
}

func TestCodebaseMapMarkersPresent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCodebaseMapStructure(dir, "x"); err != nil {
		t.Fatal(err)
	}
	c, _ := ReadCodebaseMap(dir)
	for _, marker := range []string{cmStructStart, cmStructEnd, cmNarrStart, cmNarrEnd} {
		if !strings.Contains(c, marker) {
			t.Errorf("map missing marker %q", marker)
		}
	}
}

// TestCodebaseMapPinnedSurvivesBothWriters is the contract for the hand-authored
// PINNED scope: neither the structural generator nor an @map narrative rewrite
// may ever touch it.
func TestCodebaseMapPinnedSurvivesBothWriters(t *testing.T) {
	dir := t.TempDir()
	const pin = "ARCH: relay owns session ownership; never dual-write."

	if err := WriteCodebaseMapPinned(dir, pin); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapPinned(dir); got != pin {
		t.Fatalf("pinned not stored: %q", got)
	}

	// A structural refresh must preserve pinned (and narrative).
	if err := WriteCodebaseMapNarrative(dir, "NARR"); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodebaseMapStructure(dir, "STRUCT"); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapPinned(dir); got != pin {
		t.Errorf("structure write clobbered pinned: %q", got)
	}

	// An @map narrative rewrite must preserve pinned (and structure).
	if err := WriteCodebaseMapNarrative(dir, "NARR2"); err != nil {
		t.Fatal(err)
	}
	if got := CodebaseMapPinned(dir); got != pin {
		t.Errorf("narrative write clobbered pinned: %q", got)
	}
	if got := CodebaseMapStructure(dir); got != "STRUCT" {
		t.Errorf("narrative write clobbered structure: %q", got)
	}
	if got := CodebaseMapNarrative(dir); got != "NARR2" {
		t.Errorf("narrative not updated: %q", got)
	}

	// Writing pinned must not disturb the other two.
	if err := WriteCodebaseMapPinned(dir, "PIN2"); err != nil {
		t.Fatal(err)
	}
	if CodebaseMapStructure(dir) != "STRUCT" || CodebaseMapNarrative(dir) != "NARR2" {
		t.Error("pinned write disturbed other sections")
	}
}
