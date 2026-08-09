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
	malformed  bool // a permissions file existed but could not be read/parsed
}

// Load reads the global and project rules for workingDir. Missing or malformed
// files yield empty rule sets (best effort). The default file-edit allow rules
// are materialized by EnsureGlobalDefaults at startup, not synthesized here, so
// the files stay the single source of truth.
func Load(workingDir string) *Rules {
	global, gOK := read(globalPath())
	project, pOK := read(projectPath(workingDir))
	return &Rules{
		workingDir: workingDir,
		global:     global,
		project:    project,
		malformed:  !gOK || !pOK,
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
	denies := append(append([]string{}, r.project.Deny...), r.global.Deny...)
	allows := append(append([]string{}, r.project.Allow...), r.global.Allow...)
	if tool == "bash" {
		return checkBash(command, denies, allows)
	}
	// Non-bash tools match by exact tool name. Deny wins.
	for _, rule := range denies {
		if rule == tool {
			return Deny
		}
	}
	for _, rule := range allows {
		if rule == tool {
			return Allow
		}
	}
	return Default
}

// checkBash resolves a bash command against the rules with awareness of shell
// operators. The command is split into its top-level simple-command segments
// (on ; | && ||): a deny rule fires when its prefix matches ANY segment — so a
// denied command can't be smuggled behind "true; …" or "… && …" — while a
// prefix ALLOW fires only when the command is a shape we can vet (no command
// substitution, redirection, or backgrounding) AND every segment is covered by
// some allow prefix, so an allowlisted prefix ("git ") can't carry a second
// command ("git status; rm -rf ~"). A bare "bash" rule allows/denies the tool
// outright. Deny always wins.
//
// Note: prefixes are still textual, so a deny like "rm -rf" can be evaded by an
// equivalent spelling ("/bin/rm …", "rm --recursive"). Deny rules are a
// convenience gate, not a sandbox; combine with the OS sandbox for real
// confinement.
func checkBash(command string, denies, allows []string) Decision {
	segments, vettable := bashSegments(command)

	for _, rule := range denies {
		if rule == "bash" {
			return Deny
		}
		if prefix, ok := bashRulePrefix(rule); ok && anyHasPrefix(segments, prefix) {
			return Deny
		}
	}
	for _, rule := range allows {
		if rule == "bash" {
			return Allow
		}
	}

	// Exact whole-command allow. Every deny check above has already run, so a deny still
	// wins; this only decides whether an ALLOW applies.
	//
	// Needed because "always allow" persists the exact command the user approved
	// ("bash:make build && make test"), and the prefix path below refuses to consider any
	// command containing >, `, $( or & — so such a rule could never match itself back and
	// the approval silently re-prompted after every reload.
	//
	// Safe where the prefix path is not: this compares the WHOLE command, so it grants
	// exactly the string that was approved and nothing else. "bash:make build" does not
	// allow "make build && rm -rf ~", because that is a different string. The prefix path
	// stays conservative precisely because a prefix rule is deliberately fuzzy.
	trimmedCmd := strings.TrimSpace(command)
	for _, rule := range allows {
		if exact, ok := bashRulePrefix(rule); ok && exactCommandMatch(exact, command, trimmedCmd) {
			return Allow
		}
	}

	if vettable && len(segments) > 0 {
		var allowPrefixes []string
		for _, rule := range allows {
			if prefix, ok := bashRulePrefix(rule); ok {
				allowPrefixes = append(allowPrefixes, prefix)
			}
		}
		if len(allowPrefixes) > 0 && allCovered(segments, allowPrefixes) {
			return Allow
		}
	}
	return Default
}

// exactCommandMatch reports whether an allow rule names this exact command.
//
// Compared both raw and space-trimmed: normalizeRule keeps significant trailing spaces on
// bash rules (a trailing space is what makes "bash:git " a prefix rule rather than a rule
// about the command "git"), so a rule stored with incidental surrounding whitespace still
// matches the command it was created from. A rule that is ONLY whitespace is rejected,
// since it would otherwise match a whitespace-only command.
func exactCommandMatch(rule, command, trimmedCommand string) bool {
	if strings.TrimSpace(rule) == "" {
		return false
	}
	return rule == command || strings.TrimSpace(rule) == trimmedCommand
}

// normalizeRule trims a rule for storage. Tool-name rules are fully trimmed;
// bash command-prefix rules keep any trailing space after "bash:" because that
// space is significant (e.g. "bash:git " must not match "gitx").
func normalizeRule(rule string) string {
	rule = strings.TrimLeft(rule, " \t")
	if strings.HasPrefix(rule, bashPrefix) {
		rule = strings.TrimRight(rule, "\r\n") // never trim significant spaces
		// "bash:" with no command is meaningless as a rule and dangerous as an allow (it
		// would name every command), so reject it rather than store it. A rule that is
		// deliberately about the bash tool as a whole is written as plain "bash".
		if strings.TrimSpace(strings.TrimPrefix(rule, bashPrefix)) == "" {
			return ""
		}
		return rule
	}
	return strings.TrimSpace(rule)
}

// bashRulePrefix returns the command prefix of a "bash:<prefix>" rule.
func bashRulePrefix(rule string) (string, bool) {
	if !strings.HasPrefix(rule, bashPrefix) {
		return "", false
	}
	return strings.TrimPrefix(rule, bashPrefix), true
}

// bashSegments splits command into its top-level simple-command segments,
// breaking on the shell control operators ; | && ||. vettable is false when the
// command contains constructs this package can't reason about — command
// substitution ($(...) or backticks), redirection (>), or backgrounding (&) — in
// which case a prefix ALLOW must not fire (fail toward prompting), though a DENY
// may still match an individual segment. Mirrors config.SafeBashCommand's
// tokenization.
func bashSegments(command string) (segments []string, vettable bool) {
	vettable = !strings.ContainsAny(command, ">`") && !strings.Contains(command, "$(")
	norm := strings.ReplaceAll(command, "&&", "\x00")
	norm = strings.ReplaceAll(norm, "||", "\x00")
	if strings.Contains(norm, "&") {
		vettable = false
	}
	norm = strings.ReplaceAll(norm, "|", "\x00")
	norm = strings.ReplaceAll(norm, ";", "\x00")
	norm = strings.ReplaceAll(norm, "\n", "\x00") // newlines separate commands too
	norm = strings.ReplaceAll(norm, "\r", "\x00")
	for _, seg := range strings.Split(norm, "\x00") {
		if s := strings.TrimSpace(seg); s != "" {
			segments = append(segments, s)
		}
	}
	return segments, vettable
}

// anyHasPrefix reports whether any segment starts with prefix.
func anyHasPrefix(segments []string, prefix string) bool {
	for _, seg := range segments {
		if strings.HasPrefix(seg, prefix) {
			return true
		}
	}
	return false
}

// allCovered reports whether every segment starts with at least one of prefixes.
func allCovered(segments, prefixes []string) bool {
	for _, seg := range segments {
		covered := false
		for _, p := range prefixes {
			if coveredByPrefix(seg, p) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// coveredByPrefix reports whether an ALLOW prefix covers a command segment, requiring the
// match to end on a token boundary.
//
// A bare strings.HasPrefix over-allows: the rule "make build" would cover "make buildx"
// (and "make build --evil"), so approving one command silently blesses different ones that
// merely start with the same characters. That matters here because "always allow" persists
// rules of exactly this shape ("bash:" + the approved command).
//
// Deny keeps using plain HasPrefix on purpose: over-matching a deny fails safe, while
// over-matching an allow is an escalation.
func coveredByPrefix(seg, prefix string) bool {
	// An empty prefix would both index out of range below and, worse, cover every possible
	// command — turning a malformed "bash:" rule into a blanket allow. Refuse it. (The deny
	// path keeps plain HasPrefix: an empty prefix there over-denies, which fails safe.)
	if strings.TrimSpace(prefix) == "" {
		return false
	}
	if !strings.HasPrefix(seg, prefix) {
		return false
	}
	rest := seg[len(prefix):]
	if rest == "" {
		return true // the segment is exactly the rule
	}
	// A rule written with a trailing separator ("git ", "cat /tmp/", "--git-dir=", or a
	// quote as in `git commit -m '`) is explicitly a prefix rule: the author wrote the
	// boundary themselves, so whatever follows is what they meant to cover.
	if end := prefix[len(prefix)-1]; isRuleBoundary(end) || end == '\'' || end == '"' {
		return true
	}
	// Otherwise the rule ends mid-token, so the command must continue at a boundary the
	// shell cannot erase. Quotes are deliberately NOT accepted here: shell quote removal
	// joins tokens, so treating them as boundaries would let the rule "make build" cover
	// make build'x' — which the shell executes as "make buildx", a different command.
	return isRuleBoundary(rest[0])
}

// isRuleBoundary reports whether c ends one shell token and cannot be elided by the shell.
// '=' and '/' are included so a flag rule ("git --git-dir") still covers
// "git --git-dir=/tmp/repo" and a path rule ("cat /tmp") still covers "cat /tmp/file.txt",
// as they did before token boundaries were enforced.
func isRuleBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '=' || c == '/'
}

// Allowed/Denied expose the merged rule lists (project first, then global) for
// display in /permissions.
func (r *Rules) Allowed() []string {
	return append(append([]string{}, r.project.Allow...), r.global.Allow...)
}
func (r *Rules) Denied() []string {
	return append(append([]string{}, r.project.Deny...), r.global.Deny...)
}

// Malformed reports whether a permissions file existed but could not be read or
// parsed, so the merged rule set may be missing deny rules. Callers should fail
// closed — never auto-run a mutating tool — when this is true.
func (r *Rules) Malformed() bool { return r != nil && r.malformed }

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

// read loads one scope's rules. The bool is false only when the file EXISTS but
// can't be read or parsed — a missing file (or empty path) is a normal empty
// scope and returns true. A parse failure must not silently drop deny rules, so
// callers treat false as "rules untrusted" and fail closed.
func read(path string) (set, bool) {
	var st set
	if path == "" {
		return st, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, true // no file yet — a legitimately empty scope
		}
		return st, false // exists but unreadable — don't trust an empty rule set
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return set{}, false // malformed — deny rules may be missing; fail closed
	}
	return st, true
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
