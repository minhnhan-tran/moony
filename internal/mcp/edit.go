package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// AddServer writes (or overwrites) a server definition in the sqe MCP config —
// the project's ./.sqe/mcp.json, or the global ~/.config/moony/mcp.json
// when global is true. A non-empty url makes it a remote (HTTP) server; headers
// (optional) are sent on every request to a remote server and may reference
// secrets via ${ENV_VAR} (expanded at load time). Returns the file written.
func AddServer(name, command string, args []string, url string, headers map[string]string, global bool, workingDir string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("server name required")
	}
	if command == "" && url == "" {
		return "", fmt.Errorf("provide a command or a url")
	}
	path, err := sqeConfigPath(global, workingDir)
	if err != nil {
		return "", err
	}
	cfg := readRawConfig(path)
	if cfg.Servers == nil {
		cfg.Servers = map[string]rawServer{}
	}
	cfg.Servers[name] = rawServer{Command: command, Args: args, URL: url, Headers: headers}
	if err := writeRawConfig(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveServer deletes a server from the sqe config files (project, then global).
// It returns the file it was removed from, or an error if the server isn't in an
// sqe-owned config (e.g. a Claude-discovered one — disable it instead).
func RemoveServer(name, workingDir string) (string, error) {
	var paths []string
	paths = append(paths, filepath.Join(workingDir, ".sqe", "mcp.json"))
	if p, err := config.GlobalConfigPath("mcp.json"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		cfg := readRawConfig(p)
		if _, ok := cfg.Servers[name]; ok {
			delete(cfg.Servers, name)
			if err := writeRawConfig(p, cfg); err != nil {
				return "", err
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("server %q isn't in an sqe config (it may be Claude-discovered); disable it with /mcps disable instead", name)
}

// ParseAddArgs parses `/mcps add` arguments: "<name> -- <command> [args...]" or
// "<name> <url>", with an optional --global/-g flag anywhere.
func ParseAddArgs(args []string) (name, command string, cmdArgs []string, url string, global bool, err error) {
	var rest []string
	for _, a := range args {
		if a == "--global" || a == "-g" {
			global = true
			continue
		}
		rest = append(rest, a)
	}
	usage := fmt.Errorf("usage: /mcps add <name> -- <command> [args] | /mcps add <name> <url> [--global]")
	if len(rest) < 2 {
		return "", "", nil, "", global, usage
	}
	name = rest[0]
	spec := rest[1:]
	if spec[0] == "--" {
		spec = spec[1:]
	}
	if len(spec) == 0 {
		return "", "", nil, "", global, usage
	}
	if len(spec) == 1 && (strings.HasPrefix(spec[0], "http://") || strings.HasPrefix(spec[0], "https://")) {
		url = spec[0]
	} else {
		command = spec[0]
		cmdArgs = spec[1:]
	}
	return name, command, cmdArgs, url, global, nil
}

func sqeConfigPath(global bool, workingDir string) (string, error) {
	if global {
		return config.GlobalConfigPath("mcp.json")
	}
	return filepath.Join(workingDir, ".sqe", "mcp.json"), nil
}

func readRawConfig(path string) rawConfig {
	var cfg rawConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func writeRawConfig(path string, cfg rawConfig) error {
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
