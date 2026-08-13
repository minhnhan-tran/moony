package auth

import (
	"path/filepath"
	"testing"
	"time"
)

// authWithStore builds a local-mode Auth over a fresh account store.
func authWithStore(t *testing.T) (*Auth, *AccountStore) {
	t.Helper()
	store, err := OpenAccounts(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := testAuth(time.Now())
	a.accounts = store
	return a, store
}

// The point of the per-account key: bumping an account's epoch must invalidate
// tokens already issued, in BOTH audiences. The connector token lives 30 days, so
// without this a stolen tunnel credential outlives any password change.
func TestEpochBumpInvalidatesLiveTokens(t *testing.T) {
	a, store := authWithStore(t)
	acct, err := store.Create("alice", "hash")
	if err != nil {
		t.Fatal(err)
	}

	web := a.Sign(acct.Account, audWeb, time.Hour)
	conn := a.Sign(acct.Account, audConnector, 30*24*time.Hour)
	if got, ok := a.Verify(web, audWeb); !ok || got != acct.Account {
		t.Fatalf("fresh web token did not verify: %q ok=%v", got, ok)
	}
	if _, ok := a.Verify(conn, audConnector); !ok {
		t.Fatal("fresh connector token did not verify")
	}

	if _, err := store.BumpEpoch(acct.Account); err != nil {
		t.Fatal(err)
	}

	if _, ok := a.Verify(web, audWeb); ok {
		t.Error("the web session survived an epoch bump")
	}
	if _, ok := a.Verify(conn, audConnector); ok {
		t.Error("the 30-day connector token survived an epoch bump")
	}

	// And a token minted after the bump works, so the bump is not a lockout.
	if _, ok := a.Verify(a.Sign(acct.Account, audWeb, time.Hour), audWeb); !ok {
		t.Error("a token minted after the bump does not verify")
	}
}

// One account's key must not verify another's token, or an epoch bump on one
// account would be meaningless while the other's key still works.
func TestTokenKeysArePerAccount(t *testing.T) {
	a, store := authWithStore(t)
	alice, _ := store.Create("alice", "h")
	bob, _ := store.Create("bob", "h")

	tok := a.Sign(alice.Account, audWeb, time.Hour)
	if got, ok := a.Verify(tok, audWeb); !ok || got != alice.Account {
		t.Fatalf("alice's token did not verify: %q %v", got, ok)
	}

	// Bumping bob must not touch alice.
	if _, err := store.BumpEpoch(bob.Account); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Verify(tok, audWeb); !ok {
		t.Error("bumping bob's epoch invalidated alice's token")
	}
}

// OIDC accounts have no store entry and keep signing with the bare secret, so
// enabling local accounts does not log every existing SSO user out.
func TestOIDCTokensAreUnaffectedByTheStore(t *testing.T) {
	a, store := authWithStore(t)
	const oidcAccount = "alice@example.com"

	tok := a.Sign(oidcAccount, audWeb, time.Hour)
	if got, ok := a.Verify(tok, audWeb); !ok || got != oidcAccount {
		t.Fatalf("OIDC token did not verify: %q %v", got, ok)
	}

	// A local account of a similar name, bumped, must not disturb it.
	local, _ := store.Create("alice", "h")
	if _, err := store.BumpEpoch(local.Account); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Verify(tok, audWeb); !ok {
		t.Error("an OIDC session was invalidated by an unrelated local account")
	}
}

// A local-looking token whose account is not in the store must fail. Otherwise a
// deleted account's token keeps working, and the account id is attacker-chosen.
func TestUnknownLocalAccountFailsClosed(t *testing.T) {
	a, _ := authWithStore(t)
	tok := a.Sign("local:ghost", audWeb, time.Hour)
	if got, ok := a.Verify(tok, audWeb); ok {
		t.Errorf("token for an account that does not exist verified as %q", got)
	}
}

// A disabled account's tokens must stop working immediately, without waiting for
// the operator to also bump the epoch.
func TestDisabledAccountTokensFail(t *testing.T) {
	a, store := authWithStore(t)
	acct, _ := store.Create("alice", "h")
	tok := a.Sign(acct.Account, audWeb, time.Hour)

	if err := store.Update(acct.Account, func(x *Account) error {
		x.Disabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Verify(tok, audWeb); ok {
		t.Error("a disabled account's token still verifies")
	}
}

// With no store configured (OIDC-only relay) nothing changes: a local-prefixed
// account cannot be minted anyway, and every other account signs with the secret.
func TestNoStoreKeepsBareSecret(t *testing.T) {
	a := testAuth(time.Now())
	tok := a.Sign("alice@example.com", audWeb, time.Hour)
	if _, ok := a.Verify(tok, audWeb); !ok {
		t.Error("without a store, an OIDC token must still verify")
	}
	// A local id with no store behind it cannot be trusted.
	if _, ok := a.Verify(a.Sign("local:alice", audWeb, time.Hour), audWeb); ok {
		t.Error("a local token verified with no account store to check it against")
	}
}
