// Package dap implements a minimal Debug Adapter Protocol (DAP) client over a
// TCP socket. It handles the Content-Length message framing, request/response
// correlation, and asynchronous event dispatch needed to drive an interactive
// debugger from the agent's tool calls.
//
// Typical usage:
//
//	sess, err := dap.Dial("localhost:54321")
//	// sess is now initialised and ready for launch/attach
//	resp, err := sess.Send("launch", map[string]any{"program": "./main"}, 10*time.Second)
//	threadID, reason, err := sess.WaitStopped(ctx)
package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Wire types ──────────────────────────────────────────────────────────────

// ProtocolMessage is the base envelope for all DAP messages.
type ProtocolMessage struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"` // "request", "response", "event"
}

// Request is sent by the client to the adapter.
type Request struct {
	ProtocolMessage
	Command   string `json:"command"`
	Arguments any    `json:"arguments,omitempty"`
}

// Response is sent by the adapter for every request, carrying a seq that
// matches the originating request's seq number.
type Response struct {
	ProtocolMessage
	RequestSeq int             `json:"request_seq"`
	Command    string          `json:"command"`
	Success    bool            `json:"success"`
	Message    string          `json:"message,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// rawEvent is the minimal shape needed to route events in the readLoop.
type rawEvent struct {
	ProtocolMessage
	Event string          `json:"event"`
	Body  json.RawMessage `json:"body,omitempty"`
}

// ─── Body types (request arguments and response bodies we actually use) ──────

// InitializeArgs are sent in the mandatory initialize request.
type InitializeArgs struct {
	ClientID         string `json:"clientID"`
	AdapterID        string `json:"adapterID"`
	LinesStartAt1    bool   `json:"linesStartAt1"`
	ColumnsStartAt1  bool   `json:"columnsStartAt1"`
	PathFormat       string `json:"pathFormat"`
	SupportsRunInTerminalRequest bool `json:"supportsRunInTerminalRequest,omitempty"`
}

// SetBreakpointsArgs sends breakpoints for a single source file.
type SetBreakpointsArgs struct {
	Source      SourceRef          `json:"source"`
	Breakpoints []SourceBreakpoint `json:"breakpoints"`
}

// SourceRef identifies a source file; Path is the canonical form adapters use.
type SourceRef struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

// SourceBreakpoint describes one breakpoint location.
type SourceBreakpoint struct {
	Line      int    `json:"line"`
	Condition string `json:"condition,omitempty"`
}

// SetBreakpointsBody is the response body from setBreakpoints.
type SetBreakpointsBody struct {
	Breakpoints []Breakpoint `json:"breakpoints"`
}

// Breakpoint is a single breakpoint as reported by the adapter.
type Breakpoint struct {
	ID       int    `json:"id,omitempty"`
	Verified bool   `json:"verified"`
	Message  string `json:"message,omitempty"`
	Source   *SourceRef `json:"source,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// StackTraceArgs requests the call stack for one thread.
type StackTraceArgs struct {
	ThreadID int `json:"threadId"`
	Levels   int `json:"levels,omitempty"`
}

// StackTraceBody is the response body from stackTrace.
type StackTraceBody struct {
	StackFrames []StackFrame `json:"stackFrames"`
	TotalFrames int          `json:"totalFrames,omitempty"`
}

// StackFrame is one level of the call stack.
type StackFrame struct {
	ID                          int        `json:"id"`
	Name                        string     `json:"name"`
	Source                      *SourceRef `json:"source,omitempty"`
	Line                        int        `json:"line"`
	Column                      int        `json:"column"`
	PresentationHint            string     `json:"presentationHint,omitempty"`
}

// ScopesArgs requests the scopes for a stack frame.
type ScopesArgs struct {
	FrameID int `json:"frameId"`
}

// ScopesBody is the response body from scopes.
type ScopesBody struct {
	Scopes []Scope `json:"scopes"`
}

// Scope groups a set of variables (e.g. "Locals", "Globals").
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	Expensive          bool   `json:"expensive"`
}

// VariablesArgs requests the children of a variables reference.
type VariablesArgs struct {
	VariablesReference int    `json:"variablesReference"`
	Filter             string `json:"filter,omitempty"` // "indexed" | "named"
}

// VariablesBody is the response body from variables.
type VariablesBody struct {
	Variables []Variable `json:"variables"`
}

// Variable is one named value in a scope or parent variable.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// EvaluateArgs asks the adapter to evaluate an expression.
type EvaluateArgs struct {
	Expression string `json:"expression"`
	FrameID    int    `json:"frameId,omitempty"`
	Context    string `json:"context,omitempty"` // "watch" | "repl" | "hover"
}

// EvaluateBody is the response body from evaluate.
type EvaluateBody struct {
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// stoppedBody is the event body for the "stopped" event.
type stoppedBody struct {
	Reason            string `json:"reason"`
	ThreadID          int    `json:"threadId"`
	PreserveFocusHint bool   `json:"preserveFocusHint,omitempty"`
	AllThreadsStopped bool   `json:"allThreadsStopped,omitempty"`
}

// outputBody is the event body for "output" events (debuggee stdout/stderr).
type outputBody struct {
	Category string `json:"category,omitempty"` // "stdout", "stderr", "console", …
	Output   string `json:"output"`
}

// ─── Session ─────────────────────────────────────────────────────────────────

// Session is a live connection to a DAP adapter. All exported methods are safe
// for concurrent use from multiple tool goroutines.
type Session struct {
	conn net.Conn
	rd   *bufio.Reader

	mu      sync.Mutex      // serialises writes + pending map
	nextSeq int             // next outgoing sequence number
	pending map[int]chan *Response // inflight requests; keyed by request seq

	// state updated by the readLoop goroutine; all protected by stateMu
	stateMu       sync.RWMutex
	stopped       bool
	threadID      int
	stopReason    string
	terminated    bool
	outputBuf     strings.Builder
	initializedCh chan struct{} // closed once the "initialized" event arrives

	done chan struct{} // closed when readLoop exits (connection gone)
	seq  atomic.Int64 // monotonic counter; used only in tests that don't dial
}

// Dial opens a TCP connection to addr, sends the mandatory DAP initialize
// request, and starts a background goroutine to read adapter messages.
// The returned session is ready for launch/attach calls.
func Dial(addr string) (*Session, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dap: connect %s: %w", addr, err)
	}
	s := &Session{
		conn:    conn,
		rd:      bufio.NewReader(conn),
		nextSeq: 1,
		pending: make(map[int]chan *Response),
		done:    make(chan struct{}),
	}
	go s.readLoop()

	// The DAP spec requires an initialize handshake before any other command.
	resp, err := s.Send("initialize", InitializeArgs{
		ClientID:        "moony",
		AdapterID:       "moony",
		LinesStartAt1:   true,
		ColumnsStartAt1: true,
		PathFormat:      "path",
	}, 10*time.Second)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("dap: initialize: %w", err)
	}
	if !resp.Success {
		conn.Close()
		return nil, fmt.Errorf("dap: initialize failed: %s", resp.Message)
	}
	return s, nil
}

// Send delivers a request to the adapter and waits for the matching response.
// A timeout of 0 uses a 30-second default.
func (s *Session) Send(command string, args any, timeout time.Duration) (*Response, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ch := make(chan *Response, 1)

	s.mu.Lock()
	if s.pending == nil {
		// readLoop has already cleaned up; session is closed.
		s.mu.Unlock()
		return nil, fmt.Errorf("dap: session closed")
	}
	seq := s.nextSeq
	s.nextSeq++
	s.pending[seq] = ch
	req := Request{
		ProtocolMessage: ProtocolMessage{Seq: seq, Type: "request"},
		Command:         command,
		Arguments:       args,
	}
	err := s.writeMessage(req)
	s.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.pending, seq)
		s.mu.Unlock()
		return nil, fmt.Errorf("dap send %s: %w", command, err)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			// readLoop closed the channel (connection gone); return a clean error.
			return nil, fmt.Errorf("dap: session closed while waiting for %s response", command)
		}
		return resp, nil
	case <-s.done:
		return nil, fmt.Errorf("dap: connection closed while waiting for %s response", command)
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, seq)
		s.mu.Unlock()
		return nil, fmt.Errorf("dap: %s response timed out after %v", command, timeout)
	}
}

// WaitStopped blocks until the adapter sends a "stopped" event (e.g., a
// breakpoint hit) or the session is terminated. It returns the thread ID and
// the stop reason ("breakpoint", "step", "exception", "entry", …).
func (s *Session) WaitStopped(ctx context.Context) (threadID int, reason string, err error) {
	for {
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-s.done:
			return 0, "", errors.New("dap: session terminated before stopping")
		case <-time.After(50 * time.Millisecond):
		}
		s.stateMu.RLock()
		stopped, tid, rsn, term := s.stopped, s.threadID, s.stopReason, s.terminated
		s.stateMu.RUnlock()
		if stopped {
			return tid, rsn, nil
		}
		if term {
			return 0, "", errors.New("dap: debuggee terminated without stopping")
		}
	}
}

// WaitInitialized blocks until the adapter sends the "initialized" event,
// which signals that it's ready to receive setBreakpoints / configurationDone.
func (s *Session) WaitInitialized(ctx context.Context) error {
	// We detect the initialized event via a small polling trick: the readLoop
	// sets a flag when it arrives. Alternatively, we could use a channel, but
	// the polling approach keeps the struct simpler (initializedCh would need
	// special once-close handling; not worth it for a 50ms poll budget).
	s.stateMu.Lock()
	if s.initializedCh == nil {
		s.initializedCh = make(chan struct{})
	}
	ch := s.initializedCh
	s.stateMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("dap: session closed before initialized event")
	case <-time.After(15 * time.Second):
		return errors.New("dap: timed out waiting for initialized event")
	}
}

// ClearStopped resets the stopped flag after the caller has handled the stop
// (e.g. right before sending continue/next/step). Thread-safe.
func (s *Session) ClearStopped() {
	s.stateMu.Lock()
	s.stopped = false
	s.stateMu.Unlock()
}

// DrainOutput returns all adapter output events buffered since the last call,
// clearing the internal buffer. Safe for concurrent use.
func (s *Session) DrainOutput() string {
	s.stateMu.Lock()
	out := s.outputBuf.String()
	s.outputBuf.Reset()
	s.stateMu.Unlock()
	return out
}

// Terminated reports whether the adapter sent a "terminated" event.
func (s *Session) Terminated() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.terminated
}

// Close sends a disconnect request (best-effort) and closes the connection.
func (s *Session) Close() error {
	// Only attempt disconnect if the readLoop is still running (connection live).
	select {
	case <-s.done:
		// readLoop already exited; connection is gone, skip the disconnect send.
	default:
		_, _ = s.Send("disconnect", map[string]any{"terminateDebuggee": true}, 3*time.Second)
	}
	return s.conn.Close()
}

// ─── readLoop ────────────────────────────────────────────────────────────────

func (s *Session) readLoop() {
	defer close(s.done)
	for {
		raw, err := s.readMessage()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				// Swallow other read errors silently; the done channel signals the close.
			}
			// Drain remaining pending requests.
			s.mu.Lock()
			for _, ch := range s.pending {
				close(ch)
			}
			s.pending = nil
			s.mu.Unlock()
			return
		}

		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			continue
		}

		switch base.Type {
		case "response":
			var resp Response
			if err := json.Unmarshal(raw, &resp); err != nil {
				continue
			}
			s.mu.Lock()
			if ch, ok := s.pending[resp.RequestSeq]; ok {
				delete(s.pending, resp.RequestSeq)
				ch <- &resp
			}
			s.mu.Unlock()

		case "event":
			var ev rawEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			s.handleEvent(ev)
		}
	}
}

func (s *Session) handleEvent(ev rawEvent) {
	switch ev.Event {
	case "stopped":
		var body stoppedBody
		_ = json.Unmarshal(ev.Body, &body)
		s.stateMu.Lock()
		s.stopped = true
		s.threadID = body.ThreadID
		s.stopReason = body.Reason
		s.stateMu.Unlock()

	case "continued":
		s.stateMu.Lock()
		s.stopped = false
		s.stateMu.Unlock()

	case "output":
		var body outputBody
		_ = json.Unmarshal(ev.Body, &body)
		if body.Output != "" {
			s.stateMu.Lock()
			s.outputBuf.WriteString(body.Output)
			s.stateMu.Unlock()
		}

	case "initialized":
		s.stateMu.Lock()
		if s.initializedCh != nil {
			select {
			case <-s.initializedCh:
				// already closed
			default:
				close(s.initializedCh)
			}
		} else {
			ch := make(chan struct{})
			close(ch)
			s.initializedCh = ch
		}
		s.stateMu.Unlock()

	case "terminated", "exited":
		s.stateMu.Lock()
		s.terminated = true
		s.stateMu.Unlock()
	}
}

// ─── Wire framing ────────────────────────────────────────────────────────────

// writeMessage JSON-encodes v and writes it with a DAP Content-Length header.
// Caller must hold s.mu.
func (s *Session) writeMessage(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(s.conn, header); err != nil {
		return err
	}
	_, err = s.conn.Write(body)
	return err
}

// readMessage reads one DAP message from the adapter, returning the raw JSON
// body. It blocks until a full message is available or an error occurs.
func (s *Session) readMessage() (json.RawMessage, error) {
	// Read headers terminated by a blank line (\r\n\r\n).
	var contentLength int
	for {
		line, err := s.rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line: end of headers
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, _ = strconv.Atoi(v)
		}
	}
	if contentLength == 0 {
		return nil, errors.New("dap: zero-length or missing Content-Length header")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(s.rd, buf); err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

// ─── SessionHolder ───────────────────────────────────────────────────────────

// SessionHolder is a thread-safe mutable reference to the current debug
// session. All DAP tools share one holder; dap_connect populates it, and every
// other tool calls Get() before doing any work.
type SessionHolder struct {
	mu sync.Mutex
	s  *Session
}

// Set stores a new session in the holder, replacing any previous one.
func (h *SessionHolder) Set(s *Session) {
	h.mu.Lock()
	h.s = s
	h.mu.Unlock()
}

// Get returns the current session or an actionable error when none is set.
func (h *SessionHolder) Get() (*Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.s == nil {
		return nil, errors.New("not connected to a DAP adapter — call dap_connect first")
	}
	return h.s, nil
}

// Clear nils out the held session (e.g. after dap_disconnect).
func (h *SessionHolder) Clear() {
	h.mu.Lock()
	h.s = nil
	h.mu.Unlock()
}
