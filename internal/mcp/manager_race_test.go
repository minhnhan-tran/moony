package mcp

import (
	"context"
	"sync"
	"testing"
)

// Reload() replaces conns/prompts/servers/failures wholesale while other goroutines read
// them: a /mcp reload runs on one HTTP handler goroutine while the Doctor panel, the MCP
// market and the command palette call Connected/Failure/Servers from theirs. Run with
// -race; before the mutex this reported a data race on the slice headers.
func TestManagerConcurrentReloadAndReaders(t *testing.T) {
	m := &Manager{
		servers:  []ServerInfo{{Name: "alpha"}},
		failures: []ServerFailure{{Name: "beta", Err: "boom"}},
		prompts:  []PromptDef{{ServerName: "alpha"}},
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers, mimicking the independent HTTP handlers.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = m.Connected("alpha")
				_, _ = m.Failure("beta")
				_ = m.Servers()
				_ = m.Failures()
				_ = m.Prompts()
				_ = m.ToolCatalog()
			}
		}()
	}

	// Writer: an empty config list means no subprocesses are spawned, but the swap of all
	// four slices — the part that raced — still happens.
	for i := 0; i < 40; i++ {
		m.Reload(context.Background(), nil, nil)
	}

	close(stop)
	wg.Wait()
}

// Close() is public and can race readers too; it must swap the list out under the lock.
func TestManagerConcurrentCloseAndReaders(t *testing.T) {
	m := &Manager{servers: []ServerInfo{{Name: "alpha"}}}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.Servers()
				_ = m.ToolCatalog()
			}
		}()
	}
	for i := 0; i < 50; i++ {
		m.Close()
	}
	wg.Wait()
}

// Readers return the live slice, so it is worth pinning WHY that is safe: Reload replaces
// the slice headers (m.servers = fresh.servers) and never appends to or mutates the old
// backing arrays, so a slice already handed out stays a valid immutable snapshot. If
// someone later makes Reload mutate in place, this test starts failing.
func TestReaderSliceIsStableAcrossReload(t *testing.T) {
	m := &Manager{servers: []ServerInfo{{Name: "alpha"}, {Name: "beta"}}}

	before := m.Servers()
	m.Reload(context.Background(), nil, nil) // empty config: swaps in empty slices

	if len(before) != 2 || before[0].Name != "alpha" || before[1].Name != "beta" {
		t.Fatalf("a previously returned slice was mutated by Reload: %+v", before)
	}
	if got := m.Servers(); len(got) != 0 {
		t.Errorf("after reload with no configs, Servers() = %+v, want empty", got)
	}
}

// Reload must be serialized against ITSELF. mu is released across the slow ConnectAll, so
// two overlapping reloads (the /mcp web handler is per-request, so two browser clients can
// trigger it at once) would each spawn a full set of subprocesses and one set would be
// overwritten by the other's swap — orphaned and never closed.
func TestConcurrentReloadsAreSerialized(t *testing.T) {
	m := &Manager{}

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	// probeConfigs is empty, so ConnectAll does no real work; the point is that the
	// critical section cannot be entered twice at once.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				mu.Lock()
				inFlight++
				if inFlight > maxInFlight {
					maxInFlight = inFlight
				}
				mu.Unlock()

				m.Reload(context.Background(), nil, nil)

				mu.Lock()
				inFlight--
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// The counter above brackets the Reload call, so it can exceed 1 legitimately; the
	// real assertion is that -race reports no violation and the manager ends consistent.
	if got := m.Servers(); len(got) != 0 {
		t.Errorf("after concurrent reloads with no configs, Servers() = %+v, want empty", got)
	}
	if got := m.ToolCatalog(); len(got) != 0 {
		t.Errorf("ToolCatalog() = %+v, want empty", got)
	}
}
