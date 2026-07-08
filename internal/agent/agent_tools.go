// agent_tools.go — tool-call dispatch, read dedup, parallel execution, and approval-gated runs.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/hooks"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/tools"
)

// runToolCalls executes every tool call the model requested this turn and
// returns the tool_result blocks in the original order. Independent read-only
// calls (and, by design, sub-agent `task` calls) in a contiguous run are
// executed concurrently; mutating file/shell tools act as barriers and run
// one at a time, in place, so ordering and approvals are preserved. Approvals
// always happen serially (in approveToolCall) before any concurrent execution
// begins, so the UI never faces two prompts at once.
func (a *Agent) runToolCalls(ctx context.Context, emit func(Event), calls []llm.ContentBlock) []llm.ContentBlock {
	results := make([]llm.ContentBlock, len(calls))

	// Short-circuit duplicate idempotent reads (breaks the stale-result
	// re-read treadmill). skip is in/out — dedup fills slots not already
	// handled. skip[i] true means results[i] is already filled and the call
	// must not run.
	skip := make([]bool, len(calls))
	a.dedupReadCalls(calls, results, skip, emit)

	enabled, limit := parallelToolConfig()
	if !enabled || len(calls) == 1 {
		for i, call := range calls {
			if skip[i] {
				continue
			}
			results[i] = a.runToolCall(ctx, emit, call)
		}
		a.dedupRepeatedResults(calls, results)
		return results
	}

	for i := 0; i < len(calls); {
		if skip[i] {
			i++
			continue
		}
		if !a.parallelSafe(calls[i].Name) {
			results[i] = a.runToolCall(ctx, emit, calls[i])
			i++
			continue
		}
		// Gather the contiguous run of parallel-safe calls.
		j := i
		for j < len(calls) && a.parallelSafe(calls[j].Name) {
			j++
		}
		if j-i == 1 {
			if !skip[i] {
				results[i] = a.runToolCall(ctx, emit, calls[i])
			}
		} else {
			a.runConcurrent(ctx, emit, calls[i:j], results[i:j], skip[i:j], limit)
		}
		i = j
	}
	a.dedupRepeatedResults(calls, results)
	return results
}

// dedupRepeatedResults collapses a freshly produced tool result whose body is
// byte-identical to a large result still present in recent history into a short
// pointer stub. This catches cross-tool repeats the per-tool caches miss (e.g.
// git_diff then a bash "git diff", or the same directory listing) without losing
// anything: only exact matches above the size floor are collapsed, and only when
// the identical output is still visible above in the conversation.
func (a *Agent) dedupRepeatedResults(calls, results []llm.ContentBlock) {
	const (
		minRepeatDedupBytes = 800
		historyTail         = 80
	)
	seen := make(map[string]bool)
	msgs := a.messages
	if len(msgs) > historyTail {
		msgs = msgs[len(msgs)-historyTail:]
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.ContentList == nil && len(b.Content) >= minRepeatDedupBytes {
				seen[b.Content] = true
			}
		}
	}
	for i := range results {
		r := &results[i]
		if r.Type != llm.BlockToolResult || r.IsError || r.ContentList != nil || len(r.Content) < minRepeatDedupBytes {
			continue
		}
		if seen[r.Content] {
			r.Content = repeatedResultStub(callName(calls, r.ToolUseID))
		} else {
			seen[r.Content] = true // also collapse a later identical result within this same batch
		}
	}
}

func callName(calls []llm.ContentBlock, id string) string {
	for _, c := range calls {
		if c.Type == llm.BlockToolUse && c.ID == id {
			return c.Name
		}
	}
	return "tool"
}

func repeatedResultStub(name string) string {
	return fmt.Sprintf("(this %s call produced output byte-for-byte identical to an earlier tool result still shown above in this conversation — omitted here to save context; scroll up for the full output)", name)
}

// idempotentReadTools are read-only tools whose output is a pure function of the
// current filesystem/repo state — re-running one with identical arguments (with
// no intervening mutation) yields the same result, so a repeat within a turn can
// be skipped. Deliberately excludes bash (side effects), task/delegate (do new
// work), and network tools (time-varying).
var idempotentReadTools = map[string]bool{
	"read_file":       true,
	"grep":            true,
	"search_code":     true,
	"glob":            true,
	"list_dir":        true,
	"find_symbol":     true,
	"read_symbol":     true,
	"find_references": true,
	"git_status":      true,
	"git_diff":        true,
	"git_log":         true,
	"git_branch":      true,
}

// dedupReadCalls scans a step's tool calls in order and short-circuits any
// idempotent read whose identical (name+input) call already ran this turn since
// the last mutation AND whose result is still in context: it fills results[i]
// with a cheap stub pointing at that result and marks skip[i], so the (possibly
// large) output isn't re-executed or re-sent. When pruneStaleToolResults elides
// a read's result it drops that read's entry from the seen-set, so a re-read of
// an elided result is NOT short-circuited — it re-executes and re-serves the
// data (breaking the prune/dedup re-read treadmill). A mutating call clears the
// seen-set, since a write may change what a re-read would return. A no-op when
// pruning is disabled (no treadmill to break). skip is in/out: indices already
// marked are left untouched and NOT recorded as seen — they never executed, so
// a later identical read must still be able to run.
func (a *Agent) dedupReadCalls(calls []llm.ContentBlock, results []llm.ContentBlock, skip []bool, emit func(Event)) {
	if a.cfg.ToolResultKeepRounds <= 0 {
		return
	}
	if a.turnReadCalls == nil {
		a.turnReadCalls = map[string]bool{}
	}
	for i, c := range calls {
		if skip[i] {
			continue // already handled — don't record or re-handle
		}
		if a.tools.Mutates(c.Name) {
			a.turnReadCalls = map[string]bool{} // a mutation may change any read's result
			if c.Name != "task" {
				a.clearTaskCache() // a workspace change can invalidate cached explorations
			}
			continue
		}
		if !idempotentReadTools[c.Name] {
			continue
		}
		key := c.Name + "\x00" + string(c.Input)
		if a.turnReadCalls[key] {
			msg := fmt.Sprintf("[skipped re-running %s: it was already called with these exact arguments this turn and its result is still in the conversation above — reuse that result instead of re-reading.]", c.Name)
			emit(ToolStart{ID: c.ID, Name: c.Name, Input: string(c.Input)})
			emit(ToolEnd{ID: c.ID, Name: c.Name, Output: msg})
			results[i] = llm.ToolResultBlock(c.ID, msg, false)
			skip[i] = true
			continue
		}
		a.turnReadCalls[key] = true
	}
}

// readToolCallIDs returns the tool_use IDs of the read-style calls in a batch,
// used to measure how much read output a turn is accumulating for the
// auto-offload heuristic.
func readToolCallIDs(calls []llm.ContentBlock) map[string]bool {
	m := make(map[string]bool)
	for _, c := range calls {
		if c.Type == llm.BlockToolUse && idempotentReadTools[c.Name] {
			m[c.ID] = true
		}
	}
	return m
}

// readOnlyToolBatch returns true iff the batch is non-empty and every tool call
// is an idempotent read tool (grep, read_file, find_symbol, …). Used by the
// cheap-reads model-routing logic: a round whose previous step was pure
// exploration is routed to the cheaper FastModel for the next call.
func readOnlyToolBatch(calls []llm.ContentBlock) bool {
	n := 0
	for _, c := range calls {
		if c.Type != llm.BlockToolUse {
			continue
		}
		n++
		if !idempotentReadTools[c.Name] {
			return false
		}
	}
	return n > 0
}

// runConcurrent runs a batch of parallel-safe calls. Approval/preview happens
// serially first (phase 1) so prompts stay one-at-a-time; the approved calls
// then execute concurrently (phase 2), each writing its result into its own
// slot so order is preserved. limit caps concurrency. skip[k] marks a call whose
// result is already filled (a short-circuited duplicate read) — it is neither
// approved nor executed.
func (a *Agent) runConcurrent(ctx context.Context, emit func(Event), calls, out []llm.ContentBlock, skip []bool, limit int) {
	proceed := make([]bool, len(calls))
	for k, call := range calls {
		if skip[k] {
			continue
		}
		ok, denial := a.approveToolCall(ctx, emit, call)
		proceed[k] = ok
		if !ok {
			out[k] = denial
		}
	}

	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for k := range calls {
		if !proceed[k] {
			continue
		}
		k, call := k, calls[k]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[k] = a.executeApprovedCall(ctx, emit, call)
		}()
	}
	wg.Wait()
}

// parallelSafe reports whether a call may run concurrently with other
// parallel-safe calls this turn. Read-only tools qualify; the `task` sub-agent
// and `delegate` worker are included so independent agents run in parallel.
// (delegate dispatches to an out-of-process worker; the worker.Manager
// serializes same-worker dispatches, so concurrent delegates are safe even when
// they target the same worker.) todo_write is excluded (it's non-mutating but
// writes the shared task list), as are mutating file/shell tools (ordering +
// approval make them barriers).
func (a *Agent) parallelSafe(name string) bool {
	switch name {
	case "task", "delegate":
		return true
	case "todo_write":
		return false
	case "ask_user":
		// Non-mutating, but it blocks on a UI round-trip and the host holds a
		// single pending-question slot; run it serially so prompts never overlap.
		return false
	case "exit_plan":
		// Non-mutating, but it blocks on a UI confirm round-trip (reusing the
		// question slot) and flips plan mode; run it serially like ask_user.
		return false
	case "remember":
		// Non-mutating, but it writes the shared AGENTS.md + rebuilds the system
		// prompt, so run it serially rather than concurrently.
		return false
	}
	return !a.tools.Mutates(name)
}

// runToolCall previews, optionally gates, executes a single tool call and
// returns the tool_result block for the model. It is the sequential path:
// approve then execute.
func (a *Agent) runToolCall(ctx context.Context, emit func(Event), call llm.ContentBlock) llm.ContentBlock {
	proceed, denial := a.approveToolCall(ctx, emit, call)
	if !proceed {
		return denial
	}
	return a.executeApprovedCall(ctx, emit, call)
}

// approveToolCall handles the user-facing, must-be-serial half of a tool call:
// the plan-mode gate, change preview, ToolStart event, and approval prompt. It
// returns (true, _) when the call is cleared to run, or (false, result) with a
// ready-made denial/blocked tool_result otherwise. Because this never runs
// concurrently, approval prompts are always presented one at a time.
func (a *Agent) approveToolCall(ctx context.Context, emit func(Event), call llm.ContentBlock) (bool, llm.ContentBlock) {
	// Defense in depth: refuse a tool the active profile DENIES (e.g. a plan
	// agent's write/edit/task), or any mutating built-in while a drafted plan is
	// still awaiting approval. The draft case matters because the model can
	// produce a plan via plan_write during a normal build-mode turn and would
	// otherwise execute it immediately, without the user approving.
	if a.activeAgent().Rule(call.Name) == config.PermDeny {
		msg := "The active agent \"" + a.activeAgent().Name + "\" disables the " + call.Name + " tool. Ask the user to switch agents (/agent <name>) before any such action."
		if a.PlanMode() {
			msg = "Plan mode is enabled; the " + call.Name + " tool is disabled. Ask the user to run /build before any change."
		}
		emit(ToolStart{ID: call.ID, Name: call.Name, Input: string(call.Input), Mutating: true})
		emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
		return false, llm.ToolResultBlock(call.ID, msg, true)
	}
	if a.hasDraftPlan() && planModeBlocked[call.Name] {
		msg := "A draft plan is awaiting the user's approval; the " + call.Name + " tool is disabled until the plan is approved. Call exit_plan (or ask the user to run /plan approve) and wait — do not edit files or run commands yet."
		emit(ToolStart{ID: call.ID, Name: call.Name, Input: string(call.Input), Mutating: true})
		emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
		return false, llm.ToolResultBlock(call.ID, msg, true)
	}

	mutating := a.tools.Mutates(call.Name)

	// Consult persisted permission rules before anything interactive. A deny
	// rule blocks the call outright (even under --no-approval); an allow rule
	// pre-approves it so no prompt is shown.
	permDecision := permissions.Default
	if a.perms != nil {
		var cmd string
		if call.Name == "bash" {
			cmd = bashCommandField(call.Input)
		}
		permDecision = a.perms.Check(call.Name, cmd)
	}
	if permDecision == permissions.Deny {
		msg := "A permission rule denies the " + call.Name + " tool for this call. Do not retry; ask the user how to proceed."
		emit(ToolStart{ID: call.ID, Name: call.Name, Input: string(call.Input), Mutating: mutating})
		emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
		return false, llm.ToolResultBlock(call.ID, msg, true)
	}

	var diff, summary string
	if prev, ok := a.tools.Preview(ctx, call.Name, call.Input); ok {
		diff, summary = prev.Diff, prev.Summary
	}

	// For a delegation, resolve the sub-agent's model so the UI can show it on
	// the task card (which persona/model the work is running on).
	var model string
	if call.Name == "task" {
		if tt, ok := a.tools.Get("task").(*taskTool); ok {
			model = tt.ResolveModel(call.Input)
		}
	}

	emit(ToolStart{
		ID:       call.ID,
		Name:     call.Name,
		Input:    string(call.Input),
		Diff:     diff,
		Summary:  summary,
		Mutating: mutating,
		Model:    model,
	})

	// If a permissions file existed but couldn't be parsed, we can't trust the
	// absence of a matching deny rule. Fail closed: never auto-run a mutating tool
	// on untrusted rules — require interactive approval, or deny outright when
	// there's no interactive user (headless run / sub-agent). Read-only tools are
	// unaffected. This deliberately overrides the danger-skip/auto short-circuits
	// below.
	if mutating && a.perms != nil && a.perms.Malformed() {
		if !a.interactive {
			msg := "The permissions file is malformed, so deny rules can't be trusted; refusing to auto-run the mutating " + call.Name + " tool. Fix permissions.json (see /permissions) and retry."
			emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
			return false, llm.ToolResultBlock(call.ID, msg, true)
		}
		if a.requestApproval(ctx, emit, call.Name, summary, diff) == Deny {
			msg := "User denied execution of this tool. Do not retry; ask the user how to proceed."
			emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
			return false, llm.ToolResultBlock(call.ID, msg, true)
		}
		return true, llm.ContentBlock{}
	}

	// danger-skip-permission: auto-approve every remaining gate, including the
	// git commit/push hard gate below. The structural gates above still apply —
	// plan mode / a pending plan draft still block edits until the plan is
	// approved via exit_plan, and an explicit deny rule already blocked — so
	// plan-draft approval remains a required user action.
	if config.NormalizeApprovalPolicy(a.cfg.ApprovalPolicy) == config.ApprovalDangerSkip {
		return true, llm.ContentBlock{}
	}

	// Hard gate: git commit/push must ALWAYS be confirmed by the user, ignoring
	// the approval policy (auto/--no-approval) and any allow/always-allow rule.
	// A Deny permission rule has already blocked above. With no interactive
	// approver (headless run, sub-agent) the action is denied, never auto-run.
	if hardApproval(call.Name, call.Input) {
		if !a.interactive {
			msg := "The " + call.Name + " call requires explicit user approval (git commit/push) and cannot run without an interactive user. Ask the user to perform or approve it."
			emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
			return false, llm.ToolResultBlock(call.ID, msg, true)
		}
		// Always prompt; do NOT honor "always allow" for these (ApproveAlways is
		// treated as a one-time Approve), so every commit/push is re-confirmed.
		if a.requestApproval(ctx, emit, call.Name, summary, diff) == Deny {
			msg := "User denied execution of this tool. Do not retry; ask the user how to proceed."
			emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
			return false, llm.ToolResultBlock(call.ID, msg, true)
		}
		return true, llm.ContentBlock{}
	}

	// A profile that marks this tool "ask" (e.g. a plan agent's bash) must
	// prompt even when the approval policy wouldn't (auto) and even for a
	// non-mutating tool — unless a persisted allow rule already cleared it.
	profileAsk := a.activeAgent().Rule(call.Name) == config.PermAsk
	if permDecision != permissions.Allow && (profileAsk || a.needsApproval(call.Name, call.Input)) {
		// Key the "always allow" memory + persisted rule by what the user is
		// actually approving: the tool name in general, but the SPECIFIC command
		// for bash (a "bash:<command>" prefix rule). Approving one shell command
		// must never blanket-allow every future bash call.
		rule := alwaysAllowRule(call.Name, call.Input)
		if rule == "" || !a.alwaysAllow[rule] {
			switch a.requestApproval(ctx, emit, call.Name, summary, diff) {
			case ApproveAlways:
				if rule != "" {
					a.alwaysAllow[rule] = true
					// Persist as a project-scoped allow rule so it survives restarts
					// and resumed sessions (equivalent to "/permissions allow <rule>").
					// Surface a notice if it can't be written — the in-memory memory
					// above still suppresses re-prompts for the rest of this session.
					if a.perms != nil {
						if err := a.perms.Add(rule, false, permissions.Project); err != nil {
							emit(Info{Text: "Could not persist the always-allow rule \"" + rule + "\": " + err.Error() + " — it applies for this session only."})
						}
					}
				}
			case Deny:
				msg := "User denied execution of this tool. Do not retry; ask the user how to proceed."
				emit(ToolEnd{ID: call.ID, Name: call.Name, Output: msg, IsError: true, Denied: true})
				return false, llm.ToolResultBlock(call.ID, msg, true)
			}
		}
	}
	return true, llm.ContentBlock{}
}

// alwaysAllowRule returns the permission rule (and in-memory key) to remember
// when the user picks "always allow" for a call. For every tool except bash that
// is just the tool name. For bash it's a command-scoped "bash:<command>" prefix
// rule, so approving one shell command never silently allows every future bash
// call — the blanket grant a bare "bash" rule would give. It returns "" when
// there's nothing safe to remember (an empty bash command), so that call is
// allowed for this turn only and nothing is persisted.
func alwaysAllowRule(name string, input json.RawMessage) string {
	if name != "bash" {
		return name
	}
	cmd := strings.TrimSpace(bashCommandField(input))
	if cmd == "" {
		return ""
	}
	return "bash:" + cmd
}

// needsApproval reports whether a tool call must be confirmed by the user under
// the current approval policy. Read-only tools never need approval. Under
// "safe", read-only bash commands (per config.SafeBashCommand) run unconfirmed;
// under "auto" nothing is confirmed.
func (a *Agent) needsApproval(name string, input json.RawMessage) bool {
	if !a.tools.Mutates(name) {
		return false
	}
	switch config.NormalizeApprovalPolicy(a.cfg.ApprovalPolicy) {
	case config.ApprovalAuto, config.ApprovalDangerSkip:
		return false
	case config.ApprovalSafe:
		if name == "bash" && config.SafeBashCommand(bashCommandField(input)) {
			return false
		}
		return true
	default: // ApprovalPrompt
		return true
	}
}

// bashCommandField extracts the "command" string from a bash tool call's JSON
// input, returning "" when it can't be parsed.
func bashCommandField(input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Command
}

// hardApproval reports whether a call is a high-stakes action that must ALWAYS
// be confirmed by the user — regardless of the approval policy (including auto /
// --no-approval) and regardless of any persisted/"always-allow" rule. These are
// irreversible or remote-publishing git operations: a commit, or a push (run
// either via the git_commit tool or a `git commit` / `git push` shell command).
func hardApproval(name string, input json.RawMessage) bool {
	switch name {
	case "git_commit":
		return true
	case "bash":
		return bashRunsGitCommitOrPush(bashCommandField(input))
	}
	return false
}

// HardApprovalRequired reports whether a tool call is subject to the git
// commit/push hard gate — it must always be user-confirmed and is never auto-run
// without an interactive approver. Exported for out-of-loop executors (e.g. the
// workflow runner) that invoke tools outside the normal agent approval loop.
func HardApprovalRequired(name string, input json.RawMessage) bool {
	return hardApproval(name, input)
}

// bashRunsGitCommitOrPush reports whether a shell command invokes `git commit`
// or `git push`. It scans every token, and for each `git` token finds the
// subcommand (skipping global flags like -C <path> / -c <k=v>), so it matches
// "git push", "git commit -m …", "git -C repo commit", and chained forms like
// "git add -A && git commit", while ignoring read-only git (status/log/diff).
func bashRunsGitCommitOrPush(cmd string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != "git" {
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			t := fields[j]
			if strings.HasPrefix(t, "-") {
				if t == "-C" || t == "-c" {
					j++ // these global flags take a value; skip it too
				}
				continue
			}
			if t == "push" || t == "commit" {
				return true
			}
			break // some other git subcommand (status, log, …)
		}
	}
	return false
}

// executeApprovedCall runs the side-effecting half of a tool call: pre-tool
// hook, undo snapshot, execution, LSP diagnostics, the ToolEnd event, and the
// post-tool hook. It assumes approval already happened and is safe to call from
// a goroutine — it touches only per-call data, the (read-only) registry, and
// the concurrency-safe emit channel and llm client.
func (a *Agent) executeApprovedCall(ctx context.Context, emit func(Event), call llm.ContentBlock) llm.ContentBlock {
	if a.hooks != nil {
		_ = a.hooks.Run(ctx, hooks.PreTool, hooks.Context{
			WorkingDir: a.cfg.WorkingDir,
			ToolName:   call.Name,
			ToolInput:  string(call.Input),
		})
	}

	// Snapshot the prior contents of any file this tool is about to mutate, so
	// /undo can revert just this turn's writes. (Only mutating file tools, which
	// run on the serial path, ever produce snapshot paths.)
	for _, p := range snapshotPaths(call.Name, call.Input) {
		a.SnapshotPath(p)
	}

	// Let streaming tools (delegate) report live progress: stash an emitter in
	// the context, tagged with this call's id so the UI can nest the activity.
	ctx = withWorkerActivity(ctx, func(worker, kind, text string) {
		emit(WorkerActivity{ID: call.ID, Worker: worker, Kind: kind, Text: text})
	})

	// Let the ask_user tool round-trip a question to the user: stash an asker in
	// the context that emits a QuestionRequest and blocks for the answer. ask_user
	// is excluded from parallel execution (see parallelSafe), so this blocks the
	// serial path one question at a time, exactly like an approval prompt.
	ctx = tools.WithUserAsker(ctx, func(ctx context.Context, req tools.AskRequest) ([]string, error) {
		return a.requestQuestion(ctx, emit, req)
	})

	// The ask_user questions[] form asks several questions in one call: stash a
	// batch asker that, on a grouped-panel surface, collects every answer in a
	// single round-trip (else it asks them one at a time — see requestQuestionGroup).
	ctx = tools.WithUserBatchAsker(ctx, func(ctx context.Context, reqs []tools.AskRequest) ([][]string, error) {
		return a.requestQuestionGroup(ctx, emit, reqs)
	})

	// Let the exit_plan tool round-trip a build-mode confirmation: stash a
	// confirmer that prompts the user (reusing the question UI) and, on approval,
	// approves the plan and leaves read-only plan mode so this same turn flows
	// into execution with the mutating tools re-enabled.
	ctx = tools.WithExitPlanConfirmer(ctx, func(ctx context.Context, req tools.ExitPlanRequest) (bool, error) {
		return a.requestExitPlan(ctx, emit, req)
	})

	// Give the task tool access to this turn's UI event sink and the executing
	// call's ID so a delegated sub-agent's tool calls can be forwarded nested
	// under this call (see taskTool.Execute). Harmless for every other tool.
	ctx = withTaskEmitter(ctx, emit, call.ID)

	output, images, err := a.tools.ExecuteImages(ctx, call.Name, call.Input)
	isErr := err != nil
	if isErr {
		output = "Error: " + err.Error()
		images = nil
	}
	output = a.capToolOutput(call.Name, call.Input, output, a.cfg.MaxToolOutputBytes)

	// Friction signal for the "auto" reasoning ramp (see rampedAutoEffort): a tool
	// that actually failed (isErr), or a bash command that exited non-zero — bash
	// reports that in-band with a trailing "[exit error:" note rather than IsError.
	// Benign non-zero exits (grep "no match", diff "differ", test "false") are
	// EXCLUDED (benignBashNonzeroExit): they're routine control flow, not failures,
	// and counting them spuriously raised reasoning ("hitting friction") on turns
	// whose tools all finished cleanly. Counted AFTER approval, so policy denials /
	// plan-mode blocks never inflate it. Atomic: read-only tools execute concurrently.
	if isErr || (call.Name == "bash" && strings.Contains(output, "[exit error:") && !benignBashNonzeroExit(call.Input)) {
		a.turnFriction.Add(1)
	}

	// After a successful file write, run configured formatters in place so the
	// change lands already formatted. This happens before diagnostics below so
	// the LSP sees the formatted code. Formatter failures append a short note
	// but never block the agent.
	if !isErr && !a.format.Empty() {
		for _, rel := range snapshotPaths(call.Name, call.Input) {
			abs := rel
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(a.cfg.WorkingDir, rel)
			}
			if note := a.format.FormatFile(ctx, abs); note != "" {
				output += note
			}
		}
	}

	// After a successful file write, ask the LSP manager for diagnostics on
	// each changed path and append them so the model sees fresh errors without
	// an extra tool round-trip. Read failures here are non-fatal.
	if !isErr && a.lsp != nil {
		for _, tgt := range diagnosticTargets(a.cfg.WorkingDir, call.Name, call.Input) {
			if diag := a.lsp.DiagnosticsFor(ctx, tgt.path, tgt.content); diag != "" {
				output += diag
			}
		}
	}

	toolImages := make([]ToolImage, 0, len(images))
	for _, im := range images {
		toolImages = append(toolImages, ToolImage{MediaType: im.MediaType, Data: im.Data})
	}
	emit(ToolEnd{ID: call.ID, Name: call.Name, Output: output, IsError: isErr, Images: toolImages})

	if a.hooks != nil {
		hctx := hooks.Context{
			WorkingDir: a.cfg.WorkingDir,
			ToolName:   call.Name,
			ToolInput:  string(call.Input),
			ToolOutput: output,
		}
		if isErr {
			hctx.ToolError = output
		}
		_ = a.hooks.Run(ctx, hooks.PostTool, hctx)
	}
	// When the tool produced images (e.g. a browser screenshot), forward them to
	// the model as image blocks alongside the text, so it can actually see them.
	if len(images) > 0 {
		blocks := make([]llm.ContentBlock, 0, len(images)+1)
		if strings.TrimSpace(output) != "" {
			blocks = append(blocks, llm.TextBlock(output))
		}
		for _, im := range images {
			blocks = append(blocks, llm.ImageBlock(im.MediaType, base64.StdEncoding.EncodeToString(im.Data)))
		}
		return llm.ToolResultBlocks(call.ID, blocks, isErr)
	}
	return llm.ToolResultBlock(call.ID, output, isErr)
}

// benignBashNonzeroExit reports whether a bash command whose exit was non-zero is a
// conventionally-benign one: grep/rg exit 1 on "no match", diff/cmp on "differ",
// test/[ on "false" — routine control flow, not a failure. Such exits must NOT count
// as friction for the auto-reasoning ramp; counting them raised reasoning on turns
// where every tool actually finished fine (bash reports a non-zero exit in-band, so
// its tool result is not marked IsError and the UI shows it finished). Only the
// command's leading token is inspected — enough for the common `grep foo file` case.
func benignBashNonzeroExit(input json.RawMessage) bool {
	var b struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &b) != nil {
		return false
	}
	cmd := strings.TrimSpace(b.Command)
	if i := strings.IndexAny(cmd, " \t\n"); i >= 0 {
		cmd = cmd[:i] // leading token
	}
	if j := strings.LastIndexByte(cmd, '/'); j >= 0 {
		cmd = cmd[j+1:] // strip any path (/usr/bin/grep -> grep)
	}
	switch cmd {
	case "grep", "rg", "egrep", "fgrep", "ag", "ack", "diff", "cmp", "test", "[":
		return true
	}
	return false
}

// defaultParallelToolLimit bounds how many parallel-safe tool calls run at once
// when the model requests a batch, keeping goroutine/fd use sane.
const defaultParallelToolLimit = 8
