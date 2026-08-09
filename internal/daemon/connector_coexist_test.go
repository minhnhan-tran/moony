package daemon

// Regression tests for connector coexistence between two terminals on one machine.
//
// The bug: the lock's identity check compared the holder's process name against a
// hardcoded "moony". Legitimate holders are not always called that — `go run .`
// (documented in docs/development.md) builds a temp binary named "main", a debugger
// builds "__debug_binNNNN", and packagers rename freely. Those holders were rejected
// as "not one of ours", so a second terminal concluded the slot was free, dialed the
// relay too, and the two evicted each other in a reconnect loop — the user sees an
// endlessly retrying pill instead of "shared".
//
// The fix makes the lock self-describing: the holder records its own executable name
// alongside its pid, so identity no longer depends on guessing the binary's name.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHoldSlotHelper is not a test: it is the long-lived child that
// liveForeignProcess re-execs to stand in for "the other terminal". This file used
// to spawn `sleep`, which does not exist on Windows, so every coexistence test here
// was silently Unix-only — and Windows is exactly where the liveness probe was
// broken.
func TestHoldSlotHelper(t *testing.T) {
	if os.Getenv("MOONY_HOLD_SLOT_HELPER") != "1" {
		t.Skip("helper process, not a test")
	}
	time.Sleep(60 * time.Second)
}

// liveForeignProcess starts a real process to stand in for "the other terminal" and
// returns its pid. It re-execs the test binary so it works on every OS.
func liveForeignProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHoldSlotHelper", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "MOONY_HOLD_SLOT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// writeLockAs writes a lock recording pid and (when non-empty) an executable name.
func writeLockAs(t *testing.T, pid int, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := connectorLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := strconv.Itoa(pid)
	if name != "" {
		payload += "\n" + name
	}
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A holder running under any of these names must still own the slot, so the second
// terminal stands by and reports the holder's state instead of dialing.
func TestSecondTerminalStandsDownForAnyHolderName(t *testing.T) {
	for _, holder := range []struct{ name, as string }{
		{"installed binary", "moony"},
		{"windows binary", "moony.exe"},
		{"go run . temp build", "main"},
		{"debugger build", "__debug_bin40521"},
		{"renamed by a packager", "moony-nightly"},
		{"fully custom name", "agentctl"},
	} {
		t.Run(holder.name, func(t *testing.T) {
			pid := liveForeignProcess(t)
			writeLockAs(t, pid, holder.as)
			// The live process is really `sleep`; the recorded name is what the
			// holder claimed. Report the recorded name as the live one.
			stubProcessName(t, func(int) (string, bool) { return holder.as, true })

			if !connectorHeldByOther() {
				t.Errorf("a live holder named %q must keep the slot", holder.as)
			}
			if _, ok := TryHoldConnector(); ok {
				t.Errorf("second terminal claimed the slot from a live %q holder; "+
					"both would dial the relay and evict each other", holder.as)
			}
		})
	}
}

// The whole point of the identity check still has to work: a recycled pid now owned
// by an unrelated program must NOT keep the slot, or the machine is left with no
// connector at all.
func TestRecycledPidStillFreesTheSlot(t *testing.T) {
	pid := liveForeignProcess(t)
	writeLockAs(t, pid, "moony")
	// The recorded holder was "moony", but the pid is now a different program.
	stubProcessName(t, func(int) (string, bool) { return "/usr/bin/python3", true })

	if connectorHeldByOther() {
		t.Error("a recycled pid running an unrelated program must not hold the slot")
	}
}

// Locks written by an older build record only a pid. Those holders must still be
// honoured — including when they were started via `go run` — or upgrading strands
// the running terminal.
func TestLegacyPidOnlyLockStillHonoured(t *testing.T) {
	for _, name := range []string{"moony", "main", "__debug_bin991", "exe"} {
		t.Run(name, func(t *testing.T) {
			pid := liveForeignProcess(t)
			writeLockAs(t, pid, "") // no recorded name: pre-upgrade format
			stubProcessName(t, func(int) (string, bool) { return name, true })

			if !connectorHeldByOther() {
				t.Errorf("a legacy lock held by a live %q must still block a second dial", name)
			}
		})
	}
}

// A claim must record both pid and executable name, and releasing must remove it —
// comparing only the pid line. Getting that wrong strands the slot permanently.
func TestClaimRecordsIdentityAndReleaseClearsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	rel, ok := TryHoldConnector()
	if !ok {
		t.Fatal("claiming a free slot should succeed")
	}
	path := connectorLockPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pidLine, nameLine, found := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if !found || strings.TrimSpace(nameLine) == "" {
		t.Errorf("the lock should record our executable name, got %q", data)
	}
	if pidLine != strconv.Itoa(os.Getpid()) {
		t.Errorf("the first line should be our pid, got %q", pidLine)
	}

	rel()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("release must remove our own lock, or the slot is stranded for every later terminal")
	}
}

func TestSameExecName(t *testing.T) {
	same := [][2]string{
		{"moony", "moony"},
		{"/usr/local/bin/moony", "moony"},
		{"Moony", "moony"},
		{"moony.exe", "moony"},
		{"main", "main"},
		// Linux clamps comm to 15 chars, so a longer recorded name arrives cut.
		{"averyverylongbi", "averyverylongbinaryname"},
	}
	for _, c := range same {
		if !sameExecName(c[0], c[1]) {
			t.Errorf("sameExecName(%q, %q) should be true", c[0], c[1])
		}
	}
	differ := [][2]string{
		{"python3", "moony"},
		{"launchd", "main"},
		{"", "moony"},
		{"moony", ""},
		{"moo", "moony"}, // a short prefix is not a truncation
	}
	for _, c := range differ {
		if sameExecName(c[0], c[1]) {
			t.Errorf("sameExecName(%q, %q) should be false", c[0], c[1])
		}
	}
}

// A truncated comm must only be accepted when it really is at the clamp length,
// otherwise any short prefix would match an unrelated binary.
func TestSameExecNameTruncationNeedsFullLength(t *testing.T) {
	long := "averyverylongbinaryname"
	if !sameExecName(long[:linuxCommLen], long) {
		t.Error("a name clamped to exactly 15 chars should match its full form")
	}
	if sameExecName(long[:5], long) {
		t.Error("a 5-char prefix is not a comm truncation and must not match")
	}
}
