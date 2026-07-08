package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandMentionsAttachesFile(t *testing.T) {
	dir := t.TempDir()
	want := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	got := expandMentions("look at @main.go please", dir)
	if !strings.Contains(got, "look at @main.go please") {
		t.Error("user text should be preserved")
	}
	if !strings.Contains(got, "attached files") {
		t.Error("expected an attached-files section")
	}
	if !strings.Contains(got, want) {
		t.Errorf("file body missing from prompt:\n%s", got)
	}
}

func TestExpandMentionsHandlesMissingAndDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	in := "see @missing.go and @subdir for context"
	got := expandMentions(in, dir)
	if got != in {
		t.Fatalf("with no resolvable mentions, text should be unchanged; got %q", got)
	}
}

func TestExpandMentionsDeduplicates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := expandMentions("@a.txt and again @a.txt", dir)
	if strings.Count(got, "(path: ") != 1 {
		t.Errorf("expected duplicate mention to be attached once, got:\n%s", got)
	}
}

func TestExpandMentionsIgnoresNonMentions(t *testing.T) {
	in := "ping @user about #issue (no files)"
	if got := expandMentions(in, t.TempDir()); got != in {
		t.Errorf("expected unchanged text, got %q", got)
	}
}
