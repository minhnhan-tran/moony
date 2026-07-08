//go:build !unix

package schedule

import "os"

// lockFile is a no-op on platforms without flock (e.g. Windows). withLock
// already documents locking as best-effort — it runs fn even when the lock
// can't be acquired — so here the load→modify→save cycle simply runs
// unsynchronized. The single-process daemon path remains correct; only the
// cross-process guard is absent.
func lockFile(*os.File) error { return nil }

// unlockFile is the no-op counterpart to lockFile.
func unlockFile(*os.File) error { return nil }
