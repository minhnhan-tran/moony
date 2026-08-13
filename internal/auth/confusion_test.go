package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The OIDC login-transaction cookie and a session token are produced by the same
// construction (b64(payload).b64(HMAC(secret,payload))) — so without domain
// separation, a tx blob IS a valid session token. The tx payload ends with a
// caller-supplied return path, which is enough to shape the "account|exp|aud"
// tail Verify parses.
//
// This is an authentication bypass on an OIDC-only relay: no credential, no
// identity provider round-trip, just a public GET /auth/login.
func TestLoginTransactionCookieIsNotASessionToken(t *testing.T) {
	a := testAuth(time.Now())

	// The tail an attacker wants Verify to see: | far-future expiry | audience.
	for _, aud := range []string{audWeb, audConnector} {
		t.Run(aud, func(t *testing.T) {
			// Build the blob exactly as handleLogin does, with a return path
			// carrying the separators.
			returnTo := "/x|4102444800|" + aud
			blob := a.signBlob("state123" + txSep + "verifier456" + txSep + returnTo)

			if acct, ok := a.Verify(blob, aud); ok {
				t.Errorf("a login-transaction blob verified as account %q in audience %q: "+
					"anyone can mint a session from the public /auth/login route", acct, aud)
			}
		})
	}
}

// The same confusion in the other direction: a session token must not open as a
// login transaction, or a stolen session could steer the OIDC callback.
func TestSessionTokenIsNotALoginTransaction(t *testing.T) {
	a := testAuth(time.Now())
	tok := a.Sign("alice@example.com", audWeb, time.Hour)
	if s, ok := a.openBlob(tok); ok {
		t.Errorf("a session token opened as a login transaction: %q", s)
	}
}

// sameHostRedirect feeds the tx blob, so it must not pass through the bytes that
// Verify treats as structure.
func TestSameHostRedirectRejectsTokenSeparators(t *testing.T) {
	for _, in := range []string{
		"/x|4102444800|web",
		"/x\x00y",
		"/path|with|pipes",
	} {
		got := sameHostRedirect(in)
		if strings.ContainsAny(got, "|\x00") {
			t.Errorf("sameHostRedirect(%q) = %q, which still carries token separators", in, got)
		}
	}
	// Ordinary paths must survive untouched.
	for _, in := range []string{"/", "/l/laptop/", "/?open=host&sess=abc"} {
		if got := sameHostRedirect(in); got != in {
			t.Errorf("sameHostRedirect(%q) = %q, want it unchanged", in, got)
		}
	}
}

// A NUL byte cannot appear in a canonical local account or a real OIDC claim, and
// it is the tx blob's separator — so Verify must refuse it outright rather than
// letting it shape an account id.
func TestVerifyRejectsNULInTheAccount(t *testing.T) {
	a := testAuth(time.Now())
	tok := a.Sign("alice\x00bob", audWeb, time.Hour)
	if acct, ok := a.Verify(tok, audWeb); ok {
		t.Errorf("an account containing NUL verified: %q", acct)
	}
}

// An identity provider must not be able to hand out an account id inside the
// namespace reserved for local accounts: that id resolves to the local user's
// store entry, so the SSO session would inherit their connectors — shell access
// on their machine.
func TestOIDCClaimCannotClaimTheLocalNamespace(t *testing.T) {
	a := testAuth(time.Now())
	a.claim = "email"
	for _, claimed := range []string{"local:alice", "LOCAL:alice", "  local:alice  "} {
		got := a.accountFromClaims(map[string]any{"email": claimed, "sub": "fallback-sub"})
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(got)), localPrefix) {
			t.Errorf("claim %q produced account %q, inside the local namespace", claimed, got)
		}
	}
}

// /auth/login is public, so the bypass above needs no credential at all. This
// drives the real route to prove the blob it sets cannot be replayed as a session.
func TestPublicLoginRouteCannotMintASession(t *testing.T) {
	a := testAuth(time.Now())
	a.oauth = nil // no IdP configured: handleLogin must not panic, and must not help

	req := httptest.NewRequest(http.MethodGet, "/auth/login?return=%2Fx%7C4102444800%7Cweb", nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Value == "" {
			continue
		}
		if acct, ok := a.Verify(c.Value, audWeb); ok {
			t.Errorf("cookie %s from the public login route verifies as %q", c.Name, acct)
		}
	}
}
