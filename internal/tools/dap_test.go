package tools_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/dap"
	"github.com/pysty1995/moony/internal/tools"
)

// ─── fake DAP server helpers (mirrored from dap_test.go) ──────────────────────

func startDAPServer(t *testing.T, handler func(conn net.Conn)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handler(conn)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	return ln
}

func dapReadMsg(t *testing.T, rd *bufio.Reader) json.RawMessage {
	t.Helper()
	var n int
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("dapReadMsg: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")), &n)
		}
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rd, buf); err != nil {
		t.Fatalf("dapReadMsg body: %v", err)
	}
	return buf
}

func dapWriteMsg(conn net.Conn, v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(body))
	conn.Write(body)
}

func dapReplyOK(conn net.Conn, reqSeq int, command string, body any) {
	rawBody, _ := json.Marshal(body)
	dapWriteMsg(conn, map[string]any{
		"seq": 99, "type": "response",
		"request_seq": reqSeq, "command": command,
		"success": true, "body": json.RawMessage(rawBody),
	})
}

func dapSendEvent(conn net.Conn, event string, body any) {
	rawBody, _ := json.Marshal(body)
	dapWriteMsg(conn, map[string]any{
		"seq": 100, "type": "event",
		"event": event, "body": json.RawMessage(rawBody),
	})
}

// readSeq reads one DAP request and returns its seq.
func readSeq(t *testing.T, rd *bufio.Reader) (int, string) {
	t.Helper()
	raw := dapReadMsg(t, rd)
	var req struct {
		Seq     int    `json:"seq"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("readSeq: %v", err)
	}
	return req.Seq, req.Command
}

// dialledSession dials the fake server, reads the mandatory initialize request,
// replies with capabilities, and returns the live *dap.Session.
func dialledSession(t *testing.T, ln net.Listener, extraHandler func(conn net.Conn, rd *bufio.Reader)) *dap.Session {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		rd := bufio.NewReader(conn)
		seq, _ := readSeq(t, rd)
		dapReplyOK(conn, seq, "initialize", map[string]bool{"supportsConfigurationDoneRequest": true})
		if extraHandler != nil {
			extraHandler(conn, rd)
		}
		close(ready)
	}()
	sess, err := dap.Dial(ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	<-ready
	return sess
}

// ─── tool schema tests ─────────────────────────────────────────────────────────

func TestDAPToolsRegistered(t *testing.T) {
	// Verify all 14 DAP tools appear in the registry's schema list.
	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	schemas := r.Schemas()
	names := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		names[s.Name] = true
	}

	want := []string{
		"dap_connect", "dap_disconnect", "dap_launch", "dap_attach",
		"dap_set_breakpoints", "dap_continue", "dap_next", "dap_step_in",
		"dap_step_out", "dap_pause", "dap_stack_trace", "dap_scopes",
		"dap_variables", "dap_evaluate",
	}
	for _, name := range want {
		if !names[name] {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestDAPToolSchemasHaveRequiredFields(t *testing.T) {
	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	// Tools that specify "required" in their schema — spot-check a few.
	required := map[string][]string{
		"dap_connect":          {"addr"},
		"dap_set_breakpoints":  {"file", "lines"},
		"dap_continue":         {"thread_id"},
		"dap_stack_trace":      {"thread_id"},
		"dap_evaluate":         {"expression"},
		"dap_variables":        {"variables_reference"},
	}

	schemas := r.Schemas()
	byName := make(map[string]json.RawMessage, len(schemas))
	for _, s := range schemas {
		byName[s.Name] = s.InputSchema
	}

	for toolName, wantReqs := range required {
		raw, ok := byName[toolName]
		if !ok {
			t.Errorf("tool %q not found in schemas", toolName)
			continue
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("%s: parse schema: %v", toolName, err)
			continue
		}
		reqSet := make(map[string]bool, len(schema.Required))
		for _, r := range schema.Required {
			reqSet[r] = true
		}
		for _, req := range wantReqs {
			if !reqSet[req] {
				t.Errorf("%s: required field %q missing from schema", toolName, req)
			}
		}
	}
}

// ─── "not connected" error path ───────────────────────────────────────────────

func TestDAPToolsErrorWhenNotConnected(t *testing.T) {
	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	// All tools except dap_connect should error when no session is set.
	toolsNeedingSession := []string{
		"dap_disconnect", "dap_launch", "dap_attach",
		"dap_set_breakpoints", "dap_continue", "dap_next", "dap_step_in",
		"dap_step_out", "dap_pause", "dap_stack_trace", "dap_scopes",
		"dap_variables", "dap_evaluate",
	}

	// Craft minimal valid JSON for each tool.
	validInput := map[string]string{
		"dap_disconnect":     `{}`,
		"dap_launch":         `{"args":{"program":"./main"}}`,
		"dap_attach":         `{"args":{"processId":1}}`,
		"dap_set_breakpoints": `{"file":"main.go","lines":[1]}`,
		"dap_continue":       `{"thread_id":1}`,
		"dap_next":           `{"thread_id":1}`,
		"dap_step_in":        `{"thread_id":1}`,
		"dap_step_out":       `{"thread_id":1}`,
		"dap_pause":          `{"thread_id":1}`,
		"dap_stack_trace":    `{"thread_id":1}`,
		"dap_scopes":         `{"frame_id":0}`,
		"dap_variables":      `{"variables_reference":1}`,
		"dap_evaluate":       `{"expression":"x"}`,
	}

	// Use execute via the registry's internal Execute helper — access via
	// schemas tool invocation.
	schemas := r.Schemas()
	schemaByName := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		schemaByName[s.Name] = true
	}

	ctx := context.Background()
	for _, name := range toolsNeedingSession {
		if !schemaByName[name] {
			t.Errorf("%s not registered", name)
			continue
		}
		input := json.RawMessage(validInput[name])
		_, err := r.Execute(ctx, name, input)
		if err == nil {
			t.Errorf("%s: expected 'not connected' error but got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "not connected") && !strings.Contains(err.Error(), "dap_connect") {
			t.Errorf("%s: error %q should mention 'not connected' or 'dap_connect'", name, err.Error())
		}
	}
}

// ─── dap_connect Execute ──────────────────────────────────────────────────────

func TestDAPConnectToolWiresSession(t *testing.T) {
	ln := startDAPServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)
		seq, _ := readSeq(t, rd)
		dapReplyOK(conn, seq, "initialize", map[string]bool{"supportsConfigurationDoneRequest": true})
		// Read disconnect, ignore.
		time.Sleep(200 * time.Millisecond)
	})

	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	input, _ := json.Marshal(map[string]string{"addr": ln.Addr().String()})
	result, err := r.Execute(context.Background(), "dap_connect", input)
	if err != nil {
		t.Fatalf("dap_connect: %v", err)
	}
	if !strings.Contains(result, "Connected") {
		t.Errorf("result = %q, want it to mention 'Connected'", result)
	}

	// After dap_connect, other tools should no longer return "not connected".
	// (They'll fail with a real DAP error since there's nothing to step into,
	// but the "not connected" guard is gone.)
	_, err = r.Execute(context.Background(), "dap_stack_trace", json.RawMessage(`{"thread_id":1}`))
	if err != nil && strings.Contains(err.Error(), "not connected") {
		t.Error("after dap_connect, dap_stack_trace should not return 'not connected'")
	}
}

// ─── dap_set_breakpoints Execute ──────────────────────────────────────────────

func TestDAPSetBreakpointsFormatsVerified(t *testing.T) {
	ln := startDAPServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)
		// initialize
		seq, _ := readSeq(t, rd)
		dapReplyOK(conn, seq, "initialize", nil)
		// setBreakpoints
		seq, cmd := readSeq(t, rd)
		if cmd != "setBreakpoints" {
			t.Errorf("expected setBreakpoints, got %q", cmd)
		}
		dapReplyOK(conn, seq, "setBreakpoints", map[string]any{
			"breakpoints": []map[string]any{
				{"verified": true, "line": 10},
				{"verified": false, "line": 20, "message": "not yet bound"},
			},
		})
		time.Sleep(100 * time.Millisecond)
	})

	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	// Connect first.
	connInput, _ := json.Marshal(map[string]string{"addr": ln.Addr().String()})
	if _, err := r.Execute(context.Background(), "dap_connect", connInput); err != nil {
		t.Fatalf("dap_connect: %v", err)
	}

	input, _ := json.Marshal(map[string]any{"file": "main.go", "lines": []int{10, 20}})
	result, err := r.Execute(context.Background(), "dap_set_breakpoints", input)
	if err != nil {
		t.Fatalf("dap_set_breakpoints: %v", err)
	}
	if !strings.Contains(result, "verified") {
		t.Errorf("result = %q, want 'verified' status", result)
	}
	if !strings.Contains(result, "unverified") {
		t.Errorf("result = %q, want 'unverified' status for line 20", result)
	}
}

// ─── dap_stack_trace Execute ──────────────────────────────────────────────────

func TestDAPStackTraceFormatsTable(t *testing.T) {
	ln := startDAPServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)
		seq, _ := readSeq(t, rd)
		dapReplyOK(conn, seq, "initialize", nil)
		seq, cmd := readSeq(t, rd)
		if cmd != "stackTrace" {
			t.Errorf("expected stackTrace, got %q", cmd)
		}
		dapReplyOK(conn, seq, "stackTrace", map[string]any{
			"stackFrames": []map[string]any{
				{"id": 1, "name": "main.main", "source": map[string]string{"path": "/src/main.go"}, "line": 42},
				{"id": 2, "name": "runtime.main", "line": 0},
			},
		})
		time.Sleep(100 * time.Millisecond)
	})

	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	connInput, _ := json.Marshal(map[string]string{"addr": ln.Addr().String()})
	if _, err := r.Execute(context.Background(), "dap_connect", connInput); err != nil {
		t.Fatalf("dap_connect: %v", err)
	}

	result, err := r.Execute(context.Background(), "dap_stack_trace", json.RawMessage(`{"thread_id":1}`))
	if err != nil {
		t.Fatalf("dap_stack_trace: %v", err)
	}
	if !strings.Contains(result, "main.main") || !strings.Contains(result, "/src/main.go") {
		t.Errorf("result = %q, want function name and file path", result)
	}
}

// ─── dap_evaluate Execute ─────────────────────────────────────────────────────

func TestDAPEvaluateReturnsResult(t *testing.T) {
	ln := startDAPServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)
		seq, _ := readSeq(t, rd)
		dapReplyOK(conn, seq, "initialize", nil)
		seq, cmd := readSeq(t, rd)
		if cmd != "evaluate" {
			t.Errorf("expected evaluate, got %q", cmd)
		}
		dapReplyOK(conn, seq, "evaluate", map[string]any{
			"result": "42",
			"type":   "int",
		})
		time.Sleep(100 * time.Millisecond)
	})

	r := tools.NewRegistry(t.TempDir())
	r.RegisterDefaults(1 << 20)

	connInput, _ := json.Marshal(map[string]string{"addr": ln.Addr().String()})
	if _, err := r.Execute(context.Background(), "dap_connect", connInput); err != nil {
		t.Fatalf("dap_connect: %v", err)
	}

	input, _ := json.Marshal(map[string]any{"expression": "x", "frame_id": 1})
	result, err := r.Execute(context.Background(), "dap_evaluate", input)
	if err != nil {
		t.Fatalf("dap_evaluate: %v", err)
	}
	if !strings.Contains(result, "42") || !strings.Contains(result, "int") {
		t.Errorf("result = %q, want '42' and 'int'", result)
	}
}
