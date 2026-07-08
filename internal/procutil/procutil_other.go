//go:build !unix

package procutil

import (
	"os"
	"os/exec"
)

// Detach is a no-op on non-Unix platforms. The controlling-terminal corruption
// it guards against (stray child writes to /dev/tty while the TUI holds the
// alternate screen) is a Unix-specific concern; there is no setsid equivalent
// to apply here. See the Unix implementation for the full rationale.
func Detach(cmd *exec.Cmd) {}

// Terminate kills the process. The Unix build signals the whole process group
// so launcher grandchildren die too; without setsid-based grouping here we can
// only kill the process itself.
func Terminate(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
