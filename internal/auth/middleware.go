package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "moony_session" // signed web session (aud=web)
	txCookie      = "moony_oidc_tx" // short-lived login transaction (state+PKCE+return)
	txSep         = "\x00"          // field separator inside the tx blob
)

// Wrap fronts the relay's HTTP handler with the auth gate. It serves the
// /auth/* endpoints and /connect itself, lets a fixed set of public paths
// through unauthenticated, and requires a valid session cookie for everything
// else (redirecting browsers to /auth/login, returning 401 to XHR/API callers).
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			a.handleLogin(w, r)
			return
		case "/auth/callback":
			a.handleCallback(w, r)
			return
		case "/auth/logout":
			a.handleLogout(w, r)
			return
		case "/auth/me":
			a.handleMe(w, r)
			return
		case "/connect":
			a.handleConnect(w, r)
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.sessionAccount(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		// Unauthenticated: send browsers to login, tell API callers 401.
		if wantsJSON(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/auth/login?return="+urlQueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

// isPublicPath reports whether a path is reachable without a session — the
// installer/download endpoints, static assets, health check, the relay version
// probe (hit by the CLI's unauthenticated /update check; carries no per-user
// data), and the auth routes needed to *reach* a login.
//
// The /auth/ routes are listed one by one on purpose. This used to be a prefix
// match, which made every route added under /auth/ public by default — including
// the ones the local-accounts work adds for enrolling a login key and changing a
// password, where "no session required" hands an account to anyone who asks.
// Adding a public route must be a deliberate edit here. The three /auth/ entries
// are belt-and-braces: Wrap's switch above dispatches them before this is
// consulted, so they matter only if that dispatch is ever refactored away.
func isPublicPath(p string) bool {
	switch p {
	case "/healthz", "/install.sh", "/favicon.ico", "/api/relay",
		"/auth/login", "/auth/callback", "/auth/logout":
		return true
	}
	return strings.HasPrefix(p, "/download") ||
		strings.HasPrefix(p, "/static/")
}

// wantsJSON reports whether the request is an XHR/API call (so we return 401
// instead of an HTML redirect the fetch() couldn't follow usefully).
func wantsJSON(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") != "" ||
		strings.Contains(r.Header.Get("Accept"), "application/json") ||
		strings.HasPrefix(r.URL.Path, "/api/")
}

// handleLogin starts the OIDC Authorization-Code + PKCE flow: it stashes the
// state, PKCE verifier, and post-login return path in a short-lived signed
// cookie, then redirects to the IdP.
func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	state := randToken()
	verifier := oauth2.GenerateVerifier()
	returnTo := sameHostRedirect(r.URL.Query().Get("return"))
	blob := a.signBlob(state + txSep + verifier + txSep + returnTo)
	http.SetCookie(w, &http.Cookie{
		Name: txCookie, Value: blob, Path: "/", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	url := a.oauth.AuthCodeURL(state, oidc.Nonce(state), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, url, http.StatusFound)
}

// handleCallback completes the flow: verify state (CSRF), exchange the code,
// verify the ID token + nonce, extract the account claim, and set the session.
func (a *Auth) handleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(txCookie)
	if err != nil {
		http.Error(w, "login expired — try again", http.StatusBadRequest)
		return
	}
	tx, ok := a.openBlob(c.Value)
	if !ok {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(tx, txSep, 3)
	if len(parts) != 3 {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	state, verifier, returnTo := parts[0], parts[1], parts[2]
	// Clear the tx cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: txCookie, Path: "/", MaxAge: -1})
	if r.URL.Query().Get("state") != state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tok, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in response", http.StatusBadGateway)
		return
	}
	idToken, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		http.Error(w, "id_token verification failed", http.StatusUnauthorized)
		return
	}
	if idToken.Nonce != state {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "cannot read claims", http.StatusUnauthorized)
		return
	}
	account := a.accountFromClaims(claims)
	if account == "" {
		http.Error(w, "no account claim in token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.Sign(account, audWeb, a.ttl), Path: "/",
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(a.ttl.Seconds()),
	})
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// handleLogout clears the session cookie.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleMe reports the signed-in account as JSON (the web UI polls this). 401
// when anonymous so the UI shows a signed-out state.
func (a *Auth) handleMe(w http.ResponseWriter, r *http.Request) {
	account, ok := a.sessionAccount(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accountname":  account,
		"display_name": account,
		"in_group":     true,
		"is_admin":     a.admins[strings.ToLower(account)],
	})
}

// handleConnect (authenticated) mints a per-user connector token and shows the
// command to link a machine to the relay under the signed-in account.
func (a *Auth) handleConnect(w http.ResponseWriter, r *http.Request) {
	account, ok := a.sessionAccount(r)
	if !ok {
		http.Redirect(w, r, "/auth/login?return=/connect", http.StatusFound)
		return
	}
	tunnel := strings.TrimSpace(os.Getenv("MN_RELAY_TUNNEL"))
	if tunnel == "" {
		tunnel = "<relay-host:port>"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = connectTmpl.Execute(w, struct{ Account, Token, Tunnel string }{
		Account: account,
		Token:   a.Sign(account, audConnector, a.connTTL),
		Tunnel:  tunnel,
	})
}

// sessionAccount returns the account bound to a valid session cookie.
func (a *Auth) sessionAccount(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return a.Verify(c.Value, audWeb)
}

// OwnerFromRequest resolves the web user's account for the relay's per-user
// isolation (wired via Relay.SetOwnerResolver). ok=false ⇒ anonymous.
func (a *Auth) OwnerFromRequest(r *http.Request) (string, bool) { return a.sessionAccount(r) }

// IsAdmin reports whether the request's account is in MN_AUTH_ADMINS.
func (a *Auth) IsAdmin(r *http.Request) bool {
	account, ok := a.sessionAccount(r)
	return ok && a.admins[strings.ToLower(account)]
}

// VerifyConnectorToken validates a connector's fHello token and returns the
// authenticated owner (wired via Relay.SetConnectorAuth).
func (a *Auth) VerifyConnectorToken(tok string) (owner string, ok bool) {
	return a.Verify(tok, audConnector)
}

// --- small helpers ---------------------------------------------------------

// signBlob/openBlob sign an arbitrary opaque string (used for the login tx
// cookie). Same construction as Sign but with a caller-supplied payload.
func (a *Auth) signBlob(s string) string {
	return b64.EncodeToString([]byte(s)) + "." + b64.EncodeToString(a.mac(s))
}

func (a *Auth) openBlob(v string) (string, bool) {
	dot := strings.IndexByte(v, '.')
	if dot <= 0 || dot == len(v)-1 {
		return "", false
	}
	payload, err := b64.DecodeString(v[:dot])
	if err != nil {
		return "", false
	}
	sig, err := b64.DecodeString(v[dot+1:])
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sig, a.mac(string(payload))) {
		return "", false
	}
	return string(payload), true
}

// randToken returns 32 bytes of URL-safe randomness for CSRF state / nonce.
func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// FromEnvBackground is FromEnv with a background context (convenience for main).
func FromEnvBackground() (*Auth, bool, error) { return FromEnv(context.Background()) }

var connectTmpl = template.Must(template.New("connect").Parse(`<!doctype html>
<meta charset="utf-8"><title>Connect a machine · moony</title>
<style>body{font:14px/1.5 ui-monospace,Menlo,monospace;max-width:44rem;margin:3rem auto;padding:0 1.25rem;background:#0d1117;color:#c9d1d9}
code,pre{background:#161b22;border:1px solid #30363d;border-radius:6px}pre{padding:.8rem;overflow:auto;white-space:pre-wrap;word-break:break-all}
h1{font-size:1.1rem}a{color:#58a6ff}</style>
<h1>Connect a machine to the relay</h1>
<p>Signed in as <b>{{.Account}}</b>. Run this on the machine you want to reach — it links under your account (only you will see it):</p>
<pre>export MN_RELAY_TOKEN={{.Token}}
moony --relay {{.Tunnel}}</pre>
<p>Or for the always-on background daemon, set the same <code>MN_RELAY_TOKEN</code> in its service environment. The token is bound to your account and expires; revisit this page for a fresh one.</p>
<p><a href="/">← back to moony</a></p>
`))
