package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// bashTool runs a shell command in the working directory.
type bashTool struct {
	r              *Registry
	maxOutputBytes int
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Description() string {
	return "Run a shell command via 'sh -c' in the working directory and return " +
		"its combined stdout/stderr. Commands already run IN the working directory, " +
		"so you do NOT need to `cd` into it; run the command directly. Quote any path " +
		"that may contain spaces. Use for builds, tests, git, and inspecting the " +
		"system. Avoid interactive or long-running commands; supply a timeout for " +
		"anything that might hang. Set background:true to start a long-running " +
		"command (e.g. a dev server) without blocking — it returns a job id you " +
		"poll with bash_output and stop with bash_kill; add tty:true for programs " +
		"that buffer their output or prompt for input when not on a terminal."
}

func (t *bashTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"command":     {"type": "string", "description": "The shell command to execute."},
			"timeout_sec": {"type": "integer", "description": "Max seconds before the command is killed (default 120, max 600). Ignored when background is true."},
			"background":  {"type": "boolean", "description": "Run detached in the background and return a job id immediately. Poll output with bash_output and stop it with bash_kill."},
			"tty":         {"type": "boolean", "description": "With background:true, run the command under a pseudo-terminal. Use for dev servers and scripts whose output would otherwise be block-buffered (python, django) or that prompt for input; the job can then be typed into. Ignored without background."}
		},
		"required": ["command"]
	}`)
}

func (t *bashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command    string  `json:"command"`
		TimeoutSec flexInt `json:"timeout_sec"`
		Background bool    `json:"background"`
		TTY        bool    `json:"tty"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	// A bash command may modify the working tree (git ops, builds, codegen,
	// output redirection, mv/rm) in ways the structured write tools' invalidation
	// can't see, which would leave the cross-turn search cache (grep/glob/list_dir)
	// serving stale "nothing changed" stubs. The read cache self-validates by
	// size+mtime, but searches can't, so conservatively drop the search cache once
	// any command runs (covers both the foreground and background-launch paths).
	defer t.r.invalidateSearches()
	// Commands already run in the working directory (cmd.Dir below), so a leading
	// "cd <working-dir> && …" the model often prepends is redundant — and breaks
	// when the working directory contains a space and the model writes the path
	// unquoted (e.g. "cd /…/Source Code/… && git log" → cd: /…/Source: not found).
	// Drop that redundant cd; a cd to any OTHER directory is left untouched.
	in.Command = stripRedundantLeadingCD(in.Command, t.r.Root())

	// Background mode: launch detached and return a job id immediately.
	if in.Background {
		j := t.r.jobs.start(t.r.sandboxConfig(), t.r.allRoots(), in.Command, jobOpts{tty: in.TTY})
		t.r.noteOwnedJob(j.id)
		_, state, _, errMsg := j.snapshot()
		if state == jobFailed && errMsg != "" {
			return "", fmt.Errorf("%s", errMsg)
		}
		msg := fmt.Sprintf("Started background job %s.\nPoll output with bash_output(job_id=%q); stop it with bash_kill(job_id=%q).", j.id, j.id, j.id)
		if in.TTY {
			msg += "\nRunning under a pseudo-terminal: output is line-buffered and the job accepts keystrokes from the jobs panel."
		}
		return msg, nil
	}

	timeout := 120 * time.Second
	if in.TimeoutSec > 0 {
		timeout = time.Duration(int(in.TimeoutSec)) * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Optionally confine the command with an OS-level sandbox (macOS); a no-op
	// when disabled or unavailable, so the command runs identically otherwise.
	name, args := sandboxCommand(t.r.sandboxConfig(), t.r.allRoots(), "sh", "-c", in.Command)
	cmd := exec.CommandContext(cctx, name, args...)
	// Make the command killable as a whole process TREE. Without this, cancel only
	// SIGKILLs the `sh` we spawned: the real work (a build, a server, a sleep) keeps
	// running, and — because a surviving grandchild inherits our stdout/stderr pipe
	// — CombinedOutput blocks until IT exits. The visible symptom is that both the
	// timeout and a turn interrupt are ignored: a `timeout_sec: 1` call returns only
	// when the command finishes on its own.
	configureProcessGroup(cmd)
	cmd.Dir = t.r.Root()
	cmd.Env = scrubbedEnvFor(t.r.Root()) // secrets stripped; $PWD pinned to the working dir
	out, err := cmd.CombinedOutput()

	result := truncate(string(out), t.maxOutputBytes)
	if cctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		// Return output alongside a non-fatal note so the model can react.
		if result == "" {
			result = "(no output)"
		}
		return result + fmt.Sprintf("\n[exit error: %v]", err), nil
	}
	if result == "" {
		return "(command produced no output; exit 0)", nil
	}
	return result, nil
}

func (t *bashTool) Mutates() bool { return true }

// stripRedundantLeadingCD drops a leading "cd <dir>" that targets the tool's own
// working directory. Commands already run there (cmd.Dir is the working dir), so
// such a cd is redundant — and the model frequently writes the path unquoted, so
// a working directory containing a space ("…/Source Code/…") breaks the shell
// ("cd: /…/Source: No such file or directory"). Removing it is transparent: the
// command still runs in the working directory either way.
//
// Only a cd to THE working directory is removed. A cd to any other absolute path,
// or a relative cd into a subdirectory, is left untouched (it's intentional). The
// target is matched after stripping one layer of surrounding quotes and via
// filepath.Clean, so quoted, trailing-slash, and exact forms all match.
func stripRedundantLeadingCD(command, workdir string) string {
	s := strings.TrimLeft(command, " \t")
	if workdir == "" || !strings.HasPrefix(s, "cd ") {
		return command
	}
	rest := s[len("cd "):]
	// The cd ends at the first command separator ("&&" or ";"), if any.
	idx, sepLen := -1, 0
	if i := strings.Index(rest, "&&"); i >= 0 {
		idx, sepLen = i, 2
	}
	if i := strings.Index(rest, ";"); i >= 0 && (idx < 0 || i < idx) {
		idx, sepLen = i, 1
	}
	target, after := rest, ""
	if idx >= 0 {
		target, after = rest[:idx], strings.TrimLeft(rest[idx+sepLen:], " \t")
	}
	target = strings.Trim(strings.TrimSpace(target), `"'`)
	if target == "" || filepath.Clean(target) != filepath.Clean(workdir) {
		return command // a cd elsewhere (or no clear target) — leave it alone
	}
	if after == "" {
		return "true" // bare "cd <workdir>" — a harmless no-op (avoids the space break)
	}
	return after
}

// bashOutputTool returns new output (and status) from a background bash job.
type bashOutputTool struct {
	r   *Registry
	max int
}

func (t *bashOutputTool) Name() string { return "bash_output" }

func (t *bashOutputTool) Description() string {
	return "Return any new combined stdout/stderr produced by a background bash " +
		"job since the last bash_output call, along with its status (running, " +
		"done, failed, killed). Use the job_id returned by bash with background:true."
}

func (t *bashOutputTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "The background job id returned by bash (e.g. bg_1)."}
		},
		"required": ["job_id"]
	}`)
}

func (t *bashOutputTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.JobID) == "" {
		return "", fmt.Errorf("job_id is required")
	}
	j, ok := t.r.jobs.get(in.JobID)
	if !ok {
		return "", fmt.Errorf("no such background job %q", in.JobID)
	}
	out, state, exit, errMsg := j.snapshot()

	var b strings.Builder
	fmt.Fprintf(&b, "[job %s status: %s", j.id, state)
	if state == jobFailed {
		fmt.Fprintf(&b, " exit=%d", exit)
	}
	b.WriteString("]\n")
	if out == "" {
		b.WriteString("(no new output)")
	} else {
		b.WriteString(truncate(out, t.max))
	}
	if (state == jobFailed || state == jobKilled) && errMsg != "" {
		fmt.Fprintf(&b, "\n[%s]", errMsg)
	}
	return b.String(), nil
}

func (t *bashOutputTool) Mutates() bool { return false }

// bashKillTool terminates a running background bash job.
type bashKillTool struct{ r *Registry }

func (t *bashKillTool) Name() string { return "bash_kill" }

func (t *bashKillTool) Description() string {
	return "Stop a running background bash job by its job_id (from bash with " +
		"background:true). Already-finished jobs are reported as such."
}

func (t *bashKillTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": "The background job id to kill (e.g. bg_1)."}
		},
		"required": ["job_id"]
	}`)
}

func (t *bashKillTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.JobID) == "" {
		return "", fmt.Errorf("job_id is required")
	}
	j, ok := t.r.jobs.get(in.JobID)
	if !ok {
		return "", fmt.Errorf("no such background job %q", in.JobID)
	}
	_, state, _, _ := j.snapshot()
	if state != jobRunning {
		return fmt.Sprintf("Job %s already finished (status: %s).", j.id, state), nil
	}
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return fmt.Sprintf("Killed job %s.", j.id), nil
}

func (t *bashKillTool) Mutates() bool { return true }

// truncate caps s to max bytes, keeping head and tail with a marker. The head
// ends and the tail begins on a line boundary (and never mid-UTF-8-rune), so the
// model isn't handed broken lines or invalid bytes to parse; the marker reports
// the actual number of bytes elided.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	headStr := snapHeadToLine(s[:head])
	tailStr := snapTailToLine(s[len(s)-tail:])
	omitted := len(s) - len(headStr) - len(tailStr)
	return headStr + fmt.Sprintf("\n... [truncated %d bytes] ...\n", omitted) + tailStr
}

// snapHeadToLine trims a head slice so it ends at a line boundary: back up to the
// last newline (inclusive). If there is none, drop a trailing partial/invalid
// UTF-8 rune so the head is always valid UTF-8 (complete runes — including a real
// U+FFFD — are kept).
func snapHeadToLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// snapTailToLine trims a tail slice so it starts at a line boundary: advance to
// just after the first newline. If there is none, drop a leading partial/invalid
// UTF-8 rune so the tail is always valid UTF-8.
func snapTailToLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size <= 1 {
			s = s[1:]
			continue
		}
		break
	}
	return s
}
