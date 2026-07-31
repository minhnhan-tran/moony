//go:build unix

// Package procutil spawns and terminates child processes (the MCP and LSP
// servers) so they can't corrupt the TUI.
//
// MCP/LSP servers are stdio JSON-RPC processes whose stdin/stdout/stderr we
// capture via pipes — but their launchers (npx, uvx, uv) and some servers write
// progress, upgrade notices, and warnings DIRECTLY to the controlling terminal
// (/dev/tty), bypassing those pipes. While the TUI holds the alternate screen,
// those stray writes garble the display until a resize forces a full repaint.
//
// Detach starts the child in its own session (setsid), so it has NO controlling
// terminal and its /dev/tty writes go nowhere; the captured pipes still carry its
// real stdio. Because that makes the child a process-group leader, Terminate kills
// the whole group so launcher grandchildren (npx→node, uvx→python) aren't left
// orphaned. Unix-only — the app targets macOS/Linux.
package procutil

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Detach configures cmd to start in a new session with no controlling terminal,
// so neither the child nor anything it spawns can write to the terminal the TUI
// is drawing on. Call before cmd.Start().
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// Terminate kills the process and its whole process group. After Detach the child
// is a group leader (pgid == pid), so signaling -pid reaches its descendants;
// falls back to a single-process kill if the group signal fails (e.g. the child
// wasn't Detach'd, or already exited).
func Terminate(p *os.Process) {
	if p == nil {
		return
	}
	// Guard the pid before negating it. kill(0, …) signals OUR OWN process group
	// (killing the agent), and kill(-1, …) signals every process we may signal —
	// so a zero/absurd pid from an already-reaped or never-started process must
	// never reach the group form.
	if p.Pid <= 1 {
		_ = p.Kill()
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}

// KillTree makes cmd killable as a whole process TREE rather than just the
// immediate child, and must be called before Start/Run/CombinedOutput.
//
// It exists because exec.CommandContext's default cancel only SIGKILLs the direct
// child. For `sh -c <command>` that kills the shell and leaves the actual work (a
// build, a server, a sleep) running. Worse, a surviving grandchild inherits the
// stdout/stderr pipe, so CombinedOutput/Wait blocks until THAT exits — which makes
// the context timeout and turn interrupts appear to be ignored entirely.
//
// Setpgid puts the child in its own process group; the Cancel hook signals the
// whole group; WaitDelay is the backstop that forces Wait to return shortly after
// cancel if some descendant still holds the pipe.
func KillTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Guard before negating: kill(0, …) would signal our OWN process group and
		// take the agent down with it, and kill(-1, …) would signal everything we
		// have permission to. Fall back to a single-process kill instead.
		if cmd.Process.Pid <= 1 {
			return cmd.Process.Kill()
		}
		// Negative pid targets the whole process group led by the child.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// The group may already be gone (or Setpgid failed); still make sure the
			// direct child dies, or Wait would hang until WaitDelay fires.
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
}
