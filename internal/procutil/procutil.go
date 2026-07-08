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
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}
