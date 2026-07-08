package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// Store persists workflows as one JSON file per workflow under a directory.
// Workflows are global (cross-project) on the machine the connector runs on, so
// the Agent Builder is usable without a project selected; execution-locality
// (running a workflow where the agent + tools live) is preserved for later phases.
type Store struct{ dir string }

// ErrNotFound is returned by Load/Delete when no workflow has the given id.
var ErrNotFound = errors.New("workflow not found")

// Open returns a Store rooted at <GlobalConfigDir>/workflows. The directory is
// created lazily on the first Save.
func Open() (*Store, error) {
	base, err := config.GlobalConfigDir()
	if err != nil {
		return nil, err
	}
	return &Store{dir: filepath.Join(base, "workflows")}, nil
}

// OpenAt returns a Store rooted at an explicit directory (used by tests).
func OpenAt(dir string) *Store { return &Store{dir: dir} }

// validID reports whether id is a safe single-segment file name. Ids are
// generated server-side, but they reach the filesystem, so validate defensively
// against traversal and separators.
func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// List returns every saved workflow, most-recently-updated first. A missing
// directory yields an empty list (not an error). Unreadable/corrupt files are
// skipped rather than failing the whole listing.
func (s *Store) List() ([]Workflow, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Workflow{}, nil
		}
		return nil, err
	}
	out := make([]Workflow, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		wf, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, *wf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Load reads one workflow by id.
func (s *Store) Load(id string) (*Workflow, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var wf Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	return &wf, nil
}

// Save writes a workflow atomically (temp file + rename), stamping UpdatedAt
// (and CreatedAt on first write). Nil node/edge slices are normalized to empty
// so the JSON always has arrays.
func (s *Store) Save(wf *Workflow) error {
	if wf == nil || !validID(wf.ID) {
		return errors.New("invalid workflow id")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if wf.CreatedAt == "" {
		wf.CreatedAt = now
	}
	wf.UpdatedAt = now
	if wf.Nodes == nil {
		wf.Nodes = []Node{}
	}
	if wf.Edges == nil {
		wf.Edges = []Edge{}
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+wf.ID+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path(wf.ID))
}

// Delete removes a workflow by id; a missing workflow returns ErrNotFound.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	if err := os.Remove(s.path(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}
