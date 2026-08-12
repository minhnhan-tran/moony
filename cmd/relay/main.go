// Command relay fronts the moony web UI for a remotely-running
// connector. It serves browser traffic on -addr and accepts a connector's
// outbound tunnel on -tunnel, proxying every browser request to the connector —
// which serves the real web app (and the user's LOCAL projects/sessions) from
// their machine. The relay holds no agent and no session state.
//
// It serves plain HTTP by default, or HTTPS when -tls-cert/-tls-key (or
// MN_RELAY_TLS_CERT/KEY) are given.
//
// Local prototype: run `make relay`, then `make connect` on the same machine,
// and open the relay's web address.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/announce"
	"github.com/pysty1995/moony/internal/auth"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/relay"
	"github.com/pysty1995/moony/internal/web"
)

func main() {
	var (
		httpAddr    = flag.String("addr", "127.0.0.1:8770", "address to serve the browser UI on")
		tunnelAddr  = flag.String("tunnel", "127.0.0.1:8771", "address connectors dial in on")
		tunnelPub   = flag.String("tunnel-public", "", "PUBLIC host:port connectors reach the tunnel at (also MN_RELAY_TUNNEL); advertised at /api/relay + baked into /install.sh for the auto-daemon")
		tlsCert     = flag.String("tls-cert", "", "PEM certificate file to serve HTTPS (also MN_RELAY_TLS_CERT)")
		tlsKey      = flag.String("tls-key", "", "PEM private key file to serve HTTPS (also MN_RELAY_TLS_KEY)")
		downloads   = flag.String("downloads", "", "directory of prebuilt CLI binaries to serve at /download + /install.sh")
		healthCheck = flag.String("healthcheck", "", "probe MODE: GET /healthz at this host:port and exit 0/1 (for container HEALTHCHECK / k8s exec probes)")
	)
	flag.Parse()

	// Probe mode, before any listener is opened. Exists because the runtime image is
	// distroless: it has no shell, curl or wget, so a container HEALTHCHECK has nothing
	// to run except this binary. Exits 0 when the relay answers /healthz, 1 otherwise.
	if addr := strings.TrimSpace(*healthCheck); addr != "" {
		os.Exit(probeHealth(addr))
	}

	config.LoadDotEnv() // local-dev .env (won't override real env / Whisper)

	// Public tunnel address connectors dial: flag wins, else env. Optional —
	// empty just leaves the installer's auto-daemon step off.
	tunnelPublic := *tunnelPub
	if tunnelPublic == "" {
		tunnelPublic = os.Getenv("MN_RELAY_TUNNEL")
	}

	// TLS material: flags win, else env (incl. .env). Both halves required.
	certFile, keyFile := *tlsCert, *tlsKey
	if certFile == "" {
		certFile = os.Getenv("MN_RELAY_TLS_CERT")
	}
	if keyFile == "" {
		keyFile = os.Getenv("MN_RELAY_TLS_KEY")
	}
	if (certFile == "") != (keyFile == "") {
		fatal("both -tls-cert and -tls-key (or MN_RELAY_TLS_CERT/KEY) are required for HTTPS")
	}
	useTLS := certFile != ""
	// The auth layer's cookie-Secure decision needs to know we serve HTTPS; it
	// cannot read this itself, because the flag form beats the env var above.
	auth.TLSHint(useTLS)

	r := relay.New()
	r.SetShell(web.SPAHandler()) // serve the SPA shell; per-laptop API is proxied to connectors
	r.SetVersion(config.Version) // exposed at GET /api/relay so the web UI can flag older terminals
	if *downloads != "" {
		r.SetDownloads(*downloads) // serve the macOS CLI at /download + /install.sh (public)
	}
	if tunnelPublic != "" {
		r.SetTunnelAddr(tunnelPublic) // advertise the dial address + auto-daemon in /install.sh
	}

	// Maintainer announcement banners: a shared update feed shown in every web
	// UI + TUI. Persist to MN_ANNOUNCEMENTS_FILE (a mounted volume) so it
	// survives relay restarts. With no built-in auth, anyone who can reach the
	// relay can author them — front it with your own access control to restrict.
	annStore := announce.OpenAt(os.Getenv("MN_ANNOUNCEMENTS_FILE"))

	// Optional per-user auth (OIDC for browsers + signed connector tokens). When
	// MN_AUTH is off/unset this is a no-op and the relay runs open, exactly as
	// before. When on, FromEnv performs OIDC discovery and fails closed on any
	// missing var so we never start half-secured.
	authGate, authOn, aerr := auth.FromEnvBackground()
	if aerr != nil {
		fatal("auth: %v", aerr)
	}
	if authOn {
		r.SetAnnouncements(annStore, web.AnnouncementsHandler(annStore), authGate.IsAdmin)
		r.SetOwnerResolver(authGate.OwnerFromRequest) // per-user connector isolation
		r.SetConnectorAuth(authGate.VerifyConnectorToken)
	} else {
		// No built-in auth: anyone who can reach the relay can author announcements
		// and see every connector. Front it with your own access control to restrict.
		r.SetAnnouncements(annStore, web.AnnouncementsHandler(annStore), func(*http.Request) bool { return true })
	}

	handler := http.Handler(r)
	if authOn {
		handler = authGate.Wrap(handler) // OIDC login gate over all browser routes
		fmt.Println("  auth:   OIDC per-user login enabled (MN_AUTH=on)")
	} else if host, _, splitErr := net.SplitHostPort(*httpAddr); splitErr != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		// Binding beyond loopback with no auth means anyone who can reach this
		// address gets full access to every connector's filesystem/agent — flag it
		// loudly instead of failing silently open.
		fmt.Println("  auth:   OFF — WARNING: -addr is not loopback-only; this relay is reachable over the network with NO authentication (set MN_AUTH=on to require login)")
	}

	tln, err := net.Listen("tcp", *tunnelAddr)
	if err != nil {
		fatal("listen for connectors on %s: %v", *tunnelAddr, err)
	}
	go acceptConnectors(tln, r)

	hln, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		fatal("listen for browsers on %s: %v", *httpAddr, err)
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	fmt.Printf("moony relay  v%s\n", config.Version)
	fmt.Printf("  web:    %s://%s\n", scheme, browsable(hln.Addr().String()))
	fmt.Printf("  tunnel: %s  (run the connector with -relay=%s)\n", *tunnelAddr, *tunnelAddr)
	if tunnelPublic != "" {
		fmt.Printf("  public tunnel: %s  (advertised; /install.sh auto-installs the daemon)\n", tunnelPublic)
	}
	if *downloads != "" {
		fmt.Printf("  dl:     serving CLI binaries from %s at /download + /install.sh (public)\n", *downloads)
	}
	fmt.Println("  waiting for a connector to dial in…")

	srv := &http.Server{
		Handler: handler,
		// Guard against slowloris-style trickle attacks: cap how long a client
		// gets to send headers/body. ReadTimeout is generous because a legitimate
		// request body can carry a multi-attachment upload (each up to ~32MB raw,
		// larger once base64-encoded — see internal/relay.maxRoundTripBodyBytes)
		// over a slow/mobile connection, which a tight deadline would abort.
		// WriteTimeout is deliberately left at 0 (unlimited) because the API
		// proxies long-lived SSE/streaming agent responses through this same
		// server — a fixed write deadline would cut those off mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	if useTLS {
		err = srv.ServeTLS(hln, certFile, keyFile)
	} else {
		err = srv.Serve(hln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("serve: %v", err)
	}
}

func acceptConnectors(ln net.Listener, r *relay.Relay) {
	// Mirrors the backoff net/http's own Serve loop uses: a transient accept
	// error (e.g. a momentary fd/resource exhaustion) must not take down the
	// whole relay — only a closed listener should end the loop.
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else {
				backoff *= 2
			}
			if max := time.Second; backoff > max {
				backoff = max
			}
			fmt.Fprintf(os.Stderr, "relay: connector accept: %v; retrying in %v\n", err, backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		go r.AddConnector(conn)
	}
}

func browsable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		return "localhost:" + port
	}
	return addr
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "relay: "+format+"\n", args...)
	os.Exit(1)
}

// probeHealth GETs /healthz on addr and returns a process exit code: 0 when the relay
// reports healthy, 1 otherwise. Used by the container HEALTHCHECK and by Kubernetes
// exec probes, since the distroless runtime image has no HTTP client of its own.
//
// Deliberately tiny and dependency-free: a health probe that can itself fail in
// interesting ways is worse than none.
func probeHealth(addr string) int {
	// Accept "8770", ":8770" and "host:port" so the flag is forgiving in a Dockerfile.
	if !strings.Contains(addr, ":") {
		addr = "127.0.0.1:" + addr
	} else if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		return 1
	}
	if strings.TrimSpace(string(body)) != "ok" {
		fmt.Fprintf(os.Stderr, "healthcheck: unexpected body %q\n", string(body))
		return 1
	}
	return 0
}
