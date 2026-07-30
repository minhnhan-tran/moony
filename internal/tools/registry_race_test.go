package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// stubTool is a minimal Tool for registry concurrency tests.
type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "stub " + s.name }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Mutates() bool           { return false }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// TestRegistryConcurrentReloadAndRead is the regression test for a process-killing
// crash. The registry's tools map and order slice were unsynchronized, yet an MCP
// reload (mcp.Manager.Reload -> Unregister + Register, reachable at runtime from the
// web config save) mutates them while a turn goroutine calls Schemas/Get/Has/Names.
// Concurrent map read + map write is a FATAL Go runtime error — not a recoverable
// panic — so a config save during an active turn could take the whole agent down.
//
// Without the RWMutex this fails as "fatal error: concurrent map read and map
// write" (or is flagged by -race).
func TestRegistryConcurrentReloadAndRead(t *testing.T) {
	r := NewRegistry(t.TempDir())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: simulates repeated MCP reloads swapping a server's tool set.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			name := fmt.Sprintf("mcp__srv__tool%d", i%8)
			r.Register(stubTool{name: name})
			r.Unregister(name)
		}
	}()

	// Readers: simulate the turn loop advertising and dispatching tools.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = r.Schemas()
				_ = r.Names()
				_ = r.Has("read_file")
				_ = r.Get("read_file")
				_ = r.Mutates("mcp__srv__tool1")
			}
		}()
	}

	// Enough iterations to hit the window reliably.
	for i := 0; i < 2000; i++ {
		_ = r.Names()
	}
	close(stop)
	wg.Wait()
}

// TestRegistryRegisterUnregisterConsistency checks the order slice and map stay in
// step, since Schemas() indexes the map by the order slice — a desync there would
// nil-deref on t.Description().
func TestRegistryRegisterUnregisterConsistency(t *testing.T) {
	r := NewRegistry(t.TempDir())
	before := len(r.Names())

	r.Register(stubTool{name: "zz_probe"})
	if !r.Has("zz_probe") {
		t.Fatal("Register did not add the tool")
	}
	// Re-registering the same name must replace, not duplicate, the order entry.
	r.Register(stubTool{name: "zz_probe"})
	var seen int
	for _, n := range r.Names() {
		if n == "zz_probe" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("order has %d entries for zz_probe, want 1", seen)
	}
	// Schemas must not panic and must include the tool exactly once.
	var inSchemas int
	for _, sc := range r.Schemas() {
		if sc.Name == "zz_probe" {
			inSchemas++
		}
	}
	if inSchemas != 1 {
		t.Fatalf("Schemas has %d entries for zz_probe, want 1", inSchemas)
	}

	r.Unregister("zz_probe")
	if r.Has("zz_probe") {
		t.Fatal("Unregister left the tool registered")
	}
	if got := len(r.Names()); got != before {
		t.Fatalf("Names length = %d after add+remove, want the original %d", got, before)
	}
	// Schemas must still be buildable (order/map in step).
	_ = r.Schemas()
}
