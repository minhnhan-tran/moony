// Package lsp implements a minimal Language Server Protocol client focused on
// diagnostics. It is NOT a full LSP — only the subset we need to feed
// build/lint errors to the model after edits.
//
// Wire protocol: JSON-RPC 2.0 with the LSP framing
//
//	Content-Length: <bytes>\r\n
//	\r\n
//	<json>
//
// We support:
//   - initialize / initialized
//   - textDocument/didOpen
//   - textDocument/didChange (full sync)
//   - textDocument/publishDiagnostics (notification from server)
//   - shutdown / exit on Close
//
// Multiple language servers can be registered (one per language); each runs
// as its own subprocess. A Manager dispatches a NotifyChange call to the right
// server based on file extension and collects diagnostics for the affected
// path. Diagnostics are de-duplicated and the latest notification wins for any
// given URI.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/procutil"
)

// Diagnostic mirrors the LSP shape, simplified for our use.
type Diagnostic struct {
	Range struct {
		Start lspPosition `json:"start"`
		End   lspPosition `json:"end"`
	} `json:"range"`
	Severity int    `json:"severity"` // 1=error, 2=warn, 3=info, 4=hint
	Code     any    `json:"code"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// publishParams is the body of a textDocument/publishDiagnostics notification.
type publishParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Server is one running language server, scoped to a set of file extensions.
type Server struct {
	name       string
	root       string
	extensions map[string]bool
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser

	mu      sync.Mutex
	pending map[int64]chan rpcResult
	docs    map[string]int // open doc -> version
	diags   map[string][]Diagnostic
	nextID  int64

	closed atomic.Bool
	logger func(string)
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// rpcMessage is the JSON-RPC envelope (request/response/notification).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// startServer launches the server, performs the LSP handshake, and returns a
// ready Server. Startup failures are reported as errors so the caller can
// decide to skip this language quietly without blocking the agent.
func startServer(ctx context.Context, name, command string, args, env, exts []string, root string, logger func(string)) (*Server, error) {
	if logger == nil {
		logger = func(string) {}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	// Own session: no controlling terminal, so the server can't write to the TUI's
	// screen (its stdio is captured via the pipes below).
	procutil.Detach(cmd)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	cmd.Dir = root

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

	s := &Server{
		name:       name,
		root:       root,
		extensions: map[string]bool{},
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		pending:    map[int64]chan rpcResult{},
		docs:       map[string]int{},
		diags:      map[string][]Diagnostic{},
		logger:     logger,
	}
	for _, e := range exts {
		s.extensions[strings.ToLower(e)] = true
	}

	go s.readLoop()
	go s.drainStderr(stderr)

	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.handshake(hctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) handshake(ctx context.Context) error {
	rootURI := pathToURI(s.root)
	params, _ := json.Marshal(map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": filepath.Base(s.root)},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{},
				"synchronization": map[string]any{
					"didSave": true,
				},
			},
		},
	})
	if _, err := s.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return s.notify("initialized", json.RawMessage(`{}`))
}

// HandlesExtension reports whether this server should be notified when a file
// with the given extension changes.
func (s *Server) HandlesExtension(ext string) bool {
	return s.extensions[strings.ToLower(ext)]
}

// NotifyChange opens (if needed) or updates the document for path with the
// supplied content, sends a didChange, and returns. Diagnostics arrive
// asynchronously and are picked up by Diagnostics(path).
func (s *Server) NotifyChange(path, content string) error {
	uri := pathToURI(path)
	s.mu.Lock()
	version, open := s.docs[uri]
	if !open {
		s.docs[uri] = 1
		s.mu.Unlock()
		params, _ := json.Marshal(map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": languageFor(path),
				"version":    1,
				"text":       content,
			},
		})
		return s.notify("textDocument/didOpen", params)
	}
	version++
	s.docs[uri] = version
	s.mu.Unlock()
	params, _ := json.Marshal(map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": content}},
	})
	return s.notify("textDocument/didChange", params)
}

// Diagnostics waits up to timeout for diagnostics for path to arrive, then
// returns them. We poll because LSP delivers diagnostics asynchronously and
// many servers debounce them by 100–500ms.
func (s *Server) Diagnostics(ctx context.Context, path string, timeout time.Duration) []Diagnostic {
	uri := pathToURI(path)
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		d, ok := s.diags[uri]
		s.mu.Unlock()
		if ok {
			return d
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-time.After(80 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}

// Close shuts the server down (best effort).
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.call(ctx, "shutdown", json.RawMessage(`null`))
	_ = s.notify("exit", nil)
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	if s.cmd.Process != nil {
		procutil.Terminate(s.cmd.Process) // kill the whole group, not just the leader
	}
	_ = s.cmd.Wait()
	return nil
}

// call sends a JSON-RPC request and waits for the matching response.
func (s *Server) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if s.closed.Load() {
		return nil, errors.New("lsp server closed")
	}
	id := atomic.AddInt64(&s.nextID, 1)
	ch := make(chan rpcResult, 1)

	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(rpcMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.result, r.err
	}
}

// notify sends a JSON-RPC notification (no id).
func (s *Server) notify(method string, params json.RawMessage) error {
	return s.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}

// write encodes one LSP-framed message.
func (s *Server) write(msg rpcMessage) error {
	if s.closed.Load() {
		return errors.New("lsp server closed")
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.stdin, header); err != nil {
		return err
	}
	_, err = s.stdin.Write(body)
	return err
}

// lspDebug reports whether verbose LSP debug logging is enabled (MN_LSP_DEBUG
// set to 1/on/true/yes). Off by default — the diagnostics logging it guards is
// extremely chatty (publishDiagnostics fires on nearly every edit).
func lspDebug() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_LSP_DEBUG"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// severityLabel maps an LSP diagnostic severity (1=error, 2=warning, 3=info,
// 4=hint) to a human label, falling back to "sevN" for anything else.
func severityLabel(sev int) string {
	switch sev {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return fmt.Sprintf("sev%d", sev)
	}
}

// readLoop parses LSP-framed messages and dispatches them.
func (s *Server) readLoop() {
	if lspDebug() {
		s.logger(s.name + ": read loop started")
	}
	r := bufio.NewReader(s.stdout)
	for {
		length, err := readContentLength(r)
		if err != nil {
			if !s.closed.Load() {
				s.logger("lsp read: " + err.Error())
			}
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			s.logger("lsp body: " + err.Error())
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			s.logger("lsp parse: " + err.Error())
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			// response
			s.mu.Lock()
			ch := s.pending[*msg.ID]
			s.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- rpcResult{err: fmt.Errorf("lsp error %d: %s", msg.Error.Code, msg.Error.Message)}
				} else {
					ch <- rpcResult{result: msg.Result}
				}
			}
		case msg.Method == "textDocument/publishDiagnostics":
			var p publishParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				s.mu.Lock()
				s.diags[p.URI] = p.Diagnostics
				s.mu.Unlock()
				// Debug (MN_LSP_DEBUG): log what this server reported — error/warning
				// counts, then each diagnostic's 1-based line:col + severity label +
				// message. Off by default: publishDiagnostics fires on nearly every
				// edit, so this is far too chatty for normal use.
				if lspDebug() {
					var errs, warns int
					for _, d := range p.Diagnostics {
						switch d.Severity {
						case 1:
							errs++
						case 2:
							warns++
						}
					}
					s.logger(fmt.Sprintf("%s: diagnostics for %s — %d error(s), %d warning(s)", s.name, p.URI, errs, warns))
					for _, d := range p.Diagnostics {
						s.logger(fmt.Sprintf("%s:   %d:%d [%s] %s",
							s.name, d.Range.Start.Line+1, d.Range.Start.Character+1, severityLabel(d.Severity), d.Message))
					}
				}
			}
		default:
			// Unhandled requests/notifications (workspace/*, window/*) — ignore
			// silently. If the server expects a response, our silence will be
			// surfaced as a stderr complaint, which we forward via drainStderr.
		}
	}
}

func (s *Server) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for scanner.Scan() {
		s.logger(s.name + ": " + scanner.Text())
	}
}

// readContentLength reads the LSP framing header and returns the byte count.
func readContentLength(r *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if cl, ok := strings.CutPrefix(strings.ToLower(line), "content-length:"); ok {
			cl = strings.TrimSpace(cl)
			n := 0
			for _, c := range cl {
				if c < '0' || c > '9' {
					return 0, fmt.Errorf("invalid content-length: %q", cl)
				}
				n = n*10 + int(c-'0')
			}
			length = n
		}
	}
	if length < 0 {
		return 0, errors.New("missing content-length")
	}
	return length, nil
}

// pathToURI converts a filesystem path to a file:// URI.
func pathToURI(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return "file://" + (&url.URL{Path: filepath.ToSlash(p)}).String()
}

// languageFor returns the LSP languageId for a file's extension.
func languageFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".cc", ".cpp", ".cxx":
		return "cpp"
	case ".h", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	default:
		return "plaintext"
	}
}
