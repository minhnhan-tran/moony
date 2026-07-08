package worker

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// builtinBackends are the engines auto-detected as default workers when their
// CLI is installed, so `delegate` works out of the box with no workers.json.
var builtinBackends = []string{BackendOptimus, BackendClaude, BackendGemini}

// defaultEngineCommand maps a backend to the CLI it drives.
func defaultEngineCommand(backend string) string {
	switch backend {
	case BackendClaude:
		return "claude"
	case BackendOptimus:
		return "optimus"
	case BackendGemini:
		return "gemini"
	default:
		return ""
	}
}

// commandFor is the binary a worker config drives (its override, else the
// backend default).
func commandFor(c Config) string {
	if c.Command != "" {
		return c.Command
	}
	return defaultEngineCommand(c.Backend)
}

// InstallCommand returns the vetted shell one-liner that installs a backend's
// engine on this machine, or "" when the backend has no canned installer. These
// are fixed, trusted commands run as `bash -lc <cmd>`; callers must never
// substitute caller-supplied input.
func InstallCommand(backend string) string {
	switch backend {
	case BackendClaude:
		return "curl -fsSL https://claude.ai/install.sh | bash"
	case BackendGemini:
		return "npm install -g @google/gemini-cli"
	default:
		return ""
	}
}

// lookPath / probeVersion are indirected so tests can stub engine detection.
var (
	lookPath     = exec.LookPath
	probeVersion = func(command string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, command, "--version").Run() == nil
	}

	installedMu    sync.Mutex
	installedCache = map[string]bool{}
)

// Installed reports whether a command is on PATH (cached; cheap). Used to decide
// which built-in engines to auto-add.
func Installed(command string) bool {
	if command == "" {
		return false
	}
	installedMu.Lock()
	defer installedMu.Unlock()
	if v, ok := installedCache[command]; ok {
		return v
	}
	_, err := lookPath(command)
	ok := err == nil
	installedCache[command] = ok
	return ok
}

// forgetInstalled drops a command's cached PATH-lookup result so the next
// Installed call re-probes. Called after an install so a freshly-installed
// engine is detected without restarting.
func forgetInstalled(command string) {
	installedMu.Lock()
	defer installedMu.Unlock()
	delete(installedCache, command)
}

// builtinWorker is the default worker config for an auto-detected engine.
func builtinWorker(backend string) Config {
	desc := backend + " worker (auto-detected)"
	switch backend {
	case BackendClaude:
		desc = "Claude Code (auto-detected)"
	case BackendOptimus:
		desc = "Optimus (auto-detected)"
	case BackendGemini:
		desc = "Gemini (auto-detected)"
	}
	// srcPath stays empty -> this is an ephemeral auto worker until edited.
	return Config{Name: backend, Backend: backend, Desc: desc}
}

// Detect adds a default worker for each built-in engine whose CLI is installed
// and that isn't already configured by name. Idempotent and cheap (PATH lookup
// only); call it at startup and when listing workers to pick up newly-installed
// engines.
func (m *Manager) Detect() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range builtinBackends {
		if _, exists := m.byName[b]; exists {
			continue
		}
		if Installed(defaultEngineCommand(b)) {
			m.byName[b] = builtinWorker(b)
			m.order = append(m.order, b)
		}
	}
}

// ProbeReadiness runs `<command> --version` for each distinct worker engine and
// caches the result. It blocks, so callers typically run it in a goroutine; the
// cache is then read instantly by Readiness for display.
func (m *Manager) ProbeReadiness() {
	if m == nil {
		return
	}
	for _, cmd := range m.distinctCommands() {
		ok := probeVersion(cmd)
		m.readyMu.Lock()
		m.ready[cmd] = ok
		m.readyMu.Unlock()
	}
}

// RefreshReadiness forgets cached PATH lookups for every worker engine and
// re-probes them, so an engine installed during this session flips from
// "missing" to "ready" without a restart. Blocks like ProbeReadiness.
func (m *Manager) RefreshReadiness() {
	if m == nil {
		return
	}
	for _, cmd := range m.distinctCommands() {
		forgetInstalled(cmd)
	}
	m.ProbeReadiness()
}

// InstallPlanFor resolves how to install a named worker's engine on this
// machine: the engine command it drives (e.g. "claude") and the vetted shell
// one-liner that installs it. ok is false when there's no such worker or its
// backend has no canned installer.
func (m *Manager) InstallPlanFor(name string) (engine, command string, ok bool) {
	if m == nil {
		return "", "", false
	}
	m.mu.Lock()
	c, exists := m.byName[name]
	m.mu.Unlock()
	if !exists {
		return "", "", false
	}
	install := InstallCommand(c.Backend)
	if install == "" {
		return "", "", false
	}
	return commandFor(c), install, true
}

// Readiness returns each worker's engine state for display: "ready", "missing"
// (not on PATH), "not ready" (on PATH but `--version` failed), or "checking…"
// (probe not finished yet).
func (m *Manager) Readiness() map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	for _, c := range m.All() {
		cmd := commandFor(c)
		if !Installed(cmd) {
			out[c.Name] = "missing"
			continue
		}
		m.readyMu.Lock()
		ok, probed := m.ready[cmd]
		m.readyMu.Unlock()
		switch {
		case !probed:
			out[c.Name] = "checking…"
		case ok:
			out[c.Name] = "ready"
		default:
			out[c.Name] = "not ready"
		}
	}
	return out
}

// distinctCommands returns the unique engine commands across all workers.
func (m *Manager) distinctCommands() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range m.All() {
		if cmd := commandFor(c); cmd != "" && !seen[cmd] {
			seen[cmd] = true
			out = append(out, cmd)
		}
	}
	return out
}
