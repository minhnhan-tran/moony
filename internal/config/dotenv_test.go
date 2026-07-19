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
		{`MN_AUTH_SECRET=abc=def`, "MN_AUTH_SECRET", "abc=def", true}, // value may contain '='
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
		"MN_OIDC_CLIENT_ID=from-file\n"+
			"MN_AUTH_SECRET=file-secret\n"+
			"# a comment\n"+
			"export MN_OIDC_ENV=uat\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MN_ENV_FILE", envPath)
	// The "loads from file" cases must start UNSET so the file value applies.
	// On a machine that exports these (e.g. a relay's real OIDC config) they'd
	// otherwise win, since LoadDotEnv never overrides an already-set var. Unset
	// them for the test and restore afterward so the test is hermetic. (t.Setenv
	// can only set, not unset, so we manage these directly.)
	for _, k := range []string{"MN_OIDC_CLIENT_ID", "MN_OIDC_ENV"} {
		if old, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, old) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}
	// A value already in the real environment must win over the file (Whisper/prod).
	t.Setenv("MN_AUTH_SECRET", "real-env-wins")

	LoadDotEnv()

	if got := os.Getenv("MN_OIDC_CLIENT_ID"); got != "from-file" {
		t.Errorf("unset var should load from file: got %q", got)
	}
	if got := os.Getenv("MN_OIDC_ENV"); got != "uat" {
		t.Errorf("export-prefixed var should load: got %q", got)
	}
	if got := os.Getenv("MN_AUTH_SECRET"); got != "real-env-wins" {
		t.Errorf("real env must NOT be overridden by .env: got %q", got)
	}
}

func TestLoadDotEnvMissingFileIsNoop(t *testing.T) {
	t.Setenv("MN_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("HOME", t.TempDir()) // no ~/.config/.../.env
	// Run from a dir with no ./.env.
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(t.TempDir())
	LoadDotEnv() // must not panic or error
}

// TestUpsertDotEnv: new keys are appended (sorted), existing keys are updated in
// place, empty-valued keys are deleted, and unrelated lines/comments survive.
func TestUpsertDotEnv(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	t.Setenv("MN_ENV_FILE", envPath)
	if err := os.WriteFile(envPath, []byte(
		"# my secrets\n"+
			"MN_OPENAI_API_KEY=old-key\n"+
			"MN_OTHER=keepme\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UpsertDotEnv(map[string]string{
		"MN_OPENAI_API_KEY":    "new-key", // update in place
		"MN_ANTHROPIC_API_KEY": "sk-ant",  // append
		"MN_OPENAI_BASE_URL":   "",        // empty → not written / deleted
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// Parse the result back to assert on effective values.
	kv := map[string]string{}
	for _, line := range splitLines(got) {
		if k, v, ok := parseEnvLine(line); ok {
			kv[k] = v
		}
	}
	if kv["MN_OPENAI_API_KEY"] != "new-key" {
		t.Errorf("update failed: %q", kv["MN_OPENAI_API_KEY"])
	}
	if kv["MN_ANTHROPIC_API_KEY"] != "sk-ant" {
		t.Errorf("append failed: %q", kv["MN_ANTHROPIC_API_KEY"])
	}
	if kv["MN_OTHER"] != "keepme" {
		t.Errorf("unrelated key not preserved: %q", kv["MN_OTHER"])
	}
	if _, ok := kv["MN_OPENAI_BASE_URL"]; ok {
		t.Errorf("empty-valued key should not be written")
	}
	if !containsLine(got, "# my secrets") {
		t.Errorf("comment not preserved:\n%s", got)
	}

	// Deleting an existing key removes its line.
	if err := UpsertDotEnv(map[string]string{"MN_ANTHROPIC_API_KEY": ""}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(envPath)
	if containsLine(string(data), "MN_ANTHROPIC_API_KEY=sk-ant") {
		t.Errorf("delete failed, key still present:\n%s", string(data))
	}

	// Empty upsert map is a no-op (no error).
	if err := UpsertDotEnv(nil); err != nil {
		t.Errorf("nil upsert should be a no-op: %v", err)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func containsLine(content, want string) bool {
	for _, line := range splitLines(content) {
		if line == want {
			return true
		}
	}
	return false
}
