package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// claudeSession drives Claude Code's headless print mode. There's no long-lived
// process: each Prompt spawns `claude -p … --output-format json`, and statefulness
// comes from threading the returned session_id back via --resume. Read-only is
// enforced with --permission-mode plan (no Edit/Write/Bash).
type claudeSession struct {
	opts      Options
	bin       string
	sessionID string // from the previous turn; "" on the first
}

func openClaude(opts Options) Session {
	bin := opts.Command
	if bin == "" {
		bin = "claude"
	}
	return &claudeSession{opts: opts, bin: bin}
}

// claudeResult is the object Claude Code prints with --output-format json.
type claudeResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
}

func (s *claudeSession) Prompt(ctx context.Context, task string, onEvent func(Event)) (Result, error) {
	if strings.TrimSpace(task) == "" {
		return Result{}, fmt.Errorf("task is required")
	}
	if s.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Timeout)
		defer cancel()
	}

	args := s.buildArgs(task)
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Dir = s.opts.Cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// claude -p is one-shot: there's no incremental progress to stream (we only
	// get the final JSON), and the delegate tool already shows an active spinner
	// while it runs, so we don't emit a transient "running" line here.
	runErr := cmd.Run()

	res, parsed := parseClaudeJSON(stdout.Bytes())
	if parsed {
		if res.SessionID != "" {
			s.sessionID = res.SessionID // thread context into the next dispatch
		}
		if res.IsError || res.Subtype == "error" {
			msg := res.Error
			if msg == "" {
				msg = res.Result
			}
			return Result{Text: res.Result, SessionID: res.SessionID, OK: false, Err: msg}, nil
		}
		onEvent(Event{Kind: "text", Text: res.Result})
		return Result{Text: res.Result, SessionID: res.SessionID, OK: true}, nil
	}

	// Couldn't parse JSON: surface the failure with whatever the CLI emitted.
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return Result{}, fmt.Errorf("claude failed: %v: %s", runErr, truncate(detail, 500))
	}
	// Ran cleanly but unexpected output — return the raw stdout as the answer.
	return Result{Text: strings.TrimSpace(stdout.String()), OK: true}, nil
}

// buildArgs assembles the claude print-mode invocation.
func (s *claudeSession) buildArgs(task string) []string {
	args := []string{"-p", task, "--output-format", "json"}
	if s.opts.ReadOnly {
		// "plan" mode is read-only (no edits/exec); also belt-and-suspenders the
		// mutating tools in case a future default changes.
		args = append(args, "--permission-mode", "plan",
			"--disallowedTools", "Edit", "Write", "Bash")
	}
	if s.opts.Model != "" {
		args = append(args, "--model", s.opts.Model)
	}
	if s.opts.Agent != "" {
		args = append(args, "--agents", s.opts.Agent)
	}
	if s.sessionID != "" {
		args = append(args, "--resume", s.sessionID)
	}
	return args
}

func (s *claudeSession) Close() error { return nil } // stateless process; nothing to release

// parseClaudeJSON extracts the result object. Claude prints a single JSON object
// in --output-format json; if extra lines surround it, scan for the object that
// carries a "result"/"session_id".
func parseClaudeJSON(out []byte) (claudeResult, bool) {
	trimmed := bytes.TrimSpace(out)
	var r claudeResult
	if json.Unmarshal(trimmed, &r) == nil && (r.Result != "" || r.SessionID != "" || r.Type == "result") {
		return r, true
	}
	// Fall back to a line-by-line scan (defensive against log noise).
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var c claudeResult
		if json.Unmarshal(line, &c) == nil && (c.Type == "result" || c.SessionID != "") {
			return c, true
		}
	}
	return claudeResult{}, false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
