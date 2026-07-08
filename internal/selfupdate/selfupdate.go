// Package selfupdate upgrades the running moony in place: it downloads the
// latest binary for the current platform from a release host, verifies its
// checksum, and atomically replaces the running executable. The /update command
// calls this, then re-execs the new binary.
//
// There is no built-in host: self-update is disabled unless SQE_UPDATE_URL is
// set to an https:// release host that serves GET /api/relay (the available
// version), GET /download/moony_<os>_<arch> (the binary), and
// GET /download/SHA256SUMS (its checksum). The download is verified against
// SHA256SUMS and install fails closed when the checksum is missing or mismatched.
// The checksum is served from the same origin as the binary, so it is not an
// independent trust anchor — integrity rests on TLS to a host you trust — which
// is why the URL must be https.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// maxDownload caps reads from the relay as a sanity guard (binaries are ~20MB).
const maxDownload = 256 << 20

// httpClient is the client used for all requests; tests swap its transport to
// serve canned responses without binding a port.
var httpClient = http.DefaultClient

// BaseURL is the configured release host from SQE_UPDATE_URL (trailing slash
// trimmed), or "" when self-update isn't configured. There is no built-in
// default host.
func BaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("SQE_UPDATE_URL")), "/")
}

// baseURL validates and returns the configured release host. Self-update is
// disabled unless SQE_UPDATE_URL is set, and the URL must be https:// — an http
// source would let an on-path attacker swap the binary, and the same-origin
// checksum can't catch that.
func baseURL() (string, error) {
	u := BaseURL()
	if u == "" {
		return "", fmt.Errorf("self-update is not configured; set SQE_UPDATE_URL to your https:// release host")
	}
	if !strings.HasPrefix(strings.ToLower(u), "https://") {
		return "", fmt.Errorf("SQE_UPDATE_URL must be an https:// URL (got %q); refusing to fetch updates over an insecure transport", u)
	}
	return u, nil
}

// AssetName is the raw binary the relay serves at /download for this platform,
// e.g. "moony_darwin_arm64" (matches the Dockerfile's /out/dl names).
func AssetName() string {
	return fmt.Sprintf("moony_%s_%s", runtime.GOOS, runtime.GOARCH)
}

// LatestVersion returns the relay's build version (no leading "v"), from the
// public GET /api/relay endpoint.
func LatestVersion(ctx context.Context) (string, error) {
	base, err := baseURL()
	if err != nil {
		return "", err
	}
	body, err := get(ctx, base+"/api/relay")
	if err != nil {
		return "", err
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("unexpected response from %s/api/relay (is the release host reachable?): %w", base, err)
	}
	if info.Version == "" {
		return "", fmt.Errorf("%s/api/relay did not report a version", base)
	}
	return strings.TrimPrefix(strings.TrimSpace(info.Version), "v"), nil
}

// Announcement mirrors internal/announce.Announcement for clients that only
// consume the public GET /api/relay feed (the TUI); JSON tags match.
type Announcement struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	Level   string `json:"level,omitempty"`
	Author  string `json:"author,omitempty"`
	Created string `json:"created,omitempty"`
}

// Announcements returns the maintainer announcement banners from the relay's
// public GET /api/relay endpoint (nil when none / relay unreachable).
func Announcements(ctx context.Context) ([]Announcement, error) {
	base, err := baseURL()
	if err != nil {
		return nil, err
	}
	body, err := get(ctx, base+"/api/relay")
	if err != nil {
		return nil, err
	}
	var info struct {
		Announcements []Announcement `json:"announcements"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	return info.Announcements, nil
}

// Apply downloads the latest binary for this platform from the relay, verifies
// its checksum, and atomically replaces the running executable. It returns the
// path it replaced; the caller should re-exec that path to run the new build.
func Apply(ctx context.Context) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved // replace the real file, not a symlink to it
	}
	return exePath, apply(ctx, exePath)
}

// apply does the download+verify+replace against an explicit target path (so it
// can be tested without touching the test binary).
func apply(ctx context.Context, exePath string) error {
	base, err := baseURL()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exePath)
	staged, err := os.CreateTemp(dir, ".moony-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (need write access to update in place): %w", dir, err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName) // no-op once renamed away

	// Stream the binary to the staged file, hashing as we go.
	h := sha256.New()
	if err := download(ctx, base+"/download/"+AssetName(), io.MultiWriter(staged, h)); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}

	// Verify against SHA256SUMS. This is served from the same origin as the
	// binary, so it is not an independent trust anchor (TLS to a trusted host is
	// the real control) — but a MISSING or mismatched checksum must fail closed
	// rather than install an unverified binary.
	sums, err := get(ctx, base+"/download/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("cannot fetch SHA256SUMS to verify the download: %w", err)
	}
	want := checksumFor(sums, AssetName())
	if want == "" {
		return fmt.Errorf("no checksum published for %s — refusing to install an unverified binary", AssetName())
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s — refusing to install", AssetName())
	}

	if err := os.Chmod(stagedName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(stagedName, exePath); err != nil {
		return fmt.Errorf("cannot replace %s: %w", exePath, err)
	}
	return nil
}

// checksumFor returns the lower-case sha256 listed for asset in a SHA256SUMS
// body ("<hex>  <name>" lines), or "" when absent.
func checksumFor(sums []byte, asset string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && filepath.Base(strings.TrimPrefix(f[len(f)-1], "*")) == asset {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// get fetches url and returns the (size-bounded) body, erroring on non-200.
func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// download streams url's body into dst, erroring on non-200.
func download(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d (no build for %s/%s?)", url, resp.StatusCode, runtime.GOOS, runtime.GOARCH)
	}
	_, err = io.Copy(dst, io.LimitReader(resp.Body, maxDownload))
	return err
}
