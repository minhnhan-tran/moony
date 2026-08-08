package mcp

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A nil logger must still drain. Before this, drainStderr returned immediately when
// logger == nil, leaving the child's stderr pipe unread — once the OS pipe buffer filled,
// the subprocess blocked forever on its next write and the MCP server hung.
func TestDrainStderrDrainsWithNilLogger(t *testing.T) {
	pr, pw := io.Pipe()

	done := make(chan struct{})
	go func() {
		drainStderr(pr, nil)
		close(done)
	}()

	// Far more than any pipe buffer: this only completes if someone is reading.
	payload := strings.Repeat("chatty mcp server writing to stderr\n", 20_000)
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(pw, payload)
		pw.Close()
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked: stderr is not being drained with a nil logger")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainStderr did not return after EOF")
	}
}

// With a logger, lines are still delivered.
func TestDrainStderrLogsLines(t *testing.T) {
	pr, pw := io.Pipe()
	var mu sync.Mutex
	var got []string

	done := make(chan struct{})
	go func() {
		drainStderr(pr, func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() })
		close(done)
	}()

	io.WriteString(pw, "first\nsecond\n")
	pw.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "stderr: first" || got[1] != "stderr: second" {
		t.Errorf("got %q, want the two prefixed lines", got)
	}
}

// A nil reader must not panic.
func TestDrainStderrNilReader(t *testing.T) {
	drainStderr(nil, nil)
}

// A single stderr line larger than the scanner's 1 MB cap ends the scan with ErrTooLong.
// The drain must continue anyway, or the child blocks on a full pipe — the same hang the
// nil-logger case caused.
func TestDrainStderrSurvivesOversizedLine(t *testing.T) {
	pr, pw := io.Pipe()

	done := make(chan struct{})
	go func() {
		drainStderr(pr, func(string) {})
		close(done)
	}()

	writeDone := make(chan error, 1)
	go func() {
		// 2 MB with no newline, then plenty more output afterwards.
		_, err := io.WriteString(pw, strings.Repeat("x", 2<<20))
		if err == nil {
			_, err = io.WriteString(pw, "\n"+strings.Repeat("more output\n", 20_000))
		}
		pw.Close()
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked: drain stopped after an oversized line")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainStderr did not return")
	}
}
