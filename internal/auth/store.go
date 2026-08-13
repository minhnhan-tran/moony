package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
)

// localPrefix namespaces locally-registered accounts. OIDC accounts are an email
// or subject claim, so without a prefix a local user could register the spelling of
// a colleague's email and inherit whatever that account can reach. canonicalUsername
// also refuses '@' and ':' so a local name cannot mimic either form.
const localPrefix = "local:"

// PubKey is an enrolled public key for keyfile login (populated by the keyfile
// work; carried here so the on-disk format does not change again later).
type PubKey struct {
	ID      string    `json:"id"`
	Alg     string    `json:"alg"`
	Key     string    `json:"key"` // base64 SPKI
	Label   string    `json:"label,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

// Account is one locally-registered user.
type Account struct {
	Account      string    `json:"account"`  // "local:<canonical>", as signed and as linkKey sees it
	Username     string    `json:"username"` // as submitted, for display only
	PasswordHash string    `json:"password_hash"`
	PubKeys      []PubKey  `json:"pub_keys,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	Disabled     bool      `json:"disabled,omitempty"`
	IsAdmin      bool      `json:"is_admin,omitempty"`

	// Epoch is mixed into this account's token-signing key, so bumping it
	// invalidates every live token for the account — both the browser session and
	// the 30-day connector credential. See tokenKey.
	Epoch uint64 `json:"epoch,omitempty"`
}

// AccountStore is a small JSON-backed account file, held entirely in memory and
// rewritten on change. It is mutex-guarded because registration must be
// serialised: two requests racing for the same name would otherwise both see it
// free.
type AccountStore struct {
	path string

	mu       sync.RWMutex
	accounts map[string]Account // keyed by Account id
}

type accountFile struct {
	Accounts map[string]Account `json:"accounts"`
}

// OpenAccounts loads the account file at path. A missing file is first run. A file
// that exists but cannot be read or parsed is an error: presenting an empty account
// list instead would reopen registration of every existing name.
func OpenAccounts(path string) (*AccountStore, error) {
	s := &AccountStore{path: path, accounts: map[string]Account{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read account file %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	var f accountFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse account file %s (refusing to start with an empty account list): %w", path, err)
	}
	for id, a := range f.Accounts {
		if a.Account == "" {
			a.Account = id
		}
		// Validate what came off disk. The id becomes a token subject and a relay
		// linkKey component, so a hand-edited file must not be able to introduce
		// "local:alice|x", an id containing NUL, or an entry keyed differently from
		// the account it holds (which would let one account shadow another).
		if a.Account != id {
			return nil, fmt.Errorf("account file %s: entry %q holds account %q; the key and the account must match", path, id, a.Account)
		}
		name, ok := strings.CutPrefix(a.Account, localPrefix)
		if !ok {
			return nil, fmt.Errorf("account file %s: account %q is missing the %q prefix", path, a.Account, localPrefix)
		}
		canon, err := canonicalUsername(name)
		if err != nil {
			return nil, fmt.Errorf("account file %s: account %q is not a valid local account: %w", path, a.Account, err)
		}
		if canon != name {
			return nil, fmt.Errorf("account file %s: account %q is not in canonical form (want %q)", path, a.Account, localPrefix+canon)
		}
		s.accounts[a.Account] = a
	}
	return s, nil
}

// canonicalUsername folds a submitted name to the single spelling used for the
// account id, or reports why it is unacceptable.
//
// NFKC first, so visually identical spellings (fullwidth "ＡＬＩＣＥ", the "ﬁ"
// ligature) fold together instead of becoming separate accounts. Then a strict
// charset: the result must be [a-z0-9._-]{2,64} and may not start or end with
// punctuation. That excludes every byte with meaning elsewhere in the system —
// NUL (linkKey's separator), '|' (the token payload's), ':' (the namespace), '@'
// (an OIDC email), '/' and whitespace — and, because the set is ASCII, every
// non-Latin homograph as well.
func canonicalUsername(in string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(norm.NFKC.String(in)))
	if len(name) < 2 || len(name) > 64 {
		return "", fmt.Errorf("username must be between 2 and 64 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return "", fmt.Errorf("username may only contain letters, digits, '.', '_' and '-'")
		}
	}
	if strings.ContainsAny(name[:1], "._-") || strings.ContainsAny(name[len(name)-1:], "._-") {
		return "", fmt.Errorf("username must start and end with a letter or digit")
	}
	return name, nil
}

// AccountID returns the namespaced id for a submitted username.
func AccountID(username string) (string, error) {
	c, err := canonicalUsername(username)
	if err != nil {
		return "", err
	}
	return localPrefix + c, nil
}

// beforeInsert is a no-op the concurrency test replaces to widen the window
// between Create's existence check and its write. Production never sets it.
var beforeInsert = func() {}

// ErrAccountExists is returned when a name is already taken.
var ErrAccountExists = errors.New("that username is already taken")

// Create registers an account. The whole check-then-write runs under the lock, so
// concurrent registrations of the same name cannot both succeed.
func (s *AccountStore) Create(username, passwordHash string) (Account, error) {
	id, err := AccountID(username)
	if err != nil {
		return Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[id]; exists {
		return Account{}, ErrAccountExists
	}
	beforeInsert() // test hook: forces the check/insert interleaving (no-op in production)
	a := Account{
		Account:      id,
		Username:     strings.TrimSpace(username),
		PasswordHash: passwordHash,
		CreatedAt:    time.Now().UTC(),
	}
	s.accounts[id] = a
	if err := s.saveLocked(); err != nil {
		delete(s.accounts, id) // keep memory and disk in agreement
		return Account{}, err
	}
	return a, nil
}

// Lookup finds an account by submitted username, canonicalising first. The
// returned Account is a copy: callers must go through Update to change anything.
func (s *AccountStore) Lookup(username string) (Account, bool) {
	id, err := AccountID(username)
	if err != nil {
		return Account{}, false
	}
	return s.ByID(id)
}

// ByID finds an account by its namespaced id (as carried in a token).
func (s *AccountStore) ByID(id string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, false
	}
	a.PubKeys = append([]PubKey(nil), a.PubKeys...) // don't share the slice
	return a, true
}

// Update applies fn to an account and persists the result. fn runs under the
// write lock, so read-modify-write sequences are atomic.
func (s *AccountStore) Update(id string, fn func(*Account) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("no such account: %s", id)
	}
	// Deep-copy for the rollback: PubKeys shares its backing array with the stored
	// account, so a mutation inside fn would survive a failed save.
	before := a
	before.PubKeys = append([]PubKey(nil), a.PubKeys...)
	a.PubKeys = append([]PubKey(nil), a.PubKeys...)
	if err := fn(&a); err != nil {
		return err
	}
	a.Account = before.Account // the id is not the caller's to change
	s.accounts[id] = a
	if err := s.saveLocked(); err != nil {
		s.accounts[id] = before
		return err
	}
	return nil
}

// BumpEpoch invalidates every token issued for an account and returns the new
// epoch. Used by password change, logout-everywhere, disable and key removal.
func (s *AccountStore) BumpEpoch(id string) (uint64, error) {
	var epoch uint64
	err := s.Update(id, func(a *Account) error {
		a.Epoch++
		epoch = a.Epoch
		return nil
	})
	return epoch, err
}

// All returns a copy of every account, for admin listings.
func (s *AccountStore) All() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, a)
	}
	return out
}

// Count reports how many accounts exist (used to decide whether the first
// registration becomes the admin).
func (s *AccountStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}

// saveLocked writes the file temp-then-rename at 0600. The caller holds s.mu.
func (s *AccountStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create account directory: %w", err)
	}
	raw, err := json.MarshalIndent(accountFile{Accounts: s.accounts}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	// Create the temp file 0600 from the start: a 0644 window would expose the
	// hashes to anyone watching the directory.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".users-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp account file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Windows reports ErrInvalid/ENOTSUP for chmod; the ACL is inherited there.
		if !strings.Contains(err.Error(), "not supported") {
			tmp.Close()
			return fmt.Errorf("secure temp account file: %w", err)
		}
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp account file: %w", err)
	}
	if err := tmp.Sync(); err != nil { // the epoch is a revocation record; don't lose it
		tmp.Close()
		return fmt.Errorf("sync temp account file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace account file: %w", err)
	}
	// The temp file was already 0600 and rename preserves the mode, so this is a
	// belt-and-braces repair for a pre-existing file with wider permissions. It must
	// not fail the save: the data is committed, and reporting an error here would
	// roll the caller's in-memory state back out of step with the disk.
	_ = os.Chmod(s.path, 0o600)
	return nil
}
