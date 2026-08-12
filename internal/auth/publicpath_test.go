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

// The gate classifies r.URL.Path, but the routers behind it may resolve dot
// segments differently. Any path that normalises to a protected route must be
// treated as protected, or "/static/../api/laptops" is a free pass through the
// gate.
func TestTraversalCannotReachProtectedPathsThroughThePublicPrefix(t *testing.T) {
	for _, p := range []string{
		"/static/../api/laptops",
		"/download/../api/laptops",
		"/auth/../api/laptops",
		"/auth/login/../keys",
		"/static/./../l/host/",
	} {
		if isPublicPath(p) {
			t.Errorf("%s normalises into a protected path and must not be public", p)
		}
	}
	// Legitimate paths with no dot segments keep working.
	for _, p := range []string{"/static/app.css", "/static/fonts/material-symbols.woff2", "/download/SHA256SUMS"} {
		if !isPublicPath(p) {
			t.Errorf("%s should still be public", p)
		}
	}
}

// The flip side of the allowlist: an anonymous visitor must still be able to reach
// a login. Wrap dispatches these before consulting isPublicPath, so this guards
// against a future refactor that moves them behind the gate and locks everyone out.
func TestAnonymousCanStillReachTheLoginFlow(t *testing.T) {
	a := testAuth(time.Now())
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request should have been handled by the auth layer, not passed through")
	}))

	// /auth/login is excluded: it redirects through the configured OIDC provider,
	// which testAuth does not build. That it is dispatched rather than gated is
	// covered by TestOnlyListedPathsArePublic and Wrap's switch.
	for _, p := range []string{"/auth/logout", "/auth/me"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s: 404 — the route is no longer wired", p)
		}
		if rec.Code >= 500 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}

// Static assets and the health probe must stay reachable, or the login page cannot
// load its CSS and the container health check fails.
func TestAnonymousCanLoadStaticAssetsAndHealth(t *testing.T) {
	a := testAuth(time.Now())
	passed := 0
	h := a.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed++ }))

	for _, p := range []string{"/healthz", "/static/app.css", "/api/relay", "/favicon.ico"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
	}
	if passed != 4 {
		t.Errorf("%d of 4 public requests reached the handler; want all 4", passed)
	}
}
