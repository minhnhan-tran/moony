package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// probeHealth is what the container HEALTHCHECK and any exec probe run, because the
// distroless runtime image has no shell, curl or wget. Its exit code is the entire
// contract: 0 means "the relay is serving", anything else restarts the container.
//
// A probe that reports healthy when the relay is broken is worse than no probe at all —
// it hides outages. A probe that reports unhealthy when the relay is fine causes restart
// loops. Both directions are covered here.
func TestProbeHealth(t *testing.T) {
	t.Run("healthy relay exits 0", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("probed %q, want /healthz", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()
		if got := probeHealth(strings.TrimPrefix(srv.URL, "http://")); got != 0 {
			t.Errorf("exit = %d, want 0", got)
		}
	})

	t.Run("trailing newline is tolerated", func(t *testing.T) {
		// Don't fail a healthy relay over whitespace — that would be a restart loop
		// caused by a formatting detail.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok\n"))
		}))
		defer srv.Close()
		if got := probeHealth(strings.TrimPrefix(srv.URL, "http://")); got != 0 {
			t.Errorf("exit = %d, want 0", got)
		}
	})

	t.Run("non-200 exits 1", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		if got := probeHealth(strings.TrimPrefix(srv.URL, "http://")); got != 1 {
			t.Errorf("exit = %d, want 1", got)
		}
	})

	t.Run("200 with the wrong body exits 1", func(t *testing.T) {
		// A proxy or captive portal answering 200 with an HTML error page must not read
		// as healthy.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>gateway error</html>"))
		}))
		defer srv.Close()
		if got := probeHealth(strings.TrimPrefix(srv.URL, "http://")); got != 1 {
			t.Errorf("exit = %d, want 1", got)
		}
	})

	t.Run("nothing listening exits 1", func(t *testing.T) {
		// Port 1 is privileged and unused; a connection here always fails.
		if got := probeHealth("127.0.0.1:1"); got != 1 {
			t.Errorf("exit = %d, want 1", got)
		}
	})
}

// The flag is written by hand in a Dockerfile and a compose file, so accept the obvious
// shorthands rather than silently probing the wrong host.
func TestProbeHealthAddressForms(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	for _, addr := range []string{
		"127.0.0.1:" + port, // full host:port
		":" + port,          // port only, colon-prefixed
		port,                // bare port
	} {
		if got := probeHealth(addr); got != 0 {
			t.Errorf("probeHealth(%q) = %d, want 0", addr, got)
		}
	}
	if hits != 3 {
		t.Errorf("server saw %d probes, want 3 — an address form resolved elsewhere", hits)
	}
}
