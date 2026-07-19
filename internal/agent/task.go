package agent

// taskTool dispatches a focused subtask to a named persona (sub-agent), runs an
// isolated agent loop with that persona's system prompt and tool subset, and
// returns the assistant's final answer as the tool's output. This is how the
// parent agent delegates exploration ("send the explore agent to find …") or
// scoped work ("ask the planner to outline …") without polluting its own
// context.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

type taskTool struct {
	parent       *Agent
	personas     []subagents.Persona
	personasByID map[string]subagents.Persona
}

// newTaskTool constructs the tool. With no personas configured the tool's
// description tells the model only the default agent is available. Only
// subagent-capable personas are delegation targets — a "primary" profile (e.g. a
// customized build/plan) is switch-only and excluded.
func newTaskTool(parent *Agent, personas []subagents.Persona) *taskTool {
	delegatable := make([]subagents.Persona, 0, len(personas))
	for _, p := range personas {
		if p.SubagentCapable() {
			delegatable = append(delegatable, p)
		}
	}
	personas = delegatable
	byID := make(map[string]subagents.Persona, len(personas))
	for _, p := range personas {
		byID[p.Name] = p
	}
	return &taskTool{parent: parent, personas: personas, personasByID: byID}
}

func (t *taskTool) Name() string { return "task" }

func (t *taskTool) Description() string {
	if len(t.personas) == 0 {
		return "Delegate a self-contained subtask to a sub-agent. " +
			"No custom personas are configured, so this tool currently has no agents to dispatch to. " +
			"Add a persona at .sqe/agents/<name>.md to enable it."
	}
	var b strings.Builder
	b.WriteString("Delegate a self-contained subtask to a sub-agent. The sub-agent runs in an isolated context with its own system prompt and tool subset. ")
	b.WriteString("Use this for focused exploration or planning where you want a single concise answer back without filling the parent's history. ")
	b.WriteString("For editing work you can fan out several in parallel over INDEPENDENT files by passing isolate=true (each runs in its own throwaway git worktree and returns a diff you then integrate). ")
	b.WriteString("Available agents:\n")
	for _, p := range t.personas {
		desc := p.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", p.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *taskTool) Schema() json.RawMessage {
	names := make([]string, 0, len(t.personas))
	for _, p := range t.personas {
		names = append(names, fmt.Sprintf("%q", p.Name))
	}
	enum := ""
	if len(names) > 0 {
		enum = `, "enum": [` + strings.Join(names, ", ") + `]`
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"agent":  {"type": "string", "description": "The sub-agent's name."` + enum + `},
			"prompt": {"type": "string", "description": "The complete instructions for the sub-agent. Include all context the sub-agent needs; it does not see your history."},
			"isolate": {"type": "boolean", "description": "Run the sub-agent in an isolated throwaway git worktree (off HEAD) instead of the main working tree, returning its changes as a diff. Use when fanning out PARALLEL editing sub-agents over INDEPENDENT files/modules so they can't clobber each other; then review and apply each returned diff (e.g. git apply) to integrate. Requires a git repo (ignored otherwise) and starts from the last commit, so commit or stash relevant work first. Optional."}
		},
		"required": ["agent", "prompt"]
	}`)
}

// Mutates is true so a delegation goes through the same approval gate as bash —
// the sub-agent can read/write/run things, so the user should opt in.
func (t *taskTool) Mutates() bool { return true }

func (t *taskTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Agent   string `json:"agent"`
		Prompt  string `json:"prompt"`
		Isolate bool   `json:"isolate"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid task input: %v", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	persona, ok := t.personasByID[in.Agent]
	if !ok {
		return "", fmt.Errorf("unknown agent %q (available: %s)", in.Agent, strings.Join(personaNames(t.personas), ", "))
	}

	// Reuse an identical read-only exploration from earlier this session: the
	// workspace is unchanged (the cache is cleared on any mutating tool), so the
	// prior findings still hold. Only read-only, non-isolated tasks are cached —
	// isolated/editing tasks have side effects and must actually run.
	cacheable := !in.Isolate && persona.ReadOnly()
	cacheKey := ""
	if cacheable {
		cacheKey = taskCacheKey(in.Agent, in.Prompt)
		if cached, ok := t.parent.taskCacheGet(cacheKey); ok {
			return cached + "\n\n(reused: an identical " + in.Agent + " task already ran this session and nothing has changed since.)", nil
		}
	}

	// Optional git-worktree isolation: run an editing sub-agent in a throwaway
	// worktree checked out off HEAD so parallel edit tasks don't clobber each
	// other or the user's working tree. Best-effort — falls back to the main tree
	// (with a note) when it isn't a git repo or the worktree can't be created. The
	// sub-agent's changes are returned as a diff for the parent to integrate.
	workDir := t.parent.cfg.WorkingDir
	var wt *isolatedWorktree
	isolateNote := ""
	if in.Isolate {
		if isGitWorktreeRepo(ctx, t.parent.cfg.WorkingDir) {
			if w, err := newIsolatedWorktree(ctx, t.parent.cfg.WorkingDir, persona.Name); err != nil {
				isolateNote = " [isolation unavailable (" + err.Error() + ") — ran in the main working tree]"
			} else {
				wt = w
				workDir = w.path
				defer w.cleanup()
			}
		} else {
			isolateNote = " [isolation skipped: not a git repository — ran in the main working tree]"
		}
	}

	// Build an isolated registry: same root and output cap, but only the
	// allowlisted tools (or all default tools when no allowlist is set).
	var subRegistry *tools.Registry
	// boardAllow honors the persona's tool allowlist for the message-board tools:
	// nil for a default (no-allowlist) persona so it gets both, or the persona's
	// allowlist so a restricted persona only gets messaging if it opts in.
	var boardAllow map[string]bool
	if len(persona.Tools) > 0 {
		allowed := map[string]bool{}
		for _, n := range persona.Tools {
			allowed[n] = true
		}
		subRegistry = tools.NewRegistry(workDir)
		tools.RegisterAllowed(subRegistry, t.parent.cfg.MaxToolOutputBytes, allowed)
		boardAllow = allowed
	} else {
		subRegistry = tools.Default(workDir, t.parent.cfg.MaxToolOutputBytes)
	}
	// Give the sub-agent the shared message board tools so it can talk back to the
	// orchestrator and its siblings (Option C-1). A default persona gets both; a
	// persona with a restricted `tools:` allowlist only gets the ones it lists
	// (least privilege). Independent of ReadOnly model routing, which is computed
	// from the persona's declared tools, not the registry.
	registerBoardTools(subRegistry, boardAllow)

	// Sub-agents auto-run their tools: their approval prompts can't be surfaced
	// to the UI (only the final text is returned), and the parent's `task` call
	// was already gated by the user.
	subCfg := t.subConfig(persona)
	subCfg.WorkingDir = workDir // isolated worktree when in.Isolate, else the parent's dir

	subAgent := &Agent{
		cfg:         &subCfg,
		client:      t.parent.client,
		tools:       subRegistry,
		system:      persona.SystemPrompt,
		alwaysAllow: map[string]bool{},
		perms:       t.parent.perms, // inherit deny/allow rules so delegated work is gated too
		interactive: false,          // headless: the git commit/push hard gate denies rather than auto-running
	}

	// Drain events into a buffer; we return the final assistant text as the tool
	// output. When sub-agent streaming is enabled and the parent's event sink is
	// available (threaded via withTaskEmitter in runToolCall), the sub-agent's
	// tool calls are forwarded to the UI stamped with the parent task call's ID
	// (ParentID) and the persona (Agent), so the UI can nest them under this
	// task's card. Their IDs are namespaced under the parent so they never
	// collide with the parent's own tool IDs. The sub-agent's calls still run in
	// its isolated context — they don't enter the parent's message history.
	parentEmit, parentID := taskEmitterFrom(ctx)
	stream := parentEmit != nil && taskStreamEnabled()
	var finalText strings.Builder
	var (
		subCalls      int     // sub-agent tool calls observed
		subCost       float64 // summed across the sub-agent's turn(s)
		subIn, subOut int
		subCacheRead  int
	)
	emit := func(ev Event) {
		switch e := ev.(type) {
		case TextDelta:
			finalText.WriteString(e.Text)
		case ToolStart:
			subCalls++
			if stream {
				e.ParentID = parentID
				e.Agent = persona.Name
				e.ID = parentID + "/" + e.ID
				parentEmit(e)
			}
		case ToolEnd:
			if stream {
				e.ParentID = parentID
				e.Agent = persona.Name
				e.ID = parentID + "/" + e.ID
				parentEmit(e)
			}
		case TurnDone:
			// The sub-agent's own per-turn accounting; accumulate so the task
			// footer covers the whole delegation (auto-continue can emit more
			// than one TurnDone).
			subCost += e.CostUSD
			subIn += e.TurnUsage.InputTokens
			subOut += e.TurnUsage.OutputTokens
			subCacheRead += e.TurnUsage.CacheReadInputTokens
		}
	}
	start := time.Now()
	// Stamp this sub-agent's identity so its message-board posts are attributed
	// to the persona (not the orchestrator). The shared board itself is already
	// on ctx from the parent's RunWith; the sub-agent's RunWith inherits it
	// (create-if-absent) rather than starting a private one, so parent and child
	// share one board. Identity is set here so the child's RunWith won't overwrite
	// it with the active-profile name. Clear board-admin: only the top-level
	// orchestrator reads the whole board; a sub-agent sees broadcasts + messages
	// addressed to it.
	runCtx := withAgentIdentity(ctx, persona.Name)
	runCtx = withBoardAdmin(runCtx, false)
	runErr := subAgent.Run(runCtx, in.Prompt, emit)
	// Fold this delegation's token usage AND its real cost (at the sub-agent's
	// own model rate — accurate, since each sub-agent prices its own rounds via
	// its own RunWith loop) into the parent turn (independent of UI streaming)
	// so it shows up correctly in the session totals and the parent's per-turn
	// footer, instead of being re-priced at the parent's (often pricier) model
	// or vanishing into the sub-agent's isolated loop.
	subUsageFrom(ctx).addTokens(subIn, subOut, subCacheRead, subCost)
	// emitMeta surfaces a per-delegation turn-meta footer (duration · cost ·
	// model · calls) under this task's card, mirroring the parent's per-turn
	// footer. Gated on stream so MN_TASK_STREAM=0 silences it alongside the
	// sub-agent's tool cards.
	emitMeta := func() {
		if !stream {
			return
		}
		meta := TaskMeta{
			ParentID:   parentID,
			Agent:      persona.Name,
			Model:      subCfg.Model,
			CostUSD:    subCost,
			DurationMS: time.Since(start).Milliseconds(),
			Calls:      subCalls,
			InTokens:   subIn,
			OutTokens:  subOut,
		}
		// Persist the footer (keyed by the task call id) so it survives a reopen,
		// then surface it live under the task card.
		taskMetasFrom(ctx).add(meta)
		parentEmit(meta)
	}
	// appendChanges adds the isolated worktree's diff (when isolate ran) to the
	// returned text so the orchestrator can integrate it; a no-op otherwise.
	appendChanges := func(out string) string {
		if wt == nil {
			return out
		}
		if d := wt.diff(ctx, t.parent.cfg.MaxToolOutputBytes); d != "" {
			return out + "\n\n--- isolated changes (made in a throwaway worktree off HEAD; review and `git apply` from the repo root — or redo the edits — to integrate) ---\n" + d
		}
		return out + "\n\n[isolated run made no file changes]"
	}
	if runErr != nil {
		// A sub-agent runs in a single Execute call with no interactive turn, so
		// it can't be resumed with "continue". If it just ran out of tool steps,
		// don't discard everything it found — return the partial work with a clear
		// note so the parent can act on it or re-delegate a narrower task. Other
		// failures still propagate.
		if errors.Is(runErr, errStepCeiling) {
			emitMeta()
			out := strings.TrimSpace(finalText.String())
			if out == "" {
				out = "(no text produced before the step limit)"
			}
			return t.capResult(fmt.Sprintf("[%s]%s NOTE: hit the tool-step limit before fully finishing — partial result below. Re-delegate a narrower subtask, or raise MN_MAX_STEPS / MN_AUTO_CONTINUE to give sub-agents more steps.\n\n%s", persona.Name, isolateNote, appendChanges(out))), nil
		}
		return "", fmt.Errorf("sub-agent %q failed: %v", persona.Name, runErr)
	}
	emitMeta()
	out := strings.TrimSpace(finalText.String())
	if out == "" {
		out = "(sub-agent returned no text)"
	}
	result := fmt.Sprintf("[%s]%s\n%s", persona.Name, isolateNote, appendChanges(out))
	if cacheable {
		t.parent.taskCachePut(cacheKey, result)
	} else if !persona.ReadOnly() && !in.Isolate {
		// A non-isolated editing sub-agent may have changed the workspace, which
		// can invalidate cached read-only explorations.
		t.parent.clearTaskCache()
	}
	return t.capResult(result), nil
}

// capResult applies the task-specific result cap (MN_TASK_RESULT_MAX / MN_CHEAP),
// tighter than MaxToolOutputBytes, so a delegated deliverable re-sent to the
// parent on every step doesn't bloat context. 0 = no task-specific cap. Head+tail
// (via capOutput) keeps the persona header and the conclusion.
func (t *taskTool) capResult(s string) string {
	if m := t.parent.cfg.TaskResultMaxBytes; m > 0 {
		return capOutput(s, m)
	}
	return s
}

// taskCacheKey identifies a delegated task by persona + prompt (the workspace
// version is handled by clearing the cache on any mutation).
func taskCacheKey(agent, prompt string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + prompt))
	return hex.EncodeToString(sum[:])
}

func (a *Agent) taskCacheGet(key string) (string, bool) {
	a.taskCacheMu.Lock()
	defer a.taskCacheMu.Unlock()
	v, ok := a.taskCache[key]
	return v, ok
}

func (a *Agent) taskCachePut(key, val string) {
	a.taskCacheMu.Lock()
	defer a.taskCacheMu.Unlock()
	if a.taskCache == nil {
		a.taskCache = map[string]string{}
	}
	a.taskCache[key] = val
}

func (a *Agent) clearTaskCache() {
	a.taskCacheMu.Lock()
	defer a.taskCacheMu.Unlock()
	a.taskCache = nil
}

// ResolveModel returns the model a delegated sub-agent will run for the given
// task input — the persona's pinned model, the fast model for a read-only
// persona, or the inherited parent model — so the UI can show it on the task
// card. Returns "" when the input is malformed or names an unknown agent.
func (t *taskTool) ResolveModel(input json.RawMessage) string {
	var in struct {
		Agent string `json:"agent"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	persona, ok := t.personasByID[in.Agent]
	if !ok {
		return ""
	}
	return t.subConfig(persona).Model
}

func personaNames(ps []subagents.Persona) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// contextItems returns the per-persona token contributions to this tool's
// description (the "- name: description" line each persona adds), so the
// context meter can itemize the Subagents segment like Claude Code does.
func (t *taskTool) contextItems() []ContextSegment {
	out := make([]ContextSegment, 0, len(t.personas))
	for _, p := range t.personas {
		desc := p.Description
		if desc == "" {
			desc = "(no description)"
		}
		line := fmt.Sprintf("- %s: %s\n", p.Name, desc)
		out = append(out, ContextSegment{Label: p.Name, Tokens: len(line) / 4})
	}
	return out
}

// subAgentMinSteps floors a read-only surveyor's per-turn tool-step budget. The
// parent's MaxAgentSteps is a runaway-loop guard on the MAIN loop (and a knob
// users lower for cost) — but a delegated explore/review legitimately needs many
// cheap reads in its own isolated context, which is exactly why we delegate (the
// reads stay out of, and don't re-bill, the parent turn). So an aggressively
// lowered MN_MAX_STEPS must not starve the surveyor into bailing mid-survey.
// This restores the historical default; the hard ceiling is this × (1+AutoContinue).
const subAgentMinSteps = 80

// subAgentMinAutoContinue floors how many times a read-only surveyor sub-agent
// may auto-extend past its step budget before the hard ceiling. A delegated
// auditor/explorer can't be resumed with "continue" (it runs in one Execute), so
// it needs enough headroom to finish in a single shot. With subAgentMinSteps this
// yields a ceiling of 80×(1+2)=240 steps for surveyors, vs the default 160.
const subAgentMinAutoContinue = 2

// subConfig builds the config for a persona's isolated sub-agent run: a copy of
// the parent's config (so the parent's policy is untouched) with auto-approval —
// the sub-agent's prompts can't reach the UI — and, when the persona pins one,
// its own model. A persona model rides the parent's client, so it must be valid
// for the current route (/api); otherwise the run errors naturally.
func (t *taskTool) subConfig(persona subagents.Persona) config.Config {
	c := *t.parent.cfg
	c.ApprovalPolicy = config.ApprovalAuto
	// Verification runs once at the top level after the whole turn settles, not
	// inside each delegated sub-agent (which would trigger parallel builds and
	// verify an incomplete edit set). The parent verifies the final state. Same
	// for self-review — the orchestrator reviews the combined diff, not each
	// sub-agent's slice.
	c.VerifyCommand = ""
	c.SelfReview = false
	switch {
	case persona.Model != "" && llm.ModelServableBy(persona.Model, c.LLMProvider):
		c.SetModel(persona.Model) // a pinned persona model wins (and flips the route)
	case persona.ReadOnly():
		// Read-only surveyors (explore, code-reviewer, …) do grunt reading that
		// feeds the parent's synthesis — run them on the cheaper fast model unless
		// the persona pins its own model above. The fast tier may route to a
		// different provider (and reasoning level) than the session; apply both to
		// the sub-agent's config so its whole run uses that route.
		fm, fastProv, _ := config.FastModelRoute(c.Model, c.LLMProvider)
		if fastProv != "" {
			c.LLMProvider = fastProv // FastModelRoute returns a normalized id
		}
		c.SetModel(fm) // sets Model + derives the wire route from LLMProvider
		if fr := config.FastReasoning(); fr != "" {
			c.SetReasoningFor(fm, fr) // after SetModel so c.ReasoningLevel syncs
		}
	}
	// (A pinned model that the current backend can't serve — e.g. a hosted model id
	// pinned then run under Ollama, or an Ollama id under a hosted provider — is ignored by
	// modelServableBy above, so the sub-agent inherits the parent's valid model
	// instead of 404ing on a cross-provider mismatch.)
	// Don't let a cost-tuned main-loop cap cripple a delegated surveyor (see
	// subAgentMinSteps). Floor read-only personas; never lower a parent that's
	// already higher. Also floor their auto-continue headroom so a deep survey
	// can finish in one shot (sub-agents can't be resumed with "continue").
	if persona.ReadOnly() {
		if c.MaxAgentSteps < subAgentMinSteps {
			c.MaxAgentSteps = subAgentMinSteps
		}
		if c.AutoContinue < subAgentMinAutoContinue {
			c.AutoContinue = subAgentMinAutoContinue
		}
	}
	return c
}

// AttachSubAgents registers the `task` tool with the agent's tool registry so
// the model can delegate to the supplied personas. The full list is retained for
// primary-agent switching (SetAgent/AgentNames); the `task` tool is only
// registered when at least one persona is subagent-capable (a non-"primary"
// mode) — there's no point exposing a delegation tool with no valid targets.
func (a *Agent) AttachSubAgents(personas []subagents.Persona) {
	a.personas = personas
	hasDelegatable := false
	for _, p := range personas {
		if p.SubagentCapable() {
			hasDelegatable = true
			break
		}
	}
	if !hasDelegatable {
		return
	}
	a.tools.Register(newTaskTool(a, personas))
	// Expose the shared message board so the orchestrator can coordinate with the
	// sub-agents it delegates to (the sub-agents get it via their own registry in
	// the task tool's Execute). Paired with delegation: pointless without targets.
	// nil allow = the orchestrator always gets both board tools.
	registerBoardTools(a.tools, nil)
}

// Compile-time guarantee that *taskTool satisfies tools.Tool.
var _ tools.Tool = (*taskTool)(nil)

// taskEmitterKey is the context key under which the parent agent stashes its UI
// event sink and the current tool call's ID, so the task tool can forward
// sub-agent tool calls nested under that call (set in executeApprovedCall via
// withTaskEmitter).
type taskEmitterKey struct{}

// taskEmitter bundles the parent's UI event sink with the ID of the task call
// currently executing, so forwarded sub-agent events can reference their parent.
type taskEmitter struct {
	emit     func(Event)
	parentID string
}

// withTaskEmitter returns a context carrying the parent's UI event sink and the
// executing tool call's ID, so a delegated sub-agent's tool calls can be
// forwarded to the UI nested under that call.
func withTaskEmitter(ctx context.Context, emit func(Event), parentID string) context.Context {
	return context.WithValue(ctx, taskEmitterKey{}, taskEmitter{emit: emit, parentID: parentID})
}

// taskEmitterFrom returns the parent UI event sink and parent call ID stored on
// ctx; emit is nil when none was set (e.g. a sub-agent that has no task tool).
func taskEmitterFrom(ctx context.Context) (func(Event), string) {
	te, _ := ctx.Value(taskEmitterKey{}).(taskEmitter)
	return te.emit, te.parentID
}

// subUsageAcct accumulates the token usage of every sub-agent delegated during a
// parent turn (via the `task` tool). The parent folds this into its own
// TurnUsage at turn end (see RunWith), so a delegation's tokens show up in the
// session totals (status bar) and the per-turn footer instead of vanishing into
// the sub-agent's isolated loop. Concurrency-safe: contiguous `task` calls run
// in parallel and add from multiple goroutines.
type subUsageAcct struct {
	mu        sync.Mutex
	in        int
	out       int
	cacheRead int
	costUSD   float64
}

// addTokens records one finished sub-agent's token usage and its REAL cost
// (computed at the sub-agent's own model — already accurate per-round, since
// each sub-agent prices its own rounds via its own RunWith loop). Folding the
// precomputed cost here — rather than re-deriving it from the folded tokens at
// the parent's model — means a delegation to the cheaper FastModel shows up in
// the parent's total as what it actually cost, not what it would have cost on
// the parent's (often pricier) session model. Takes plain values so task.go
// needn't import the llm package just for this.
func (s *subUsageAcct) addTokens(in, out, cacheRead int, costUSD float64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.in += in
	s.out += out
	s.cacheRead += cacheRead
	s.costUSD += costUSD
	s.mu.Unlock()
}

// totals returns the accumulated sub-agent token usage and real cost so far.
func (s *subUsageAcct) totals() (in, out, cacheRead int, costUSD float64) {
	if s == nil {
		return 0, 0, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.in, s.out, s.cacheRead, s.costUSD
}

type subUsageKey struct{}

// withSubUsage threads a per-turn sub-agent usage accumulator onto ctx so the
// task tool can report a delegation's token usage back to the parent turn.
func withSubUsage(ctx context.Context, a *subUsageAcct) context.Context {
	return context.WithValue(ctx, subUsageKey{}, a)
}

// subUsageFrom returns the parent turn's sub-agent usage accumulator, or nil
// when none is set (e.g. a nested sub-agent).
func subUsageFrom(ctx context.Context) *subUsageAcct {
	a, _ := ctx.Value(subUsageKey{}).(*subUsageAcct)
	return a
}

// taskMetaAcct collects the per-delegation footers (TaskMeta) produced during a
// parent turn so the parent can persist them at turn end (keyed by the task
// tool-call id) for replay when a saved session reopens. Concurrency-safe:
// contiguous `task` calls run in parallel and add from multiple goroutines.
type taskMetaAcct struct {
	mu    sync.Mutex
	metas []TaskMeta
}

// add records one finished delegation's footer.
func (a *taskMetaAcct) add(m TaskMeta) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.metas = append(a.metas, m)
	a.mu.Unlock()
}

// drain returns the accumulated footers and clears the accumulator.
func (a *taskMetaAcct) drain() []TaskMeta {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.metas
	a.metas = nil
	return out
}

type taskMetaKey struct{}

// withTaskMetas threads a per-turn task-footer accumulator onto ctx so the task
// tool can hand each finished delegation's footer back to the parent to persist.
func withTaskMetas(ctx context.Context, a *taskMetaAcct) context.Context {
	return context.WithValue(ctx, taskMetaKey{}, a)
}

// taskMetasFrom returns the parent turn's task-footer accumulator, or nil.
func taskMetasFrom(ctx context.Context) *taskMetaAcct {
	a, _ := ctx.Value(taskMetaKey{}).(*taskMetaAcct)
	return a
}

// taskStreamEnabled reports whether a delegated sub-agent's tool activity is
// surfaced in the parent UI (default true; set MN_TASK_STREAM=0/off/false/no
// to silence it and show only the final answer).
func taskStreamEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_TASK_STREAM"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}
