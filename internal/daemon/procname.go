package daemon

// Process identity lookup for the connector lock. Kept separate from
// connector.go so tests can stub it, and so the platform-specific mechanism has
// one obvious home.

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// processNameFn resolves a pid to its executable name. Indirected through a
// variable so tests can simulate a recycled pid, a vanished process, or a
// platform where the lookup fails, without spawning real processes.
var processNameFn = lookupProcessName

// processName returns the executable name for pid. The second result is false
// when the name could not be determined at all (lookup unavailable or errored),
// which callers treat as "unknown" rather than "not ours".
func processName(pid int) (string, bool) {
	return processNameFn(pid)
}

// lookupProcessName resolves a pid to its executable name, preferring procfs on
// Linux because `ps -o comm=` truncates there (see psProcessName).
func lookupProcessName(pid int) (string, bool) {
	if runtime.GOOS == "windows" {
		// There is no procfs and no ps here; the Win32 query is the only source
		// (procname_windows.go).
		return platformProcessName(pid)
	}
	if runtime.GOOS == "linux" {
		if name, ok := procfsProcessName(pid); ok {
			return name, true
		}
		// procfs unreadable for an unexpected reason — fall back to ps rather
		// than reporting "unknown" straight away.
	}
	return psProcessName(pid)
}

// procfsProcessName reads argv[0] from /proc/<pid>/cmdline. Unlike `ps -o comm=`
// this is NOT truncated to TASK_COMM_LEN, and because the fields are
// NUL-separated an executable path containing spaces survives intact.
func procfsProcessName(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		if os.IsNotExist(err) {
			return "", true // definite: no such process
		}
		return "", false // unreadable ⇒ unknown
	}
	argv0, _, _ := strings.Cut(string(data), "\x00")
	if argv0 = strings.TrimSpace(argv0); argv0 == "" {
		return "", false // kernel threads have an empty cmdline ⇒ unknown
	}
	return argv0, true
}

// psProcessName shells out to ps, which is present on macOS and Linux and needs
// no cgo. `ps -o comm=` prints the executable path (macOS) or bare name (Linux)
// with no header; a non-zero exit means no such process.
//
// On Linux the value is truncated to TASK_COMM_LEN-1 (15) characters. "moony" is
// well under that, but a differently-named build of this binary may not be, so
// pidIsOurs tolerates truncation and procfs is still tried first there.
func psProcessName(pid int) (string, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		// Exit status 1 is ps's "no such process" — a definite answer that the
		// pid is gone, not a failed lookup.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", true
		}
		return "", false // ps missing or unusable ⇒ unknown
	}
	return strings.TrimSpace(string(out)), true
}
