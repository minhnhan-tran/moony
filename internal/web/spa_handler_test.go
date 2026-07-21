package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAHandlerStaticCaching(t *testing.T) {
	h := SPAHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if v := rec.Header().Get("Vary"); v != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", v)
	}

	// A miss shouldn't be cached.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/static/does-not-exist.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "" {
		t.Errorf("Cache-Control on a 404 = %q, want empty", cc)
	}
}

func TestSPAHandlerGzip(t *testing.T) {
	h := SPAHandler()

	req := httptest.NewRequest("GET", "/static/app.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length = %q, want stripped when gzipped", cl)
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body isn't valid gzip: %v", err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading gzip body: %v", err)
	}
	if !strings.Contains(string(plain), "{") {
		t.Errorf("decompressed body doesn't look like CSS: %q", string(plain)[:min(40, len(plain))])
	}

	// A client that explicitly disables gzip is honored.
	req = httptest.NewRequest("GET", "/static/app.css", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0, deflate")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none when gzip;q=0", enc)
	}

	// A Range request is never compressed, so byte offsets stay meaningful.
	req = httptest.NewRequest("GET", "/static/app.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for a Range request", enc)
	}

	// HEAD isn't compressed either — nothing to gain, and it'd cost the
	// Content-Length HEAD is supposed to report.
	req = httptest.NewRequest("HEAD", "/static/app.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for HEAD", enc)
	}
	if cl := rec.Header().Get("Content-Length"); cl == "" {
		t.Errorf("Content-Length missing on HEAD response")
	}
}
