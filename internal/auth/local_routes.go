package auth

import (
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Login throttling. A password form is an online guessing oracle, and argon2 makes
// each attempt expensive for us as well as the attacker, so failures are counted
// per account and per client address.
const (
	// Per account: tight, because it is a targeted guess.
	maxLoginAttempts = 8
	// Per client address: deliberately looser. Behind a reverse proxy every request
	// arrives from the proxy's address, so a bucket as tight as the per-account one
	// would let one attacker lock every user out of the relay.
	maxLoginAttemptsPerIP = 40
	// maxKeyfileFailures bounds bad signatures per account. It exists to stop log
	// spam and pointless work, not to stop brute force.
	maxKeyfileFailures = 50
	loginLockout       = 15 * time.Minute
	limiterMaxKeys     = 4096
)

const (
	csrfCookie = "moony_csrf"
	csrfField  = "csrf_token"
)

// dummyHash is verified against when no account matches, so a request for an
// unknown user costs the same as one for a known user with a wrong password.
// Without it, response time answers "does this account exist?".
var dummyHash = func() string {
	h, err := HashPassword("a-password-nobody-will-ever-use-here")
	if err != nil { // unreachable: the literal satisfies ValidatePassword
		panic("auth: cannot build the timing-equalising hash: " + err.Error())
	}
	return h
}()

// loginLimiter counts recent failures per key (account id and client IP).
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*failCount
	now   func() time.Time
}

type failCount struct {
	n     int
	until time.Time
}

// limitFor is the allowance for a key. Login-by-address gets the loose allowance
// (a proxy makes every client share one address); accounts and registration get the
// tight one.
func limitFor(key string) int {
	switch {
	case strings.HasPrefix(key, "kf:"):
		// Keyfile failures: looser than password guessing, because guessing a P-256
		// signature is not a thing that happens, and because this bucket is reachable
		// for any named account by an unauthenticated caller.
		return maxKeyfileFailures
	case strings.HasPrefix(key, "chal:"):
		// Challenge minting is metered, not a failure count, so it gets its own
		// looser bucket; sharing the login bucket would let challenge requests lock
		// an address out of logging in.
		return maxChallengeMints
	case strings.HasPrefix(key, "ip:"):
		return maxLoginAttemptsPerIP
	}
	return maxLoginAttempts
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{fails: map[string]*failCount{}, now: now}
}

// blocked reports whether any of keys is currently locked out.
func (l *loginLimiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		f, ok := l.fails[k]
		if !ok {
			continue
		}
		if now.After(f.until) {
			delete(l.fails, k) // window elapsed
			continue
		}
		if f.n >= limitFor(k) {
			return true
		}
	}
	return false
}

func (l *loginLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		f, ok := l.fails[k]
		if !ok || now.After(f.until) {
			f = &failCount{}
			l.fails[k] = f
		}
		f.n++
		f.until = now.Add(loginLockout)
	}
	// Keep the map from growing without bound on a public relay: junk usernames and
	// spoofed-looking addresses are free for a caller to invent.
	if len(l.fails) > limiterMaxKeys {
		for k, f := range l.fails {
			if now.After(f.until) {
				delete(l.fails, k)
			}
		}
		// Still over cap: every entry is live, so drop arbitrary ones. Losing a
		// counter is a smaller problem than unbounded memory, and the per-account
		// key for a real victim is refreshed on their attacker's next attempt.
		for k := range l.fails {
			if len(l.fails) <= limiterMaxKeys {
				break
			}
			delete(l.fails, k)
		}
	}
}

func (l *loginLimiter) clear(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fails, k)
	}
}

// clientIP is the throttling key for an address.
//
// X-Forwarded-For is honoured ONLY when the immediate peer is listed in
// MN_AUTH_TRUSTED_PROXIES, because otherwise the header is attacker-supplied and
// anyone could reset their own limit by varying it. Without that variable the peer
// address is used, which behind a reverse proxy is the proxy — hence the looser
// per-address allowance (maxLoginAttemptsPerIP).
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	if !peerIsTrustedProxy(peer) {
		return peer
	}
	// Right-most entry is the one the trusted proxy itself appended.
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peer
	}
	parts := strings.Split(xff, ",")
	claimed := strings.TrimSpace(parts[len(parts)-1])
	if ip := net.ParseIP(claimed); ip != nil {
		return ip.String()
	}
	return peer
}

// trustedProxies is parsed once from MN_AUTH_TRUSTED_PROXIES (comma-separated IPs
// or CIDRs).
var trustedProxies = sync.OnceValue(func() []*net.IPNet {
	var out []*net.IPNet
	for _, raw := range strings.Split(os.Getenv("MN_AUTH_TRUSTED_PROXIES"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(raw); err == nil {
			out = append(out, cidr)
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
})

func peerIsTrustedProxy(peer string) bool {
	ip := net.ParseIP(peer)
	if ip == nil {
		return false
	}
	for _, n := range trustedProxies() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// csrfToken binds a form to something the caller must already possess: either the
// session cookie (authenticated forms) or a per-visit CSRF cookie (login and
// register, where there is no session yet). A cross-site POST cannot read either.
//
// The prefix is NUL-terminated and distinct from every other MAC this package
// computes, so a CSRF token can never coincide with a per-account signing key or a
// token/challenge MAC. The old "csrf|" prefix could collide in principle with the
// per-account key derivation (account+"|"+epoch), which is the same shape of bug
// as the token-confusion fix in confusion_test.go.
func (a *Auth) csrfToken(seed string) string {
	return base64.RawURLEncoding.EncodeToString(macWith(a.secret, csrfDomain+seed))
}

func (a *Auth) csrfValid(seed, presented string) bool {
	if seed == "" || presented == "" {
		return false
	}
	return hmac.Equal([]byte(a.csrfToken(seed)), []byte(presented))
}

// issueCSRF sets the per-visit CSRF cookie and returns the token for the form.
func (a *Auth) issueCSRF(w http.ResponseWriter) string {
	seed := randToken()
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: seed, Path: "/", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteLaxMode, MaxAge: 3600,
	})
	return a.csrfToken(seed)
}

// checkFormCSRF validates the token on an unauthenticated form POST.
func (a *Auth) checkFormCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil {
		return false
	}
	return a.csrfValid(c.Value, r.PostFormValue(csrfField))
}

// registrationOpen reports whether self-registration is accepted. Open by default:
// a relay with local accounts and no way to make one is useless. Close it with
// MN_AUTH_REGISTER=off once the accounts that should exist do.
func registrationOpen() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_AUTH_REGISTER"))) {
	case "off", "0", "false", "no", "closed":
		return false
	}
	return true
}

// handleLocalLogin renders and processes the local login form.
func (a *Auth) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	returnTo := sameHostRedirect(r.FormValue("return"))
	if r.Method != http.MethodPost {
		a.renderLogin(w, loginPage{Mode: "login", ReturnTo: returnTo, CSRF: a.issueCSRF(w)})
		return
	}
	if !a.checkFormCSRF(r) {
		// A stale form is the common cause, so say so and re-issue rather than
		// leaving the visitor at a dead end.
		a.renderLoginStatus(w, http.StatusForbidden, loginPage{
			Mode: "login", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: "That form expired. Please try again.",
		})
		return
	}

	username := r.PostFormValue("username")
	password := r.PostFormValue("password")

	// Throttle on the canonical id when we can derive one, and always on the IP.
	ip := clientIP(r)
	id, idErr := AccountID(username)
	keys := []string{"ip:" + ip}
	if idErr == nil {
		keys = append(keys, id)
	}
	if a.throttler().blocked(keys...) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		a.renderLoginStatus(w, http.StatusTooManyRequests, loginPage{
			Mode: "login", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: "Too many attempts. Wait a few minutes and try again.",
		})
		return
	}

	account, ok := a.checkPassword(username, password)
	if !ok {
		a.throttler().fail(keys...)
		// One message for every failure: wrong password, no such account, disabled
		// account, malformed name. Anything more specific is a directory.
		a.renderLoginStatus(w, http.StatusUnauthorized, loginPage{
			Mode: "login", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: "Invalid username or password.",
		})
		return
	}
	a.throttler().clear(keys...)
	a.setSession(w, account.Account)
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// checkPassword verifies a submitted username and password, in constant-ish time
// whether or not the account exists.
func (a *Auth) checkPassword(username, password string) (Account, bool) {
	if a.accounts == nil {
		return Account{}, false
	}
	acct, found := a.accounts.Lookup(username)
	hash := acct.PasswordHash
	if !found || acct.Disabled || hash == "" {
		hash = dummyHash // spend the same CPU, then fail
	}
	pwOK := VerifyPassword(hash, password)
	if !found || acct.Disabled || !pwOK {
		return Account{}, false
	}
	return acct, true
}

// handleRegister renders and processes self-registration.
func (a *Auth) handleRegister(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil || !registrationOpen() {
		// 404, not 403: with registration closed the route does not exist as far as
		// a visitor is concerned.
		http.NotFound(w, r)
		return
	}
	returnTo := sameHostRedirect(r.FormValue("return"))
	if r.Method != http.MethodPost {
		a.renderLogin(w, loginPage{Mode: "register", ReturnTo: returnTo, CSRF: a.issueCSRF(w)})
		return
	}
	if !a.checkFormCSRF(r) {
		a.renderLoginStatus(w, http.StatusForbidden, loginPage{
			Mode: "register", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: "That form expired. Please try again.",
		})
		return
	}

	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")

	fail := func(msg string) {
		a.renderLoginStatus(w, http.StatusBadRequest, loginPage{
			Mode: "register", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: msg, Username: username,
		})
	}
	// Registration hashes a password, so it is throttled exactly like login —
	// otherwise the limiter on /auth/login is bypassed by posting here instead.
	// A "reg:" key, not "ip:", so registration gets the tighter allowance: it is a
	// rare action that also creates state, and account spam is its own nuisance.
	keys := []string{"reg:" + clientIP(r)}
	if a.throttler().blocked(keys...) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		a.renderLoginStatus(w, http.StatusTooManyRequests, loginPage{
			Mode: "register", ReturnTo: returnTo, CSRF: a.issueCSRF(w),
			Error: "Too many attempts. Wait a few minutes and try again.",
		})
		return
	}
	id, err := AccountID(username)
	if err != nil {
		a.throttler().fail(keys...)
		fail(err.Error())
		return
	}
	// Reject a name that is taken BEFORE hashing: otherwise re-posting a username
	// known to exist costs the relay 64MiB per request for free.
	if _, exists := a.accounts.ByID(id); exists {
		a.throttler().fail(keys...)
		fail("That username is already taken.")
		return
	}
	if password != confirm {
		a.throttler().fail(keys...)
		fail("Those passwords do not match.")
		return
	}
	if err := ValidatePassword(password); err != nil {
		a.throttler().fail(keys...)
		fail(err.Error())
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		fail(err.Error())
		return
	}
	// Create re-checks under the store lock, so a racing registration still loses.
	acct, err := a.accounts.Create(username, hash)
	if err != nil {
		if errors.Is(err, ErrAccountExists) {
			// Registration necessarily reveals that a name is taken; that is the
			// one place this is unavoidable, and it says nothing about passwords.
			fail("That username is already taken.")
			return
		}
		fail(err.Error())
		return
	}
	a.setSession(w, acct.Account)
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// handlePassword changes the signed-in account's password. Requires the current
// password, then bumps the epoch so every other credential dies.
func (a *Auth) handlePassword(w http.ResponseWriter, r *http.Request) {
	account, ok := a.sessionAccount(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if a.accounts == nil || !strings.HasPrefix(account, localPrefix) {
		http.Error(w, "this account's password is managed by your identity provider", http.StatusBadRequest)
		return
	}
	sess, _ := r.Cookie(sessionCookie)
	if r.Method != http.MethodPost {
		// Without this the route was POST-only, i.e. unusable from a browser: the
		// only way to rotate a password would have been a hand-rolled curl call.
		if sess == nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		acct, _ := a.accounts.ByID(account)
		a.renderAccount(w, http.StatusOK, accountPage{
			Username: strings.TrimPrefix(account, localPrefix),
			Account:  account,
			CSRF:     a.csrfToken(sess.Value),
			Changed:  r.URL.Query().Get("changed") == "1",
			Keys:     keyRows(acct),
		})
		return
	}
	if sess == nil || !a.csrfValid(sess.Value, r.PostFormValue(csrfField)) {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
		return
	}

	acct, found := a.accounts.ByID(account)
	if !found {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	old := r.PostFormValue("old_password")
	next := r.PostFormValue("password")

	// Check the allowance BEFORE verifying: VerifyPassword is the expensive part, so
	// testing it first would let a locked-out caller keep spending 64MiB a request.
	keys := []string{"ip:" + clientIP(r), account}
	if a.throttler().blocked(keys...) {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(loginLockout.Seconds())))
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	if !VerifyPassword(acct.PasswordHash, old) {
		a.throttler().fail(keys...)
		http.Error(w, "current password is incorrect", http.StatusForbidden)
		return
	}
	a.throttler().clear(keys...)
	if next != r.PostFormValue("confirm") {
		http.Error(w, "those passwords do not match", http.StatusBadRequest)
		return
	}
	if err := ValidatePassword(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash, err := HashPassword(next)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// One update: the new hash and the epoch bump together, so a crash between them
	// cannot leave the password changed with old tokens still valid.
	if err := a.accounts.Update(account, func(x *Account) error {
		x.PasswordHash = hash
		x.Epoch++
		return nil
	}); err != nil {
		http.Error(w, "could not save the new password", http.StatusInternalServerError)
		return
	}

	// The epoch bump just invalidated the cookie this request arrived with, so hand
	// the browser a fresh one — otherwise changing your password logs you out.
	a.setSession(w, account)
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	http.Redirect(w, r, "/auth/password?changed=1", http.StatusFound)
}

// handleLogoutAll revokes every credential for the signed-in account: all browser
// sessions and every connector token.
func (a *Auth) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	account, ok := a.sessionAccount(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, _ := r.Cookie(sessionCookie)
	if sess == nil || !a.csrfValid(sess.Value, r.PostFormValue(csrfField)) {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
		return
	}
	if a.accounts == nil || !strings.HasPrefix(account, localPrefix) {
		http.Error(w, "only local accounts can be revoked here", http.StatusBadRequest)
		return
	}
	if _, err := a.accounts.BumpEpoch(account); err != nil {
		http.Error(w, "could not revoke sessions", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

// setSession issues the browser session cookie for account.
func (a *Auth) setSession(w http.ResponseWriter, account string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.Sign(account, audWeb, a.ttl),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

// loginPage is the login/register template's data.
type loginPage struct {
	Mode     string // "login" or "register"
	Error    string
	Username string
	ReturnTo string
	CSRF     string
	SSO      bool
	Register bool // offer the link to the other form
}

func (a *Auth) renderLogin(w http.ResponseWriter, p loginPage) {
	a.renderLoginStatus(w, http.StatusOK, p)
}

func (a *Auth) renderLoginStatus(w http.ResponseWriter, code int, p loginPage) {
	p.SSO = a.SSOEnabled()
	p.Register = registrationOpen()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	// Every field is escaped by html/template; nothing is concatenated into HTML.
	if err := loginTmpl.Execute(w, p); err != nil {
		return
	}
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{if eq .Mode "register"}}Create an account{{else}}Sign in{{end}} · moony</title>
<link rel="icon" href="/favicon.ico">
<link rel="stylesheet" href="/static/app.css">
<style>
  .auth-wrap{min-height:100vh;display:grid;place-items:center;padding:1.5rem}
  .auth-card{width:min(23rem,100%);background:var(--c-surface,#fff);color:var(--c-text,#111);
    border:1px solid var(--c-border,#e3e3e3);border-radius:var(--radius-lg,14px);
    padding:1.5rem;box-shadow:0 10px 30px rgb(0 0 0 / 8%)}
  .auth-card h1{margin:0 0 .25rem;font-size:1.25rem}
  .auth-card p.sub{margin:0 0 1.25rem;color:var(--c-text-dim,#666);font-size:.875rem}
  .auth-card label{display:block;font-size:.8125rem;margin:.75rem 0 .25rem;font-weight:600}
  .auth-card input{width:100%;box-sizing:border-box;padding:.5rem .625rem;font:inherit;
    border:1px solid var(--c-border,#ccc);border-radius:var(--radius-sm,8px);
    background:var(--c-bg,#fff);color:inherit}
  .auth-card button{width:100%;margin-top:1.25rem;padding:.5rem;font:inherit;font-weight:600;
    border:0;border-radius:var(--radius-sm,8px);background:var(--c-accent,#3b6ef5);color:#fff;cursor:pointer}
  .auth-err{margin:0 0 1rem;padding:.5rem .625rem;border-radius:var(--radius-sm,8px);
    background:color-mix(in srgb,var(--c-danger,#d33) 12%,transparent);
    border:1px solid color-mix(in srgb,var(--c-danger,#d33) 40%,transparent);font-size:.8125rem}
  .auth-alt{margin:1rem 0 0;text-align:center;font-size:.8125rem}
  .auth-sso{display:block;margin-top:.75rem;padding:.5rem;text-align:center;font-weight:600;
    border:1px solid var(--c-border,#ccc);border-radius:var(--radius-sm,8px);text-decoration:none;color:inherit}
  .auth-or{display:flex;align-items:center;gap:.5rem;margin:1rem 0 0;color:var(--c-text-dim,#888);font-size:.75rem}
  .auth-or::before,.auth-or::after{content:"";flex:1;height:1px;background:var(--c-border,#ddd)}
</style>
</head><body class="auth-body"><div class="auth-wrap"><div class="auth-card">
<h1>{{if eq .Mode "register"}}Create an account{{else}}Sign in{{end}}</h1>
<p class="sub">moony relay</p>
{{if .Error}}<p class="auth-err">{{.Error}}</p>{{end}}
<form method="post" action="{{if eq .Mode "register"}}/auth/register{{else}}/auth/login{{end}}">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <input type="hidden" name="return" value="{{.ReturnTo}}">
  <label for="username">Username</label>
  <input id="username" name="username" value="{{.Username}}" autocomplete="username"
         autocapitalize="none" spellcheck="false" required autofocus>
  <label for="password">Password</label>
  <input id="password" name="password" type="password" required
         autocomplete="{{if eq .Mode "register"}}new-password{{else}}current-password{{end}}">
  {{if eq .Mode "register"}}
  <label for="confirm">Confirm password</label>
  <input id="confirm" name="confirm" type="password" required autocomplete="new-password">
  {{end}}
  <button type="submit">{{if eq .Mode "register"}}Create account{{else}}Sign in{{end}}</button>
</form>
{{if .SSO}}<div class="auth-or">or</div><a class="auth-sso" href="/auth/sso">Continue with single sign-on</a>{{end}}
{{if eq .Mode "login"}}
<div class="auth-or">or use a keyfile</div>
<p class="auth-note" hidden data-keyfile-unsupported>This browser cannot use keyfiles (it has no WebCrypto).</p>
<form id="keyfile_login" data-keyfile-panel>
  <p id="keyfile_login_status"></p>
  <label for="keyfile_file">Keyfile</label>
  <input id="keyfile_file" type="file" accept="application/json,.json" required>
  <label for="keyfile_pass">Keyfile passphrase</label>
  <input id="keyfile_pass" type="password" autocomplete="off" required>
  <button type="submit">Sign in with keyfile</button>
  <p class="auth-note">Uses the username above. The file never leaves this browser —
    it only signs a one-time challenge.</p>
</form>
{{end}}
<script src="/auth/keyfile.js" defer></script>
{{if eq .Mode "register"}}
<p class="auth-alt"><a href="/auth/login">I already have an account</a></p>
{{else if .Register}}
<p class="auth-alt"><a href="/auth/register">Create an account</a></p>
{{end}}
</div></div></body></html>
`))

// Describe reports how authentication is configured, for the startup banner. An
// operator needs to see which credentials are accepted and whether anyone may
// still register — the previous banner said "OIDC (MN_AUTH=on)" regardless.
func (a *Auth) Describe() []string {
	var out []string
	switch {
	case a.LocalEnabled() && a.SSOEnabled():
		out = append(out, "local accounts + single sign-on enabled (MN_AUTH=local+oidc)")
	case a.LocalEnabled():
		out = append(out, "local password accounts enabled (MN_AUTH=local)")
	default:
		out = append(out, "single sign-on enabled (MN_AUTH=on)")
	}
	if a.LocalEnabled() {
		out = append(out, "accounts: "+a.accounts.path+fmt.Sprintf(" (%d registered)", a.accounts.Count()))
		if registrationOpen() {
			out = append(out, "self-registration is OPEN - anyone who can reach this relay may create an account (MN_AUTH_REGISTER=off to close)")
		} else {
			out = append(out, "self-registration is closed (MN_AUTH_REGISTER=off)")
		}
	}
	if !a.secure {
		out = append(out, "session cookies are NOT marked Secure (MN_AUTH_INSECURE_COOKIES=1) - use this only for local testing")
	}
	return out
}

// accountPage is the signed-in account page's data.
type accountPage struct {
	Username string
	Account  string
	CSRF     string
	Error    string
	Changed  bool
	Keys     []keyRow
}

// keyRow is one enrolled keyfile as the account page shows it: never the key
// material, only what a user needs to recognise and revoke it.
type keyRow struct {
	ID    string
	Label string
	Added string
}

func keyRows(a Account) []keyRow {
	out := make([]keyRow, 0, len(a.PubKeys))
	for _, k := range a.PubKeys {
		out = append(out, keyRow{ID: k.ID, Label: k.Label, Added: k.AddedAt.UTC().Format("2006-01-02 15:04 UTC")})
	}
	return out
}

func (a *Auth) renderAccount(w http.ResponseWriter, code int, p accountPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = accountTmpl.Execute(w, p)
}

var accountTmpl = template.Must(template.New("account").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Your account · moony</title>
<link rel="icon" href="/favicon.ico">
<link rel="stylesheet" href="/static/app.css">
<style>
  .auth-wrap{min-height:100vh;display:grid;place-items:center;padding:1.5rem}
  .auth-card{width:min(25rem,100%);background:var(--c-surface,#fff);color:var(--c-text,#111);
    border:1px solid var(--c-border,#e3e3e3);border-radius:var(--radius-lg,14px);
    padding:1.5rem;box-shadow:0 10px 30px rgb(0 0 0 / 8%)}
  .auth-card h1{margin:0 0 .25rem;font-size:1.25rem}
  .auth-card p.sub{margin:0 0 1.25rem;color:var(--c-text-dim,#666);font-size:.875rem}
  .auth-card label{display:block;font-size:.8125rem;margin:.75rem 0 .25rem;font-weight:600}
  .auth-card input{width:100%;box-sizing:border-box;padding:.5rem .625rem;font:inherit;
    border:1px solid var(--c-border,#ccc);border-radius:var(--radius-sm,8px);
    background:var(--c-bg,#fff);color:inherit}
  .auth-card button{width:100%;margin-top:1.25rem;padding:.5rem;font:inherit;font-weight:600;
    border:0;border-radius:var(--radius-sm,8px);background:var(--c-accent,#3b6ef5);color:#fff;cursor:pointer}
  .auth-card button.danger{background:transparent;color:var(--c-danger,#d33);
    border:1px solid color-mix(in srgb,var(--c-danger,#d33) 40%,transparent)}
  .auth-note{margin:.5rem 0 0;font-size:.75rem;color:var(--c-text-dim,#777)}
  .auth-ok,.auth-err{margin:0 0 1rem;padding:.5rem .625rem;border-radius:var(--radius-sm,8px);font-size:.8125rem}
  .auth-ok{background:color-mix(in srgb,var(--c-accent,#3b6ef5) 12%,transparent)}
  .auth-err{background:color-mix(in srgb,var(--c-danger,#d33) 12%,transparent)}
  hr{border:0;border-top:1px solid var(--c-border,#e3e3e3);margin:1.5rem 0}
  .auth-alt{margin:1rem 0 0;text-align:center;font-size:.8125rem}
</style>
</head><body data-account="{{.Account}}"><div class="auth-wrap"><div class="auth-card">
<h1>Your account</h1>
<p class="sub">Signed in as {{.Username}}</p>
{{if .Changed}}<p class="auth-ok">Password changed. Every other session and connector token has been signed out.</p>{{end}}
{{if .Error}}<p class="auth-err">{{.Error}}</p>{{end}}
<form method="post" action="/auth/password">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <label for="old_password">Current password</label>
  <input id="old_password" name="old_password" type="password" required autocomplete="current-password">
  <label for="password">New password</label>
  <input id="password" name="password" type="password" required autocomplete="new-password">
  <label for="confirm">Confirm new password</label>
  <input id="confirm" name="confirm" type="password" required autocomplete="new-password">
  <button type="submit">Change password</button>
  <p class="auth-note">Changing your password signs out every other browser and revokes
    connector tokens, so any machine you have linked will need to be reconnected.</p>
</form>
<hr>
<h2 style="font-size:1rem;margin:0 0 .25rem">Keyfiles</h2>
<p class="auth-note" style="margin-bottom:.75rem">A keyfile is a second way to sign in:
  an encrypted key file plus its passphrase, both held only by you. If you lose either,
  that keyfile is gone for good — nobody can reset it, so keep a password you remember
  or enrol more than one.</p>
<p class="auth-note" hidden data-keyfile-unsupported>This browser cannot create keyfiles (it has no WebCrypto).</p>
<p id="keyfile_keys_status"></p>
{{if .Keys}}
<ul style="list-style:none;padding:0;margin:0 0 1rem;font-size:.8125rem">
  {{range .Keys}}
  <li style="display:flex;align-items:center;gap:.5rem;padding:.375rem 0;border-bottom:1px solid var(--c-border,#eee)">
    <span style="flex:1">{{if .Label}}{{.Label}}{{else}}(unlabelled){{end}}
      <span style="color:var(--c-text-dim,#888)">· added {{.Added}}</span></span>
    <button type="button" class="danger" style="width:auto;margin:0;padding:.25rem .5rem"
            data-key-id="{{.ID}}">Revoke</button>
  </li>
  {{end}}
</ul>
{{else}}
<p class="auth-note" style="margin-bottom:1rem">No keyfiles enrolled.</p>
{{end}}
<form id="keyfile_enrol" data-keyfile-panel>
  <p id="keyfile_enrol_status"></p>
  <label for="key_label">Label (optional)</label>
  <input id="key_label" maxlength="64" placeholder="work laptop">
  <label for="key_pass">Keyfile passphrase</label>
  <input id="key_pass" type="password" autocomplete="new-password" required>
  <label for="key_pass2">Confirm passphrase</label>
  <input id="key_pass2" type="password" autocomplete="new-password" required>
  <button type="submit">Create keyfile</button>
  <p class="auth-note">The key is generated in this browser; only its public half is
    uploaded. The download is encrypted with the passphrase above.</p>
</form>
<hr>
<form method="post" action="/auth/logout-all">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <button type="submit" class="danger">Sign out everywhere</button>
  <p class="auth-note">Revokes every session and connector token for this account, including this one.</p>
</form>
<p class="auth-alt"><a href="/">Back to moony</a></p>
<script src="/auth/keyfile.js" defer></script>
</div></div></body></html>
`))
