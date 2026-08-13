package auth

import (
	"strings"
	"testing"
	"time"
)

// movableAuth is an Auth whose clock the test can advance.
func movableAuth(t *testing.T) (*Auth, func(time.Duration)) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	a := &Auth{
		secret: []byte("test-secret-at-least-16-bytes-long"),
		ttl:    time.Hour,
		clock:  func() time.Time { return now },
	}
	return a, func(d time.Duration) { now = now.Add(d) }
}

func TestChallengeRoundTripIsSingleUse(t *testing.T) {
	a, _ := movableAuth(t)
	blob, err := a.mintChallenge("local:alice")
	if err != nil {
		t.Fatalf("mintChallenge: %v", err)
	}
	got, ok := a.redeemChallenge(blob)
	if !ok || got != "local:alice" {
		t.Fatalf("redeemChallenge = (%q, %v), want local:alice, true", got, ok)
	}
	if _, ok := a.redeemChallenge(blob); ok {
		t.Fatal("a challenge was redeemed twice; it must be single-use")
	}
}

func TestChallengeExpires(t *testing.T) {
	a, advance := movableAuth(t)
	blob, _ := a.mintChallenge("local:alice")
	advance(challengeTTL - time.Second)
	if _, ok := a.redeemChallenge(blob); !ok {
		t.Fatal("challenge rejected while still inside its TTL")
	}
	blob2, _ := a.mintChallenge("local:alice")
	advance(challengeTTL + time.Second)
	if _, ok := a.redeemChallenge(blob2); ok {
		t.Fatal("an expired challenge was accepted")
	}
}

func TestChallengeRejectsTamperingAndGarbage(t *testing.T) {
	a, _ := movableAuth(t)
	blob, _ := a.mintChallenge("local:alice")
	dot := strings.IndexByte(blob, '.')
	payload, sig := blob[:dot], blob[dot+1:]

	// Re-signing the payload with a different secret must not verify.
	other := &Auth{secret: []byte("another-secret-at-least-16-bytes"), clock: a.clock}
	forged, _ := other.mintChallenge("local:alice")

	bad := []string{
		"",
		".",
		payload,                   // no MAC
		payload + ".",             // empty MAC
		"." + sig,                 // empty payload
		payload + "." + sig + "x", // mutated MAC
		b64.EncodeToString([]byte("local:bob\x00n\x0099999999999")) + "." + sig, // swapped account, kept MAC
		"!!!." + sig,
		payload + ".!!!",
		forged,
	}
	for _, blob := range bad {
		if acct, ok := a.redeemChallenge(blob); ok {
			t.Errorf("redeemChallenge(%q) succeeded as %q; must fail closed", blob, acct)
		}
	}
	// A payload with the wrong field count must fail even when correctly signed.
	short := "local:alice\x001700000000"
	crafted := b64.EncodeToString([]byte(short)) + "." + b64.EncodeToString(macWith(a.secret, challengeDomain+short))
	if _, ok := a.redeemChallenge(crafted); ok {
		t.Error("a correctly signed but malformed payload was accepted")
	}
}

// The whole point of the separate construction: a challenge must never be usable
// as a session or connector token, and neither must be usable as a challenge.
func TestChallengeIsNotInterchangeableWithTokens(t *testing.T) {
	a, _ := movableAuth(t)
	blob, _ := a.mintChallenge("local:alice")
	for _, aud := range []string{audWeb, audConnector} {
		if acct, ok := a.Verify(blob, aud); ok {
			t.Fatalf("a challenge verified as a %s token for %q", aud, acct)
		}
	}
	for _, aud := range []string{audWeb, audConnector} {
		if acct, ok := a.redeemChallenge(a.Sign("local:alice", aud, time.Hour)); ok {
			t.Fatalf("a %s token redeemed as a challenge for %q", aud, acct)
		}
	}
	// And the OIDC transaction blob, the other signed thing in this package.
	tx := a.signBlob("state|4102444800|web|/")
	if acct, ok := a.redeemChallenge(tx); ok {
		t.Fatalf("an OIDC tx blob redeemed as a challenge for %q", acct)
	}
	if _, ok := a.openBlob(blob); ok {
		t.Fatal("a challenge opened as an OIDC tx blob")
	}
}

// A challenge for an account that does not exist is still minted (so the endpoint
// is not a username oracle) but cannot authenticate anything: that is enforced at
// the route, where the account is looked up and has no keys.
func TestChallengeMintedForUnknownAccount(t *testing.T) {
	a, _ := movableAuth(t)
	blob, err := a.mintChallenge("local:nobody")
	if err != nil {
		t.Fatalf("mintChallenge: %v", err)
	}
	got, ok := a.redeemChallenge(blob)
	if !ok || got != "local:nobody" {
		t.Fatalf("redeemChallenge = (%q, %v)", got, ok)
	}
}

func TestNonceSetIsBoundedAndEvictsOnExpiry(t *testing.T) {
	var n nonceSet
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(challengeTTL)
	for i := 0; i < maxLiveChallenges; i++ {
		if !n.consume(string(rune(i))+"x", exp, now) {
			t.Fatalf("consume %d failed below the cap", i)
		}
	}
	if n.consume("one-too-many", exp, now) {
		t.Fatal("the set grew past maxLiveChallenges")
	}
	if len(n.seen) != maxLiveChallenges {
		t.Fatalf("set holds %d entries, want %d", len(n.seen), maxLiveChallenges)
	}
	// Once everything has expired the set drains and accepts again.
	if !n.consume("after-expiry", exp.Add(time.Second), exp.Add(time.Second)) {
		t.Fatal("the set did not evict expired nonces")
	}
	if len(n.seen) != 1 {
		t.Fatalf("after eviction the set holds %d entries, want 1", len(n.seen))
	}
}
