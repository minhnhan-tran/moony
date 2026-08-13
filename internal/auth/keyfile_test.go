package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
)

// testKey generates a P-256 keypair and returns it with its base64 SPKI, the way
// the browser hands one to the server.
func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k, spkiOf(t, &k.PublicKey)
}

func spkiOf(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal spki: %v", err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// signP1363 signs like WebCrypto does: raw r||s, 32 bytes each, no ASN.1.
func signP1363(t *testing.T, k *ecdsa.PrivateKey, challenge string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(challenge))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return base64.StdEncoding.EncodeToString(out)
}

func TestKeyfileSignatureRoundTrip(t *testing.T) {
	k, spki := testKey(t)
	challenge := "local:alice\x00nonce\x001700000000.mac"
	if !verifyKeyfileSignature(spki, challenge, signP1363(t, k, challenge)) {
		t.Fatal("a valid P-1363 signature by the enrolled key was rejected")
	}
}

func TestKeyfileSignatureRejectsWrongKeyAndMutatedChallenge(t *testing.T) {
	k, spki := testKey(t)
	other, otherSPKI := testKey(t)
	challenge := "challenge-blob"
	sig := signP1363(t, k, challenge)

	if verifyKeyfileSignature(otherSPKI, challenge, sig) {
		t.Fatal("a signature verified against a different account's key")
	}
	if verifyKeyfileSignature(spki, challenge+"x", sig) {
		t.Fatal("a signature verified over a mutated challenge")
	}
	if verifyKeyfileSignature(spki, challenge, signP1363(t, other, challenge)) {
		t.Fatal("the wrong private key produced an accepted signature")
	}
}

// The browser emits P-1363. Accepting ASN.1 as well would mean one signature has
// two valid encodings, which is exactly the malleability we do not want.
func TestKeyfileSignatureRejectsASN1Encoding(t *testing.T) {
	k, spki := testKey(t)
	challenge := "challenge-blob"
	sum := sha256.Sum256([]byte(challenge))
	der, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	if err != nil {
		t.Fatalf("sign asn1: %v", err)
	}
	if verifyKeyfileSignature(spki, challenge, base64.StdEncoding.EncodeToString(der)) {
		t.Fatal("an ASN.1/DER signature was accepted; only raw r||s must be")
	}
	// Sanity: the same signature values in P-1363 form do verify, so the rejection
	// above is about the encoding and not a broken test key.
	var parsed struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &parsed); err != nil {
		t.Fatalf("unmarshal asn1: %v", err)
	}
	raw := make([]byte, 64)
	parsed.R.FillBytes(raw[:32])
	parsed.S.FillBytes(raw[32:])
	if !verifyKeyfileSignature(spki, challenge, base64.StdEncoding.EncodeToString(raw)) {
		t.Fatal("the same signature in P-1363 form should verify")
	}
}

func TestKeyfileRejectsNonP256Keys(t *testing.T) {
	k384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p384: %v", err)
	}
	if _, _, err := parsePubKeySPKI(spkiOf(t, &k384.PublicKey)); err == nil {
		t.Fatal("a P-384 key was accepted; only P-256 is supported")
	}
	// An RSA SPKI must be refused too, not just curves.
	rsaSPKI := "MFwwDQYJKoZIhvcNAQEBBQADSwAwSAJBALqI"
	if _, _, err := parsePubKeySPKI(rsaSPKI); err == nil {
		t.Fatal("a non-ECDSA SPKI was accepted")
	}
}

func TestKeyfileFailsClosedOnGarbage(t *testing.T) {
	k, spki := testKey(t)
	challenge := "challenge-blob"
	good := signP1363(t, k, challenge)
	sigBytes, _ := base64.StdEncoding.DecodeString(good)

	cases := []struct{ name, spki, sig string }{
		{"empty everything", "", ""},
		{"empty key", "", good},
		{"empty sig", spki, ""},
		{"not base64 key", "!!!!", good},
		{"not base64 sig", spki, "!!!!"},
		{"truncated key", spki[:20], good},
		{"truncated sig", base64.StdEncoding.EncodeToString(sigBytes[:63]), good},
		{"short sig", spki, base64.StdEncoding.EncodeToString(sigBytes[:32])},
		{"long sig", spki, base64.StdEncoding.EncodeToString(append(sigBytes, 0))},
		{"zero sig", spki, base64.StdEncoding.EncodeToString(make([]byte, 64))},
		{"oversized key", strings.Repeat("A", maxSPKILen+4), good},
	}
	for _, c := range cases {
		if verifyKeyfileSignature(c.spki, challenge, c.sig) {
			t.Errorf("%s: verification succeeded; it must fail closed", c.name)
		}
	}
}

// The stored form must be canonical so duplicate detection is meaningful: the
// same key with sloppy base64 must normalise to the same string.
func TestParsePubKeyCanonicalises(t *testing.T) {
	_, spki := testKey(t)
	_, canonA, err := parsePubKeySPKI(spki)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, canonB, err := parsePubKeySPKI("  " + spki + "\n")
	if err != nil {
		t.Fatalf("parse padded: %v", err)
	}
	if canonA != canonB || canonA != spki {
		t.Fatalf("canonical forms differ: %q vs %q (want %q)", canonA, canonB, spki)
	}
}
