package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// keyAlgP256 is the only credential algorithm accepted. Keeping this a closed set
// (rather than trusting an "alg" field from the client) means a caller cannot
// enrol, say, a 512-bit RSA key or an "alg":"none" placeholder and then have the
// verifier take the cheapest path.
const keyAlgP256 = "ecdsa-p256"

// p256SigLen is the length of the raw r||s signature WebCrypto's ECDSA
// implementation produces for P-256 (IEEE P1363, 32 bytes each). We accept
// exactly this and never fall back to ASN.1: accepting two encodings for one
// signature invites the malleability bugs that plague JOSE/ASN.1 mixtures, and
// the browser has no reason to send DER.
const p256SigLen = 64

// maxSPKILen bounds the base64 SPKI we are willing to parse. A P-256 SPKI is 91
// bytes (124 base64 chars); the slack covers whitespace and future encodings
// while keeping x509 parsing off untrusted megabytes.
const maxSPKILen = 512

var errBadPubKey = errors.New("public key must be a base64 P-256 SPKI")

// dummyPubKeySPKI is a throwaway P-256 public key, verified against when an
// account has no usable key, so that "no such account", "disabled" and "wrong
// signature" all cost one ECDSA verification. It authenticates nothing: no private
// half exists anywhere, and it is never stored on an account.
const dummyPubKeySPKI = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAED4mf9zLdaSoomYxtF5ReFjfTPZYT/LmhmnbpNj4KGRukWtf8d04vTb3wg1AhamaRdXGoxvhKpSZ0GxgudQxeag=="

// parsePubKeySPKI decodes a base64-encoded SubjectPublicKeyInfo and requires an
// ECDSA key on P-256. It returns the key plus the *canonical* base64 form (the
// key re-encoded from the parsed structure), which is what gets stored: comparing
// canonical forms is what makes duplicate detection meaningful, since the same key
// has many base64 spellings (padding, whitespace, trailing junk after the DER).
func parsePubKeySPKI(spki string) (*ecdsa.PublicKey, string, error) {
	spki = strings.TrimSpace(spki)
	if spki == "" || len(spki) > maxSPKILen {
		return nil, "", errBadPubKey
	}
	der, err := base64.StdEncoding.DecodeString(spki)
	if err != nil {
		return nil, "", errBadPubKey
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", errBadPubKey
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, "", errBadPubKey
	}
	if ec.Curve != elliptic.P256() {
		return nil, "", fmt.Errorf("%w (got a different curve)", errBadPubKey)
	}
	// x509 already rejects off-curve points, but assert it rather than assume:
	// an off-curve key turns ecdsa.Verify into an invalid-curve oracle.
	if !ec.Curve.IsOnCurve(ec.X, ec.Y) {
		return nil, "", errBadPubKey
	}
	canon, err := x509.MarshalPKIXPublicKey(ec)
	if err != nil {
		return nil, "", errBadPubKey
	}
	return ec, base64.StdEncoding.EncodeToString(canon), nil
}

// verifyKeyfileSignature reports whether sigB64 is a valid signature by the
// enrolled key over the challenge string. Every failure path returns false; no
// error distinguishes "malformed" from "wrong key", so this cannot be used to
// probe what is stored.
func verifyKeyfileSignature(spkiB64, challenge, sigB64 string) bool {
	pub, _, err := parsePubKeySPKI(spkiB64)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != p256SigLen {
		return false
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	sum := sha256.Sum256([]byte(challenge))
	return ecdsa.Verify(pub, sum[:], r, s)
}
