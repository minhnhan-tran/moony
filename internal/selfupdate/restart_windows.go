//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
)

// Restart launches exe as a child that inherits this console, then leaves it to
// the caller to exit.
//
// Windows has no execve: syscall.Exec there is a stub that only ever returns "not
// supported by windows" (verified), so the post-update restart silently did
// nothing and the user was dropped back to the shell still on the old build. The
// child shares stdin/stdout/stderr, so an interactive session carries on in the
// same window.
func Restart(exe string, args, env []string) error {
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	cmd := exec.Command(exe, rest...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// RestartReplacesProcess is false here: Restart spawns a child and returns, so the
// caller must exit itself once it succeeds.
const RestartReplacesProcess = false
