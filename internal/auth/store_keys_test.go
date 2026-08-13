package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func storeWithAccount(t *testing.T, username string) (*AccountStore, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := OpenAccounts(path)
	if err != nil {
		t.Fatalf("OpenAccounts: %v", err)
	}
	a, err := s.Create(username, "$argon2id$fake")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s, a.Account, path
}

func TestAddPubKeyRoundTripsThroughReload(t *testing.T) {
	s, id, path := storeWithAccount(t, "alice")
	_, spki := testKey(t)

	key, err := s.AddPubKey(id, spki, "laptop")
	if err != nil {
		t.Fatalf("AddPubKey: %v", err)
	}
	if key.ID == "" || key.Alg != keyAlgP256 || key.Key != spki || key.Label != "laptop" {
		t.Fatalf("unexpected stored key: %+v", key)
	}
	if key.AddedAt.IsZero() {
		t.Fatal("AddedAt was not set")
	}

	reopened, err := OpenAccounts(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.ByID(id)
	if !ok || len(got.PubKeys) != 1 || got.PubKeys[0].ID != key.ID || got.PubKeys[0].Key != spki {
		t.Fatalf("key did not survive reload: %+v", got.PubKeys)
	}
}

func TestAddPubKeyRejectsDuplicateKeyMaterial(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	_, spki := testKey(t)
	if _, err := s.AddPubKey(id, spki, "one"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Sloppy base64 of the same key must still be caught: dedup compares the
	// canonical form, not the submitted string.
	if _, err := s.AddPubKey(id, " "+spki+"\n", "two"); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("want ErrDuplicateKey for the same key, got %v", err)
	}
	got, _ := s.ByID(id)
	if len(got.PubKeys) != 1 {
		t.Fatalf("duplicate was stored anyway: %d keys", len(got.PubKeys))
	}
}

func TestAddPubKeyEnforcesTheCap(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	for i := 0; i < maxKeysPerAccount; i++ {
		_, spki := testKey(t)
		if _, err := s.AddPubKey(id, spki, "k"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	_, spki := testKey(t)
	if _, err := s.AddPubKey(id, spki, "over"); !errors.Is(err, ErrKeyLimit) {
		t.Fatalf("want ErrKeyLimit past the cap, got %v", err)
	}
	got, _ := s.ByID(id)
	if len(got.PubKeys) != maxKeysPerAccount {
		t.Fatalf("cap not enforced: %d keys", len(got.PubKeys))
	}
}

// Concurrent enrolments race on read-modify-write; the cap must hold and no
// enrolment may be lost, because both happen under the store lock.
func TestConcurrentAddPubKeyRespectsTheCap(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	const n = maxKeysPerAccount + 6
	spkis := make([]string, n)
	for i := range spkis {
		_, spkis[i] = testKey(t)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.AddPubKey(id, spkis[i], "k"); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != maxKeysPerAccount {
		t.Fatalf("%d enrolments succeeded, want exactly %d", ok, maxKeysPerAccount)
	}
	got, _ := s.ByID(id)
	if len(got.PubKeys) != maxKeysPerAccount {
		t.Fatalf("stored %d keys, want %d", len(got.PubKeys), maxKeysPerAccount)
	}
}

func TestAddPubKeyRejectsBadInput(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	for _, bad := range []string{"", "   ", "!!!", strings.Repeat("A", maxSPKILen+1)} {
		if _, err := s.AddPubKey(id, bad, ""); err == nil {
			t.Errorf("AddPubKey accepted %q", bad)
		}
	}
	_, spki := testKey(t)
	if _, err := s.AddPubKey("local:nobody", spki, ""); err == nil {
		t.Fatal("AddPubKey accepted an unknown account")
	}
}

func TestAddPubKeySanitisesLabels(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	_, spki := testKey(t)
	key, err := s.AddPubKey(id, spki, "  work\x00laptop\r\n<b>  ")
	if err != nil {
		t.Fatalf("AddPubKey: %v", err)
	}
	if strings.ContainsAny(key.Label, "\x00\r\n") {
		t.Fatalf("control characters survived in the label: %q", key.Label)
	}
	if key.Label != "worklaptop<b>" {
		t.Fatalf("label = %q", key.Label)
	}

	_, spki2 := testKey(t)
	long, err := s.AddPubKey(id, spki2, strings.Repeat("é", maxKeyLabel*2))
	if err != nil {
		t.Fatalf("AddPubKey long label: %v", err)
	}
	if n := len([]rune(long.Label)); n > maxKeyLabel {
		t.Fatalf("label is %d runes, want <= %d", n, maxKeyLabel)
	}
}

func TestRemovePubKey(t *testing.T) {
	s, id, path := storeWithAccount(t, "alice")
	_, spkiA := testKey(t)
	_, spkiB := testKey(t)
	a, _ := s.AddPubKey(id, spkiA, "a")
	b, _ := s.AddPubKey(id, spkiB, "b")

	if err := s.RemovePubKey(id, a.ID); err != nil {
		t.Fatalf("RemovePubKey: %v", err)
	}
	got, _ := s.ByID(id)
	if len(got.PubKeys) != 1 || got.PubKeys[0].ID != b.ID {
		t.Fatalf("wrong key removed: %+v", got.PubKeys)
	}
	// Revoking twice must report failure, so the UI cannot claim a live key is gone.
	if err := s.RemovePubKey(id, a.ID); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("want ErrNoSuchKey on re-revoke, got %v", err)
	}
	if err := s.RemovePubKey(id, ""); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("want ErrNoSuchKey for an empty id, got %v", err)
	}
	if err := s.RemovePubKey("local:nobody", b.ID); err == nil {
		t.Fatal("RemovePubKey accepted an unknown account")
	}

	reopened, err := OpenAccounts(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after, _ := reopened.ByID(id)
	if len(after.PubKeys) != 1 || after.PubKeys[0].ID != b.ID {
		t.Fatalf("revocation did not persist: %+v", after.PubKeys)
	}
}

// ByID/All hand out copies; mutating them must not reach into the store.
func TestKeySlicesAreNotShared(t *testing.T) {
	s, id, _ := storeWithAccount(t, "alice")
	_, spki := testKey(t)
	if _, err := s.AddPubKey(id, spki, "a"); err != nil {
		t.Fatalf("AddPubKey: %v", err)
	}
	got, _ := s.ByID(id)
	got.PubKeys[0].Key = "tampered"
	for _, a := range s.All() {
		if a.Account == id && a.PubKeys[0].Key != spki {
			t.Fatal("mutating a returned copy changed the stored key")
		}
		a.PubKeys[0].Label = "tampered too"
	}
	again, _ := s.ByID(id)
	if again.PubKeys[0].Key != spki || again.PubKeys[0].Label != "a" {
		t.Fatalf("store was mutated through a copy: %+v", again.PubKeys[0])
	}
}
