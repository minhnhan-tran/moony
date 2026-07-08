package web

// Filesystem + "open a session in any folder" endpoints. These power the web
// UI's folder picker so a session can be cold-started in an arbitrary directory
// on a machine — the key to driving an idle, daemon-connected laptop from the
// browser (otherwise only folders with prior sessions show up in the tree).
//
// Only directory *names* are ever returned (never file contents), and the
// browser user is already the machine's owner under the relay's per-user
// isolation, so listing the owner's own directories is in scope.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sessionpkg "github.com/pysty1995/moony/internal/session"
)

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type dirListing struct {
	Dir     string     `json:"dir"`
	Parent  string     `json:"parent,omitempty"`
	Home    string     `json:"home,omitempty"`
	Entries []dirEntry `json:"entries"`
}

// handleListDir lists the immediate subdirectories of a directory on this
// machine for the "new session in folder" picker. With no dir (or "~") it starts
// at the home directory; dotfiles are skipped to reduce noise.
func (s *Server) handleListDir(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	dir := expandHome(strings.TrimSpace(r.URL.Query().Get("dir")), home)
	if dir == "" {
		dir = home
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		http.Error(w, "not a directory: "+abs, http.StatusBadRequest)
		return
	}

	out := dirListing{Dir: abs, Home: home, Entries: []dirEntry{}}
	if parent := filepath.Dir(abs); parent != abs {
		out.Parent = parent
	}
	if entries, err := os.ReadDir(abs); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			out.Entries = append(out.Entries, dirEntry{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
		})
	}
	writeJSON(w, out)
}

// handleOpenDir creates and opens a new session rooted at an arbitrary existing
// directory on this machine — the web equivalent of launching the agent in a
// folder. Returns the new session id plus the project key so the UI can navigate
// to it.
func (s *Server) handleOpenDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	home, _ := os.UserHomeDir()
	abs, err := resolveNewSessionDir(body.Dir, home)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess := sessionpkg.New(s.cfg.Model, abs)
	if err := s.store.Save(sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.openSession(sess.ID) == nil {
		http.Error(w, "could not open the new session", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": sess.ID, "key": projectKey(abs), "dir": abs})
}

// expandHome resolves a leading "~" / "~/" to the home directory.
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// resolveNewSessionDir resolves the target folder for a new session. If the
// folder doesn't exist yet it is CREATED so a session can be cold-started in a
// brand-new project folder — but only ONE level under an already-existing parent
// (os.Mkdir, not MkdirAll), so a typo in the parent path errors instead of
// silently making a deep junk tree. An existing regular FILE (not a directory)
// is rejected. Returns the absolute path, or a user-facing error.
func resolveNewSessionDir(dir, home string) (string, error) {
	dir = expandHome(strings.TrimSpace(dir), home)
	if dir == "" {
		return "", errors.New("a directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("bad path")
	}
	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && info.IsDir():
		return abs, nil // already a usable directory
	case statErr == nil:
		return "", fmt.Errorf("not a directory: %s", abs)
	case !os.IsNotExist(statErr):
		return "", fmt.Errorf("bad path: %s", abs)
	}
	// Doesn't exist: create it, but only if its parent already exists.
	if pi, perr := os.Stat(filepath.Dir(abs)); perr != nil || !pi.IsDir() {
		return "", fmt.Errorf("can't create %s: its parent folder doesn't exist", abs)
	}
	if err := os.Mkdir(abs, 0o755); err != nil {
		return "", fmt.Errorf("could not create folder: %v", err)
	}
	return abs, nil
}
