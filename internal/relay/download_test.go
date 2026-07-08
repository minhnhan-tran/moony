package relay

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// relayWithDownloads builds a relay whose downloads dir holds stub binaries.
func relayWithDownloads(t *testing.T) *Relay {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"moony_darwin_arm64":        "ARM64BINARY",
		"moony_darwin_amd64":        "AMD64BINARY",
		"moony_linux_amd64":         "LINUXAMD64",
		"moony_linux_arm64":         "LINUXARM64",
		"SHA256SUMS":                "sums",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := newRelay()
	r.SetVersion("9.9.9")
	r.SetDownloads(dir)
	return r
}

func TestServeDownloadFile(t *testing.T) {
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/download/moony_darwin_arm64", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ARM64BINARY" {
		t.Fatalf("body = %q, want ARM64BINARY", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}

// A pre-rename client self-updates by requesting the OLD sqe-code-agent_<os>_<arch>
// asset; the relay serves the equivalently-named new binary so it can cross the
// rename in place (see the alias in serveDownloadFile).
func TestServeDownloadFileLegacyAlias(t *testing.T) {
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/download/sqe-code-agent_darwin_arm64", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (old name should alias to the new binary)", rec.Code)
	}
	if rec.Body.String() != "ARM64BINARY" {
		t.Fatalf("body = %q, want ARM64BINARY (new-named binary served under the old name)", rec.Body.String())
	}
}

// The alias only serves when a matching NEW binary exists — an old-prefixed name
// with no counterpart still 404s (not a blanket redirect).
func TestServeDownloadFileNoAliasWithoutNewBinary(t *testing.T) {
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/download/sqe-code-agent_plan9_mips", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no moony_plan9_mips to alias)", rec.Code)
	}
}

func TestServeDownloadFileRejectsBadNames(t *testing.T) {
	r := relayWithDownloads(t)
	// Traversal, empty name, and a missing file all 404 (never escape the dir).
	for _, p := range []string{"/download/../download.go", "/download/", "/download/nope"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestServeInstallScript(t *testing.T) {
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/install.sh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "shellscript") {
		t.Errorf("Content-Type = %q, want shellscript", ct)
	}
	body := rec.Body.String()
	// The base URL is woven in from the request; the script maps both OSes and
	// arch aliases, then builds the filename from $goos/$goarch.
	for _, want := range []string{"http://example.com", "Darwin", "Linux", "aarch64", "moony_${goos}_${goarch}", "/download/$file", "--progress-bar", "uname -m", ".zshrc", "SQE_NO_MODIFY_PATH"} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh missing %q", want)
		}
	}
}

func TestServeInstallScriptAutoDaemon(t *testing.T) {
	// With no tunnel advertised, the installer still installs the daemon (which
	// falls back to its built-in default relay): relay_tunnel="" and a bare
	// `daemon install` (no --relay).
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/install.sh", nil))
	body := rec.Body.String()
	for _, want := range []string{`relay_tunnel=""`, `"$dest/moony" daemon install;`} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh missing %q when no tunnel is advertised", want)
		}
	}

	// With a tunnel advertised, it bakes the address in and passes --relay.
	r.SetTunnelAddr("relay.example:8771")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/install.sh", nil))
	body = rec.Body.String()
	for _, want := range []string{`relay_tunnel="relay.example:8771"`, `"$dest/moony" daemon install --relay "$relay_tunnel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh missing %q with a tunnel set", want)
		}
	}
}

func TestServeDownloadPage(t *testing.T) {
	r := relayWithDownloads(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"v9.9.9", "/install.sh | sh", `href="/download/moony_darwin_arm64"`, `href="/download/moony_darwin_amd64"`, `href="/download/moony_linux_amd64"`, `href="/download/moony_linux_arm64"`} {
		if !strings.Contains(body, want) {
			t.Errorf("download page missing %q", want)
		}
	}
}

func TestDownloadsDisabledReturns404(t *testing.T) {
	r := newRelay() // no SetDownloads
	for _, p := range []string{"/download", "/install.sh", "/download/moony_darwin_arm64"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 when downloads are off", p, rec.Code)
		}
	}
}
