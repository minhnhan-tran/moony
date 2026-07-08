package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pysty1995/moony/internal/attach"
)

// stageAttachment resolves a path (relative to the working dir) into an
// attachment staged for the next user message. Returns a status line.
func (m *Model) stageAttachment(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	if path == "" {
		return "Usage: /attach <path> — stage an image, PDF, Office, or iWork file."
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(m.cfg.WorkingDir, path)
	}
	a, err := attach.FromPath(full)
	if err != nil {
		return "Could not attach " + path + ": " + err.Error()
	}
	m.attachments = append(m.attachments, a)
	return fmt.Sprintf("Staged %s — sent with your next message (%d attached).", a.Label(), len(m.attachments))
}

// attachFullPath resolves a /attach argument (relative to the working dir),
// applying the same trimming as stageAttachment. Returns "" for an empty arg. It
// lets the dispatcher peek at the target (e.g. to route a slow iWork conversion
// off the event loop) before resolving.
func (m Model) attachFullPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.Join(m.cfg.WorkingDir, path)
	}
	return path
}

// pasteClipboardImage stages an image from the macOS clipboard (no terminal
// image-paste exists, so this is an explicit command). Returns a status line.
func (m *Model) pasteClipboardImage() string {
	if runtime.GOOS != "darwin" {
		return "/paste reads the clipboard image (macOS only). Use /attach <path> instead."
	}
	data, err := clipboardImagePNG()
	if err != nil {
		return "No image on the clipboard (" + err.Error() + "). Copy an image, then /paste — or use /attach <path>."
	}
	a, err := attach.FromBytes("clipboard.png", "image/png", data)
	if err != nil {
		return "Could not attach clipboard image: " + err.Error()
	}
	m.attachments = append(m.attachments, a)
	return fmt.Sprintf("Staged %s from the clipboard — sent with your next message (%d attached).", a.Label(), len(m.attachments))
}

// clipboardImagePNG writes the macOS clipboard's image to a temp PNG via
// AppleScript and returns its bytes. It errors when the clipboard holds no
// image (the «class PNGf» coercion fails).
func clipboardImagePNG() ([]byte, error) {
	tmp, err := os.CreateTemp("", "sqe-clip-*.png")
	if err != nil {
		return nil, err
	}
	_ = tmp.Close()
	defer os.Remove(tmp.Name())

	script := fmt.Sprintf(`set outFile to POSIX file %q
try
	set pngData to (the clipboard as «class PNGf»)
on error
	error "no image on clipboard"
end try
set fh to open for access outFile with write permission
set eof of fh to 0
write pngData to fh
close access fh`, tmp.Name())

	cmd := exec.Command("osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard image was empty")
	}
	return data, nil
}

// stagedAttachmentsLine renders a one-line summary of pending attachments for
// display above the input, or "" when none are staged.
func (m Model) stagedAttachmentsLine() string {
	if len(m.attachments) == 0 {
		return ""
	}
	labels := make([]string, 0, len(m.attachments))
	for _, a := range m.attachments {
		labels = append(labels, a.Label())
	}
	return "Attached: " + strings.Join(labels, " · ")
}
