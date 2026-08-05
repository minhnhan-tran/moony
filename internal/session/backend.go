package session

// Backend is the persistence surface the rest of the app depends on for sessions: the
// web server, the agent (autosave/title), the TUI, and the app builder all talk to a
// Backend rather than the concrete *Store, so where sessions live can change without
// touching any call site.
//
// The method set is exactly what those consumers use. File-backend-specific maintenance
// (Rewrap, Dir) is deliberately NOT on the interface — callers that need it hold the
// concrete *Store, which NewStore still returns.
type Backend interface {
	// Save persists a session as real activity: it bumps UpdatedAt and derives a title
	// if the session has none. Use it after a turn.
	Save(sess *Session) error
	// Persist writes the session exactly as given — no UpdatedAt bump, no title
	// derivation. Use it for metadata-only edits that shouldn't reshuffle
	// recency-sorted listings.
	Persist(sess *Session) error
	// Load reads a session by id.
	Load(id string) (*Session, error)
	// Delete removes a session by id.
	Delete(id string) error
	// Rename sets a session's display title without counting as activity.
	Rename(id, title string) error
	// SetPinned pins/unpins a session (surfaced at the top of its project list).
	SetPinned(id string, pinned bool) error
	// List returns all sessions, most-recently-updated first.
	List() ([]*Session, error)
	// ListByDir returns the sessions for one working dir, pinned first then by recency.
	ListByDir(workingDir string) ([]*Session, error)
	// Latest returns the most recently updated session for a working dir, or nil.
	Latest(workingDir string) (*Session, error)
	// Encrypted reports whether stored sessions are encrypted at rest.
	Encrypted() bool
	// Close releases resources held by the backend (DB handle, client connections). The
	// file store is a no-op. Long-running servers keep the backend open for the process
	// lifetime; one-shot commands should Close so any write-ahead log is flushed.
	Close() error
}

// compile-time assertion that the file store satisfies the interface.
var _ Backend = (*Store)(nil)

// Close is a no-op for the file store: each operation opens and closes its own file, so
// there is no handle to release. Present so *Store satisfies Backend, letting callers
// close a backend uniformly without knowing which one they hold.
func (s *Store) Close() error { return nil }
