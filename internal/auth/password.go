package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length bounds. The minimum is a floor on guessing cost, not security
// advice. The maximum matters more than it looks: argon2 hashes whatever it is
// given, so an unbounded field lets an unauthenticated caller spend the relay's
// memory and CPU at will — a login form is a fine place to mount a DoS.
const (
	minPasswordLen = 10
	maxPasswordLen = 256
)

// argonParams are the cost parameters recorded alongside each hash, so they can be
// raised later without invalidating passwords already stored.
type argonParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

// defaultArgonParams follows the RFC 9106 second recommendation (64 MiB, t=3),
// which is a reasonable cost for a relay that also serves interactive traffic.
var defaultArgonParams = argonParams{
	memory:  64 * 1024,
	time:    3,
	threads: 2,
	keyLen:  32,
	saltLen: 16,
}

// HashPassword returns an encoded argon2id hash of password.
func HashPassword(password string) (string, error) {
	return hashPasswordWith(password, defaultArgonParams)
}

// ValidatePassword reports whether a password is acceptable to store.
func ValidatePassword(password string) error {
	if n := len(password); n < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	} else if n > maxPasswordLen {
		return fmt.Errorf("password must be at most %d characters", maxPasswordLen)
	}
	if strings.ContainsRune(password, 0) {
		return errors.New("password may not contain a NUL byte")
	}
	if !utf8.ValidString(password) {
		return errors.New("password must be valid UTF-8")
	}
	return nil
}

func hashPasswordWith(password string, p argonParams) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether password matches an encoded hash.
//
// Every failure path — a malformed encoding, an unknown variant, a hash from a
// scheme we dropped — returns false. In particular an empty or truncated stored
// hash must not verify against an empty password, which is the shape an
// unpopulated field takes.
func VerifyPassword(encoded, password string) bool {
	if len(password) == 0 || len(password) > maxPasswordLen {
		return false
	}
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return p, nil, nil, errors.New("malformed hash")
	}
	if parts[1] != "argon2id" {
		return p, nil, nil, fmt.Errorf("unsupported password hash variant %q", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, errors.New("malformed version")
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("unsupported argon2 version %d", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, errors.New("malformed parameters")
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return p, nil, nil, errors.New("degenerate parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, errors.New("malformed salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return p, nil, nil, errors.New("malformed hash")
	}
	p.saltLen = uint32(len(salt))
	p.keyLen = uint32(len(want))
	return p, salt, want, nil
}
