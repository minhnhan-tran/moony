//go:build unix

package procutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDetachSetsSetsid(t *testing.T) {
	cmd := exec.Command("true")
	Detach(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("Detach should set SysProcAttr.Setsid")
	}
	// Preserves a pre-existing SysProcAttr rather than replacing it.
	cmd2 := exec.Command("true")
	cmd2.SysProcAttr = &syscall.SysProcAttr{}
	Detach(cmd2)
	if !cmd2.SysProcAttr.Setsid {
		t.Error("Detach should set Setsid on an existing SysProcAttr")
	}
}

func TestTerminateNilSafe(t *testing.T) {
	Terminate(nil) // must not panic
}

// TestDetachStartsNewSession spawns a detached child and confirms it leads its own
// process group (pgid == pid), distinct from ours — i.e. setsid took effect, so it
// has no controlling terminal to corrupt — then Terminate cleans it up.
func TestDetachStartsNewSession(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	Detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start child: %v", err)
	}
	defer Terminate(cmd.Process)

	childPgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid(child): %v", err)
	}
	if childPgid != cmd.Process.Pid {
		t.Errorf("detached child should lead its own group: pgid=%d pid=%d", childPgid, cmd.Process.Pid)
	}
	if ourPgid, _ := syscall.Getpgid(0); childPgid == ourPgid {
		t.Errorf("detached child must not share the parent's process group (%d)", ourPgid)
	}
}

// KillTree must kill the whole process tree on cancel, and let Wait return
// promptly even when a descendant inherited the output pipe. Without it, a
// cancelled `sh -c` leaves its children running and Wait blocks until they exit —
// so timeouts and interrupts are silently ignored.
func TestKillTreeKillsGrandchildrenAndUnblocksWait(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "alive")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c",
		"sh -c 'sleep 4; touch "+marker+"' & sleep 30")
	KillTree(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_ = cmd.Wait()
	if el := time.Since(start); el > 8*time.Second {
		t.Fatalf("Wait returned after %s — a descendant kept the pipe open", el)
	}

	time.Sleep(5 * time.Second) // past the grandchild's sleep
	if _, err := os.Stat(marker); err == nil {
		t.Error("grandchild survived cancellation: process tree was not killed")
	}
}
