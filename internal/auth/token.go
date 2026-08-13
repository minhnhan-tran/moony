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
	key, ok := a.tokenKey(account)
	if !ok {
		// No key for this account (a local id with no store, or one that has been
		// removed). Sign under the bare secret: Verify refuses local ids it cannot
		// resolve, so the result simply will not verify.
		key = a.secret
	}
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(macWith(key, tokenDomain+payload))
}

// tokenDomain and txDomain separate the two things this package MACs under the
// same secret. Without them the constructions are byte-identical, so an OIDC
// login-transaction blob — handed out by the public /auth/login route, and ending
// in a caller-supplied return path — parses and verifies as a session token, in
// either audience. That is an authentication bypass with no credential involved.
// See confusion_test.go.
const (
	tokenDomain = "moony-token\x00"
	txDomain    = "moony-oidc-tx\x00"
	// csrfDomain covers CSRF tokens, which are handed to the browser. Keeping them in
	// their own domain means a token the client legitimately holds can never equal a
	// signing key or a session MAC.
	csrfDomain = "moony-csrf\x00"
)

// tokenKey returns the HMAC key for account, and whether one exists.
//
// Local accounts sign under a key derived from the account and its epoch, so
// bumping the epoch invalidates every token already issued for it — the browser
// session AND the 30-day connector credential — without a revocation list. OIDC
// accounts keep the bare secret, so turning local accounts on does not log
// existing SSO users out.
func (a *Auth) tokenKey(account string) ([]byte, bool) {
	if !strings.HasPrefix(account, localPrefix) {
		return a.secret, true
	}
	if a.accounts == nil {
		return nil, false // a local id on a relay with no account store
	}
	acct, ok := a.accounts.ByID(account)
	if !ok || acct.Disabled {
		return nil, false // deleted or disabled: fail closed, immediately
	}
	// account cannot contain "|" (canonicalUsername forbids it), so this is
	// unambiguous.
	return macWith(a.secret, account+"|"+strconv.FormatUint(acct.Epoch, 10)), true
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
	// The account must be read out BEFORE the MAC can be checked, because a local
	// account's key depends on its epoch. Parsing bytes we have not yet
	// authenticated is safe — nothing is acted on, and the MAC below still decides
	// whether any of it is true.
	//
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
	// NUL separates fields in the login-transaction blob and in the relay's
	// linkKey, and cannot occur in a canonical local account or a real OIDC claim.
	// Refuse it rather than let it shape an account id.
	if strings.ContainsRune(acct, 0) {
		return "", false
	}
	key, ok := a.tokenKey(acct)
	if !ok {
		return "", false
	}
	if !hmac.Equal(sig, macWith(key, tokenDomain+p)) {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.now().Unix() >= exp {
		return "", false
	}
	return acct, true
}

// mac computes the HMAC-SHA256 of s under the configured secret.
func (a *Auth) mac(s string) []byte { return macWith(a.secret, s) }

func macWith(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
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
