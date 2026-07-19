package session

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

// clearSessionEnv unsets every BYOK/mode env var so tests are hermetic.
func clearSessionEnv(t *testing.T) {
	for _, k := range []string{
		"MN_SESSION_ENCRYPT", "MN_SESSION_KEY", "MN_SESSION_KEY_CMD",
		"MN_SESSION_KEY_FILE", "MN_SESSION_PASSPHRASE",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func sampleSession(id string) *Session {
	return &Session{ID: id, Messages: []llm.Message{llm.UserText("secret prompt " + id)}}
}

// Default mode: auto-generates a key file and encrypts; round-trips; on-disk
// bytes are ciphertext (not plaintext JSON) and 0o600.
func TestDefaultEncryptionRoundTrip(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Encrypted() {
		t.Fatal("default mode should encrypt")
	}
	if err := st.Save(sampleSession("a")); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "a.json"))
	if !isEncrypted(raw) {
		t.Fatal("on-disk file should be encrypted")
	}
	if json.Valid(raw) {
		t.Fatal("on-disk file should NOT be valid plaintext JSON")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "a.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("session perms = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, keyFileName)); err != nil {
		t.Errorf("expected auto key file: %v", err)
	}

	got, err := st.Load("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Content[0].Text != "secret prompt a" {
		t.Errorf("round-trip mismatch: %+v", got.Messages)
	}

	// A fresh store over the same dir reuses the key file and can still decrypt.
	st2, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.Load("a"); err != nil {
		t.Errorf("reopen+load failed: %v", err)
	}
}

// Legacy plaintext files load transparently and upgrade to ciphertext on save.
func TestLegacyPlaintextTransparentMigration(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	plain, _ := json.MarshalIndent(sampleSession("old"), "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "old.json"), plain, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("old")
	if err != nil {
		t.Fatalf("should read legacy plaintext: %v", err)
	}
	if err := st.Save(got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "old.json"))
	if !isEncrypted(raw) {
		t.Error("legacy file should be encrypted after re-save")
	}
}

// MN_SESSION_ENCRYPT=off writes plaintext.
func TestEncryptionOff(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("MN_SESSION_ENCRYPT", "off")
	dir := t.TempDir()
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Encrypted() {
		t.Fatal("off mode must not encrypt")
	}
	st.Save(sampleSession("p"))
	raw, _ := os.ReadFile(filepath.Join(dir, "p.json"))
	if isEncrypted(raw) || !json.Valid(raw) {
		t.Error("off mode should write plaintext JSON")
	}
}

// required mode with no key source refuses to open.
func TestEncryptRequiredNoKeyFails(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("MN_SESSION_ENCRYPT", "required")
	if _, err := NewStoreAt(t.TempDir()); err == nil {
		t.Fatal("required mode with no key should fail to open")
	}
}

// BYOK via MN_SESSION_KEY (base64 + hex), and a wrong key can't decrypt.
func TestBYOKRawKeyAndWrongKey(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	key := make([]byte, encKeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	t.Setenv("MN_SESSION_KEY", base64.StdEncoding.EncodeToString(key))
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(sampleSession("k")); err != nil {
		t.Fatal(err)
	}

	// hex form of the same key decrypts fine.
	t.Setenv("MN_SESSION_KEY", hex.EncodeToString(key))
	st2, _ := NewStoreAt(dir)
	if _, err := st2.Load("k"); err != nil {
		t.Errorf("hex form of same key should decrypt: %v", err)
	}

	// A different key is rejected with a clear wrong-key error.
	other := make([]byte, encKeyLen)
	other[0] = 0xFF
	t.Setenv("MN_SESSION_KEY", base64.StdEncoding.EncodeToString(other))
	// verifyKey should fail at open (marker was written under the first key).
	if _, err := NewStoreAt(dir); err == nil {
		t.Error("wrong key should fail the startup key check")
	}
}

// A passphrase reproduces the same key across runs via the stored salt.
func TestBYOKPassphraseStable(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	t.Setenv("MN_SESSION_PASSPHRASE", "correct horse battery staple")
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Save(sampleSession("pp"))

	st2, err := NewStoreAt(dir) // same passphrase + salt file → same key
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.Load("pp"); err != nil {
		t.Errorf("same passphrase should decrypt: %v", err)
	}
}

// Tampering with the ciphertext is detected (GCM auth).
func TestTamperDetected(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Save(sampleSession("t"))
	p := filepath.Join(dir, "t.json")
	raw, _ := os.ReadFile(p)
	raw[len(raw)-1] ^= 0xFF // flip a tag/ciphertext bit
	os.WriteFile(p, raw, 0o600)
	if _, err := st.Load("t"); err == nil {
		t.Error("tampered ciphertext should fail to decrypt")
	}
}

// Rewrap encrypts a dir of legacy plaintext sessions.
func TestRewrapEncryptsPlaintext(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	for _, id := range []string{"one", "two"} {
		plain, _ := json.MarshalIndent(sampleSession(id), "", "  ")
		os.WriteFile(filepath.Join(dir, id+".json"), plain, 0o644)
	}
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.Rewrap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rewrapped %d, want 2", n)
	}
	for _, id := range []string{"one", "two"} {
		raw, _ := os.ReadFile(filepath.Join(dir, id+".json"))
		if !isEncrypted(raw) {
			t.Errorf("%s not encrypted after rewrap", id)
		}
	}
}

func TestDecodeKey(t *testing.T) {
	key := make([]byte, encKeyLen)
	key[0] = 9
	for _, enc := range []string{
		base64.StdEncoding.EncodeToString(key),
		hex.EncodeToString(key),
	} {
		if got, err := decodeKey(enc); err != nil || len(got) != encKeyLen {
			t.Errorf("decodeKey(%q) failed: %v", enc, err)
		}
	}
	if _, err := decodeKey("tooshort"); err == nil {
		t.Error("short key should be rejected")
	}
}
