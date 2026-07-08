package tools

// Apple Fetch / Apple Search tools, ported from Optimus (Apple's OpenCode fork).
// Both delegate to a native macOS runtime, "AppleFetchMCP", which authenticates
// to Apple-internal web content via AppleConnect/AppSSO. We talk to it over the
// Model Context Protocol's JSON-RPC-on-stdio transport:
//
//	initialize  ->  initialized (notify)  ->  tools/call {AppleFetch|AppleSearch}
//
// We vendor the runtime under assets/apple-fetch (the AppleFetch.framework from
// the AppleFetchBinaries Swift package + a small MCP wrapper executable), so the
// tools work without an Optimus install. The bundle is committed unsigned and
// ad-hoc signed on first use. An external copy (Optimus install, or
// SQE_APPLE_FETCH_PATH) is used in preference when present.
//
// These tools are macOS-only and read-only (no filesystem mutation), so they are
// not gated behind the mutating-tool approval policy. On non-macOS hosts, or when
// the runtime can't be found, Execute returns a clear, actionable error.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// appleFetchExecutable is the path inside the .app bundle to the native runtime.
const appleFetchExecutable = "Contents/MacOS/AppleFetchMCP"

// appleFetchEnvPaths are env vars (in priority order) that may point at the
// runtime — either the executable itself or the .app bundle directory. The
// OPTIMUS_/APPLE_FETCH names match Optimus so an existing install is reused.
var appleFetchEnvPaths = []string{
	"SQE_APPLE_FETCH_PATH",
	"OPTIMUS_APPLE_FETCH_PATH",
	"OPTIMUS_APPLE_FETCH_MCP_PATH",
	"APPLE_FETCH_MCP_PATH",
}

// appleRPCTimeout bounds the initialize/handshake calls.
const appleRPCTimeout = 30 * time.Second

// ---- tool: apple_fetch -----------------------------------------------------

// appleFetchTool fetches Apple-internal web content with native AppleConnect
// authentication, via the bundled AppleFetchMCP runtime. Read-only.
type appleFetchTool struct{}

func (t *appleFetchTool) Name() string { return "apple_fetch" }

func (t *appleFetchTool) Description() string {
	return "Fetch Apple-internal web content with native Apple Fetch (AppleConnect/AppSSO) " +
		"authentication. Prefer this over web_fetch for `.apple.com` URLs that require " +
		"Apple corporate network access or AppSSO. Returns extracted text/markdown from the " +
		"page. Read-only; macOS only. The URL may omit the scheme (normalized to HTTPS). " +
		"For public, non-Apple sites use web_fetch instead."
}

func (t *appleFetchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"url":           {"type": "string", "description": "The Apple-internal URL to fetch. The scheme may be omitted."},
			"timeout":       {"type": "integer", "description": "Request timeout in milliseconds (1000-60000). Defaults to 30000."},
			"preserveLinks": {"type": "boolean", "description": "Preserve HTML links in the markdown output. Defaults to true."},
			"returnRawHTML": {"type": "boolean", "description": "Return raw HTML instead of processed text. Defaults to false."},
			"maxLength":     {"type": "integer", "description": "Maximum output length in characters. Defaults to 50000."}
		},
		"required": ["url"]
	}`)
}

func (t *appleFetchTool) Mutates() bool { return false }

func (t *appleFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL           string `json:"url"`
		Timeout       int    `json:"timeout,omitempty"`
		PreserveLinks *bool  `json:"preserveLinks,omitempty"`
		ReturnRawHTML *bool  `json:"returnRawHTML,omitempty"`
		MaxLength     int    `json:"maxLength,omitempty"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.URL) == "" {
		return "", fmt.Errorf("url is required")
	}
	// Build the native tool arguments, omitting unset optionals so the runtime
	// applies its own defaults.
	args := map[string]any{"url": in.URL}
	if in.Timeout > 0 {
		args["timeout"] = in.Timeout
	}
	if in.PreserveLinks != nil {
		args["preserveLinks"] = *in.PreserveLinks
	}
	if in.ReturnRawHTML != nil {
		args["returnRawHTML"] = *in.ReturnRawHTML
	}
	if in.MaxLength > 0 {
		args["maxLength"] = in.MaxLength
	}

	timeout := appleRPCTimeout
	if in.Timeout >= 1000 && in.Timeout <= 60000 {
		timeout = time.Duration(in.Timeout)*time.Millisecond + 5*time.Second
	}
	return callAppleFetch(ctx, "AppleFetch", args, timeout)
}

// ---- tool: apple_search ----------------------------------------------------

// appleSearchTool searches Apple's internal knowledge base via the bundled
// AppleFetchMCP runtime. Read-only.
type appleSearchTool struct{}

func (t *appleSearchTool) Name() string { return "apple_search" }

func (t *appleSearchTool) Description() string {
	return "Search Apple's internal knowledge base with native Apple Fetch (AppleConnect/AppSSO) " +
		"authentication. Use this to find internal Apple documentation, process pages, and " +
		"resources when the exact URL is unknown; then use apple_fetch on a specific result. " +
		"Read-only; macOS only."
}

func (t *appleSearchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The Apple-internal knowledge base search query."}
		},
		"required": ["query"]
	}`)
}

func (t *appleSearchTool) Mutates() bool { return false }

func (t *appleSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	return callAppleFetch(ctx, "AppleSearch", map[string]any{"query": in.Query}, appleRPCTimeout)
}

// ---- native runtime resolution ---------------------------------------------

// resolveAppleFetchExecutable finds the AppleFetchMCP native runtime, or "" if
// it isn't available. Search order: env-var overrides, the runtime vendored in
// this repo (assets/apple-fetch), an existing Optimus install's downloaded copy,
// a copy beside our binary, and finally the PATH. macOS-only.
//
// The vendored bundle is the AppleFetch.framework from the AppleFetchBinaries
// Swift package plus the small MCP wrapper executable. Its code signature is
// stripped before committing (signatures don't survive a git checkout), so we
// ad-hoc re-sign it on first use — see ensureAppleFetchSigned.
func resolveAppleFetchExecutable() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, key := range appleFetchEnvPaths {
		if p := executableFromCandidate(os.Getenv(key)); p != "" {
			return p
		}
	}
	var execDir string
	if exe, err := os.Executable(); err == nil {
		execDir = filepath.Dir(exe)
	}
	candidates := []string{
		// The runtime vendored in this repo (works without Optimus installed).
		filepath.Join(execDir, "assets", "apple-fetch", "AppleFetchMCP.app", appleFetchExecutable),
		// Source-tree layout: <repo>/bin/moony -> <repo>/assets/...
		filepath.Join(execDir, "..", "assets", "apple-fetch", "AppleFetchMCP.app", appleFetchExecutable),
		// An existing Optimus install's downloaded runtime.
		optimusAppleFetchPath(),
		// A copy installed alongside our own binary / in the user install dir.
		filepath.Join(execDir, "apple-fetch", "AppleFetchMCP.app", appleFetchExecutable),
		filepath.Join(execDir, "AppleFetchMCP.app", appleFetchExecutable),
		filepath.Join(os.Getenv("HOME"), ".sqe", "apple-fetch", "AppleFetchMCP.app", appleFetchExecutable),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "assets", "apple-fetch", "AppleFetchMCP.app", appleFetchExecutable))
	}
	for _, c := range candidates {
		if existingFile(c) {
			return c
		}
	}
	if p, err := exec.LookPath("AppleFetchMCP"); err == nil {
		return p
	}
	return ""
}

// appBundleFor returns the .../AppleFetchMCP.app directory containing the given
// executable path (…/Contents/MacOS/AppleFetchMCP), or "" if the layout doesn't
// match.
func appBundleFor(executable string) string {
	// executable = <app>/Contents/MacOS/AppleFetchMCP
	app := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}

// ensureAppleFetchSigned ad-hoc signs the bundle if needed so dyld will load its
// AppleFetch.framework. We vendor the bundle without a signature (git can't keep
// one), and a marker file records that we've signed this copy so repeated calls
// are cheap. Best-effort: if signing fails we still try to run (an externally
// installed, already-signed copy needs nothing). Only the vendored copy under a
// writable dir is touched; codesign is a no-op when already valid.
func ensureAppleFetchSigned(executable string) {
	app := appBundleFor(executable)
	if app == "" {
		return
	}
	marker := filepath.Join(app, ".sqe-codesigned")
	if existingFile(marker) {
		return
	}
	// Already validly signed? Then just record it.
	if exec.Command("/usr/bin/codesign", "-v", "--deep", app).Run() == nil {
		_ = os.WriteFile(marker, []byte("ok\n"), 0o644)
		return
	}
	fw := filepath.Join(app, "Contents", "Frameworks", "AppleFetch.framework", "Versions", "A")
	if existingFile(filepath.Join(fw, "AppleFetch")) {
		_ = exec.Command("/usr/bin/codesign", "-f", "-s", "-", fw).Run()
	}
	if exec.Command("/usr/bin/codesign", "-f", "-s", "-", "--deep", app).Run() == nil {
		_ = os.WriteFile(marker, []byte("ok\n"), 0o644)
	}
}

// optimusAppleFetchPath returns the path where an installed Optimus keeps its
// downloaded AppleFetchMCP runtime: $XDG_DATA_HOME/optimus (default
// ~/.local/share/optimus) /desktop-apple-fetch/current/apple-fetch/...
func optimusAppleFetchPath() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(data, "optimus", "desktop-apple-fetch", "current",
		"apple-fetch", "AppleFetchMCP.app", appleFetchExecutable)
}

// executableFromCandidate resolves a user-supplied path that may be the
// executable directly, an .app bundle dir, or a parent containing one.
func executableFromCandidate(candidate string) string {
	if candidate == "" {
		return ""
	}
	if strings.HasPrefix(candidate, "~/") {
		candidate = filepath.Join(os.Getenv("HOME"), candidate[2:])
	}
	if existingFile(candidate) {
		return candidate
	}
	for _, rel := range []string{
		appleFetchExecutable,
		filepath.Join("AppleFetchMCP.app", appleFetchExecutable),
		filepath.Join("apple-fetch", "AppleFetchMCP.app", appleFetchExecutable),
	} {
		if p := filepath.Join(candidate, rel); existingFile(p) {
			return p
		}
	}
	return ""
}

func existingFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ---- minimal MCP-stdio JSON-RPC client -------------------------------------

// callAppleFetch spawns the native runtime, performs the MCP handshake, calls
// the named tool, and returns its concatenated text content.
func callAppleFetch(ctx context.Context, name string, args map[string]any, timeout time.Duration) (string, error) {
	bin := resolveAppleFetchExecutable()
	if bin == "" {
		if runtime.GOOS != "darwin" {
			return "", fmt.Errorf("apple_fetch/apple_search require macOS (AppleConnect/AppSSO); current OS is %s", runtime.GOOS)
		}
		return "", fmt.Errorf("Apple Fetch native runtime not found. " +
			"It ships vendored under assets/apple-fetch; if you removed it, install Optimus " +
			"(which downloads AppleFetchMCP) or set SQE_APPLE_FETCH_PATH to an AppleFetchMCP.app")
	}
	// Vendored copy is committed unsigned; ad-hoc sign it once so dyld loads its
	// framework. No-op for an already-signed external copy.
	ensureAppleFetchSigned(bin)

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start AppleFetchMCP: %w", err)
	}

	c := &appleRPCClient{stdin: stdin, reader: bufio.NewReaderSize(stdout, 1<<20)}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Handshake.
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "moony", "version": "native-tool"},
	}, appleRPCTimeout); err != nil {
		return "", fmt.Errorf("apple fetch initialize: %w", err)
	}
	if err := c.notify("initialized", nil); err != nil {
		return "", err
	}

	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, timeout)
	if err != nil {
		return "", fmt.Errorf("apple fetch %s: %w", name, err)
	}
	text, isErr := textFromToolResult(res)
	if isErr {
		if text == "" {
			text = name + " failed"
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" {
		return "(no content returned)", nil
	}
	return text, nil
}

// appleRPCClient is a tiny synchronous JSON-RPC-over-stdio client: one in-flight
// request at a time, which is all the handshake + single tools/call needs.
type appleRPCClient struct {
	mu     sync.Mutex
	nextID int
	stdin  interface{ Write([]byte) (int, error) }
	reader *bufio.Reader
}

type appleRPCResponse struct {
	ID     json.Number     `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call writes a request and reads newline-delimited responses until it sees the
// one with the matching id (skipping notifications/other lines).
func (c *appleRPCClient) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
		return nil, err
	}

	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		for {
			line, err := c.reader.ReadBytes('\n')
			if len(line) > 0 {
				var resp appleRPCResponse
				if json.Unmarshal(line, &resp) == nil && resp.ID.String() != "" {
					if n, _ := resp.ID.Int64(); int(n) == id {
						if resp.Error != nil {
							done <- result{err: fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)}
							return
						}
						done <- result{raw: resp.Result}
						return
					}
				}
			}
			if err != nil {
				done <- result{err: fmt.Errorf("AppleFetchMCP closed the connection: %w", err)}
				return
			}
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("AppleFetchMCP timed out handling %s", method)
	case r := <-done:
		return r.raw, r.err
	}
}

func (c *appleRPCClient) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return c.write(req)
}

func (c *appleRPCClient) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// textFromToolResult extracts the joined text content from an MCP tools/call
// result and whether the result was flagged isError.
func textFromToolResult(raw json.RawMessage) (string, bool) {
	var r struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", false
	}
	var parts []string
	for _, item := range r.Content {
		if item.Type == "text" && item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n\n"), r.IsError
}

// AppleFetchAvailable reports whether the native runtime is resolvable, so hosts
// (e.g. /doctor) can surface the tools' availability.
func AppleFetchAvailable() bool { return resolveAppleFetchExecutable() != "" }
