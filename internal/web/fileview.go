package web

// Read-only file viewer endpoints for the web UI.
//
// SECURITY: this is the only place the app serves file *contents* over HTTP, so
// every path is confined to the session's working directory (its repo root).
// That confinement is the whole point: the browser may be reaching a laptop
// across the relay, so an unconfined read would expose ~/.ssh/id_rsa,
// ~/.aws/credentials, .env files and anything else on the machine to anyone who
// can open a session. Confinement is enforced AFTER symlink resolution, so a
// symlink inside the repo cannot be used to escape it.
//
// The viewer is deliberately read-only. Writes would bypass the agent's entire
// safety model (Mutates() -> approvals -> permissions.json -> per-turn snapshots
// for Undo/Rewind) and could silently clobber a concurrent agent edit, so saving
// is not offered here.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxViewBytes caps a served file. Big enough for essentially any source file,
// small enough that a stray multi-GB artifact can't exhaust memory or the wire.
const maxViewBytes = 2 << 20 // 2 MiB

// fileView is the JSON payload for a viewed file.
type fileView struct {
	Path      string `json:"path"`                // repo-relative, forward slashes
	Abs       string `json:"abs"`                 // absolute path on the machine
	Size      int64  `json:"size"`                // true size on disk
	Content   string `json:"content,omitempty"`   // omitted for binary
	Truncated bool   `json:"truncated,omitempty"` // content stops at maxViewBytes
	Binary    bool   `json:"binary,omitempty"`    // not valid UTF-8; no content sent
	Lines     int    `json:"lines,omitempty"`
}

// fileTreeEntry is one row of a directory listing inside the workspace.
type fileTreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"` // repo-relative
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// fileTree is a confined directory listing, rooted at the workspace.
type fileTree struct {
	Root    string          `json:"root"`             // absolute workspace root
	Path    string          `json:"path"`             // repo-relative dir ("" = root)
	Parent  string          `json:"parent,omitempty"` // repo-relative, absent at root
	Entries []fileTreeEntry `json:"entries"`
}

// skipDirNames are never listed or descended: heavy build/vendor output and the
// VCS internals. Keeping them out makes the tree usable and avoids serving
// git object files.
var skipDirNames = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".build": true, "dist": true, "build": true, ".venv": true,
	"__pycache__": true, ".gradle": true, ".idea": true, "DerivedData": true,
}

// resolveInWorkspace maps a caller-supplied, repo-relative path to an absolute
// path proven to live inside root.
//
// The check runs on the RESOLVED path (symlinks followed) rather than the
// lexical one, because a symlink inside the repo pointing at ~/.ssh would pass a
// purely textual prefix test. Returns ok=false for anything outside root.
func resolveInWorkspace(root, rel string) (abs string, ok bool) {
	if root == "" {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	// Resolve the root too: on macOS /var, /tmp and $TMPDIR are symlinks to
	// /private/..., so comparing an unresolved root against a resolved child
	// would reject legitimate in-repo files.
	if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = r
	}

	rel = strings.TrimSpace(rel)
	// Reject absolute paths and Windows-style drive/UNC forms outright: callers
	// address files relative to the repo root, so an absolute path is either a
	// mistake or an escape attempt.
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", false
	}
	// filepath.Join applies Clean, collapsing ".." — but a leading ".." would
	// escape, so verify containment below rather than trusting the join.
	candidate := filepath.Join(rootAbs, filepath.FromSlash(rel))

	// Follow symlinks as far as they exist. For a path that doesn't exist yet we
	// fall back to the lexical candidate; containment is still checked.
	resolved := candidate
	if r, err := filepath.EvalSymlinks(candidate); err == nil {
		resolved = r
	}
	if !withinRoot(rootAbs, resolved) {
		return "", false
	}
	return resolved, true
}

// withinRoot reports whether p is root itself or lives beneath it. Uses path
// segments (via filepath.Rel) instead of string prefixes so "/repo-secrets"
// isn't treated as inside "/repo".
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// relForDisplay renders an absolute path as a repo-relative, slash-separated one.
func relForDisplay(root, abs string) string {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Base(abs)
	}
	if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = r
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// handleViewFile serves one file's contents for the read-only viewer, confined to
// the session's working directory.
//
// GET /api/sessions/{id}/file?path=<repo-relative path>
func (s *Server) handleViewFile(w http.ResponseWriter, r *http.Request) {
	wd, _, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rel := r.URL.Query().Get("path")
	if strings.TrimSpace(rel) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, ok := resolveInWorkspace(wd, rel)
	if !ok {
		// Deliberately identical for "outside the workspace" and "no such file":
		// a distinct error would let a caller probe for paths outside the root.
		http.Error(w, "file not found in this workspace", http.StatusNotFound)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		http.Error(w, "file not found in this workspace", http.StatusNotFound)
		return
	}
	// Only regular files: a device or FIFO could block forever on read.
	if !info.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "could not read the file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	buf, err := io.ReadAll(io.LimitReader(f, maxViewBytes))
	if err != nil {
		http.Error(w, "could not read the file", http.StatusInternalServerError)
		return
	}

	out := fileView{
		Path:      relForDisplay(wd, abs),
		Abs:       abs,
		Size:      info.Size(),
		Truncated: info.Size() > int64(len(buf)),
	}
	// Binary detection: a NUL byte, or invalid UTF-8 that isn't just a multi-byte
	// rune clipped by the size cap. Sending raw bytes would corrupt the JSON view.
	if bytesLookBinary(buf, out.Truncated) {
		out.Binary = true
	} else {
		out.Content = string(buf)
		out.Lines = strings.Count(out.Content, "\n")
		if len(out.Content) > 0 && !strings.HasSuffix(out.Content, "\n") {
			out.Lines++
		}
	}
	writeJSON(w, out)
}

// bytesLookBinary reports whether b should be treated as binary. When truncated
// is set, invalid UTF-8 at the very end is tolerated: the cap can slice a
// multi-byte rune in half, which must not mark a text file binary.
func bytesLookBinary(b []byte, truncated bool) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	if utf8.Valid(b) {
		return false
	}
	if !truncated {
		return true
	}
	// Drop up to 3 trailing bytes (max rune length - 1) and re-check.
	for i := 1; i <= 3 && i < len(b); i++ {
		if utf8.Valid(b[:len(b)-i]) {
			return false
		}
	}
	return true
}

// handleFileTree lists a directory inside the session's workspace for the
// viewer's file browser.
//
// GET /api/sessions/{id}/files?path=<repo-relative dir>
func (s *Server) handleFileTree(w http.ResponseWriter, r *http.Request) {
	wd, _, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rel := r.URL.Query().Get("path")
	abs, ok := resolveInWorkspace(wd, rel)
	if !ok {
		http.Error(w, "directory not found in this workspace", http.StatusNotFound)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		http.Error(w, "directory not found in this workspace", http.StatusNotFound)
		return
	}

	relPath := relForDisplay(wd, abs)
	if relPath == "." {
		relPath = ""
	}
	out := fileTree{Root: wd, Path: relPath, Entries: []fileTreeEntry{}}
	if relPath != "" {
		if parent := filepath.ToSlash(filepath.Dir(relPath)); parent != "." {
			out.Parent = parent
		} else {
			out.Parent = "" // the root; the client shows it as the top level
		}
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, "could not read the directory", http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != ".sqe" {
			continue // dotfiles are noise in a browser; .sqe holds project config
		}
		if e.IsDir() && skipDirNames[name] {
			continue
		}
		child := fileTreeEntry{
			Name: name,
			Path: strings.TrimPrefix(filepath.ToSlash(filepath.Join(relPath, name)), "/"),
			Dir:  e.IsDir(),
		}
		// Skip symlinks that point outside the workspace so the tree can't be
		// used to browse the rest of the machine.
		if !e.IsDir() {
			if full, ok := resolveInWorkspace(wd, child.Path); !ok {
				continue
			} else if fi, err := os.Stat(full); err == nil {
				if !fi.Mode().IsRegular() {
					continue
				}
				child.Size = fi.Size()
			}
		} else if _, ok := resolveInWorkspace(wd, child.Path); !ok {
			continue
		}
		out.Entries = append(out.Entries, child)
	}
	// Directories first, then files, each alphabetical — the conventional shape.
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	writeJSON(w, out)
}
