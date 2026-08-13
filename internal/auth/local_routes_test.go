package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct-horse-battery"

// localAuth builds a local-mode Auth with one registered account.
func localAuth(t *testing.T) (*Auth, Account) {
	t.Helper()
	a, store := authWithStore(t)
	a.clock = time.Now // the real clock: tokens must be valid now
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := store.Create("alice", hash)
	if err != nil {
		t.Fatal(err)
	}
	return a, acct
}

// postForm drives a POST through the gate the way a browser would, carrying any
// cookies given and returning the recorder.
func postForm(t *testing.T, a *Auth, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

// loginForm fetches GET /auth/login and returns the CSRF token and its cookie, so
// a test can POST the form the way the page does.
func loginForm(t *testing.T, a *Auth, path string) (string, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200\n%s", path, rec.Code, rec.Body.String())
	}
	token := formValue(rec.Body.String(), csrfField)
	if token == "" {
		t.Fatalf("GET %s rendered no %s field:\n%s", path, csrfField, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("GET %s set no %s cookie", path, csrfCookie)
	}
	return token, cookie
}

// redactCSRF replaces the per-render CSRF value so two pages can be compared for
// everything else.
func redactCSRF(html string) string {
	tok := formValue(html, csrfField)
	if tok == "" {
		return html
	}
	return strings.ReplaceAll(html, tok, "<redacted>")
}

// formValue pulls the value of a hidden input out of rendered HTML.
func formValue(html, name string) string {
	marker := `name="` + name + `" value="`
	i := strings.Index(html, marker)
	if i < 0 {
		return ""
	}
	rest := html[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

// The whole point: a password gets you a session, and the session reaches a
// protected route.
func TestLocalLoginEndToEnd(t *testing.T) {
	a, acct := localAuth(t)
	token, csrf := loginForm(t, a, "/auth/login")

	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"Alice"}, // the canonicaliser must accept the display spelling
		"password": {testPassword},
		csrfField:  {token},
	}, csrf)

	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302\n%s", rec.Code, rec.Body.String())
	}
	sess := sessionCookieFrom(rec)
	if sess == nil {
		t.Fatal("login set no session cookie")
	}
	if !sess.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	if sess.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", sess.SameSite)
	}

	got, ok := a.Verify(sess.Value, audWeb)
	if !ok || got != acct.Account {
		t.Fatalf("session cookie carries %q ok=%v, want %q", got, ok, acct.Account)
	}

	// And it opens a protected route.
	req := httptest.NewRequest(http.MethodGet, "/api/laptops", nil)
	req.AddCookie(sess)
	rec2 := httptest.NewRecorder()
	a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("protected"))
	})).ServeHTTP(rec2, req)
	if rec2.Body.String() != "protected" {
		t.Errorf("session did not open a protected route: %d %s", rec2.Code, rec2.Body.String())
	}
}

// A wrong password and an unknown user must be indistinguishable, or the form is a
// list of who has an account here.
func TestLoginIsNotAUsernameOracle(t *testing.T) {
	a, _ := localAuth(t)

	var bodies []string
	var codes []int
	for _, creds := range [][2]string{
		{"alice", "wrong-password-entirely"},
		{"nosuchuser", "wrong-password-entirely"},
		{"nosuchuser", testPassword},
	} {
		token, csrf := loginForm(t, a, "/auth/login")
		rec := postForm(t, a, "/auth/login", url.Values{
			"username": {creds[0]}, "password": {creds[1]}, csrfField: {token},
		}, csrf)
		if sessionCookieFrom(rec) != nil {
			t.Fatalf("%v was granted a session", creds)
		}
		codes = append(codes, rec.Code)
		// The CSRF token is fresh on every render, so blank it out: otherwise the
		// bodies always differ and this test can never fail.
		bodies = append(bodies, redactCSRF(rec.Body.String()))
	}
	for i := range codes {
		if codes[i] != codes[0] {
			t.Errorf("status %d differs from %d; the response distinguishes the cases", codes[i], codes[0])
		}
		if bodies[i] != bodies[0] {
			t.Errorf("body for case %d differs, so the form reveals whether an account exists", i)
		}
	}
	if !strings.Contains(bodies[0], "Invalid username or password") {
		t.Errorf("expected the uniform message, got:\n%s", bodies[0])
	}
}

func TestLoginRejectsADisabledAccount(t *testing.T) {
	a, acct := localAuth(t)
	if err := a.accounts.Update(acct.Account, func(x *Account) error {
		x.Disabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	token, csrf := loginForm(t, a, "/auth/login")
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword}, csrfField: {token},
	}, csrf)
	if sessionCookieFrom(rec) != nil {
		t.Error("a disabled account was granted a session")
	}
}

// Without the CSRF token the POST must be refused: otherwise any site can submit
// the authenticated forms on a visitor's behalf.
func TestFormsRequireCSRF(t *testing.T) {
	a, _ := localAuth(t)
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("login without a CSRF token = %d, want 403", rec.Code)
	}
	if sessionCookieFrom(rec) != nil {
		t.Error("a CSRF-less POST was granted a session")
	}

	// A token that does not match its cookie is equally void.
	token, _ := loginForm(t, a, "/auth/login")
	_, otherCookie := loginForm(t, a, "/auth/login")
	rec = postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword}, csrfField: {token},
	}, otherCookie)
	if rec.Code == http.StatusFound {
		t.Error("a CSRF token from a different session was accepted")
	}
}

func TestRegisterCreatesAnAccountAndSignsIn(t *testing.T) {
	a, _ := localAuth(t)
	token, csrf := loginForm(t, a, "/auth/register")

	rec := postForm(t, a, "/auth/register", url.Values{
		"username": {"bob"}, "password": {"another-good-password"},
		"confirm": {"another-good-password"}, csrfField: {token},
	}, csrf)
	if rec.Code != http.StatusFound {
		t.Fatalf("register = %d, want 302\n%s", rec.Code, rec.Body.String())
	}
	if _, ok := a.accounts.Lookup("bob"); !ok {
		t.Error("the account was not created")
	}
	sess := sessionCookieFrom(rec)
	if sess == nil {
		t.Fatal("registration did not sign the new user in")
	}
	if got, ok := a.Verify(sess.Value, audWeb); !ok || got != "local:bob" {
		t.Errorf("session = %q ok=%v, want local:bob", got, ok)
	}
}

func TestRegisterRejectsDuplicatesAndWeakInput(t *testing.T) {
	a, _ := localAuth(t)
	cases := []struct {
		name             string
		username, pw, cf string
	}{
		{"duplicate", "alice", "another-good-password", "another-good-password"},
		{"duplicate different case", "ALICE", "another-good-password", "another-good-password"},
		{"short password", "carol", "short", "short"},
		{"mismatched confirmation", "carol", "another-good-password", "different-password"},
		{"bad username", "c", "another-good-password", "another-good-password"},
		{"username with @", "carol@example.com", "another-good-password", "another-good-password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, csrf := loginForm(t, a, "/auth/register")
			rec := postForm(t, a, "/auth/register", url.Values{
				"username": {tc.username}, "password": {tc.pw},
				"confirm": {tc.cf}, csrfField: {token},
			}, csrf)
			if rec.Code == http.StatusFound {
				t.Errorf("%s was accepted", tc.name)
			}
			if sessionCookieFrom(rec) != nil {
				t.Errorf("%s was granted a session", tc.name)
			}
		})
	}
	if n := a.accounts.Count(); n != 1 {
		t.Errorf("store holds %d accounts, want 1 (only alice)", n)
	}
}

// Registration can be closed once the operator has their account.
func TestRegisterCanBeDisabled(t *testing.T) {
	a, _ := localAuth(t)
	t.Setenv("MN_AUTH_REGISTER", "off")

	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/register", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /auth/register with registration off = %d, want 404", rec.Code)
	}

	rec = postForm(t, a, "/auth/register", url.Values{
		"username": {"bob"}, "password": {"another-good-password"},
		"confirm": {"another-good-password"},
	})
	if rec.Code == http.StatusFound {
		t.Error("registration succeeded while disabled")
	}
	if _, ok := a.accounts.Lookup("bob"); ok {
		t.Error("an account was created while registration was disabled")
	}
}

// Changing the password must invalidate every existing credential — including the
// 30-day connector token — while keeping the browser that made the change signed in.
func TestPasswordChangeRevokesOldCredentials(t *testing.T) {
	a, acct := localAuth(t)
	oldSession := &http.Cookie{Name: sessionCookie, Value: a.Sign(acct.Account, audWeb, time.Hour)}
	oldConnector := a.Sign(acct.Account, audConnector, 30*24*time.Hour)

	rec := postForm(t, a, "/auth/password", url.Values{
		"old_password": {testPassword},
		"password":     {"a-brand-new-password"},
		"confirm":      {"a-brand-new-password"},
		csrfField:      {a.csrfToken(oldSession.Value)},
	}, oldSession)
	if rec.Code != http.StatusFound && rec.Code != http.StatusOK {
		t.Fatalf("password change = %d\n%s", rec.Code, rec.Body.String())
	}

	if _, ok := a.Verify(oldSession.Value, audWeb); ok {
		t.Error("the old session cookie still works after a password change")
	}
	if _, ok := a.Verify(oldConnector, audConnector); ok {
		t.Error("the old 30-day connector token still works after a password change")
	}

	// The browser that changed the password gets a fresh cookie, so it stays in.
	fresh := sessionCookieFrom(rec)
	if fresh == nil {
		t.Fatal("password change did not issue a new session cookie")
	}
	if got, ok := a.Verify(fresh.Value, audWeb); !ok || got != acct.Account {
		t.Errorf("new session = %q ok=%v", got, ok)
	}

	// The new password works and the old one does not.
	stored, _ := a.accounts.ByID(acct.Account)
	if !VerifyPassword(stored.PasswordHash, "a-brand-new-password") {
		t.Error("the new password was not stored")
	}
	if VerifyPassword(stored.PasswordHash, testPassword) {
		t.Error("the old password still verifies")
	}
}

// The old password is required, so a stolen session cannot silently take the
// account over.
func TestPasswordChangeRequiresTheOldPassword(t *testing.T) {
	a, acct := localAuth(t)
	sess := &http.Cookie{Name: sessionCookie, Value: a.Sign(acct.Account, audWeb, time.Hour)}

	rec := postForm(t, a, "/auth/password", url.Values{
		"old_password": {"not-the-old-password"},
		"password":     {"a-brand-new-password"},
		"confirm":      {"a-brand-new-password"},
		csrfField:      {a.csrfToken(sess.Value)},
	}, sess)
	if rec.Code == http.StatusFound {
		t.Error("the password was changed without the old one")
	}
	if _, ok := a.Verify(sess.Value, audWeb); !ok {
		t.Error("a failed password change invalidated the session")
	}
	stored, _ := a.accounts.ByID(acct.Account)
	if !VerifyPassword(stored.PasswordHash, testPassword) {
		t.Error("the stored password changed despite the failure")
	}
}

// Anonymous requests must not reach either mutating route (plan 1's allowlist).
func TestPasswordAndLogoutAllRequireASession(t *testing.T) {
	a, _ := localAuth(t)
	for _, path := range []string{"/auth/password", "/auth/logout-all"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		rec := httptest.NewRecorder()
		a.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Errorf("%s reached the protected handler anonymously", path)
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401", path, rec.Code)
		}
	}
}

func TestLogoutAllRevokesEverySession(t *testing.T) {
	a, acct := localAuth(t)
	phone := a.Sign(acct.Account, audWeb, time.Hour)
	laptop := a.Sign(acct.Account, audWeb, time.Hour)
	connector := a.Sign(acct.Account, audConnector, 30*24*time.Hour)

	rec := postForm(t, a, "/auth/logout-all", url.Values{
		csrfField: {a.csrfToken(phone)},
	}, &http.Cookie{Name: sessionCookie, Value: phone})
	if rec.Code != http.StatusFound && rec.Code != http.StatusOK {
		t.Fatalf("logout-all = %d\n%s", rec.Code, rec.Body.String())
	}
	for name, tok := range map[string]string{"phone": phone, "laptop": laptop} {
		if _, ok := a.Verify(tok, audWeb); ok {
			t.Errorf("%s session survived logout-all", name)
		}
	}
	if _, ok := a.Verify(connector, audConnector); ok {
		t.Error("the connector token survived logout-all")
	}
}

// Repeated wrong passwords must start being refused, or the form is an offline
// guessing oracle at online speed. The limit is per account AND per IP.
func TestLoginRateLimit(t *testing.T) {
	a, _ := localAuth(t)
	var last int
	for i := 0; i < maxLoginAttempts+2; i++ {
		token, csrf := loginForm(t, a, "/auth/login")
		rec := postForm(t, a, "/auth/login", url.Values{
			"username": {"alice"}, "password": {"wrong-password-here"}, csrfField: {token},
		}, csrf)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after %d failures the status was %d, want 429", maxLoginAttempts+2, last)
	}

	// And the correct password is refused too while the limit holds — otherwise the
	// limit only slows the attacker down, it does not stop the guess that lands.
	token, csrf := loginForm(t, a, "/auth/login")
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword}, csrfField: {token},
	}, csrf)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("during lockout a correct password returned %d, want 429", rec.Code)
	}
	if sessionCookieFrom(rec) != nil {
		t.Error("a session was granted during lockout")
	}
}

// A successful login clears the failure count, so a user who mistypes twice then
// succeeds is not locked out later.
func TestSuccessfulLoginClearsTheFailureCount(t *testing.T) {
	a, _ := localAuth(t)
	for i := 0; i < maxLoginAttempts-1; i++ {
		token, csrf := loginForm(t, a, "/auth/login")
		postForm(t, a, "/auth/login", url.Values{
			"username": {"alice"}, "password": {"wrong-password-here"}, csrfField: {token},
		}, csrf)
	}
	token, csrf := loginForm(t, a, "/auth/login")
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"alice"}, "password": {testPassword}, csrfField: {token},
	}, csrf)
	if rec.Code != http.StatusFound {
		t.Fatalf("login before the limit = %d, want 302", rec.Code)
	}
	for i := 0; i < maxLoginAttempts-1; i++ {
		token, csrf := loginForm(t, a, "/auth/login")
		rec := postForm(t, a, "/auth/login", url.Values{
			"username": {"alice"}, "password": {"wrong-password-here"}, csrfField: {token},
		}, csrf)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("locked out after %d failures following a success; the counter was not cleared", i+1)
		}
	}
}

// In OIDC-only mode nothing local is exposed: no form, no registration, and
// /auth/login keeps redirecting to the identity provider.
func TestLocalRoutesAbsentWithoutLocalMode(t *testing.T) {
	a := testAuth(time.Now()) // no account store
	for _, path := range []string{"/auth/register", "/auth/password", "/auth/logout-all"} {
		rec := postForm(t, a, path, url.Values{})
		if rec.Code == http.StatusFound || rec.Code == http.StatusOK {
			t.Errorf("%s answered %d with local accounts disabled", path, rec.Code)
		}
	}
}

// The login page must offer SSO only when an identity provider is configured.
func TestLoginPageOffersSSOOnlyWhenConfigured(t *testing.T) {
	a, _ := localAuth(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	a.Wrap(http.NotFoundHandler()).ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "/auth/sso") {
		t.Error("the login page offers SSO with no identity provider configured")
	}
	if !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Error("the login page has no password field")
	}
}

// The body being identical is not enough: if an unknown username skips the hash
// entirely, response time answers "does this account exist?" just as well. Both
// paths must perform one argon2 verification.
//
// A timing assertion, but not a tight one — the effect it guards against is the
// difference between one 64MiB hash (tens of ms) and none at all (microseconds), so
// a quarter of the known-account cost is a floor that only a skipped hash breaks.
func TestUnknownUserCostsTheSameAsAWrongPassword(t *testing.T) {
	a, _ := localAuth(t)

	median := func(username, password string) time.Duration {
		var samples []time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			if _, ok := a.checkPassword(username, password); ok {
				t.Fatalf("%s/%s unexpectedly authenticated", username, password)
			}
			samples = append(samples, time.Since(start))
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[1]
	}

	known := median("alice", "wrong-password-entirely")
	unknown := median("nosuchuser", "wrong-password-entirely")
	disabled := known // recorded below once the account is disabled

	if err := a.accounts.Update("local:alice", func(x *Account) error {
		x.Disabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	disabled = median("alice", testPassword)

	if known <= 0 {
		t.Fatal("a known-account attempt took no measurable time; the hash is not being computed")
	}
	for name, d := range map[string]time.Duration{"unknown user": unknown, "disabled account": disabled} {
		if d < known/4 {
			t.Errorf("%s took %v against %v for a known account: the hash is being skipped, which leaks whether the account exists",
				name, d, known)
		}
	}
}
