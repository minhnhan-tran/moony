package lsp

// Editing the sqe LSP config files (lsp.json) — the project's ./.sqe/lsp.json or
// the global ~/.config/moony/lsp.json. Mirrors internal/mcp/edit.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pysty1995/moony/internal/config"
)

// AddServer writes (or overwrites) a language-server definition in the sqe LSP
// config — the project's ./.sqe/lsp.json, or the global file when global is
// true. Returns the file written.
func AddServer(name, command string, args, extensions []string, global bool, workingDir string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("server name required")
	}
	if command == "" || len(extensions) == 0 {
		return "", fmt.Errorf("a command and at least one extension are required")
	}
	path, err := sqeConfigPath(global, workingDir)
	if err != nil {
		return "", err
	}
	cfg := readRawCfg(path)
	if cfg.Servers == nil {
		cfg.Servers = map[string]rawServer{}
	}
	cfg.Servers[name] = rawServer{Command: command, Args: args, Extensions: extensions}
	if err := writeRawCfg(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveServer deletes a server from the sqe config files (project, then
// global). Returns the file it was removed from.
func RemoveServer(name, workingDir string) (string, error) {
	paths := []string{filepath.Join(workingDir, ".sqe", "lsp.json")}
	if p, err := config.GlobalConfigPath("lsp.json"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		cfg := readRawCfg(p)
		if _, ok := cfg.Servers[name]; ok {
			delete(cfg.Servers, name)
			if err := writeRawCfg(p, cfg); err != nil {
				return "", err
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("language server %q isn't configured", name)
}

func sqeConfigPath(global bool, workingDir string) (string, error) {
	if global {
		return config.GlobalConfigPath("lsp.json")
	}
	return filepath.Join(workingDir, ".sqe", "lsp.json"), nil
}

func readRawCfg(path string) rawConfig {
	var cfg rawConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func writeRawCfg(path string, cfg rawConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
