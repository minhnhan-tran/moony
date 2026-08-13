package auth

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Keyfile login: a public /auth/challenge mints a signed single-use blob,
// /auth/login-keyfile takes that blob plus an ECDSA signature over it and issues a
// session. Enrolment (/auth/keys) and revocation (/auth/keys/revoke) require an
// existing session, so a keyfile is always added by someone already signed in.
//
// Two rules shape this file:
//   - The account being authenticated is read from the SIGNED challenge only,
//     never from a request field. A caller cannot sign a challenge for alice and
//     then ask to be logged in as bob.
//   - The account being modified is read from the SESSION only. A signed-in user
//     cannot enrol a key onto, or revoke a key from, anyone else's account.
const (
	// maxChallengeMints bounds how many challenges one address may take per window.
	// Minting is cheap (rand + HMAC) so this is generous; its purpose is to stop a
	// single client filling the single-use set.
	maxChallengeMints = 120
	// maxKeyfileBody caps the login/enrol JSON we read. A challenge plus a 64-byte
	// signature plus an SPKI is a few hundred bytes.
	maxKeyfileBody = 8 << 10
)

// keyfileJS is served from this package rather than internal/web/static because
// the login page needs it *before* there is a session, and because the relay
// embeds its own copy of the static tree: shipping the script here means one
// binary cannot serve a stale version of it.
//
//go:embed keyfile.js
var keyfileJS []byte

// keyfileJSETag is the script's content hash, computed once, so browsers can
// revalidate cheaply and a redeploy never serves a cached mismatch.
var keyfileJSETag = func() string {
	sum := sha256.Sum256(keyfileJS)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`
}()

// handleKeyfileScript serves the browser half of keyfile login. Public: it is
// static code, contains no secrets, and the login page cannot work without it.
func (a *Auth) handleKeyfileScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("ETag", keyfileJSETag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match == keyfileJSETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(keyfileJS)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes a bounded JSON body. Unknown fields are rejected so a typo in
// the client ("signature2") fails loudly instead of silently authenticating with a
// zero value.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKeyfileBody))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// sameOriginPost reports whether a state-changing request came from our own pages.
//
// Sec-Fetch-Site is preferred (browsers set it and script cannot forge it);
// Origin is the fallback for older browsers. A request with neither is refused:
// that is the shape of a cross-site <form> post from a browser that sends no
// metadata, and legitimate callers (our own JS, or curl) can always send Origin.
func sameOriginPost(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// checkAPICSRF validates the CSRF token on an authenticated JSON request.
//
// The seed is the SESSION cookie, matching every other authenticated form in this
// package (/auth/password, /auth/logout-all) and what the account page renders.
// Seeding from the per-visit moony_csrf cookie instead would be both broken (the
// account page never issues that cookie) and weaker: a cookie is not bound to the
// authenticated identity, and cookies are not origin-isolated, so an attacker able
// to plant one from a sibling subdomain would know the token.
//
// Header only, no form-field fallback: this is a JSON API, and reading the form
// would consume the body we are about to decode.
func (a *Auth) checkAPICSRF(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return a.csrfValid(c.Value, r.Header.Get("X-CSRF-Token"))
}

// handleChallenge mints a login challenge. Public and unauthenticated.
//
// It answers identically for a real account, an unknown one and a malformed name:
// same status, same shape, same cost. Anything else makes this endpoint a username
// oracle for a relay whose whole point is to be exposed to the internet.
func (a *Auth) handleChallenge(w http.ResponseWriter, r *http.Request) {
	if !a.LocalEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	mintKey := "chal:ip:" + ip
	if a.throttler().blocked(mintKey, "ip:"+ip) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
		return
	}
	// Every mint counts, successful or not: this bucket meters use, not failure.
	a.throttler().fail(mintKey)

	// An unusable name still gets a challenge, bound to an id that cannot exist:
	// canonical ids never contain a space, and the store refuses to load one that
	// does. It must not contain NUL either — that is this blob's field separator, so
	// a NUL would make the challenge malformed and give a different error on login
	// than an unknown-but-well-formed name does.
	account := "local: invalid"
	if id, err := AccountID(r.FormValue("username")); err == nil {
		account = id
	}
	blob, err := a.mintChallenge(account)
	if err != nil {
		http.Error(w, "could not create a challenge", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge":  blob,
		"expires_in": int(challengeTTL.Seconds()),
	})
}

// handleKeyfileLogin completes a challenge/response login.
func (a *Auth) handleKeyfileLogin(w http.ResponseWriter, r *http.Request) {
	if !a.LocalEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Challenge string `json:"challenge"`
		Signature string `json:"signature"`
		Return    string `json:"return"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}

	ip := clientIP(r)
	ipKey := "ip:" + ip
	if a.throttler().blocked(ipKey) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}

	// Redeeming burns the nonce whatever happens next, so a captured challenge
	// cannot be retried with a different signature.
	account, ok := a.redeemChallenge(body.Challenge)
	if !ok {
		a.throttler().fail(ipKey)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired challenge"})
		return
	}
	// Deliberately NOT the shared per-account bucket: /auth/challenge mints a
	// challenge for any name, so counting keyfile failures against that bucket would
	// let an unauthenticated stranger lock a known username out of PASSWORD login
	// with a handful of garbage signatures. Keyfile failures get their own, looser
	// bucket; brute force is bounded by the signature itself, not by this counter.
	acctKey := "kf:" + account
	if a.throttler().blocked(acctKey) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}

	if !a.keyfileSignatureOK(account, body.Challenge, body.Signature) {
		a.throttler().fail(ipKey, acctKey)
		// One message for "no such account", "disabled", "no keys enrolled" and
		// "bad signature" alike.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "key not accepted"})
		return
	}
	a.throttler().clear(ipKey, acctKey)
	a.setSession(w, account)
	writeJSON(w, http.StatusOK, map[string]string{
		"ok":     "1",
		"return": sameHostRedirect(body.Return),
	})
}

// keyfileSignatureOK reports whether the signature matches any key enrolled on a
// usable account.
func (a *Auth) keyfileSignatureOK(account, challenge, signature string) bool {
	var candidates []PubKey
	if a.accounts != nil && strings.HasPrefix(account, localPrefix) {
		if acct, found := a.accounts.ByID(account); found && !acct.Disabled {
			candidates = acct.PubKeys
		}
	}
	// An account that does not exist, is disabled, or has no keys still pays for one
	// verification. Without this, response time would tell an attacker which of
	// those it was — undoing the care taken to keep /auth/challenge from being a
	// username oracle.
	if len(candidates) == 0 {
		verifyKeyfileSignature(dummyPubKeySPKI, challenge, signature)
		return false
	}
	for _, k := range candidates {
		if k.Alg != keyAlgP256 {
			continue
		}
		if verifyKeyfileSignature(k.Key, challenge, signature) {
			return true
		}
	}
	return false
}

// handleKeys lists (GET) or enrols (POST) keyfile credentials for the signed-in
// account. Gated: reached only through Wrap's authenticated path.
func (a *Auth) handleKeys(w http.ResponseWriter, r *http.Request) {
	account, ok := a.localSessionAccount(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		acct, found := a.accounts.ByID(account)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such account"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": publicKeyList(acct)})
	case http.MethodPost:
		if !sameOriginPost(r) || !a.checkAPICSRF(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "request rejected"})
			return
		}
		var body struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		}
		if err := readJSON(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return
		}
		key, err := a.accounts.AddPubKey(account, body.Key, body.Label)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": keyView(key)})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleKeysRevoke removes one enrolled key from the signed-in account.
func (a *Auth) handleKeysRevoke(w http.ResponseWriter, r *http.Request) {
	account, ok := a.localSessionAccount(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginPost(r) || !a.checkAPICSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "request rejected"})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	if err := a.accounts.RemovePubKey(account, body.ID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// localSessionAccount resolves the local account behind the session, writing the
// error response itself when there is none. IdP-backed accounts are refused: their
// credentials live at the identity provider, not here.
func (a *Auth) localSessionAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	account, ok := a.sessionAccount(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return "", false
	}
	if a.accounts == nil || !strings.HasPrefix(account, localPrefix) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "keyfile credentials are only available for local accounts",
		})
		return "", false
	}
	return account, true
}

// keyView is the safe projection of a key for the browser: never the key material
// itself, which the account page has no use for.
func keyView(k PubKey) map[string]any {
	return map[string]any{
		"id":       k.ID,
		"label":    k.Label,
		"added_at": k.AddedAt.UTC().Format("2006-01-02 15:04 UTC"),
	}
}

func publicKeyList(a Account) []map[string]any {
	out := make([]map[string]any, 0, len(a.PubKeys))
	for _, k := range a.PubKeys {
		out = append(out, keyView(k))
	}
	return out
}
