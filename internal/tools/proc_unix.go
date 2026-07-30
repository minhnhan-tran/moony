//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup makes cmd killable as a whole process TREE, not just
// the immediate child. Two problems it solves for `sh -c <command>`:
//
//   - exec.CommandContext's default cancel only SIGKILLs the direct child
//     (the `sh`), leaving grandchildren (the actual `sleep`, build, server, …)
//     alive. We put the child in its own process group (Setpgid) and, on
//     cancel, signal the whole group (kill(-pgid)) so descendants die too.
//   - Even after the processes are gone, CombinedOutput()/Wait() block until
//     the stdout/stderr pipe write-ends close — but a lingering grandchild that
//     inherited them keeps the pipe open, so Wait can hang indefinitely. Group
//     kill fixes the common case; WaitDelay is the backstop that forces Wait to
//     return shortly after cancel even if some descendant still holds the pipe.
//
// The net effect: interrupting the turn (context cancel) actually stops a
// running bash command promptly instead of appearing to hang.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid targets the whole process group led by the child.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second
}
