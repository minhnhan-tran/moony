// Package daemon runs moony as an always-on, headless relay connector
// and manages it as a background OS service (launchd on macOS, systemd --user on
// Linux). With the daemon installed, a machine stays registered with the relay
// so its projects and sessions are browsable and drivable from the web UI
// (the configured relay's host) without anyone hand-launching the TUI.
//
// The daemon serves the same multi-project app the terminal serves over a relay
// (see app.BuildServer) — every browser-opened session runs its own agent rooted
// at the session's working dir and persists to disk. It has no TUI and no live
// in-terminal mirror; it's purely the connector half.
package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/app"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/relay"
	"github.com/pysty1995/moony/internal/selfupdate"
	"os/user"
)

// dialTimeout bounds a single relay connection attempt; retryDelay paces
// reconnects after a failed dial or a dropped tunnel.
const (
	dialTimeout = 10 * time.Second
	retryDelay  = 3 * time.Second
)

// DefaultRelay is a placeholder relay connector-tunnel address. It is NOT a real
// host — deployments must override it via --relay / MN_RELAY / a saved
// /relay so `moony daemon install` targets their own relay. The daemon falls
// back to it only when no relay is otherwise configured; the interactive TUI
// never auto-connects to a relay.
const DefaultRelay = "relay.example.com:8771"

// relayAddrFor resolves the relay the daemon should dial: the configured address
// if set, else DefaultRelay.
func relayAddrFor(cfg *config.Config) string {
	if cfg != nil {
		if a := strings.TrimSpace(cfg.RelayAddr); a != "" {
			return a
		}
	}
	return DefaultRelay
}

// Serve runs the relay connector headlessly until ctx is canceled. It builds the
// full multi-project app server for this machine and keeps a best-effort
// outbound connection to the relay (cfg.RelayAddr, i.e. MN_RELAY / saved
// /relay; DefaultRelay when unset), auto-reconnecting on drops. logf receives
// human-readable status lines (the service redirects them to a log file / journal).
func Serve(ctx context.Context, cfg *config.Config, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	addr := relayAddrFor(cfg)

	srv, _, err := app.BuildServer(cfg)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	// A browser /update on a daemon-hosted session self-updates the binary in
	// place; relaunch on the new build by exiting so the service manager
	// (launchd KeepAlive / systemd Restart=always) restarts us. The brief delay
	// lets the "restarting…" line reach the browser before the tunnel drops.
	srv.SetUpdateRestart(func() {
		logf("self-update applied — exiting so the service manager relaunches the new build")
		go func() {
			time.Sleep(2 * time.Second)
			os.Exit(0)
		}()
	})
	handler := srv.Handler()
	host := config.MachineName()

	logf("moony daemon v%s — serving %q to relay %s", config.Version, host, addr)

	// Drive time-based schedules alongside the relay connector. The scheduler
	// stops when ctx is canceled (i.e. when the daemon shuts down).
	go runScheduler(ctx, logf)

	standby := false
	var lastUpdateCheck time.Time // zero => check on the first iteration
	updateDeferred := false       // true while an update is held back by a running turn (logged once)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Keep an unattended daemon current. Gated on AutoUpdate so a machine can be
		// pinned to its current build, and skipped entirely while any session is
		// mid-turn: the relaunch below drops in-flight work, and a daemon has nobody
		// watching to notice or resend. A turn is minutes at worst; losing one costs the
		// user real work.
		if cfg.AutoUpdate && time.Since(lastUpdateCheck) >= updateCheckInterval {
			if n, name := srv.ActiveTurns(); n > 0 {
				// Log once, not every loop, or a long turn floods the log.
				if !updateDeferred {
					logf("update check deferred — %d session(s) mid-turn (e.g. %q)", n, name)
					updateDeferred = true
				}
			} else {
				updateDeferred = false
				lastUpdateCheck = time.Now()
				checkAndSelfUpdate(ctx, logf, srv.ActiveTurns)
			}
		}
		// Yield the connector to a hand-run TUI while one is active (the relay
		// keeps only one connector per machine). Re-check on a timer so we
		// reconnect once the TUI exits.
		if connectorHeldByOther() {
			if !standby {
				logf("a local moony (TUI) holds the relay connector — standing by")
				standby = true
			}
			if !wait(ctx, retryDelay) {
				return ctx.Err()
			}
			continue
		}
		if standby {
			logf("local TUI released the connector — reconnecting")
			standby = false
		}
		// Owner = the local OS user running this daemon. The relay shows this
		// laptop only to that user (per-user isolation); an empty owner hides it.
		// When the relay has auth enabled, MN_RELAY_TOKEN (a per-user connector
		// token from the relay's /connect page) is the trusted credential and the
		// relay derives the owner from it, ignoring this OS username.
		owner := ""
		if u, uerr := user.Current(); uerr == nil {
			owner = u.Username
		} else {
			logf("warning: local user unavailable (%v) — this laptop stays hidden in the web tree", uerr)
		}
		token := strings.TrimSpace(os.Getenv("MN_RELAY_TOKEN"))

		conn, derr := dialRelay(ctx, addr)
		if derr != nil {
			logf("relay dial %s failed: %v (retrying in %s)", addr, derr, retryDelay)
			if !wait(ctx, retryDelay) {
				return ctx.Err()
			}
			continue
		}
		logf("connected to relay %s as %q", addr, owner)
		// Blocks until the tunnel drops or the relay closes it.
		_ = relay.ServeConnector(conn, host, owner, token, config.Version, handler, func(string) {})
		logf("relay connection closed — reconnecting in %s", retryDelay)
		if !wait(ctx, retryDelay) {
			return ctx.Err()
		}
	}
}

func dialRelay(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// wait sleeps for d, returning false if ctx is canceled first.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// updateCheckInterval is how often an idle daemon looks for a newer build. Long enough
// that the check is invisible next to normal work, short enough that a machine picks up
// a fix the same day.
const updateCheckInterval = 30 * time.Minute

// checkAndSelfUpdate downloads and installs a newer build, then relaunches onto it.
//
// activeTurns is re-consulted AFTER the download: Apply can run for minutes, and
// relaunching would kill a turn that started meanwhile with no chance to persist its
// tail. The new binary is already on disk at that point, so the next idle iteration
// picks it up for free — deferring costs nothing.
func checkAndSelfUpdate(ctx context.Context, logf func(string, ...any), activeTurns func() (int, string)) {
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	latest, err := selfupdate.LatestVersion(vctx)
	cancel()
	if err != nil {
		logf("update check failed: %v", err)
		return
	}
	if !config.VersionLess(config.Version, latest) {
		return // already current or ahead
	}
	logf("v%s is available (running v%s) — self-updating", latest, config.Version)

	actx, acancel := context.WithTimeout(ctx, 10*time.Minute)
	_, err = selfupdate.Apply(actx)
	acancel()
	if err != nil {
		logf("self-update failed: %v — staying on v%s", err, config.Version)
		return
	}
	if activeTurns != nil {
		if n, name := activeTurns(); n > 0 {
			logf("v%s installed but relaunch deferred — %d session(s) started running during the download (e.g. %q)", latest, n, name)
			return
		}
	}
	// Exit and let the service manager (launchd KeepAlive / systemd Restart=always)
	// start the new build, matching the /update path above. Preferred over an in-process
	// exec: it works the same on every platform and leaves process supervision to the
	// thing that owns it.
	logf("self-update to v%s installed — exiting so the service manager relaunches", latest)
	os.Exit(0)
}
