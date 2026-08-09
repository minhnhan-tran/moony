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
		if err := os.WriteFile(tmp, []byte(lockPayload()), 0o644); err != nil {
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
			// The lock is "pid\nexecutable"; compare only the pid line, or we'd
			// never recognise our own lock and would leave it behind on exit —
			// stranding the slot for every later terminal.
			pidText, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
			if pid, _ := strconv.Atoi(strings.TrimSpace(pidText)); pid == os.Getpid() {
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
// process's pid that is actually one of ours.
//
// Liveness alone is not enough. A pid is only a small integer that the OS recycles
// (kern.maxproc is 12k on macOS), so after an instance exits without releasing — a
// crash, SIGKILL, or a laptop that slept and woke — an unrelated process can inherit
// its number. Without an identity check the daemon would see a "live holder" forever
// and stand by indefinitely, leaving this machine with no connector and no way to
// recover short of deleting the lock by hand.
func lockHeldByOther(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pidText, wantName, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		return false // no such live process
	}
	return pidIsOurs(pid, strings.TrimSpace(wantName))
}

// lockPayload is what a holder writes: its pid and the base name of its own
// executable, newline-separated.
//
// Recording the name makes the lock self-describing, which matters because the
// holder is not always called "moony": `go run .` produces a temp binary called
// "main", a debugger builds "__debug_binNNNN", and packagers rename freely.
// Guessing the name instead — as this used to — rejected those holders, so a second
// terminal believed the slot was free, dialed the relay too, and the two evicted
// each other in a reconnect loop instead of one showing "shared".
func lockPayload() string {
	return strconv.Itoa(os.Getpid()) + "\n" + selfExecName()
}

// selfExecName is the base name of this process's executable, lowercased.
func selfExecName() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return connectorProcessName // fall back to the build's own name
	}
	return strings.ToLower(filepath.Base(exe))
}

// pidIsOurs reports whether pid looks like one of our own processes, so a recycled
// pid belonging to an unrelated program is not mistaken for a live connector holder.
//
// Deliberately fail-safe in the "held" direction: when the identity of a live pid
// cannot be determined (an unexpected ps failure, or a platform where the lookup is
// unavailable) the historical behaviour is kept and the slot is treated as held.
// Guessing "free" instead would let two instances dial the relay and evict each
// other in a reconnect loop, which is worse than a delayed takeover.
func pidIsOurs(pid int, wantName string) bool {
	name, ok := processName(pid)
	if !ok {
		return true // unknown ⇒ assume held (never risk a dual-dial flap)
	}
	if name == "" {
		return false // the process vanished between the liveness probe and here
	}
	// Prefer the name the holder recorded for itself: it is authoritative and
	// covers `go run`, debug builds and renamed binaries that a hardcoded name
	// would wrongly reject.
	if wantName != "" {
		return sameExecName(name, wantName)
	}
	return nameLooksLikeConnector(name)
}

// sameExecName compares a live process's executable name with the one recorded in
// the lock, tolerating Linux's 15-char comm truncation and a ".exe" suffix.
func sameExecName(live, recorded string) bool {
	l := strings.ToLower(strings.TrimSpace(filepath.Base(live)))
	r := strings.ToLower(strings.TrimSpace(filepath.Base(recorded)))
	if l == "" || r == "" {
		return false
	}
	l = strings.TrimSuffix(l, ".exe")
	r = strings.TrimSuffix(r, ".exe")
	if l == r {
		return true
	}
	// comm is clamped to 15 chars, so a longer name arrives truncated.
	return strings.HasPrefix(r, l) && len(l) >= linuxCommLen
}

// nameLooksLikeConnector matches an executable name against this binary's name,
// comparing base names so it survives being run from ./bin, ~/.local/bin or a
// Homebrew prefix.
func nameLooksLikeConnector(name string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if base == "" {
		return false
	}
	if strings.Contains(base, connectorProcessName) {
		return true
	}
	// A lock written by an older build records only a pid, so we must guess from
	// the process name. Accept the shapes a legitimate holder can take when it
	// wasn't installed as "moony": `go run .` builds into .../b001/exe/main, and
	// a debugger builds __debug_binNNNN. Being permissive here only risks a
	// delayed takeover; being too strict causes two live connectors to fight,
	// which is the far worse failure.
	if base == "main" || base == "exe" || strings.HasPrefix(base, "__debug_bin") {
		return true
	}
	// Linux reports a truncated comm; only relevant if the binary name exceeds it.
	if truncatedConnectorProcessName != connectorProcessName {
		return strings.Contains(base, truncatedConnectorProcessName)
	}
	return false
}

// connectorProcessName is the substring expected in a connector-holding process's
// executable name.
const connectorProcessName = "moony"

// linuxCommLen is TASK_COMM_LEN-1: the kernel stores a task's comm in a 16-byte
// field including the NUL terminator, so names are clamped to 15 characters.
const linuxCommLen = 15

// truncatedConnectorProcessName is connectorProcessName as Linux would report it
// via comm. "moony" is shorter than the limit, so this is normally identical to
// connectorProcessName — the slice is guarded so a longer rename stays correct
// instead of panicking.
var truncatedConnectorProcessName = truncateComm(connectorProcessName)

// truncateComm clamps a name the way Linux's comm field does. Written as a function
// over a variable rather than a constant slice expression, because the compiler
// rejects connectorProcessName[:linuxCommLen] outright when the name is shorter
// than the limit — as "moony" is.
func truncateComm(name string) string {
	if len(name) > linuxCommLen {
		return name[:linuxCommLen]
	}
	return name
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
