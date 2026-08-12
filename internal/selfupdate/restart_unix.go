//go:build !windows

package selfupdate

import "syscall"

// Restart hands this process's pid, terminal and exit status over to exe, so the
// updated binary continues in place. It only returns if the exec failed.
func Restart(exe string, args, env []string) error {
	return syscall.Exec(exe, args, env)
}

// RestartReplacesProcess reports whether Restart takes this process over (execve)
// rather than spawning a child. Callers use it to decide whether the code after a
// successful Restart can still run.
const RestartReplacesProcess = true
