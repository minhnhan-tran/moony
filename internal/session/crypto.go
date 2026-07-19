package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Session history is encrypted at rest with AES-256-GCM. By default (no BYOK
// source configured) a random 32-byte key is generated once and stored in a
// 0o600 key file next to the sessions dir, so encryption "just works" locally.
// Users can Bring Their Own Key via, in priority order:
//
//	MN_SESSION_KEY         base64 or hex 32-byte key (key never touches disk)
//	MN_SESSION_KEY_CMD     shell command whose stdout is the key (KMS/keychain/1Password/Vault…)
//	MN_SESSION_KEY_FILE    path to a file containing the key
//	MN_SESSION_PASSPHRASE  a passphrase, stretched to a key via PBKDF2-SHA256 + a stored salt
//
// MN_SESSION_ENCRYPT controls the mode:
//
//	""/"auto" (default)  encrypt with the resolved/auto key; read legacy plaintext transparently
//	"required"           refuse to start if no key can be resolved (never silently plaintext)
//	"off"                disable encryption entirely (write plaintext) — opt-out escape hatch
//
// On-disk encrypted layout: magic || version || keyID(4) || nonce(12) || ciphertext+tag.
// The keyID (a short hash of the key) lets a wrong key/passphrase be reported
// clearly and lets rotated keys coexist during a rewrap.

const (
	encMagic     = "SQEC"  // 4-byte magic marking an encrypted session file
	encVersion   = byte(1) // format version
	encKeyIDLen  = 4       // bytes of key fingerprint stored in the header
	encNonceLen  = 12      // AES-GCM standard nonce size
	encKeyLen    = 32      // AES-256
	encHeaderLen = 4 + 1 + encKeyIDLen + encNonceLen
	pbkdf2Iters  = 600_000 // OWASP-recommended floor for PBKDF2-SHA256
	saltFileName = "session-kdf-salt"
	keyFileName  = "session.key"
)

// errNoKey means no BYOK source resolved and the caller must decide whether to
// auto-generate (auto mode) or fail (required mode).
var errNoKey = errors.New("no session encryption key configured")

// cryptoConfig is the resolved encryption state for a Store.
type cryptoConfig struct {
	enabled bool   // false => write plaintext (MN_SESSION_ENCRYPT=off)
	key     []byte // 32 bytes when enabled
	keyID   [encKeyIDLen]byte
}

// keyFingerprint derives a short, non-secret id from a key so we can detect a
// wrong key and namespace rotated keys. It is a truncated SHA-256 of a domain
// separator + key (not the key itself), so it leaks nothing usable.
func keyFingerprint(key []byte) [encKeyIDLen]byte {
	h := sha256.Sum256(append([]byte("sqe-session-keyid\x00"), key...))
	var id [encKeyIDLen]byte
	copy(id[:], h[:encKeyIDLen])
	return id
}

// resolveCrypto determines the encryption mode + key for a store rooted at dir.
// It never returns a partially-initialized config: on any hard error it returns
// (nil, err) and the caller should refuse to open the store.
func resolveCrypto(dir string) (*cryptoConfig, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MN_SESSION_ENCRYPT")))
	if mode == "off" {
		return &cryptoConfig{enabled: false}, nil
	}

	key, err := resolveBYOKKey(dir)
	switch {
	case err == nil:
		// got a BYOK key
	case errors.Is(err, errNoKey):
		if mode == "required" {
			return nil, fmt.Errorf("MN_SESSION_ENCRYPT=required but %w (set MN_SESSION_KEY / _KEY_CMD / _KEY_FILE / _PASSPHRASE)", errNoKey)
		}
		// auto (default): generate/load a local key file so encryption is on by default.
		key, err = loadOrCreateKeyFile(filepath.Join(dir, keyFileName))
		if err != nil {
			return nil, fmt.Errorf("session encryption: %w", err)
		}
	default:
		return nil, fmt.Errorf("session encryption: %w", err)
	}

	if len(key) != encKeyLen {
		return nil, fmt.Errorf("session encryption: key must be %d bytes, got %d", encKeyLen, len(key))
	}
	return &cryptoConfig{enabled: true, key: key, keyID: keyFingerprint(key)}, nil
}

// resolveBYOKKey tries each user-supplied key source in priority order. It
// returns errNoKey (wrapped) when none is set, so the caller can fall back.
func resolveBYOKKey(dir string) ([]byte, error) {
	if v := strings.TrimSpace(os.Getenv("MN_SESSION_KEY")); v != "" {
		key, err := decodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_KEY: %w", err)
		}
		return key, nil
	}
	if cmd := strings.TrimSpace(os.Getenv("MN_SESSION_KEY_CMD")); cmd != "" {
		out, err := exec.Command("sh", "-c", cmd).Output()
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_KEY_CMD failed: %w", err)
		}
		key, err := decodeKey(strings.TrimSpace(string(out)))
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_KEY_CMD output: %w", err)
		}
		return key, nil
	}
	if p := strings.TrimSpace(os.Getenv("MN_SESSION_KEY_FILE")); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_KEY_FILE: %w", err)
		}
		key, err := decodeKey(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_KEY_FILE %q: %w", p, err)
		}
		return key, nil
	}
	if pass := os.Getenv("MN_SESSION_PASSPHRASE"); strings.TrimSpace(pass) != "" {
		salt, err := loadOrCreateSalt(filepath.Join(dir, saltFileName))
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_PASSPHRASE: %w", err)
		}
		key, err := pbkdf2.Key(sha256.New, pass, salt, pbkdf2Iters, encKeyLen)
		if err != nil {
			return nil, fmt.Errorf("MN_SESSION_PASSPHRASE kdf: %w", err)
		}
		return key, nil
	}
	return nil, errNoKey
}

// decodeKey accepts a 32-byte key as base64 (std or url) or hex.
func decodeKey(s string) ([]byte, error) {
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == encKeyLen {
			return b, nil
		}
	}
	return nil, fmt.Errorf("must be a %d-byte key in base64 or hex", encKeyLen)
}

// loadOrCreateKeyFile reads a 32-byte key from path, creating a random one
// (0o600) if absent. This is the default (non-BYOK) key.
func loadOrCreateKeyFile(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		key, derr := decodeKey(strings.TrimSpace(string(data)))
		if derr != nil {
			return nil, fmt.Errorf("key file %q is corrupt: %w", path, derr)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, encKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(key)
	if err := writeFile0600(path, []byte(enc)); err != nil {
		return nil, fmt.Errorf("writing key file %q: %w", path, err)
	}
	return key, nil
}

// loadOrCreateSalt reads (or creates) the per-store PBKDF2 salt so a passphrase
// reproduces the same key across runs. The salt is not secret.
func loadOrCreateSalt(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) >= 16 {
		return data, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if err := writeFile0600(path, salt); err != nil {
		return nil, err
	}
	return salt, nil
}

func writeFile0600(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isEncrypted reports whether data is an encrypted session blob (has our magic).
func isEncrypted(data []byte) bool {
	return len(data) >= encHeaderLen && string(data[:4]) == encMagic
}

// encrypt seals plaintext with AES-256-GCM under c.key, producing:
// magic || version || keyID || nonce || ciphertext+tag.
func (c *cryptoConfig) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, encHeaderLen)
	copy(header[:4], encMagic)
	header[4] = encVersion
	copy(header[5:5+encKeyIDLen], c.keyID[:])
	nonce := header[5+encKeyIDLen:]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Bind the header (incl. keyID) as additional authenticated data.
	return gcm.Seal(header, nonce, plaintext, header[:5+encKeyIDLen]), nil
}

// decrypt opens an encrypted blob. It returns a clear "wrong key" error when the
// stored key fingerprint doesn't match the configured key, and a generic auth
// error when the ciphertext fails GCM verification (wrong key or tampering).
func (c *cryptoConfig) decrypt(blob []byte) ([]byte, error) {
	if !isEncrypted(blob) {
		return nil, errors.New("not an encrypted session blob")
	}
	if blob[4] != encVersion {
		return nil, fmt.Errorf("unsupported session encryption version %d", blob[4])
	}
	var fileID [encKeyIDLen]byte
	copy(fileID[:], blob[5:5+encKeyIDLen])
	if subtle.ConstantTimeCompare(fileID[:], c.keyID[:]) != 1 {
		return nil, fmt.Errorf("wrong session key (file was encrypted with a different key %x)", fileID)
	}
	nonce := blob[5+encKeyIDLen : encHeaderLen]
	ciphertext := blob[encHeaderLen:]
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ciphertext, blob[:5+encKeyIDLen])
	if err != nil {
		return nil, fmt.Errorf("session decrypt failed (wrong key or corrupted file): %w", err)
	}
	return pt, nil
}

// keyMarkerPath is a tiny file we write once to detect a wrong BYOK key/passphrase
// at startup with a friendly message (rather than every Load failing).
func keyMarkerPath(dir string) string { return filepath.Join(dir, ".session-keycheck") }

// verifyKey writes/reads a marker so a mistyped key or passphrase is caught early.
// First run under a key writes the marker; later runs verify it decrypts.
func (c *cryptoConfig) verifyKey(dir string) error {
	if !c.enabled {
		return nil
	}
	path := keyMarkerPath(dir)
	if blob, err := os.ReadFile(path); err == nil {
		if _, derr := c.decrypt(blob); derr != nil {
			return fmt.Errorf("session key check failed: %w", derr)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	blob, err := c.encrypt([]byte("sqe-session-keycheck"))
	if err != nil {
		return err
	}
	return writeFile0600(path, blob)
}
