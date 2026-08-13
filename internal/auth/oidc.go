package auth

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Auth holds the resolved OIDC + signing configuration. Construct it with
// FromEnv; a nil *Auth means auth is disabled and callers must not wire it.
type Auth struct {
	secret   []byte
	ttl      time.Duration // browser session cookie lifetime
	connTTL  time.Duration // connector-token lifetime (long-lived; machines stay linked)
	claim    string        // ID-token claim used as the account (e.g. "email")
	admins   map[string]bool
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	secure   bool             // set the session cookie Secure (see cookieSecure)
	clock    func() time.Time // nil ⇒ time.Now; overridable in tests

	// accounts is the local account store, nil on an OIDC-only relay. It also
	// supplies each local account's token epoch, so it is consulted on every
	// Verify of a local token.
	accounts *AccountStore

	// limiter throttles password attempts per account and per client address.
	// Reached through throttler(), which creates it on demand: an Auth built
	// directly (tests, or a future caller that skips FromEnv) must not end up with
	// throttling quietly switched off.
	limiter     *loginLimiter
	limiterOnce sync.Once
}

// throttler returns the login limiter, creating it if necessary.
func (a *Auth) throttler() *loginLimiter {
	a.limiterOnce.Do(func() {
		if a.limiter == nil {
			a.limiter = newLoginLimiter(a.clock)
		}
	})
	return a.limiter
}

// defaults for optional knobs.
const (
	defaultSessionTTL   = 12 * time.Hour
	defaultConnectorTTL = 30 * 24 * time.Hour
	defaultAccountClaim = "email"
)

// cookieSecure decides whether the session cookie carries Secure.
//
// This used to be inferred from the OIDC redirect URL alone, so any configuration
// without an https redirect URL silently produced a cookie sent in clear. That
// cookie is a credential for shell access on a connector's machine, so the default
// is now "secure, or refuse to start"; plain HTTP has to be asked for.
func cookieSecure(redirectURL string) (bool, error) {
	if truthyEnv("MN_AUTH_COOKIE_SECURE") {
		return true, nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(redirectURL)), "https://") {
		return true, nil
	}
	if tlsHint || strings.TrimSpace(os.Getenv("MN_RELAY_TLS_CERT")) != "" {
		return true, nil // the relay terminates TLS itself (flag via TLSHint, or env)
	}
	if truthyEnv("MN_AUTH_INSECURE_COOKIES") {
		return false, nil
	}
	return false, fmt.Errorf("cannot tell whether the relay is served over HTTPS, so the session cookie would be sent in clear: set MN_AUTH_COOKIE_SECURE=1 (recommended, and required when a proxy such as Caddy terminates TLS) or MN_AUTH_INSECURE_COOKIES=1 to accept plain HTTP")
}

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// authMode is which credentials the relay accepts. It is a bitmask so
// "local+oidc" is expressible without a third enum value.
type authMode uint8

const (
	modeOff   authMode = 0
	modeOIDC  authMode = 1 << iota // browser login through an identity provider
	modeLocal                      // username + password accounts held by the relay
)

func (m authMode) String() string {
	switch m {
	case modeOff:
		return "off"
	case modeOIDC:
		return "oidc"
	case modeLocal:
		return "local"
	case modeLocal | modeOIDC:
		return "local+oidc"
	}
	return "invalid"
}

// authModeFromEnv parses MN_AUTH. An unrecognised value is an error rather than
// "off": cmd/relay installs the gate, the owner resolver and the connector-token
// check only when auth is enabled, so quietly reading a typo — or a mode name from
// a newer build — as "off" serves the relay to anyone.
func authModeFromEnv() (authMode, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("MN_AUTH")))
	switch raw {
	case "", "off", "0", "false", "no":
		return modeOff, nil
	}
	var mode authMode
	// Split without discarding empty segments: "local+" almost certainly means the
	// operator's "local+oidc" was truncated, and quietly reading it as plain
	// "local" would hand them less than they asked for.
	for _, part := range strings.Split(strings.ReplaceAll(raw, ",", "+"), "+") {
		switch strings.TrimSpace(part) {
		case "on", "1", "true", "yes", "oidc":
			mode |= modeOIDC
		case "local":
			mode |= modeLocal
		default:
			return modeOff, fmt.Errorf("MN_AUTH=%q is not a recognised value (use off, on, local, or local+oidc); refusing to start with authentication silently disabled", raw)
		}
	}
	if mode == modeOff {
		// e.g. "+" or ",," — parsed to nothing, which must not read as "off".
		return modeOff, fmt.Errorf("MN_AUTH=%q is not a recognised value (use off, on, local, or local+oidc)", raw)
	}
	return mode, nil
}

// LocalEnabled reports whether username/password accounts are accepted.
func (a *Auth) LocalEnabled() bool { return a != nil && a.accounts != nil }

// SSOEnabled reports whether an identity provider is configured, so the login page
// knows whether to offer the SSO button.
func (a *Auth) SSOEnabled() bool { return a != nil && a.oauth != nil }

// accountStorePath decides where local accounts live. It is deliberately explicit:
// an implicit path inside a container would lose every account on redeploy and let
// the freed names be registered by anyone.
func accountStorePath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MN_AUTH_USERS_FILE")); p != "" {
		return p, nil
	}
	if ann := strings.TrimSpace(os.Getenv("MN_ANNOUNCEMENTS_FILE")); ann != "" {
		return filepath.Join(filepath.Dir(ann), "users.json"), nil
	}
	return "", fmt.Errorf("MN_AUTH=local needs somewhere durable to keep accounts: set MN_AUTH_USERS_FILE (a path on a mounted volume), or MN_ANNOUNCEMENTS_FILE to default beside it")
}

// FromEnv builds an Auth from the environment. It returns (nil, false, nil) when
// MN_AUTH is unset/off — the relay then runs open, exactly as before. When
// MN_AUTH is on it requires the full OIDC + secret set and performs OIDC
// discovery against the issuer; a missing var or unreachable issuer is a fatal
// error (fail closed — never start half-secured).
func FromEnv(ctx context.Context) (*Auth, bool, error) {
	mode, err := authModeFromEnv()
	if err != nil {
		return nil, false, err
	}
	if mode == modeOff {
		return nil, false, nil
	}
	wantOIDC := mode&modeOIDC != 0
	wantLocal := mode&modeLocal != 0
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	secret := get("MN_AUTH_SECRET")
	issuer := get("MN_OIDC_ISSUER")
	clientID := get("MN_OIDC_CLIENT_ID")
	clientSecret := get("MN_OIDC_CLIENT_SECRET")
	redirect := get("MN_OIDC_REDIRECT_URL")

	required := map[string]string{"MN_AUTH_SECRET": secret}
	if wantOIDC {
		required["MN_OIDC_ISSUER"] = issuer
		required["MN_OIDC_CLIENT_ID"] = clientID
		required["MN_OIDC_CLIENT_SECRET"] = clientSecret
		required["MN_OIDC_REDIRECT_URL"] = redirect
	}
	var missing []string
	for k, v := range required {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing) // stable message, and MN_OIDC_ISSUER is easy to spot
		return nil, true, fmt.Errorf("MN_AUTH=%s but missing required var(s): %s", mode, strings.Join(missing, ", "))
	}
	if len(secret) < 16 {
		return nil, true, fmt.Errorf("MN_AUTH_SECRET must be at least 16 bytes")
	}

	secure, err := cookieSecure(redirect)
	if err != nil {
		// enabled=true: auth WAS requested. Reporting false here alongside an error
		// would let a caller that branches on it run open on exactly the
		// misconfiguration this check exists to catch.
		return nil, true, err
	}

	// Load the account store before discovery: it is local, and a corrupt store is
	// fatal, so there is no reason to wait on the network to find out.
	var accounts *AccountStore
	if wantLocal {
		path, err := accountStorePath()
		if err != nil {
			return nil, true, err
		}
		if accounts, err = OpenAccounts(path); err != nil {
			return nil, true, err
		}
	}

	// Discovery is a network round-trip, so every purely local check belongs above
	// it: an operator should not wait for their IdP to answer to be told a variable
	// is wrong.
	var provider *oidc.Provider
	if wantOIDC {
		if provider, err = oidc.NewProvider(ctx, issuer); err != nil {
			return nil, true, fmt.Errorf("OIDC discovery for issuer %q: %w", issuer, err)
		}
	}

	ttl := defaultSessionTTL
	if v := get("MN_AUTH_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ttl = d
		} else {
			return nil, true, fmt.Errorf("MN_AUTH_TTL %q is not a valid duration (e.g. 12h)", v)
		}
	}
	claim := get("MN_OIDC_ACCOUNT_CLAIM")
	if claim == "" {
		claim = defaultAccountClaim
	}
	admins := map[string]bool{}
	for _, a := range strings.Split(get("MN_AUTH_ADMINS"), ",") {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			admins[a] = true
		}
	}

	a := &Auth{
		secret:   []byte(secret),
		ttl:      ttl,
		connTTL:  defaultConnectorTTL,
		claim:    claim,
		admins:   admins,
		secure:   secure,
		accounts: accounts,
		limiter:  newLoginLimiter(nil),
	}
	// Only wire the OIDC halves when an identity provider was configured; in local
	// mode there is none, and provider.Verifier would nil-dereference.
	if provider != nil {
		a.provider = provider
		a.verifier = provider.Verifier(&oidc.Config{ClientID: clientID})
		a.oauth = &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirect,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		}
	}
	return a, true, nil
}

// accountFromClaims extracts the configured account claim (default "email") from
// an ID token's claim set, falling back to "sub". Returns "" if neither is set.
func (a *Auth) accountFromClaims(claims map[string]any) string {
	// usable rejects a claim value that would collide with the local account
	// namespace: "local:alice" from an identity provider would resolve to local
	// user alice's store entry — and therefore to her connectors, i.e. shell access
	// on her machine. Claims like preferred_username are often user-editable, so
	// this cannot be left to the IdP. NUL is refused for the same reason as in
	// Verify.
	usable := func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		if v == "" || strings.ContainsRune(v, 0) {
			return "", false
		}
		if strings.HasPrefix(strings.ToLower(v), localPrefix) {
			return "", false
		}
		return v, true
	}
	if v, ok := claims[a.claim].(string); ok {
		if acct, good := usable(v); good {
			return acct
		}
	}
	if v, ok := claims["sub"].(string); ok {
		if acct, good := usable(v); good {
			return acct
		}
	}
	return ""
}

// sameHostRedirect guards the post-login redirect target against open-redirect
// abuse: only a root-relative path ("/...") is allowed, else "/".
func sameHostRedirect(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	// This value is embedded in the signed login-transaction blob, and "|" and NUL
	// are the separators the token and blob formats are parsed on. url.Parse accepts
	// both in a path, so refuse them here: a return path must never be able to shape
	// the structure of something we sign.
	if strings.ContainsAny(p, "|\x00") {
		return "/"
	}
	return p
}
