package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestConnectorLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// No lock file yet → the connector is free.
	if connectorHeldByOther() {
		t.Fatal("no lock file should mean the connector is free")
	}

	path := connectorLockPath()
	if path == "" {
		t.Fatal("could not resolve the connector lock path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	// A live, different pid (our parent — same user, signalable) → held.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if !connectorHeldByOther() {
		t.Error("a live foreign pid should report the connector as held")
	}

	// A stale pid (no such process) → free again.
	if err := os.WriteFile(path, []byte("2147483646"), 0o644); err != nil {
		t.Fatal(err)
	}
	if connectorHeldByOther() {
		t.Error("a dead pid should be treated as a stale lock (free)")
	}

	// MarkConnectorActive writes our own pid, which never counts as "another"
	// holder (so the daemon doesn't yield to itself), and writes the file.
	release := MarkConnectorActive()
	if connectorHeldByOther() {
		t.Error("our own pid must not count as another connector holder")
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock = %q, want our pid %d", strings.TrimSpace(string(data)), os.Getpid())
	}
	// Release removes our lock.
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("release should remove the lock file (stat err = %v)", err)
	}
}

func TestTryHoldConnector(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := connectorLockPath()
	if path == "" {
		t.Fatal("no connector lock path")
	}

	// A free slot can be claimed; release removes the lock.
	rel, ok := TryHoldConnector()
	if !ok {
		t.Fatal("claim on a free slot should succeed")
	}
	// The claim must publish our pid atomically — never an empty lock file. The
	// empty window is what let a second instance starting at the same time read
	// the lock, treat it as stale, delete it, and also claim, so both dialed the
	// relay and flapped.
	if data, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("claimed lock = %q (err %v), want our pid %d (never empty)", strings.TrimSpace(string(data)), err, os.Getpid())
	}
	rel()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("release should remove the lock (stat err = %v)", err)
	}

	// A live, different holder (our parent) blocks the claim.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := TryHoldConnector(); ok {
		t.Error("claim should fail while a live process holds the slot")
	}

	// A stale lock (dead pid) is reclaimed.
	if err := os.WriteFile(path, []byte("2147483646"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel2, ok := TryHoldConnector()
	if !ok {
		t.Error("claim should reclaim a stale lock")
	}
	rel2()

	// An empty lock file (a legacy or aborted claim) is reclaimed and replaced
	// with a valid pid, never left empty.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rel3, ok := TryHoldConnector()
	if !ok {
		t.Error("claim should reclaim an empty lock")
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("after reclaiming an empty lock, lock = %q, want our pid", strings.TrimSpace(string(data)))
	}
	rel3()
}

func TestConnectorLegacyLockHonored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	legacy := legacyConnectorLockPath()
	if legacy == "" {
		t.Skip("legacy lock path coincides with the current path on this OS (e.g. Linux)")
	}

	// New-path lock absent, but a live process holds the LEGACY lock: an upgraded
	// build must still see the slot as held (so it stands down for a still-running
	// pre-2.16 connector instead of both dialing the relay and flapping).
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if !connectorHeldByOther() {
		t.Error("a live holder of the legacy lock must report the connector as held")
	}
	if _, ok := TryHoldConnector(); ok {
		t.Error("claim should fail while a live process holds the legacy lock")
	}

	// A stale legacy lock (dead pid) must not block a claim.
	if err := os.WriteFile(legacy, []byte("2147483646"), 0o644); err != nil {
		t.Fatal(err)
	}
	if connectorHeldByOther() {
		t.Error("a dead pid in the legacy lock should be treated as free")
	}
	rel, ok := TryHoldConnector()
	if !ok {
		t.Error("claim should succeed when the legacy lock is stale")
	}
	rel()
}

func TestConnectorStatusPublishRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Nothing published yet → unknown.
	if got := ConnectorStatus(); got != "" {
		t.Errorf("ConnectorStatus with no file = %q, want \"\"", got)
	}

	// Round-trip, including an overwrite (the holder republishes on each change).
	for _, want := range []string{"connecting", "connected", "retrying"} {
		PublishConnectorStatus(want)
		if got := ConnectorStatus(); got != want {
			t.Errorf("ConnectorStatus = %q, want %q", got, want)
		}
	}
}
