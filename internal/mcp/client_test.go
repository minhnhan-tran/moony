package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport is a controllable in-process Transport that mimics an MCP
// server so we can exercise Client without spawning processes or binding ports.
type fakeTransport struct {
	mu      sync.Mutex
	frames  chan []byte
	closed  bool
	handler func(req rpcRequest) (any, *rpcError) // returns result or error
}

func newFakeTransport(handler func(req rpcRequest) (any, *rpcError)) *fakeTransport {
	return &fakeTransport{
		frames:  make(chan []byte, 32),
		handler: handler,
	}
}

func (f *fakeTransport) Send(data []byte) error {
	var req rpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}
	if req.ID == nil { // notification, no reply
		return nil
	}
	go func() {
		result, rpcErr := f.handler(req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			resp["error"] = rpcErr
		} else {
			resp["result"] = result
		}
		out, _ := json.Marshal(resp)

		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return
		}
		f.frames <- out
	}()
	return nil
}

func (f *fakeTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case frame, ok := <-f.frames:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.frames)
	return nil
}

// newFakeClient stitches a Client onto a fakeTransport and runs the handshake.
func newFakeClient(t *testing.T, handler func(req rpcRequest) (any, *rpcError)) *Client {
	t.Helper()
	tr := newFakeTransport(handler)
	c := &Client{
		transport: tr,
		logger:    func(string) {},
		pending:   map[string]chan rpcResponse{},
	}
	go c.readLoop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClientToolsRoundTrip(t *testing.T) {
	c := newFakeClient(t, func(req rpcRequest) (any, *rpcError) {
		switch req.Method {
		case "initialize":
			return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}, nil
		case "tools/list":
			return map[string]any{
				"tools": []map[string]any{{
					"name":        "echo",
					"description": "echoes its input",
					"inputSchema": map[string]any{"type": "object"},
				}},
			}, nil
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echo: " + args.Text}},
			}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("expected one tool 'echo', got %+v", tools)
	}

	args, _ := json.Marshal(map[string]string{"text": "ping"})
	out, isErr, err := c.CallTool(ctx, "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if isErr || !strings.Contains(out, "echo: ping") {
		t.Fatalf("unexpected output: isErr=%v out=%q", isErr, out)
	}
}

func TestClientResourcesAndPrompts(t *testing.T) {
	c := newFakeClient(t, func(req rpcRequest) (any, *rpcError) {
		switch req.Method {
		case "initialize":
			return map[string]any{}, nil
		case "resources/list":
			return map[string]any{
				"resources": []map[string]any{{
					"uri": "file:///doc/readme.md", "name": "README",
				}},
			}, nil
		case "resources/read":
			return map[string]any{
				"contents": []map[string]any{{"uri": "file:///doc/readme.md", "text": "hello"}},
			}, nil
		case "prompts/list":
			return map[string]any{
				"prompts": []map[string]any{{"name": "review", "description": "review code"}},
			}, nil
		case "prompts/get":
			return map[string]any{
				"messages": []map[string]any{{
					"role":    "user",
					"content": map[string]any{"type": "text", "text": "Please review:\n\nfoo"},
				}},
			}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resources, err := c.ListResources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].URI != "file:///doc/readme.md" {
		t.Fatalf("unexpected resources: %+v", resources)
	}
	body, err := c.ReadResource(ctx, resources[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}

	prompts, err := c.ListPrompts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0].Name != "review" {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	got, err := c.GetPrompt(ctx, "review", map[string]string{"target": "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Please review:") {
		t.Fatalf("expected prompt body, got %q", got)
	}
}

func TestResourcesUnsupportedReturnsNil(t *testing.T) {
	c := newFakeClient(t, func(req rpcRequest) (any, *rpcError) {
		if req.Method == "initialize" {
			return map[string]any{}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := c.ListResources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil for unsupported, got %+v", got)
	}
}

func TestLoadConfigsProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	gd := filepath.Join(home, ".config", "moony")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "mcp.json"),
		[]byte(`{"servers":{"fs":{"command":"global-bin"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	pd := filepath.Join(proj, ".sqe")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "mcp.json"),
		[]byte(`{"servers":{"fs":{"command":"project-bin","args":["x"]},"db":{"command":"db-bin","disabled":true},"remote":{"url":"https://x.example/mcp"}}}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs(proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 enabled servers, got %+v", cfgs)
	}
	var fs, remote *Config
	for i := range cfgs {
		switch cfgs[i].Name {
		case "fs":
			fs = &cfgs[i]
		case "remote":
			remote = &cfgs[i]
		}
	}
	if fs == nil || fs.Command != "project-bin" {
		t.Errorf("fs override failed: %+v", fs)
	}
	if remote == nil || remote.URL != "https://x.example/mcp" {
		t.Errorf("remote URL not parsed: %+v", remote)
	}
}

func TestLoadConfigsMalformed(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, ".sqe")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "mcp.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), ".config"))
	if _, err := LoadConfigs(dir); err == nil {
		t.Fatal("expected error for malformed config")
	}
}

// Sanity: ensure isMethodNotFound matches the JSON-RPC standard code.
func TestIsMethodNotFound(t *testing.T) {
	if !isMethodNotFound(&rpcCallError{code: -32601, msg: "method not found"}) {
		t.Error("expected match for -32601")
	}
	if isMethodNotFound(errors.New("other")) {
		t.Error("non-rpc error should not match")
	}
}
