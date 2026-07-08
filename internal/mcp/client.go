// Package mcp implements a small Model Context Protocol client over stdio
// and HTTP+SSE transports.
//
// MCP is a JSON-RPC 2.0 protocol that lets an LLM application (us) connect to
// external "servers" providing tools, resources, and prompts. We implement the
// core surface we need:
//
//   - initialize handshake
//   - notifications/initialized
//   - tools/list, tools/call
//   - resources/list, resources/read
//   - prompts/list, prompts/get
//
// Sampling and progress notifications are out of scope for this client.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol constants. The version string is what the spec calls a date-coded
// MCP protocol revision; the server may negotiate a different one.
const (
	protocolVersion = "2024-11-05"
	clientName      = "moony"
	clientVersion   = "0.1.0"
)

// rpcRequest is one JSON-RPC 2.0 request to the server.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"` // omitted for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one JSON-RPC 2.0 response from the server.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolDescriptor mirrors the shape of one entry in tools/list.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ResourceDescriptor mirrors one entry in resources/list.
type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// PromptDescriptor mirrors one entry in prompts/list.
type PromptDescriptor struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Arguments   []PromptArgDescriptor `json:"arguments"`
}

// PromptArgDescriptor describes one argument of a server-supplied prompt.
type PromptArgDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// callContent is one block of a tools/call response (we only consume text).
type callContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callResult is the structured part of a tools/call response.
type callResult struct {
	Content []callContent `json:"content"`
	IsError bool          `json:"isError"`
}

// resourceContent is one block of a resources/read response.
type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Blob     string `json:"blob"` // base64 — we expose its presence as a marker
}

// resourceResult is the structured part of a resources/read response.
type resourceResult struct {
	Contents []resourceContent `json:"contents"`
}

// promptMessage is one entry in a prompts/get response (we treat it as text).
type promptMessage struct {
	Role    string `json:"role"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// promptResult is the structured part of a prompts/get response.
type promptResult struct {
	Description string          `json:"description"`
	Messages    []promptMessage `json:"messages"`
}

// Client is a single connection to an MCP server.
type Client struct {
	transport Transport
	logger    func(string)

	mu      sync.Mutex
	pending map[string]chan rpcResponse
	nextID  int64
	closed  atomic.Bool
}

// Config describes one server to connect to. Either Command (stdio) or URL
// (HTTP+SSE) must be set; URL takes precedence when both are provided.
type Config struct {
	// Name is the user-facing label (also used as the tool-name prefix).
	Name string
	// Command is the stdio program to run.
	Command string
	// Args are the program's arguments.
	Args []string
	// URL is the HTTP+SSE endpoint base, e.g. "https://example.com/mcp".
	URL string
	// Env supplies extra environment vars for stdio (KEY=value), or HTTP
	// headers for the URL transport.
	Env []string
}

// Connect dials the server using the appropriate transport and performs the
// MCP handshake. The returned Client must be closed to release resources.
func Connect(ctx context.Context, cfg Config, logger func(string)) (*Client, error) {
	if logger == nil {
		logger = func(string) {}
	}
	var transport Transport
	var err error
	switch {
	case cfg.URL != "":
		transport, err = newHTTPSSETransport(ctx, cfg.URL, cfg.Env, logger)
	case cfg.Command != "":
		transport, err = newStdioTransport(ctx, cfg.Command, cfg.Args, cfg.Env, logger)
	default:
		return nil, fmt.Errorf("server %q: either command or url is required", cfg.Name)
	}
	if err != nil {
		return nil, err
	}

	c := &Client{
		transport: transport,
		logger:    logger,
		pending:   map[string]chan rpcResponse{},
	}
	go c.readLoop()

	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.handshake(hctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// handshake performs the MCP initialize/initialized exchange.
func (c *Client) handshake(ctx context.Context) error {
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	})
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("notifications/initialized", nil); err != nil {
		return fmt.Errorf("send initialized: %w", err)
	}
	return nil
}

// ListTools queries the server for the available tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	res, err := c.call(ctx, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse tools/list: %w", err)
	}
	return out.Tools, nil
}

// ListResources queries the server for the available resources. Returns nil,nil
// when the server does not implement resources (a non-standard but common case).
func (c *Client) ListResources(ctx context.Context) ([]ResourceDescriptor, error) {
	res, err := c.call(ctx, "resources/list", json.RawMessage(`{}`))
	if err != nil {
		if isMethodNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out struct {
		Resources []ResourceDescriptor `json:"resources"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse resources/list: %w", err)
	}
	return out.Resources, nil
}

// ReadResource fetches one resource. Binary blobs are surfaced as a placeholder
// so the model knows there's content but doesn't need the raw base64.
func (c *Client) ReadResource(ctx context.Context, uri string) (string, error) {
	params, _ := json.Marshal(map[string]string{"uri": uri})
	res, err := c.call(ctx, "resources/read", params)
	if err != nil {
		return "", err
	}
	var r resourceResult
	if err := json.Unmarshal(res, &r); err != nil {
		return "", fmt.Errorf("parse resources/read: %w", err)
	}
	var b strings.Builder
	for _, c := range r.Contents {
		if c.Text != "" {
			b.WriteString(c.Text)
			continue
		}
		if c.Blob != "" {
			fmt.Fprintf(&b, "(binary blob, %d bytes base64-encoded)\n", len(c.Blob))
		}
	}
	return b.String(), nil
}

// ListPrompts queries the server for prompt templates. Returns nil,nil when
// the server does not implement prompts.
func (c *Client) ListPrompts(ctx context.Context) ([]PromptDescriptor, error) {
	res, err := c.call(ctx, "prompts/list", json.RawMessage(`{}`))
	if err != nil {
		if isMethodNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out struct {
		Prompts []PromptDescriptor `json:"prompts"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("parse prompts/list: %w", err)
	}
	return out.Prompts, nil
}

// GetPrompt fetches one prompt template, expanded with the supplied arguments,
// and concatenates its messages into a single text body suitable for sending
// as a user turn.
func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, err := c.call(ctx, "prompts/get", params)
	if err != nil {
		return "", err
	}
	var r promptResult
	if err := json.Unmarshal(res, &r); err != nil {
		return "", fmt.Errorf("parse prompts/get: %w", err)
	}
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Content.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(m.Content.Text)
		}
	}
	return b.String(), nil
}

// CallTool invokes a tool on the server. The returned text concatenates every
// text content block in the result.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args)})
	res, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", false, err
	}
	var r callResult
	if err := json.Unmarshal(res, &r); err != nil {
		return "", false, fmt.Errorf("parse tools/call: %w", err)
	}
	var b []byte
	for _, c := range r.Content {
		if c.Type == "text" {
			b = append(b, c.Text...)
		}
	}
	return string(b), r.IsError, nil
}

// Close terminates the transport and the read loop.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.transport != nil {
		_ = c.transport.Close()
	}
	c.failPending(errors.New("client closed"))
	return nil
}

// call sends a JSON-RPC request and waits for the matching response.
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, errors.New("client is closed")
	}
	id := fmt.Sprintf("%d", atomic.AddInt64(&c.nextID, 1))
	ch := make(chan rpcResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.transport.Send(data); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, &rpcCallError{code: resp.Error.Code, msg: resp.Error.Message}
		}
		return resp.Result, nil
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string, params json.RawMessage) error {
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.transport.Send(data)
}

// readLoop consumes server frames and dispatches them to waiters.
func (c *Client) readLoop() {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		// Inherit Close() via the c.closed flag check below; we don't have a
		// shared cancel here intentionally so a long-lived stream doesn't
		// cancel mid-frame.
		_ = cancel

		frame, err := c.transport.Receive(ctx)
		cancel()
		if err != nil {
			if !c.closed.Load() {
				c.logger("read: " + err.Error())
			}
			c.failPending(err)
			return
		}
		var resp rpcResponse
		if err := json.Unmarshal(frame, &resp); err != nil {
			c.logger("malformed message: " + err.Error())
			continue
		}
		if len(resp.ID) == 0 || string(resp.ID) == "null" {
			continue
		}
		var idStr string
		if err := json.Unmarshal(resp.ID, &idStr); err != nil {
			idStr = string(resp.ID)
		}
		c.mu.Lock()
		ch := c.pending[idStr]
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

// failPending unblocks every waiting caller with an error.
func (c *Client) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msg := rpcResponse{Error: &rpcError{Code: -1, Message: err.Error()}}
	for id, ch := range c.pending {
		select {
		case ch <- msg:
		default:
		}
		delete(c.pending, id)
	}
}

// rpcCallError is the error type returned from c.call when the server replied
// with an error object. We type it so isMethodNotFound can identify the JSON-
// RPC "Method not found" code (-32601) used to detect optional capabilities.
type rpcCallError struct {
	code int
	msg  string
}

func (e *rpcCallError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.code, e.msg) }

func isMethodNotFound(err error) bool {
	var rpc *rpcCallError
	if errors.As(err, &rpc) {
		return rpc.code == -32601
	}
	return false
}
