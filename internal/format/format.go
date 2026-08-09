// Package format runs configured code formatters on files after the agent
// writes or edits them — the same lifecycle point where LSP diagnostics are
// gathered — so changes land already formatted (gofmt, prettier, ruff, …).
//
// Config lives in format.json (global + per-project, project wins):
//
//	{
//	  "formatters": {
//	    "go":     { "command": "gofmt",   "args": ["-w"], "extensions": [".go"] },
//	    "web":    { "command": "prettier","args": ["--write"], "extensions": [".ts",".tsx",".js",".css",".json"] },
//	    "python": { "command": "ruff",    "args": ["format"], "extensions": [".py"] }
//	  }
//	}
//
// The file path is appended as the final argument, so each formatter is invoked
// as `<command> <args...> <file>` and is expected to edit the file in place.
package format

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

type rawConfig struct {
	Formatters map[string]rawFormatter `json:"formatters"`
}

type rawFormatter struct {
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Extensions []string `json:"extensions"`
	Disabled   bool     `json:"disabled"`
}

// Config is one runtime formatter entry.
type Config struct {
	Name       string
	Command    string
	Args       []string
	Extensions []string
}

// LoadConfigs reads formatter definitions from the global and project config
// files (project entries override global ones with the same name).
func LoadConfigs(workingDir string) ([]Config, error) {
	merged := map[string]rawFormatter{}
	if p, err := config.GlobalConfigPath("format.json"); err == nil {
		got, err := readConfig(p)
		if err != nil {
			return nil, fmt.Errorf("global format.json: %w", err)
		}
		for k, v := range got {
			merged[k] = v
		}
	}
	got, err := readConfig(config.ProjectPath(workingDir, "format.json"))
	if err != nil {
		return nil, fmt.Errorf("project format.json: %w", err)
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
		out = append(out, Config{Name: n, Command: raw.Command, Args: raw.Args, Extensions: raw.Extensions})
	}
	return out, nil
}

func readConfig(path string) (map[string]rawFormatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg rawConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg.Formatters, nil
}

// Manager runs formatters by file extension.
type Manager struct {
	root  string
	byExt map[string]Config // lowercased extension -> formatter (first match wins)
}

// New builds a Manager from configs, rooted at workingDir (used as the
// formatter's working directory so tools find their project config).
func New(workingDir string, cfgs []Config) *Manager {
	m := &Manager{root: workingDir, byExt: map[string]Config{}}
	for _, c := range cfgs {
		for _, ext := range c.Extensions {
			e := strings.ToLower(ext)
			if _, ok := m.byExt[e]; !ok {
				m.byExt[e] = c
			}
		}
	}
	return m
}

// Empty reports whether no formatters are configured.
func (m *Manager) Empty() bool { return m == nil || len(m.byExt) == 0 }

// FormatFile formats absPath in place if a formatter matches its extension and
// the file exists. It returns an empty string on success or no-op, and a short
// bracketed note if the formatter failed (e.g. a syntax error) so the model
// sees it. Failures never block the agent.
func (m *Manager) FormatFile(ctx context.Context, absPath string) string {
	if m.Empty() {
		return ""
	}
	c, ok := m.byExt[strings.ToLower(filepath.Ext(absPath))]
	if !ok {
		return ""
	}
	if info, err := os.Stat(absPath); err != nil || info.IsDir() {
		return "" // deleted, missing, or a directory
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append(append([]string{}, c.Args...), absPath)
	cmd := exec.CommandContext(ctx, c.Command, args...)
	cmd.Dir = m.root
	if out, err := cmd.CombinedOutput(); err != nil {
		note := strings.TrimSpace(string(out))
		if note == "" {
			note = err.Error()
		}
		return fmt.Sprintf("\n[formatter %s failed: %s]", c.Name, note)
	}
	return ""
}
