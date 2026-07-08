package daemon

// Connector coexistence: the relay accepts one connector per machine (keyed by
// owner+host, last-wins), so the always-on daemon and a hand-run TUI must not
// both hold it or they'd evict each other in a loop. The interactive TUI records
// itself as the active connector in a small lock file while it's connected; the
// daemon checks that file and stands down (stops dialing) until the TUI exits.
// So the TUI takes priority while open — its live in-terminal session is the
// drivable one — and the daemon resumes when you quit it.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/pysty1995/moony/internal/config"
)

// connectorLockPath is the file the foreground TUI writes its pid into while it
// holds the relay connector slot for this machine.
func connectorLockPath() string {
	p, err := config.GlobalConfigPath("connector.lock")
	if err != nil {
		return ""
	}
	return p
}

// MarkConnectorActive records this process as the active relay connector and
// returns a release func to call on exit. The interactive TUI calls it so the
// background daemon yields the connector while the TUI is connected. A no-op
// release is returned if the lock path can't be resolved.
func MarkConnectorActive() func() {
	path := connectorLockPath()
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
	return releaseConnectorFunc(path)
}

// TryHoldConnector atomically claims the per-machine relay-connector slot. It
// returns ok=false (and a no-op release) when another LIVE moony already
// holds it, so the caller runs relay-free instead of fighting over the relay's
// single per-(owner,host) connector — two connectors on one machine otherwise
// evict each other in a reconnect loop (the relay is last-wins). A stale lock
// (the recorded process is gone) is reclaimed.
//
// The claim is atomic WITH its pid already written: we write a temp file holding
// our pid, then hard-link it onto the lock path (os.Link fails if the path
// exists, and the linked file is never empty). A plain O_CREATE|O_EXCL would
// publish the lock empty and write the pid in a second step — a window in which a
// second instance starting at the same time reads the empty file, treats it as a
// stale lock, deletes it, and ALSO claims the slot. Both would then dial the
// relay and evict each other endlessly (the flapping pill when two TUIs start
// together).
func TryHoldConnector() (release func(), ok bool) {
	// A pre-2.16 build (config dir under os.UserConfigDir(), e.g. ~/Library/
	// Application Support on macOS) may already hold the slot via the legacy lock
	// path. Honor it so an in-progress upgrade doesn't run two connectors that
	// evict each other on the relay's single per-machine slot.
	if lockHeldByOther(legacyConnectorLockPath()) {
		return func() {}, false
	}
	path := connectorLockPath()
	if path == "" {
		return func() {}, true // can't resolve a lock path — don't block connecting
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}, true
	}
	// Process-unique temp name so two instances racing the claim never collide on
	// the staging file (only the final os.Link arbitrates the winner).
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			return func() {}, true // can't stage the lock — don't block connecting
		}
		err := os.Link(tmp, path) // atomic: fails if the lock already exists
		_ = os.Remove(tmp)
		if err == nil {
			return releaseConnectorFunc(path), true
		}
		if !os.IsExist(err) {
			return func() {}, true // unexpected error — don't block connecting
		}
		if connectorHeldByOther() {
			return func() {}, false // a live process holds the slot
		}
		_ = os.Remove(path) // stale lock (dead pid) — drop it and retry the claim
	}
	return func() {}, false
}

// releaseConnectorFunc removes the lock on exit, but only if it's still ours, so
// a newer holder's claim isn't clobbered when an older one exits.
func releaseConnectorFunc(path string) func() {
	return func() {
		if data, err := os.ReadFile(path); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid == os.Getpid() {
				_ = os.Remove(path)
			}
		}
	}
}

// connectorHeldByOther reports whether a different, live process (a running TUI
// or daemon) currently holds the connector slot. It checks both the current
// lock path AND the pre-2.16 legacy path (os.UserConfigDir()/moony,
// e.g. ~/Library/Application Support on macOS): when the config dir moved to
// ~/.config, a still-running connector from an older build keeps its lock at the
// legacy path, and without this a freshly-upgraded build wouldn't see it — so
// both would dial the relay and evict each other in a reconnect loop. A missing
// lock, an unparsable pid, our own pid, or a stale lock (recorded process gone)
// all count as "free".
func connectorHeldByOther() bool {
	return lockHeldByOther(connectorLockPath()) || lockHeldByOther(legacyConnectorLockPath())
}

// legacyConnectorLockPath is where pre-2.16 builds wrote connector.lock — under
// Go's os.UserConfigDir() (≈ ~/Library/Application Support/moony on
// macOS), before the config dir was pinned to ~/.config/moony. It
// returns "" when it can't be resolved or coincides with the current path (e.g.
// on Linux, where os.UserConfigDir() already is ~/.config) so it's only an extra
// check during the macOS upgrade transition.
func legacyConnectorLockPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	legacy := filepath.Join(base, "moony", "connector.lock")
	if legacy == connectorLockPath() {
		return ""
	}
	return legacy
}

// lockHeldByOther reports whether the lock file at path records a different, live
// process's pid.
func lockHeldByOther(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil // nil error ⇒ the process is alive
}

// connectorStatusPath is where the instance holding the connector slot publishes
// its live relay connectivity, so standby instances — which don't dial the relay
// themselves and so can't observe the tunnel directly — can reflect the holder's
// real state instead of blindly claiming the machine is reachable.
func connectorStatusPath() string {
	p, err := config.GlobalConfigPath("connector.status")
	if err != nil {
		return ""
	}
	return p
}

// PublishConnectorStatus records the connector holder's current relay state (an
// opaque token such as "connected", "connecting", or "retrying") for standby
// instances to read. Best effort and atomic (write-temp + rename); a no-op if
// the path can't be resolved.
func PublishConnectorStatus(status string) {
	path := connectorStatusPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(status), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// ConnectorStatus returns the relay state most recently published by the
// instance holding the connector slot, or "" when unknown (nothing published
// yet, or the holder is an older build). Standbys only consult it while a LIVE
// holder owns the lock, so a left-behind file from a since-exited holder is never
// read (that case reclaims the slot and promotes the standby instead).
func ConnectorStatus() string {
	path := connectorStatusPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
