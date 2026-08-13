package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// maxKeysPerAccount caps enrolled credentials. Without a cap, an attacker who
// briefly holds a session (or an XSS foothold) can enrol thousands of keys, both
// bloating the users file that is rewritten on every write and making the real
// keys impossible to audit in the account UI.
const maxKeysPerAccount = 8

// maxKeyLabel bounds the user-chosen label. It is rendered back into HTML, so it
// is also stripped of control characters at the door.
const maxKeyLabel = 64

var (
	// ErrKeyLimit is returned when the account already holds maxKeysPerAccount keys.
	ErrKeyLimit = fmt.Errorf("this account already has the maximum of %d keys", maxKeysPerAccount)
	// ErrDuplicateKey is returned when the same public key is enrolled twice.
	ErrDuplicateKey = errors.New("that key is already enrolled on this account")
	// ErrNoSuchKey is returned when revoking a key id the account does not have.
	ErrNoSuchKey = errors.New("no such key")
)

// sanitizeLabel makes a user label safe to store and render: control characters
// (including the NUL that separates fields elsewhere in this package) are dropped
// rather than replaced, and the result is truncated by rune so a multi-byte label
// cannot be cut mid-character.
func sanitizeLabel(in string) string {
	var b strings.Builder
	for _, r := range in {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > maxKeyLabel {
		out = strings.TrimSpace(string(r[:maxKeyLabel]))
	}
	return out
}

// newKeyID mints the key's identifier server-side. The client never chooses it:
// a client-chosen id could collide with another key's (making revocation
// ambiguous) or carry markup into the account page.
func newKeyID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate key id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// AddPubKey enrols a public key on an account. The SPKI is parsed and normalised
// first, so only a real P-256 key is ever written and duplicate detection compares
// canonical bytes rather than base64 spellings. The whole check-then-write happens
// inside Update, i.e. under the store's lock, so two concurrent enrolments cannot
// both pass the cap.
func (s *AccountStore) AddPubKey(id, spkiB64, label string) (PubKey, error) {
	_, canon, err := parsePubKeySPKI(spkiB64)
	if err != nil {
		return PubKey{}, err
	}
	keyID, err := newKeyID()
	if err != nil {
		return PubKey{}, err
	}
	key := PubKey{
		ID:      keyID,
		Alg:     keyAlgP256,
		Key:     canon,
		Label:   sanitizeLabel(label),
		AddedAt: time.Now().UTC(),
	}
	err = s.Update(id, func(a *Account) error {
		if len(a.PubKeys) >= maxKeysPerAccount {
			return ErrKeyLimit
		}
		for _, k := range a.PubKeys {
			if k.Key == canon {
				return ErrDuplicateKey
			}
		}
		a.PubKeys = append(a.PubKeys, key)
		return nil
	})
	if err != nil {
		return PubKey{}, err
	}
	return key, nil
}

// RemovePubKey revokes one enrolled key. Removing a key the account does not have
// is an error, not a silent success: the UI must not be able to report "revoked"
// for a key that is still usable.
func (s *AccountStore) RemovePubKey(id, keyID string) error {
	if strings.TrimSpace(keyID) == "" {
		return ErrNoSuchKey
	}
	return s.Update(id, func(a *Account) error {
		out := make([]PubKey, 0, len(a.PubKeys))
		found := false
		for _, k := range a.PubKeys {
			if k.ID == keyID {
				found = true
				continue
			}
			out = append(out, k)
		}
		if !found {
			return ErrNoSuchKey
		}
		a.PubKeys = out
		return nil
	})
}
