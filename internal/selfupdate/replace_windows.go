//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// backupSuffix prefixes the name the outgoing image is parked under. It stays in
// the executable's own directory so the moves below are same-volume renames.
const backupSuffix = ".old-"

// replaceExecutable swaps the freshly staged binary in for the running one.
//
// Windows will not let a running image be overwritten: renaming onto it fails
// with ERROR_ACCESS_DENIED (verified: "os.Rename(staged -> RUNNING exe): Access
// is denied"), which is why /update used to download and verify a new build and
// then fall over on its very last step.
//
// Renaming the running image AWAY is permitted, though, so the swap goes
// aside-then-move. The parked file cannot be deleted while its process lives, so
// deletion is left to CleanupStaleBackups on a later start.
func replaceExecutable(staged, exePath string) error {
	backup := exePath + backupSuffix + strconv.FormatInt(time.Now().UnixNano(), 36)

	if err := os.Rename(exePath, backup); err != nil {
		return fmt.Errorf("cannot move the running executable aside: %w", err)
	}
	if err := os.Rename(staged, exePath); err != nil {
		// Put the old binary back, or this install would leave no moony at all
		// at the path the user (and the service manager) invokes.
		if rollback := os.Rename(backup, exePath); rollback != nil {
			return fmt.Errorf("cannot install %s (%w) and cannot restore the previous binary from %s (%v) — restore it by hand",
				exePath, err, backup, rollback)
		}
		return fmt.Errorf("cannot install the new binary: %w", err)
	}

	// Best effort: this fails with "Access is denied" while the old image is still
	// running, which is the normal case. CleanupStaleBackups sweeps it later.
	_ = os.Remove(backup)
	return nil
}
