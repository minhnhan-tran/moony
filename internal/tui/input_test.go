package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestSanitizeForInput(t *testing.T) {
	cases := []struct {
		name string
		in   tea.Msg
		keep bool
		want string // expected runes when kept and it's a KeyMsg
	}{
		{"plain text", runes("hello"), true, "hello"},
		{"sgr mouse fragment with bracket", runes("[<64;30;15M"), false, ""},
		{"sgr mouse core no bracket", runes("<64;30;15M"), false, ""},
		{"sgr release fragment", runes("<0;12;7m"), false, ""},
		{"concatenated scroll burst", runes("[<64;30;15M[<64;30;16M"), false, ""},
		{"csi arrow fragment", runes("[A"), false, ""},
		{"bracketed-paste marker", runes("[200~"), false, ""},
		{"text with embedded mouse leak", runes("foo<64;30;15M"), true, "foo"},
		{"legit text with angle bracket", runes("a < b"), true, "a < b"},
		{"legit bracket text", runes("arr[i]"), true, "arr[i]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := sanitizeForInput(tc.in)
			if keep != tc.keep {
				t.Fatalf("keep = %v, want %v", keep, tc.keep)
			}
			if keep {
				k, ok := got.(tea.KeyMsg)
				if !ok {
					t.Fatalf("expected KeyMsg, got %T", got)
				}
				if string(k.Runes) != tc.want {
					t.Errorf("runes = %q, want %q", string(k.Runes), tc.want)
				}
			}
		})
	}
}

func TestSanitizeForInputPassesNonRuneKeys(t *testing.T) {
	// Editing/navigation keys (enter, backspace, etc.) must pass through.
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyBackspace}, {Type: tea.KeyLeft}} {
		if _, keep := sanitizeForInput(k); !keep {
			t.Errorf("non-rune key %v should be kept", k.Type)
		}
	}
}

// TestSanitizeDropsAltIntroducer covers the lone "[" (Alt+[) that leaks when a
// short read splits a mouse sequence right after its "\x1b[" introducer.
func TestSanitizeDropsAltIntroducer(t *testing.T) {
	drop := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true}, // CSI introducer remnant
		{Type: tea.KeyRunes, Runes: []rune{'O'}, Alt: true}, // SS3 introducer remnant
	}
	for _, k := range drop {
		if _, keep := sanitizeForInput(k); keep {
			t.Errorf("Alt+%q should be dropped as a leaked introducer", string(k.Runes))
		}
	}

	keep := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'['}},            // a genuinely typed "["
		{Type: tea.KeyRunes, Runes: []rune{'O'}},            // a genuinely typed "O"
		{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true}, // Alt+f word-nav, not an introducer
	}
	for _, k := range keep {
		if _, ok := sanitizeForInput(k); !ok {
			t.Errorf("%v should be kept (not a leaked introducer)", k)
		}
	}
}

// TestAltBracketDoesNotReachInput is the end-to-end check for the reported bug:
// scrolling injects a stray "[" into the input box.
func TestAltBracketDoesNotReachInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true})
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Errorf("leaked Alt+[ reached the input: %q", got)
	}

	// A genuinely typed "[" must still work.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = next.(Model)
	if got := m.input.Value(); got != "[" {
		t.Errorf("a typed [ should reach the input, got %q", got)
	}
}

// TestScrollLeakDoesNotReachInput feeds a split mouse-scroll fragment through
// Update (as Bubble Tea would after a bad read boundary) and verifies the
// input box stays empty.
func TestScrollLeakDoesNotReachInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	next, _ := m.Update(runes("[<64;30;15M"))
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Errorf("leaked scroll fragment reached the input: %q", got)
	}

	// A normal keystroke still types fine.
	next, _ = m.Update(runes("h"))
	m = next.(Model)
	if got := m.input.Value(); got != "h" {
		t.Errorf("normal input should reach the textarea, got %q", got)
	}
}

// TestMouseWheelDoesNotReachInput ensures wheel events drive the viewport only.
func TestMouseWheelDoesNotReachInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 10, Y: 10})
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Errorf("wheel event should not type into the input, got %q", got)
	}
}

// TestInputPromptShownOnce guards the fix for the multi-line input box showing a
// stack of "›" prompt markers (one per visible line).
func TestInputPromptShownOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)
	if got := strings.Count(m.View(), "›"); got != 1 {
		t.Errorf("input prompt '›' count = %d, want 1 (only the first line)", got)
	}
}

// TestInputBoxRestingHeight verifies the input box opens at the roomier minimum
// height rather than a single line.
func TestInputBoxRestingHeight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	if got := m.input.Height(); got < minInputLines {
		t.Errorf("input box height = %d, want >= %d", got, minInputLines)
	}
}
