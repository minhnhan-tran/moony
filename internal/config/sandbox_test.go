package config

import "testing"

func TestNormalizeSandboxMode(t *testing.T) {
	on := []string{"on", "1", "true", "yes", "workdir", "WORK", "confined"}
	for _, s := range on {
		if got := NormalizeSandboxMode(s); got != SandboxWorkdir {
			t.Errorf("NormalizeSandboxMode(%q) = %q, want %q", s, got, SandboxWorkdir)
		}
	}
	off := []string{"", "off", "0", "false", "no", "nonsense"}
	for _, s := range off {
		if got := NormalizeSandboxMode(s); got != SandboxOff {
			t.Errorf("NormalizeSandboxMode(%q) = %q, want %q", s, got, SandboxOff)
		}
	}
}

func TestSandboxLabel(t *testing.T) {
	if SandboxLabel(SandboxOff) != "" {
		t.Error("off should have no label")
	}
	if SandboxLabel("on") != "sandbox" {
		t.Errorf("on label = %q, want sandbox", SandboxLabel("on"))
	}
}

func TestSandboxDefaultsOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SQE_SANDBOX", "")
	t.Setenv("SQE_SANDBOX_NET", "")
	c := Load()
	if c.SandboxMode != SandboxOff {
		t.Errorf("default SandboxMode = %q, want off", c.SandboxMode)
	}
	if !c.SandboxNetwork {
		t.Error("default SandboxNetwork should be true")
	}
}

func TestSandboxEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SQE_SANDBOX", "on")
	t.Setenv("SQE_SANDBOX_NET", "0")
	c := Load()
	if c.SandboxMode != SandboxWorkdir {
		t.Errorf("SQE_SANDBOX=on -> %q, want workdir", c.SandboxMode)
	}
	if c.SandboxNetwork {
		t.Error("SQE_SANDBOX_NET=0 should disable network")
	}
}

func TestMaxStepsEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SQE_MAX_STEPS", "")
	if c := Load(); c.MaxAgentSteps != 80 {
		t.Errorf("default MaxAgentSteps = %d, want 80", c.MaxAgentSteps)
	}
	t.Setenv("SQE_MAX_STEPS", "300")
	if c := Load(); c.MaxAgentSteps != 300 {
		t.Errorf("SQE_MAX_STEPS=300 -> %d, want 300", c.MaxAgentSteps)
	}
	// Invalid values are ignored (keep the default).
	t.Setenv("SQE_MAX_STEPS", "-5")
	if c := Load(); c.MaxAgentSteps != 80 {
		t.Errorf("negative SQE_MAX_STEPS should be ignored, got %d", c.MaxAgentSteps)
	}
}

func TestContextCostDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"SQE_CHEAP", "SQE_COMPACT_THRESHOLD", "SQE_COMPACT_KEEP", "SQE_MAX_TOOL_OUTPUT", "SQE_TOOL_RESULT_KEEP", "SQE_READ_LINES", "SQE_AUTO_OFFLOAD_READ_BYTES"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.CompactThresholdTokens != 80000 {
		t.Errorf("default CompactThresholdTokens = %d, want 80000", c.CompactThresholdTokens)
	}
	if c.CompactKeepTokens != 32000 {
		t.Errorf("default CompactKeepTokens = %d, want 32000", c.CompactKeepTokens)
	}
	if c.MaxToolOutputBytes != 16000 {
		t.Errorf("default MaxToolOutputBytes = %d, want 16000", c.MaxToolOutputBytes)
	}
	if c.ToolResultKeepRounds != 8 {
		t.Errorf("default ToolResultKeepRounds = %d, want 8", c.ToolResultKeepRounds)
	}
	if c.MaxReadLines != 800 {
		t.Errorf("default MaxReadLines = %d, want 800", c.MaxReadLines)
	}
	if c.AutoOffloadReadBytes != 32768 {
		t.Errorf("default AutoOffloadReadBytes = %d, want 32768", c.AutoOffloadReadBytes)
	}
}

func TestContextCostEnvOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SQE_COMPACT_THRESHOLD", "80k")
	t.Setenv("SQE_COMPACT_KEEP", "30000")
	t.Setenv("SQE_MAX_TOOL_OUTPUT", "12k")
	t.Setenv("SQE_TOOL_RESULT_KEEP", "2")
	t.Setenv("SQE_READ_LINES", "300")
	t.Setenv("SQE_AUTO_OFFLOAD_READ_BYTES", "48k")
	c := Load()
	if c.CompactThresholdTokens != 80000 {
		t.Errorf("SQE_COMPACT_THRESHOLD=80k -> %d, want 80000", c.CompactThresholdTokens)
	}
	if c.CompactKeepTokens != 30000 {
		t.Errorf("SQE_COMPACT_KEEP=30000 -> %d, want 30000", c.CompactKeepTokens)
	}
	if c.MaxToolOutputBytes != 12000 {
		t.Errorf("SQE_MAX_TOOL_OUTPUT=12k -> %d, want 12000", c.MaxToolOutputBytes)
	}
	if c.ToolResultKeepRounds != 2 {
		t.Errorf("SQE_TOOL_RESULT_KEEP=2 -> %d, want 2", c.ToolResultKeepRounds)
	}
	if c.MaxReadLines != 300 {
		t.Errorf("SQE_READ_LINES=300 -> %d, want 300", c.MaxReadLines)
	}
	if c.AutoOffloadReadBytes != 48000 {
		t.Errorf("SQE_AUTO_OFFLOAD_READ_BYTES=48k -> %d, want 48000", c.AutoOffloadReadBytes)
	}

	// 0 disables tool-result pruning; invalid tool-output is ignored.
	t.Setenv("SQE_TOOL_RESULT_KEEP", "0")
	t.Setenv("SQE_MAX_TOOL_OUTPUT", "nonsense")
	c = Load()
	if c.ToolResultKeepRounds != 0 {
		t.Errorf("SQE_TOOL_RESULT_KEEP=0 -> %d, want 0", c.ToolResultKeepRounds)
	}
	if c.MaxToolOutputBytes != 16000 {
		t.Errorf("invalid SQE_MAX_TOOL_OUTPUT should keep default, got %d", c.MaxToolOutputBytes)
	}
}
