package tui

import "os"

// tuiBellEnabled reports whether the terminal bell should ring on turn
// completion / approval requests. On by default (MN_TUI_BELL="0" to
// disable) — most terminals either play a short sound or flash, and it's the
// simplest cue that something needs attention while the pane isn't focused.
func tuiBellEnabled() bool {
	return os.Getenv("MN_TUI_BELL") != "0"
}

// ringBell writes the ASCII BEL character, which most terminal emulators
// turn into an audible/visual alert. A no-op when disabled.
func ringBell() {
	if tuiBellEnabled() {
		os.Stdout.Write([]byte{'\a'})
	}
}
