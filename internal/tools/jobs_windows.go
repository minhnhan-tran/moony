//go:build windows

package tools

import (
	"os/exec"
	"time"
)

// configureProcess bounds how long Wait() may block after a job is canceled.
// Windows has no process groups via SysProcAttr.Setpgid the way Unix does;
// exec.CommandContext's default Kill of the child plus a WaitDelay is enough to
// unblock Wait() promptly here (the CI targets that exercise process-group kill
// are the Unix ones).
func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
