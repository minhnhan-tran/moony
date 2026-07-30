//go:build windows

package tools

import (
	"os/exec"
	"time"
)

// configureProcessGroup: on Windows there's no process-group SIGKILL, so we
// only set WaitDelay as a backstop against a hung Wait. exec.CommandContext's
// default cancel (Process.Kill) still terminates the direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
}
