//go:build unix

package schedule

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory (flock) lock on f, blocking until it is
// available. Unix-only; withLock treats a failure here as best-effort and runs
// unsynchronized rather than disabling scheduling.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the advisory lock taken by lockFile.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
