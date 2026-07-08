package attach

// iWork support: Pages/Numbers/Keynote bundles aren't Office Open XML and no Go
// library reads them, so we hand them to the SQEQCT office converter service
// (an AppleScript-driven macOS box that drives the real iWork apps) which
// returns the equivalent Office file. We then text-extract that like any other
// Office attachment, so the model sees the document's content.
//
// Reference service: github.pie.apple.com sqeqct-office-converter-service —
// POST /convert/to-office, multipart field "file"; returns the .docx/.xlsx/.pptx.

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// iworkExts maps an iWork extension to the Office extension the converter
// returns (Pages → docx, Numbers → xlsx, Keynote → pptx). It drives both
// detection and the extractor chosen for the converted bytes.
var iworkExts = map[string]string{
	".pages":   ".docx",
	".numbers": ".xlsx",
	".key":     ".pptx",
}

// defaultConverterURL is the SQEQCT office converter service. Override with the
// MOONY_CONVERTER_URL env var; set that var to empty to disable iWork
// conversion (then iWork attachments are rejected with a clear message).
const defaultConverterURL = "https://sqeqct.corp.apple.com:5052"

// converterTimeout bounds one conversion. The service launches a real iWork app
// (open → export → quit), so it's slow; keep this generous.
const converterTimeout = 2 * time.Minute

// converterClient performs the conversion upload. It's a package var so tests
// can stub its Transport (the sandbox forbids binding a real TCP port).
var converterClient = &http.Client{Timeout: converterTimeout}

func init() {
	// Opt-in escape hatch for environments where the internal converter's TLS
	// chain isn't trusted by the host. Never insecure by default.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MOONY_CONVERTER_INSECURE"))); v == "1" || v == "true" || v == "yes" {
		converterClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in for the internal converter only
		}
	}
}

// converterURL returns the configured converter base URL. An unset env var
// yields the default; an explicitly-empty env var disables conversion.
func converterURL() string {
	if v, ok := os.LookupEnv("MOONY_CONVERTER_URL"); ok {
		return strings.TrimSpace(v)
	}
	return defaultConverterURL
}

// IsIWork reports whether name is a Pages/Numbers/Keynote file. Callers (e.g.
// the terminal's /attach) use it to route the slow, network-bound conversion off
// the UI thread.
func IsIWork(name string) bool {
	_, ok := iworkExts[strings.ToLower(filepath.Ext(name))]
	return ok
}

// convertIWork uploads an iWork file to the converter's /convert/to-office and
// returns the converted Office bytes. The filename (with its iWork extension) is
// sent verbatim — the service keys the target app off it.
func convertIWork(name string, data []byte) ([]byte, error) {
	base := converterURL()
	if base == "" {
		return nil, fmt.Errorf("iWork conversion is disabled (no converter configured; set MOONY_CONVERTER_URL)")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(name))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	url := strings.TrimRight(base, "/") + "/convert/to-office"
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := converterClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("converter at %s is unreachable: %w", base, err)
	}
	defer resp.Body.Close()

	// Cap the response so a runaway conversion can't exhaust memory.
	out, _ := io.ReadAll(io.LimitReader(resp.Body, MaxOfficeBytes+1))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("converter returned %s: %s", resp.Status, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("converter returned an empty file")
	}
	if len(out) > MaxOfficeBytes {
		return nil, fmt.Errorf("converted file exceeds %s", humanSize(MaxOfficeBytes))
	}
	return out, nil
}
