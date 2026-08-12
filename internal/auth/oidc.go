package auth

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
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

// authEnabled reports whether authentication is switched on. An unrecognised value
// is an error rather than "off": cmd/relay installs the gate, the owner resolver
// and the connector-token check only when this returns true, so quietly reading a
// typo — or a mode name from a newer build — as "off" serves the relay to anyone.
func authEnabled() (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("MN_AUTH"))); v {
	case "", "off", "0", "false", "no":
		return false, nil
	case "on", "1", "true", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("MN_AUTH=%q is not a recognised value (use on or off); refusing to start with authentication silently disabled", v)
	}
}

// FromEnv builds an Auth from the environment. It returns (nil, false, nil) when
// MN_AUTH is unset/off — the relay then runs open, exactly as before. When
// MN_AUTH is on it requires the full OIDC + secret set and performs OIDC
// discovery against the issuer; a missing var or unreachable issuer is a fatal
// error (fail closed — never start half-secured).
func FromEnv(ctx context.Context) (*Auth, bool, error) {
	on, err := authEnabled()
	if err != nil {
		return nil, false, err
	}
	if !on {
		return nil, false, nil
	}
	get := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	secret := get("MN_AUTH_SECRET")
	issuer := get("MN_OIDC_ISSUER")
	clientID := get("MN_OIDC_CLIENT_ID")
	clientSecret := get("MN_OIDC_CLIENT_SECRET")
	redirect := get("MN_OIDC_REDIRECT_URL")

	var missing []string
	for k, v := range map[string]string{
		"MN_AUTH_SECRET":        secret,
		"MN_OIDC_ISSUER":        issuer,
		"MN_OIDC_CLIENT_ID":     clientID,
		"MN_OIDC_CLIENT_SECRET": clientSecret,
		"MN_OIDC_REDIRECT_URL":  redirect,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, true, fmt.Errorf("MN_AUTH=on but missing required var(s): %s", strings.Join(missing, ", "))
	}
	if len(secret) < 16 {
		return nil, true, fmt.Errorf("MN_AUTH_SECRET must be at least 16 bytes")
	}

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, true, fmt.Errorf("OIDC discovery for issuer %q: %w", issuer, err)
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
	secure, err := cookieSecure(redirect)
	if err != nil {
		// enabled=true: auth WAS requested. Reporting false here alongside an error
		// would let a caller that branches on it run open on exactly the
		// misconfiguration this check exists to catch.
		return nil, true, err
	}

	a := &Auth{
		secret:   []byte(secret),
		ttl:      ttl,
		connTTL:  defaultConnectorTTL,
		claim:    claim,
		admins:   admins,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		oauth: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirect,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		secure: secure,
	}
	return a, true, nil
}

// accountFromClaims extracts the configured account claim (default "email") from
// an ID token's claim set, falling back to "sub". Returns "" if neither is set.
func (a *Auth) accountFromClaims(claims map[string]any) string {
	if v, ok := claims[a.claim].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := claims["sub"].(string); ok {
		return strings.TrimSpace(v)
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
	return p
}
