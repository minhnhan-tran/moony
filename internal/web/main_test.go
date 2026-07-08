package web

import (
	"os"
	"testing"
)

// TestMain makes the web package's tests hermetic against the developer's
// environment. NewServer calls auth.FromEnv() at construction, which reads
// MOONY_AUTH (the auth gate switch) and the OIDC vars from the process
// environment. On a machine that runs the relay with auth on (MOONY_AUTH=1 plus
// OIDC config exported in the shell), that ambient config would leak into every
// test server, gate all the API routes, and make session-creating tests fail
// with 401s. Clearing the auth env group here sets a known "auth off" baseline;
// tests that specifically exercise the gate (e.g. TestServerGatesWhenAuthEnabled)
// opt back in with t.Setenv, which overrides this baseline for their duration.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"MOONY_AUTH",
		"MOONY_AUTH_SECRET",
		"MOONY_AUTH_TTL",
		"MOONY_OIDC_CLIENT_ID",
		"MOONY_OIDC_CLIENT_SECRET",
		"MOONY_OIDC_REDIRECT_URL",
		"MOONY_OIDC_ENV",
	} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
