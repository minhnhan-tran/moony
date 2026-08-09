package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/daemon"
	"github.com/pysty1995/moony/internal/tui"
)

// TestHoldSlotHelper is the long-lived child standing in for terminal 1. It used
// to be `sleep 30`, which does not exist on Windows — so this end-to-end test only
// ever ran on Unix, and the Windows-only liveness bug it is meant to catch shipped
// anyway.
func TestHoldSlotHelper(t *testing.T) {
	if os.Getenv("MOONY_HOLD_SLOT_HELPER") != "1" {
		t.Skip("helper process, not a test")
	}
	time.Sleep(60 * time.Second)
}

// End to end for the reported bug: with two terminals on one machine the second
// kept retrying to reach the relay instead of showing "shared".
//
// Terminal 1 here was started via `go run .` (see docs/development.md), so its
// process is named "main". The identity check used to demand the name contain
// "moony", rejected it, and let terminal 2 claim the slot too — both dialed, the
// relay (one connector per machine, last-wins) evicted each in turn, and neither
// ever settled. Terminal 2 must stand by and mirror the holder's state.
func TestE2ESecondTerminalShowsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	cmd := exec.Command(os.Args[0], "-test.run=TestHoldSlotHelper", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "MOONY_HOLD_SLOT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	lock := filepath.Join(home, ".config", "moony", "connector.lock")
	os.MkdirAll(filepath.Dir(lock), 0o755)
	// Record the holder under its real executable name. The point of this test is
	// that ANY legitimate name works — a `go run .` build is called "main", a
	// debugger's is "__debug_binNNNN" — and internal/daemon's coexist test covers
	// that matrix by stubbing the lookup. Here the lookup is real, so the recorded
	// name has to be the holder's actual one, or the identity check correctly
	// rejects it as a recycled pid and this stops testing coexistence at all.
	holder := filepath.Base(os.Args[0])
	os.WriteFile(lock, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"+holder), 0o644)
	daemon.PublishConnectorStatus("connected")

	// The decision that actually keeps this terminal off the relay (main.go:430).
	// Asserting only on the pill below would pass with the lock logic completely
	// broken, since standbyRelayState is a pure status-to-pill mapping — which is
	// how the Windows failure hid from this test.
	if release, ok := daemon.TryHoldConnector(); ok {
		if release != nil {
			release()
		}
		t.Error("second terminal claimed the connector slot from a live holder; it would dial the relay and fight terminal 1")
	}

	pill := standbyRelayState(daemon.ConnectorStatus())
	t.Logf("second terminal pill = %v (RelayShared=%v)", pill, tui.RelayShared)
	if pill != tui.RelayShared {
		t.Errorf("want the 'shared' pill, got %v", pill)
	}
}
