package dap_test

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
)

// ─── fake DAP server helpers ──────────────────────────────────────────────────

// fakeServer is a minimal in-process DAP adapter for testing. It listens on a
// TCP port, echoes the connection through fakeHandler, and closes cleanly.
type fakeServer struct {
	ln net.Listener
	mu sync.Mutex
	wg sync.WaitGroup
}

func startFakeServer(t *testing.T, handler func(conn net.Conn)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fakeServer: listen: %v", err)
	}
	fs := &fakeServer{ln: ln}
	fs.wg.Add(1)
	go func() {
		defer fs.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			fs.wg.Add(1)
			go func() {
				defer fs.wg.Done()
				handler(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		fs.wg.Wait()
	})
	return fs
}

func (fs *fakeServer) Addr() string { return fs.ln.Addr().String() }

// readDAP reads one DAP message (Content-Length framed JSON) from a connection.
func readDAP(t *testing.T, rd *bufio.Reader) json.RawMessage {
	t.Helper()
	var contentLength int
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("readDAP: read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")), &contentLength)
		}
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(rd, buf); err != nil {
		t.Fatalf("readDAP: read body: %v", err)
	}
	return json.RawMessage(buf)
}

// writeDAP sends one DAP message (Content-Length framed JSON) to a connection.
func writeDAP(t *testing.T, conn net.Conn, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("writeDAP: marshal: %v", err)
	}
	hdr := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(conn, hdr); err != nil {
		t.Fatalf("writeDAP: write header: %v", err)
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatalf("writeDAP: write body: %v", err)
	}
}

// sendResponse sends a DAP response matching the given request_seq.
func sendResponse(t *testing.T, conn net.Conn, reqSeq int, command string, body any) {
	t.Helper()
	bodyRaw, _ := json.Marshal(body)
	writeDAP(t, conn, map[string]any{
		"seq":         1,
		"type":        "response",
		"request_seq": reqSeq,
		"command":     command,
		"success":     true,
		"body":        json.RawMessage(bodyRaw),
	})
}

// sendEvent sends a DAP event to the client.
func sendEvent(t *testing.T, conn net.Conn, eventName string, body any) {
	t.Helper()
	bodyRaw, _ := json.Marshal(body)
	writeDAP(t, conn, map[string]any{
		"seq":   99,
		"type":  "event",
		"event": eventName,
		"body":  json.RawMessage(bodyRaw),
	})
}

// ─── wire framing round-trip ─────────────────────────────────────────────────

func TestFramingRoundTrip(t *testing.T) {
	// Start a fake server that accepts one connection, reads one DAP message,
	// echoes it back as a response, then closes.
	fs := startFakeServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)
		raw := readDAP(t, rd)

		var req struct {
			Seq     int    `json:"seq"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}

		// For initialize: reply with capabilities body.
		sendResponse(t, conn, req.Seq, req.Command, map[string]any{
			"supportsConfigurationDoneRequest": true,
		})
	})

	// Create a raw TCP connection and send one DAP initialize-like message.
	conn, err := net.DialTimeout("tcp", fs.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Write a raw Content-Length framed message.
	msg := `{"seq":1,"type":"request","command":"initialize","arguments":{"clientID":"test"}}`
	fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n%s", len(msg), msg)

	// Read the response back and check it round-tripped.
	rd := bufio.NewReader(conn)
	raw := readDAP(t, rd)
	var resp struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Type != "response" || resp.Command != "initialize" || !resp.Success {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// ─── Session.Send request/response correlation ───────────────────────────────

func TestSessionSendCorrelation(t *testing.T) {
	// A fake server that handles the mandatory initialize handshake and then
	// responds to one arbitrary request.
	fs := startFakeServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)

		// Respond to initialize.
		raw := readDAP(t, rd)
		var req struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(raw, &req)
		sendResponse(t, conn, req.Seq, "initialize", map[string]bool{"supportsConfigurationDoneRequest": true})

		// Respond to the next request with a custom body.
		raw = readDAP(t, rd)
		_ = json.Unmarshal(raw, &req)
		sendResponse(t, conn, req.Seq, "evaluate", map[string]any{"result": "42", "type": "int"})
	})

	sess, err := dap.Dial(fs.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	resp, err := sess.Send("evaluate", map[string]any{"expression": "x"}, 5*time.Second)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !resp.Success {
		t.Fatalf("response not success: %s", resp.Message)
	}
	var body dap.EvaluateBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Result != "42" || body.Type != "int" {
		t.Errorf("body = %+v, want result=42 type=int", body)
	}
}

// ─── event dispatching ────────────────────────────────────────────────────────

func TestStoppedEventUpdatesState(t *testing.T) {
	fs := startFakeServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)

		// Respond to initialize.
		raw := readDAP(t, rd)
		var req struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(raw, &req)
		sendResponse(t, conn, req.Seq, "initialize", nil)

		// Fire a "stopped" event.
		sendEvent(t, conn, "stopped", map[string]any{
			"reason":   "breakpoint",
			"threadId": 1,
		})

		// Keep alive long enough for the client to read it.
		time.Sleep(200 * time.Millisecond)
	})

	sess, err := dap.Dial(fs.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tid, reason, err := sess.WaitStopped(ctx)
	if err != nil {
		t.Fatalf("WaitStopped: %v", err)
	}
	if tid != 1 || reason != "breakpoint" {
		t.Errorf("WaitStopped = tid=%d reason=%q, want tid=1 reason=breakpoint", tid, reason)
	}
}

func TestOutputEventIsBuffered(t *testing.T) {
	fs := startFakeServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)

		raw := readDAP(t, rd)
		var req struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(raw, &req)
		sendResponse(t, conn, req.Seq, "initialize", nil)

		sendEvent(t, conn, "output", map[string]any{"category": "stdout", "output": "hello\n"})
		sendEvent(t, conn, "output", map[string]any{"category": "stdout", "output": "world\n"})
		time.Sleep(200 * time.Millisecond)
	})

	sess, err := dap.Dial(fs.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	// Wait briefly so the events arrive.
	time.Sleep(300 * time.Millisecond)
	out := sess.DrainOutput()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("DrainOutput = %q, want both 'hello' and 'world'", out)
	}
	// Second drain should be empty (buffer reset).
	if sess.DrainOutput() != "" {
		t.Error("second DrainOutput should be empty")
	}
}

// ─── WaitStopped timeout ──────────────────────────────────────────────────────

func TestWaitStoppedTimesOut(t *testing.T) {
	fs := startFakeServer(t, func(conn net.Conn) {
		defer conn.Close()
		rd := bufio.NewReader(conn)

		raw := readDAP(t, rd)
		var req struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(raw, &req)
		sendResponse(t, conn, req.Seq, "initialize", nil)
		// Never fire a stopped event.
		time.Sleep(2 * time.Second)
	})

	sess, err := dap.Dial(fs.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err = sess.WaitStopped(ctx)
	if err == nil {
		t.Error("WaitStopped should have returned an error on timeout")
	}
}

// ─── SessionHolder ────────────────────────────────────────────────────────────

func TestSessionHolderGetBeforeSet(t *testing.T) {
	h := &dap.SessionHolder{}
	_, err := h.Get()
	if err == nil {
		t.Error("Get() on empty holder should return an error")
	}
	if !strings.Contains(err.Error(), "dap_connect") {
		t.Errorf("error should mention dap_connect, got: %v", err)
	}
}

func TestSessionHolderSetClearGet(t *testing.T) {
	// Use a nil session for the holder value; we only care about Set/Clear/Get.
	h := &dap.SessionHolder{}
	// Can't set nil *Session easily (Get checks for nil), so just verify Clear
	// returns the "not connected" error.
	h.Clear()
	if _, err := h.Get(); err == nil {
		t.Error("Get() after Clear() should return an error")
	}
}
