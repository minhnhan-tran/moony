package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeACPServer speaks just enough of the agent side of ACP to drive the client:
// it answers initialize/session.new, and on session/prompt it (optionally) asks
// for permission on an edit tool, streams an assistant message chunk, then
// returns a stop reason. It records the permission outcome the client chose.
type fakeACPServer struct {
	in            *bufio.Reader // client -> server (the client's stdin)
	out           io.Writer     // server -> client (the client's stdout)
	askPermission bool

	permissionOutcome string // captured "selected"/"cancelled"
	permissionOptID   string
}

func (s *fakeACPServer) run() {
	for {
		line, err := s.in.ReadBytes('\n')
		if len(line) > 0 {
			s.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (s *fakeACPServer) send(v any) {
	data, _ := json.Marshal(v)
	s.out.Write(append(data, '\n'))
}

func (s *fakeACPServer) handle(line []byte) {
	var m rpcMsg
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.Method {
	case "initialize":
		s.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID), "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}})
	case "session/new":
		s.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID), "result": map[string]any{"sessionId": "sess-1"}})
	case "session/prompt":
		if s.askPermission {
			s.requestPermission()
		}
		// Stream the assistant's answer, then finish.
		s.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "sess-1",
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "hello "}},
		}})
		s.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "sess-1",
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "world"}},
		}})
		s.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID), "result": map[string]any{"stopReason": "end_turn"}})
	}
}

// requestPermission asks the client to approve an "edit" tool and records what
// the client decides (a read-only client must reject it).
func (s *fakeACPServer) requestPermission() {
	reqID := json.RawMessage(`"perm-1"`)
	s.send(map[string]any{"jsonrpc": "2.0", "id": reqID, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "sess-1",
		"toolCall":  map[string]any{"toolCallId": "tc1", "kind": "edit"},
		"options": []any{
			map[string]any{"optionId": "ok", "name": "Allow", "kind": "allow_once"},
			map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
		},
	}})
	// Read the client's response to the permission request.
	line, err := s.in.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var resp rpcMsg
	if json.Unmarshal(line, &resp) == nil {
		var out struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(resp.Result, &out)
		s.permissionOutcome = out.Outcome.Outcome
		s.permissionOptID = out.Outcome.OptionID
	}
}

// newPipedACPSession wires an acpSession to an in-process fake server over pipes.
func newPipedACPSession(t *testing.T, readOnly, askPermission bool) (*acpSession, *fakeACPServer) {
	t.Helper()
	clientR, clientW := io.Pipe() // client stdin: client writes -> server reads
	serverR, serverW := io.Pipe() // client stdout: server writes -> client reads

	srv := &fakeACPServer{in: bufio.NewReader(clientR), out: serverW, askPermission: askPermission}
	go srv.run()

	s := newACPSession(Options{Cwd: t.TempDir(), ReadOnly: readOnly, Timeout: 5 * time.Second}, clientW, serverR, nil)
	if err := s.handshake(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return s, srv
}

func TestACPHandshakeAndPrompt(t *testing.T) {
	s, _ := newPipedACPSession(t, true, false)
	defer s.Close()

	if s.sessionID != "sess-1" {
		t.Fatalf("handshake did not capture sessionId, got %q", s.sessionID)
	}

	var streamed strings.Builder
	res, err := s.Prompt(context.Background(), "say hi", func(e Event) {
		if e.Kind == "text" {
			streamed.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if res.Text != "hello world" {
		t.Fatalf("accumulated text = %q, want %q", res.Text, "hello world")
	}
	if !res.OK {
		t.Fatalf("expected OK result, got %+v", res)
	}
	if streamed.String() != "hello world" {
		t.Fatalf("streamed text = %q", streamed.String())
	}
}

func TestACPReadOnlyRejectsEdit(t *testing.T) {
	s, srv := newPipedACPSession(t, true, true)
	defer s.Close()

	if _, err := s.Prompt(context.Background(), "edit something", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if srv.permissionOutcome != "selected" || srv.permissionOptID != "no" {
		t.Fatalf("read-only client should select the reject option, got outcome=%q optID=%q", srv.permissionOutcome, srv.permissionOptID)
	}
}

func TestACPWritableAllowsEdit(t *testing.T) {
	s, srv := newPipedACPSession(t, false, true) // not read-only
	defer s.Close()

	if _, err := s.Prompt(context.Background(), "edit something", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if srv.permissionOutcome != "selected" || srv.permissionOptID != "ok" {
		t.Fatalf("writable client should allow the edit, got outcome=%q optID=%q", srv.permissionOutcome, srv.permissionOptID)
	}
}

func TestACPPermissionDecisionHelper(t *testing.T) {
	s := &acpSession{opts: Options{ReadOnly: true}}
	params := json.RawMessage(`{"toolCall":{"kind":"read"},"options":[{"optionId":"a","kind":"allow_once"},{"optionId":"r","kind":"reject_once"}]}`)
	got := s.permissionDecision(params)
	outcome := got["outcome"].(map[string]any)
	if outcome["optionId"] != "a" {
		t.Fatalf("read tool should be allowed even read-only, got %v", outcome)
	}
}

// TestACPLiveHandshake validates the client against the real `optimus acp`
// binary: it confirms the NDJSON framing, protocol version, and
// initialize+session/new handshake actually interoperate. Skipped unless
// MN_ACP_LIVE=1 (needs an ACP agent installed).
func TestACPLiveHandshake(t *testing.T) {
	if os.Getenv("MN_ACP_LIVE") != "1" {
		t.Skip("set MN_ACP_LIVE=1 to run the live optimus acp handshake test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := openACP(ctx, Options{Cwd: t.TempDir(), ReadOnly: true, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("openACP against real optimus: %v", err)
	}
	defer sess.Close()
	if s, ok := sess.(*acpSession); !ok || s.sessionID == "" {
		t.Fatalf("expected a session id from the live handshake")
	}
	t.Log("live ACP handshake OK")
}
