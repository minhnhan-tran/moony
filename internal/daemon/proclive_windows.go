//go:build windows

package daemon

import (
	"errors"

	"golang.org/x/sys/windows"
)

// processAlive reports whether pid is a currently running process.
//
// The portable Signal(0) probe cannot be used here: os.Process.Signal supports
// only Kill on Windows and returns EWINDOWS ("not supported by windows") for
// everything else — including for a process that is very much alive. Reading
// that error as "no such process" is what let a second connector decide the lock
// was stale, claim the slot, and fight the live holder for the relay instead of
// standing by as "shared".
//
// Liveness is decided by waiting on the process handle with a zero timeout:
// WAIT_TIMEOUT means it has not signalled, i.e. it is still running.
// GetExitCodeProcess is the usual suggestion but cannot distinguish a live
// process from one that exited with code 259 (STILL_ACTIVE).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// PROCESS_QUERY_LIMITED_INFORMATION is the least privilege that still works
	// across integrity levels; SYNCHRONIZE is what lets us wait on the handle.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Only a definite "there is no such pid" means dead. Access denied (a
		// holder running as another user or at higher integrity) means the
		// process exists, and any unexpected error leaves us unable to tell —
		// both must count as ALIVE, matching pidIsOurs's stance that an
		// undeterminable holder keeps the slot. Guessing "free" is precisely
		// what produced the mutual-eviction loop.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(h)

	switch ev, err := windows.WaitForSingleObject(h, 0); {
	case err != nil:
		return true // handle opened but unwaitable: assume alive
	case ev == uint32(windows.WAIT_TIMEOUT):
		return true // still running
	default:
		return false // signalled => exited
	}
}
