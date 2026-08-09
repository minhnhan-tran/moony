package tools

// Project-scoped background jobs.
//
// A background job belongs to the PROJECT, not to the session that started it. Two
// things follow, and both are what a user expects of a dev server they left running:
//
//   - every session of the same project sees, tails and can stop the same jobs, and
//   - a job survives its session being switched away from, reloaded or evicted.
//
// Before this, each tool Registry made its own store, so a job was reachable only
// from the one session that spawned it. Worse, evicting that session dropped the only
// reference to the store while the child process kept running: the job became
// invisible AND unkillable until the process exited.
//
// The store is keyed by working directory, which is exactly what identifies a project
// elsewhere (config.ProjectPath hangs a project's state off it). Stores live for the
// life of the process; the history behind them is persisted in jobs.json (see
// jobspersist.go) so it also survives a restart.

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	jobStoresMu sync.Mutex
	jobStores   = map[string]*JobStore{}
)

// JobStoreFor returns the shared job store for a project root, creating it — and
// restoring the project's persisted job history — on first use.
//
// An empty root gets a private, non-persisted store: there is no project to attach it
// to, and sharing one global store between rootless registries would leak jobs between
// unrelated callers (tests, mostly).
func JobStoreFor(root string) *JobStore {
	if root == "" {
		return newJobStore()
	}
	key := jobStoreKey(root)

	jobStoresMu.Lock()
	defer jobStoresMu.Unlock()
	if s := jobStores[key]; s != nil {
		return s
	}
	s := newJobStore()
	// The canonical path, not the caller's: a store created from a relative root would
	// otherwise resolve its history file against whatever the cwd is at write time.
	s.root = key
	s.restore() // best effort; a missing or unreadable file just means no history
	jobStores[key] = s
	return s
}

// jobStoreKey canonicalises a root so ".", a trailing slash or a different case on
// Windows cannot produce two stores for one project.
func jobStoreKey(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	abs = filepath.Clean(abs)
	// Windows paths are case-insensitive, and the history file would be the SAME file
	// either way: two differently-cased stores would show different job lists while
	// overwriting each other's jobs.json. Symlinks are deliberately not resolved — that
	// needs the path to exist and would merge distinct mount points on macOS.
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// KillAllProjectJobs reaps every background job in the process, across projects.
//
// This is the process-exit reaper. It is deliberately separate from Registry.Close,
// which may only kill the jobs ITS registry started: the store is shared now, so a
// finishing sub-agent (or one session closing) must not stop another session's job.
// Nothing else owns these children, so the process that spawned them still has to
// reap them before it goes away.
func KillAllProjectJobs() {
	jobStoresMu.Lock()
	stores := make([]*JobStore, 0, len(jobStores))
	for _, s := range jobStores {
		stores = append(stores, s)
	}
	jobStoresMu.Unlock()

	for _, s := range stores {
		s.KillAll()
	}
	// Deliberately no save() here: a cancel is asynchronous, so at this point the
	// reapers have not yet recorded the exits and a snapshot would still say "running".
	// That is exactly the state restore() already interprets — a job that was running
	// when the process went away comes back marked as stopped by the restart.
}
