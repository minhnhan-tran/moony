package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/pysty1995/moony/internal/procutil"
)

// Pseudo-terminal backing for background jobs (`bash(background:true, tty:true)` and
// the jobs panel's "TTY" switch).
//
// Why a real terminal and not just pipes: the two workloads people actually background
// — a dev server and a long script — both behave badly on a pipe. libc and Python
// switch stdout from line- to block-buffered when it isn't a terminal, so a Django
// server's request log arrives in 4 KB lumps (or not until it exits); tools drop their
// colours; and anything that asks a question has no keyboard to answer it with. Under a
// pty the process sees a terminal, so output streams line by line and SendInput can type.
//
// The cost is that the stream now carries what a terminal emulator would have eaten:
// cursor moves, colour codes, CRLF line ends, and — on Windows ConPTY, which renders a
// virtual screen — padding to the window width. ttyFilter strips that back down to plain
// text before it reaches the buffer, because the same buffer is what `bash_output` feeds
// to the model: escape codes there are pure token cost and confusion.

const (
	// ttyCols/ttyRows size the virtual terminal. Wider than the usual 80 columns on
	// purpose: ConPTY hard-wraps at the window width, and log lines with timestamps
	// and paths are routinely longer than that.
	ttyCols = 200
	ttyRows = 50

	// ttyDrainGrace is how long the reader gets to pick up what a process wrote just
	// before exiting, before the pty is closed. Closing it is what unblocks the
	// reader, but on Windows the console buffer outlives the process, so closing the
	// instant Wait() returns truncates the last lines.
	ttyDrainGrace = 300 * time.Millisecond

	// maxTTYStringSeq bounds an OSC/DCS sequence before the filter gives up waiting
	// for its terminator and goes back to emitting text.
	maxTTYStringSeq = 512
)

// newPTY opens a pseudo-terminal sized for log output. Callers must treat a failure as
// "this host has no pty" and fall back to pipes: go-pty returns ErrUnsupported on
// platforms without ConPTY or openpty.
func newPTY() (pty.Pty, error) {
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	// Best-effort: a terminal that refuses to resize is still a usable terminal.
	_ = p.Resize(ttyCols, ttyRows)
	return p, nil
}

// runTTY starts the job's command attached to p and wires up the reader/waiter
// goroutines. It never returns an error: a command that fails to start is recorded on
// the job itself, exactly like the pipe path, so the caller always gets a job to show.
func (s *JobStore) runTTY(j *job, jctx context.Context, cancel context.CancelFunc, p pty.Pty, name string, args []string, dir string) {
	// Killing a pty job means both cancelling the process and closing the terminal.
	// The close matters twice over: it unblocks the reader, and on Unix hanging up the
	// line sends SIGHUP to the foreground process group, which takes the shell's
	// children with it instead of leaving an orphaned server holding the port.
	// Idempotent on purpose: stop is reachable from the waiter, from KillJob and from
	// KillAll at shutdown, and closing a pseudo-console twice corrupts the heap on
	// Windows (ClosePseudoConsole frees handles the second call frees again).
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Refuse further input first. A write that slips through is harmless —
			// both pty ends are *os.File, so Go's descriptor refcounting turns a
			// racing write into ErrClosed rather than a write to a recycled handle —
			// but there is nothing left to type at.
			j.mu.Lock()
			c := j.ttyCmd
			j.stdin = nil
			j.mu.Unlock()

			cancel()
			// cancel() only reaches the shell. Kill the process GROUP too, or a
			// `manage.py runserver` that reloads itself into a child survives the
			// kill and keeps the port. go-pty starts the child with setsid, so it
			// leads its own group; on Windows this degrades to killing the child.
			if c != nil && c.Process != nil {
				procutil.Terminate(c.Process)
			}
			_ = p.Close()
		})
	}

	c := p.CommandContext(jctx, ttyLookPath(name), args...)
	j.mu.Lock()
	j.tty = true
	j.stdin = p
	j.ttyCmd = c
	j.cancel = stop
	j.mu.Unlock()

	c.Dir = dir
	// TERM advertises the terminal to the child; without it curses-style tools bail
	// out with "unknown terminal type" even though they now have a tty.
	c.Env = append(scrubbedEnvFor(dir), "TERM=xterm-256color")

	s.add(j)

	if err := c.Start(); err != nil {
		j.setState(jobFailed, -1, fmt.Sprintf("failed to start: %v", err))
		stop()
		return
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		f := &ttyFilter{w: j}
		_, _ = io.Copy(f, p)
		f.Flush()
	}()

	go func() {
		err := c.Wait()
		// Sampled BEFORE stop(), which cancels jctx: reading jctx.Err() afterwards
		// would report every job — including a clean exit — as killed.
		killed := jctx.Err() != nil
		select {
		case <-drained:
		case <-time.After(ttyDrainGrace):
		}
		stop()
		// Bounded: a wedged reader must not leak this goroutine or delay the final
		// state the panel is polling for.
		select {
		case <-drained:
		case <-time.After(ttyDrainGrace):
		}

		switch {
		case killed:
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
	}()
}

// ttyLookPath resolves a bare command name against PATH.
//
// exec.Command does this for us; pty.Cmd does not — it takes the name verbatim and
// then resolves it RELATIVE TO THE WORKING DIRECTORY, so an unqualified "sh" turns into
// "<workdir>/sh" and every TTY job dies with "executable file not found". Failures fall
// through unchanged so the start error names the command the caller actually asked for.
func ttyLookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// ttyFilter rewrites a terminal stream into the plain text a job log wants: escape
// sequences removed, CRLF collapsed to LF, and the trailing spaces ConPTY pads lines
// with dropped. It is a stream filter, so state (a half-seen escape sequence, a pending
// run of spaces) carries across Write calls.
type ttyFilter struct {
	w io.Writer

	st      ttyState
	spaces  int  // run of spaces held back: discarded at end of line, flushed otherwise
	strLen  int  // bytes consumed by the current OSC/DCS-style sequence
	cr      bool // saw a bare CR; it becomes a newline unless an LF follows
	started bool // emitted anything yet? until then, blank lines are dropped
}

type ttyState int

const (
	ttyText    ttyState = iota
	ttyEsc              // just saw ESC
	ttyCSI              // ESC [ … terminated by a byte in 0x40–0x7E
	ttyString           // ESC ] / P / X / ^ / _ … terminated by BEL or ESC \
	ttyStrEsc           // saw ESC inside a string sequence: a following \ ends it
	ttyCharset          // ESC ( ) * + : one more byte to swallow
)

func (f *ttyFilter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch f.st {
		case ttyEsc:
			switch b {
			case '[':
				f.st = ttyCSI
			case ']', 'P', 'X', '^', '_':
				f.st, f.strLen = ttyString, 0
			case '(', ')', '*', '+':
				f.st = ttyCharset
			case 0x1b: // ESC ESC: stay armed rather than emitting a stray escape
			default:
				f.st = ttyText
			}
			continue
		case ttyCSI:
			if b >= 0x40 && b <= 0x7e {
				f.st = ttyText
			}
			continue
		case ttyString:
			// Bounded on purpose: a program that dies mid-OSC (or a corrupt byte that
			// lands us here) would otherwise blackhole every remaining line of output.
			f.strLen++
			switch {
			case b == 0x07:
				f.st = ttyText
			case b == 0x1b:
				f.st = ttyStrEsc
			case b == '\n' || f.strLen > maxTTYStringSeq:
				f.st = ttyText
			}
			continue
		case ttyStrEsc:
			// Only ESC \ terminates; anything else is still part of the string.
			f.st = ttyText
			if b != '\\' {
				f.st = ttyString
				f.strLen++
			}
			continue
		case ttyCharset:
			f.st = ttyText
			continue
		}

		switch b {
		case 0x1b:
			f.st = ttyEsc
		case '\r':
			// Held: CRLF is one line ending, and a lone CR is a line rewrite
			// (progress bars, spinners) that reads best as its own line.
			f.cr = true
		case '\n':
			f.cr = false
			f.spaces = 0
			// ConPTY paints its whole window before the program says a word, so a
			// job's log would otherwise open with a screenful of blank lines.
			if !f.started {
				continue
			}
			out = append(out, '\n')
		case 0x07, '\b': // bell and backspace are cursor theatre, not content
		default:
			if f.cr {
				f.cr = false
				f.spaces = 0
				if f.started {
					out = append(out, '\n')
				}
			}
			if b == ' ' {
				f.spaces++
				continue
			}
			for ; f.spaces > 0; f.spaces-- {
				out = append(out, ' ')
			}
			out = append(out, b)
			f.started = true
		}
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	// io.Copy insists the whole slice was consumed; the bytes we dropped were
	// terminal control, not data.
	return len(p), nil
}

// Flush emits a trailing bare CR as a newline so the last line of a stream that ended
// mid-rewrite isn't lost. Held-back spaces are dropped on purpose.
func (f *ttyFilter) Flush() {
	if f.cr {
		f.cr = false
		f.spaces = 0
		if f.started {
			_, _ = f.w.Write([]byte{'\n'})
		}
	}
}
