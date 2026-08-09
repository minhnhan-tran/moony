package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// jobState is the lifecycle status of a background bash job.
type jobState string

const (
	jobRunning jobState = "running"
	jobDone    jobState = "done"   // exited 0
	jobFailed  jobState = "failed" // exited non-zero
	jobKilled  jobState = "killed" // terminated by bash_kill or shutdown
)

// job is a single background shell command, its captured output, and status.
type job struct {
	id      string
	command string
	started time.Time
	// ended is when the job left the running state. Kept so the persisted history can
	// be aged out (and so a finished job's runtime is a fact rather than "now minus
	// started").
	ended time.Time
	// store is the project store this job belongs to, so a state change can persist the
	// history without the caller having to remember to. nil for a detached job.
	store *JobStore
	// restored marks a job read back from jobs.json rather than started here. Its
	// process died with the run that spawned it: there is nothing to stop, tail live or
	// type into, only a record to read and a command to re-run.
	restored bool

	mu   sync.Mutex
	buf  bytes.Buffer // combined stdout/stderr, grows as the process writes
	read int          // bytes already returned by bash_output (for incremental reads)
	// dropped counts bytes discarded from the front by trimLocked, so a viewer's
	// absolute cursor can be rebased instead of pointing into bytes that no longer exist.
	dropped int
	state   jobState
	exit    int    // exit code once finished
	errMsg  string // non-empty if the process couldn't start / was killed

	// tty is set when the command runs under a pseudo-terminal; stdin is then the
	// terminal's write end, so SendInput can type into the running program.
	tty    bool
	stdin  io.Writer
	ttyCmd *pty.Cmd // kept for the kill path, which must reach the group behind the shell
	// inputLine is what has been typed since the last Enter. It exists so a
	// caller cannot smuggle a command past the destructive-command check by
	// splitting it across calls: the check runs on the assembled line.
	inputLine string
	// writeMu is held by an in-flight write to the terminal, not by the caller
	// that started it: see SendInput.
	writeMu sync.Mutex

	cancel context.CancelFunc
}

// jobOpts are the per-job knobs the bash tool and the jobs panel expose.
type jobOpts struct {
	// tty runs the command under a pseudo-terminal instead of pipes.
	//
	// Two things change, and both matter for the "run a dev server / a script" case:
	// programs that block-buffer their output when stdout is a pipe (python, most
	// logging setups) go back to line buffering, so the log streams instead of
	// arriving in 4 KB lumps at exit; and the job gets a real keyboard, so prompts
	// can be answered with SendInput.
	tty bool
}

// Write implements io.Writer so cmd can stream combined output into the buffer
// under the lock (stdout and stderr are both pointed here).
// maxJobOutputBytes caps a single job's buffered output.
//
// The buffer lives in memory for the process's whole life, so an unbounded one is a
// denial of service: `yes`, `cat /dev/urandom`, or any fast-printing loop backgrounded
// once would grow it until the host OOM-kills the agent. 8 MB is far more than any log
// a human or model reads, and small enough that even several runaway jobs stay bounded.
const maxJobOutputBytes = 8 << 20

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n, err := j.buf.Write(p)
	j.trimLocked()
	return n, err
}

// trimLocked drops the OLDEST output once the buffer exceeds the cap, keeping the tail.
//
// The tail is what matters: a runaway job's interesting part is where it is now (and its
// eventual error), not its first megabyte. Both cursors are rebased by the amount
// dropped so neither the model's incremental reads nor a UI tail can end up pointing
// into freed bytes — a stale cursor would otherwise re-send the whole buffer or slice
// out of range. Caller holds j.mu.
func (j *job) trimLocked() {
	over := j.buf.Len() - maxJobOutputBytes
	if over <= 0 {
		return
	}
	// Drop a whole chunk rather than trimming byte-by-byte on every write: repeated
	// single-byte drops on a chatty job would be O(n²) memmoves.
	drop := over + maxJobOutputBytes/8
	if drop > j.buf.Len() {
		drop = j.buf.Len()
	}
	j.buf.Next(drop) // discards from the front
	j.dropped += drop
	if j.read > drop {
		j.read -= drop
	} else {
		j.read = 0 // the model had not caught up; it now resumes from the oldest kept byte
	}
}

// snapshot returns any output not yet read plus the current status, advancing
// the read cursor so each bash_output call yields only new bytes.
func (j *job) snapshot() (newOutput string, state jobState, exit int, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	all := j.buf.Bytes()
	if j.read < len(all) {
		newOutput = string(all[j.read:])
		j.read = len(all)
	}
	return newOutput, j.state, j.exit, j.errMsg
}

// status returns the current state without consuming buffered output, so
// callers polling for completion don't steal bytes from the next snapshot.
func (j *job) status() jobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

func (j *job) setState(s jobState, exit int, errMsg string) {
	j.mu.Lock()
	j.state = s
	j.exit = exit
	if errMsg != "" {
		j.errMsg = errMsg
	}
	if s != jobRunning && j.ended.IsZero() {
		j.ended = time.Now()
	}
	store := j.store
	j.mu.Unlock()

	// Persist outside j.mu: save() snapshots every job in the store and takes each
	// job's lock in turn, so holding this one would deadlock against itself.
	store.save()
}

// JobStore is a concurrency-safe registry of background bash jobs, shared by every
// tool Registry working in the same project (see jobscope.go): a job belongs to the
// project, not to the session that started it. Jobs are kept after they finish so the
// model can still read their final output, the history is persisted to jobs.json, and
// KillAllProjectJobs reaps survivors when the process exits.
type JobStore struct {
	mu   sync.Mutex
	jobs map[string]*job
	next int
	// root is the project working dir, and thus where the history file lives. Empty
	// for a detached store (no project, no persistence).
	root string
	// saveMu serialises history writes. Separate from mu: a save snapshots under mu,
	// releases it, then does file I/O that must not be interleaved with another save.
	saveMu sync.Mutex
}

func newJobStore() *JobStore { return &JobStore{jobs: map[string]*job{}} }

func (s *JobStore) add(j *job) {
	s.mu.Lock()
	// Set here as well as in the start paths, so a job added by any future caller can
	// still persist its own state changes.
	j.store = s
	s.jobs[j.id] = j
	s.trimLocked()
	s.mu.Unlock()
	s.save()
}

// trimLocked forgets the oldest FINISHED jobs once the store is over the cap. The store
// used to die with its registry; it now lives as long as the process and is shared by
// every session of the project, so without this a long-lived daemon accumulates every
// job it ever ran — each holding its output buffer. Running jobs are never dropped: the
// store is the only thing that can stop them.
func (s *JobStore) trimLocked() {
	if len(s.jobs) <= maxPersistedJobs {
		return
	}
	finished := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		j.mu.Lock()
		done := j.state != jobRunning
		j.mu.Unlock()
		if done {
			finished = append(finished, j)
		}
	}
	sort.Slice(finished, func(i, k int) bool { return jobSeqOf(finished[i].id) < jobSeqOf(finished[k].id) })
	for _, j := range finished {
		if len(s.jobs) <= maxPersistedJobs {
			return
		}
		delete(s.jobs, j.id)
	}
}

func (s *JobStore) get(id string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) newID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return "bg_" + strconv.Itoa(s.next)
}

// list returns jobs in start order for stable display. Sorted by the NUMBER in the id:
// a string sort puts bg_10 before bg_2, which is visible as soon as ten jobs have run —
// and a project's history now spans sessions, so that happens routinely.
func (s *JobStore) list() []*job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return jobSeqOf(out[i].id) < jobSeqOf(out[k].id) })
	return out
}

// KillAll cancels every still-running job. Safe to call on a nil store.
func (s *JobStore) KillAll() {
	if s == nil {
		return
	}
	// Collect first, cancel after releasing the store lock. A cancel can end the job
	// synchronously (the TTY path stops the pty and sets the state, which persists the
	// history and so re-enters this store), and it takes j.mu; running it under the
	// store lock would deadlock.
	s.mu.Lock()
	cancels := make([]func(), 0, len(s.jobs))
	for _, j := range s.jobs {
		j.mu.Lock()
		if j.state == jobRunning && j.cancel != nil {
			cancels = append(cancels, j.cancel)
		}
		j.mu.Unlock()
	}
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// maxJobInputBytes bounds a single SendInput call. Input is meant for answering a
// prompt or typing a command, not for streaming a file into the process.
const maxJobInputBytes = 4 << 10

// jobInputTimeout bounds how long a write to a wedged terminal may block. A var so
// tests don't have to wait it out.
var jobInputTimeout = 2 * time.Second

// SendInput types data into a running TTY job, appending a carriage return when
// enter is set (that, not "\n", is what a terminal sends for the Enter key).
//
// Only TTY jobs accept input: a pipe-backed job is started with no stdin at all, so
// programs see EOF immediately and there is nothing to write to. The terminal echoes
// what is typed, so input shows up in the job log like it would on a console.
// check, when non-nil, vets the line assembled so far and can refuse it.
func (s *JobStore) SendInput(id, data string, enter bool, check func(line string) error) error {
	if s == nil {
		return fmt.Errorf("no such background job %q", id)
	}
	j, ok := s.get(id)
	if !ok {
		return fmt.Errorf("no such background job %q", id)
	}
	if len(data) > maxJobInputBytes {
		return fmt.Errorf("input too long (%d bytes, max %d)", len(data), maxJobInputBytes)
	}
	// A job read back from the history has no process and no pty: its run ended with the
	// agent that spawned it. Checked explicitly rather than inferred from its state, so a
	// restored row can never be typed into however the states are reshuffled later.
	if j.isRestored() {
		return fmt.Errorf("job %s ended when the agent restarted; run it again to interact with it", id)
	}
	j.mu.Lock()
	running, tty, w := j.state == jobRunning, j.tty, j.stdin
	j.mu.Unlock()
	if !running {
		return fmt.Errorf("job %s is not running", id)
	}
	// Vetted before the TTY complaint so the refusal is the same answer on a host
	// without a pty as on one with it.
	if err := j.stageInput(data, enter, check); err != nil {
		return err
	}
	if !tty || w == nil {
		return fmt.Errorf("job %s has no TTY: start it with tty enabled to send input", id)
	}
	if enter {
		data += "\r"
	}
	// Written off the job lock — a full terminal buffer would otherwise block every
	// reader of this job's status behind a process that isn't consuming input — and
	// under a deadline, because a program that has stopped reading stdin must not park
	// an HTTP handler forever.
	//
	// writeMu, held by the writer goroutine rather than by this call, is what keeps
	// that honest. A write that outlives its deadline still owns the terminal, so the
	// next SendInput is refused instead of queueing behind it: at most one write is
	// ever in flight, and bytes can't reach the shell out of order after their caller
	// was told they failed. The stranded goroutine is bounded and self-clearing —
	// killing the job (or the job exiting) closes the pty, which unblocks the write.
	if !j.writeMu.TryLock() {
		return fmt.Errorf("job %s is not reading input (a previous write is still pending)", id)
	}
	done := make(chan error, 1) // buffered: the goroutine must never block on send
	go func() {
		defer j.writeMu.Unlock()
		_, err := io.WriteString(w, data)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write to job %s: %w", id, err)
		}
		return nil
	case <-time.After(jobInputTimeout):
		return fmt.Errorf("job %s is not reading input", id)
	}
}

// stageInput folds data into the job's pending input line the way a terminal would and
// hands every line it produces to check.
//
// Chunking is the reason this exists: "rm -rf" plus " /" plus Enter is the same command
// as sending it in one call, so vetting each call in isolation would vet nothing. Every
// line the shell will see is vetted — the ones an embedded CR/LF submits mid-string as
// well as the trailing remainder — because the raw data is written to the terminal
// verbatim, so anything not vetted here runs unvetted. The trailing remainder is
// checked even when it isn't submitted yet, which can flag a prefix the user was about
// to make harmless ("rm -rf /" on the way to "/tmp/x"): the safe direction to be wrong
// in, and not a case the panel produces, since it sends whole lines.
func (j *job) stageInput(data string, enter bool, check func(string) error) error {
	j.mu.Lock()
	line := j.inputLine
	j.mu.Unlock()

	var vet []string // every line this call hands to the shell
	for _, r := range data {
		switch {
		case r == 0x03 || r == 0x15: // Ctrl-C / Ctrl-U discard the line, as on a console
			line = ""
		case r == '\r' || r == '\n': // submits the line rather than dropping it
			vet = append(vet, line)
			line = ""
		case r >= 0x20 || r == '\t':
			line += string(r)
		}
		if len(line) > maxJobInputBytes {
			return fmt.Errorf("pending input line too long (max %d bytes)", maxJobInputBytes)
		}
	}
	vet = append(vet, line) // the remainder: submitted by Enter, or still being typed

	if check != nil {
		for _, l := range vet {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if err := check(l); err != nil {
				return err // refused: the pending line is left exactly as it was
			}
		}
	}

	j.mu.Lock()
	if enter {
		line = "" // the shell has the line now; start a fresh one
	}
	j.inputLine = line
	j.mu.Unlock()
	return nil
}

// start launches command as a background job under the given sandbox config,
// returning the new job. The process runs detached from the caller's context
// (it uses a fresh context cancelled only by bash_kill or KillAll) so it
// survives the tool call that spawned it.
func (s *JobStore) start(set sandboxSetting, roots []string, command string, opts jobOpts) *job {
	jctx, cancel := context.WithCancel(context.Background())
	j := &job{
		id:      s.newID(),
		command: command,
		started: time.Now(),
		state:   jobRunning,
		cancel:  cancel,
		store:   s,
	}

	name, args := sandboxCommand(set, roots, "sh", "-c", command)

	if opts.tty {
		p, err := newPTY()
		if err == nil {
			s.runTTY(j, jctx, cancel, p, name, args, roots[0])
			return j
		}
		// A missing pty is not fatal: fall back to pipes so the job still runs on
		// hosts without ConPTY/openpty. The note goes into the log the model reads,
		// so "why is this output buffered again" has an answer in the transcript.
		fmt.Fprintf(j, "(no pty available: %v — running without a TTY)\n", err)
	}

	cmd := exec.CommandContext(jctx, name, args...)
	cmd.Dir = roots[0]
	cmd.Env = scrubbedEnvFor(roots[0]) // secrets stripped; $PWD pinned to the working dir
	cmd.Stdout = j
	cmd.Stderr = j
	// Make the job killable as a unit (its own process group on Unix) and bound
	// how long Wait() may block after cancel, so grandchildren of the shell
	// (e.g. `sleep` under dash) can't keep the job alive or wedge Wait().
	configureProcess(cmd)

	s.add(j)

	if err := cmd.Start(); err != nil {
		j.setState(jobFailed, -1, fmt.Sprintf("failed to start: %v", err))
		cancel()
		return j
	}

	go func() {
		err := cmd.Wait()
		switch {
		case jctx.Err() == context.Canceled:
			j.setState(jobKilled, -1, "killed")
		case err == nil:
			j.setState(jobDone, 0, "")
		default:
			exit := -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			}
			j.setState(jobFailed, exit, err.Error())
		}
		cancel()
	}()

	return j
}
