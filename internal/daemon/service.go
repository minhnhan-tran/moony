package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Service identifiers. launchdLabel is the user LaunchAgent label (and plist
// basename); systemdUnitName is the systemd --user unit.
const (
	launchdLabel    = "moony.coding-agent"
	systemdUnitName = "moony.service"
)

// legacyLaunchdLabels/legacySystemdUnits are service identifiers used by earlier
// builds, before the move to the moony.* names. Install() best-effort tears any
// of them down if present so upgrading the binary doesn't leave an orphaned
// daemon running under an old name alongside the new one. These are teardown
// anchors that must match what older installs actually registered as, not
// branding.
var (
	legacyLaunchdLabels = []string{"sqeqct.coding-agent", "sqeqct.sqe-code-agent"}
	legacySystemdUnits  = []string{"sqe-code-agent.service"}
)

// Install writes the OS service unit (launchd plist / systemd --user unit) that
// runs `moony daemon run` with the given relay address, and starts it.
// relay must be a host:port the connector dials out to.
func Install(relay string) error {
	relay = strings.TrimSpace(relay)
	if relay == "" {
		return fmt.Errorf("a relay address is required: moony daemon install --relay host:port")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cleanupLegacyService(home) // best-effort; ignore errors, there may be nothing to clean up

	switch runtime.GOOS {
	case "darwin":
		err = installDarwin(exe, relay, home)
	case "linux":
		err = installLinux(exe, relay, home)
	default:
		return fmt.Errorf("daemon service install is supported on macOS and Linux, not %s — run `moony daemon run` under your own supervisor", runtime.GOOS)
	}
	if err != nil {
		return err
	}
	return nil
}

// Uninstall stops the service and removes its unit file.
func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		path := launchdPlistPath(home)
		_, _ = runCmd("launchctl", "bootout", guiDomain()+"/"+launchdLabel)
		_, _ = runCmd("launchctl", "unload", "-w", path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case "linux":
		_, _ = runCmd("systemctl", "--user", "disable", "--now", systemdUnitName)
		if err := os.Remove(systemdUnitPath(home)); err != nil && !os.IsNotExist(err) {
			return err
		}
		_, _ = runCmd("systemctl", "--user", "daemon-reload")
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Status returns a human-readable status line for the service.
func Status() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		if !fileExists(launchdPlistPath(home)) {
			return "not installed (run: moony daemon install --relay host:port)", nil
		}
		out, err := runCmd("launchctl", "print", guiDomain()+"/"+launchdLabel)
		if err != nil {
			return "installed but not loaded — try: moony daemon start", nil
		}
		return "installed; " + launchdState(out), nil
	case "linux":
		if !fileExists(systemdUnitPath(home)) {
			return "not installed (run: moony daemon install --relay host:port)", nil
		}
		active, _ := runCmd("systemctl", "--user", "is-active", systemdUnitName)
		enabled, _ := runCmd("systemctl", "--user", "is-enabled", systemdUnitName)
		return fmt.Sprintf("installed; active=%s enabled=%s", strings.TrimSpace(active), strings.TrimSpace(enabled)), nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Start (re)loads/starts an already-installed service.
func Start() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		path := launchdPlistPath(home)
		if !fileExists(path) {
			return fmt.Errorf("not installed — run: moony daemon install --relay host:port")
		}
		return reloadLaunchd(path)
	case "linux":
		if !fileExists(systemdUnitPath(home)) {
			return fmt.Errorf("not installed — run: moony daemon install --relay host:port")
		}
		if out, err := runCmd("systemctl", "--user", "start", systemdUnitName); err != nil {
			return fmt.Errorf("systemctl start: %w: %s", err, out)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Stop stops the service without removing its unit file.
func Stop() error {
	switch runtime.GOOS {
	case "darwin":
		if out, err := runCmd("launchctl", "bootout", guiDomain()+"/"+launchdLabel); err != nil {
			return fmt.Errorf("launchctl bootout: %w: %s", err, out)
		}
		return nil
	case "linux":
		if out, err := runCmd("systemctl", "--user", "stop", systemdUnitName); err != nil {
			return fmt.Errorf("systemctl stop: %w: %s", err, out)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// --- macOS (launchd) ---------------------------------------------------------

func installDarwin(exe, relay, home string) error {
	logPath := filepath.Join(home, "Library", "Logs", "moony", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := launchdPlistPath(home)
	plist := renderLaunchd(launchdParams{
		Label: launchdLabel, Exe: exe, Relay: relay,
		Log: logPath, Home: home, Path: servicePATH(exe),
	})
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	return reloadLaunchd(path)
}

// reloadLaunchd (re)loads the plist and guarantees the daemon ends up running —
// including the update/reinstall case where a previous instance is already
// loaded. That case is the tricky one: `launchctl bootout` returns BEFORE
// launchd has finished tearing the old (KeepAlive) job down, so an immediate
// `bootstrap` races the teardown and fails with error 5 ("Input/output error").
// The old code booted out then bootstrapped right away, so on reinstall the old
// daemon was gone and the new one never started — leaving the machine with no
// relay connector at all. To avoid the race: boot out any existing job, wait for
// launchd to actually forget the label, then bootstrap with a short retry loop,
// and kickstart to be sure it's up.
func reloadLaunchd(path string) error {
	target := guiDomain() + "/" + launchdLabel

	// If an instance is already loaded, boot it out and wait for launchd to drop
	// the label before re-bootstrapping (otherwise bootstrap races the teardown).
	if _, err := runCmd("launchctl", "print", target); err == nil {
		_, _ = runCmd("launchctl", "bootout", target)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := runCmd("launchctl", "print", target); err != nil {
				break // launchd has forgotten the label — safe to bootstrap
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Bootstrap, retrying the transient errors that follow a fresh bootout.
	var lastOut string
	var lastErr error
	for attempt := 0; attempt < 15; attempt++ {
		out, err := runCmd("launchctl", "bootstrap", guiDomain(), path)
		if err == nil {
			_, _ = runCmd("launchctl", "kickstart", target) // ensure it's running now
			return nil
		}
		// Already registered (a half-finished prior load): just (re)start it so
		// the new binary is picked up.
		if strings.Contains(out, "already bootstrapped") || strings.Contains(out, "service already loaded") {
			_, _ = runCmd("launchctl", "kickstart", "-k", target)
			return nil
		}
		lastOut, lastErr = out, err
		time.Sleep(200 * time.Millisecond)
	}

	// Legacy launchd without `bootstrap`: fall back to `load -w`.
	if out2, err2 := runCmd("launchctl", "load", "-w", path); err2 != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s; load -w: %v: %s",
			lastErr, strings.TrimSpace(lastOut), err2, strings.TrimSpace(out2))
	}
	_, _ = runCmd("launchctl", "kickstart", target)
	return nil
}

func launchdPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func guiDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// cleanupLegacyService best-effort removes daemons installed under any earlier
// service name, ignoring all errors — there may be nothing to clean up, or
// insufficient permissions, either way Install() should proceed with the current
// name regardless.
func cleanupLegacyService(home string) {
	switch runtime.GOOS {
	case "darwin":
		for _, label := range legacyLaunchdLabels {
			_, _ = runCmd("launchctl", "bootout", guiDomain()+"/"+label)
			_ = os.Remove(filepath.Join(home, "Library", "LaunchAgents", label+".plist"))
		}
	case "linux":
		for _, unit := range legacySystemdUnits {
			_, _ = runCmd("systemctl", "--user", "disable", "--now", unit)
			_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", unit))
		}
	}
}

// launchdState pulls a short "state = …" line out of `launchctl print` output.
func launchdState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "state =") {
			return s
		}
	}
	return "loaded"
}

type launchdParams struct{ Label, Exe, Relay, Log, Home, Path string }

func renderLaunchd(p launchdParams) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(p.Label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(p.Exe) + `</string>
		<string>daemon</string>
		<string>run</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>MOONY_RELAY</key>
		<string>` + xmlEscape(p.Relay) + `</string>
		<key>PATH</key>
		<string>` + xmlEscape(p.Path) + `</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>WorkingDirectory</key>
	<string>` + xmlEscape(p.Home) + `</string>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(p.Log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(p.Log) + `</string>
</dict>
</plist>
`
}

// --- Linux (systemd --user) --------------------------------------------------

func installLinux(exe, relay, home string) error {
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unit := renderSystemd(systemdParams{Exe: exe, Relay: relay, Path: servicePATH(exe)})
	if err := os.WriteFile(systemdUnitPath(home), []byte(unit), 0o644); err != nil {
		return err
	}
	if out, err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}
	if out, err := runCmd("systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return fmt.Errorf("systemctl enable --now: %w: %s", err, out)
	}
	return nil
}

func systemdUnitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
}

type systemdParams struct{ Exe, Relay, Path string }

func renderSystemd(p systemdParams) string {
	return `[Unit]
Description=moony relay connector (drive this machine from the web UI)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + p.Exe + ` daemon run
Environment=MOONY_RELAY=` + p.Relay + `
Environment=PATH=` + p.Path + `
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`
}

// --- shared helpers ----------------------------------------------------------

// servicePATH builds a PATH for the service: the executable's dir and the usual
// user/tool dirs prepended to the install-time PATH, since launchd/systemd start
// with a sparse environment and the agent shells out to git/npx/claude/etc.
func servicePATH(exe string) string {
	home, _ := os.UserHomeDir()
	parts := []string{
		filepath.Dir(exe),
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	if cur := os.Getenv("PATH"); cur != "" {
		parts = append(parts, cur)
	}
	parts = append(parts, "/usr/bin", "/bin", "/usr/sbin", "/sbin")

	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		for _, seg := range strings.Split(p, ":") {
			if seg == "" || seen[seg] {
				continue
			}
			seen[seg] = true
			out = append(out, seg)
		}
	}
	return strings.Join(out, ":")
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
