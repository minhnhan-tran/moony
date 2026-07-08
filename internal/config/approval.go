package config

import "strings"

// Approval policy levels, from most to least cautious. They control when the
// agent gates a mutating tool call behind a user confirmation prompt:
//
//	prompt — confirm every mutating tool (write_file, edit_file, bash, task).
//	         Read-only tools never prompt. This is the default.
//	safe   — additionally auto-run read-only bash commands (see SafeBashCommand);
//	         still confirm writes, edits, task, and any non-trivial bash.
//	auto   — never prompt; auto-run everything EXCEPT the hard-gated
//	         git commit/push, which are always confirmed.
//	danger-skip-permission — auto-approve EVERYTHING, including git commit/push.
//	         Structural gates still apply: plan mode / a pending plan draft still
//	         block edits until the plan is approved (exit_plan), and an explicit
//	         "deny" permission rule still blocks. Use with care.
const (
	ApprovalPrompt     = "prompt"
	ApprovalSafe       = "safe"
	ApprovalAuto       = "auto"
	ApprovalDangerSkip = "danger-skip-permission"
)

// ApprovalPolicies lists the selectable policies in order (cautious → permissive).
var ApprovalPolicies = []string{ApprovalPrompt, ApprovalSafe, ApprovalAuto, ApprovalDangerSkip}

// NormalizeApprovalPolicy maps a string (including a few friendly aliases) to a
// valid policy, defaulting to "prompt".
func NormalizeApprovalPolicy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ApprovalSafe, "safe-bash", "readonly", "read-only":
		return ApprovalSafe
	case ApprovalAuto, "auto-run", "off", "none", "no", "false", "yolo":
		return ApprovalAuto
	case ApprovalDangerSkip, "danger-skip-permissions", "skip-permissions",
		"dangerously-skip-permissions", "danger", "danger-skip":
		return ApprovalDangerSkip
	default:
		return ApprovalPrompt
	}
}

// safeBashPrograms are commands that only read/inspect state and never mutate
// the filesystem or run arbitrary code, so they're auto-runnable under the
// "safe" policy.
var safeBashPrograms = map[string]bool{
	"ls": true, "pwd": true, "echo": true, "cat": true, "head": true, "tail": true,
	"wc": true, "grep": true, "egrep": true, "fgrep": true, "rg": true, "find": true,
	"which": true, "type": true, "file": true, "stat": true, "date": true, "uname": true,
	"env": true, "printenv": true, "sort": true, "uniq": true, "cut": true, "tr": true,
	"diff": true, "cmp": true, "tree": true, "du": true, "df": true, "basename": true,
	"dirname": true, "realpath": true, "readlink": true, "true": true, "test": true,
	"id": true, "whoami": true, "hostname": true, "column": true, "nl": true,
}

// safeGitSubcommands are read-only git subcommands.
var safeGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true,
	"ls-files": true, "ls-tree": true, "cat-file": true, "blame": true,
	"describe": true, "shortlog": true, "show-ref": true, "symbolic-ref": true,
}

// safeGoSubcommands are go subcommands that build/inspect but don't fetch deps
// or rewrite sources.
var safeGoSubcommands = map[string]bool{
	"build": true, "test": true, "vet": true, "list": true, "doc": true,
	"version": true, "env": true,
}

// SafeBashCommand reports whether a shell command is read-only enough to run
// without confirmation under the "safe" approval policy. It is deliberately
// conservative: it refuses anything it can't confidently classify, including
// output redirection, command substitution, backgrounding, env assignments, and
// any program not on the allowlist. A command is safe only if EVERY simple
// command (split across | && || ;) is itself safe.
func SafeBashCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Reject constructs we can't reason about: file-writing redirection (>),
	// command substitution ($(...) or backticks), and process substitution.
	if strings.ContainsAny(cmd, ">`") || strings.Contains(cmd, "$(") {
		return false
	}

	// Normalize control operators to a single delimiter. Handle "&&"/"||"
	// first so a leftover single "&" (backgrounding) disqualifies the command.
	norm := strings.ReplaceAll(cmd, "&&", "\x00")
	norm = strings.ReplaceAll(norm, "||", "\x00")
	if strings.Contains(norm, "&") {
		return false
	}
	norm = strings.ReplaceAll(norm, "|", "\x00")
	norm = strings.ReplaceAll(norm, ";", "\x00")
	norm = strings.ReplaceAll(norm, "\n", "\x00") // newlines separate commands too
	norm = strings.ReplaceAll(norm, "\r", "\x00")

	for _, seg := range strings.Split(norm, "\x00") {
		if !safeSimpleCommand(strings.Fields(seg)) {
			return false
		}
	}
	return true
}

// safeSimpleCommand classifies one simple command (already split into fields).
func safeSimpleCommand(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	prog := fields[0]
	// A leading VAR=value assignment (e.g. "FOO=bar cmd") is too open-ended to
	// vet, so treat it as unsafe.
	if strings.Contains(prog, "=") {
		return false
	}
	switch prog {
	case "git":
		return len(fields) >= 2 && safeGitSubcommands[fields[1]]
	case "go":
		return len(fields) >= 2 && safeGoSubcommands[fields[1]]
	default:
		return safeBashPrograms[prog]
	}
}
