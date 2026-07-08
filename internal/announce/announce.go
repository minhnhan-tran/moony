// Package announce is a small persisted store of maintainer announcements shown
// as an update banner in the web UI and TUI. It mirrors internal/members: the
// relay owns the authoritative store (persisted to its mounted volume via
// SQE_ANNOUNCEMENTS_FILE), and the admin-gated web handler is the only writer.
package announce

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxAnnouncements caps the stored list so it can't grow without bound; the
// oldest are dropped past this count.
const maxAnnouncements = 50

// Announcement is a single maintainer update/notice.
type Announcement struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Body    string    `json:"body,omitempty"`
	Level   string    `json:"level,omitempty"` // "info" (default), "warn", "critical"
	Author  string    `json:"author,omitempty"`
	Created time.Time `json:"created"`
}

// Store is a concurrency-safe list of announcements, newest first.
type Store struct {
	mu    sync.RWMutex
	items []Announcement
	path  string // persistence path; "" => in-memory only
}

type wire struct {
	Announcements []Announcement `json:"announcements,omitempty"`
}

// OpenAt loads announcements from an explicit path (the relay persists to its
// mounted volume, e.g. SQE_ANNOUNCEMENTS_FILE=/data/announcements.json). An empty
// path yields an in-memory-only store. Best effort: a missing/malformed file
// yields an empty store.
func OpenAt(path string) *Store {
	s := &Store{path: strings.TrimSpace(path)}
	if s.path == "" {
		return s
	}
	if data, err := os.ReadFile(s.path); err == nil {
		var w wire
		if json.Unmarshal(data, &w) == nil {
			s.items = w.Announcements
			s.sortLocked()
		}
	}
	return s
}

// List returns a copy of the announcements, newest first.
func (s *Store) List() []Announcement {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Announcement(nil), s.items...)
}

// Add records a new announcement (assigning id + created time) and persists it.
// A blank title/body is rejected. Returns the created announcement.
func (s *Store) Add(title, body, level, author string) (Announcement, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return Announcement{}, os.ErrInvalid
	}
	a := Announcement{
		ID:      newID(),
		Title:   title,
		Body:    body,
		Level:   normalizeLevel(level),
		Author:  strings.TrimSpace(author),
		Created: time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, a)
	s.sortLocked()
	if len(s.items) > maxAnnouncements {
		s.items = s.items[:maxAnnouncements]
	}
	return a, s.saveLocked()
}

// Remove deletes the announcement with the given id and persists.
func (s *Store) Remove(id string) error {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0:0]
	for _, a := range s.items {
		if a.ID != id {
			out = append(out, a)
		}
	}
	s.items = out
	return s.saveLocked()
}

func (s *Store) sortLocked() {
	sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].Created.After(s.items[j].Created) })
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(wire{Announcements: s.items}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normalizeLevel(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "warn", "warning":
		return "warn"
	case "critical", "crit", "danger":
		return "critical"
	default:
		return "info"
	}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
