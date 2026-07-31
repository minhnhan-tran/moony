package web

import (
	"os"
	"testing"
)

// TestMain makes the web package's tests hermetic against the developer's
// environment. NewServer calls auth.FromEnv() at construction, which reads
// MN_AUTH (the auth gate switch) and the OIDC vars from the process
// environment. On a machine that runs the relay with auth on (MN_AUTH=1 plus
// OIDC config exported in the shell), that ambient config would leak into every
// test server, gate all the API routes, and make session-creating tests fail
// with 401s. Clearing the auth env group here sets a known "auth off" baseline;
// tests that specifically exercise the gate (e.g. TestServerGatesWhenAuthEnabled)
// opt back in with t.Setenv, which overrides this baseline for their duration.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"MN_AUTH",
		"MN_AUTH_SECRET",
		"MN_AUTH_TTL",
		"MN_OIDC_CLIENT_ID",
		"MN_OIDC_CLIENT_SECRET",
		"MN_OIDC_REDIRECT_URL",
		"MN_OIDC_ENV",
	} {
		_ = os.Unsetenv(k)
	}
	// Never spawn real MCP subprocesses from this package's tests. newTestServer
	// sets this per-test, but t.Setenv only covers tests that go through it — and
	// only until that test ends, while a connect goroutine can still be in flight.
	// A real server (e.g. an npx-based one) then downloads packages into the
	// test's temp HOME and keeps writing there after the test returns, which makes
	// t.TempDir cleanup fail ("directory not empty") in whichever test happens to
	// be running. Pinning it for the whole binary removes that cross-test flake.
	// A test that genuinely wants MCP can still opt in with t.Setenv("MN_MCP","1").
	_ = os.Setenv("MN_MCP", "0")
	os.Exit(m.Run())
}
