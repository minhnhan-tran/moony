package tools

// Persistence for the project's background-job history.
//
// The store lives in memory for the life of the process; this file is what makes a
// job list survive the process itself, so re-opening a project (or reloading a
// session's history) still shows what ran and what it printed.
//
// What is NOT attempted: resuming a job. Background jobs are children of the process
// that spawned them, so when it exits they die with it. Pretending otherwise would be
// worse than useless — a restored "running" row would offer a Stop button for a pid
// that is gone. A job that was running at exit therefore comes back marked as ended
// by the restart, with its output tail intact and the strip's restart button one click
// away.
//
// Writes happen on state changes (start, exit, kill), not on output: a chatty job
// would otherwise rewrite the file thousands of times a second. The cost is that a job
// still running when the process dies keeps only the tail it had when it started; its
// final bytes are lost. Everything that ENDED is complete, which is the case worth
// having.
//
// Limitation, deliberate: the file is last-writer-wins. Two processes on the same
// project (a daemon plus a one-shot run, or two paths to it via a symlink) each keep
// their own store and allocate ids from bg_1, so merging their histories by id is
// unsound — the later writer's snapshot replaces the earlier one's records. Nothing is
// ever killed or resumed from the file, so the blast radius is a lost log, never a lost
// or double-killed process.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

const (
	// jobsFileName is the per-project history, under the project state dir.
	jobsFileName = "jobs.json"
	// maxPersistedJobs bounds the file: the newest runs are kept.
	maxPersistedJobs = 50
	// persistedTailBytes is how much of each job's output is written. Enough to see
	// why something died, small enough that 50 of them stay a sane file.
	persistedTailBytes = 16 << 10
	// persistedMaxAge drops long-dead jobs on load, so opening a project does not
	// resurrect a wall of rows from last week.
	persistedMaxAge = 24 * time.Hour
)

// jobsFile is the on-disk shape. A struct with a version leaves room to change the
// record without a future build mistaking an old file for a corrupt one.
type jobsFile struct {
	Version int         `json:"version"`
	Jobs    []jobRecord `json:"jobs"`
}

type jobRecord struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
	State   string    `json:"state"`
	Exit    int       `json:"exit,omitempty"`
	Err     string    `json:"err,omitempty"`
	TTY     bool      `json:"tty,omitempty"`
	// Dropped is the absolute offset the tail starts at, so a viewer's cursor still
	// means the same thing after a restore as it did before (see jobsview.go).
	Dropped int    `json:"dropped,omitempty"`
	Tail    string `json:"tail,omitempty"`
}

// jobsPath is the project's history file, or "" for a store with no project.
func (s *JobStore) jobsPath() string {
	if s == nil || s.root == "" {
		return ""
	}
	return config.ProjectPathNew(s.root, jobsFileName)
}

// save writes the project's job history. Best effort: a project whose state dir cannot
// be written (read-only checkout, missing permissions) keeps working, just without
// history. Safe on a nil store and on one with no project root.
func (s *JobStore) save() {
	path := s.jobsPath()
	if path == "" {
		return
	}

	s.mu.Lock()
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()

	sort.Slice(jobs, func(i, k int) bool { return jobSeqOf(jobs[i].id) < jobSeqOf(jobs[k].id) })
	if len(jobs) > maxPersistedJobs {
		jobs = jobs[len(jobs)-maxPersistedJobs:] // keep the newest
	}

	file := jobsFile{Version: 1, Jobs: make([]jobRecord, 0, len(jobs))}
	for _, j := range jobs {
		file.Jobs = append(file.Jobs, j.record())
	}

	blob, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	// Only write inside a project that still exists. A job's reaper saves when the
	// process exits, which can be after the directory has been deleted (a removed
	// checkout, or a test's temp dir): re-creating the state dir there would resurrect a
	// tree someone is in the middle of deleting.
	if _, err := os.Stat(s.root); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	// One writer at a time. Every job's reaper goroutine calls save() when it exits, so
	// two jobs finishing together would otherwise race: with a shared temp name they
	// interleave writes into the same file and the loser's rename can publish a
	// truncated snapshot.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	// Unique temp file + rename, so a reader never sees a half-written history.
	tmp, err := os.CreateTemp(filepath.Dir(path), "jobs-*.json")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, werr := tmp.Write(blob)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return
	}
	// 0600: the tail can contain whatever a command printed, including secrets.
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
	}
}

// record snapshots one job for the history file.
func (j *job) record() jobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()

	tail := j.buf.Bytes()
	dropped := j.dropped
	if len(tail) > persistedTailBytes {
		dropped += len(tail) - persistedTailBytes
		tail = tail[len(tail)-persistedTailBytes:]
	}
	return jobRecord{
		ID:      j.id,
		Command: j.command,
		Started: j.started,
		Ended:   j.ended,
		State:   string(j.state),
		Exit:    j.exit,
		Err:     j.errMsg,
		TTY:     j.tty,
		Dropped: dropped,
		Tail:    string(tail),
	}
}

// restore loads the project's history into an empty store. Called once, before the
// store is published to other goroutines, so it takes no locks of its own.
func (s *JobStore) restore() {
	path := s.jobsPath()
	if path == "" {
		return
	}
	// Read through ProjectPath as well, so a project still on the legacy state dir
	// keeps its history when the directory is migrated.
	blob, err := os.ReadFile(path)
	if err != nil {
		if blob, err = os.ReadFile(config.ProjectPath(s.root, jobsFileName)); err != nil {
			return
		}
	}
	var file jobsFile
	if err := json.Unmarshal(blob, &file); err != nil {
		return // a corrupt history is not worth failing a session over
	}

	now := time.Now()
	if len(file.Jobs) > maxPersistedJobs {
		file.Jobs = file.Jobs[len(file.Jobs)-maxPersistedJobs:] // newest, as written
	}
	for _, rec := range file.Jobs {
		if rec.ID == "" {
			continue
		}
		when := rec.Ended
		if when.IsZero() {
			when = rec.Started
		}
		if !when.IsZero() && now.Sub(when) > persistedMaxAge {
			continue
		}

		j := &job{
			id:      rec.ID,
			command: rec.Command,
			started: rec.Started,
			ended:   rec.Ended,
			state:   jobState(rec.State),
			exit:    rec.Exit,
			errMsg:  rec.Err,
			tty:     rec.TTY,
			dropped: rec.Dropped,
			store:   s,
			// restored is what stops the UI offering to stop or type into a job whose
			// process no longer exists.
			restored: true,
		}
		j.buf.WriteString(rec.Tail)
		// The model has not read any of this in THIS process, so let it read the tail
		// rather than starting at the end and reporting nothing.
		j.read = 0

		// A job that was running when the process exited did not survive it. Say so
		// instead of showing a live row for a dead pid.
		if j.state == jobRunning || j.state == "" {
			j.state = jobKilled
			j.exit = -1
			j.errMsg = "the agent exited while this job was running, so the process was stopped"
			if j.ended.IsZero() {
				j.ended = when
			}
		}
		s.jobs[j.id] = j

		// New ids must not collide with restored ones.
		if n := jobSeqOf(j.id); n > s.next {
			s.next = n
		}
	}
}

// isRestored reports whether this job came from the history file rather than from a
// process this run started.
func (j *job) isRestored() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.restored
}

// jobSeqOf is the number in a "bg_N" id, or 0. Ids sort correctly by it, which a
// string sort does not do once ten jobs have run (bg_10 < bg_2).
func jobSeqOf(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "bg_"))
	if err != nil {
		return 0
	}
	return n
}
