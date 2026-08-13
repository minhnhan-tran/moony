package auth

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// doJSON drives a JSON request through the gate the way our page script does:
// same-origin metadata, CSRF token in the header, cookies attached.
func doJSON(t *testing.T, a *Auth, method, path, body, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected"))
	})).ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// passwordLogin signs in with the password and returns the session cookie plus the
// CSRF token the account page renders for it.
func passwordLogin(t *testing.T, a *Auth) (sess *http.Cookie, csrf string) {
	t.Helper()
	token, ck := loginForm(t, a, "/auth/login")
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword}, csrfField: {token},
	}, ck)
	if rec.Code != http.StatusFound {
		t.Fatalf("password login = %d, want 302\n%s", rec.Code, rec.Body.String())
	}
	s := sessionCookieFrom(rec)
	if s == nil {
		t.Fatal("password login set no session cookie")
	}
	return s, accountCSRF(t, a, s)
}

// accountCSRF fetches the account page as the browser does and extracts the CSRF
// token it embeds, so the tests exercise the same token the real UI uses.
func accountCSRF(t *testing.T, a *Auth, sess *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/password", nil)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("account page = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no csrf token on the account page")
	}
	return m[1]
}

// enrol generates a keypair and enrols the public half through the HTTP route.
func enrol(t *testing.T, a *Auth, label string, sess *http.Cookie, csrf string) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, spki := testKey(t)
	body, _ := json.Marshal(map[string]string{"key": spki, "label": label})
	rec := doJSON(t, a, http.MethodPost, "/auth/keys", string(body), csrf, sess)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON(t, rec)
	view, _ := got["key"].(map[string]any)
	id, _ := view["id"].(string)
	if id == "" {
		t.Fatalf("enrol returned no key id: %v", got)
	}
	if strings.Contains(rec.Body.String(), spki[:32]) {
		t.Error("the enrolment response echoed key material back")
	}
	return key, id
}

// challengeFor asks for a challenge the way the login page does.
func challengeFor(t *testing.T, a *Auth, username string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/challenge?username="+url.QueryEscape(username), nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	blob, _ := decodeJSON(t, rec)["challenge"].(string)
	if blob == "" {
		t.Fatalf("no challenge in %s", rec.Body.String())
	}
	return blob
}

func keyfileLogin(t *testing.T, a *Auth, blob, sig string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"challenge": blob, "signature": sig, "return": "/"})
	return doJSON(t, a, http.MethodPost, "/auth/login-keyfile", string(body), "")
}

func TestKeyfileLoginEndToEnd(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)

	blob := challengeFor(t, a, "Alice") // display spelling must canonicalise
	rec := keyfileLogin(t, a, blob, signP1363(t, key, blob))
	if rec.Code != http.StatusOK {
		t.Fatalf("keyfile login = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	newSess := sessionCookieFrom(rec)
	if newSess == nil {
		t.Fatal("keyfile login set no session cookie")
	}
	if !newSess.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	// And it actually reaches a protected route.
	req := httptest.NewRequest(http.MethodGet, "/api/laptops", nil)
	req.AddCookie(newSess)
	rec2 := httptest.NewRecorder()
	a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("protected route with a keyfile session = %d, want 200", rec2.Code)
	}
}

// A challenge is single-use even at the route level, and a signature over a
// different challenge is worthless.
func TestKeyfileLoginRejectsReplayAndSubstitution(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)

	blob := challengeFor(t, a, "alice")
	sig := signP1363(t, key, blob)
	if rec := keyfileLogin(t, a, blob, sig); rec.Code != http.StatusOK {
		t.Fatalf("first login = %d, want 200", rec.Code)
	}
	if rec := keyfileLogin(t, a, blob, sig); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed challenge = %d, want 401", rec.Code)
	}
	// A signature made over challenge A cannot authorise challenge B.
	blobA := challengeFor(t, a, "alice")
	blobB := challengeFor(t, a, "alice")
	if rec := keyfileLogin(t, a, blobB, signP1363(t, key, blobA)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mismatched challenge = %d, want 401", rec.Code)
	}
}

// The account comes from the signed challenge. Naming another account in the body
// must not be honoured — and because the JSON is strict, it is not even accepted.
func TestKeyfileLoginIgnoresAccountFields(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)
	if _, err := a.accounts.Create("bob", "$argon2id$fake"); err != nil {
		t.Fatal(err)
	}

	blob := challengeFor(t, a, "alice")
	body, _ := json.Marshal(map[string]string{
		"challenge": blob, "signature": signP1363(t, key, blob), "account": "local:bob",
	})
	rec := doJSON(t, a, http.MethodPost, "/auth/login-keyfile", string(body), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("login with an extra account field = %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	// Alice's own key must never authenticate bob even when bob is the named user
	// in the challenge request: the challenge is bound to the account it names.
	bobBlob := challengeFor(t, a, "bob")
	if rec := keyfileLogin(t, a, bobBlob, signP1363(t, key, bobBlob)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("alice's key logged in as bob: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestKeyEnrolmentRequiresASession(t *testing.T) {
	a, _ := localAuth(t)
	_, spki := testKey(t)
	body, _ := json.Marshal(map[string]string{"key": spki, "label": "x"})

	rec := doJSON(t, a, http.MethodPost, "/auth/keys", string(body), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated enrol = %d, want 401\n%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, a, http.MethodGet, "/auth/keys", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated key list = %d, want 401", rec.Code)
	}
	acct, _ := a.accounts.ByID("local:alice")
	if len(acct.PubKeys) != 0 {
		t.Fatalf("a key was enrolled without a session: %+v", acct.PubKeys)
	}
}

// CSRF and same-origin are both required, so a hostile page cannot enrol its own
// key onto a signed-in visitor's account (which would be a persistent backdoor).
func TestKeyEnrolmentRequiresCSRFAndSameOrigin(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	_, spki := testKey(t)
	body, _ := json.Marshal(map[string]string{"key": spki, "label": "evil"})

	if rec := doJSON(t, a, http.MethodPost, "/auth/keys", body2str(body), "", sess); rec.Code != http.StatusForbidden {
		t.Fatalf("enrol without a CSRF token = %d, want 403", rec.Code)
	}
	if rec := doJSON(t, a, http.MethodPost, "/auth/keys", body2str(body), "wrong-token", sess); rec.Code != http.StatusForbidden {
		t.Fatalf("enrol with a bogus CSRF token = %d, want 403", rec.Code)
	}
	// A token minted for someone else's session is not interchangeable with ours.
	if _, err := a.accounts.Create("carol", "$argon2id$fake"); err != nil {
		t.Fatal(err)
	}
	carol := sessionFor(t, a, "local:carol")
	if rec := doJSON(t, a, http.MethodPost, "/auth/keys", body2str(body), accountCSRF(t, a, carol), sess); rec.Code != http.StatusForbidden {
		t.Fatalf("enrol with another session's CSRF token = %d, want 403", rec.Code)
	}
	// Correct token, but the request announces itself as cross-site.
	req := httptest.NewRequest(http.MethodPost, "/auth/keys", strings.NewReader(body2str(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site enrol = %d, want 403", rec.Code)
	}
	// A request with no origin metadata at all is refused too.
	req2 := httptest.NewRequest(http.MethodPost, "/auth/keys", strings.NewReader(body2str(body)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-CSRF-Token", csrf)
	req2.AddCookie(sess)
	rec2 := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("enrol with no origin metadata = %d, want 403", rec2.Code)
	}

	acct, _ := a.accounts.ByID("local:alice")
	if len(acct.PubKeys) != 0 {
		t.Fatalf("a key was enrolled by a rejected request: %+v", acct.PubKeys)
	}
}

func body2str(b []byte) string { return string(b) }

func TestDisabledAccountCannotKeyfileLogin(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)

	if err := a.accounts.Update("local:alice", func(acct *Account) error {
		acct.Disabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blob := challengeFor(t, a, "alice")
	rec := keyfileLogin(t, a, blob, signP1363(t, key, blob))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account keyfile login = %d, want 401\n%s", rec.Code, rec.Body.String())
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("a disabled account was given a session")
	}
}

// A password change bumps the epoch (killing sessions and connector tokens). It
// must NOT invalidate enrolled keys: the keyfile is a separate credential.
func TestEpochBumpKeepsKeysUsableButRevocationDoesNot(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, id := enrol(t, a, "laptop", sess, csrf)

	if err := a.accounts.Update("local:alice", func(acct *Account) error {
		acct.Epoch++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blob := challengeFor(t, a, "alice")
	rec := keyfileLogin(t, a, blob, signP1363(t, key, blob))
	if rec.Code != http.StatusOK {
		t.Fatalf("keyfile login after an epoch bump = %d, want 200\n%s", rec.Code, rec.Body.String())
	}

	// Revoke through the route, using the fresh session the keyfile login produced.
	newSess := sessionCookieFrom(rec)
	csrf = accountCSRF(t, a, newSess) // tokens are bound to the session they came from
	revoke, _ := json.Marshal(map[string]string{"id": id})
	rr := doJSON(t, a, http.MethodPost, "/auth/keys/revoke", body2str(revoke), csrf, newSess)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	blob2 := challengeFor(t, a, "alice")
	if rec := keyfileLogin(t, a, blob2, signP1363(t, key, blob2)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked key still logged in: %d", rec.Code)
	}
	// Revoking again must fail rather than silently succeed.
	if rr := doJSON(t, a, http.MethodPost, "/auth/keys/revoke", body2str(revoke), csrf, newSess); rr.Code != http.StatusBadRequest {
		t.Fatalf("re-revoke = %d, want 400", rr.Code)
	}
}

// One session may never touch another account's keys. The route reads the account
// from the session, so there is no field to abuse — assert the property directly.
func TestKeysAreScopedToTheSessionAccount(t *testing.T) {
	a, _ := localAuth(t)
	if _, err := a.accounts.Create("bob", "$argon2id$fake"); err != nil {
		t.Fatal(err)
	}
	sess, csrf := passwordLogin(t, a)
	_, id := enrol(t, a, "alice-key", sess, csrf)

	// Bob's session cannot revoke alice's key id.
	bobSess := sessionFor(t, a, "local:bob")
	bobCSRF := accountCSRF(t, a, bobSess)
	revoke, _ := json.Marshal(map[string]string{"id": id})
	rec := doJSON(t, a, http.MethodPost, "/auth/keys/revoke", body2str(revoke), bobCSRF, bobSess)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-account revoke = %d, want 400\n%s", rec.Code, rec.Body.String())
	}
	acct, _ := a.accounts.ByID("local:alice")
	if len(acct.PubKeys) != 1 {
		t.Fatal("bob revoked alice's key")
	}
	// And bob's enrolment lands on bob, not on alice.
	_, spki := testKey(t)
	body, _ := json.Marshal(map[string]string{"key": spki, "label": "bob"})
	if rec := doJSON(t, a, http.MethodPost, "/auth/keys", body2str(body), bobCSRF, bobSess); rec.Code != http.StatusOK {
		t.Fatalf("bob's own enrolment = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	alice, _ := a.accounts.ByID("local:alice")
	bob, _ := a.accounts.ByID("local:bob")
	if len(alice.PubKeys) != 1 || len(bob.PubKeys) != 1 {
		t.Fatalf("keys landed on the wrong accounts: alice=%d bob=%d", len(alice.PubKeys), len(bob.PubKeys))
	}
}

// sessionFor mints a session cookie for an account directly.
func sessionFor(t *testing.T, a *Auth, account string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	a.setSession(rec, account)
	c := sessionCookieFrom(rec)
	if c == nil {
		t.Fatalf("no session cookie for %s", account)
	}
	return c
}

// The challenge endpoint must not reveal which accounts exist.
func TestChallengeEndpointIsNotAUsernameOracle(t *testing.T) {
	a, _ := localAuth(t)
	known := challengeFor(t, a, "alice")
	unknown := challengeFor(t, a, "definitely-not-here")
	malformed := challengeFor(t, a, "a") // too short to be a valid account name
	if known == "" || unknown == "" || malformed == "" {
		t.Fatal("a challenge was withheld")
	}
	// Same shape for all three: payload.mac, and nothing that leaks existence.
	for _, blob := range []string{known, unknown, malformed} {
		if strings.Count(blob, ".") != 1 {
			t.Fatalf("unexpected challenge shape %q", blob)
		}
	}
	// The unusable ones cannot be turned into a session even with a valid signature
	// by a key enrolled elsewhere.
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)
	for _, blob := range []string{unknown, malformed} {
		if rec := keyfileLogin(t, a, blob, signP1363(t, key, blob)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a challenge for a non-account authenticated: %d", rec.Code)
		}
	}
}

func TestKeyfileLoginRateLimitTrips(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	key, _ := enrol(t, a, "laptop", sess, csrf)
	wrongKey, _ := testKey(t)

	tripped := false
	for i := 0; i < maxLoginAttemptsPerIP+2 && !tripped; i++ {
		// Once the address is locked out the challenge endpoint closes too, which is
		// itself the limiter working.
		req := httptest.NewRequest(http.MethodGet, "/auth/challenge?username=alice", nil)
		crec := httptest.NewRecorder()
		a.Wrap(http.NotFoundHandler()).ServeHTTP(crec, req)
		if crec.Code == http.StatusTooManyRequests {
			tripped = true
			break
		}
		blob, _ := decodeJSON(t, crec)["challenge"].(string)
		rec := keyfileLogin(t, a, blob, signP1363(t, wrongKey, blob))
		if rec.Code == http.StatusTooManyRequests {
			tripped = true
			if rec.Header().Get("Retry-After") == "" {
				t.Error("a throttled response carried no Retry-After")
			}
		}
	}
	if !tripped {
		t.Fatalf("the rate limit never tripped after %d bad keyfile logins", maxLoginAttemptsPerIP+2)
	}
	// Even the right key is refused while the lockout holds.
	blob, err := a.mintChallenge("local:alice")
	if err != nil {
		t.Fatal(err)
	}
	if rec := keyfileLogin(t, a, blob, signP1363(t, key, blob)); rec.Code == http.StatusOK {
		t.Fatal("a locked-out address logged in with a valid signature")
	}
}

// Bad keyfile signatures must not lock the account out of PASSWORD login: anyone
// can ask for a challenge naming any user, so that would be a free denial of
// service against a known username.
func TestKeyfileFailuresDoNotBlockPasswordLogin(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	enrol(t, a, "laptop", sess, csrf)
	wrongKey, _ := testKey(t)
	for i := 0; i < maxLoginAttempts+3; i++ {
		blob, err := a.mintChallenge("local:alice")
		if err != nil {
			t.Fatal(err)
		}
		if rec := keyfileLogin(t, a, blob, signP1363(t, wrongKey, blob)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, rec.Code)
		}
	}
	if a.throttler().blocked("local:alice") {
		t.Fatal("keyfile failures locked the account's password bucket")
	}
}

// Two goroutines racing on one challenge: exactly one may win.
func TestConcurrentRedeemOfOneChallenge(t *testing.T) {
	a, _ := movableAuth(t)
	blob, err := a.mintChallenge("local:alice")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := a.redeemChallenge(blob)
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d goroutines redeemed the same challenge, want exactly 1", wins)
	}
}

// Revocation is state-changing, so it needs the same CSRF and origin checks as
// enrolment; without them a hostile page could disable someone's credentials.
func TestRevokeRequiresCSRFAndSameOrigin(t *testing.T) {
	a, _ := localAuth(t)
	sess, csrf := passwordLogin(t, a)
	_, id := enrol(t, a, "laptop", sess, csrf)
	body, _ := json.Marshal(map[string]string{"id": id})

	if rec := doJSON(t, a, http.MethodPost, "/auth/keys/revoke", body2str(body), "", sess); rec.Code != http.StatusForbidden {
		t.Fatalf("revoke without a CSRF token = %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/keys/revoke", strings.NewReader(body2str(body)))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site revoke = %d, want 403", rec.Code)
	}
	acct, _ := a.accounts.ByID("local:alice")
	if len(acct.PubKeys) != 1 {
		t.Fatal("a rejected request revoked the key anyway")
	}
}

// An IdP-only deployment has no account store, so the key routes must refuse
// rather than panic on a nil store.
func TestKeyRoutesRefuseWithoutAnAccountStore(t *testing.T) {
	a := testAuth(time.Now())
	rec := httptest.NewRecorder()
	a.setSession(rec, "idp-subject")
	sess := sessionCookieFrom(rec)
	if sess == nil {
		t.Fatal("no session cookie")
	}
	for _, path := range []string{"/auth/keys", "/auth/keys/revoke"} {
		r := doJSON(t, a, http.MethodPost, path, "{}", "irrelevant", sess)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("%s with no account store = %d, want 400\n%s", path, r.Code, r.Body.String())
		}
	}
}

// With local mode off there is nothing to challenge or log into.
func TestKeyfileRoutesAbsentWithoutLocalMode(t *testing.T) {
	a := testAuth(time.Now())
	for _, path := range []string{"/auth/challenge", "/auth/login-keyfile"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s without local mode = %d, want 404", path, rec.Code)
		}
	}
}

// The browser half must be reachable before login, or the keyfile panel is dead.
func TestKeyfileScriptIsPublicAndCacheable(t *testing.T) {
	a, _ := localAuth(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/keyfile.js", nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/keyfile.js = %d, want 200 without a session", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("content type = %q", ct)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the script")
	}
	if !strings.Contains(rec.Body.String(), "login-keyfile") {
		t.Fatal("the served script does not look like keyfile.js")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/auth/keyfile.js", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", rec2.Code)
	}
}

// The pages must actually offer the feature, and the account page must list keys
// without ever exposing key material.
func TestPagesRenderKeyfileUI(t *testing.T) {
	a, _ := localAuth(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`id="keyfile_login"`, `/auth/keyfile.js`, `id="keyfile_file"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login page is missing %s", want)
		}
	}

	sess, csrf := passwordLogin(t, a)
	_, spki := testKey(t)
	enrolBody, _ := json.Marshal(map[string]string{"key": spki, "label": "work <laptop>"})
	if rr := doJSON(t, a, http.MethodPost, "/auth/keys", body2str(enrolBody), csrf, sess); rr.Code != http.StatusOK {
		t.Fatalf("enrol = %d\n%s", rr.Code, rr.Body.String())
	}
	page := httptest.NewRequest(http.MethodGet, "/auth/password", nil)
	page.AddCookie(sess)
	prec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(prec, page)
	if prec.Code != http.StatusOK {
		t.Fatalf("account page = %d\n%s", prec.Code, prec.Body.String())
	}
	acctBody := prec.Body.String()
	for _, want := range []string{`id="keyfile_enrol"`, `data-key-id=`, `/auth/keyfile.js`, "added "} {
		if !strings.Contains(acctBody, want) {
			t.Errorf("account page is missing %s", want)
		}
	}
	if strings.Contains(acctBody, spki[:32]) {
		t.Error("the account page leaked key material")
	}
	// A label is user input rendered into HTML: it must arrive escaped.
	if strings.Contains(acctBody, "work <laptop>") {
		t.Error("a key label was rendered unescaped")
	}
	if !strings.Contains(acctBody, "work &lt;laptop&gt;") {
		t.Error("the escaped label is missing from the page")
	}
}
