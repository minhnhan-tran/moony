package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func TestRenderLaunchd(t *testing.T) {
	out := renderLaunchd(launchdParams{
		Label: launchdLabel, Exe: "/opt/bin/moony", Relay: "relay.example:8771",
		Log: "/tmp/daemon.log", Home: "/Users/x", Path: "/opt/bin:/usr/bin",
	})
	for _, want := range []string{
		`<plist version="1.0">`,
		"<string>" + launchdLabel + "</string>",
		"<string>/opt/bin/moony</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>MOONY_RELAY</key>",
		"<string>relay.example:8771</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/tmp/daemon.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("launchd plist missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSystemd(t *testing.T) {
	out := renderSystemd(systemdParams{Exe: "/opt/bin/moony", Relay: "relay.example:8771", Path: "/opt/bin:/usr/bin"})
	for _, want := range []string{
		"ExecStart=/opt/bin/moony daemon run",
		"Environment=MOONY_RELAY=relay.example:8771",
		"Environment=PATH=/opt/bin:/usr/bin",
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("systemd unit missing %q\n---\n%s", want, out)
		}
	}
}

func TestServicePATH(t *testing.T) {
	p := servicePATH("/opt/tools/moony")
	segs := strings.Split(p, ":")
	// The executable's dir must be present and ahead of the system dirs.
	if segs[0] != "/opt/tools" {
		t.Errorf("first PATH segment = %q, want the exe dir /opt/tools", segs[0])
	}
	if !contains(segs, "/usr/bin") {
		t.Errorf("PATH missing /usr/bin: %s", p)
	}
	// No empty or duplicate segments.
	seen := map[string]bool{}
	for _, s := range segs {
		if s == "" {
			t.Errorf("empty PATH segment in %q", p)
		}
		if seen[s] {
			t.Errorf("duplicate PATH segment %q in %q", s, p)
		}
		seen[s] = true
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a & b < c > d`); got != "a &amp; b &lt; c &gt; d" {
		t.Errorf("xmlEscape = %q", got)
	}
}

func TestRelayAddrFor(t *testing.T) {
	// No relay configured → the production default.
	if got := relayAddrFor(&config.Config{}); got != DefaultRelay {
		t.Errorf("empty cfg = %q, want default %q", got, DefaultRelay)
	}
	if got := relayAddrFor(nil); got != DefaultRelay {
		t.Errorf("nil cfg = %q, want default %q", got, DefaultRelay)
	}
	// An explicit relay address wins over the default.
	if got := relayAddrFor(&config.Config{RelayAddr: "relay.example:1234"}); got != "relay.example:1234" {
		t.Errorf("explicit relay = %q, want relay.example:1234", got)
	}
	// DefaultRelay is a placeholder that deployments must override.
	if DefaultRelay != "relay.example.com:8771" {
		t.Errorf("DefaultRelay = %q", DefaultRelay)
	}
}

func TestPaths(t *testing.T) {
	home := t.TempDir()
	if got := launchdPlistPath(home); filepath.Base(got) != launchdLabel+".plist" {
		t.Errorf("plist path = %q", got)
	}
	if got := systemdUnitPath(home); filepath.Base(got) != systemdUnitName {
		t.Errorf("unit path = %q", got)
	}
}

// TestLegacyServiceIdentifiersMatchPreRenameNames pins the earlier service
// identifiers cleanupLegacyService looks for, so an upgrading user's old daemon
// is actually found and torn down instead of silently orphaned. Also guards
// against a legacy unit colliding with the current one, which would make
// Install() tear down the service it just made.
func TestLegacyServiceIdentifiersMatchPreRenameNames(t *testing.T) {
	for _, unit := range legacySystemdUnits {
		if unit != "sqe-code-agent.service" {
			t.Errorf("unexpected legacy systemd unit %q", unit)
		}
		if unit == systemdUnitName {
			t.Errorf("legacy systemd unit %q must differ from the current systemdUnitName", unit)
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
