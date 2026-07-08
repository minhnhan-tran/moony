package mcp

// Transport abstracts the wire-level connection to an MCP server. It is the
// minimum surface needed by Client: send a single JSON-RPC frame, receive the
// next one. Each transport implementation handles its own framing (stdio uses
// newline-delimited JSON, HTTP+SSE uses POST request / SSE event response).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/procutil"
)

// Transport is implemented by stdio, HTTP+SSE, and any future wire types.
type Transport interface {
	// Send writes one JSON-RPC frame.
	Send(req []byte) error
	// Receive blocks until the next frame is available or ctx is done.
	Receive(ctx context.Context) ([]byte, error)
	// Close releases any resources held by the transport.
	Close() error
}

// stdioTransport runs an MCP server as a child process and exchanges newline-
// delimited JSON over its stdio.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	lines  chan []byte
	errc   chan error
	once   sync.Once
}

// newStdioTransport launches `command args...` and starts a reader goroutine.
func newStdioTransport(ctx context.Context, command string, args, env []string, logger func(string)) (*stdioTransport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	// Run the server in its own session so it (and its launcher — npx/uvx/uv) has
	// no controlling terminal and can't write to the TUI's screen; its real stdio
	// is still captured via the pipes below.
	procutil.Detach(cmd)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", command, err)
	}
	t := &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		lines:  make(chan []byte, 32),
		errc:   make(chan error, 1),
	}
	go t.readLoop()
	go drainStderr(stderr, logger)
	return t, nil
}

func (t *stdioTransport) Send(req []byte) error {
	if _, err := t.stdin.Write(append(req, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *stdioTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case line, ok := <-t.lines:
		if !ok {
			return nil, io.EOF
		}
		return line, nil
	case err := <-t.errc:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) Close() error {
	t.once.Do(func() {
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		if t.cmd.Process != nil {
			procutil.Terminate(t.cmd.Process) // kill the whole group (npx→node, uvx→python)
		}
		_ = t.cmd.Wait()
	})
	return nil
}

func (t *stdioTransport) readLoop() {
	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		// Copy because the scanner reuses its buffer.
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		t.lines <- line
	}
	if err := scanner.Err(); err != nil {
		t.errc <- err
	}
	close(t.lines)
}

// httpSSETransport speaks the MCP HTTP+SSE flavor: POST every request to a JSON
// endpoint and read the streamed responses from a long-running SSE GET on the
// same URL. This implementation is deliberately simple — it assumes responses
// arrive as `data: <json>\n\n` events on the SSE channel and matches them to
// request ids in the Client's request/response map.
type httpSSETransport struct {
	client      *http.Client
	url         string
	headers     http.Header
	cancel      context.CancelFunc
	stream      io.ReadCloser
	frames      chan []byte
	errc        chan error
	once        sync.Once
	connectOnce atomic.Bool
}

// newHTTPSSETransport opens the SSE channel and returns once the first 'open'
// event (or any byte) has been received. cfg.Env is mapped onto request
// headers (HEADER=value form), so e.g. "Authorization=Bearer ..." works out of
// the box.
func newHTTPSSETransport(ctx context.Context, url string, env []string, logger func(string)) (*httpSSETransport, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	t := &httpSSETransport{
		client:  &http.Client{Timeout: 0},
		url:     url,
		cancel:  cancel,
		frames:  make(chan []byte, 32),
		errc:    make(chan error, 1),
		headers: http.Header{},
	}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			t.headers.Set(k, v)
		}
	}

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range t.headers {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("SSE GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	t.stream = resp.Body
	go t.readLoop(logger)
	return t, nil
}

func (t *httpSSETransport) Send(req []byte) error {
	r, err := http.NewRequest(http.MethodPost, t.url, bytes.NewReader(req))
	if err != nil {
		return err
	}
	for k, v := range t.headers {
		r.Header[k] = v
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json,text/event-stream")
	resp, err := t.client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: %s: %s", t.url, resp.Status, strings.TrimSpace(string(body)))
	}
	// Some servers return the response inline (application/json) instead of
	// streaming it; if so, surface it through the same channel so Client.call
	// can pick it up.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		if len(body) > 0 {
			t.frames <- body
		}
	}
	return nil
}

func (t *httpSSETransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case f, ok := <-t.frames:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	case err := <-t.errc:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *httpSSETransport) Close() error {
	t.once.Do(func() {
		t.cancel()
		if t.stream != nil {
			_ = t.stream.Close()
		}
	})
	return nil
}

// readLoop parses SSE: lines like "data: <json>", terminated by a blank line.
func (t *httpSSETransport) readLoop(logger func(string)) {
	scanner := bufio.NewScanner(t.stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				t.frames <- append([]byte(nil), data.Bytes()...)
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		default:
			// "event:" / "id:" / "retry:" lines are accepted but unused here.
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		if logger != nil {
			logger("sse read: " + err.Error())
		}
		t.errc <- err
	}
	close(t.frames)
}

// drainStderr is shared between transports that have a child stderr.
func drainStderr(r io.Reader, logger func(string)) {
	if logger == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for scanner.Scan() {
		logger("stderr: " + scanner.Text())
	}
}

// _ keeps json/time imports honest if no caller uses them yet.
var _ = json.RawMessage(nil)
var _ = time.Second
