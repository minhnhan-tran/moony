package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAppleToolsMetadata(t *testing.T) {
	af := &appleFetchTool{}
	as := &appleSearchTool{}
	if af.Name() != "apple_fetch" || as.Name() != "apple_search" {
		t.Fatalf("names: %q %q", af.Name(), as.Name())
	}
	if af.Mutates() || as.Mutates() {
		t.Error("apple tools must be read-only (non-mutating)")
	}
	// Schemas must be valid JSON with the required fields.
	for _, tc := range []struct {
		raw      json.RawMessage
		required string
	}{
		{af.Schema(), "url"},
		{as.Schema(), "query"},
	} {
		var s struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(tc.raw, &s); err != nil {
			t.Fatalf("schema not valid JSON: %v", err)
		}
		if len(s.Required) != 1 || s.Required[0] != tc.required {
			t.Errorf("required = %v, want [%s]", s.Required, tc.required)
		}
	}
}

func TestExecuteValidatesInput(t *testing.T) {
	if _, err := (&appleFetchTool{}).Execute(context.Background(), json.RawMessage(`{"url":""}`)); err == nil {
		t.Error("empty url should error")
	}
	if _, err := (&appleSearchTool{}).Execute(context.Background(), json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Error("blank query should error")
	}
}

func TestExecutableFromCandidate(t *testing.T) {
	dir := t.TempDir()
	// Lay out a fake .app bundle: <dir>/AppleFetchMCP.app/Contents/MacOS/AppleFetchMCP
	exe := filepath.Join(dir, "AppleFetchMCP.app", appleFetchExecutable)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A parent dir containing the .app resolves to the executable.
	if got := executableFromCandidate(dir); got != exe {
		t.Errorf("from parent dir = %q, want %q", got, exe)
	}
	// The executable path itself resolves to itself.
	if got := executableFromCandidate(exe); got != exe {
		t.Errorf("from exe = %q, want %q", got, exe)
	}
	// A nonexistent path resolves to "".
	if got := executableFromCandidate(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("nonexistent = %q, want empty", got)
	}
	if got := executableFromCandidate(""); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
}

func TestTextFromToolResult(t *testing.T) {
	ok := json.RawMessage(`{"content":[{"type":"text","text":"hello"},{"type":"text","text":"world"},{"type":"image"}]}`)
	got, isErr := textFromToolResult(ok)
	if isErr || got != "hello\n\nworld" {
		t.Errorf("got %q isErr=%v", got, isErr)
	}
	bad := json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"boom"}]}`)
	got, isErr = textFromToolResult(bad)
	if !isErr || got != "boom" {
		t.Errorf("error result: got %q isErr=%v", got, isErr)
	}
}

func TestAppBundleFor(t *testing.T) {
	exe := filepath.Join("x", "AppleFetchMCP.app", "Contents", "MacOS", "AppleFetchMCP")
	if got := appBundleFor(exe); got != filepath.Join("x", "AppleFetchMCP.app") {
		t.Errorf("appBundleFor = %q", got)
	}
	if got := appBundleFor("/usr/bin/AppleFetchMCP"); got != "" {
		t.Errorf("non-bundle exe should give empty, got %q", got)
	}
}

func TestEnsureAppleFetchSignedNoBundle(t *testing.T) {
	// A bare executable (not in a .app) must be a safe no-op, not a panic/crash.
	dir := t.TempDir()
	exe := filepath.Join(dir, "AppleFetchMCP")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ensureAppleFetchSigned(exe) // should simply return
}

// TestRPCClientRoundTrip drives the JSON-RPC stdio framing against a fake
// "AppleFetchMCP" (pointed at via SQE_APPLE_FETCH_PATH) implemented as a shell
// script that echoes canned responses, exercising initialize -> initialized ->
// tools/call without the real native runtime.
func TestRPCClientRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake server is POSIX-only")
	}
	dir := t.TempDir()
	// Build a fake .app whose executable is a shell script reading line-delimited
	// JSON-RPC on stdin and replying with matching ids. It answers initialize
	// (id 1), ignores the initialized notification, then answers tools/call (id 2)
	// with a text content block.
	exe := filepath.Join(dir, "AppleFetchMCP.app", appleFetchExecutable)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}' ;;
    *'"method":"tools/call"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"fetched body"}]}}' ;;
  esac
done
`
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQE_APPLE_FETCH_PATH", exe)

	if runtime.GOOS != "darwin" {
		// resolveAppleFetchExecutable refuses non-macOS, so callAppleFetch will
		// return the macOS-required error rather than running the fake. Verify
		// that path instead of the round trip.
		_, err := callAppleFetch(context.Background(), "AppleFetch", map[string]any{"url": "x.apple.com"}, time.Second)
		if err == nil {
			t.Fatal("expected macOS-required error on non-darwin")
		}
		return
	}

	out, err := callAppleFetch(context.Background(), "AppleFetch", map[string]any{"url": "x.apple.com"}, 5*time.Second)
	if err != nil {
		t.Fatalf("callAppleFetch: %v", err)
	}
	if out != "fetched body" {
		t.Errorf("output = %q, want %q", out, "fetched body")
	}
}
