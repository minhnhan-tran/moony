package config

// Per-project notes: a free-form markdown scratchpad the user keeps for each
// project directory, edited from the web UI's "Project memory & notes" panel and
// consumed by the agent through the "@notes" mention.
//
// Stored globally (keyed by project directory) rather than inside each project so
// notes survive a clean checkout and never risk being committed.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// notesMu serializes the read-modify-write cycles callers perform (load, mutate one
// key, save), which would otherwise interleave and lose an edit when two sessions
// on the same machine save at once.
var notesMu sync.Mutex

// projectNotesFile is the basename of the store inside GlobalConfigDir.
const projectNotesFile = "project_notes.json"

// LoadProjectNotes returns the mapping of project directory -> notes.
// A missing file is not an error: it yields an empty map, so first run behaves the
// same as "no notes yet".
func LoadProjectNotes() (map[string]string, error) {
	notesMu.Lock()
	defer notesMu.Unlock()
	return loadProjectNotesLocked()
}

func loadProjectNotesLocked() (map[string]string, error) {
	dir, err := GlobalConfigDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, projectNotesFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	var notes map[string]string
	if err := json.Unmarshal(data, &notes); err != nil {
		return nil, err
	}
	if notes == nil { // a literal "null" in the file unmarshals to a nil map
		notes = make(map[string]string)
	}
	return notes, nil
}

// SaveProjectNotes writes the mapping of project directory -> notes.
//
// The write goes to a temp file that is then renamed over the target, so a crash or
// a full disk mid-write leaves the previous notes intact rather than a truncated
// file — these are user-authored and not reconstructible.
func SaveProjectNotes(notes map[string]string) error {
	notesMu.Lock()
	defer notesMu.Unlock()

	return saveProjectNotesLocked(notes)
}

// saveProjectNotesLocked writes the store; callers must hold notesMu.
func saveProjectNotesLocked(notes map[string]string) error {
	dir, err := GlobalConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, projectNotesFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // don't leave a stray .tmp behind if the rename failed
		return err
	}
	return nil
}

// canonicalProjectDir normalizes a project directory into the single form used as
// the map key. Notes are written by the web handler (whose dir comes from the
// session store) and read by the agent (whose dir is its working dir); if those two
// disagree on relative-vs-absolute or on a trailing "/." the lookup silently misses
// and @notes appears empty. Canonicalizing on both paths keeps them in step.
func canonicalProjectDir(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(dir)
}

// SetProjectNotes stores (or, when notes is empty, removes) the notes for one
// project directory under a single lock.
//
// Callers that only touch one project should prefer this over Load+mutate+Save:
// doing it in two calls opens a window where a concurrent save is silently
// overwritten with a stale map.
func SetProjectNotes(dir, notes string) error {
	notesMu.Lock()
	defer notesMu.Unlock()

	all, err := loadProjectNotesLocked()
	if err != nil {
		return err
	}
	key := canonicalProjectDir(dir)
	if notes == "" {
		// Drop any legacy non-canonical spelling of the same project too, so a
		// clear can't leave an old key behind that keeps resurfacing.
		delete(all, key)
		delete(all, dir)
	} else {
		if dir != key {
			delete(all, dir)
		}
		all[key] = notes
	}

	return saveProjectNotesLocked(all)
}

// ProjectNotesFor returns the notes for one project directory, tolerating a
// relative/absolute mismatch between how the directory was keyed at save time and
// how the caller knows it. Errors are reported as "no notes" — callers use this on
// the message path, where failing the turn over an unreadable scratchpad is worse
// than proceeding without it.
func ProjectNotesFor(dir string) string {
	if dir == "" {
		return ""
	}
	all, err := LoadProjectNotes()
	if err != nil || all == nil {
		return ""
	}
	if n := all[canonicalProjectDir(dir)]; n != "" {
		return n
	}
	return all[dir] // fall back to a pre-canonicalization key
}
