package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// geminiSession drives Gemini's headless mode. Like Claude Code there's no
// long-lived process: each Prompt spawns `gemini -p … -o json`. gemini
// is a thin authenticated wrapper around Google's gemini-cli, so it
// exposes the same one-shot flags (-p/--prompt, -o/--output-format, -m/--model,
// --approval-mode). Read-only maps to --approval-mode plan; otherwise yolo.
//
// Unlike Claude there's no resumable session id in the JSON envelope, so each
// dispatch is independent (stateless).
type geminiSession struct {
	opts Options
	bin  string
}

func openGemini(opts Options) Session {
	bin := opts.Command
	if bin == "" {
		bin = "gemini"
	}
	return &geminiSession{opts: opts, bin: bin}
}

// geminiResult is the object gemini-cli prints with -o json. On success it
// carries "response"; on failure an "error" object. Parsed defensively since the
// wrapper may emit extra log lines around the JSON.
type geminiResult struct {
	Response string `json:"response"`
	Error    *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

func (s *geminiSession) Prompt(ctx context.Context, task string, onEvent func(Event)) (Result, error) {
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

	// gemini -p is one-shot: no incremental progress to stream (we only get
	// the final JSON), and the delegate tool shows a spinner while it runs, so we
	// don't emit a transient "running" line here.
	runErr := cmd.Run()

	res, parsed := parseGeminiJSON(stdout.Bytes())
	if parsed {
		if res.Error != nil {
			msg := strings.TrimSpace(res.Error.Message)
			if msg == "" {
				msg = res.Error.Type
			}
			return Result{Text: res.Response, OK: false, Err: msg}, nil
		}
		onEvent(Event{Kind: "text", Text: res.Response})
		return Result{Text: res.Response, OK: true}, nil
	}

	// Couldn't parse JSON: surface the failure with whatever the CLI emitted.
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return Result{}, fmt.Errorf("gemini failed: %v: %s", runErr, truncate(detail, 500))
	}
	// Ran cleanly but unexpected output — return the raw stdout as the answer.
	return Result{Text: strings.TrimSpace(stdout.String()), OK: true}, nil
}

// buildArgs assembles the gemini headless invocation.
func (s *geminiSession) buildArgs(task string) []string {
	args := []string{"-p", task, "-o", "json"}
	if s.opts.ReadOnly {
		// "plan" is read-only (no edits/exec); "yolo" auto-approves everything.
		args = append(args, "--approval-mode", "plan")
	} else {
		args = append(args, "--approval-mode", "yolo")
	}
	if s.opts.Model != "" {
		args = append(args, "-m", s.opts.Model)
	}
	return args
}

func (s *geminiSession) Close() error { return nil } // stateless process; nothing to release

// parseGeminiJSON extracts the result object. gemini-cli prints a single JSON
// object with -o json; if extra lines surround it, scan for the object that
// carries a "response" or "error".
func parseGeminiJSON(out []byte) (geminiResult, bool) {
	trimmed := bytes.TrimSpace(out)
	var r geminiResult
	if json.Unmarshal(trimmed, &r) == nil && (r.Response != "" || r.Error != nil) {
		return r, true
	}
	// Fall back to a line-by-line scan (defensive against log noise).
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var c geminiResult
		if json.Unmarshal(line, &c) == nil && (c.Response != "" || c.Error != nil) {
			return c, true
		}
	}
	return geminiResult{}, false
}
