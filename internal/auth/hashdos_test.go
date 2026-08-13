package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// argon2id at 64MiB per attempt is a memory amplifier: a handful of concurrent
// POSTs to a public form can outweigh the whole relay's normal footprint. Attempts
// must therefore be capped in flight, not merely counted after the fact.
func TestPasswordHashingIsBoundedInFlight(t *testing.T) {
	if maxConcurrentHashes <= 0 {
		t.Fatal("maxConcurrentHashes must be positive")
	}
	if maxConcurrentHashes > 8 {
		t.Errorf("maxConcurrentHashes = %d: at %d MiB each that is %d MiB of resident memory an unauthenticated caller can demand",
			maxConcurrentHashes, defaultArgonParams.memory/1024,
			int(defaultArgonParams.memory/1024)*maxConcurrentHashes)
	}

	a, _ := localAuth(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var peak, cur int

	// Drive many logins at once and watch the gate's own counter.
	hashGateProbe = func(delta int) {
		mu.Lock()
		cur += delta
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
	}
	t.Cleanup(func() { hashGateProbe = nil })

	for i := 0; i < maxConcurrentHashes*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.checkPassword("alice", "wrong-password-entirely")
		}()
	}
	wg.Wait()

	if peak > maxConcurrentHashes {
		t.Errorf("%d hashes ran concurrently, limit is %d", peak, maxConcurrentHashes)
	}
	if peak == 0 {
		t.Error("the probe never fired; the gate is not on the hashing path")
	}
}

// Registration hashes too, so it needs the same throttling as login — otherwise the
// limiter on /auth/login is simply bypassed by posting to /auth/register.
func TestRegistrationIsThrottled(t *testing.T) {
	a, _ := localAuth(t)

	var last int
	for i := 0; i < maxLoginAttempts+3; i++ {
		token, csrf := loginForm(t, a, "/auth/register")
		rec := postForm(t, a, "/auth/register", url.Values{
			// Always invalid, so nothing is created and every attempt is a failure.
			"username": {"x"}, "password": {"another-good-password"},
			"confirm": {"another-good-password"}, csrfField: {token},
		}, csrf)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after %d bad registrations the status was %d, want 429", maxLoginAttempts+3, last)
	}
}

// A duplicate username must be rejected before the password is hashed: otherwise
// anyone can spend 64MiB per request by re-posting a name they know exists.
func TestDuplicateRegistrationDoesNotHash(t *testing.T) {
	a, _ := localAuth(t)
	hashes := 0
	hashGateProbe = func(delta int) {
		if delta > 0 {
			hashes++
		}
	}
	t.Cleanup(func() { hashGateProbe = nil })

	token, csrf := loginForm(t, a, "/auth/register")
	rec := postForm(t, a, "/auth/register", url.Values{
		"username": {"alice"}, // already registered
		"password": {"another-good-password"},
		"confirm":  {"another-good-password"}, csrfField: {token},
	}, csrf)
	if rec.Code == http.StatusFound {
		t.Fatal("the duplicate was accepted")
	}
	if hashes != 0 {
		t.Errorf("a known-duplicate registration performed %d hash(es); check the name first", hashes)
	}
}

// The throttle must be consulted before the expensive verification on the
// password-change route too, or a locked-out caller still costs a hash per request.
func TestPasswordChangeChecksTheLimitBeforeHashing(t *testing.T) {
	a, acct := localAuth(t)
	sess := &http.Cookie{Name: sessionCookie, Value: a.Sign(acct.Account, audWeb, a.ttl)}

	// Burn through the allowance with wrong current-password attempts.
	for i := 0; i < maxLoginAttempts+1; i++ {
		postForm(t, a, "/auth/password", url.Values{
			"old_password": {"not-the-old-password"},
			"password":     {"a-brand-new-password"},
			"confirm":      {"a-brand-new-password"},
			csrfField:      {a.csrfToken(sess.Value)},
		}, sess)
	}

	hashes := 0
	hashGateProbe = func(delta int) {
		if delta > 0 {
			hashes++
		}
	}
	t.Cleanup(func() { hashGateProbe = nil })

	rec := postForm(t, a, "/auth/password", url.Values{
		"old_password": {"not-the-old-password"},
		"password":     {"a-brand-new-password"},
		"confirm":      {"a-brand-new-password"},
		csrfField:      {a.csrfToken(sess.Value)},
	}, sess)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 once the limit is reached", rec.Code)
	}
	if hashes != 0 {
		t.Errorf("a throttled password change still performed %d hash(es)", hashes)
	}
}

// Behind a proxy every request shares one RemoteAddr, so an IP bucket as tight as
// the per-account one would let a single attacker lock out the whole relay. The IP
// allowance must therefore be looser than the per-account allowance.
func TestIPAllowanceIsLooserThanPerAccount(t *testing.T) {
	if maxLoginAttemptsPerIP <= maxLoginAttempts {
		t.Errorf("per-IP allowance (%d) must exceed the per-account allowance (%d): behind a "+
			"reverse proxy all clients share one address, so the tighter bucket would be a global lockout",
			maxLoginAttemptsPerIP, maxLoginAttempts)
	}
}

// A wrong password for one account must not lock a different account out.
func TestOneAccountsFailuresDoNotLockAnother(t *testing.T) {
	a, _ := localAuth(t)
	hash, err := HashPassword("bobs-own-good-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.accounts.Create("bob", hash); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < maxLoginAttempts+1; i++ {
		token, csrf := loginForm(t, a, "/auth/login")
		postForm(t, a, "/auth/login", url.Values{
			"username": {"alice"}, "password": {"wrong-password-entirely"}, csrfField: {token},
		}, csrf)
	}

	token, csrf := loginForm(t, a, "/auth/login")
	rec := postForm(t, a, "/auth/login", url.Values{
		"username": {"bob"}, "password": {"bobs-own-good-password"}, csrfField: {token},
	}, csrf)
	if rec.Code != http.StatusFound {
		t.Errorf("bob's login returned %d after alice was locked out; want 302", rec.Code)
	}
	if sessionCookieFrom(rec) == nil {
		t.Error("bob got no session: one account's failures locked another out")
	}
}

// The limiter's map must not grow without bound from junk usernames.
func TestLimiterMapIsBounded(t *testing.T) {
	l := newLoginLimiter(nil)
	for i := 0; i < 20000; i++ {
		l.fail("ip:198.51.100." + itoa(i))
	}
	l.mu.Lock()
	n := len(l.fails)
	l.mu.Unlock()
	if n > limiterMaxKeys {
		t.Errorf("limiter holds %d keys, cap is %d", n, limiterMaxKeys)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Sanity: the gate must not deadlock or leak permits under repeated use.
func TestHashGateReleasesPermits(t *testing.T) {
	for i := 0; i < maxConcurrentHashes*3; i++ {
		if _, err := HashPassword("a-perfectly-fine-password"); err != nil {
			t.Fatal(err)
		}
	}
	// If permits leaked, this would block forever and the test would time out.
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentHashes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = VerifyPassword(dummyHash, "whatever-this-is")
		}()
	}
	wg.Wait()
}

var _ = httptest.NewRequest // keep the import if the file is trimmed
