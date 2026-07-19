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
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/pysty1995/moony/internal/announce"
	"github.com/pysty1995/moony/internal/auth"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/relay"
	"github.com/pysty1995/moony/internal/web"
)

func main() {
	var (
		httpAddr   = flag.String("addr", "127.0.0.1:8770", "address to serve the browser UI on")
		tunnelAddr = flag.String("tunnel", "127.0.0.1:8771", "address connectors dial in on")
		tunnelPub  = flag.String("tunnel-public", "", "PUBLIC host:port connectors reach the tunnel at (also MN_RELAY_TUNNEL); advertised at /api/relay + baked into /install.sh for the auto-daemon")
		tlsCert    = flag.String("tls-cert", "", "PEM certificate file to serve HTTPS (also MN_RELAY_TLS_CERT)")
		tlsKey     = flag.String("tls-key", "", "PEM private key file to serve HTTPS (also MN_RELAY_TLS_KEY)")
		downloads  = flag.String("downloads", "", "directory of prebuilt CLI binaries to serve at /download + /install.sh")
	)
	flag.Parse()

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

	if useTLS {
		err = http.ServeTLS(hln, handler, certFile, keyFile)
	} else {
		err = http.Serve(hln, handler)
	}
	if err != nil {
		fatal("serve: %v", err)
	}
}

func acceptConnectors(ln net.Listener, r *relay.Relay) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			fatal("connector accept: %v", err)
		}
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
