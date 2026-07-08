package tui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

var onePxPNG = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	data, _ := base64.StdEncoding.DecodeString(b64)
	return data
}()

func TestAttachmentMentionsResolvesImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), onePxPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	atts, notes := attachmentMentions("see @shot.png here", dir)
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if len(atts) != 1 || atts[0].Name != "shot.png" {
		t.Fatalf("expected the image staged, got %+v", atts)
	}
}

func TestExpandMentionsSkipsAttachableFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), onePxPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	// An image @-mention must NOT be inlined as text by expandMentions (it's an
	// attachment instead), so no attached-files section is produced.
	got := expandMentions("check @shot.png", dir)
	if strings.Contains(got, "attached files") {
		t.Errorf("image mention should not be inlined as text:\n%s", got)
	}
}

func TestAttachmentMentionsIgnoresTextFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	atts, _ := attachmentMentions("read @main.go", dir)
	if len(atts) != 0 {
		t.Fatalf("text files should not become attachments, got %+v", atts)
	}
}

func TestStageAttachment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), onePxPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Model{cfg: &config.Config{WorkingDir: dir}}
	msg := m.stageAttachment("pic.png")
	if !strings.Contains(msg, "Staged") {
		t.Fatalf("unexpected status: %q", msg)
	}
	if len(m.attachments) != 1 {
		t.Fatalf("want 1 staged attachment, got %d", len(m.attachments))
	}
	if line := m.stagedAttachmentsLine(); !strings.Contains(line, "pic.png") {
		t.Errorf("staged line should mention the file, got %q", line)
	}
}

func TestStageAttachmentMissing(t *testing.T) {
	m := &Model{cfg: &config.Config{WorkingDir: t.TempDir()}}
	if msg := m.stageAttachment("nope.png"); !strings.Contains(msg, "Could not attach") {
		t.Errorf("expected failure note, got %q", msg)
	}
	if len(m.attachments) != 0 {
		t.Error("nothing should be staged on failure")
	}
}
