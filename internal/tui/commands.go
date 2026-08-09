package tui

// Custom slash commands. Users place .md files in either:
//   - ~/.config/moony/commands/<name>.md   (global)
//   - ./.moony/commands/<name>.md                     (per-project)
//
// Each file's body becomes a templated user message, with $ARGUMENTS replaced
// by whatever the user typed after the command name. An optional YAML-ish
// frontmatter block lets the author set a one-line description:
//
//   ---
//   description: review my staged diff and suggest improvements
//   ---
//
//   Please review the following staged diff and suggest improvements:
//
//   $ARGUMENTS

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// userCommand is one custom slash command loaded from disk.
type userCommand struct {
	name        string // e.g. "/review"
	description string
	template    string
	source      string // "global" or "project"
	path        string // absolute path on disk (for diagnostics)
}

// loadUserCommands reads commands from the global config dir and the project
// `.moony/commands` dir. Project commands win when names collide.
func loadUserCommands(workingDir string) []userCommand {
	merged := map[string]userCommand{}

	if base, err := config.GlobalConfigDir(); err == nil {
		for _, c := range readCommandDir(filepath.Join(base, "commands"), "global") {
			merged[c.name] = c
		}
	}
	for _, c := range readCommandDir(config.ProjectPath(workingDir, "commands"), "project") {
		merged[c.name] = c
	}

	out := make([]userCommand, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// readCommandDir reads every *.md in dir as a userCommand.
func readCommandDir(dir, source string) []userCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []userCommand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		desc, body := parseCommandFile(string(data))
		name := "/" + strings.TrimSuffix(e.Name(), ".md")
		if desc == "" {
			desc = source + " command"
		}
		out = append(out, userCommand{
			name:        name,
			description: desc,
			template:    body,
			source:      source,
			path:        full,
		})
	}
	return out
}

// parseCommandFile splits an optional `---` frontmatter block from the body and
// extracts the `description` key. Anything else in the frontmatter is ignored.
func parseCommandFile(s string) (description, body string) {
	body = s
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", strings.TrimSpace(body)
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", strings.TrimSpace(body)
	}
	front := s[4 : 4+end]
	body = strings.TrimLeft(s[4+end+4:], "\r\n")
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "description") {
			description = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return description, strings.TrimSpace(body)
}

// expandUserCommand substitutes the user's arguments into the command template.
// $ARGUMENTS is the canonical placeholder; if the template doesn't contain it,
// the args are appended on a new line so they're not lost.
func expandUserCommand(cmd userCommand, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(cmd.template, "$ARGUMENTS") {
		return strings.ReplaceAll(cmd.template, "$ARGUMENTS", args)
	}
	if args == "" {
		return cmd.template
	}
	return cmd.template + "\n\n" + args
}
