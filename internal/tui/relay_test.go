package tui

import (
	"strings"
	"testing"
)

// TestRelayPillAndStatus covers the relay status pill: hidden when unconfigured,
// "connecting" once set, updated by a relayStatusMsg, and hidden again when
// disabled.
func TestRelayPillAndStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)

	if got := m.relayPill(); got != "" {
		t.Errorf("relayPill with no relay configured should be empty, got %q", got)
	}

	m.SetRelay("relay.example:9000")
	if m.relayState != RelayConnecting {
		t.Fatalf("SetRelay should seed RelayConnecting, got %v", m.relayState)
	}
	if got := m.relayPill(); !strings.Contains(got, "relay") || !strings.Contains(got, "connecting") {
		t.Errorf("connecting pill should mention relay + connecting, got %q", got)
	}

	next, _ := m.Update(relayStatusMsg{state: RelayConnected})
	m = next.(Model)
	if m.relayState != RelayConnected {
		t.Errorf("relayStatusMsg should update state, got %v", m.relayState)
	}
	if got := m.relayPill(); !strings.Contains(got, "connected") {
		t.Errorf("connected pill should say connected, got %q", got)
	}

	// A second TUI on the same machine can't hold the connector slot, so it shows
	// "shared" (reachable via the holder) rather than being stuck on "connecting".
	next, _ = m.Update(relayStatusMsg{state: RelayShared})
	m = next.(Model)
	if got := m.relayPill(); !strings.Contains(got, "relay") || !strings.Contains(got, "shared") {
		t.Errorf("shared pill should mention relay + shared, got %q", got)
	}

	m.SetRelay("")
	if got := m.relayPill(); got != "" {
		t.Errorf("relayPill after disable should be empty, got %q", got)
	}
}

func TestRelayStateLabel(t *testing.T) {
	cases := map[RelayState]string{
		RelayOff:        "",
		RelayConnecting: "connecting",
		RelayConnected:  "connected",
		RelayRetrying:   "retrying",
		RelayShared:     "shared",
	}
	for s, want := range cases {
		if got := s.label(); got != want {
			t.Errorf("RelayState(%d).label() = %q, want %q", s, got, want)
		}
	}
}

// TestRelayReporterDelivers verifies RelayReporter sends a relayStatusMsg the
// model's Update loop understands.
func TestRelayReporterDelivers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 80, 24)
	m.SetRelay("h:1")
	// Simulate what RelayReporter -> prog.Send delivers.
	next, _ := m.Update(relayStatusMsg{state: RelayRetrying})
	if next.(Model).relayState != RelayRetrying {
		t.Error("retrying state should be applied")
	}
}
