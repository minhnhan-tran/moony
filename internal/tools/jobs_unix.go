//go:build !windows

package tools

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// configureProcess makes a background job killable as a unit on Unix.
//
// exec.CommandContext, on context cancel, only signals the direct child
// (here `sh`). A shell often forks its command (e.g. `sh -c "sleep 30"` under
// dash), so the grandchild survives AND keeps the stdout/stderr pipe open,
// which makes cmd.Wait() block indefinitely — the job then never reaches the
// "killed" state. Putting the child in its own process group and killing the
// whole group (negative PID) terminates the grandchildren too; WaitDelay is a
// belt-and-braces bound so Wait() still returns promptly even if something
// lingers on the pipe.
//
// The pid is guarded before it is negated — see the Cancel hook: a group kill on
// an unguarded pid can signal the agent's own process group, or every process it
// may signal.
// killGroup signals an entire process group. Indirected through a variable so a test can
// observe exactly which pid would reach the kernel — the whole point of the guard below is
// that a dangerous pid must never get here, and that is not observable if the real syscall
// is the only path.
var killGroup = func(pgid int, sig syscall.Signal) error { return syscall.Kill(pgid, sig) }

func configureProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Guard the pid BEFORE negating it. Negation is the hazard, not the pid: kill(0, …)
		// signals our OWN process group and would take the agent down with the job, and
		// kill(-1, …) is a POSIX broadcast to every process we have permission to signal.
		// A zero/absurd pid from a never-started or already-reaped process must therefore
		// never reach the group form. Mirrors procutil.KillTree, which guards identically.
		if cmd.Process.Pid <= 1 {
			return cmd.Process.Kill()
		}
		// With Setpgid the child is its own group leader (pgid == pid), so
		// -pid targets the whole group.
		if err := killGroup(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// ESRCH means the group is already gone — that is success, not failure, and
			// the single-process fallback would only return another ESRCH.
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			// Otherwise (e.g. Setpgid failed, so there is no group) still make sure the
			// direct child dies, or Wait would hang until WaitDelay fires.
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}
