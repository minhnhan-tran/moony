//go:build !windows

package daemon

// platformProcessName has no non-Windows implementation: lookupProcessName
// already covers Linux (procfs) and the BSD/macOS `ps` fallback directly.
func platformProcessName(int) (string, bool) { return "", false }
