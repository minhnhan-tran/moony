package agent

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// destructiveKind labels a category of irreversible, work-discarding command so
// the guard can ask "did the user actually request THIS kind of action?" before
// letting an agent (especially an auto-approved or background sub-agent) run it.
type destructiveKind string

const (
	kindGitDiscard destructiveKind = "discard uncommitted changes" // reset --hard, checkout -- ., restore, clean
	kindGitAmend   destructiveKind = "rewrite git history"         // commit --amend, rebase, push --force
	kindRmRf       destructiveKind = "recursively delete files"    // rm -rf
	kindInfraDown  destructiveKind = "tear down infrastructure"    // terraform/pulumi/cdk destroy
	kindDBDrop     destructiveKind = "drop a database/table"       // drop database/table
)

// destructiveCommand classifies a tool call as a work-destroying operation and
// returns its kind. Unlike the static allow/deny lists in permissions.json, this
// is a CONTEXTUAL guard: the caller pairs it with userRequestedDestructive so a
// command that irreversibly discards work is only auto-run when the user plainly
// asked for it — otherwise it's forced through human approval (and denied for a
// non-interactive/background agent). Mirrors Claude Code's auto-mode classifier.
//
// It is deliberately conservative (favours flagging): a false positive just adds
// one approval prompt, while a miss can nuke a user's uncommitted work.
func destructiveCommand(name string, input json.RawMessage) (destructiveKind, bool) {
	if name != "bash" {
		return "", false
	}
	return classifyDestructive(bashCommandField(input))
}

func classifyDestructive(cmd string) (destructiveKind, bool) {
	lc := strings.ToLower(cmd)
	fields := strings.Fields(lc)
	// Case-preserving fields for git, where flag case is significant (-D vs -d,
	// -f). The lowercased fields are fine for subcommand names.
	rawFields := strings.Fields(cmd)

	// rm -rf / rm -fr / rm -r -f (recursive delete). Match an rm with both
	// recursive and force flags (combined "-rf" or separate).
	if k, ok := detectRmRf(fields); ok {
		return k, true
	}

	// git subcommands that discard or rewrite work (case-sensitive: -D != -d).
	if k, ok := detectDestructiveGit(rawFields); ok {
		return k, true
	}

	// Infra teardown: terraform destroy / pulumi destroy / cdk destroy /
	// terraform apply -destroy.
	for i, f := range fields {
		switch f {
		case "terraform", "pulumi", "cdk", "terragrunt":
			for _, rest := range fields[i+1:] {
				if rest == "destroy" || rest == "-destroy" || rest == "--destroy" {
					return kindInfraDown, true
				}
			}
		case "kubectl":
			// `kubectl delete` of namespaces/all is destructive; flag delete.
			for _, rest := range fields[i+1:] {
				if rest == "delete" {
					return kindInfraDown, true
				}
			}
		case "find":
			// `find … -delete` recursively deletes matched files.
			for _, rest := range fields[i+1:] {
				if rest == "-delete" {
					return kindRmRf, true
				}
			}
		case "shred":
			return kindRmRf, true
		case "dd":
			// `dd of=/dev/… ` / `dd of=<file>` overwrites its target.
			for _, rest := range fields[i+1:] {
				if strings.HasPrefix(rest, "of=") {
					return kindRmRf, true
				}
			}
		case "mkfs", "mkfs.ext4", "mkfs.xfs":
			return kindRmRf, true
		}
	}

	// SQL drop (best-effort substring — SQL is quoted, not tokenized).
	if strings.Contains(lc, "drop database") || strings.Contains(lc, "drop table") || strings.Contains(lc, "truncate table") {
		return kindDBDrop, true
	}

	return "", false
}

// detectRmRf reports an `rm` invocation carrying both -r and -f (in any flag
// form). Guards against matching unrelated tokens like "confirm" or "form".
func detectRmRf(fields []string) (destructiveKind, bool) {
	for i, f := range fields {
		if f != "rm" {
			continue
		}
		recursive, force := false, false
		for _, t := range fields[i+1:] {
			if !strings.HasPrefix(t, "-") {
				continue // a path operand; keep scanning flags before it in practice
			}
			if strings.HasPrefix(t, "--") {
				switch t {
				case "--recursive":
					recursive = true
				case "--force":
					force = true
				}
				continue
			}
			// short-flag cluster like -rf, -fr, -r, -f
			for _, c := range t[1:] {
				switch c {
				case 'r', 'R':
					recursive = true
				case 'f':
					force = true
				}
			}
		}
		// A recursive delete is destructive whether or not -f is present: `rm -r dir`
		// discards the same work, it just stops on the first permission error.
		if recursive {
			_ = force
			return kindRmRf, true
		}
	}
	return "", false
}

// detectDestructiveGit walks git tokens (skipping -C/-c global flags with their
// values) and flags subcommands that discard uncommitted work or rewrite history.
func detectDestructiveGit(fields []string) (destructiveKind, bool) {
	for i, f := range fields {
		if f != "git" {
			continue
		}
		// Find the subcommand and its args, skipping global flags.
		sub := ""
		args := []string{}
		for j := i + 1; j < len(fields); j++ {
			t := fields[j]
			if sub == "" {
				if strings.HasPrefix(t, "-") {
					if t == "-c" || t == "-C" {
						j++ // takes a value
					}
					continue
				}
				sub = t
				continue
			}
			args = append(args, t)
		}
		switch sub {
		case "reset":
			if hasToken(args, "--hard") || hasTokenPrefix(args, "--hard=") {
				return kindGitDiscard, true
			}
		case "clean":
			// clean with -f/-d/-x removes untracked files.
			for _, a := range args {
				if strings.HasPrefix(a, "-") && strings.ContainsAny(a, "fdx") {
					return kindGitDiscard, true
				}
			}
		case "checkout", "restore":
			// `git checkout -- .` / `git checkout .` / `git restore .` discards
			// working-tree edits.
			if hasToken(args, "--") || hasToken(args, ".") {
				return kindGitDiscard, true
			}
		case "commit":
			if hasToken(args, "--amend") {
				return kindGitAmend, true
			}
		case "rebase":
			return kindGitAmend, true
		case "push":
			if hasToken(args, "--force") || hasToken(args, "-f") || hasTokenPrefix(args, "--force-with-lease") {
				return kindGitAmend, true
			}
			// Force push via refspec: `git push origin +main`.
			for _, a := range args {
				if strings.HasPrefix(a, "+") && len(a) > 1 {
					return kindGitAmend, true
				}
			}
		case "rm":
			// `git rm -rf` force-removes tracked files from the tree.
			for _, a := range args {
				if strings.HasPrefix(a, "-") && strings.ContainsAny(a, "rf") {
					return kindGitDiscard, true
				}
			}
		case "filter-branch", "filter-repo":
			return kindGitAmend, true
		case "branch":
			if hasToken(args, "-D") {
				return kindGitDiscard, true
			}
		case "stash":
			for _, a := range args {
				if a == "drop" || a == "clear" {
					return kindGitDiscard, true
				}
			}
		}
	}
	return "", false
}

func hasToken(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasTokenPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// bashEscapesRoot reports whether a shell command reaches outside the isolated
// worktree rooted at root, returning a short human reason. It is a conservative
// defense-in-depth layer on top of the registry root confinement of file tools
// (NOT a security sandbox — a determined command can still evade a regex). It
// errs toward refusing: it flags the escape vectors an LLM realistically emits —
// an absolute path outside root anywhere in a token, `cd`/`pushd` above root,
// `git -C`/`--git-dir`/`--work-tree` elsewhere, home/tilde expansion, and shell
// features it can't statically analyze (command substitution, `eval`, `bash -c`).
func bashEscapesRoot(cmd, root string) (string, bool) {
	root = filepath.Clean(root)

	// Home/parent expansion and command substitution can resolve anywhere; since
	// we can't evaluate them statically, refuse rather than guess. `~` only counts
	// as expansion at a token boundary (start or after =,:).
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") || strings.ContainsRune(cmd, '`') || hasVarExpansion(cmd) {
		return "uses shell/command/variable substitution, which can resolve outside the worktree", true
	}
	if hasTildeExpansion(cmd) {
		return "uses ~ home-directory expansion, which resolves outside the worktree", true
	}
	// Nested shells / eval hide their real command from this check — refuse.
	for _, tok := range strings.Fields(cmd) {
		switch tok {
		case "eval", "source":
			return "uses " + tok + ", which can run commands outside the worktree", true
		}
	}
	if bashDashC(cmd) {
		return "wraps another shell (bash -c/sh -c), whose command can't be checked", true
	}

	// Tokenize on whitespace AND shell operators (so `cd&&…`, `>/abs`, `|` don't
	// hide an escaping operand by gluing it to an operator).
	fields := splitShellTokens(cmd)
	for i := 0; i < len(fields); i++ {
		f := fields[i]

		if f == "cd" || f == "pushd" {
			if i+1 < len(fields) {
				if target := fields[i+1]; !strings.HasPrefix(target, "-") {
					if p, ok := resolveUnderRoot(root, target); !ok {
						return "changes directory to " + p + " outside the worktree", true
					}
				}
			}
			continue
		}

		// git targeting another tree explicitly (space-separated form).
		if f == "-C" || f == "--git-dir" || f == "--work-tree" {
			if i+1 < len(fields) {
				if p, ok := resolveUnderRoot(root, fields[i+1]); !ok {
					return "points git at " + p + " outside the worktree", true
				}
			}
			continue
		}
		if v, ok := flagValue(f, "--git-dir"); ok {
			if p, ok := resolveUnderRoot(root, v); !ok {
				return "points git at " + p + " outside the worktree", true
			}
		}
		if v, ok := flagValue(f, "--work-tree"); ok {
			if p, ok := resolveUnderRoot(root, v); !ok {
				return "points git at " + p + " outside the worktree", true
			}
		}

		// Any absolute path embedded in the token (bare operand `/x`, redirection
		// `>/x`, or flag value `--out=/x`) that lands outside root and isn't a safe
		// system location.
		if p, out := absPathEscapes(f, root); out {
			return "references absolute path " + p + " outside the worktree", true
		}
	}
	return "", false
}

// absPathEscapes extracts an absolute path from a token — whether bare (`/x`),
// after a redirection (`>/x`, `2>/x`), or after `=` (`--out=/x`) — and reports
// whether it lands outside root (excluding safe system paths).
func absPathEscapes(tok, root string) (string, bool) {
	cand := tok
	cand = strings.TrimLeft(cand, "<>&0123456789") // strip redirection prefixes
	if eq := strings.IndexByte(cand, '='); eq >= 0 && !strings.HasPrefix(cand, "/") {
		cand = cand[eq+1:] // value after --flag=
	}
	cand = strings.Trim(cand, `"'`)
	if !strings.HasPrefix(cand, "/") || isSafeSystemPath(cand) {
		return "", false
	}
	if p, ok := resolveUnderRoot(root, cand); !ok {
		return p, true
	}
	return "", false
}

// splitShellTokens splits on whitespace and shell operators (; & | && || > >> <
// ( )) so an escaping operand glued to an operator is still inspected as its own
// token. Approximate — good enough for the heuristic.
func splitShellTokens(cmd string) []string {
	repl := cmd
	for _, op := range []string{"&&", "||", ">>", ";", "|", "&", ">", "<", "(", ")"} {
		repl = strings.ReplaceAll(repl, op, " "+op+" ")
	}
	return strings.Fields(repl)
}

func hasTildeExpansion(cmd string) bool {
	for _, tok := range strings.Fields(cmd) {
		if tok == "~" || strings.HasPrefix(tok, "~/") || strings.HasPrefix(tok, "~") && len(tok) > 1 && tok[1] != '=' {
			return true
		}
	}
	return false
}

func bashDashC(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if (f == "bash" || f == "sh" || f == "zsh" || f == "dash") && i+1 < len(fields) {
			for _, rest := range fields[i+1:] {
				if rest == "-c" {
					return true
				}
			}
		}
	}
	return false
}

// resolveUnderRoot joins target onto root (or cleans it if absolute) and reports
// whether the result stays within root. Returns the cleaned path for messaging.
func resolveUnderRoot(root, target string) (string, bool) {
	target = strings.Trim(target, `"'`)
	var abs string
	if filepath.IsAbs(target) {
		abs = filepath.Clean(target)
	} else {
		abs = filepath.Clean(filepath.Join(root, target))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs, false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs, false
	}
	return abs, true
}

// flagValue extracts the value from a `--flag=value` token.
func flagValue(tok, name string) (string, bool) {
	prefix := name + "="
	if strings.HasPrefix(tok, prefix) {
		return strings.TrimPrefix(tok, prefix), true
	}
	return "", false
}

// isSafeSystemPath allows absolute references to standard read-only system
// locations that shell commands routinely touch, so the escape guard doesn't
// false-positive on e.g. `/usr/bin/env` or `/tmp` scratch files.
func isSafeSystemPath(p string) bool {
	for _, ok := range []string{"/bin", "/sbin", "/usr", "/opt", "/dev", "/tmp", "/var/tmp", "/etc", "/private/tmp", "/private/var/folders", "/System", "/Library"} {
		if p == ok || strings.HasPrefix(p, ok+"/") {
			return true
		}
	}
	return false
}

// recentUserTexts returns the plain text of the most recent USER messages in the
// conversation (newest first, capped), for the destructive-command intent gate.
// Only user-authored text is considered — never assistant/tool output, which an
// attacker could use to smuggle "please rm -rf" via indirect prompt injection.
func (a *Agent) recentUserTexts() []string {
	const maxMsgs = 6
	var out []string
	for i := len(a.messages) - 1; i >= 0 && len(out) < maxMsgs; i-- {
		m := a.messages[i]
		if m.Role != llm.RoleUser {
			continue
		}
		var b strings.Builder
		for _, c := range m.Content {
			// Only real user prose. Tool-result blocks are carried on user-role
			// messages too (RoleUser + ToolResult), so skip anything that isn't a
			// text block to avoid treating tool output as user intent.
			if c.Type == "" || c.Type == "text" {
				if c.Text != "" {
					b.WriteString(c.Text)
					b.WriteByte('\n')
				}
			}
		}
		if s := b.String(); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// userRequestedDestructive reports whether the user's own words plainly asked for
// a destructive action of the given kind, so the guard can let it proceed instead
// of blocking. Deliberately narrow, because a false positive silently skips a
// human confirmation for irreversible work:
//
//   - only the CURRENT turn's user message is considered (userTexts[0], newest
//     first) — an "ok delete the temp dir" three turns ago must not pre-authorize
//     an unrelated destructive command later;
//   - only USER text is read (never the model's own or tool output, which could be
//     prompt-injected — see recentUserTexts);
//   - a cue inside a QUESTION ("why is rm -rf dangerous?") or a NEGATED clause
//     ("never run git reset --hard", "don't delete anything") does not count.
//
// When in doubt it returns false, i.e. "require approval".
func userRequestedDestructive(userTexts []string, kind destructiveKind) bool {
	if len(userTexts) == 0 {
		return false
	}
	// Verb sets per kind — the user must have used discard/reset-style language.
	var cues []string
	switch kind {
	case kindGitDiscard:
		cues = []string{"discard", "reset --hard", "hard reset", "throw away", "revert my changes", "revert the changes", "clean up untracked", "wipe", "start over", "blow away", "nuke"}
	case kindGitAmend:
		cues = []string{"amend", "rewrite history", "force push", "force-push", "rebase", "squash"}
	case kindRmRf:
		cues = []string{"rm -rf", "delete", "remove", "wipe", "clean out", "nuke", "blow away"}
	case kindInfraDown:
		cues = []string{"destroy", "tear down", "teardown", "delete", "bring down"}
	case kindDBDrop:
		cues = []string{"drop", "delete", "truncate", "wipe"}
	}
	// Current turn only.
	return clauseRequests(userTexts[0], cues)
}

// negationRE spots phrasing that ASKS US NOT to do the thing (or merely discusses
// it), so a cue in that clause must not authorize the action.
var negationRE = regexp.MustCompile(`(?i)\b(do\s?n'?t|does\s?n'?t|did\s?n'?t|never|no\s+need|without|avoid|instead\s+of|rather\s+than|why\s+is|why\s+does|what\s+(does|happens)|is\s+it\s+safe|should\s+i|can\s+you\s+explain|explain)\b`)

// clauseRequests reports whether any clause of text contains one of the cues as an
// affirmative instruction. Splitting on clause boundaries keeps "delete the build
// dir, but don't touch .git" from being read as a blanket authorization derived
// from the negated half (and vice versa).
func clauseRequests(text string, cues []string) bool {
	for _, clause := range splitClauses(strings.ToLower(text)) {
		if strings.Contains(clause, "?") || negationRE.MatchString(clause) {
			continue // a question or a "don't do X" clause is not a request
		}
		for _, c := range cues {
			if strings.Contains(clause, c) {
				return true
			}
		}
	}
	return false
}

// splitClauses breaks text on sentence/clause punctuation and coordinating "but".
func splitClauses(text string) []string {
	repl := text
	for _, sep := range []string{".", "!", ";", "\n", ", but ", " but ", ", and ", " however "} {
		repl = strings.ReplaceAll(repl, sep, "\x00")
	}
	var out []string
	for _, part := range strings.Split(repl, "\x00") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hasVarExpansion reports a bare $VAR / $1 shell expansion, whose value can't be
// resolved statically and so could point anywhere.
func hasVarExpansion(cmd string) bool {
	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] != '$' {
			continue
		}
		c := cmd[i+1]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return true
		}
	}
	return false
}
