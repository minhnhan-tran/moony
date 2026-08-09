package mcp

// Config loading for MCP servers. The format mirrors what most MCP clients use
// (Claude Desktop, Cursor, etc.) so existing server configs work unchanged.
//
// Servers are discovered and merged from several files, later sources overriding
// earlier ones with the same name (so your moony config always wins):
//
//	Claude (auto-discovered; "mcpServers" key) — set MN_MCP_CLAUDE=0 to skip:
//	  - ~/Library/Application Support/Claude/claude_desktop_config.json (macOS)
//	  - ~/.config/Claude/claude_desktop_config.json                     (Linux)
//	  - ~/.claude.json                       (top-level + projects["<workingDir>"])
//	  - ./.mcp.json                          (Claude Code project file)
//	moony ("servers" key):
//	  - <global>/mcp.json                    (global; <global> = ~/.config/moony,
//	                                            honoring $XDG_CONFIG_HOME, on every OS — see config.GlobalConfigDir)
//	  - ./.moony/mcp.json                      (project; overrides)
//
// Schema (sqe files use "servers", Claude files use "mcpServers"; entries are
// otherwise identical):
//
//	{
//	  "servers": {                 // or "mcpServers" for Claude files
//	    "<name>": {
//	      "command": "npx",
//	      "args":    ["-y", "@modelcontextprotocol/server-filesystem", "/path"],
//	      "env":     { "API_KEY": "..." },
//	      "url":     "https://...",  // remote servers (Claude adds "type")
//	      "headers": { "Authorization": "..." },
//	      "disabled": false
//	    }
//	  }
//	}

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

type rawConfig struct {
	Servers map[string]rawServer `json:"servers"`
}

// claudeConfig parses the Claude family of files: claude_desktop_config.json and
// project .mcp.json carry only "mcpServers"; ~/.claude.json additionally scopes
// servers per project under "projects". Unknown keys are ignored.
type claudeConfig struct {
	MCPServers map[string]rawServer `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]rawServer `json:"mcpServers"`
	} `json:"projects"`
}

type rawServer struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	URL      string            `json:"url"`
	Env      map[string]string `json:"env"`
	Headers  map[string]string `json:"headers"`
	Disabled bool              `json:"disabled"`
}

// LoadConfigs reads MCP server definitions from the Claude files (best-effort)
// and the moony config files, merging by name with later sources
// overriding earlier ones (sqe overrides Claude; project overrides global). A
// missing file is not an error; a malformed sqe file is, but a malformed Claude
// file is skipped so a foreign config can't break startup.
func LoadConfigs(workingDir string) ([]Config, error) {
	merged := map[string]rawServer{}
	apply := func(m map[string]rawServer) {
		for k, v := range m {
			merged[k] = v
		}
	}

	// 1) Claude-managed servers (auto-discovered, lowest precedence).
	if claudeDiscoveryEnabled() {
		for _, m := range claudeServers(workingDir) {
			apply(m)
		}
	}

	// 2) moony global, then project (highest precedence).
	if p, err := config.GlobalConfigPath("mcp.json"); err == nil {
		got, err := readConfig(p)
		if err != nil {
			return nil, fmt.Errorf("global mcp.json: %w", err)
		}
		apply(got)
	}
	got, err := readConfig(config.ProjectPath(workingDir, "mcp.json"))
	if err != nil {
		return nil, fmt.Errorf("project mcp.json: %w", err)
	}
	apply(got)

	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Config, 0, len(names))
	for _, n := range names {
		raw := merged[n]
		if raw.Disabled {
			continue
		}
		if raw.Command == "" && raw.URL == "" {
			continue
		}
		var env []string
		// For stdio servers, Env is the program's environment.
		for k, v := range raw.Env {
			env = append(env, k+"="+expandVars(v))
		}
		// For HTTP servers, Headers is sent on every request; we map them onto
		// the same Env field (transport disambiguates by URL vs Command).
		for k, v := range raw.Headers {
			env = append(env, k+"="+expandVars(v))
		}
		out = append(out, Config{
			Name:    n,
			Command: raw.Command,
			Args:    raw.Args,
			URL:     raw.URL,
			Env:     env,
		})
	}
	return out, nil
}

// ReferencedEnvVars returns the names of environment variables referenced by
// the MCP catalog (declared Needs) and by configured MCP servers — via ${VAR}
// references in env values, headers, args and url, plus stdio env keys. Used to
// auto-populate credential fields in the web Persona panel so any personal
// token an MCP/tool needs shows up without hardcoding. Best-effort: unreadable
// configs are skipped rather than erroring.
func ReferencedEnvVars(workingDir string) []string {
	set := map[string]bool{}
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	scan := func(s string) {
		for _, m := range braceVar.FindAllString(s, -1) {
			add(m[2 : len(m)-1]) // strip ${ and }
		}
	}

	// Catalog-declared requirements.
	for _, e := range Catalog() {
		for _, n := range e.Needs {
			add(n)
		}
	}

	// Configured servers (raw, pre-expansion so ${VAR} names survive).
	merged := map[string]rawServer{}
	apply := func(m map[string]rawServer) {
		for k, v := range m {
			merged[k] = v
		}
	}
	if claudeDiscoveryEnabled() {
		for _, m := range claudeServers(workingDir) {
			apply(m)
		}
	}
	if p, err := config.GlobalConfigPath("mcp.json"); err == nil {
		if got, err := readConfig(p); err == nil {
			apply(got)
		}
	}
	if got, err := readConfig(config.ProjectPath(workingDir, "mcp.json")); err == nil {
		apply(got)
	}
	for _, raw := range merged {
		if raw.Disabled {
			continue
		}
		for k, v := range raw.Env {
			add(k) // the stdio env key is itself a var the server reads
			scan(v)
		}
		for _, v := range raw.Headers {
			scan(v)
		}
		for _, a := range raw.Args {
			scan(a)
		}
		scan(raw.URL)
	}

	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// braceVar matches a ${NAME} reference (the braced form only).
var braceVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// expandVars replaces ${NAME} references in s with the value of environment
// variable NAME (empty when unset). Only the braced form is expanded, so literal
// "$" characters in tokens/values are left untouched. This lets MCP env and
// header values reference secrets from the environment — e.g.
// "headers": {"Authorization": "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}"} — instead of
// hardcoding credentials in mcp.json.
func expandVars(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return braceVar.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1]) // strip "${" and "}"
	})
}

// claudeServers returns the mcpServers maps found across Claude's config files,
// in increasing precedence. Parse/read failures are skipped (best effort) so a
// foreign or malformed Claude config never blocks moony.
func claudeServers(workingDir string) []map[string]rawServer {
	var out []map[string]rawServer
	add := func(m map[string]rawServer) {
		if len(m) > 0 {
			out = append(out, m)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// Claude Desktop (macOS then Linux/XDG location).
		for _, p := range []string{
			filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
			filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
		} {
			if c := readClaude(p); c != nil {
				add(c.MCPServers)
			}
		}
		// Claude Code user config: global servers, then this project's servers.
		if c := readClaude(filepath.Join(home, ".claude.json")); c != nil {
			add(c.MCPServers)
			if p, ok := c.Projects[workingDir]; ok {
				add(p.MCPServers)
			}
		}
	}
	// Claude Code project file.
	if c := readClaude(filepath.Join(workingDir, ".mcp.json")); c != nil {
		add(c.MCPServers)
	}
	return out
}

// claudeDiscoveryEnabled reports whether Claude MCP auto-discovery is on (the
// default). Set MN_MCP_CLAUDE=0/off/false/no to disable it.
func claudeDiscoveryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_MCP_CLAUDE"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// readClaude parses one Claude config file, returning nil if it's missing or
// can't be parsed (best effort — these files aren't ours to validate).
func readClaude(path string) *claudeConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c claudeConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	return &c
}

// Enabled reports whether MCP servers should be connected at all. Default on;
// set MN_MCP=0/off/false/no to skip connecting to every MCP server (config
// discovery and listings still work).
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_MCP"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// readConfig parses one mcp.json file. Returns (nil, nil) when the file is
// missing so callers can treat it as "no servers".
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
