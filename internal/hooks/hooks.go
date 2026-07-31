// Package hooks runs user-defined shell scripts at agent lifecycle points so
// users can plug in notifications, linters, log forwarders, etc., without
// recompiling.
//
// Hooks are loaded from JSON configuration files in:
//   - ~/.config/moony/hooks.json   (global)
//   - ./.sqe/hooks.json                     (per-project; merged over global)
//
// File format (all fields optional):
//
//	{
//	  "pre_turn":  ["bash -c 'echo turn started'"],
//	  "post_turn": ["my-notifier 'finished'"],
//	  "pre_tool":  ["./check-tool.sh"],   // run before every tool call
//	  "post_tool": ["./log-tool.sh"]      // run after every tool call
//	}
//
// Each command is run via "sh -c", with these environment variables set:
//
//	MN_HOOK         the event name (pre_turn, post_turn, pre_tool, post_tool)
//	MN_WORKING_DIR  the agent's working directory
//	MN_TOOL_NAME    tool name (pre_tool / post_tool only)
//	MN_TOOL_INPUT   the tool's JSON input (pre_tool only)
//	MN_TOOL_OUTPUT  the tool's text output (post_tool only)
//	MN_TOOL_ERROR   the tool's error message, or empty (post_tool only)
//
// Hooks are best-effort: any error is logged via the optional logger but never
// blocks the agent. Each hook has a 10-second timeout.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"

	"github.com/pysty1995/moony/internal/procutil"
)

// Event identifies a lifecycle point.
type Event string

const (
	PreTurn  Event = "pre_turn"
	PostTurn Event = "post_turn"
	PreTool  Event = "pre_tool"
	PostTool Event = "post_tool"
)

// Context carries optional event-specific data.
type Context struct {
	WorkingDir string
	ToolName   string
	ToolInput  string
	ToolOutput string
	ToolError  string
}

// Set is a runner for a configured set of hooks.
type Set struct {
	hooks  map[Event][]string
	logger func(string)
}

// New loads hooks from the global and project config files. err is non-nil only
// when a discovered file is malformed; a missing file is not an error.
func New(workingDir string, logger func(string)) (*Set, error) {
	merged := map[Event][]string{}

	if p, err := config.GlobalConfigPath("hooks.json"); err == nil {
		if got, err := loadFile(p); err != nil {
			return nil, fmt.Errorf("global hooks: %w", err)
		} else if got != nil {
			for k, v := range got {
				merged[k] = append(merged[k], v...)
			}
		}
	}
	if got, err := loadFile(filepath.Join(workingDir, ".sqe", "hooks.json")); err != nil {
		return nil, fmt.Errorf("project hooks: %w", err)
	} else if got != nil {
		for k, v := range got {
			merged[k] = append(merged[k], v...)
		}
	}

	return &Set{hooks: merged, logger: logger}, nil
}

// Empty reports whether no hooks are configured.
func (s *Set) Empty() bool { return s == nil || len(s.hooks) == 0 }

// Count returns the total number of configured hook commands across all events.
func (s *Set) Count() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, cmds := range s.hooks {
		n += len(cmds)
	}
	return n
}

// Events returns the names of the lifecycle events that have at least one hook
// configured, in a stable order.
func (s *Set) Events() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, ev := range []Event{PreTurn, PostTurn, PreTool, PostTool} {
		if len(s.hooks[ev]) > 0 {
			out = append(out, string(ev))
		}
	}
	return out
}

// Run fires every command bound to ev, with the given context, in registered
// order. It always returns nil (errors are reported via the logger) so a misbehaving
// hook never blocks the agent.
func (s *Set) Run(ctx context.Context, ev Event, hctx Context) error {
	if s.Empty() {
		return nil
	}
	cmds := s.hooks[ev]
	if len(cmds) == 0 {
		return nil
	}
	env := append(os.Environ(),
		"MN_HOOK="+string(ev),
		"MN_WORKING_DIR="+hctx.WorkingDir,
	)
	if hctx.ToolName != "" {
		env = append(env, "MN_TOOL_NAME="+hctx.ToolName)
	}
	if hctx.ToolInput != "" {
		env = append(env, "MN_TOOL_INPUT="+hctx.ToolInput)
	}
	if hctx.ToolOutput != "" {
		env = append(env, "MN_TOOL_OUTPUT="+hctx.ToolOutput)
	}
	if hctx.ToolError != "" {
		env = append(env, "MN_TOOL_ERROR="+hctx.ToolError)
	}
	for _, c := range cmds {
		s.runOne(ctx, c, env, hctx.WorkingDir)
	}
	return nil
}

// runOne runs a single shell command with a 10s timeout. Output is forwarded to
// the logger, prefixed with the event name and a snippet of the command, so
// debugging stays readable even with many hooks.
func (s *Set) runOne(parent context.Context, command string, env []string, dir string) {
	if strings.TrimSpace(command) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	// A hook that spawns a child must not be able to outlive its 10s budget (or
	// wedge us holding the output pipe) — see procutil.KillTree.
	procutil.KillTree(cmd)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.log("hook timed out: " + truncate(command, 60))
		return
	}
	s.log(fmt.Sprintf("hook failed (%s): %v: %s", truncate(command, 60), err, strings.TrimSpace(string(out))))
}

func (s *Set) log(msg string) {
	if s != nil && s.logger != nil {
		s.logger(msg)
	}
}

// loadFile parses a hooks.json into a map of event -> commands. Returns nil,nil
// when the file does not exist (so callers can treat that as "no hooks").
func loadFile(path string) (map[Event][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	out := map[Event][]string{}
	for k, v := range raw {
		ev := Event(k)
		switch ev {
		case PreTurn, PostTurn, PreTool, PostTool:
		default:
			continue // ignore unknown keys for forward compatibility
		}
		switch vv := v.(type) {
		case string:
			out[ev] = append(out[ev], vv)
		case []any:
			for _, it := range vv {
				if s, ok := it.(string); ok {
					out[ev] = append(out[ev], s)
				}
			}
		}
	}
	return out, nil
}

// saveFile writes a hooks map back to path as JSON (event name -> commands),
// creating the parent directory, via a temp file + atomic rename.
func saveFile(path string, m map[Event][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw := make(map[string][]string, len(m))
	for ev, cmds := range m {
		raw[string(ev)] = cmds
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EnsureGlobalCommand adds command under event in the global hooks.json if it
// isn't already present, preserving every other configured hook. It lets the
// installer seed a built-in maintenance hook (e.g. refresh the codebase-memory
// index after edits) without clobbering a user's existing hooks. Idempotent and
// best-effort (a missing file is created).
func EnsureGlobalCommand(event Event, command string) error {
	switch event {
	case PreTurn, PostTurn, PreTool, PostTool:
	default:
		return fmt.Errorf("unknown hook event %q", event)
	}
	p, err := config.GlobalConfigPath("hooks.json")
	if err != nil {
		return err
	}
	m, err := loadFile(p)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[Event][]string{}
	}
	for _, c := range m[event] {
		if c == command {
			return nil // already present — nothing to do
		}
	}
	m[event] = append(m[event], command)
	return saveFile(p, m)
}

// ReplaceGlobalCommand ensures command is present under event in the global
// hooks.json, first removing any OTHER command for that event that contains
// marker — so an upgraded built-in hook supersedes the (different) string a
// prior release seeded, instead of running alongside it. User hooks and
// built-in hooks for other purposes (which don't contain marker) are preserved.
// A no-op write is avoided when nothing changed. Idempotent and best-effort.
func ReplaceGlobalCommand(event Event, marker, command string) error {
	switch event {
	case PreTurn, PostTurn, PreTool, PostTool:
	default:
		return fmt.Errorf("unknown hook event %q", event)
	}
	p, err := config.GlobalConfigPath("hooks.json")
	if err != nil {
		return err
	}
	m, err := loadFile(p)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[Event][]string{}
	}
	kept := make([]string, 0, len(m[event])+1)
	present, changed := false, false
	for _, c := range m[event] {
		switch {
		case c == command:
			present = true
			kept = append(kept, c)
		case marker != "" && strings.Contains(c, marker):
			changed = true // a stale variant of this built-in hook — drop it
		default:
			kept = append(kept, c)
		}
	}
	if !present {
		kept = append(kept, command)
		changed = true
	}
	if !changed {
		return nil
	}
	m[event] = kept
	return saveFile(p, m)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
