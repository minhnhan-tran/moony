//go:build !windows

package selfupdate

import "os"

// replaceExecutable swaps the freshly staged binary in for the running one.
//
// On Unix a running executable's inode stays valid for the processes that mapped
// it, so renaming a new file over the path is atomic and safe: existing processes
// keep running the old inode, and the next exec picks up the new one.
func replaceExecutable(staged, exePath string) error {
	return os.Rename(staged, exePath)
}

// backupSuffix is unused outside Windows; see replace_windows.go.
const backupSuffix = ".old-"
