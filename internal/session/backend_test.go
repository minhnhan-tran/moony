package session

import (
	"errors"
	"sync"
	"testing"
)

// memBackend is a minimal in-memory Backend. Its existence is the point of the
// interface: if this compiles and the app can run on it, sessions are genuinely
// decoupled from the filesystem and another backend (SQL, a document store) can be
// added without touching any call site.
type memBackend struct {
	mu      sync.Mutex
	byID    map[string]*Session
	closed  bool
	saveErr error
}

func newMemBackend() *memBackend { return &memBackend{byID: map[string]*Session{}} }

func (m *memBackend) Save(s *Session) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.byID[s.ID] = &cp
	return nil
}
func (m *memBackend) Persist(s *Session) error { return m.Save(s) }
func (m *memBackend) Load(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *s
	return &cp, nil
}
func (m *memBackend) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, id)
	return nil
}
func (m *memBackend) Rename(id, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.byID[id]; ok {
		s.Title = title
	}
	return nil
}
func (m *memBackend) SetPinned(id string, pinned bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.byID[id]; ok {
		s.Pinned = pinned
	}
	return nil
}
func (m *memBackend) List() ([]*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}
func (m *memBackend) ListByDir(dir string) ([]*Session, error) {
	all, _ := m.List()
	out := []*Session{}
	for _, s := range all {
		if s.WorkingDir == dir {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *memBackend) Latest(dir string) (*Session, error) {
	byDir, _ := m.ListByDir(dir)
	if len(byDir) == 0 {
		return nil, nil
	}
	return byDir[0], nil
}
func (m *memBackend) Encrypted() bool { return false }
func (m *memBackend) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// An alternative backend must satisfy the interface without any change to the package.
var _ Backend = (*memBackend)(nil)

// The file store must satisfy the interface, since it is the default and every existing
// deployment runs on it.
func TestFileStoreSatisfiesBackend(t *testing.T) {
	var b Backend = &Store{}
	if b == nil {
		t.Fatal("unreachable")
	}
	// Close must be a no-op that succeeds: the file store holds no handle, and callers
	// close a backend uniformly without knowing which one they have.
	if err := (&Store{}).Close(); err != nil {
		t.Errorf("file store Close() = %v, want nil", err)
	}
}

// Exercise the whole interface against a non-file implementation, to catch a method
// that only works because of a filesystem side effect.
func TestBackendContract(t *testing.T) {
	run := func(t *testing.T, b Backend) {
		t.Helper()
		s := &Session{ID: "s1", WorkingDir: "/w", Title: "first"}
		if err := b.Save(s); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := b.Load("s1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.ID != "s1" || got.WorkingDir != "/w" {
			t.Errorf("round-trip lost data: %+v", got)
		}

		if err := b.Rename("s1", "renamed"); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if got, _ := b.Load("s1"); got.Title != "renamed" {
			t.Errorf("Title = %q, want renamed", got.Title)
		}

		if err := b.SetPinned("s1", true); err != nil {
			t.Fatalf("SetPinned: %v", err)
		}
		if got, _ := b.Load("s1"); !got.Pinned {
			t.Error("SetPinned did not stick")
		}

		if list, err := b.List(); err != nil || len(list) != 1 {
			t.Errorf("List = %d sessions, %v; want 1, nil", len(list), err)
		}
		if byDir, err := b.ListByDir("/w"); err != nil || len(byDir) != 1 {
			t.Errorf("ListByDir = %d, %v; want 1, nil", len(byDir), err)
		}
		// A directory with no sessions must return empty, not an error — callers treat
		// an error as "storage is broken".
		if byDir, err := b.ListByDir("/nowhere"); err != nil || len(byDir) != 0 {
			t.Errorf("ListByDir(unknown) = %d, %v; want 0, nil", len(byDir), err)
		}
		if latest, err := b.Latest("/w"); err != nil || latest == nil {
			t.Errorf("Latest = %v, %v; want a session", latest, err)
		}
		// No sessions for a dir must be (nil, nil), not an error.
		if latest, err := b.Latest("/nowhere"); err != nil || latest != nil {
			t.Errorf("Latest(unknown) = %v, %v; want nil, nil", latest, err)
		}

		if err := b.Delete("s1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := b.Load("s1"); err == nil {
			t.Error("Load succeeded after Delete")
		}
		if err := b.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}

	t.Run("memory", func(t *testing.T) { run(t, newMemBackend()) })
	t.Run("file", func(t *testing.T) {
		store, err := NewStoreAt(t.TempDir())
		if err != nil {
			t.Fatalf("NewStoreAt: %v", err)
		}
		run(t, store)
	})
}

// Load of an unknown id must report an error rather than a nil session with nil error,
// which every caller would dereference.
func TestBackendLoadUnknownIsAnError(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	for _, b := range []Backend{store, newMemBackend()} {
		s, err := b.Load("nope")
		if err == nil && s == nil {
			t.Errorf("%T: got (nil, nil); callers would nil-deref", b)
		}
	}
}
