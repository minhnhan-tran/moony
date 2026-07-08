package diff

import (
	"strings"
	"testing"
)

func TestUnifiedAddRemove(t *testing.T) {
	old := "line one\nline two\nline three\n"
	new := "line one\nline 2\nline three\n"

	out, stats := Unified(old, new)
	if stats.Added != 1 || stats.Removed != 1 {
		t.Fatalf("stats = %v, want +1 -1", stats)
	}
	if !strings.Contains(out, "- line two") {
		t.Errorf("expected deletion of 'line two', got:\n%s", out)
	}
	if !strings.Contains(out, "+ line 2") {
		t.Errorf("expected addition of 'line 2', got:\n%s", out)
	}
	// Unchanged context lines should be present and marked with two spaces.
	if !strings.Contains(out, "  line one") {
		t.Errorf("expected context 'line one', got:\n%s", out)
	}
}

func TestUnifiedNoChange(t *testing.T) {
	out, stats := Unified("same\n", "same\n")
	if out != "" || stats.Added != 0 || stats.Removed != 0 {
		t.Errorf("expected empty diff, got %q %v", out, stats)
	}
}

func TestUnifiedNewFile(t *testing.T) {
	out, stats := Unified("", "a\nb\n")
	if stats.Added != 2 {
		t.Errorf("added = %d, want 2", stats.Added)
	}
	if !strings.Contains(out, "+ a") || !strings.Contains(out, "+ b") {
		t.Errorf("expected both additions, got:\n%s", out)
	}
}
