package web

import (
	"os"
	"strings"
	"testing"
)

// ActiveTurns gates the daemon's self-update. A wrong "0" here means the daemon
// relaunches mid-turn, and since the relaunch drops in-flight work with no chance to
// persist, the user silently loses a turn they were waiting on.
func TestActiveTurnsCountsBusyStates(t *testing.T) {
	srv, id := jobsFixture(t, "auto")
	sess := srv.openSession(id)
	if sess == nil {
		t.Fatal("no session")
	}

	// Idle must not block an update, or the daemon would never update at all.
	if n, _ := srv.ActiveTurns(); n != 0 {
		t.Errorf("idle session counted as active (n=%d)", n)
	}

	// Every state that holds real work must block. stateApproval/stateQuestion are
	// waiting on the USER — no tokens are streaming, but a relaunch still destroys the
	// pending prompt and the turn behind it.
	for _, st := range []string{stateBusy, stateApproval, stateQuestion, stateJobPrompt} {
		sess.mu.Lock()
		sess.state = st
		sess.mu.Unlock()
		n, name := srv.ActiveTurns()
		if n != 1 {
			t.Errorf("state %q: n = %d, want 1 — an update here would kill the turn", st, n)
		}
		if name == "" {
			t.Errorf("state %q: no label returned; the deferral log would be useless", st)
		}
	}

	// Back to idle: updates may proceed again.
	sess.mu.Lock()
	sess.state = stateIdle
	sess.mu.Unlock()
	if n, _ := srv.ActiveTurns(); n != 0 {
		t.Errorf("returned to idle but n = %d", n)
	}
}

// The daemon must re-check AFTER the download, because Apply can run for minutes and a
// turn may start meanwhile. The new binary is already on disk, so deferring is free.
func TestDaemonRechecksTurnsAfterDownload(t *testing.T) {
	b, err := os.ReadFile("../daemon/daemon.go")
	if err != nil {
		t.Fatalf("read daemon.go: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "func checkAndSelfUpdate")
	if i < 0 {
		t.Fatal("checkAndSelfUpdate not found")
	}
	body := src[i:]
	iApply := strings.Index(body, "selfupdate.Apply(")
	iRecheck := strings.Index(body, "activeTurns()")
	iExit := strings.Index(body, "os.Exit(0)")
	if iApply < 0 || iRecheck < 0 || iExit < 0 {
		t.Fatalf("expected Apply, a re-check and an exit; got %d/%d/%d", iApply, iRecheck, iExit)
	}
	if !(iApply < iRecheck && iRecheck < iExit) {
		t.Error("the turn re-check must sit between the download and the relaunch")
	}
	// And the loop must gate on the config toggle.
	if !strings.Contains(src, "cfg.AutoUpdate &&") {
		t.Error("the poll must be gated on cfg.AutoUpdate so a machine can be pinned")
	}
	// The pre-download check must exist too, so an obviously-busy daemon skips the work.
	if !strings.Contains(src, "srv.ActiveTurns(); n > 0") {
		t.Error("the loop must skip the check entirely while a turn is running")
	}
}
