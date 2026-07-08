// Package selfupdate upgrades the running moony in place: it downloads
// the latest binary for the current platform from the relay's public download
// endpoints (the same host that serves the web UI), verifies its checksum, and
// atomically replaces the running executable. The /update command calls this,
// then re-execs the new binary.
//
// The source is the relay (default https://code.sqeqct.ops.apple.com, override
// with SQE_UPDATE_URL): GET /api/relay reports the available version and
// GET /download/moony_<os>_<arch> serves the matching binary
// (verified against GET /download/SHA256SUMS). All three are public, so no
// AppleConnect login or gh CLI is needed.
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

// BaseURL is the relay that serves the CLI downloads. Overridable via
// SQE_UPDATE_URL for forks/testing (trailing slash trimmed).
func BaseURL() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("SQE_UPDATE_URL")), "/"); u != "" {
		return u
	}
	return "https://code.sqeqct.ops.apple.com"
}

// AssetName is the raw binary the relay serves at /download for this platform,
// e.g. "moony_darwin_arm64" (matches the Dockerfile's /out/dl names).
func AssetName() string {
	return fmt.Sprintf("moony_%s_%s", runtime.GOOS, runtime.GOARCH)
}

// LatestVersion returns the relay's build version (no leading "v"), from the
// public GET /api/relay endpoint.
func LatestVersion(ctx context.Context) (string, error) {
	body, err := get(ctx, BaseURL()+"/api/relay")
	if err != nil {
		return "", err
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("unexpected response from %s/api/relay (is the relay reachable?): %w", BaseURL(), err)
	}
	if info.Version == "" {
		return "", fmt.Errorf("%s/api/relay did not report a version", BaseURL())
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
	body, err := get(ctx, BaseURL()+"/api/relay")
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
	dir := filepath.Dir(exePath)
	staged, err := os.CreateTemp(dir, ".sqe-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (need write access to update in place): %w", dir, err)
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName) // no-op once renamed away

	// Stream the binary to the staged file, hashing as we go.
	h := sha256.New()
	if err := download(ctx, BaseURL()+"/download/"+AssetName(), io.MultiWriter(staged, h)); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}

	// Verify against SHA256SUMS when the relay publishes a matching entry. A
	// missing file/entry is tolerated (the download came over HTTPS); a present
	// mismatch is fatal.
	if sums, err := get(ctx, BaseURL()+"/download/SHA256SUMS"); err == nil {
		if want := checksumFor(sums, AssetName()); want != "" {
			if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
				return fmt.Errorf("checksum mismatch for %s — refusing to install", AssetName())
			}
		}
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
