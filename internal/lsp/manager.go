package lsp

// Manager owns a set of language-specific LSP servers and dispatches change
// notifications to the right one based on file extension. It is wired into the
// agent so that after every write_file/edit_file, diagnostics for the changed
// file are appended to the tool's output (so the model sees fresh errors
// without an extra round-trip).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// rawConfig is the on-disk format.
type rawConfig struct {
	Servers map[string]rawServer `json:"servers"`
}

// rawServer mirrors the JSON config for one LSP server.
type rawServer struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	Extensions []string          `json:"extensions"`
	Disabled   bool              `json:"disabled"`
}

// Config is the runtime version of one server entry.
type Config struct {
	Name       string
	Command    string
	Args       []string
	Env        []string
	Extensions []string
}

// LoadConfigs reads LSP server definitions from the global and project config
// files (project entries override global ones with the same name).
func LoadConfigs(workingDir string) ([]Config, error) {
	merged := map[string]rawServer{}
	if p, err := config.GlobalConfigPath("lsp.json"); err == nil {
		got, err := readConfig(p)
		if err != nil {
			return nil, fmt.Errorf("global lsp.json: %w", err)
		}
		for k, v := range got {
			merged[k] = v
		}
	}
	got, err := readConfig(config.ProjectPath(workingDir, "lsp.json"))
	if err != nil {
		return nil, fmt.Errorf("project lsp.json: %w", err)
	}
	for k, v := range got {
		merged[k] = v
	}

	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Config, 0, len(names))
	for _, n := range names {
		raw := merged[n]
		if raw.Disabled || raw.Command == "" || len(raw.Extensions) == 0 {
			continue
		}
		var env []string
		for k, v := range raw.Env {
			env = append(env, k+"="+v)
		}
		out = append(out, Config{
			Name:       n,
			Command:    raw.Command,
			Args:       raw.Args,
			Env:        env,
			Extensions: raw.Extensions,
		})
	}
	return out, nil
}

func readConfig(path string) (map[string]rawServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cfg rawConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	return cfg.Servers, nil
}

// Manager runs every configured language server and dispatches edits to the
// matching one(s).
type Manager struct {
	servers []*Server
	root    string
	logger  func(string)
}

// Start launches every configured server. Servers that fail to start are
// logged and skipped — they should never block the agent.
func Start(ctx context.Context, root string, cfgs []Config, logger func(string)) *Manager {
	if logger == nil {
		logger = func(string) {}
	}
	m := &Manager{root: root, logger: logger}
	for _, cfg := range cfgs {
		s, err := startOne(ctx, root, cfg, logger)
		if err != nil {
			logger(fmt.Sprintf("lsp: %s start failed: %v", cfg.Name, err))
			continue
		}
		m.servers = append(m.servers, s)
		logger(fmt.Sprintf("lsp: %s started (extensions: %s)", cfg.Name, strings.Join(cfg.Extensions, ",")))
	}
	return m
}

func startOne(ctx context.Context, root string, cfg Config, logger func(string)) (*Server, error) {
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return startServer(startCtx, cfg.Name, cfg.Command, cfg.Args, cfg.Env, cfg.Extensions, root, logger)
}

// Empty reports whether no servers are running.
func (m *Manager) Empty() bool { return m == nil || len(m.servers) == 0 }

// Handles reports whether any running server handles path's extension. Used by
// the lsp_diagnostics tool to distinguish "no server for this language" from
// "server ran and found nothing".
func (m *Manager) Handles(path string) bool {
	if m.Empty() {
		return false
	}
	ext := filepath.Ext(path)
	for _, srv := range m.servers {
		if srv.HandlesExtension(ext) {
			return true
		}
	}
	return false
}

// Running returns the names of the currently-running servers (those that
// started successfully), sorted. Used by /lsp to show live state.
func (m *Manager) Running() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.servers))
	for _, s := range m.servers {
		names = append(names, s.name)
	}
	sort.Strings(names)
	return names
}

// Close shuts down every running server.
func (m *Manager) Close() {
	for _, s := range m.servers {
		_ = s.Close()
	}
	m.servers = nil
}

// DiagnosticsFor sends a didChange to every server that handles path's
// extension, waits briefly for diagnostics, and returns a formatted report
// suitable for appending to a tool's output. Returns "" when no server handles
// this language or no diagnostics arrive within the timeout.
func (m *Manager) DiagnosticsFor(ctx context.Context, path, content string) string {
	if m.Empty() {
		return ""
	}
	ext := filepath.Ext(path)
	var report strings.Builder
	for _, srv := range m.servers {
		if !srv.HandlesExtension(ext) {
			continue
		}
		_ = srv.NotifyChange(path, content)
		ds := srv.Diagnostics(ctx, path, 1500*time.Millisecond)
		if len(ds) == 0 {
			continue
		}
		fmt.Fprintf(&report, "\n\n[%s diagnostics]\n", srv.name)
		for _, d := range ds {
			report.WriteString(formatDiagnostic(path, d))
			report.WriteByte('\n')
		}
	}
	return report.String()
}

func formatDiagnostic(path string, d Diagnostic) string {
	sev := "info"
	switch d.Severity {
	case 1:
		sev = "error"
	case 2:
		sev = "warn"
	case 4:
		sev = "hint"
	}
	src := d.Source
	if src == "" {
		src = "lsp"
	}
	return fmt.Sprintf("  %s:%d:%d: %s: [%s] %s",
		filepath.Base(path), d.Range.Start.Line+1, d.Range.Start.Character+1,
		sev, src, strings.TrimSpace(d.Message))
}
