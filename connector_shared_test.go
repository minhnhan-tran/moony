package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pysty1995/moony/internal/daemon"
	"github.com/pysty1995/moony/internal/tui"
)

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

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	lock := filepath.Join(home, ".config", "moony", "connector.lock")
	os.MkdirAll(filepath.Dir(lock), 0o755)
	// A `go run .` holder records itself as "main".
	os.WriteFile(lock, []byte(strconv.Itoa(cmd.Process.Pid)+"\nmain"), 0o644)
	daemon.PublishConnectorStatus("connected")

	pill := standbyRelayState(daemon.ConnectorStatus())
	t.Logf("second terminal pill = %v (RelayShared=%v)", pill, tui.RelayShared)
	if pill != tui.RelayShared {
		t.Errorf("want the 'shared' pill, got %v", pill)
	}
}
