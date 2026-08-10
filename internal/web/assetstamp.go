package web

// assetstamp.go: cache-busting for the embedded SPA assets.
//
// The shell references its assets by bare path ("/static/app-core.js"), and they
// were served with max-age=3600. So after an upgrade a browser kept running the
// PREVIOUS JS/CSS for up to an hour unless the user knew to hard-refresh — new UI
// looked simply missing, and a stale script against a new API is worse than an
// obvious break. Rebuilding the binary can't help: the URL never changes.
//
// Fix: rewrite asset URLs in the shell to carry a ?v=<stamp> derived from the
// contents of the embedded files, and serve stamped requests as immutable. The
// stamp changes exactly when an asset changes, so upgrades invalidate themselves
// and unchanged deploys still hit cache.

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// assetStampOnce computes the stamp once per process; the embedded FS is fixed at
// build time, so it can't change underneath us.
var (
	assetStampOnce sync.Once
	assetStampVal  string
)

// assetStamp returns a short content hash over every embedded static asset.
// Hashing ALL of them (rather than one file, or the binary's mtime) means the
// stamp is stable across restarts of the same build — important because the relay
// and the connector are separate processes serving the same shell — while still
// changing whenever any asset does.
func assetStamp() string {
	assetStampOnce.Do(func() {
		h := sha256.New()
		var names []string
		_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			names = append(names, p)
			return nil
		})
		sort.Strings(names) // WalkDir is ordered, but don't depend on it for a cache key.
		for _, n := range names {
			b, err := staticFS.ReadFile(n)
			if err != nil {
				continue
			}
			h.Write([]byte(n))
			h.Write(b)
		}
		assetStampVal = hex.EncodeToString(h.Sum(nil))[:12]
	})
	return assetStampVal
}

// assetRefRe matches the src=/href= attributes pointing at our own /static/
// assets in the shell. Deliberately narrow: only absolute /static/ paths, only
// js/css, and only when there's no query already, so a hand-written ?v= or an
// external URL is left alone.
var assetRefRe = regexp.MustCompile(`(src|href)="(/static/[^"?]+\.(?:js|css))"`)

// stampAssets rewrites the shell's asset URLs to include the build stamp.
func stampAssets(page []byte) []byte {
	v := assetStamp()
	return assetRefRe.ReplaceAll(page, []byte(`$1="$2?v=`+v+`"`))
}

// indexHTML returns the SPA shell with stamped asset URLs, shared by every place
// that serves it (the connector, the relay's SPAHandler, and the live mirror) so
// they can't drift apart on caching behaviour.
func indexHTML() ([]byte, error) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		return nil, err
	}
	return stampAssets(page), nil
}

// isStampedAsset reports whether a request carries the current build stamp, in
// which case the response is safe to cache indefinitely.
func isStampedAsset(query string) bool {
	if query == "" {
		return false
	}
	for _, part := range strings.Split(query, "&") {
		if strings.TrimPrefix(part, "v=") == assetStamp() && strings.HasPrefix(part, "v=") {
			return true
		}
	}
	return false
}

// assetIsFingerprinted reports whether a path is already content-addressed by a
// vendor build (e.g. "app.4f2a1c.js"), which we also treat as immutable.
func assetIsFingerprinted(p string) bool {
	base := path.Base(p)
	parts := strings.Split(base, ".")
	if len(parts) < 3 {
		return false
	}
	hash := parts[len(parts)-2]
	if len(hash) < 8 {
		return false
	}
	for _, c := range hash {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
