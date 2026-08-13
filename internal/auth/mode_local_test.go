package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localModeEnv clears everything FromEnv reads, so each case states its own world.
func localModeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MN_AUTH", "MN_AUTH_SECRET", "MN_AUTH_TTL", "MN_AUTH_ADMINS",
		"MN_AUTH_USERS_FILE", "MN_ANNOUNCEMENTS_FILE", "MN_AUTH_COOKIE_SECURE",
		"MN_AUTH_INSECURE_COOKIES", "MN_RELAY_TLS_CERT",
		"MN_OIDC_ISSUER", "MN_OIDC_CLIENT_ID", "MN_OIDC_CLIENT_SECRET",
		"MN_OIDC_REDIRECT_URL", "MN_OIDC_ACCOUNT_CLAIM",
	} {
		t.Setenv(k, "")
	}
}

const testSecret = "0123456789abcdefghij"

// The whole point of local mode: a relay with accounts and no identity provider.
func TestLocalModeBootsWithoutOIDC(t *testing.T) {
	localModeEnv(t)
	dir := t.TempDir()
	t.Setenv("MN_AUTH", "local")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_AUTH_USERS_FILE", filepath.Join(dir, "users.json"))
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")

	a, on, err := FromEnv(context.Background())
	if err != nil {
		t.Fatalf("local mode should not need any OIDC variable: %v", err)
	}
	if !on || a == nil {
		t.Fatal("local mode must report auth enabled")
	}
	if a.accounts == nil {
		t.Error("local mode must wire an account store")
	}
	if a.provider != nil || a.oauth != nil {
		t.Error("local mode must not build an OIDC provider")
	}
	if !a.secure {
		t.Error("MN_AUTH_COOKIE_SECURE=1 was ignored")
	}
	if !a.LocalEnabled() {
		t.Error("LocalEnabled must report true in local mode")
	}
	if a.SSOEnabled() {
		t.Error("SSOEnabled must be false with no OIDC configured")
	}
}

// Accounts must live somewhere durable. An implicit temp path would lose every
// account on redeploy and let the names be registered again by anyone.
func TestLocalModeRequiresAUsersFile(t *testing.T) {
	localModeEnv(t)
	t.Setenv("MN_AUTH", "local")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")

	_, on, err := FromEnv(context.Background())
	if err == nil {
		t.Fatal("local mode with nowhere to store accounts must be a startup error")
	}
	if !strings.Contains(err.Error(), "MN_AUTH_USERS_FILE") {
		t.Errorf("the error should name the variable to set, got: %v", err)
	}
	if !on {
		t.Error("auth was requested, so enabled must stay true alongside the error")
	}
}

// Deployments already mount a volume for announcements; default beside it.
func TestUsersFileDefaultsBesideAnnouncements(t *testing.T) {
	localModeEnv(t)
	dir := t.TempDir()
	t.Setenv("MN_AUTH", "local")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_ANNOUNCEMENTS_FILE", filepath.Join(dir, "announcements.json"))
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")

	a, _, err := FromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.accounts.Create("alice", "h"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "users.json")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected the account file beside the announcements file at %s: %v", want, err)
	}
}

// A corrupt account file must stop the relay rather than presenting no accounts.
func TestLocalModeRefusesACorruptStore(t *testing.T) {
	localModeEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MN_AUTH", "local")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_AUTH_USERS_FILE", path)
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")

	if _, _, err := FromEnv(context.Background()); err == nil {
		t.Fatal("a corrupt account store must be fatal")
	}
}

// local mode still needs a secret, and still refuses an unknowable cookie scope.
func TestLocalModeStillRequiresSecretAndCookieDecision(t *testing.T) {
	dir := t.TempDir()

	t.Run("no secret", func(t *testing.T) {
		localModeEnv(t)
		t.Setenv("MN_AUTH", "local")
		t.Setenv("MN_AUTH_USERS_FILE", filepath.Join(dir, "u.json"))
		t.Setenv("MN_AUTH_COOKIE_SECURE", "1")
		if _, _, err := FromEnv(context.Background()); err == nil {
			t.Error("local mode without MN_AUTH_SECRET must fail")
		}
	})

	t.Run("short secret", func(t *testing.T) {
		localModeEnv(t)
		t.Setenv("MN_AUTH", "local")
		t.Setenv("MN_AUTH_SECRET", "tooshort")
		t.Setenv("MN_AUTH_USERS_FILE", filepath.Join(dir, "u.json"))
		t.Setenv("MN_AUTH_COOKIE_SECURE", "1")
		if _, _, err := FromEnv(context.Background()); err == nil {
			t.Error("a short secret must fail in local mode too")
		}
	})

	t.Run("undecidable cookie scope", func(t *testing.T) {
		localModeEnv(t)
		t.Setenv("MN_AUTH", "local")
		t.Setenv("MN_AUTH_SECRET", testSecret)
		t.Setenv("MN_AUTH_USERS_FILE", filepath.Join(dir, "u.json"))
		if _, _, err := FromEnv(context.Background()); err == nil {
			t.Error("local mode must apply the same cookie-Secure rule as OIDC mode")
		}
	})
}

// local+oidc means both, so the OIDC set is required.
func TestLocalPlusOIDCRequiresTheOIDCVars(t *testing.T) {
	localModeEnv(t)
	dir := t.TempDir()
	t.Setenv("MN_AUTH", "local+oidc")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_AUTH_USERS_FILE", filepath.Join(dir, "users.json"))
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")

	_, _, err := FromEnv(context.Background())
	if err == nil {
		t.Fatal("local+oidc without the OIDC variables must fail")
	}
	if !strings.Contains(err.Error(), "MN_OIDC_ISSUER") {
		t.Errorf("the error should list the missing OIDC vars, got: %v", err)
	}
}

// OIDC-only mode must not create an account store, so nothing new appears on disk
// for deployments that did not ask for local accounts.
func TestOIDCOnlyModeHasNoStore(t *testing.T) {
	localModeEnv(t)
	t.Setenv("MN_AUTH", "on")
	t.Setenv("MN_AUTH_SECRET", testSecret)
	t.Setenv("MN_AUTH_COOKIE_SECURE", "1")
	// Deliberately no OIDC vars: this fails at the missing-vars check, which is
	// enough to prove the store is not created before it.
	a, _, err := FromEnv(context.Background())
	if err == nil {
		t.Fatal("expected the missing-OIDC-vars error")
	}
	if a != nil {
		t.Error("no Auth should be returned when configuration is incomplete")
	}
}

func TestModeParsing(t *testing.T) {
	cases := map[string]struct {
		mode    authMode
		wantErr bool
	}{
		"":            {mode: modeOff},
		"off":         {mode: modeOff},
		"no":          {mode: modeOff},
		"on":          {mode: modeOIDC},
		"true":        {mode: modeOIDC},
		"oidc":        {mode: modeOIDC},
		"local":       {mode: modeLocal},
		"LOCAL":       {mode: modeLocal},
		"  local  ":   {mode: modeLocal},
		"local+oidc":  {mode: modeLocal | modeOIDC},
		"oidc+local":  {mode: modeLocal | modeOIDC},
		"local,oidc":  {mode: modeLocal | modeOIDC},
		"locl":        {wantErr: true},
		"local+":      {wantErr: true},
		"local+sso":   {wantErr: true},
		"on+local":    {mode: modeLocal | modeOIDC},
		"enabled":     {wantErr: true},
		"local oidc":  {wantErr: true},
		"local;;oidc": {wantErr: true},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Setenv("MN_AUTH", in)
			got, err := authModeFromEnv()
			if want.wantErr {
				if err == nil {
					t.Fatalf("MN_AUTH=%q parsed as %v; want an error", in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("MN_AUTH=%q: unexpected error %v", in, err)
			}
			if got != want.mode {
				t.Errorf("MN_AUTH=%q = %v, want %v", in, got, want.mode)
			}
		})
	}
}

// A session for an account that has since been disabled must stop working, and the
// gate must not let it through.
func TestSessionRejectsADisabledAccount(t *testing.T) {
	a, store := authWithStore(t)
	acct, err := store.Create("alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	token := a.Sign(acct.Account, audWeb, defaultSessionTTL)

	if _, ok := a.Verify(token, audWeb); !ok {
		t.Fatal("fresh session should verify")
	}
	if err := store.Update(acct.Account, func(x *Account) error {
		x.Disabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Verify(token, audWeb); ok {
		t.Error("a disabled account's session still verifies")
	}
}
