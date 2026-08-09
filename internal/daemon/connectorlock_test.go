package daemon

// Connector-lock identity tests.
//
// The bug these pin: lockHeldByOther used to accept ANY live pid recorded in
// connector.lock. A pid is a small recycled integer, so after an instance died
// without releasing the lock (crash, SIGKILL, sleep/wake) an unrelated process
// could inherit its number — and every later instance would then see a "live
// holder" forever, stand by, and leave the machine with no connector at all.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func stubProcessName(t *testing.T, fn func(int) (string, bool)) {
	t.Helper()
	prev := processNameFn
	processNameFn = fn
	t.Cleanup(func() { processNameFn = prev })
}

func writeLockFile(t *testing.T, pid int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "connector.lock")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// livePidOtherThanOurs returns a pid that is alive but is not this process, so the
// `pid == os.Getpid()` short-circuit cannot answer first.
func livePidOtherThanOurs(t *testing.T) int {
	t.Helper()
	ppid := os.Getppid()
	if ppid <= 0 || ppid == os.Getpid() {
		t.Skip("no distinct live parent pid available")
	}
	return ppid
}

func TestLockHeldByOtherIgnoresRecycledPid(t *testing.T) {
	stubProcessName(t, func(int) (string, bool) { return "/usr/bin/python3", true })
	if lockHeldByOther(writeLockFile(t, livePidOtherThanOurs(t))) {
		t.Fatal("a live pid belonging to an unrelated process must NOT count as held — " +
			"that is the wedge that leaves the machine with no connector")
	}
}

func TestLockHeldByOtherHonorsRealHolder(t *testing.T) {
	stubProcessName(t, func(int) (string, bool) { return "/Users/x/.local/bin/moony", true })
	if !lockHeldByOther(writeLockFile(t, livePidOtherThanOurs(t))) {
		t.Fatal("a live moony process must still hold the connector")
	}
}

// Unknown identity must fail SAFE (held): two instances dialling the relay and
// evicting each other in a reconnect loop is worse than a delayed takeover.
func TestLockHeldByOtherUnknownIdentityStaysHeld(t *testing.T) {
	stubProcessName(t, func(int) (string, bool) { return "", false })
	if !lockHeldByOther(writeLockFile(t, livePidOtherThanOurs(t))) {
		t.Fatal("unknown identity must fail safe to held")
	}
}

// A definite "no such process" answer (name "", ok true) means the pid vanished
// between the liveness probe and the identity lookup: free, not held.
func TestLockHeldByOtherVanishedProcess(t *testing.T) {
	stubProcessName(t, func(int) (string, bool) { return "", true })
	if lockHeldByOther(writeLockFile(t, livePidOtherThanOurs(t))) {
		t.Fatal("a vanished process must not hold the connector")
	}
}

func TestLockHeldByOtherOwnPidIsNotOther(t *testing.T) {
	stubProcessName(t, func(int) (string, bool) { return "/usr/bin/python3", true })
	if lockHeldByOther(writeLockFile(t, os.Getpid())) {
		t.Error("our own pid is not another holder")
	}
}

func TestLockHeldByOtherMalformedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connector.lock")
	for _, content := range []string{"", "   ", "not-a-pid", "-1", "0"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if lockHeldByOther(path) {
			t.Errorf("a lock containing %q must not be treated as held", content)
		}
	}
	if lockHeldByOther(filepath.Join(dir, "does-not-exist")) {
		t.Error("a missing lock file must not be treated as held")
	}
	if lockHeldByOther("") {
		t.Error("an empty path must not be treated as held")
	}
}

func TestPidIsOursMatchesRegardlessOfInstallPath(t *testing.T) {
	for _, name := range []string{
		"moony",
		"/Users/admin/.local/bin/moony",
		"./bin/moony",
		"/opt/homebrew/bin/Moony",
		"moony.exe",
	} {
		stubProcessName(t, func(int) (string, bool) { return name, true })
		if !pidIsOurs(1234, "") {
			t.Errorf("%q should be recognized as one of ours", name)
		}
	}
	for _, name := range []string{"/sbin/launchd", "/usr/bin/python3", "node"} {
		stubProcessName(t, func(int) (string, bool) { return name, true })
		if pidIsOurs(1234, "") {
			t.Errorf("%q must not be mistaken for a connector holder", name)
		}
	}
}

// Linux clamps comm to 15 chars. "moony" is far shorter, so nothing is truncated —
// but the fallback must stay correct if the binary is ever renamed to something
// longer. This asserts the invariant rather than a hard-coded truncation.
func TestTruncatedConnectorNameInvariant(t *testing.T) {
	if len(connectorProcessName) <= linuxCommLen {
		if truncatedConnectorProcessName != connectorProcessName {
			t.Errorf("a short name must not be truncated: %q vs %q",
				truncatedConnectorProcessName, connectorProcessName)
		}
	} else if truncatedConnectorProcessName != truncateComm(connectorProcessName) {
		t.Errorf("truncation is wrong: %q", truncatedConnectorProcessName)
	}
	// truncateComm is what makes the above safe for either length.
	if got := truncateComm("short"); got != "short" {
		t.Errorf("truncateComm(short) = %q", got)
	}
	if got := truncateComm("abcdefghijklmnopqrstuvwxyz"); got != "abcdefghijklmno" {
		t.Errorf("truncateComm(long) = %q, want a 15-char clamp", got)
	}
	// A longer hypothetical name must still be recognised from its clamped form.
	if !nameLooksLikeConnector(truncateComm(connectorProcessName)) {
		t.Error("the clamped form of our own name must be recognised")
	}
}

func TestNameLooksLikeConnector(t *testing.T) {
	ours := []string{
		"moony",
		"/Users/admin/.local/bin/moony",
		"./bin/moony",
		"/opt/homebrew/bin/Moony", // case-insensitive
		"  /usr/local/bin/moony  ",
		"moony.exe",
		"/opt/x/moony-relay", // sibling binary, still ours
	}
	for _, n := range ours {
		if !nameLooksLikeConnector(n) {
			t.Errorf("%q should be recognized as one of ours", n)
		}
	}
	notOurs := []string{"/sbin/launchd", "/usr/bin/python3", "node", "", "   ", "moon", "oony"}
	for _, n := range notOurs {
		if nameLooksLikeConnector(n) {
			t.Errorf("%q must not be mistaken for a connector holder", n)
		}
	}
}

// The end-to-end shape of the bug, using a REAL unrelated live process rather than
// a stub, so the fix is proven against an actual recycled pid rather than a mock.
func TestRecycledPidWithRealProcess(t *testing.T) {
	pid := livePidOtherThanOurs(t)
	name, ok := processName(pid)
	if !ok {
		t.Skip("process identity lookup unavailable here")
	}
	if nameLooksLikeConnector(name) {
		t.Skipf("parent process %q is itself one of ours; cannot use it as an unrelated pid", name)
	}
	if lockHeldByOther(writeLockFile(t, pid)) {
		t.Fatalf("live but unrelated process %q (pid %d) must not hold the connector", name, pid)
	}
}

// The user-visible scenario: a previous instance died without releasing the lock,
// and an unrelated program later inherited its pid. A NEW terminal must be able to
// take the connector slot instead of standing by forever.
func TestSecondTerminalCanTakeOverAfterRecycledPid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	path := connectorLockPath()
	if path == "" {
		t.Fatal("no lock path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A live pid that is NOT one of ours (the recycled-pid case).
	stubProcessName(t, func(int) (string, bool) { return "/usr/bin/python3", true })
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}

	if connectorHeldByOther() {
		t.Fatal("the slot is wedged: a new terminal would stand by forever")
	}
	// And it can actually claim it.
	release, ok := TryHoldConnector()
	if !ok {
		t.Fatal("a new terminal could not claim the freed connector slot")
	}
	release()
	t.Log("a new terminal correctly reclaims the slot from a recycled pid")
}
