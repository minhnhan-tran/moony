//go:build !windows

package tools

import (
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
func configureProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// With Setpgid the child is its own group leader (pgid == pid), so
		// -pid targets the whole group. Fall back to the single process if the
		// group signal fails.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}
