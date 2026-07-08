package config

import "testing"

func TestApplyCheapPreset(t *testing.T) {
	c := &Config{
		AdaptiveModel:          false,
		ConciseToolSchemas:     false,
		ToolResultKeepRounds:   8,
		CompactThresholdTokens: 100000,
	}
	applyCheapPreset(c)
	if !c.AdaptiveModel || !c.ConciseToolSchemas {
		t.Errorf("cheap preset should enable AdaptiveModel + ConciseToolSchemas, got %+v", c)
	}
	if c.ToolResultKeepRounds != 4 {
		t.Errorf("ToolResultKeepRounds = %d, want 4", c.ToolResultKeepRounds)
	}
	if c.CompactThresholdTokens != 50000 {
		t.Errorf("CompactThresholdTokens = %d, want 50000", c.CompactThresholdTokens)
	}
	if c.TaskResultMaxBytes != 6000 {
		t.Errorf("TaskResultMaxBytes = %d, want 6000", c.TaskResultMaxBytes)
	}
}

func TestCheapModeEnabled(t *testing.T) {
	for _, on := range []string{"1", "on", "true", "yes", "YES", " On "} {
		t.Setenv("SQE_CHEAP", on)
		if !cheapModeEnabled() {
			t.Errorf("SQE_CHEAP=%q should enable cheap mode", on)
		}
	}
	for _, off := range []string{"", "0", "off", "false", "no"} {
		t.Setenv("SQE_CHEAP", off)
		if cheapModeEnabled() {
			t.Errorf("SQE_CHEAP=%q should NOT enable cheap mode", off)
		}
	}
}

// SQE_CHEAP applies the preset, but an explicit individual knob parsed afterward
// still wins — you can opt into cheap mode and tune one dimension back.
func TestCheapPresetOverriddenByExplicitEnv(t *testing.T) {
	t.Setenv("SQE_CHEAP", "1")
	t.Setenv("SQE_ADAPTIVE_MODEL", "0") // explicit: keep the full model
	c := Load()
	if c.AdaptiveModel {
		t.Error("explicit SQE_ADAPTIVE_MODEL=0 should override the cheap preset")
	}
	if !c.ConciseToolSchemas {
		t.Error("SQE_CHEAP should still enable ConciseToolSchemas when not individually overridden")
	}
	if c.ToolResultKeepRounds != 4 {
		t.Errorf("SQE_CHEAP should still set ToolResultKeepRounds=4, got %d", c.ToolResultKeepRounds)
	}
}
