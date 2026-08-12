package auth

import (
	"context"
	"strings"
	"testing"
)

// An unrecognised MN_AUTH must be a startup error. Returning "off" instead means a
// typo (or a mode name from a newer build) silently serves the relay with no
// authentication at all: cmd/relay only installs the gate, the owner resolver and
// the connector-token check when auth reports enabled.
func TestUnrecognisedAuthModeIsFatal(t *testing.T) {
	for _, v := range []string{"local", "enabled", "yes please", "onn", "2", "on;local"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("MN_AUTH", v)
			_, on, err := FromEnv(context.Background())
			if err == nil {
				t.Fatalf("MN_AUTH=%q returned on=%v with no error; the relay would start unauthenticated", v, on)
			}
			if !strings.Contains(err.Error(), "MN_AUTH") {
				t.Errorf("error should name the offending variable, got %v", err)
			}
		})
	}
}

// The documented off switches stay silent and disabled.
func TestAuthOffValues(t *testing.T) {
	for _, v := range []string{"", "off", "0", "false", "no", "OFF", "  no  "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("MN_AUTH", v)
			a, on, err := FromEnv(context.Background())
			if err != nil {
				t.Fatalf("MN_AUTH=%q: unexpected error %v", v, err)
			}
			if on || a != nil {
				t.Errorf("MN_AUTH=%q: want auth disabled, got on=%v auth=%v", v, on, a)
			}
		})
	}
}

// "on" without the OIDC variables must still fail closed, as it does today.
func TestAuthOnWithoutOIDCIsFatal(t *testing.T) {
	t.Setenv("MN_AUTH", "on")
	t.Setenv("MN_OIDC_ISSUER", "")
	t.Setenv("MN_OIDC_CLIENT_ID", "")
	t.Setenv("MN_AUTH_SECRET", "")
	if _, _, err := FromEnv(context.Background()); err == nil {
		t.Error("MN_AUTH=on with no issuer/client/secret must be a startup error")
	}
}
