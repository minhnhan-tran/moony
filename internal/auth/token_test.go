package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testAuth builds an Auth with a fixed clock for deterministic expiry tests.
func testAuth(now time.Time) *Auth {
	return &Auth{
		secret: []byte("test-secret-at-least-16-bytes-long"),
		ttl:    time.Hour,
		clock:  func() time.Time { return now },
		admins: map[string]bool{"admin@example.com": true},
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	tok := a.Sign("alice@example.com", audWeb, time.Hour)
	got, ok := a.Verify(tok, audWeb)
	if !ok || got != "alice@example.com" {
		t.Fatalf("Verify = (%q, %v), want alice@example.com, true", got, ok)
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	web := a.Sign("alice@example.com", audWeb, time.Hour)
	// A web session token must NOT pass as a connector token, and vice-versa.
	if _, ok := a.Verify(web, audConnector); ok {
		t.Error("web token accepted as connector audience")
	}
	conn := a.Sign("alice@example.com", audConnector, time.Hour)
	if _, ok := a.Verify(conn, audWeb); ok {
		t.Error("connector token accepted as web audience")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	tok := a.Sign("alice@example.com", audWeb, time.Minute)
	a.clock = func() time.Time { return time.Unix(1_000_000+61, 0) } // 61s later
	if _, ok := a.Verify(tok, audWeb); ok {
		t.Error("expired token accepted")
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	tok := a.Sign("alice@example.com", audWeb, time.Hour)
	// Flip the last char of the payload (before the dot).
	b := []byte(tok)
	for i := 0; i < len(b); i++ {
		if b[i] == '.' {
			if i > 0 {
				b[i-1] ^= 0x01
			}
			break
		}
	}
	if _, ok := a.Verify(string(b), audWeb); ok {
		t.Error("tampered token accepted")
	}
	// A token signed under a different secret must fail.
	other := testAuth(time.Unix(1_000_000, 0))
	other.secret = []byte("a-completely-different-secret-key")
	if _, ok := a.Verify(other.Sign("alice@example.com", audWeb, time.Hour), audWeb); ok {
		t.Error("token signed under a different secret accepted")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	for _, bad := range []string{"", ".", "abc", "abc.", ".abc", "not-base64!.also-not"} {
		if _, ok := a.Verify(bad, audWeb); ok {
			t.Errorf("malformed token %q accepted", bad)
		}
	}
}

func TestSignBlobRoundTrip(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	blob := a.signBlob("state\x00verifier\x00/return/path")
	got, ok := a.openBlob(blob)
	if !ok || got != "state\x00verifier\x00/return/path" {
		t.Fatalf("openBlob = (%q, %v)", got, ok)
	}
	if _, ok := a.openBlob(blob + "x"); ok {
		t.Error("tampered blob accepted")
	}
}

func TestOwnerAndAdminFromCookie(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	// Anonymous: no cookie.
	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := a.OwnerFromRequest(r); ok {
		t.Error("anonymous request resolved an owner")
	}
	if a.IsAdmin(r) {
		t.Error("anonymous request is admin")
	}
	// Non-admin session.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.Sign("alice@example.com", audWeb, time.Hour)})
	if o, ok := a.OwnerFromRequest(r2); !ok || o != "alice@example.com" {
		t.Errorf("owner = (%q,%v)", o, ok)
	}
	if a.IsAdmin(r2) {
		t.Error("alice should not be admin")
	}
	// Admin session.
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.Sign("admin@example.com", audWeb, time.Hour)})
	if !a.IsAdmin(r3) {
		t.Error("admin@example.com should be admin")
	}
}

func TestVerifyConnectorToken(t *testing.T) {
	a := testAuth(time.Unix(1_000_000, 0))
	tok := a.Sign("bob@example.com", audConnector, time.Hour)
	if o, ok := a.VerifyConnectorToken(tok); !ok || o != "bob@example.com" {
		t.Errorf("VerifyConnectorToken = (%q,%v)", o, ok)
	}
	// A web-audience token must not authenticate a connector.
	if _, ok := a.VerifyConnectorToken(a.Sign("bob@example.com", audWeb, time.Hour)); ok {
		t.Error("web token authenticated a connector")
	}
}
