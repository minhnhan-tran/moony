// spawn.go — starting a debug adapter as a child process.
//
// Dial attaches to an adapter that is ALREADY listening, which means the user has
// to run `dlv dap --listen=...` (or debugpy, etc.) in another terminal first. Spawn
// removes that step: it starts the adapter itself and connects to it.
//
// Two adapter styles are supported, because the ecosystem is split:
//
//   - stdio adapters (the DAP default: `dlv dap`, `node --inspect` wrappers,
//     `debugpy.adapter`) speak the protocol over stdin/stdout.
//   - socket adapters, started with a flag like `--listen=127.0.0.1:PORT`, which
//     print nothing useful on stdout and expect a TCP client.
//
// Which one is in use is inferred from the command itself, so the caller does not
// have to declare it.

package dap

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// listenFlagRe matches an explicit listen address in a spawn command, e.g.
// `--listen=127.0.0.1:4711`, `--listen 127.0.0.1:4711` or `--port 5678`.
var listenFlagRe = regexp.MustCompile(`--(?:listen|port|server)[= ]([^\s]+)`)

// SpawnOptions configures a spawned adapter.
type SpawnOptions struct {
	// Command is the shell command that starts the adapter, e.g. "dlv dap".
	Command string
	// Dir is the working directory for the adapter (defaults to the caller's).
	Dir string
	// Env is extra environment, in "K=V" form, appended to the parent's.
	Env []string
}

// Spawn starts a debug adapter and returns an initialised session.
//
// The returned session owns the child process: Close terminates it, so a debugging
// session cannot leak a daemon after the agent moves on.
func Spawn(ctx context.Context, opts SpawnOptions) (*Session, error) {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		return nil, fmt.Errorf("dap: no adapter command given")
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = append(cmd.Environ(), opts.Env...)
	}

	// A socket adapter is identified by an explicit listen address in the command.
	if addr, ok := listenAddr(command); ok {
		return spawnSocket(ctx, cmd, addr)
	}
	return spawnStdio(cmd)
}

// listenAddr extracts the address a socket-style adapter will listen on.
// A bare port ("--port 5678") is normalised to a loopback address.
func listenAddr(command string) (string, bool) {
	m := listenFlagRe.FindStringSubmatch(command)
	if m == nil {
		return "", false
	}
	addr := strings.Trim(m[1], `"'`)
	if addr == "" {
		return "", false
	}
	if !strings.Contains(addr, ":") {
		return "127.0.0.1:" + addr, true // bare port
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr, true
	}
	return addr, true
}

// spawnStdio wires the adapter's stdin/stdout into a session.
func spawnStdio(cmd *exec.Cmd) (*Session, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dap: adapter stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dap: adapter stdout: %w", err)
	}
	// Adapters chatter on stderr; capture a bounded tail so a failed handshake can
	// explain itself instead of reporting a bare EOF.
	tail := &tailBuffer{limit: 4096}
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dap: start adapter: %w", err)
	}

	// Tee stdout into the tail as well. For a stdio adapter, stdout IS the protocol
	// channel, so when the command is not actually an adapter the giveaway (a shell
	// error, a usage message) is consumed by the DAP parser and lost. Keeping a copy
	// turns "session closed while waiting for initialize" into an actionable message.
	s := newSession(io.TeeReader(stdout, tail), stdin, processCloser(cmd, stdin))
	if err := s.handshake(); err != nil {
		return nil, annotate(err, tail)
	}
	return s, nil
}

// spawnSocket starts a socket-style adapter and dials it once it is listening.
func spawnSocket(ctx context.Context, cmd *exec.Cmd, addr string) (*Session, error) {
	tail := &tailBuffer{limit: 4096}
	cmd.Stdout = tail
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dap: start adapter: %w", err)
	}

	// The adapter needs a moment to bind. Poll rather than sleeping a fixed time,
	// so a fast adapter is not penalised and a slow one is not cut off.
	deadline := time.Now().Add(15 * time.Second)
	var conn net.Conn
	for {
		if ctx.Err() != nil {
			_ = killProcess(cmd)
			return nil, ctx.Err()
		}
		var derr error
		conn, derr = net.DialTimeout("tcp", addr, time.Second)
		if derr == nil {
			break
		}
		// If the adapter already exited, dialling forever is pointless.
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return nil, annotate(fmt.Errorf("dap: adapter exited before listening on %s", addr), tail)
		}
		if time.Now().After(deadline) {
			_ = killProcess(cmd)
			return nil, annotate(fmt.Errorf("dap: adapter did not listen on %s within 15s", addr), tail)
		}
		time.Sleep(100 * time.Millisecond)
	}

	s := newSession(conn, conn, func() error {
		cerr := conn.Close()
		_ = killProcess(cmd)
		return cerr
	})
	if err := s.handshake(); err != nil {
		return nil, annotate(err, tail)
	}
	return s, nil
}

// processCloser returns a closer that shuts the adapter's stdin and then makes
// sure the process is gone, so no adapter outlives its session.
func processCloser(cmd *exec.Cmd, stdin io.Closer) func() error {
	var once sync.Once
	return func() error {
		var err error
		once.Do(func() {
			// Closing stdin asks a well-behaved adapter to exit on its own.
			err = stdin.Close()
			_ = killProcess(cmd)
		})
		return err
	}
}

// killProcess ends the adapter, giving it a brief chance to exit cleanly first.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// annotate attaches the adapter's captured output to an error. A DAP handshake
// failure otherwise surfaces as "EOF", which says nothing about the real cause
// (a missing binary, a bad flag, a port already in use).
func annotate(err error, tail *tailBuffer) error {
	out := strings.TrimSpace(tail.String())
	if out == "" {
		return err
	}
	if len(out) > 500 {
		out = out[len(out)-500:]
	}
	return fmt.Errorf("%w (adapter output: %s)", err, out)
}

// tailBuffer keeps only the last limit bytes written to it, so a chatty adapter
// cannot grow it without bound.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
