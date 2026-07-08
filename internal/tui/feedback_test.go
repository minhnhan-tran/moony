package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCtrlYCopyShortcut verifies ctrl+y is wired to the copy action. It uses the
// "nothing to copy" path so the test doesn't depend on a working clipboard.
func TestCtrlYCopyShortcut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	before := len(m.trans.entries)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = next.(Model)

	if len(m.trans.entries) != before+1 {
		t.Fatalf("ctrl+y should add a status entry; entries %d → %d", before, len(m.trans.entries))
	}
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryInfo || !strings.Contains(last.text, "Nothing to copy") {
		t.Errorf("expected a 'Nothing to copy' info entry, got kind=%v text=%q", last.kind, last.text)
	}
}

// TestStatusBarShowsCopyShortcut verifies the idle status row advertises ctrl+y.
func TestStatusBarShowsCopyShortcut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30) // wide enough that nothing is clipped

	out := m.statusView()
	if !strings.Contains(out, "ctrl+y") || !strings.Contains(out, "copy") {
		t.Errorf("status bar should show the ctrl+y copy shortcut, got:\n%s", out)
	}
}

func TestSoundEnabled(t *testing.T) {
	t.Setenv("SQE_SOUND", "")
	if !soundEnabled() {
		t.Error("the completion chime should be on by default")
	}
	for _, v := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv("SQE_SOUND", v)
		if soundEnabled() {
			t.Errorf("SQE_SOUND=%q should disable the chime", v)
		}
	}
}

func TestPlayDoneSoundCmdDisabled(t *testing.T) {
	t.Setenv("SQE_SOUND", "0")
	if playDoneSoundCmd() != nil {
		t.Error("a disabled chime should yield a nil command")
	}
}
