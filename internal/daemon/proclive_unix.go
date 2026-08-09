//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// processAlive reports whether pid is a currently running process.
//
// Signal 0 performs the kernel's permission and existence checks without
// delivering anything, which is the portable POSIX way to ask "is this pid
// live?". EPERM (a process owned by another user) still means the pid exists, so
// only a plain failure to find the process counts as dead.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
