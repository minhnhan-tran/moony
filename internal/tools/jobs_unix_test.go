//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A background job is killed by signalling its whole process GROUP (a negative pid), because
// a shell forks its command and the grandchild would otherwise survive holding the stdout
// pipe open. Negation is what makes an unchecked pid dangerous:
//
//	kill(0, sig)  → signals the caller's OWN process group, i.e. the agent itself
//	kill(-1, sig) → POSIX broadcast to every process the caller may signal
//
// So a zero/absurd pid (never-started or already-reaped process) must never reach the group
// form. procutil.KillTree guards `pid <= 1` for exactly this reason; this pins the same
// guarantee for background jobs, which previously had no guard at all.
func TestCancelNeverGroupKillsUnsafePid(t *testing.T) {
	for _, pid := range []int{0, 1, -1, -42} {
		t.Run("pid="+strconv.Itoa(pid), func(t *testing.T) {
			var signalled []int
			restore := stubKillGroup(t, func(pgid int, _ syscall.Signal) error {
				signalled = append(signalled, pgid)
				return nil
			})
			defer restore()

			cmd := exec.Command("sh", "-c", "true")
			configureProcess(cmd)
			// Fake an unsafe pid without starting anything: os.Process is what Cancel reads.
			cmd.Process = &os.Process{Pid: pid}

			// The error is irrelevant (killing a fake process fails); what matters is that
			// no group signal was attempted.
			_ = cmd.Cancel()

			for _, got := range signalled {
				t.Errorf("pid %d reached the GROUP kill as %d; kill(0)/kill(-1) would signal the agent's own group or broadcast", pid, got)
			}
		})
	}
}

// The guard must not break the actual feature: a safe pid still gets a group kill, since that
// is what reaps the forked grandchild.
func TestCancelGroupKillsSafePid(t *testing.T) {
	var signalled []int
	restore := stubKillGroup(t, func(pgid int, _ syscall.Signal) error {
		signalled = append(signalled, pgid)
		return nil
	})
	defer restore()

	cmd := exec.Command("sh", "-c", "true")
	configureProcess(cmd)
	cmd.Process = &os.Process{Pid: 4242}

	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(signalled) != 1 || signalled[0] != -4242 {
		t.Errorf("group signal = %v, want exactly [-4242] (the whole group, so grandchildren die)", signalled)
	}
}

// ESRCH means the group is already gone. That is success: the job is stopped, which is all the
// caller asked for. Treating it as failure previously fell through to a single-process kill
// that can only return ESRCH again, so a caller that checks the error saw a spurious failure.
func TestCancelTreatsMissingGroupAsStopped(t *testing.T) {
	restore := stubKillGroup(t, func(int, syscall.Signal) error { return syscall.ESRCH })
	defer restore()

	cmd := exec.Command("sh", "-c", "true")
	configureProcess(cmd)
	cmd.Process = &os.Process{Pid: 4242}

	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel returned %v for an already-gone group; a stopped process is success", err)
	}
}

// End-to-end: the guard must not regress the behaviour the group kill exists for. `sh -c` forks
// its command, so cancelling must kill the GRANDCHILD too — otherwise it survives holding the
// stdout pipe and Wait blocks until WaitDelay.
func TestCancelKillsForkedGrandchild(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/alive"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The inner `sh` records its own pid, then sleeps. Killing only the direct child would
	// leave this one running.
	cmd := exec.CommandContext(ctx, "sh", "-c",
		"sh -c 'echo $$ > "+marker+"; sleep 30' & wait")
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the grandchild to announce itself.
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 1 {
				pid = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Skip("grandchild never reported its pid; shell behaviour differs on this platform")
	}

	cancel()
	_ = cmd.Wait() // killed, so a non-nil error is expected

	// The grandchild must be gone. Poll: signal delivery is asynchronous.
	gone := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Getpgid returns ESRCH once the process is truly gone (a zombie would still
		// answer kill(pid, 0), so that check alone can pass on a corpse).
		if _, err := syscall.Getpgid(pid); err != nil {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(pid, syscall.SIGKILL) // don't leak it out of the test
		t.Errorf("forked grandchild %d survived cancellation; the group kill is not reaching it", pid)
	}
}

// stubKillGroup swaps the group-signal hook and returns a restore func.
func stubKillGroup(t *testing.T, fn func(int, syscall.Signal) error) func() {
	t.Helper()
	prev := killGroup
	killGroup = fn
	return func() { killGroup = prev }
}
