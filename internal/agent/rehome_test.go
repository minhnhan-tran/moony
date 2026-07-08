package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// SetWorkingDir re-roots a live agent: the config + the tools registry both point
// at the new dir afterward (so file ops, bash, and the path sandbox follow), and
// the registry's tools survive the switch.
func TestSetWorkingDirReRoots(t *testing.T) {
	isolateConfig(t)
	oldDir := t.TempDir()
	newDir := t.TempDir()
	cfg := &config.Config{Model: "anthropic.claude-sonnet-4-6", WorkingDir: oldDir}
	reg := tools.Default(oldDir, 30000)
	ag := New(cfg, nil, reg, nil, nil)

	ag.SetWorkingDir(newDir)

	if cfg.WorkingDir != newDir {
		t.Errorf("cfg.WorkingDir = %q, want %q", cfg.WorkingDir, newDir)
	}
	if got := ag.Registry().Root(); got != newDir {
		t.Errorf("registry root = %q, want %q", got, newDir)
	}
	// A relative tool path now resolves under the new dir, and the toolset is intact.
	if !ag.Registry().Has("read_file") {
		t.Error("read_file should survive the re-root")
	}
}
