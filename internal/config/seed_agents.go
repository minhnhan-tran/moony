package config

import (
	"embed"
	"os"
	"path/filepath"
)

// defaultGlobalAGENTSFS embeds the seed content for the global AGENTS.md — the
// user-level memory file at <config>/moony/AGENTS.md that every
// project's agent reads alongside its own project AGENTS.md (see
// internal/agent/memory.go's memoryFile). Baking it into the binary means a
// fresh install/machine gets the house rules (e.g. the CHANGELOG.md/versioning
// convention) without any manual setup step.
//
//go:embed default_global_agents.md
var defaultGlobalAGENTSFS embed.FS

// SeedGlobalAGENTS writes the embedded default global AGENTS.md to
// <config>/moony/AGENTS.md, but ONLY if that file doesn't exist yet —
// it never overwrites a user's existing global memory. Mirrors
// subagents.SeedGlobalDefaultAgents's "seed once, never clobber" shape.
// Returns true if it wrote the file.
func SeedGlobalAGENTS() (bool, error) {
	path, err := GlobalConfigPath("AGENTS.md")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil // already present — don't touch a user's own rules
	} else if !os.IsNotExist(err) {
		return false, err
	}
	content, err := defaultGlobalAGENTSFS.ReadFile("default_global_agents.md")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, content, 0o644)
}
