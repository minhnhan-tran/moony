package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A keyfile login is challenge/response: the server hands out a short-lived blob,
// the browser signs it with the private key from the user's keyfile, and the
// server checks the signature against an enrolled public key. The challenge must
// therefore be (a) unforgeable, (b) short-lived, (c) usable exactly once, and
// (d) impossible to mistake for any other token this package signs.
//
// (d) is why this has its own construction instead of reusing Sign/Verify. A
// challenge is handed to *unauthenticated* callers for an account they name, so if
// it shared the session-token construction it would be a free session token — the
// exact confusion bug fixed in confusion_test.go. Different domain prefix,
// different separator (NUL, which no account id may contain), different parser.
const (
	challengeDomain = "moony-keyfile-challenge\x00"
	// challengeTTL is deliberately short: it only has to cover one round trip plus
	// the user's passphrase prompt.
	challengeTTL = 90 * time.Second
	// maxLiveChallenges bounds the single-use set. Entries are only added when a
	// *valid* challenge is redeemed, and every entry expires within challengeTTL, so
	// this is a backstop against unbounded growth rather than a rate limit (the
	// route limiter is that). If it is ever hit we refuse logins instead of evicting
	// live nonces, because evicting one would make it replayable.
	maxLiveChallenges = 8192
)

// nonceSet remembers redeemed challenge nonces until they expire, which is what
// makes a challenge single-use.
type nonceSet struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// consume records nonce as used and reports whether it was still unused. exp is
// the nonce's own expiry, so entries can be dropped exactly when replaying them
// would fail on expiry anyway.
func (n *nonceSet) consume(nonce string, exp time.Time, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = make(map[string]time.Time)
	}
	for k, e := range n.seen {
		if !now.Before(e) {
			delete(n.seen, k)
		}
	}
	if _, used := n.seen[nonce]; used {
		return false
	}
	if len(n.seen) >= maxLiveChallenges {
		return false
	}
	n.seen[nonce] = exp
	return true
}

// mintChallenge returns a signed, single-use challenge bound to account.
//
// It does NOT check that the account exists: /auth/challenge is public, and
// answering differently for a real and an unknown account turns it into a
// username oracle. An unknown account gets a perfectly well-formed challenge that
// simply cannot be used to log in, because there is no enrolled key to satisfy it.
func (a *Auth) mintChallenge(account string) (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw[:])
	exp := a.now().Add(challengeTTL).Unix()
	payload := account + "\x00" + nonce + "\x00" + strconv.FormatInt(exp, 10)
	return b64.EncodeToString([]byte(payload)) + "." +
		b64.EncodeToString(macWith(a.secret, challengeDomain+payload)), nil
}

// redeemChallenge authenticates a challenge blob and burns it, returning the
// account it was minted for. A blob that is malformed, tampered with, expired, or
// already used yields ("", false) — no distinction is reported to the caller.
func (a *Auth) redeemChallenge(blob string) (string, bool) {
	account, nonce, exp, ok := a.parseChallenge(blob)
	if !ok {
		return "", false
	}
	if !a.challenges.consume(nonce, exp, a.now()) {
		return "", false
	}
	return account, true
}

// parseChallenge verifies the MAC and expiry without consuming the nonce.
func (a *Auth) parseChallenge(blob string) (account, nonce string, exp time.Time, ok bool) {
	dot := strings.IndexByte(blob, '.')
	if dot <= 0 || dot == len(blob)-1 {
		return "", "", time.Time{}, false
	}
	payload, err := b64.DecodeString(blob[:dot])
	if err != nil {
		return "", "", time.Time{}, false
	}
	sig, err := b64.DecodeString(blob[dot+1:])
	if err != nil {
		return "", "", time.Time{}, false
	}
	p := string(payload)
	if !hmac.Equal(sig, macWith(a.secret, challengeDomain+p)) {
		return "", "", time.Time{}, false
	}
	parts := strings.Split(p, "\x00")
	if len(parts) != 3 {
		return "", "", time.Time{}, false
	}
	account, nonce = parts[0], parts[1]
	if account == "" || nonce == "" {
		return "", "", time.Time{}, false
	}
	unix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", time.Time{}, false
	}
	expiry := time.Unix(unix, 0)
	if !a.now().Before(expiry) {
		return "", "", time.Time{}, false
	}
	return account, nonce, expiry, true
}
