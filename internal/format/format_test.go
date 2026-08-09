package format

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// writeProjectConfig drops a .moony/format.json under dir.
func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	sqe := filepath.Join(dir, config.ProjectDir)
	if err := os.MkdirAll(sqe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sqe, "format.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigsProjectOnly(t *testing.T) {
	// Isolate the global config dir so the host's real one can't leak in.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
	  "formatters": {
	    "go": { "command": "gofmt", "args": ["-w"], "extensions": [".go"] }
	  }
	}`)

	cfgs, err := LoadConfigs(dir)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("want 1 config, got %d: %+v", len(cfgs), cfgs)
	}
	if cfgs[0].Name != "go" || cfgs[0].Command != "gofmt" {
		t.Fatalf("unexpected config: %+v", cfgs[0])
	}
}

func TestLoadConfigsSkipsDisabledAndIncomplete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
	  "formatters": {
	    "off":     { "command": "gofmt", "extensions": [".go"], "disabled": true },
	    "nocmd":   { "extensions": [".py"] },
	    "noext":   { "command": "ruff" },
	    "good":    { "command": "gofmt", "extensions": [".go"] }
	  }
	}`)

	cfgs, err := LoadConfigs(dir)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
	if len(cfgs) != 1 || cfgs[0].Name != "good" {
		t.Fatalf("want only 'good', got %+v", cfgs)
	}
}

func TestEmpty(t *testing.T) {
	var nilMgr *Manager
	if !nilMgr.Empty() {
		t.Fatal("nil Manager should be Empty")
	}
	if !New(t.TempDir(), nil).Empty() {
		t.Fatal("Manager with no configs should be Empty")
	}
	m := New(t.TempDir(), []Config{{Name: "x", Command: "true", Extensions: []string{".go"}}})
	if m.Empty() {
		t.Fatal("configured Manager should not be Empty")
	}
}

// fmtConfig is a formatter that rewrites the target file's contents to "FMT".
// The file path is appended as the final arg, becoming $0 to `sh -c`.
func fmtConfig() Config {
	return Config{
		Name:       "stub",
		Command:    "sh",
		Args:       []string{"-c", `printf 'FMT' > "$0"`},
		Extensions: []string{".go"},
	}
}

func TestFormatFileRewritesInPlace(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	if err := os.WriteFile(f, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(dir, []Config{fmtConfig()})
	if note := m.FormatFile(context.Background(), f); note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FMT" {
		t.Fatalf("file not formatted in place: %q", got)
	}
}

func TestFormatFileUnmatchedExtNoOp(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(f, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(dir, []Config{fmtConfig()}) // only handles .go
	if note := m.FormatFile(context.Background(), f); note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
	got, _ := os.ReadFile(f)
	if string(got) != "original" {
		t.Fatalf("unmatched file should be untouched, got %q", got)
	}
}

func TestFormatFileMissingNoOp(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, []Config{fmtConfig()})
	if note := m.FormatFile(context.Background(), filepath.Join(dir, "gone.go")); note != "" {
		t.Fatalf("missing file should be a silent no-op, got %q", note)
	}
}

func TestFormatFileFailureNote(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(dir, []Config{{
		Name:       "boom",
		Command:    "sh",
		Args:       []string{"-c", "echo bad syntax >&2; exit 1"},
		Extensions: []string{".go"},
	}})
	note := m.FormatFile(context.Background(), f)
	if note == "" {
		t.Fatal("expected a failure note")
	}
	if want := "formatter boom failed"; !strings.Contains(note, want) {
		t.Fatalf("note %q should mention %q", note, want)
	}
}

func TestEmptyManagerFormatFileNoOp(t *testing.T) {
	var nilMgr *Manager
	if note := nilMgr.FormatFile(context.Background(), "/whatever.go"); note != "" {
		t.Fatalf("nil Manager FormatFile should be a no-op, got %q", note)
	}
}
