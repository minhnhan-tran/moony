package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// processAlive is the gate in front of the whole coexistence check: if it answers
// "dead" for a live holder, every connector concludes the slot is free, claims it,
// and they evict each other off the relay in a loop. It used to do exactly that on
// Windows, where os.Process.Signal only supports Kill and returns EWINDOWS for the
// signal-0 probe even when the process is running.
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this very process reported as not alive")
	}
	for _, pid := range []int{0, -1, -12345} {
		if processAlive(pid) {
			t.Errorf("pid %d reported alive", pid)
		}
	}

	// A real child, before and after it exits — the transition the lock depends on.
	cmd := exec.Command(os.Args[0], "-test.run=TestHoldSlotHelper", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "MOONY_HOLD_SLOT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !processAlive(pid) {
		t.Errorf("running child %d reported as not alive", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // reap, so the pid is fully gone rather than a zombie
	if processAlive(pid) {
		t.Errorf("killed child %d still reported alive", pid)
	}
}

// The identity half: without a working name lookup pidIsOurs can only fail safe and
// assume every live pid in the lock is ours, which would let a recycled pid pin the
// slot forever. Calls lookupProcessName directly so a stubbed processNameFn in
// another test cannot mask a missing platform implementation.
func TestLookupProcessNameResolvesSelf(t *testing.T) {
	name, ok := lookupProcessName(os.Getpid())
	if !ok {
		t.Skipf("no process-name lookup on %s/%s", os.Getenv("GOOS"), os.Getenv("GOARCH"))
	}
	want := strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))
	got := strings.ToLower(strings.TrimSuffix(name, ".exe"))
	if got != want && !sameExecName(got, want) {
		t.Errorf("lookupProcessName(self) = %q, want the test binary %q", name, want)
	}
}
