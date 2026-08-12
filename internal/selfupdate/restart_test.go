package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// After a successful install the TUI restarts into the new build. That used to go
// through syscall.Exec unconditionally, which on Windows is a stub returning "not
// supported by windows" — and main discarded the error, so the update silently left
// the user back in the shell on the old binary. Restart is platform-split now, and
// RestartReplacesProcess tells the caller whether it must exit afterwards.
func TestRestartSpawnsSuccessorWhereThereIsNoExecve(t *testing.T) {
	if RestartReplacesProcess {
		t.Skip("execve platform: calling Restart here would replace the test process itself")
	}

	// The successor: this test binary with a filter that matches the helper but
	// without its env, so it skips and exits immediately instead of lingering.
	exe := os.Args[0]
	args := []string{exe, "-test.run=TestRunningTargetHelper", "-test.timeout=60s"}
	if err := Restart(exe, args, os.Environ()); err != nil {
		t.Errorf("Restart could not launch the successor: %v", err)
	}
}

func TestRestartReportsAFailedLaunch(t *testing.T) {
	if RestartReplacesProcess {
		t.Skip("execve platform: a failed exec is reported by syscall.Exec's return")
	}
	missing := filepath.Join(t.TempDir(), "no-such-binary")
	if err := Restart(missing, []string{missing}, os.Environ()); err == nil {
		t.Error("Restart reported success for a nonexistent executable; main would exit and leave nothing running")
	}
}
