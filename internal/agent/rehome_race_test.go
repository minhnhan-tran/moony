package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// A background prompt rebuild (what the symbol/repo-map builders trigger after the
// turn that started them has moved on) must not race a concurrent READ of the
// prompt. This is the realistic pairing: SetWorkingDir is refused while a turn
// runs (see TestSetWorkingDirRefusedMidTurn), but rebuilds are not.
func TestConcurrentPromptRebuildAndRead(t *testing.T) {
	mk := func() string {
		d := t.TempDir()
		_ = os.WriteFile(filepath.Join(d, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
		return d
	}
	d1, d2 := mk(), mk()
	cfg := &config.Config{Model: "test", WorkingDir: d1}
	a := New(cfg, nil, tools.NewRegistry(d1), nil, nil)

	_ = d2
	var wg sync.WaitGroup
	wg.Add(3)
	// Two background rebuilders (MCP connect + a symbol-map refresh can overlap).
	for range 2 {
		go func() {
			defer wg.Done()
			for range 40 {
				a.rebuildSystemPrompt()
			}
		}()
	}
	// A turn reading the prompt to build its request, plus the context breakdown.
	go func() {
		defer wg.Done()
		for range 40 {
			_ = a.systemPrompt()
			_ = a.ContextBreakdown()
		}
	}()
	wg.Wait()
}

// Re-rooting mid-turn would swap the path sandbox, ignore set, skills and
// permissions out from under in-flight tool calls, so it must be refused.
func TestSetWorkingDirRefusedMidTurn(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	cfg := &config.Config{Model: "test", WorkingDir: d1}
	a := New(cfg, nil, tools.NewRegistry(d1), nil, nil)

	a.inTurn.Add(1) // simulate a running turn
	if err := a.SetWorkingDir(d2); err == nil {
		t.Fatal("SetWorkingDir succeeded during a turn; want refusal")
	}
	if cfg.WorkingDir != d1 {
		t.Errorf("working dir changed to %q despite the refusal", cfg.WorkingDir)
	}
	if got := a.tools.Root(); got != d1 {
		t.Errorf("registry root changed to %q despite the refusal", got)
	}

	a.inTurn.Add(-1) // turn finished
	if err := a.SetWorkingDir(d2); err != nil {
		t.Fatalf("SetWorkingDir when idle: %v", err)
	}
	if cfg.WorkingDir != d2 || a.tools.Root() != d2 {
		t.Errorf("re-root did not take effect: cfg=%q root=%q", cfg.WorkingDir, a.tools.Root())
	}
}
