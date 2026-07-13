package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/dap"
)

// RegisterDAP registers all 14 DAP tools onto r. holder is the shared session
// reference populated by dap_connect (or Agent.AttachDAP at startup).
// Tools that need an active session error clearly when holder is empty.
func (r *Registry) RegisterDAP(holder *dap.SessionHolder) {
	for _, t := range []Tool{
		&dapConnectTool{r: r, holder: holder},
		&dapDisconnectTool{holder: holder},
		&dapLaunchTool{holder: holder},
		&dapAttachTool{holder: holder},
		&dapSetBreakpointsTool{holder: holder},
		&dapContinueTool{holder: holder},
		&dapNextTool{holder: holder},
		&dapStepInTool{holder: holder},
		&dapStepOutTool{holder: holder},
		&dapPauseTool{holder: holder},
		&dapStackTraceTool{holder: holder},
		&dapScopesTool{holder: holder},
		&dapVariablesTool{holder: holder},
		&dapEvaluateTool{holder: holder},
	} {
		r.Register(t)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func dapNotConnected() error {
	return fmt.Errorf("not connected to a DAP adapter — call dap_connect first")
}

// stepResult formats the result of a step/continue that ended in a stopped event.
func stepResult(sess *dap.Session, threadID int, reason string) string {
	out := sess.DrainOutput()
	var sb strings.Builder
	fmt.Fprintf(&sb, "stopped: %s (thread %d)", reason, threadID)
	if out != "" {
		sb.WriteString("\n\noutput since last step:\n")
		sb.WriteString(out)
	}
	return sb.String()
}

// ─── 1. dap_connect ──────────────────────────────────────────────────────────

type dapConnectTool struct {
	r      *Registry
	holder *dap.SessionHolder
}

func (t *dapConnectTool) Name() string { return "dap_connect" }
func (t *dapConnectTool) Description() string {
	return "Connect to a running Debug Adapter Protocol server at host:port, sending the mandatory " +
		"initialize handshake. All other DAP tools become available once this succeeds. If " +
		"MOONY_DAP_ADDR is set the connection is established automatically at startup."
}
func (t *dapConnectTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"addr":{"type":"string","description":"TCP address of the DAP adapter, e.g. \"localhost:54321\"."}
	},"required":["addr"]}`)
}
func (t *dapConnectTool) Mutates() bool { return false }
func (t *dapConnectTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Addr string `json:"addr"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Addr == "" {
		return "", fmt.Errorf("addr is required")
	}
	sess, err := dap.Dial(in.Addr)
	if err != nil {
		return "", err
	}
	t.holder.Set(sess)
	t.r.addCloser(func() { _ = sess.Close() })
	return fmt.Sprintf("Connected to DAP adapter at %s and initialized successfully.", in.Addr), nil
}

// ─── 2. dap_disconnect ───────────────────────────────────────────────────────

type dapDisconnectTool struct{ holder *dap.SessionHolder }

func (t *dapDisconnectTool) Name() string { return "dap_disconnect" }
func (t *dapDisconnectTool) Description() string {
	return "Send a disconnect request to the DAP adapter and close the connection, " +
		"optionally terminating the debuggee process."
}
func (t *dapDisconnectTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"terminate_debuggee":{"type":"boolean","description":"Ask the adapter to kill the debuggee process too (default true)."}
	}}`)
}
func (t *dapDisconnectTool) Mutates() bool { return false }
func (t *dapDisconnectTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		TerminateDebuggee *bool `json:"terminate_debuggee"`
	}
	_ = decode(input, &in)
	terminate := true
	if in.TerminateDebuggee != nil {
		terminate = *in.TerminateDebuggee
	}
	_, _ = sess.Send("disconnect", map[string]any{"terminateDebuggee": terminate}, 5*time.Second)
	_ = sess.Close()
	t.holder.Clear()
	return "Disconnected from DAP adapter.", nil
}

// ─── 3. dap_launch ───────────────────────────────────────────────────────────

type dapLaunchTool struct{ holder *dap.SessionHolder }

func (t *dapLaunchTool) Name() string { return "dap_launch" }
func (t *dapLaunchTool) Description() string {
	return "Launch the debug target. Sends the DAP launch request with the given adapter-specific " +
		"arguments, waits for the adapter's 'initialized' event, sends configurationDone, then " +
		"blocks until the debuggee stops (breakpoint, entry, exception) or terminates. " +
		"Common args for dlv: {\"program\": \"./main\", \"stopOnEntry\": true}."
}
func (t *dapLaunchTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"args":{"type":"object","description":"Adapter-specific launch arguments passed verbatim (e.g. {\"program\":\"./main\",\"stopOnEntry\":true})."},
		"breakpoints":{"type":"array","description":"Optional breakpoints to set before configurationDone. Each item: {\"file\": \"path\", \"line\": N}.","items":{"type":"object"}},
		"timeout_secs":{"type":"integer","description":"Seconds to wait for the first stopped/terminated event (default 30)."}
	},"required":["args"]}`)
}
func (t *dapLaunchTool) Mutates() bool { return false }
func (t *dapLaunchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return launchOrAttach(ctx, t.holder, input, "launch")
}

// ─── 4. dap_attach ───────────────────────────────────────────────────────────

type dapAttachTool struct{ holder *dap.SessionHolder }

func (t *dapAttachTool) Name() string { return "dap_attach" }
func (t *dapAttachTool) Description() string {
	return "Attach to an already-running process. Sends the DAP attach request, waits for " +
		"'initialized', sends configurationDone, then blocks until the first stop or termination. " +
		"Common args for dlv: {\"mode\": \"local\", \"processId\": 12345}."
}
func (t *dapAttachTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"args":{"type":"object","description":"Adapter-specific attach arguments (e.g. {\"processId\":12345})."},
		"breakpoints":{"type":"array","description":"Optional breakpoints to set before configurationDone.","items":{"type":"object"}},
		"timeout_secs":{"type":"integer","description":"Seconds to wait for the first stop (default 30)."}
	},"required":["args"]}`)
}
func (t *dapAttachTool) Mutates() bool { return false }
func (t *dapAttachTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return launchOrAttach(ctx, t.holder, input, "attach")
}

// launchOrAttach handles the shared launch+attach flow: send the command, wait
// for the 'initialized' event, optionally set inline breakpoints, send
// configurationDone, then block until the first stopped/terminated event.
func launchOrAttach(ctx context.Context, holder *dap.SessionHolder, input json.RawMessage, command string) (string, error) {
	sess, err := holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		Args        map[string]any   `json:"args"`
		Breakpoints []map[string]any `json:"breakpoints"`
		TimeoutSecs int              `json:"timeout_secs"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.TimeoutSecs <= 0 {
		in.TimeoutSecs = 30
	}
	deadline := time.Duration(in.TimeoutSecs) * time.Second
	tctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	// Send launch/attach; the adapter processes it asynchronously and fires
	// the "initialized" event when ready for configuration.
	resp, err := sess.Send(command, in.Args, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("dap_%s: %w", command, err)
	}
	if !resp.Success {
		return "", fmt.Errorf("dap_%s failed: %s", command, resp.Message)
	}

	// Wait for "initialized" event before setting breakpoints.
	if err := sess.WaitInitialized(tctx); err != nil {
		return "", fmt.Errorf("waiting for initialized event: %w", err)
	}

	// Set any inline breakpoints now (between initialized and configurationDone).
	for _, bp := range groupBreakpointsByFile(in.Breakpoints) {
		if _, err := sess.Send("setBreakpoints", bp, 5*time.Second); err != nil {
			return "", fmt.Errorf("setBreakpoints: %w", err)
		}
	}

	// Signal configuration is complete; the debuggee starts running.
	if _, err := sess.Send("configurationDone", nil, 5*time.Second); err != nil {
		// Not all adapters require configurationDone; ignore errors.
		_ = err
	}

	// Block until the debuggee stops or terminates.
	tid, reason, err := sess.WaitStopped(tctx)
	if err != nil {
		if sess.Terminated() {
			return "Debuggee launched and terminated immediately (no stopped event).\n" + sess.DrainOutput(), nil
		}
		return "", fmt.Errorf("waiting for first stop: %w", err)
	}
	return stepResult(sess, tid, reason), nil
}

// groupBreakpointsByFile converts the flat breakpoints list from tool input
// into the per-source SetBreakpointsArgs the DAP protocol requires.
func groupBreakpointsByFile(bps []map[string]any) []dap.SetBreakpointsArgs {
	byFile := make(map[string][]dap.SourceBreakpoint)
	for _, bp := range bps {
		file, _ := bp["file"].(string)
		if file == "" {
			continue
		}
		var line int
		switch v := bp["line"].(type) {
		case float64:
			line = int(v)
		case int:
			line = v
		}
		cond, _ := bp["condition"].(string)
		byFile[file] = append(byFile[file], dap.SourceBreakpoint{Line: line, Condition: cond})
	}
	var out []dap.SetBreakpointsArgs
	for file, bps := range byFile {
		out = append(out, dap.SetBreakpointsArgs{
			Source:      dap.SourceRef{Path: file},
			Breakpoints: bps,
		})
	}
	return out
}

// ─── 5. dap_set_breakpoints ──────────────────────────────────────────────────

type dapSetBreakpointsTool struct{ holder *dap.SessionHolder }

func (t *dapSetBreakpointsTool) Name() string { return "dap_set_breakpoints" }
func (t *dapSetBreakpointsTool) Description() string {
	return "Set (or replace) all breakpoints in one source file. The adapter verifies each " +
		"breakpoint and reports its actual line; passing an empty lines list clears all " +
		"breakpoints in that file."
}
func (t *dapSetBreakpointsTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"file":{"type":"string","description":"Absolute or relative path to the source file."},
		"lines":{"type":"array","items":{"type":"integer"},"description":"Line numbers for breakpoints (1-based)."},
		"conditions":{"type":"array","items":{"type":"string"},"description":"Optional per-line conditions (same length as lines, or omit/empty for unconditional)."}
	},"required":["file","lines"]}`)
}
func (t *dapSetBreakpointsTool) Mutates() bool { return false }
func (t *dapSetBreakpointsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		File       string   `json:"file"`
		Lines      []int    `json:"lines"`
		Conditions []string `json:"conditions"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.File == "" {
		return "", fmt.Errorf("file is required")
	}
	bps := make([]dap.SourceBreakpoint, len(in.Lines))
	for i, line := range in.Lines {
		bp := dap.SourceBreakpoint{Line: line}
		if i < len(in.Conditions) {
			bp.Condition = in.Conditions[i]
		}
		bps[i] = bp
	}
	args := dap.SetBreakpointsArgs{
		Source:      dap.SourceRef{Path: in.File},
		Breakpoints: bps,
	}
	resp, err := sess.Send("setBreakpoints", args, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("setBreakpoints failed: %s", resp.Message)
	}
	var body dap.SetBreakpointsBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "Breakpoints set (could not parse response).", nil
	}
	var sb strings.Builder
	for i, bp := range body.Breakpoints {
		line := bp.Line
		if line == 0 && i < len(in.Lines) {
			line = in.Lines[i]
		}
		status := "✓ verified"
		if !bp.Verified {
			status = "✗ unverified"
			if bp.Message != "" {
				status += ": " + bp.Message
			}
		}
		fmt.Fprintf(&sb, "%s:%d — %s\n", in.File, line, status)
	}
	if sb.Len() == 0 {
		return "All breakpoints cleared.", nil
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ─── 6. dap_continue ─────────────────────────────────────────────────────────

type dapContinueTool struct{ holder *dap.SessionHolder }

func (t *dapContinueTool) Name() string { return "dap_continue" }
func (t *dapContinueTool) Description() string {
	return "Resume execution of a stopped thread. Blocks until the next stopped event " +
		"(breakpoint, exception, pause) or the timeout expires, then returns the new location."
}
func (t *dapContinueTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Thread to resume. Use the value from the last dap_stack_trace or stopped result."},
		"timeout_secs":{"type":"integer","description":"Maximum seconds to wait for the next stop (default 30)."}
	},"required":["thread_id"]}`)
}
func (t *dapContinueTool) Mutates() bool { return false }
func (t *dapContinueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		ThreadID    int `json:"thread_id"`
		TimeoutSecs int `json:"timeout_secs"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.TimeoutSecs <= 0 {
		in.TimeoutSecs = 30
	}
	sess.ClearStopped()
	resp, err := sess.Send("continue", map[string]any{"threadId": in.ThreadID}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("continue failed: %s", resp.Message)
	}
	tctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSecs)*time.Second)
	defer cancel()
	tid, reason, err := sess.WaitStopped(tctx)
	if err != nil {
		if sess.Terminated() {
			return "Debuggee terminated." + maybeOutput(sess), nil
		}
		return "Execution is still running (timeout reached). Call dap_pause to inspect state, or wait and call dap_stack_trace.", nil
	}
	return stepResult(sess, tid, reason), nil
}

// ─── 7. dap_next (step over) ─────────────────────────────────────────────────

type dapNextTool struct{ holder *dap.SessionHolder }

func (t *dapNextTool) Name() string { return "dap_next" }
func (t *dapNextTool) Description() string {
	return "Step over one statement in the stopped thread (same call depth, skipping into calls). " +
		"Blocks until the next stopped event and returns the new source location + any output."
}
func (t *dapNextTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Stopped thread to step."}
	},"required":["thread_id"]}`)
}
func (t *dapNextTool) Mutates() bool { return false }
func (t *dapNextTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return doStep(ctx, t.holder, input, "next")
}

// ─── 8. dap_step_in ──────────────────────────────────────────────────────────

type dapStepInTool struct{ holder *dap.SessionHolder }

func (t *dapStepInTool) Name() string { return "dap_step_in" }
func (t *dapStepInTool) Description() string {
	return "Step into a function call in the stopped thread. If the current statement is not a " +
		"function call, behaves like dap_next. Returns the new location + output."
}
func (t *dapStepInTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Stopped thread to step."},
		"target_id":{"type":"integer","description":"Optional step-in target ID (from a stepInTargets response) to choose which call to enter."}
	},"required":["thread_id"]}`)
}
func (t *dapStepInTool) Mutates() bool { return false }
func (t *dapStepInTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		ThreadID int `json:"thread_id"`
		TargetID int `json:"target_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	args := map[string]any{"threadId": in.ThreadID}
	if in.TargetID != 0 {
		args["targetId"] = in.TargetID
	}
	return sendStepAndWait(ctx, sess, "stepIn", args)
}

// ─── 9. dap_step_out ─────────────────────────────────────────────────────────

type dapStepOutTool struct{ holder *dap.SessionHolder }

func (t *dapStepOutTool) Name() string { return "dap_step_out" }
func (t *dapStepOutTool) Description() string {
	return "Step out of the current function in the stopped thread, resuming until the caller's " +
		"next statement. Returns the new location + any output generated by the function."
}
func (t *dapStepOutTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Stopped thread to step out of."}
	},"required":["thread_id"]}`)
}
func (t *dapStepOutTool) Mutates() bool { return false }
func (t *dapStepOutTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return doStep(ctx, t.holder, input, "stepOut")
}

// ─── 10. dap_pause ───────────────────────────────────────────────────────────

type dapPauseTool struct{ holder *dap.SessionHolder }

func (t *dapPauseTool) Name() string { return "dap_pause" }
func (t *dapPauseTool) Description() string {
	return "Pause a running thread, suspending execution so you can inspect the stack and variables."
}
func (t *dapPauseTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Thread to pause."}
	},"required":["thread_id"]}`)
}
func (t *dapPauseTool) Mutates() bool { return false }
func (t *dapPauseTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		ThreadID int `json:"thread_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	resp, err := sess.Send("pause", map[string]any{"threadId": in.ThreadID}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("pause failed: %s", resp.Message)
	}
	// Wait briefly for the stopped event that pause triggers.
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tid, reason, err := sess.WaitStopped(tctx)
	if err != nil {
		return "Pause request sent; stopped event not yet received.", nil
	}
	return stepResult(sess, tid, reason), nil
}

// ─── 11. dap_stack_trace ─────────────────────────────────────────────────────

type dapStackTraceTool struct{ holder *dap.SessionHolder }

func (t *dapStackTraceTool) Name() string { return "dap_stack_trace" }
func (t *dapStackTraceTool) Description() string {
	return "Return the call stack for a stopped thread as a formatted list of frames with " +
		"file:line and frame IDs. Use frame IDs with dap_scopes, dap_variables, dap_evaluate."
}
func (t *dapStackTraceTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"thread_id":{"type":"integer","description":"Stopped thread whose stack to retrieve."},
		"levels":{"type":"integer","description":"Maximum number of frames to return (default all)."}
	},"required":["thread_id"]}`)
}
func (t *dapStackTraceTool) Mutates() bool { return false }
func (t *dapStackTraceTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		ThreadID int `json:"thread_id"`
		Levels   int `json:"levels"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	args := dap.StackTraceArgs{ThreadID: in.ThreadID, Levels: in.Levels}
	resp, err := sess.Send("stackTrace", args, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("stackTrace failed: %s", resp.Message)
	}
	var body dap.StackTraceBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("parse stackTrace response: %w", err)
	}
	if len(body.StackFrames) == 0 {
		return "Stack trace is empty.", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-5s  %-40s  %s\n", "frame", "function", "location")
	fmt.Fprintf(&sb, "%-5s  %-40s  %s\n", "-----", "--------", "--------")
	for _, f := range body.StackFrames {
		loc := ""
		if f.Source != nil && f.Source.Path != "" {
			loc = fmt.Sprintf("%s:%d", f.Source.Path, f.Line)
		} else if f.Line > 0 {
			loc = fmt.Sprintf("line %d", f.Line)
		}
		fmt.Fprintf(&sb, "%-5d  %-40s  %s\n", f.ID, f.Name, loc)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ─── 12. dap_scopes ──────────────────────────────────────────────────────────

type dapScopesTool struct{ holder *dap.SessionHolder }

func (t *dapScopesTool) Name() string { return "dap_scopes" }
func (t *dapScopesTool) Description() string {
	return "List the variable scopes (e.g. Locals, Globals, Closure) available for a stack frame. " +
		"Each scope has a variablesReference you pass to dap_variables to expand its contents."
}
func (t *dapScopesTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"frame_id":{"type":"integer","description":"Frame ID from a dap_stack_trace result."}
	},"required":["frame_id"]}`)
}
func (t *dapScopesTool) Mutates() bool { return false }
func (t *dapScopesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		FrameID int `json:"frame_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	resp, err := sess.Send("scopes", dap.ScopesArgs{FrameID: in.FrameID}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("scopes failed: %s", resp.Message)
	}
	var body dap.ScopesBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("parse scopes response: %w", err)
	}
	if len(body.Scopes) == 0 {
		return "No scopes available for this frame.", nil
	}
	var sb strings.Builder
	for _, s := range body.Scopes {
		expensive := ""
		if s.Expensive {
			expensive = " (expensive)"
		}
		fmt.Fprintf(&sb, "%-20s  ref=%-6d%s\n", s.Name, s.VariablesReference, expensive)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ─── 13. dap_variables ───────────────────────────────────────────────────────

type dapVariablesTool struct{ holder *dap.SessionHolder }

func (t *dapVariablesTool) Name() string { return "dap_variables" }
func (t *dapVariablesTool) Description() string {
	return "List variables in a scope or nested variable reference returned by dap_scopes or a " +
		"prior dap_variables call. Pass a non-zero variablesReference to expand a structured value."
}
func (t *dapVariablesTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"variables_reference":{"type":"integer","description":"VariablesReference from a dap_scopes or dap_variables result."},
		"filter":{"type":"string","enum":["indexed","named"],"description":"'indexed' for array elements, 'named' for named children; omit for both."}
	},"required":["variables_reference"]}`)
}
func (t *dapVariablesTool) Mutates() bool { return false }
func (t *dapVariablesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		VariablesReference int    `json:"variables_reference"`
		Filter             string `json:"filter"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	args := dap.VariablesArgs{
		VariablesReference: in.VariablesReference,
		Filter:             in.Filter,
	}
	resp, err := sess.Send("variables", args, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("variables failed: %s", resp.Message)
	}
	var body dap.VariablesBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("parse variables response: %w", err)
	}
	if len(body.Variables) == 0 {
		return "No variables.", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-30s  %-12s  %s\n", "name", "type", "value")
	fmt.Fprintf(&sb, "%-30s  %-12s  %s\n", "----", "----", "-----")
	for _, v := range body.Variables {
		nested := ""
		if v.VariablesReference > 0 {
			nested = fmt.Sprintf("  (ref=%d)", v.VariablesReference)
		}
		fmt.Fprintf(&sb, "%-30s  %-12s  %s%s\n", v.Name, v.Type, v.Value, nested)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ─── 14. dap_evaluate ────────────────────────────────────────────────────────

type dapEvaluateTool struct{ holder *dap.SessionHolder }

func (t *dapEvaluateTool) Name() string { return "dap_evaluate" }
func (t *dapEvaluateTool) Description() string {
	return "Evaluate an expression in the context of a stopped frame. " +
		"Use context 'repl' for side-effect expressions, 'watch' for inspecting values (default), " +
		"or 'hover' for tooltip-style type info."
}
func (t *dapEvaluateTool) Schema() json.RawMessage {
	return schema(`{"type":"object","properties":{
		"expression":{"type":"string","description":"Expression to evaluate (Go syntax, Python, JS, etc. depending on adapter)."},
		"frame_id":{"type":"integer","description":"Frame ID from dap_stack_trace; 0 or omit for the global context."},
		"context":{"type":"string","enum":["watch","repl","hover","variables"],"description":"Evaluation context (default 'watch')."}
	},"required":["expression"]}`)
}
func (t *dapEvaluateTool) Mutates() bool { return false }
func (t *dapEvaluateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	sess, err := t.holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		Expression string `json:"expression"`
		FrameID    int    `json:"frame_id"`
		Context    string `json:"context"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if in.Expression == "" {
		return "", fmt.Errorf("expression is required")
	}
	if in.Context == "" {
		in.Context = "watch"
	}
	args := dap.EvaluateArgs{
		Expression: in.Expression,
		FrameID:    in.FrameID,
		Context:    in.Context,
	}
	resp, err := sess.Send("evaluate", args, 10*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("evaluate failed: %s", resp.Message)
	}
	var body dap.EvaluateBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return "", fmt.Errorf("parse evaluate response: %w", err)
	}
	result := body.Result
	if body.Type != "" {
		result = fmt.Sprintf("%s  (%s)", body.Result, body.Type)
	}
	if body.VariablesReference > 0 {
		result += fmt.Sprintf("  [expandable ref=%d]", body.VariablesReference)
	}
	return result, nil
}

// ─── shared step helpers ─────────────────────────────────────────────────────

// doStep handles the common case for next/stepOut: parse thread_id, clear
// stopped state, send the command, wait for the next stopped event.
func doStep(ctx context.Context, holder *dap.SessionHolder, input json.RawMessage, command string) (string, error) {
	sess, err := holder.Get()
	if err != nil {
		return "", dapNotConnected()
	}
	var in struct {
		ThreadID int `json:"thread_id"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	return sendStepAndWait(ctx, sess, command, map[string]any{"threadId": in.ThreadID})
}

// sendStepAndWait sends a step command and blocks until the next stopped event.
func sendStepAndWait(ctx context.Context, sess *dap.Session, command string, args any) (string, error) {
	sess.ClearStopped()
	resp, err := sess.Send(command, args, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("dap_%s: %w", command, err)
	}
	if !resp.Success {
		return "", fmt.Errorf("dap_%s failed: %s", command, resp.Message)
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tid, reason, err := sess.WaitStopped(tctx)
	if err != nil {
		if sess.Terminated() {
			return "Debuggee terminated." + maybeOutput(sess), nil
		}
		return "", fmt.Errorf("%s: waiting for stopped event: %w", command, err)
	}
	return stepResult(sess, tid, reason), nil
}

func maybeOutput(sess *dap.Session) string {
	if out := sess.DrainOutput(); out != "" {
		return "\n\noutput:\n" + out
	}
	return ""
}
