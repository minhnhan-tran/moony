package web

import "testing"

// Read-only pickers (menu-openers) run in any state; value-setting forms do not.
func TestIsPickerMenuCommand(t *testing.T) {
	open := []string{"/models", "/model", "/provider", "/providers", "/reasoning", "/think", "/thinking", "  /models  "}
	for _, c := range open {
		if !isPickerMenuCommand(c) {
			t.Errorf("%q should be a menu-opening picker (run while busy)", c)
		}
	}
	notOpen := []string{
		"/model anthropic.claude-opus-4-8", // value-setting → must NOT bypass the gate
		"/provider anthropic",
		"/reasoning high",
		"/models extra", // /models takes no arg; an arg means it's not the bare picker
		"/clear", "/help", "hello", "", "/",
	}
	for _, c := range notOpen {
		if isPickerMenuCommand(c) {
			t.Errorf("%q should NOT be treated as a read-only menu-opener", c)
		}
	}
}
