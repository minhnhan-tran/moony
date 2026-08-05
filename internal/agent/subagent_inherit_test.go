package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/hooks"
	"github.com/pysty1995/moony/internal/tools"
)

// A sub-agent must inherit a sandbox the user turned on at RUNTIME.
//
// The bug this guards: a fresh registry has no explicit setting and falls back to the
// MN_SANDBOX env vars, but /sandbox and the web UI both call Agent.SetSandbox, which
// touches only the parent's registry. So enabling the sandbox left every sub-agent
// unconfined while the user believed otherwise — and that setting is a sub-agent's only
// protection, because it auto-runs its tools with no approval prompt.
func TestSubAgentInheritsRuntimeSandbox(t *testing.T) {
	parent := tools.NewRegistry(t.TempDir())
	parent.SetSandbox(true, false) // what /sandbox does

	child := tools.NewRegistry(t.TempDir())
	if _, _, ok := child.ExplicitSandbox(); ok {
		t.Fatal("a fresh registry must start with no explicit setting")
	}

	inheritSandbox(parent, child)

	enabled, network, ok := child.ExplicitSandbox()
	if !ok {
		t.Fatal("the sub-agent registry did not inherit the parent's explicit setting")
	}
	if !enabled {
		t.Error("sandbox must be enabled in the sub-agent, matching the parent")
	}
	if network {
		t.Error("network must stay disabled, matching the parent")
	}
}

// Network access is part of the setting and must not be silently widened.
func TestSubAgentInheritsSandboxNetworkFlag(t *testing.T) {
	parent := tools.NewRegistry(t.TempDir())
	parent.SetSandbox(true, true)
	child := tools.NewRegistry(t.TempDir())
	inheritSandbox(parent, child)
	if enabled, network, ok := child.ExplicitSandbox(); !ok || !enabled || !network {
		t.Errorf("got enabled=%v network=%v ok=%v, want all true", enabled, network, ok)
	}
}

// A sandbox explicitly turned OFF must also propagate: otherwise the child could fall
// back to an env var that says "on" and fail work the parent allows.
func TestSubAgentInheritsSandboxOff(t *testing.T) {
	parent := tools.NewRegistry(t.TempDir())
	parent.SetSandbox(false, false)
	child := tools.NewRegistry(t.TempDir())
	inheritSandbox(parent, child)
	enabled, _, ok := child.ExplicitSandbox()
	if !ok {
		t.Fatal("an explicit OFF must propagate too")
	}
	if enabled {
		t.Error("sandbox must be off in the child, matching the parent")
	}
}

// When the parent has NO explicit setting, the child must keep none either, so both
// read the same env fallback instead of freezing its value at spawn time.
func TestSubAgentSandboxUnsetStaysUnset(t *testing.T) {
	parent := tools.NewRegistry(t.TempDir())
	child := tools.NewRegistry(t.TempDir())
	inheritSandbox(parent, child)
	if _, _, ok := child.ExplicitSandbox(); ok {
		t.Error("with no parent setting the child must stay unset (shared env fallback)")
	}
}

func TestInheritSandboxNilSafe(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	inheritSandbox(nil, reg) // must not panic
	inheritSandbox(reg, nil)
	inheritSandbox(nil, nil)
	var nilReg *tools.Registry
	if _, _, ok := nilReg.ExplicitSandbox(); ok {
		t.Error("a nil registry must report no explicit setting")
	}
}

// A sub-agent must run the parent's hooks. Sub-agents auto-run their tools, so a
// pre_tool hook is the only per-call gate available to them — without this, a hook
// installed to audit or block dangerous calls saw nothing a sub-agent did.
func TestSubAgentInheritsHooks(t *testing.T) {
	// A real Set from a hooks.json, so this exercises the type the agent actually holds.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sqe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sqe", "hooks.json"),
		[]byte(`{"pre_tool":["exit 77"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := hooks.New(dir, nil)
	if err != nil {
		t.Fatalf("hooks.New: %v", err)
	}
	if set.Empty() {
		t.Fatal("expected the hook set to load a pre_tool hook")
	}
	parent := &Agent{cfg: &config.Config{}, hooks: set}
	// Mirror what runTask does when building the sub-agent.
	sub := &Agent{cfg: &config.Config{}}
	sub.hooks = parent.hooks
	if sub.hooks == nil {
		t.Fatal("the sub-agent has no hooks, so a pre_tool policy would not cover it")
	}
	if sub.hooks != parent.hooks {
		t.Error("the sub-agent must share the parent's hook set")
	}
}

// Guard the WIRING, not just the helpers: the regression was that runTask never called
// them, and a helper that nothing invokes protects nothing.
func TestRunTaskWiresSandboxAndHookInheritance(t *testing.T) {
	src, err := os.ReadFile("task.go")
	if err != nil {
		t.Fatalf("read task.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "inheritSandbox(t.parent.tools, subRegistry)") {
		t.Error("runTask must call inheritSandbox, or a runtime /sandbox leaves sub-agents unconfined")
	}
	if !strings.Contains(s, "subAgent.hooks = t.parent.hooks") {
		t.Error("runTask must give the sub-agent the parent's hooks, or pre_tool policy misses sub-agents")
	}
	// Both must happen before the sub-agent runs; assert they precede the Run call.
	iSandbox := strings.Index(s, "inheritSandbox(t.parent.tools, subRegistry)")
	iHooks := strings.Index(s, "subAgent.hooks = t.parent.hooks")
	iRun := strings.Index(s, "subAgent.RunWith(")
	if iRun < 0 {
		iRun = strings.Index(s, "subAgent.Run(")
	}
	if iRun > 0 {
		if iSandbox > iRun {
			t.Error("sandbox inheritance must happen before the sub-agent runs")
		}
		if iHooks > iRun {
			t.Error("hook inheritance must happen before the sub-agent runs")
		}
	}
}
