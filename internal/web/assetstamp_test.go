package web

// Tests for the SPA cache-busting stamp. The bug these cover: the shell
// referenced "/static/app-core.js" with no version and it was served with
// max-age=3600, so after an upgrade browsers ran the OLD JS for up to an hour and
// new UI appeared to be missing entirely.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestIndexStampsAssetURLs(t *testing.T) {
	page, err := indexHTML()
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)

	// Every one of our own js/css refs must carry the stamp.
	bare := regexp.MustCompile(`(?:src|href)="(/static/[^"?]+\.(?:js|css))"`)
	if m := bare.FindAllStringSubmatch(html, -1); len(m) > 0 {
		t.Errorf("unstamped asset refs remain (browsers will cache them stale): %v", m[0][1])
	}

	stamped := regexp.MustCompile(`(?:src|href)="/static/[^"?]+\.(?:js|css)\?v=([0-9a-f]{12})"`)
	got := stamped.FindAllStringSubmatch(html, -1)
	if len(got) == 0 {
		t.Fatal("no stamped asset refs found in the shell")
	}
	for _, m := range got {
		if m[1] != assetStamp() {
			t.Errorf("asset stamped with %q, want the build stamp %q", m[1], assetStamp())
		}
	}
	// Sanity: the scripts we actually care about are among them.
	for _, want := range []string{"app-core.js", "app-manage.js", "app.css"} {
		if !strings.Contains(html, want+"?v="+assetStamp()) {
			t.Errorf("%s should be stamped in the shell", want)
		}
	}
}

// The stamp must be derived from asset CONTENT: that's what makes a rebuild
// invalidate the browser's copy on its own.
func TestAssetStampIsContentDerived(t *testing.T) {
	s := assetStamp()
	if len(s) != 12 {
		t.Fatalf("stamp %q: want 12 hex chars", s)
	}
	if s != assetStamp() {
		t.Error("stamp must be stable within a build (two processes serve the same shell)")
	}
	if strings.Trim(s, "0123456789abcdef") != "" {
		t.Errorf("stamp %q should be hex", s)
	}
}

func TestStampAssetsRewritesOnlyOurAssets(t *testing.T) {
	in := []byte(`<link href="/static/app.css">` +
		`<script src="/static/app-core.js"></script>` +
		`<script src="https://cdn.example.com/x.js"></script>` +
		`<script src="/static/vendor.js?v=pinned"></script>` +
		`<img src="/static/logo.png">`)
	out := string(stampAssets(in))
	v := assetStamp()

	if !strings.Contains(out, `/static/app.css?v=`+v) || !strings.Contains(out, `/static/app-core.js?v=`+v) {
		t.Error("our own js/css should be stamped")
	}
	if !strings.Contains(out, `https://cdn.example.com/x.js"`) {
		t.Error("external URLs must be left alone")
	}
	if !strings.Contains(out, `/static/vendor.js?v=pinned"`) {
		t.Error("an existing query must be left alone")
	}
	if !strings.Contains(out, `/static/logo.png"`) {
		t.Error("non js/css assets should be left alone")
	}
}

// Stamped requests are immutable; unstamped ones must revalidate so a direct hit
// still notices a rebuild. The old behaviour (max-age=3600 for everything) is
// what caused the stale UI.
func TestStaticCacheHeaders(t *testing.T) {
	h := SPAHandler()

	for _, tc := range []struct {
		name, url, want string
	}{
		{"stamped", "/static/app-core.js?v=" + assetStamp(), "immutable"},
		{"unstamped", "/static/app-core.js", "must-revalidate"},
		{"wrong stamp", "/static/app-core.js?v=deadbeefcafe", "must-revalidate"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, tc.want) {
			t.Errorf("%s: Cache-Control %q should contain %q", tc.name, cc, tc.want)
		}
		if tc.name == "stamped" && strings.Contains(cc, "max-age=3600") {
			t.Error("stamped assets should not use the old 1-hour window")
		}
	}
}

// The shell itself carries the stamp, so caching it would defeat the whole thing.
func TestShellIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	SPAHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("shell Cache-Control = %q, want no-cache", cc)
	}
	if !strings.Contains(rec.Body.String(), "?v="+assetStamp()) {
		t.Error("shell served by SPAHandler should have stamped assets")
	}
}

func TestAssetIsFingerprinted(t *testing.T) {
	for _, p := range []string{"/static/app.4f2a1c9b.js", "/static/x.0123456789ab.css"} {
		if !assetIsFingerprinted(p) {
			t.Errorf("%s should count as fingerprinted", p)
		}
	}
	for _, p := range []string{"/static/app-core.js", "/static/app.min.js", "/static/a.zz.js", "/static/marked.min.js"} {
		if assetIsFingerprinted(p) {
			t.Errorf("%s should NOT count as fingerprinted", p)
		}
	}
}
