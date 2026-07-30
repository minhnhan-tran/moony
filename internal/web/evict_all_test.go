package web

// TestEvictAllLive verifies the daemon-standdown safeguard: EvictAllLive drops
// every live session from the server, so a later re-open (by this process or
// another) always starts fresh from the last saved checkpoint instead of
// forking from an orphaned in-memory copy. See EvictAllLive's doc comment.

import (
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

func TestEvictAllLive(t *testing.T) {
	// This test builds a Server directly rather than via newTestServer, so pin the
	// same env guards: without MN_MCP=0 a real MCP subprocess can be spawned into
	// the t.TempDir() and its files break TempDir cleanup.
	t.Setenv("MN_MCP", "0")
	t.Setenv("HOME", t.TempDir())
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	srv := NewServer(store, dir, cfg, func(string, *sessionpkg.Session) *agent.Agent { return nil })

	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		s := sessionpkg.New("m", dir)
		s.ID = name
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
		if srv.openSession(s.ID) == nil {
			t.Fatalf("openSession(%s) returned nil", name)
		}
		ids = append(ids, s.ID)
	}

	srv.mu.Lock()
	openBefore := len(srv.sessions)
	srv.mu.Unlock()
	if openBefore != 3 {
		t.Fatalf("expected 3 live sessions before eviction, got %d", openBefore)
	}

	srv.EvictAllLive()

	srv.mu.Lock()
	openAfter := len(srv.sessions)
	srv.mu.Unlock()
	if openAfter != 0 {
		t.Errorf("expected 0 live sessions after EvictAllLive, got %d", openAfter)
	}

	// Each id must be re-openable (fresh from disk) after eviction, not left
	// dangling in a half-evicted state.
	for _, id := range ids {
		if srv.openSession(id) == nil {
			t.Errorf("openSession(%s) failed after EvictAllLive", id)
		}
	}
}

// TestEvictAllLiveEmpty guards against a nil/zero-value panic when nothing is live.
func TestEvictAllLiveEmpty(t *testing.T) {
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	srv := NewServer(store, dir, cfg, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.EvictAllLive() // must not panic on an empty server
}
