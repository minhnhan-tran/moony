// Package permissions implements persisted allow/deny rules that pre-decide
// whether a tool call needs approval, checked before the agent falls through to
// its interactive approval policy (prompt/safe/auto).
//
// Rules live in two scopes, both named permissions.json:
//
//   - global:  ~/.config/moony/permissions.json
//   - project: ./.sqe/permissions.json
//
// Each scope has an "allow" and a "deny" list. A rule is either a bare tool
// name (e.g. "write_file") or a bash command-prefix rule of the form
// "bash:<prefix>" (e.g. "bash:git ", "bash:rm -rf"). A bash rule matches when
// the command, after trimming leading whitespace, starts with <prefix>.
//
// Precedence, most decisive first:
//
//	deny match  -> Deny    (never runs, even with --no-approval)
//	allow match -> Allow   (runs without a prompt)
//	otherwise   -> Default (fall through to the approval policy)
//
// Deny always wins over allow, across both scopes.
package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// Decision is the outcome of consulting the rules for a tool call.
type Decision int

const (
	// Default means no rule matched; the caller's approval policy decides.
	Default Decision = iota
	// Allow means a rule pre-approved the call; skip the prompt.
	Allow
	// Deny means a rule forbids the call; it must not run.
	Deny
)

// Scope selects which file a rule is written to.
type Scope string

const (
	Global  Scope = "global"
	Project Scope = "project"
)

// bashPrefix is the marker for bash command-prefix rules.
const bashPrefix = "bash:"

// set is the on-disk shape for one scope.
type set struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Rules is the merged allow/deny state for a working directory.
type Rules struct {
	workingDir string
	global     set
	project    set
}

// Load reads the global and project rules for workingDir. Missing or malformed
// files yield empty rule sets (best effort). The default file-edit allow rules
// are materialized by EnsureGlobalDefaults at startup, not synthesized here, so
// the files stay the single source of truth.
func Load(workingDir string) *Rules {
	return &Rules{
		workingDir: workingDir,
		global:     read(globalPath()),
		project:    read(projectPath(workingDir)),
	}
}

// defaultGlobalAllow pre-approves the everyday file read/edit tools on a fresh
// install so basic editing runs without a prompt. Riskier tools (bash, the git
// mutations, bash_kill) are intentionally omitted and still fall through to the
// approval policy.
var defaultGlobalAllow = []string{"write_file", "edit_file", "multi_edit", "apply_patch"}

// EnsureGlobalDefaults writes the default global permissions file the first time
// the agent runs (when none exists yet) so the defaults are active and are
// visible and editable in the /permissions command and the web UI's Tools menu.
// It never overwrites an existing file, so user edits are always preserved.
// Best effort: a failure to write just leaves it unconfigured.
func EnsureGlobalDefaults() {
	path := globalPath()
	if path == "" {
		return
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return // file exists, or stat failed for some other reason — leave it
	}
	_ = write(path, set{Allow: append([]string{}, defaultGlobalAllow...)})
}

// Check returns the decision for a tool call. command is the bash command (only
// consulted for the "bash" tool); pass "" for other tools.
func (r *Rules) Check(tool, command string) Decision {
	if r == nil {
		return Default
	}
	// Deny wins, so check all deny rules across both scopes first.
	for _, rule := range append(append([]string{}, r.project.Deny...), r.global.Deny...) {
		if matches(rule, tool, command) {
			return Deny
		}
	}
	for _, rule := range append(append([]string{}, r.project.Allow...), r.global.Allow...) {
		if matches(rule, tool, command) {
			return Allow
		}
	}
	return Default
}

// normalizeRule trims a rule for storage. Tool-name rules are fully trimmed;
// bash command-prefix rules keep any trailing space after "bash:" because that
// space is significant (e.g. "bash:git " must not match "gitx").
func normalizeRule(rule string) string {
	rule = strings.TrimLeft(rule, " \t")
	if strings.HasPrefix(rule, bashPrefix) {
		return strings.TrimRight(rule, "\r\n") // never trim significant spaces
	}
	return strings.TrimSpace(rule)
}

// matches reports whether a single rule applies to a tool/command.
func matches(rule, tool, command string) bool {
	if strings.HasPrefix(rule, bashPrefix) {
		if tool != "bash" {
			return false
		}
		prefix := strings.TrimPrefix(rule, bashPrefix)
		return strings.HasPrefix(strings.TrimSpace(command), prefix)
	}
	return rule == tool
}

// Allowed/Denied expose the merged rule lists (project first, then global) for
// display in /permissions.
func (r *Rules) Allowed() []string {
	return append(append([]string{}, r.project.Allow...), r.global.Allow...)
}
func (r *Rules) Denied() []string {
	return append(append([]string{}, r.project.Deny...), r.global.Deny...)
}

// Lists returns the allow and deny rules for a single scope, for editors that
// target one file (e.g. the web UI's Tools menu, which writes the global scope).
func (r *Rules) Lists(scope Scope) (allow, deny []string) {
	if r == nil {
		return nil, nil
	}
	if scope == Global {
		return append([]string{}, r.global.Allow...), append([]string{}, r.global.Deny...)
	}
	return append([]string{}, r.project.Allow...), append([]string{}, r.project.Deny...)
}

// Add records an allow (or deny) rule in the given scope and persists it. A
// rule added to the opposite list in the same scope is removed so the two never
// conflict within one file.
func (r *Rules) Add(rule string, deny bool, scope Scope) error {
	rule = normalizeRule(rule)
	if rule == "" {
		return os.ErrInvalid
	}
	var st *set
	var path string
	if scope == Global {
		st, path = &r.global, globalPath()
	} else {
		st, path = &r.project, projectPath(r.workingDir)
	}
	if deny {
		st.Deny = add(st.Deny, rule)
		st.Allow = remove(st.Allow, rule)
	} else {
		st.Allow = add(st.Allow, rule)
		st.Deny = remove(st.Deny, rule)
	}
	return write(path, *st)
}

// Remove deletes a rule from both lists in the given scope and persists it.
func (r *Rules) Remove(rule string, scope Scope) error {
	rule = normalizeRule(rule)
	var st *set
	var path string
	if scope == Global {
		st, path = &r.global, globalPath()
	} else {
		st, path = &r.project, projectPath(r.workingDir)
	}
	st.Allow = remove(st.Allow, rule)
	st.Deny = remove(st.Deny, rule)
	return write(path, *st)
}

func globalPath() string {
	p, err := config.GlobalConfigPath("permissions.json")
	if err != nil {
		return ""
	}
	return p
}

func projectPath(workingDir string) string {
	return filepath.Join(workingDir, ".sqe", "permissions.json")
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
