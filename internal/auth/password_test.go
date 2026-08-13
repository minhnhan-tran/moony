package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordUsesARandomSalt(t *testing.T) {
	a, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("the same password hashed twice produced identical output; the salt is not random")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$") {
		t.Errorf("unexpected encoding: %s", a)
	}
	// Parameters must travel with the hash so they can be raised later without
	// invalidating existing passwords.
	if !strings.Contains(a, "m=") || !strings.Contains(a, "t=") || !strings.Contains(a, "p=") {
		t.Errorf("encoding does not carry its parameters: %s", a)
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, err := HashPassword("hunter2-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "hunter2-secret") {
		t.Error("the correct password did not verify")
	}
	for _, wrong := range []string{"hunter3-secret", "", "HUNTER2-SECRET", "hunter2-secret ", " hunter2-secret", "hunter2\x00"} {
		if VerifyPassword(hash, wrong) {
			t.Errorf("password %q verified against a hash of %q", wrong, "hunter2-secret")
		}
	}
}

// Anything we cannot parse must fail closed. A hash field that is empty, truncated
// or from a scheme we don't know must never verify — least of all against an empty
// password, which is what an unpopulated field invites.
func TestVerifyPasswordFailsClosedOnBadEncodings(t *testing.T) {
	good, err := HashPassword("hunter2-secret")
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"", " ", "not-a-hash",
		"$argon2id$", "$argon2id$v=19$",
		"$argon2id$v=19$m=65536,t=3,p=2$", // no salt or hash
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$", // empty hash
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$aGFzaA", // wrong version
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",  // wrong variant
		"$argon2id$v=19$m=x,t=3,p=2$c2FsdA$aGFzaA",     // unparseable cost
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$aGFzaA",    // bad base64 salt
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$!!!",    // bad base64 hash
		"$2y$10$abcdefghijklmnopqrstuv",                // bcrypt
		good[:len(good)-4],                             // truncated real hash
		strings.Replace(good, "$argon2id$", "$argon2id$$", 1),
	}
	for _, h := range bad {
		for _, pw := range []string{"", "hunter2-secret", "anything"} {
			if VerifyPassword(h, pw) {
				t.Errorf("VerifyPassword(%q, %q) = true, want false", h, pw)
			}
		}
	}
}

// The parameters in the encoding are the ones used to verify, so raising the cost
// later still lets existing passwords in.
func TestVerifyHonoursTheEncodedParameters(t *testing.T) {
	weak, err := hashPasswordWith("hunter2-secret", argonParams{memory: 8 * 1024, time: 1, threads: 1, keyLen: 32, saltLen: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(weak, "m=8192,t=1,p=1") {
		t.Errorf("parameters not recorded: %s", weak)
	}
	// Verified with the *encoded* parameters, not today's defaults.
	if !VerifyPassword(weak, "hunter2-secret") {
		t.Error("a hash made with older parameters must still verify")
	}
	if VerifyPassword(weak, "wrong") {
		t.Error("wrong password verified against the weak hash")
	}
}

// A password long enough to be a paste of a file must not be hashed at all: argon2
// would happily chew through it and hand an attacker a cheap way to burn CPU.
func TestHashPasswordBoundsInput(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", maxPasswordLen+1)); err == nil {
		t.Error("an over-long password must be refused, not hashed")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Error("a password below the minimum length must be refused")
	}
	if _, err := HashPassword(strings.Repeat("a", minPasswordLen)); err != nil {
		t.Errorf("a password at the minimum length must be accepted: %v", err)
	}
	// Verification must bound it too, or the same DoS arrives at login.
	hash, _ := HashPassword("hunter2-secret")
	if VerifyPassword(hash, strings.Repeat("a", maxPasswordLen+1)) {
		t.Error("an over-long candidate must not verify")
	}
}

func TestHashPasswordRejectsControlBytes(t *testing.T) {
	if _, err := HashPassword("hunter2\x00hunter2"); err == nil {
		t.Error("a password containing NUL must be refused at registration")
	}
}
