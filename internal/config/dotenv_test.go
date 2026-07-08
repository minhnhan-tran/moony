package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvLine(t *testing.T) {
	cases := []struct {
		line     string
		key, val string
		ok       bool
	}{
		{`FOO=bar`, "FOO", "bar", true},
		{`  export FOO=bar  `, "FOO", "bar", true},
		{`FOO="quoted value"`, "FOO", "quoted value", true},
		{`FOO='single'`, "FOO", "single", true},
		{`FOO=`, "FOO", "", true},
		{`# comment`, "", "", false},
		{``, "", "", false},
		{`novalue`, "", "", false},
		{`MOONY_AUTH_SECRET=abc=def`, "MOONY_AUTH_SECRET", "abc=def", true}, // value may contain '='
	}
	for _, c := range cases {
		k, v, ok := parseEnvLine(c.line)
		if ok != c.ok || k != c.key || v != c.val {
			t.Errorf("parseEnvLine(%q) = (%q,%q,%v), want (%q,%q,%v)", c.line, k, v, ok, c.key, c.val, c.ok)
		}
	}
}

func TestLoadDotEnvDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(envPath, []byte(
		"MOONY_OIDC_CLIENT_ID=from-file\n"+
			"MOONY_AUTH_SECRET=file-secret\n"+
			"# a comment\n"+
			"export MOONY_OIDC_ENV=uat\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOONY_ENV_FILE", envPath)
	// The "loads from file" cases must start UNSET so the file value applies.
	// On a machine that exports these (e.g. a relay's real OIDC config) they'd
	// otherwise win, since LoadDotEnv never overrides an already-set var. Unset
	// them for the test and restore afterward so the test is hermetic. (t.Setenv
	// can only set, not unset, so we manage these directly.)
	for _, k := range []string{"MOONY_OIDC_CLIENT_ID", "MOONY_OIDC_ENV"} {
		if old, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, old) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}
	// A value already in the real environment must win over the file (Whisper/prod).
	t.Setenv("MOONY_AUTH_SECRET", "real-env-wins")

	LoadDotEnv()

	if got := os.Getenv("MOONY_OIDC_CLIENT_ID"); got != "from-file" {
		t.Errorf("unset var should load from file: got %q", got)
	}
	if got := os.Getenv("MOONY_OIDC_ENV"); got != "uat" {
		t.Errorf("export-prefixed var should load: got %q", got)
	}
	if got := os.Getenv("MOONY_AUTH_SECRET"); got != "real-env-wins" {
		t.Errorf("real env must NOT be overridden by .env: got %q", got)
	}
}

func TestLoadDotEnvMissingFileIsNoop(t *testing.T) {
	t.Setenv("MOONY_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("HOME", t.TempDir()) // no ~/.config/.../.env
	// Run from a dir with no ./.env.
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(t.TempDir())
	LoadDotEnv() // must not panic or error
}
