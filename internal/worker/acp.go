package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/pysty1995/moony/internal/procutil"
)

// acpSession drives a worker over the Agent Client Protocol (ACP): a JSON-RPC 2.0
// conversation, newline-delimited over the worker process's stdio. We act as the
// ACP client and `optimus acp` is the agent. The process is held open across
// Prompt calls so the worker keeps its session context.
//
// Read-only enforcement lives on the client side: we advertise no write
// capability and reject any edit/execute permission request the agent makes.
type acpSession struct {
	opts Options

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex // serializes writes to the agent's stdin

	mu      sync.Mutex
	nextID  int
	pending map[string]chan rpcMsg // our request id -> response
	closed  bool

	promptMu sync.Mutex // one Prompt at a time per session

	sessionID string

	// per-prompt accumulation, guarded by accMu
	accMu   sync.Mutex
	acc     *strings.Builder
	onEvent func(Event)
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// openACP starts the worker's ACP server and performs the initialize +
// session/new handshake.
func openACP(ctx context.Context, opts Options) (Session, error) {
	bin := opts.Command
	if bin == "" {
		bin = "optimus"
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	args := []string{"acp", "--cwd", cwd}

	cmd := exec.Command(bin, args...) // not ctx-bound: the process must outlive open()
	cmd.Dir = cwd
	// Own session: no controlling terminal, so the worker (and anything it spawns)
	// can't write to the TUI's screen — parity with the MCP/LSP transports.
	procutil.Detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Capture the worker's stderr instead of letting it inherit ours: writing to
	// our os.Stderr would paint over the alt-screen TUI (the same class of bug as
	// the MCP connect logs). Drained and discarded unless MN_WORKER_DEBUG is set.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s acp: %w", bin, err)
	}
	go drainWorkerStderr(bin, stderr)

	s := newACPSession(opts, stdin, stdout, cmd)
	if err := s.handshake(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// drainWorkerStderr consumes the worker's stderr so it can never reach the
// controlling terminal (where it would corrupt an alt-screen TUI). Lines are
// discarded unless MN_WORKER_DEBUG is set — the rare case of debugging a worker
// outside the TUI — mirroring MN_LSP_DEBUG.
func drainWorkerStderr(name string, r io.Reader) {
	debug := workerDebug()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for scanner.Scan() {
		if debug {
			fmt.Fprintln(os.Stderr, name+": "+scanner.Text())
		}
	}
}

// workerDebug reports whether MN_WORKER_DEBUG enables forwarding worker stderr.
func workerDebug() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_WORKER_DEBUG"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// newACPSession wires an ACP client to an already-connected transport (the
// agent's stdin/stdout) and starts the read loop. cmd may be nil in tests.
func newACPSession(opts Options, stdin io.WriteCloser, stdout io.Reader, cmd *exec.Cmd) *acpSession {
	s := &acpSession{
		opts:    opts,
		cmd:     cmd,
		stdin:   stdin,
		pending: map[string]chan rpcMsg{},
	}
	go s.readLoop(stdout)
	return s
}

// handshake runs initialize then session/new, recording the session id.
func (s *acpSession) handshake(ctx context.Context) error {
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": true, "writeTextFile": false},
		},
	}
	if _, err := s.call(ctx, "initialize", initParams); err != nil {
		return fmt.Errorf("acp initialize: %w", err)
	}

	newParams := map[string]any{"cwd": s.opts.Cwd, "mcpServers": []any{}}
	res, err := s.call(ctx, "session/new", newParams)
	if err != nil {
		return fmt.Errorf("acp session/new: %w", err)
	}
	var nr struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &nr); err != nil || nr.SessionID == "" {
		return fmt.Errorf("acp session/new returned no sessionId")
	}
	s.sessionID = nr.SessionID
	return nil
}

func (s *acpSession) Prompt(ctx context.Context, task string, onEvent func(Event)) (Result, error) {
	if strings.TrimSpace(task) == "" {
		return Result{}, fmt.Errorf("task is required")
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()

	if s.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Timeout)
		defer cancel()
	}

	// Fresh accumulator for this turn.
	s.accMu.Lock()
	s.acc = &strings.Builder{}
	s.onEvent = onEvent
	s.accMu.Unlock()

	params := map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": task}},
	}
	res, err := s.call(ctx, "session/prompt", params)

	s.accMu.Lock()
	text := strings.TrimSpace(s.acc.String())
	s.acc = nil
	s.onEvent = nil
	s.accMu.Unlock()

	if err != nil {
		// Tell the worker to stop the turn it may still be running.
		s.notify("session/cancel", map[string]any{"sessionId": s.sessionID})
		return Result{Text: text, SessionID: s.sessionID}, fmt.Errorf("acp session/prompt: %w", err)
	}
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &pr)
	ok := pr.StopReason != "refusal"
	r := Result{Text: text, SessionID: s.sessionID, OK: ok}
	if !ok {
		r.Err = "worker stopped: " + pr.StopReason
	}
	return r, nil
}

// Close terminates the worker process and fails any in-flight calls.
func (s *acpSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, ch := range s.pending {
		close(ch)
	}
	s.pending = map[string]chan rpcMsg{}
	s.mu.Unlock()

	_ = s.stdin.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		procutil.Terminate(s.cmd.Process) // kill the whole group (the detached session leader + kids)
		_ = s.cmd.Wait()
	}
	return nil
}

// ---- JSON-RPC plumbing ------------------------------------------------------

// call sends a request and waits for its response (or ctx cancellation).
func (s *acpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("session closed")
	}
	s.nextID++
	id := strconv.Itoa(s.nextID)
	ch := make(chan rpcMsg, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(rpcMsg{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: mustRaw(params)}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("session closed")
		}
		if m.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", m.Error.Code, m.Error.Message)
		}
		return m.Result, nil
	}
}

func (s *acpSession) notify(method string, params any) {
	_ = s.write(rpcMsg{JSONRPC: "2.0", Method: method, Params: mustRaw(params)})
}

func (s *acpSession) respond(id json.RawMessage, result any) {
	_ = s.write(rpcMsg{JSONRPC: "2.0", ID: id, Result: mustRaw(result)})
}

func (s *acpSession) respondErr(id json.RawMessage, code int, msg string) {
	_ = s.write(rpcMsg{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// write serializes a single JSON-RPC message as one newline-delimited line.
func (s *acpSession) write(m rpcMsg) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// readLoop consumes the agent's stdout, routing responses to waiting calls and
// handling agent-initiated requests/notifications.
func (s *acpSession) readLoop(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			s.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	// stdout closed: unblock any waiters.
	s.mu.Lock()
	for _, ch := range s.pending {
		close(ch)
	}
	s.pending = map[string]chan rpcMsg{}
	s.mu.Unlock()
}

func (s *acpSession) dispatch(line []byte) {
	var m rpcMsg
	if json.Unmarshal(line, &m) != nil {
		return // ignore non-JSON noise
	}
	switch {
	case m.Method == "" && len(m.ID) > 0:
		// Response to one of our requests.
		s.mu.Lock()
		ch := s.pending[idKey(m.ID)]
		s.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case m.Method != "" && len(m.ID) > 0:
		s.handleRequest(m) // agent -> client request
	case m.Method != "":
		s.handleNotification(m) // agent -> client notification
	}
}

// handleRequest answers agent-initiated requests: permission decisions and
// (read-only) filesystem reads.
func (s *acpSession) handleRequest(m rpcMsg) {
	switch m.Method {
	case "session/request_permission":
		s.respond(m.ID, s.permissionDecision(m.Params))
	case "fs/read_text_file":
		s.handleReadFile(m)
	case "fs/write_text_file":
		s.respondErr(m.ID, -32000, "writes are disabled for this worker (read-only)")
	default:
		s.respondErr(m.ID, -32601, "method not supported by client")
	}
}

// permissionDecision allows read-ish tool calls and rejects edit/execute ones
// when the session is read-only.
func (s *acpSession) permissionDecision(params json.RawMessage) map[string]any {
	var p struct {
		ToolCall struct {
			Kind string `json:"kind"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(params, &p)

	allow := true
	if s.opts.ReadOnly {
		switch p.ToolCall.Kind {
		case "edit", "delete", "move", "execute":
			allow = false
		}
	}

	want := []string{"allow_once", "allow_always"}
	if !allow {
		want = []string{"reject_once", "reject_always"}
	}
	for _, w := range want {
		for _, o := range p.Options {
			if o.Kind == w {
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
			}
		}
	}
	// No suitable option offered — cancel the request.
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// handleReadFile serves an fs/read_text_file request, confined to the worker cwd.
func (s *acpSession) handleReadFile(m rpcMsg) {
	var p struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(m.Params, &p) != nil || p.Path == "" {
		s.respondErr(m.ID, -32602, "invalid params")
		return
	}
	abs := p.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.opts.Cwd, abs)
	}
	// Keep reads within the worker's working directory.
	if rel, err := filepath.Rel(s.opts.Cwd, abs); err != nil || strings.HasPrefix(rel, "..") {
		s.respondErr(m.ID, -32000, "path outside working directory")
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		s.respondErr(m.ID, -32000, err.Error())
		return
	}
	s.respond(m.ID, map[string]any{"content": string(data)})
}

// handleNotification accumulates assistant text and surfaces activity events.
func (s *acpSession) handleNotification(m rpcMsg) {
	if m.Method != "session/update" {
		return
	}
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Title string `json:"title"`
		} `json:"update"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	s.accMu.Lock()
	acc, onEvent := s.acc, s.onEvent
	s.accMu.Unlock()

	switch p.Update.SessionUpdate {
	case "agent_message_chunk":
		if t := p.Update.Content.Text; t != "" {
			if acc != nil {
				s.accMu.Lock()
				acc.WriteString(t)
				s.accMu.Unlock()
			}
			if onEvent != nil {
				onEvent(Event{Kind: "text", Text: t})
			}
		}
	case "tool_call", "tool_call_update":
		if onEvent != nil && p.Update.Title != "" {
			onEvent(Event{Kind: "tool", Text: p.Update.Title})
		}
	}
}

func idKey(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	return s
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
