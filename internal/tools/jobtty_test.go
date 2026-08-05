package tools

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ttyFilter is what keeps a pty job's log readable — and, more importantly, keeps the
// escape soup out of the buffer `bash_output` feeds to the model. These pin the cases
// that actually show up: colours, CRLF, ConPTY's line padding, and progress rewrites.
func TestTTYFilterStripsTerminalControl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"csi colour", "\x1b[32mok\x1b[0m\n", "ok\n"},
		{"crlf", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"osc title", "\x1b]0;a title\x07done\n", "done\n"},
		{"osc st terminated", "\x1b]0;t\x1b\\x\n", "x\n"},
		{"conpty padding", "line   \r\nnext\r\n", "line\nnext\n"},
		{"bare cr rewrite", "50%\r100%\n", "50%\n100%\n"},
		{"cursor moves", "\x1b[2J\x1b[Hhi\n", "hi\n"},
		{"bell and backspace", "a\x07b\bc\n", "abc\n"},
		{"charset select", "\x1b(Bplain\n", "plain\n"},
		{"interior spaces kept", "a  b\n", "a  b\n"},
		// ConPTY paints the window before the program runs; that opening screenful of
		// blank lines must not land in the log the model reads.
		{"leading blank paint", "\r\n\r\n   \r\nreal\r\n", "real\n"},
		// An OSC nobody terminated must not blackhole the rest of the stream.
		{"unterminated osc", "\x1b]0;no terminator\nafter\n", "after\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			f := &ttyFilter{w: &sb}
			n, err := f.Write([]byte(tc.in))
			if err != nil || n != len(tc.in) {
				t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(tc.in))
			}
			f.Flush()
			if got := sb.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A pty delivers bytes whenever it feels like it, so a sequence can straddle two
// Writes. Splitting at every offset proves the parser carries its state.
func TestTTYFilterHandlesSplitSequences(t *testing.T) {
	const in = "\x1b[32mgreen\x1b[0m\r\ntail\r\n"
	const want = "green\ntail\n"
	for i := 1; i < len(in); i++ {
		var sb strings.Builder
		f := &ttyFilter{w: &sb}
		if _, err := f.Write([]byte(in[:i])); err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		if _, err := f.Write([]byte(in[i:])); err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		f.Flush()
		if got := sb.String(); got != want {
			t.Fatalf("split at %d: got %q, want %q", i, got, want)
		}
	}
}

// A trailing bare CR still terminates the last line, or a spinner's final frame would
// vanish from the log.
func TestTTYFilterFlushesPendingCR(t *testing.T) {
	var sb strings.Builder
	f := &ttyFilter{w: &sb}
	_, _ = f.Write([]byte("done\r"))
	f.Flush()
	if got := sb.String(); got != "done\n" {
		t.Fatalf("got %q, want %q", got, "done\n")
	}
}

// Input is a TTY-only capability: a pipe job has no stdin at all, so the error has to
// say so rather than silently dropping the keystrokes.
func TestSendInputRejectsNonTTYJob(t *testing.T) {
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `sleep 5`, jobOpts{})
	defer s.KillAll()

	err := s.SendInput(j.id, "hello", true, nil)
	if err == nil || !strings.Contains(err.Error(), "no TTY") {
		t.Fatalf("want a no-TTY error, got %v", err)
	}
	if err := s.SendInput("bg_nope", "x", true, nil); err == nil {
		t.Fatal("unknown job should error")
	}
	if err := s.SendInput(j.id, strings.Repeat("x", maxJobInputBytes+1), true, nil); err == nil {
		t.Fatal("oversized input should be refused")
	}
}

// End-to-end: a TTY job runs, echoes what it is typed, and stops when killed. Skipped
// where the host has no pty (go-pty returns ErrUnsupported), since the fallback to
// pipes is covered by the tests above.
func TestBackgroundJobTTYEchoesInput(t *testing.T) {
	requirePTY(t)

	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `cat`, jobOpts{tty: true})
	defer s.KillAll()

	if !j.tty {
		t.Fatal("job should be marked as running under a TTY")
	}
	if !waitFor(3*time.Second, func() bool {
		return s.SendInput(j.id, "ping", true, nil) == nil
	}) {
		t.Fatal("input was never accepted")
	}

	var seen strings.Builder
	if !waitFor(5*time.Second, func() bool {
		out, _, _, _ := j.snapshot()
		seen.WriteString(out)
		return strings.Contains(seen.String(), "ping")
	}) {
		t.Fatalf("never saw the echo; log so far: %q", seen.String())
	}
	// The escape sequences a terminal emits must not reach the buffer the model reads.
	if strings.Contains(seen.String(), "\x1b") {
		t.Fatalf("escape sequences leaked into the job log: %q", seen.String())
	}

	if !s.KillJob(j.id) {
		t.Fatal("kill reported no such job")
	}
	if !waitFor(3*time.Second, func() bool { return j.status() != jobRunning }) {
		t.Fatal("killed TTY job never left the running state")
	}
	if err := s.SendInput(j.id, "late", true, nil); err == nil {
		t.Fatal("input to a finished job should be refused")
	}
}

// A job that ends on its own must not be filed as killed: the panel and the model both
// read that state, and "killed" would make a clean exit look like a failure someone
// caused.
func TestBackgroundJobTTYNaturalExit(t *testing.T) {
	requirePTY(t)
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `exit 0`, jobOpts{tty: true})
	defer s.KillAll()

	if !waitFor(10*time.Second, func() bool { return j.status() != jobRunning }) {
		t.Fatal("job never finished")
	}
	if got := j.status(); got != jobDone {
		out, _, exit, msg := j.snapshot()
		t.Fatalf("state = %s (exit %d, err %q, out %q); want done", got, exit, msg, out)
	}
}

// A non-zero exit stays a failure, with the code preserved.
func TestBackgroundJobTTYFailureExit(t *testing.T) {
	requirePTY(t)
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `exit 3`, jobOpts{tty: true})
	defer s.KillAll()

	if !waitFor(10*time.Second, func() bool { return j.status() != jobRunning }) {
		t.Fatal("job never finished")
	}
	_, state, exit, _ := j.snapshot()
	if state != jobFailed || exit != 3 {
		t.Fatalf("state = %s exit = %d; want failed/3", state, exit)
	}
}

// The whole point of the check hook: a command split across calls is still one command
// by the time the shell sees it, so the check must run on the assembled line.
func TestStageInputAssemblesLineForCheck(t *testing.T) {
	j := &job{}
	var seen []string
	check := func(line string) error {
		seen = append(seen, line)
		if strings.Contains(line, "rm -rf /") {
			return errRefused
		}
		return nil
	}
	if err := j.stageInput("rm -rf", false, check); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if err := j.stageInput(" /", false, check); err == nil {
		t.Fatal("the assembled line should have been refused")
	}
	if len(seen) < 2 || seen[1] != "rm -rf /" {
		t.Fatalf("check saw %q; want the assembled line", seen)
	}
	// A refused chunk must not be retained, or the job is stuck refusing forever.
	if j.inputLine != "rm -rf" {
		t.Fatalf("pending line = %q", j.inputLine)
	}
	// Ctrl-C discards the pending line, as it would on a console.
	if err := j.stageInput("\x03", false, check); err != nil {
		t.Fatalf("ctrl-c: %v", err)
	}
	if j.inputLine != "" {
		t.Fatalf("ctrl-c left %q pending", j.inputLine)
	}
	// Enter hands the line to the shell and starts a fresh one.
	if err := j.stageInput("echo hi", true, check); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if j.inputLine != "" {
		t.Fatalf("after Enter the pending line should be empty, got %q", j.inputLine)
	}
}

var errRefused = errors.New("refused")

func requirePTY(t *testing.T) {
	t.Helper()
	p, err := newPTY()
	if err != nil {
		t.Skipf("no pty on this host: %v", err)
	}
	_ = p.Close()
}

// An embedded newline SUBMITS a line to the shell, so it has to be vetted. Resetting on
// it instead would have made "rm -rf /\r" a one-call bypass: the raw bytes reach the
// terminal either way, and the checker would only ever see the empty remainder.
func TestStageInputVetsEmbeddedNewlines(t *testing.T) {
	j := &job{}
	var seen []string
	check := func(line string) error {
		seen = append(seen, line)
		if strings.Contains(line, "rm -rf /") {
			return errRefused
		}
		return nil
	}
	if err := j.stageInput("rm -rf /"+string(rune(13)), false, check); err == nil {
		t.Fatal("a command terminated by CR should have been refused")
	}
	if len(seen) == 0 || seen[0] != "rm -rf /" {
		t.Fatalf("check saw %q; want the submitted line", seen)
	}
	if j.inputLine != "" {
		t.Fatalf("a refused call must not change the pending line, got %q", j.inputLine)
	}

	// Several lines in one call: each is vetted, and the remainder stays pending.
	seen = nil
	if err := j.stageInput("echo a\necho b\nech", false, check); err != nil {
		t.Fatalf("harmless multi-line input: %v", err)
	}
	if len(seen) != 3 || seen[0] != "echo a" || seen[1] != "echo b" || seen[2] != "ech" {
		t.Fatalf("check saw %q; want each line plus the remainder", seen)
	}
	if j.inputLine != "ech" {
		t.Fatalf("pending line = %q, want the unterminated remainder", j.inputLine)
	}
}

// A process that never drains its terminal must not park the caller, leak an unbounded
// number of writers, or let a later write overtake the stuck one.
func TestSendInputTimesOutAndSerialises(t *testing.T) {
	old := jobInputTimeout
	jobInputTimeout = 50 * time.Millisecond
	defer func() { jobInputTimeout = old }()

	release := make(chan struct{})
	s := newJobStore()
	j := &job{id: "bg_1", state: jobRunning, tty: true, stdin: blockingWriter{release}}
	s.add(j)

	start := time.Now()
	if err := s.SendInput("bg_1", "first", true, nil); err == nil || !strings.Contains(err.Error(), "not reading input") {
		t.Fatalf("stuck write: err = %v, want a not-reading error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SendInput waited %v; the deadline should have cut it short", elapsed)
	}
	// The first write still owns the terminal, so the second must be refused rather
	// than delivered out of order behind it.
	if err := s.SendInput("bg_1", "second", true, nil); err == nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatalf("second write: err = %v, want a pending-write refusal", err)
	}
	// Unblocking the terminal (what closing the pty does) releases the writer.
	close(release)
	if !waitFor(2*time.Second, func() bool { return s.SendInput("bg_1", "third", true, nil) == nil }) {
		t.Fatal("the terminal never became writable again")
	}
}

// blockingWriter parks until released, standing in for a program that has stopped
// reading its terminal.
type blockingWriter struct{ release chan struct{} }

func (b blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}
