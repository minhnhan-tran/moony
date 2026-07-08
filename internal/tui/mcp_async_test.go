package tui

import (
	"strings"
	"testing"
)

// TestStartupHoldsTurnsThenReleases covers the staged startup behavior: while
// the background checks (user → MCP → LSP) run, a model prompt is held
// (queued) and no turn starts; slash commands are not held; and once the
// sequence finishes (startupDoneMsg) the held prompt is released and the turn
// begins. Tool registration (applyMCP) runs on the update loop, so it never
// races a turn goroutine.
func TestStartupHoldsTurnsThenReleases(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)
	m.BeginStartupChecks(true, false) // user + MCP
	if !m.startupActive {
		t.Fatal("BeginStartupChecks should mark startup active")
	}
	if len(m.startupChecks) != 2 {
		t.Fatalf("expected user + MCP checks, got %d", len(m.startupChecks))
	}

	// A slash command is NOT held while starting up (it doesn't start a turn).
	m.input.SetValue("/help")
	res, _ := m.submit()
	m = res.(Model)
	if len(m.queued) != 0 {
		t.Errorf("slash command should not be queued during startup: %v", m.queued)
	}

	// A plain model prompt IS held: queued, with no turn started.
	m.input.SetValue("hello world")
	res, _ = m.submit()
	m = res.(Model)
	if m.state != stateIdle {
		t.Errorf("a model turn must not start during startup; state=%v", m.state)
	}
	if len(m.queued) != 1 || m.queued[0] != "hello world" {
		t.Fatalf("the prompt should be queued until ready, got %v", m.queued)
	}

	// Step through the checks. the user resolves with a username.
	res, _ = m.Update(startupStepMsg{step: stepUser})
	m = res.(Model)
	if m.startupChecks[0].state != checkRunning {
		t.Errorf("the user check should be running after its step msg, got %v", m.startupChecks[0].state)
	}
	res, _ = m.Update(userResultMsg{user: "Jane Appleseed"})
	m = res.(Model)
	if m.startupChecks[0].state != checkOK {
		t.Errorf("the user check should be OK after a successful result, got %v", m.startupChecks[0].state)
	}
	if m.trans.userLabel != "Jane Appleseed" {
		t.Errorf("a successful user result should set the user label, got %q", m.trans.userLabel)
	}

	// MCP connects (nil manager = nothing to register, 0 connected).
	res, _ = m.Update(startupStepMsg{step: stepMCP})
	m = res.(Model)
	res, _ = m.Update(mcpResultMsg{mgr: nil})
	m = res.(Model)
	if m.startupChecks[1].step != stepMCP || m.startupChecks[1].state != checkOK || m.startupChecks[1].detail != "0 connected" {
		t.Errorf("MCP check should read OK / 0 connected, got %+v", m.startupChecks[1])
	}

	// The held prompt must still be queued until the sequence finishes.
	if len(m.queued) != 1 {
		t.Fatalf("the prompt should still be held mid-sequence, queue=%v", m.queued)
	}

	// startupDoneMsg ends startup: turn-holding stops and the held question
	// drains, starting a turn — which also dismisses the startup panel.
	res, _ = m.Update(startupDoneMsg{})
	m = res.(Model)
	if m.startupActive {
		t.Error("startupDoneMsg should clear startupActive")
	}
	if len(m.queued) != 0 {
		t.Errorf("the held message should have been drained once ready, queue=%v", m.queued)
	}
	if m.state != stateBusy {
		t.Errorf("draining the held prompt should start a turn (stateBusy), got %v", m.state)
	}
	if len(m.startupChecks) != 0 {
		t.Errorf("starting a turn should dismiss the startup panel, got %d checks", len(m.startupChecks))
	}
}

// TestStartupUserWarning verifies that a failed local-user check marks
// its row as a warning and surfaces a prominent transcript notice.
func TestStartupUserWarning(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)
	m.BeginStartupChecks(false, false) // user only

	res, _ := m.Update(userResultMsg{errMsg: "session expired"})
	m = res.(Model)
	if m.startupChecks[0].state != checkWarn {
		t.Errorf("a failed user result should warn, got %v", m.startupChecks[0].state)
	}

	out := m.View()
	if !strings.Contains(out, "Could not resolve the local user account") {
		t.Fatalf("the user warning should surface in the transcript; got:\n%s", out)
	}
}

// TestStartupViewRendersChecks verifies the startup panel renders one row per
// configured check, and that it stays on screen after startup finishes.
func TestStartupViewRendersChecks(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)
	m.BeginStartupChecks(true, true) // user + MCP + LSP

	out := m.View()
	for _, label := range []string{"Checking user", "Checking MCP", "Checking LSP"} {
		if !strings.Contains(out, label) {
			t.Errorf("startup panel should render %q; got:\n%s", label, out)
		}
	}

	// The panel persists after startup ends (the user keeps seeing the results)…
	res, _ := m.Update(startupDoneMsg{})
	m = res.(Model)
	if !strings.Contains(m.View(), "Checking user") {
		t.Error("the startup panel should stay visible after startupDoneMsg")
	}

	// …until the user sends their first question, at which point it's dismissed.
	m.input.SetValue("what does this repo do?")
	res, _ = m.submit()
	m = res.(Model)
	if len(m.startupChecks) != 0 {
		t.Errorf("sending a question should dismiss the startup panel, got %d checks", len(m.startupChecks))
	}
	if strings.Contains(m.View(), "Checking user") {
		t.Error("the startup panel should be gone once the user sends a question")
	}
}
