// Package schedule lets users register prompts that run headlessly on a
// recurring or one-shot basis. A schedule pairs a trigger (every <dur>, daily at
// <HH:MM>, or once at <time>) with a prompt and a working directory; when due,
// the daemon (or `moony schedule tick`) runs the prompt non-interactively
// by invoking the binary's headless `-p` mode, which persists a normal session
// that's then browsable in the web UI.
package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// Kind is a schedule's trigger type.
type Kind string

const (
	KindInterval Kind = "interval" // every IntervalSec seconds
	KindDaily    Kind = "daily"    // every day at AtMinute (minutes since midnight)
	KindOnce     Kind = "once"     // a single run at At
)

// runTimeout bounds a single scheduled headless run.
const runTimeout = 30 * time.Minute

// Schedule is one registered recurring/one-shot prompt.
type Schedule struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Kind        Kind      `json:"kind"`
	IntervalSec int       `json:"interval_sec,omitempty"`
	AtMinute    int       `json:"at_minute,omitempty"`
	At          time.Time `json:"at,omitempty"`
	Prompt      string    `json:"prompt"`
	WorkingDir  string    `json:"working_dir,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	LastRun     time.Time `json:"last_run,omitempty"`
	LastStatus  string    `json:"last_status,omitempty"`
	NextRun     time.Time `json:"next_run,omitempty"`
}

// Due reports whether the schedule should fire at now.
func (s Schedule) Due(now time.Time) bool {
	return s.Enabled && !s.NextRun.IsZero() && !now.Before(s.NextRun)
}

// nextAfter computes the next fire time strictly after now.
func (s Schedule) nextAfter(now time.Time) time.Time {
	switch s.Kind {
	case KindInterval:
		d := time.Duration(s.IntervalSec) * time.Second
		if d <= 0 {
			d = time.Minute
		}
		return now.Add(d)
	case KindDaily:
		t := time.Date(now.Year(), now.Month(), now.Day(), s.AtMinute/60, s.AtMinute%60, 0, 0, now.Location())
		for !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return t
	case KindOnce:
		return s.At
	}
	return time.Time{}
}

// advance records a run at now and schedules the next fire (a one-shot disables
// itself after running).
func (s *Schedule) advance(now time.Time) {
	s.LastRun = now
	if s.Kind == KindOnce {
		s.Enabled = false
		s.NextRun = time.Time{}
		return
	}
	s.NextRun = s.nextAfter(now)
}

// Describe renders a one-line human description of the trigger.
func (s Schedule) Describe() string {
	switch s.Kind {
	case KindInterval:
		return "every " + (time.Duration(s.IntervalSec) * time.Second).String()
	case KindDaily:
		return fmt.Sprintf("daily at %02d:%02d", s.AtMinute/60, s.AtMinute%60)
	case KindOnce:
		return "once at " + s.At.Format(time.RFC3339)
	}
	return string(s.Kind)
}

// StateLabel is "enabled" or "disabled", for display.
func (s Schedule) StateLabel() string {
	if s.Enabled {
		return "enabled"
	}
	return "disabled"
}

// DisplayName returns the schedule's name, or "(unnamed)" when it has none.
func (s Schedule) DisplayName() string {
	if strings.TrimSpace(s.Name) == "" {
		return "(unnamed)"
	}
	return s.Name
}

// Summary renders a compact one-line summary (name, state, trigger) for use as a
// menu-item label.
func (s Schedule) Summary() string {
	return fmt.Sprintf("%s  [%s]  %s", s.DisplayName(), s.StateLabel(), s.Describe())
}

// FormatList renders schedules as a multi-line block shared by the CLI, the TUI
// and the web UI. It returns "" for an empty list so callers can show their own
// "no schedules" hint.
func FormatList(list []Schedule) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range list {
		next := "—"
		if !s.NextRun.IsZero() {
			next = s.NextRun.Format(time.RFC1123)
		}
		fmt.Fprintf(&b, "%s  %s  [%s]  %s\n", s.ID, s.DisplayName(), s.StateLabel(), s.Describe())
		fmt.Fprintf(&b, "    next: %s   dir: %s\n", next, s.WorkingDir)
		fmt.Fprintf(&b, "    prompt: %.80s\n", s.Prompt)
		if s.LastStatus != "" {
			last := s.LastRun.Format(time.RFC1123)
			fmt.Fprintf(&b, "    last: %s (%s)\n", s.LastStatus, last)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ParseSpec parses a trigger spec into a partial Schedule (Kind + trigger
// fields). Supported: "every <dur>" (30s/15m/2h/1d), "daily <HH:MM>",
// "once <RFC3339 or 2006-01-02T15:04>".
func ParseSpec(spec string) (Schedule, error) {
	f := strings.Fields(strings.TrimSpace(spec))
	if len(f) < 2 {
		return Schedule{}, fmt.Errorf("spec must be: every <dur> | daily <HH:MM> | once <time>")
	}
	switch strings.ToLower(f[0]) {
	case "every":
		d, err := parseDuration(f[1])
		if err != nil {
			return Schedule{}, err
		}
		if d < time.Minute {
			return Schedule{}, fmt.Errorf("interval must be at least 1 minute")
		}
		return Schedule{Kind: KindInterval, IntervalSec: int(d / time.Second)}, nil
	case "daily":
		hh, mm, err := parseHHMM(f[1])
		if err != nil {
			return Schedule{}, err
		}
		return Schedule{Kind: KindDaily, AtMinute: hh*60 + mm}, nil
	case "once", "at":
		t, err := parseAt(f[1])
		if err != nil {
			return Schedule{}, err
		}
		if !t.After(time.Now()) {
			return Schedule{}, fmt.Errorf("one-shot time %q is in the past", t.Format(time.RFC3339))
		}
		return Schedule{Kind: KindOnce, At: t}, nil
	}
	return Schedule{}, fmt.Errorf("unknown schedule type %q (use: every | daily | once)", f[0])
}

// parseDuration accepts Go durations plus a "d" (day) suffix.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid day duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (e.g. 30m, 2h, 1d)", s)
	}
	return d, nil
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	hh, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	mm, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("invalid time %q (want HH:MM, 24-hour)", s)
	}
	return hh, mm, nil
}

func parseAt(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want RFC3339 or 2006-01-02T15:04)", s)
}

// Store persists schedules as JSON.
type Store struct{ path string }

// Open returns the default store at ~/.config/moony/schedules.json.
func Open() (*Store, error) {
	p, err := config.GlobalConfigPath("schedules.json")
	if err != nil {
		return nil, err
	}
	return &Store{path: p}, nil
}

// OpenAt returns a store backed by an explicit path (tests).
func OpenAt(path string) *Store { return &Store{path: path} }

// Load reads all schedules (empty slice when the file is missing).
func (st *Store) Load() ([]Schedule, error) {
	data, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Schedule
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// save atomically writes list to disk. It is NOT internally synchronized across
// processes; callers that perform a read-modify-write cycle must hold withLock.
// The file is written 0600 (and its dir 0700) because prompts may carry
// sensitive instructions or paths, then durably flushed and renamed into place
// so a reader never observes a partial file and a crash can't leave a truncated
// one.
func (st *Store) save(list []Schedule) error {
	dir := filepath.Dir(st.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "schedules-*.json.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// lockPath is the sidecar advisory-lock file guarding read-modify-write cycles
// on the store.
func (st *Store) lockPath() string { return st.path + ".lock" }

// withLock runs fn while holding an exclusive, cross-process advisory lock on the
// store. This serializes load→modify→save cycles so the daemon's scheduler and a
// concurrent CLI (e.g. `schedule run`/`tick`, or another `schedule add`) can't
// interleave and double-fire a schedule or clobber each other's writes. Locking
// is best-effort: if the lock file can't be created or locked, fn still runs
// (degrading to unsynchronized behavior) rather than disabling scheduling.
//
// The lock is a POSIX flock and therefore advisory: it only serializes other
// callers that also go through withLock, and it may be a no-op on some network
// filesystems (e.g. NFS without lockd). The single-process daemon path is always
// safe; for the cross-process case, the `schedule` usage text warns against
// running `tick`/`run` from cron while the daemon is already firing schedules.
//
// fn must use the unlocked primitives (Load/save) directly and must NOT call the
// public lock-taking methods, which would deadlock on the same lock.
func (st *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(st.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fn()
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fn()
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// Add assigns an id, enables the schedule, computes its first run, and saves it.
func (st *Store) Add(s Schedule) (Schedule, error) {
	err := st.withLock(func() error {
		list, err := st.Load()
		if err != nil {
			return err
		}
		s.ID = newID()
		s.Enabled = true
		s.CreatedAt = time.Now()
		s.NextRun = s.nextAfter(time.Now())
		list = append(list, s)
		return st.save(list)
	})
	if err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// Get returns the schedule with id.
func (st *Store) Get(id string) (Schedule, bool) {
	list, _ := st.Load()
	for _, s := range list {
		if s.ID == id {
			return s, true
		}
	}
	return Schedule{}, false
}

// Update replaces the schedule with the same id and saves.
func (st *Store) Update(s Schedule) error {
	return st.withLock(func() error {
		list, err := st.Load()
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == s.ID {
				list[i] = s
				return st.save(list)
			}
		}
		return fmt.Errorf("schedule %s not found", s.ID)
	})
}

// Remove deletes the schedule with id.
func (st *Store) Remove(id string) error {
	return st.withLock(func() error {
		list, err := st.Load()
		if err != nil {
			return err
		}
		out := list[:0]
		found := false
		for _, s := range list {
			if s.ID == id {
				found = true
				continue
			}
			out = append(out, s)
		}
		if !found {
			return fmt.Errorf("schedule %s not found", id)
		}
		return st.save(out)
	})
}

// SetEnabled toggles a schedule, recomputing its next run when re-enabled.
func (st *Store) SetEnabled(id string, enabled bool) error {
	return st.withLock(func() error {
		list, err := st.Load()
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID != id {
				continue
			}
			list[i].Enabled = enabled
			if enabled && list[i].NextRun.IsZero() && list[i].Kind != KindOnce {
				list[i].NextRun = list[i].nextAfter(time.Now())
			}
			return st.save(list)
		}
		return fmt.Errorf("schedule %s not found", id)
	})
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405-") + hex.EncodeToString(b[:])
}

// Run executes one schedule's prompt headlessly by invoking exe in -p/--json
// mode, returning the session id the run produced.
func Run(ctx context.Context, s Schedule, exe string) (string, error) {
	args := []string{"-p", s.Prompt, "--json"}
	if s.WorkingDir != "" {
		args = append(args, "-C", s.WorkingDir)
	}
	cctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, exe, args...).Output()
	if err != nil {
		return "", err
	}
	var r struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"sessionId"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(out, &r)
	if !r.OK && r.Error != "" {
		return r.SessionID, fmt.Errorf("%s", r.Error)
	}
	return r.SessionID, nil
}

// RunDue runs every schedule due at now, sequentially. The whole claim step
// (load → find due → advance → persist) happens under withLock so neither the
// daemon's next tick nor a concurrent process can observe the same slot as due
// and double-fire it; the actual runs happen outside the lock (a run can take
// minutes), and each outcome is recorded back under the lock.
func RunDue(ctx context.Context, st *Store, exe string, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	now := time.Now()

	// Phase 1 — atomically claim due schedules by advancing and persisting them
	// before anything runs.
	var due []Schedule
	if err := st.withLock(func() error {
		list, err := st.Load()
		if err != nil {
			return err
		}
		changed := false
		for i := range list {
			if !list[i].Due(now) {
				continue
			}
			due = append(due, list[i]) // snapshot to run
			list[i].advance(now)       // reserve the slot
			changed = true
		}
		if !changed {
			return nil
		}
		return st.save(list)
	}); err != nil {
		logf("scheduler: reserve failed: %v", err)
		return
	}

	// Phase 2 — run each claimed schedule outside the lock, then record only its
	// LastRun/LastStatus under the lock (leaving the already-advanced NextRun and
	// Enabled state untouched, and no-op'ing if it was removed mid-run).
	for _, s := range due {
		logf("scheduler: running %s (%s): %.60q", s.ID, s.Describe(), s.Prompt)
		sid, runErr := Run(ctx, s, exe)
		done := time.Now()
		status := "ok (session " + sid + ")"
		if runErr != nil {
			status = "error: " + runErr.Error()
			logf("scheduler: %s failed: %v", s.ID, runErr)
		} else {
			logf("scheduler: %s -> session %s", s.ID, sid)
		}
		id := s.ID
		_ = st.withLock(func() error {
			list, err := st.Load()
			if err != nil {
				return err
			}
			for i := range list {
				if list[i].ID == id {
					list[i].LastRun = done
					list[i].LastStatus = status
					return st.save(list)
				}
			}
			return nil
		})
	}
}
