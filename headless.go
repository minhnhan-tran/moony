package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pysty1995/moony/internal/agent"
)

// headlessPrompt assembles the prompt for a non-interactive (-p/--print) run
// from, in order: the flag value, any piped stdin, and trailing positional
// args. It returns ok=false when no headless prompt was requested at all (no
// flag, no positional args, and stdin is a terminal) so the caller falls back
// to the interactive TUI.
func headlessPrompt(printFlag, printShort string) (string, bool) {
	var parts []string

	flagVal := strings.TrimSpace(printShort)
	if flagVal == "" {
		flagVal = strings.TrimSpace(printFlag)
	}
	// An explicitly-passed -p/--print (even empty) opts into headless mode.
	requested := isFlagPassed("p") || isFlagPassed("print")
	if flagVal != "" {
		parts = append(parts, flagVal)
	}

	// Positional args (after flags) are treated as additional prompt text.
	if args := flag.Args(); len(args) > 0 {
		parts = append(parts, strings.Join(args, " "))
		requested = true
	}

	// Piped stdin (not a terminal) is appended. We only consume stdin when
	// headless was already requested OR stdin is actually piped, so an
	// interactive launch never blocks reading stdin.
	if piped, data := readPipedStdin(); piped {
		if s := strings.TrimSpace(string(data)); s != "" {
			parts = append(parts, s)
		}
		requested = true
	}

	if !requested {
		return "", false
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), true
}

// isFlagPassed reports whether the named flag was set on the command line.
func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// runHeadless executes a single prompt to completion without the TUI. It
// streams assistant text to stdout and routes tool/diagnostic activity to
// stderr; with jsonOut it instead prints one JSON object with the final text.
// Approval prompts are auto-approved (deny permission rules still block, since
// they're enforced before the approval policy). cleanup runs before returning.
// The return value is the process exit code.
func runHeadless(ctx context.Context, ag *agent.Agent, prompt string, jsonOut bool, cleanup func()) int {
	defer cleanup()

	if strings.TrimSpace(prompt) == "" {
		fmt.Fprintln(os.Stderr, "error: empty prompt (use -p \"...\" or pipe text on stdin)")
		return 2
	}

	var assistant strings.Builder
	emit := func(ev agent.Event) {
		switch e := ev.(type) {
		case agent.ThinkingDelta:
			// Stream reasoning to stderr (not stdout) so -p output stays just the
			// answer, but the thinking is still visible live. Suppressed under
			// --json (the structured result carries only the response).
			if !jsonOut {
				fmt.Fprint(os.Stderr, e.Text)
			}
		case agent.TextDelta:
			assistant.WriteString(e.Text)
			if !jsonOut {
				fmt.Print(e.Text)
			}
		case agent.ToolStart:
			fmt.Fprintf(os.Stderr, "[tool] %s %s\n", e.Name, summarizeInput(e.Input))
		case agent.ToolEnd:
			if e.IsError {
				fmt.Fprintf(os.Stderr, "[tool:%s] error: %s\n", e.Name, firstLine(e.Output))
			}
		case agent.Info:
			fmt.Fprintln(os.Stderr, "[info] "+e.Text)
		case agent.ApprovalRequest:
			// No interactive user in headless mode: auto-approve. (A deny
			// permission rule would have blocked the call before this point.)
			e.Reply <- agent.Approve
		}
	}

	runErr := ag.Run(ctx, prompt, emit)

	if jsonOut {
		out := map[string]any{
			"ok":        runErr == nil,
			"response":  assistant.String(),
			"sessionId": ag.SessionID(),
		}
		if runErr != nil {
			out["error"] = runErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else if !strings.HasSuffix(assistant.String(), "\n") {
		fmt.Println() // ensure a trailing newline after streamed text
	}

	if runErr != nil {
		if !jsonOut {
			fmt.Fprintln(os.Stderr, "error: "+runErr.Error())
		}
		return 1
	}
	return 0
}

// summarizeInput shortens a tool input JSON blob for the stderr activity log.
func summarizeInput(s string) string {
	s = strings.ReplaceAll(firstLine(s), "  ", " ")
	const max = 120
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// readPipedStdin reads stdin when it is not a terminal (i.e. data was piped or
// redirected). It returns piped=false for an interactive terminal so the caller
// never blocks waiting for input.
func readPipedStdin() (piped bool, data []byte) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false, nil
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return false, nil // a terminal, not a pipe/redirect
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return false, nil
	}
	return true, b
}
