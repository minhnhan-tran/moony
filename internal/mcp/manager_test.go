package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/tools"
)

func hasTool(reg *tools.Registry, name string) bool {
	for _, n := range reg.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// TestRegisterToolsAndDetach verifies a connected server's tools land in the
// registry under mcp.<server>.<tool> and that detach (used by Reload) removes
// exactly those.
func TestRegisterToolsAndDetach(t *testing.T) {
	c := newFakeClient(t, func(req rpcRequest) (any, *rpcError) {
		if req.Method == "initialize" {
			return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})
	m := &Manager{conns: []connected{{
		client:    c,
		name:      "demo",
		tools:     []ToolDescriptor{{Name: "echo"}, {Name: "add"}},
		resources: make([]ResourceDescriptor, 1),
	}}}

	reg := tools.NewRegistry(t.TempDir())
	reg.Register(builtinStub{})
	m.RegisterTools(reg)

	want := []string{"mcp.demo.echo", "mcp.demo.add", "mcp.demo.read_resource"}
	for _, n := range want {
		if !hasTool(reg, n) {
			t.Errorf("expected MCP tool %q to be registered", n)
		}
	}

	m.detach()
	for _, n := range want {
		if hasTool(reg, n) {
			t.Errorf("MCP tool %q should be removed after detach", n)
		}
	}
	// detach must leave non-MCP tools alone.
	if !hasTool(reg, "builtin") {
		t.Error("detach removed an unrelated tool")
	}
}

// builtinStub is a minimal non-MCP tool used to confirm detach is surgical.
type builtinStub struct{}

func (builtinStub) Name() string                                             { return "builtin" }
func (builtinStub) Description() string                                      { return "stub" }
func (builtinStub) Schema() json.RawMessage                                  { return json.RawMessage(`{"type":"object"}`) }
func (builtinStub) Mutates() bool                                            { return false }
func (builtinStub) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

// TestConnectAllRecordsFailures verifies a server that can't start is recorded
// in Failures() (not silently dropped) and reported as not connected, so status
// displays can explain why an enabled server is unavailable.
func TestConnectAllRecordsFailures(t *testing.T) {
	// A config with neither command nor url fails immediately — no subprocess or
	// network needed — which is exactly the failure path we want to record.
	m := ConnectAll(context.Background(), []Config{{Name: "broken"}}, nil)

	if _, ok := m.Connected("broken"); ok {
		t.Error("broken server should not report as connected")
	}
	errMsg, ok := m.Failure("broken")
	if !ok || errMsg == "" {
		t.Fatalf("Failure(broken) = %q, %v; want a recorded error", errMsg, ok)
	}
	if len(m.Failures()) != 1 {
		t.Errorf("Failures() = %d, want 1", len(m.Failures()))
	}
	if len(m.Servers()) != 0 {
		t.Errorf("Servers() = %d, want 0", len(m.Servers()))
	}
}
