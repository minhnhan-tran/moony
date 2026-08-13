package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func tempStore(t *testing.T) *AccountStore {
	t.Helper()
	s, err := OpenAccounts(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatalf("OpenAccounts: %v", err)
	}
	return s
}

// Names are canonicalised once, on create and on lookup, so two spellings of the
// same name cannot become two accounts — nor two *different* accounts one.
func TestCanonicalUsername(t *testing.T) {
	ok := []struct{ in, want string }{
		{"alice", "alice"},
		{"  Alice  ", "alice"},
		{"ALICE", "alice"},
		{"al.ice_1-x", "al.ice_1-x"},
		{"ＡＬＩＣＥ", "alice"},       // NFKC folds fullwidth onto ASCII
		{"ﬁnn", "finn"},          // ligature
		{"ali\u0301ce", "álice"}, // combining acute composes, then fails the charset below
	}
	for _, tc := range ok {
		got, err := canonicalUsername(tc.in)
		if tc.want == "álice" {
			if err == nil {
				t.Errorf("canonicalUsername(%q) = %q, want an error (non-ASCII)", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("canonicalUsername(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("canonicalUsername(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	bad := []string{
		"", " ", "a", // too short
		strings.Repeat("a", 65),                   // too long
		"alice bob", "al/ice", "al:ice", "al|ice", // separators used by linkKey, tokens, paths
		"ali\x00ce",     // NUL: linkKey's separator
		"admin@example", // '@' would let a local name mimic an OIDC email
		"-alice", "alice-", ".alice", "_alice",
		"локальный", "路人",
	}
	for _, in := range bad {
		if got, err := canonicalUsername(in); err == nil {
			t.Errorf("canonicalUsername(%q) = %q, want an error", in, got)
		}
	}
}

// The account id is namespaced so a local user can never collide with an OIDC
// account, whose id is an email or subject claim.
func TestAccountIDIsNamespaced(t *testing.T) {
	s := tempStore(t)
	a, err := s.Create("Alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if a.Account != "local:alice" {
		t.Errorf("Account = %q, want local:alice", a.Account)
	}
	if a.Username != "Alice" {
		t.Errorf("Username = %q, want the submitted spelling for display", a.Username)
	}
	if !strings.HasPrefix(a.Account, localPrefix) {
		t.Error("account id must carry the local: namespace")
	}
}

func TestCreateRejectsDuplicates(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Create("alice", "h"); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []string{"alice", "ALICE", "  Alice  ", "ＡＬＩＣＥ"} {
		if _, err := s.Create(dup, "h"); err == nil {
			t.Errorf("Create(%q) succeeded; it must collide with the existing account", dup)
		}
	}
	if n := len(s.All()); n != 1 {
		t.Errorf("store holds %d accounts, want 1", n)
	}
}

// Registration is serialised under the store lock, so a race cannot hand the same
// name to two requests.
func TestConcurrentCreateYieldsOneAccount(t *testing.T) {
	s := tempStore(t)
	const n = 64
	var wg sync.WaitGroup
	wins := make([]bool, n)

	// A start gate matters: without it the goroutines are created serially and the
	// early ones finish before the later ones begin, so an unsynchronised
	// check-then-insert never gets its window and the test passes regardless.
	// beforeInsert additionally yields inside Create, between the existence check
	// and the write, so the interleaving is forced rather than hoped for.
	release := make(chan struct{})
	beforeInsert = func() { runtime.Gosched(); time.Sleep(time.Millisecond) }
	t.Cleanup(func() { beforeInsert = func() {} })

	for i := range wins {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, err := s.Create("alice", "h")
			wins[i] = err == nil
		}(i)
	}
	close(release)
	wg.Wait()

	won := 0
	for _, w := range wins {
		if w {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d of %d concurrent creates succeeded, want exactly 1", won, n)
	}
	if n := len(s.All()); n != 1 {
		t.Errorf("store holds %d accounts, want 1", n)
	}
}

// The file holds password hashes, so it must not be world- or group-readable.
func TestStoreFileIsNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	s, err := OpenAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("alice", "hash"); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // Unix mode bits are not meaningful here
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %#o, want 0600 (the file holds password hashes)", perm)
	}
}

// A store we cannot parse must stop the relay, not silently present an empty
// account list — that would let anyone re-register an existing name and inherit
// whatever that account can reach.
func TestCorruptStoreIsFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAccounts(path); err == nil {
		t.Fatal("OpenAccounts accepted a corrupt store; it must refuse to start")
	}
}

// A missing file is the first-run case and must be fine.
func TestMissingStoreStartsEmpty(t *testing.T) {
	s, err := OpenAccounts(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatalf("a missing store is first run, not an error: %v", err)
	}
	if n := len(s.All()); n != 0 {
		t.Errorf("new store holds %d accounts, want 0", n)
	}
}

func TestAccountsSurviveReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	s, err := OpenAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("Alice", "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("local:alice", func(a *Account) error {
		a.IsAdmin = true
		a.Epoch = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Lookup("  ALICE ")
	if !ok {
		t.Fatal("account did not survive the reload, or lookup does not canonicalise")
	}
	if got.PasswordHash != "hash-1" || !got.IsAdmin || got.Epoch != 7 || got.Username != "Alice" {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

// Lookup must not hand out a pointer into the store's map: a caller mutating it
// would change the in-memory account without persisting, so a later save would
// write back state nobody chose.
func TestLookupReturnsACopy(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Create("alice", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("local:alice", func(a *Account) error {
		a.PubKeys = []PubKey{{ID: "k1", Label: "laptop"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	a, _ := s.Lookup("alice")
	a.Disabled = true
	a.Epoch = 99
	// The slice is the real hazard: Account itself copies by value, but a shared
	// backing array would let a caller rewrite an enrolled key in place.
	a.PubKeys[0].Key = "attacker-supplied"
	a.PubKeys[0].ID = "k1-tampered"

	again, _ := s.Lookup("alice")
	if again.Disabled || again.Epoch != 0 {
		t.Error("mutating a looked-up account changed the store")
	}
	if again.PubKeys[0].Key == "attacker-supplied" || again.PubKeys[0].ID != "k1" {
		t.Error("mutating a looked-up account's PubKeys changed the store")
	}
}

func TestUpdateUnknownAccountErrors(t *testing.T) {
	s := tempStore(t)
	if err := s.Update("local:nobody", func(*Account) error { return nil }); err == nil {
		t.Error("Update on an unknown account must fail")
	}
}

// BumpEpoch is how every credential for an account is revoked, so it must persist
// immediately — a bump lost to a crash would leave stolen tokens valid.
func TestBumpEpochPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	s, _ := OpenAccounts(path)
	if _, err := s.Create("alice", "h"); err != nil {
		t.Fatal(err)
	}
	e1, err := s.BumpEpoch("local:alice")
	if err != nil {
		t.Fatal(err)
	}
	if e1 == 0 {
		t.Error("first bump should move the epoch off zero")
	}

	reopened, _ := OpenAccounts(path)
	got, _ := reopened.Lookup("alice")
	if got.Epoch != e1 {
		t.Errorf("epoch after reload = %d, want %d", got.Epoch, e1)
	}
}

// The on-disk form is plain JSON an operator can read and edit.
func TestOnDiskFormatIsReadableJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	s, _ := OpenAccounts(path)
	if _, err := s.Create("alice", "h"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Accounts map[string]Account `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("on-disk JSON does not round-trip: %v\n%s", err, raw)
	}
	if _, ok := f.Accounts["local:alice"]; !ok {
		t.Errorf("account not keyed by its id on disk: %s", raw)
	}
}
