package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Anything under /auth/ used to be public because the rule was a prefix match. That
// makes every future /auth/* route public by default — the wrong default for a gate
// whose job is to keep strangers away from a connector's shell.
func TestOnlyListedPathsArePublic(t *testing.T) {
	public := []string{
		"/healthz", "/install.sh", "/favicon.ico", "/api/relay",
		"/auth/login", "/auth/callback", "/auth/logout",
		"/static/app.css", "/download/moony_linux_amd64",
	}
	for _, p := range public {
		if !isPublicPath(p) {
			t.Errorf("%s should be reachable without a session", p)
		}
	}

	gated := []string{
		"/auth/keys", "/auth/keys/abc", "/auth/password", "/auth/logout-all",
		"/auth/anything-added-later",
		"/api/laptops", "/l/host/", "/",
	}
	for _, p := range gated {
		if isPublicPath(p) {
			t.Errorf("%s must require a session", p)
		}
	}
}

// The gate itself must refuse an unauthenticated request to a non-public /auth/
// route, not merely classify it correctly.
func TestGateRejectsUnauthenticatedAuthRoutes(t *testing.T) {
	a := testAuth(time.Now())
	reached := false
	h := a.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	for _, p := range []string{"/auth/keys", "/auth/password", "/auth/logout-all"} {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest") // ask for 401, not an HTML redirect
		h.ServeHTTP(rec, req)

		if reached {
			t.Errorf("%s reached the protected handler with no session", p)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", p, rec.Code)
		}
	}
}
