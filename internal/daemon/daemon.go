// Package daemon runs moony as an always-on, headless relay connector
// and manages it as a background OS service (launchd on macOS, systemd --user on
// Linux). With the daemon installed, a machine stays registered with the relay
// so its projects and sessions are browsable and drivable from the web UI
// (code.sqeqct.ops.apple.com) without anyone hand-launching the TUI.
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
	"os/user"
)

// dialTimeout bounds a single relay connection attempt; retryDelay paces
// reconnects after a failed dial or a dropped tunnel.
const (
	dialTimeout = 10 * time.Second
	retryDelay  = 3 * time.Second
)

// DefaultRelay is the production relay's public connector-tunnel address. The
// daemon falls back to it when no relay is otherwise configured (--relay /
// SQE_RELAY / saved /relay), so `moony daemon install` works with no
// arguments. Only the daemon uses this default; the interactive TUI never
// auto-connects to a relay.
const DefaultRelay = "code.sqeqct.ops.apple.com:8771"

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
// outbound connection to the relay (cfg.RelayAddr, i.e. SQE_RELAY / saved
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
	for {
		if ctx.Err() != nil {
			return ctx.Err()
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
		owner := ""
		if u, uerr := user.Current(); uerr == nil {
			owner = u.Username
		} else {
			logf("warning: local user unavailable (%v) — this laptop stays hidden in the web tree", uerr)
		}

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
		_ = relay.ServeConnector(conn, host, owner, config.Version, handler, func(string) {})
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
