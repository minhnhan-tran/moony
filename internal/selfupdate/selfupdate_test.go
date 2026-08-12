package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// roundTripFunc serves canned HTTP responses by URL path, so tests exercise the
// download/verify logic without binding a port (the sandbox blocks TCP binds).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// serve installs an httpClient that answers from routes (path -> body), 404s the
// rest, and restores the client + sets MN_UPDATE_URL for the test.
func serve(t *testing.T, routes map[string]string) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := routes[r.URL.Path]
		code := http.StatusOK
		if !ok {
			code, body = http.StatusNotFound, "not found"
		}
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	t.Cleanup(func() { httpClient = old })
	t.Setenv("MN_UPDATE_URL", "https://relay.test")
}

func TestAssetName(t *testing.T) {
	want := fmt.Sprintf("moony_%s_%s", runtime.GOOS, runtime.GOARCH)
	if got := AssetName(); got != want {
		t.Errorf("AssetName() = %q, want %q", got, want)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("abc123  moony_darwin_arm64\ndef456 *some/other_file\n")
	if got := checksumFor(sums, "moony_darwin_arm64"); got != "abc123" {
		t.Errorf("checksumFor = %q, want abc123", got)
	}
	if got := checksumFor(sums, "missing"); got != "" {
		t.Errorf("checksumFor(missing) = %q, want empty", got)
	}
}

func TestLatestVersionStripsV(t *testing.T) {
	serve(t, map[string]string{"/api/relay": `{"version":"v9.9.9","downloads":true}`})
	v, err := LatestVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "9.9.9" {
		t.Errorf("LatestVersion = %q, want 9.9.9", v)
	}
}

func TestApplyReplacesBinary(t *testing.T) {
	content := "NEW-BINARY-BYTES"
	sum := sha256.Sum256([]byte(content))
	asset := AssetName()
	serve(t, map[string]string{
		"/download/" + asset:   content,
		"/download/SHA256SUMS": hex.EncodeToString(sum[:]) + "  " + asset + "\n",
	})

	dir := t.TempDir()
	exe := filepath.Join(dir, "moony")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := apply(context.Background(), exe); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != content {
		t.Errorf("after apply, exe = %q, want %q", got, content)
	}
	// Windows has no execute bit: os.Stat reports 0666 for any writable file, so
	// this can only hold on Unix — where executability is what makes the replaced
	// binary runnable at all. (This assertion failed on Windows before the
	// platform split too; it is a test-only artifact, not an install defect.)
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(exe); info.Mode().Perm()&0o100 == 0 {
			t.Error("replaced binary should be executable")
		}
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	asset := AssetName()
	serve(t, map[string]string{
		"/download/" + asset:   "REAL-BYTES",
		"/download/SHA256SUMS": strings.Repeat("0", 64) + "  " + asset + "\n",
	})
	dir := t.TempDir()
	exe := filepath.Join(dir, "moony")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	if err := apply(context.Background(), exe); err == nil {
		t.Fatal("expected a checksum-mismatch error")
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD" {
		t.Errorf("exe must be left untouched on mismatch, got %q", got)
	}
}

func TestApplyFailsWithoutChecksum(t *testing.T) {
	asset := AssetName()
	serve(t, map[string]string{ // no SHA256SUMS route -> 404
		"/download/" + asset: "SOME-BYTES",
	})
	dir := t.TempDir()
	exe := filepath.Join(dir, "moony")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	if err := apply(context.Background(), exe); err == nil {
		t.Fatal("apply must fail closed when SHA256SUMS is missing")
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD" {
		t.Errorf("exe must be left untouched when unverifiable, got %q", got)
	}
}

func TestApplyFailsWithoutChecksumEntry(t *testing.T) {
	asset := AssetName()
	serve(t, map[string]string{
		"/download/" + asset:   "SOME-BYTES",
		"/download/SHA256SUMS": "abc123  some_other_asset\n", // no entry for our asset
	})
	dir := t.TempDir()
	exe := filepath.Join(dir, "moony")
	_ = os.WriteFile(exe, []byte("OLD"), 0o755)
	if err := apply(context.Background(), exe); err == nil {
		t.Fatal("apply must fail closed when the asset has no checksum entry")
	}
}

func TestBaseURLValidation(t *testing.T) {
	t.Setenv("MN_UPDATE_URL", "")
	if _, err := baseURL(); err == nil {
		t.Error("unset update URL must disable self-update (error)")
	}
	t.Setenv("MN_UPDATE_URL", "http://insecure.test")
	if _, err := baseURL(); err == nil {
		t.Error("http:// update URL must be rejected")
	}
	t.Setenv("MN_UPDATE_URL", "https://ok.test/")
	if got, err := baseURL(); err != nil || got != "https://ok.test" {
		t.Errorf("baseURL() = %q, %v; want trimmed https URL", got, err)
	}
}
