//go:build windows

package daemon

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// platformProcessName returns the executable name for pid on Windows.
//
// Without this the lookup returns "unknown", and pidIsOurs then has to assume any
// live pid in the lock belongs to us. That is safe against two connectors fighting
// over the relay, but it defeats the recycled-pid check the identity comparison
// exists for: after a crash the slot would stay "held" by whatever unrelated
// process later inherited the number, and this machine would never get a
// connector back.
func platformProcessName(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)

	// QueryFullProcessImageName works across 32/64-bit boundaries, unlike the
	// older GetModuleFileNameEx, and needs no PROCESS_VM_READ.
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", false
	}
	name := strings.TrimSpace(filepath.Base(windows.UTF16ToString(buf[:size])))
	if name == "" {
		return "", false
	}
	return strings.ToLower(name), true
}
