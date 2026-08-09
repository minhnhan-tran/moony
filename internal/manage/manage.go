// Package manage tracks which discovered skills, MCP servers, and subagent
// personas are enabled, without editing their source files. State lives in two
// scopes:
//
//   - global:  ~/.config/moony/manage.json   (a disabled list)
//   - project: ./.moony/manage.json                     (disabled + enabled overrides)
//
// Effective state, most- to least-specific:
//
//	project explicitly enabled  -> on   (re-enables a globally-disabled item)
//	project explicitly disabled -> off
//	global disabled             -> off
//	otherwise                   -> on
//
// It operates on item names so it doesn't depend on the skills/mcp packages;
// callers filter their discovered lists with SkillEnabled / MCPEnabled.
package manage

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pysty1995/moony/internal/config"
)

// Scope selects which file a toggle is written to.
type Scope string

const (
	Global  Scope = "global"
	Project Scope = "project"
)

// set is the on-disk shape for one scope. The global file uses only the
// Disabled* lists; the project file may also use Enabled* to override a global
// disable.
type set struct {
	DisabledSkills []string `json:"disabledSkills,omitempty"`
	EnabledSkills  []string `json:"enabledSkills,omitempty"`
	DisabledMCP    []string `json:"disabledMCP,omitempty"`
	EnabledMCP     []string `json:"enabledMCP,omitempty"`
	DisabledAgents []string `json:"disabledAgents,omitempty"`
	EnabledAgents  []string `json:"enabledAgents,omitempty"`
	DisabledLSP    []string `json:"disabledLSP,omitempty"`
	EnabledLSP     []string `json:"enabledLSP,omitempty"`
}

// Settings is the merged enable/disable state for a working directory.
type Settings struct {
	workingDir string
	global     set
	project    set
}

// Load reads the global and project state for workingDir. Missing or malformed
// files yield empty state (best effort — this is convenience state, not config).
func Load(workingDir string) *Settings {
	return &Settings{
		workingDir: workingDir,
		global:     read(globalPath()),
		project:    read(projectPath(workingDir)),
	}
}

// SkillEnabled reports whether a skill should be active.
func (s *Settings) SkillEnabled(name string) bool {
	return effective(name, s.project.EnabledSkills, s.project.DisabledSkills, s.global.DisabledSkills)
}

// MCPEnabled reports whether an MCP server should be active.
func (s *Settings) MCPEnabled(name string) bool {
	return effective(name, s.project.EnabledMCP, s.project.DisabledMCP, s.global.DisabledMCP)
}

// AgentEnabled reports whether a subagent persona should be active.
func (s *Settings) AgentEnabled(name string) bool {
	return effective(name, s.project.EnabledAgents, s.project.DisabledAgents, s.global.DisabledAgents)
}

// LSPEnabled reports whether a language server should be active.
func (s *Settings) LSPEnabled(name string) bool {
	return effective(name, s.project.EnabledLSP, s.project.DisabledLSP, s.global.DisabledLSP)
}

// Filter returns the items whose name (via nameOf) passes enabled. It lets
// callers prune discovered skill/MCP lists without this package importing them,
// e.g. manage.Filter(found, func(s Skill) string { return s.Name }, m.SkillEnabled).
func Filter[T any](items []T, nameOf func(T) string, enabled func(string) bool) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if enabled(nameOf(it)) {
			out = append(out, it)
		}
	}
	return out
}

// SetSkill enables/disables a skill in the given scope and persists that scope.
func (s *Settings) SetSkill(name string, enabled bool, scope Scope) error {
	return s.apply(scope, enabled,
		func(st *set) *[]string { return &st.EnabledSkills },
		func(st *set) *[]string { return &st.DisabledSkills },
		name)
}

// SetMCP enables/disables an MCP server in the given scope and persists it.
func (s *Settings) SetMCP(name string, enabled bool, scope Scope) error {
	return s.apply(scope, enabled,
		func(st *set) *[]string { return &st.EnabledMCP },
		func(st *set) *[]string { return &st.DisabledMCP },
		name)
}

// SetAgent enables/disables a subagent persona in the given scope and persists it.
func (s *Settings) SetAgent(name string, enabled bool, scope Scope) error {
	return s.apply(scope, enabled,
		func(st *set) *[]string { return &st.EnabledAgents },
		func(st *set) *[]string { return &st.DisabledAgents },
		name)
}

// SetLSP enables/disables a language server in the given scope and persists it.
func (s *Settings) SetLSP(name string, enabled bool, scope Scope) error {
	return s.apply(scope, enabled,
		func(st *set) *[]string { return &st.EnabledLSP },
		func(st *set) *[]string { return &st.DisabledLSP },
		name)
}

// apply mutates the chosen scope's enabled/disabled lists and writes it back.
// At global scope only the disabled list is used (no enable-override concept);
// at project scope an explicit enable/disable override is recorded.
func (s *Settings) apply(scope Scope, enabled bool, enabledList, disabledList func(*set) *[]string, name string) error {
	var st *set
	var path string
	if scope == Global {
		st, path = &s.global, globalPath()
	} else {
		st, path = &s.project, projectPath(s.workingDir)
	}
	if scope == Global {
		// Global is just a disabled list; "enable" means "not disabled".
		if enabled {
			*disabledList(st) = remove(*disabledList(st), name)
		} else {
			*disabledList(st) = add(*disabledList(st), name)
		}
	} else {
		if enabled {
			*enabledList(st) = add(*enabledList(st), name)
			*disabledList(st) = remove(*disabledList(st), name)
		} else {
			*disabledList(st) = add(*disabledList(st), name)
			*enabledList(st) = remove(*enabledList(st), name)
		}
	}
	return write(path, *st)
}

// effective resolves the precedence rules described in the package doc.
func effective(name string, projEnabled, projDisabled, globalDisabled []string) bool {
	switch {
	case contains(projEnabled, name):
		return true
	case contains(projDisabled, name):
		return false
	case contains(globalDisabled, name):
		return false
	default:
		return true
	}
}

func globalPath() string {
	p, err := config.GlobalConfigPath("manage.json")
	if err != nil {
		return ""
	}
	return p
}

func projectPath(workingDir string) string {
	return config.ProjectPath(workingDir, "manage.json")
}

func read(path string) set {
	var st set
	if path == "" {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func write(path string, st set) error {
	if path == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func add(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func remove(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
