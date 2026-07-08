package web

import (
	"os"
	"testing"
)

// TestMain makes the web package's tests hermetic against the developer's
// environment. NewServer calls auth.FromEnv() at construction, which reads
// SQE_AUTH (the AppleConnect gate switch) and the OIDC vars from the process
// environment. On a machine that runs the relay with auth on (SQE_AUTH=1 plus
// OIDC config exported in the shell), that ambient config would leak into every
// test server, gate all the API routes, and make session-creating tests fail
// with 401s. Clearing the auth env group here sets a known "auth off" baseline;
// tests that specifically exercise the gate (e.g. TestServerGatesWhenAuthEnabled)
// opt back in with t.Setenv, which overrides this baseline for their duration.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"SQE_AUTH",
		"SQE_AUTH_SECRET",
		"SQE_AUTH_TTL",
		"SQE_OIDC_CLIENT_ID",
		"SQE_OIDC_CLIENT_SECRET",
		"SQE_OIDC_REDIRECT_URL",
		"SQE_OIDC_ENV",
	} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
