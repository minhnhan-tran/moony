// Package auth implements optional per-user authentication for the relay:
// browser-facing OIDC login (Authorization Code + PKCE) and matching
// HMAC-signed, stateless tokens used both for the browser session cookie and
// for the connector (TUI/daemon) credential on the relay tunnel.
//
// Everything hinges on one shared secret (MN_AUTH_SECRET). Tokens carry the
// authenticated account, an expiry, and an audience ("web" vs "connector") so a
// browser cookie can never be replayed as a connector credential (or vice
// versa). Nothing is persisted — verification is a signature + expiry check.
//
// Auth is opt-in: with MN_AUTH unset/off the relay runs exactly as before
// (open, self-declared connector owners). See FromEnv.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Audiences for signed tokens. Verify rejects a token presented at the wrong
// audience, so a "web" session cookie can't authenticate a connector and a
// "connector" token can't stand in for a browser session.
const (
	audWeb       = "web"
	audConnector = "connector"
)

// b64 is URL-safe base64 without padding — cookie/token safe, no '=' or '/'.
var b64 = base64.RawURLEncoding

// Sign returns a stateless token binding account to aud, valid for ttl. Format:
//
//	base64url(account "|" expUnix "|" aud) "." base64url(HMAC-SHA256(payload))
//
// account must not contain "|" (an OIDC email/sub never does); it is left as-is.
func (a *Auth) Sign(account, aud string, ttl time.Duration) string {
	exp := a.now().Add(ttl).Unix()
	payload := account + "|" + strconv.FormatInt(exp, 10) + "|" + aud
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(a.mac(payload))
}

// Verify checks a token's signature, expiry, and audience, returning the bound
// account. A tampered, expired, malformed, or wrong-audience token yields
// ("", false). Constant-time signature comparison.
func (a *Auth) Verify(tok, wantAud string) (account string, ok bool) {
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 || dot == len(tok)-1 {
		return "", false
	}
	payloadB64, sigB64 := tok[:dot], tok[dot+1:]
	payload, err := b64.DecodeString(payloadB64)
	if err != nil {
		return "", false
	}
	sig, err := b64.DecodeString(sigB64)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sig, a.mac(string(payload))) {
		return "", false
	}
	// payload = account|exp|aud (account never contains "|"; split on the last two).
	p := string(payload)
	audCut := strings.LastIndexByte(p, '|')
	if audCut < 0 {
		return "", false
	}
	expCut := strings.LastIndexByte(p[:audCut], '|')
	if expCut < 0 {
		return "", false
	}
	acct, expStr, aud := p[:expCut], p[expCut+1:audCut], p[audCut+1:]
	if aud != wantAud || acct == "" {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.now().Unix() >= exp {
		return "", false
	}
	return acct, true
}

// mac computes the HMAC-SHA256 of s under the configured secret.
func (a *Auth) mac(s string) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// now returns the current time, overridable in tests via a.clock.
func (a *Auth) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}
