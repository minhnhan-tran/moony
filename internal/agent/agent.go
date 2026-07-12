// Package agent implements the core agent loop: it sends the conversation to
// the model, executes any requested tool calls (gated by approval for mutating
// tools), compacts long histories, and persists sessions.
package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/format"
	"github.com/pysty1995/moony/internal/hooks"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
)

// Decision is the user's response to an ApprovalRequest.
type Decision int

const (
	Deny Decision = iota
	Approve
	ApproveAlways
)

// Agent ties together the model client, tools, history, and persistence.
type Agent struct {
	cfg      *config.Config
	client   llm.Streamer
	tools    *tools.Registry
	system   string
	messages []llm.Message

	store       *session.Store
	sess        *session.Session
	alwaysAllow map[string]bool

	// perms, when set, holds persisted allow/deny rules consulted before the
	// approval policy: a deny match blocks the call, an allow match skips the
	// prompt, anything else falls through to needsApproval.
	perms *permissions.Rules

	// titled is set once a session has a final (model-generated or resumed)
	// title, so auto-titling runs at most once and never on resumed sessions.
	titled bool

	// lastTurnHard records whether the previous turn hit real friction (failed
	// tools / non-zero exits / goal-judge rejections — see turnFriction), not
	// merely that it ran many tool rounds. The "auto" reasoning level uses it as a
	// cross-turn signal to start the next turn's effort higher.
	lastTurnHard bool

	// turnFriction counts this turn's friction events — tools that actually FAILED
	// (execution errors), bash commands that exited non-zero, and goal-judge
	// rejections. It's the difficulty signal the "auto" reasoning ramp keys off of
	// (rampedAutoEffort), replacing raw tool-round count so a long-but-mechanical
	// read/grep loop never inflates reasoning. Reset each turn; incremented from
	// concurrent tool workers (read-only tools run in parallel), so it's atomic.
	turnFriction atomic.Int64

	// turnAuxCostBits accumulates, as IEEE-754 bits (via math.Float64bits), the
	// real $ cost of every streamAux call made this turn — self-review, checkpoint
	// distillation, goal-judge, session titling, profile refresh, "btw" notes.
	// Those calls run on AuxModel from methods outside the main round loop, so
	// without this their spend was invisible to the turn's displayed/persisted
	// cost. Reset at the start of each turn; folded into the turn total when it
	// finishes. Atomic (CAS loop) since aux calls can run from concurrent tool
	// workers, mirroring turnFriction above. Use addAuxCost/auxCostUSD/resetAuxCost.
	turnAuxCostBits atomic.Uint64

	// turnThinkHard counts steps this turn where the model's OWN adaptive thinking
	// engaged heavily (a thinking block present AND a large output) — a model-driven
	// difficulty signal that raises the auto-reasoning floor for the rest of the
	// turn, complementing the friction ramp (which only reacts to failures). Reset
	// each turn; written on the Run goroutine, read via modelThinkFloor. Atomic for
	// symmetry with turnFriction (and any concurrent read).
	turnThinkHard atomic.Int64

	// turnHard is a proactive per-turn difficulty flag (from turnLooksHard on the
	// user's request). When set, adaptive routing starts the turn on the strong
	// model instead of the cheap FastModel. Reset/recomputed each turn on the Run
	// goroutine.
	turnHard bool

	// tokEst predicts real input tokens from conversation char counts, learning
	// the provider's chars-per-token slope and fixed overhead (system prompt, tool
	// schemas, checkpoint) online from reported input_tokens. estTokens uses it so
	// the compaction triggers (maybeCompact/compactBoundary) track real context
	// size instead of a blind bytes/4 guess. It replaces the old additive-overhead
	// scalar: a constant can't correct a proportional error in bytes/4, whereas a
	// slope+intercept fit can. Uncalibrated it falls back to bytes/4. Only touched
	// on the Run goroutine. See calibrateTokens.
	tokEst llm.TokenEstimator

	// turnReadCalls dedups identical idempotent read calls within a turn (keyed by
	// tool name + input): a repeat whose result is STILL in context is
	// short-circuited to a cheap stub instead of re-executing and re-sending the
	// (possibly large) output. Crucially, pruneStaleToolResults removes a read's
	// entry here when it elides that read's result — so a re-read of an *elided*
	// result is allowed to re-execute and bring the data back into the recent
	// window, instead of being blocked with a stub the model can no longer act on
	// (the prune/dedup re-read treadmill). Reset each turn and cleared whenever a
	// mutating tool runs (a write may change what a re-read returns). See
	// dedupReadCalls and pruneStaleToolResults.
	turnReadCalls map[string]bool

	// taskCache memoizes read-only, non-isolated sub-agent (task) results by a
	// hash of agent+prompt, so an identical exploration this session returns the
	// prior findings instead of re-running the sub-agent (and regenerating its
	// bulky context). Cleared whenever a mutating tool runs (a workspace change
	// can invalidate earlier findings). Guarded by taskCacheMu since task may run
	// off the Run goroutine.
	taskCache   map[string]string
	taskCacheMu sync.Mutex

	// lazyMCP gates connected MCP servers' tool schemas out of the advertised
	// tool list until the model activates them via the enable_toolset tool (see
	// config.LazyMCPTools). lazyBuiltins does the same for the non-core built-in
	// tool GROUPS (git/browser; see builtinToolGroups and
	// config.LazyBuiltinTools). enabledToolsets holds the servers/groups turned on
	// this session; toolsetMu guards it since enable_toolset may run off the Run
	// goroutine. A toolset is "dormant" iff its lazy flag is on && it's not in
	// enabledToolsets.
	lazyMCP         bool
	lazyBuiltins    bool
	toolsetMu       sync.Mutex
	enabledToolsets map[string]bool

	// todos, when set, is the shared task list updated by the todo_write tool.
	// Updates are surfaced as TodosUpdated events during a Run.
	todos *tools.TodoStore

	// plan, when set, is the shared plan artifact updated by the plan_write
	// tool. Drafts are surfaced as PlanUpdated events and synced to the session.
	plan *tools.PlanStore

	// planRanATurn reports whether the current plan (draft OR approved) was still
	// pending at the END of a turn — i.e. it has survived past the turn it was
	// created/approved in. It gates the turn-start retirement of a stale plan
	// (retireExecutedPlan for a finished approved plan; retireStaleDraft for an
	// unapproved draft the user moved past in build mode) so a freshly drafted or
	// just-approved plan still gets its presentation/execution turn first. Reset
	// when a plan is drafted or approved; set at the end of a turn that left a plan
	// pending.
	planRanATurn bool

	// planIdleTurns counts consecutive top-level turns an approved, unfinished plan
	// has ended WITHOUT progress on its steps; planResolvedMark is the resolved
	// (completed/cancelled) mirrored-todo count seen at the previous turn's end, so a
	// rise means progress. trackPlanAbandon uses them to retire a plan the user has
	// stopped driving (the approved-plan analogue of retireStaleDraft), while a plan
	// still advancing — including one resumed after a step-cap continue — resets the
	// tally and is kept. Reset via resetPlanProgress when a plan is drafted/approved/cleared.
	planIdleTurns    int
	planResolvedMark int

	// hooks fires user-defined shell commands at lifecycle points.
	hooks *hooks.Set

	// active is the current agent profile (opencode-style): "build" (default,
	// full access) or "plan" (read-only). It carries a per-tool allow/ask/deny
	// permission map consulted in visibleTools + approveToolCall. A nil pointer
	// means the default build profile. Atomic because it is written on the Run
	// goroutine (requestExitPlan / MaybeAutoPlan) while the UI reads it
	// concurrently via PlanMode()/ActiveAgent(). PlanMode() reports whether the
	// plan profile is active, which still drives the read-only steering preface
	// and the plan-artifact workflow.
	active atomic.Pointer[config.Agent]

	// personas are the attached sub-agent definitions (see AttachSubAgents).
	// Primary-capable ones can also be switched to as a primary agent profile
	// via SetAgent, so a user-defined `.sqe/agents/*.md` agent works like a
	// custom build/plan profile.
	personas []subagents.Persona

	// interactive reports whether a real user can answer approval/question
	// prompts on this agent (true for the TUI and web sessions; false for
	// sub-agents and headless runs). High-stakes actions that must always be
	// confirmed (git commit/push, leaving plan mode to execute) are DENIED when
	// this is false rather than silently auto-run — see hardApproval.
	interactive bool

	// groupedQuestions reports whether this agent's surface can render several
	// ask_user questions in one panel (the web tabs UI). When true the questions[]
	// form is emitted as a single QuestionGroupRequest answered all at once; when
	// false (TUI, sub-agents) the questions are asked one at a time. See
	// requestQuestionGroup.
	groupedQuestions bool

	// snapshots is the undo log; recorded automatically before every
	// write_file/edit_file/multi_edit. Reverted by /undo.
	snapshots *snapshotStore

	// lsp, when set, receives didChange after each successful write_file /
	// edit_file / multi_edit and the resulting diagnostics are appended to the
	// tool output.
	lsp *lsp.Manager

	// format, when set, runs configured formatters (gofmt, prettier, …) on each
	// file written/edited by a successful mutating tool, right before LSP
	// diagnostics are gathered so diagnostics see the formatted code.
	format *format.Manager

	// workers, when set, backs the `delegate` tool: out-of-process worker agents
	// (Claude Code / Optimus) the model can dispatch tasks to.
	workers *worker.Manager

	// workflowNames, when set, lists the saved Agent Builder workflows runnable via
	// the run_workflow tool — used to resolve and dispatch @workflow mentions.
	workflowNames WorkflowLister

	// goal, when set, is the session stop-condition: a turn won't end until an
	// independent judge model confirms the goal is met (see goal.go). Set via
	// /goal and persisted with the session.
	goal string
	// goalCriteria are the concrete completion criteria the goal is judged
	// against; goalBudgetTokens/goalTokensUsed enforce an optional token budget;
	// goalBlocked + goalNotMetStreak track the "blocked" state. All persisted
	// with the session (see goal.go).
	goalCriteria     []string
	goalBudgetTokens int
	goalTokensUsed   int
	goalBlocked      bool
	goalNotMetStreak int
}

// New constructs an Agent. store and sess may be nil to disable persistence;
// if store is non-nil but sess is nil, a fresh session is created.
func New(cfg *config.Config, client llm.Streamer, registry *tools.Registry, store *session.Store, sess *session.Session) *Agent {
	if store != nil && sess == nil {
		sess = session.New(cfg.Model, cfg.WorkingDir)
	}
	a := &Agent{
		cfg:         cfg,
		client:      client,
		tools:       registry,
		system:      buildSystemPrompt(cfg, registry),
		store:       store,
		sess:        sess,
		alwaysAllow: map[string]bool{},
		snapshots:   &snapshotStore{},
		// A session that already has history (a resumed one) keeps its name; a
		// fresh session — even one pre-saved with a placeholder title, as the web
		// does — has no messages yet and is eligible for auto-titling.
		titled: sess != nil && len(sess.Messages) > 0,

		lazyMCP:         cfg.LazyMCPTools,
		lazyBuiltins:    cfg.LazyBuiltinTools,
		enabledToolsets: map[string]bool{},
	}
	if sess != nil {
		a.messages = sess.Messages
		a.loadGoalFromSession() // restore any persisted stop-condition + criteria/budget
	}
	// Apply the configured read_file default line cap (process-wide; read tools
	// read it per-call, so this also affects already-registered tools).
	tools.SetDefaultReadLines(cfg.MaxReadLines)
	// Register the enable_toolset meta-tool so the model can activate dormant
	// MCP servers (no-op visibility-wise when lazyMCP is off — see visibleTools).
	if registry != nil {
		registry.Register(&enableToolsetTool{a: a})
		registry.SetConciseSchemas(cfg.ConciseToolSchemas)
		// Wire the usage tool to this agent's live session so it can report
		// token/cost totals; returns nil (tool says "no data") when persistence
		// is disabled.
		registry.RegisterUsage(func() []session.TurnMeta {
			if a.sess == nil {
				return nil
			}
			return a.sess.TurnMetas
		})
		// Enable hybrid semantic reranking in search_code when an embeddings
		// model is configured and the client can produce embeddings.
		if cfg.EmbeddingsModel != "" {
			if ec, ok := client.(embedderClient); ok {
				base := boundEmbedder{c: ec, model: cfg.EmbeddingsModel}
				// Back the embedder with a persistent, content-addressed cache so
				// unchanged code chunks / repeated queries aren't re-embedded across
				// searches or sessions — keeping the rerank cheap enough to stay on.
				var emb tools.Embedder = base
				if path, err := config.GlobalConfigPath("embed-cache.gob"); err == nil {
					emb = llm.NewEmbedCache(path, cfg.EmbeddingsModel, base.Embed)
				}
				registry.SetEmbedder(emb)
			}
		}
	}
	return a
}

// embedderClient is the subset of the LLM client used for embeddings. *llm.Client
// satisfies it; kept as a local interface so agent doesn't hard-depend on the
// concrete type.
type embedderClient interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// boundEmbedder adapts an embedderClient to tools.Embedder by binding the model.
type boundEmbedder struct {
	c     embedderClient
	model string
}

func (b boundEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return b.c.Embed(ctx, b.model, texts)
}

// Reset clears the conversation history, starting a new session if persisting.
func (a *Agent) Reset() {
	a.messages = nil
	a.clearGoalState() // a fresh session has no stop-condition
	if a.store != nil {
		a.sess = session.New(a.cfg.Model, a.cfg.WorkingDir)
	}
	a.titled = false // a brand-new session is eligible for auto-titling again
	if a.todos != nil {
		a.todos.Set(nil) // a fresh session has no task list
	}
	if a.plan != nil {
		a.plan.Sync(nil) // a fresh session has no plan (host-side reset, no notify)
	}
}

// Resume switches the active session to sess, loading its history so the next
// turn continues that conversation and subsequent saves write back to it. It's
// a no-op when persistence is disabled (no store) or sess is nil. The task list
// is rebuilt from the new session's history so the todo panel reflects it.
func (a *Agent) Resume(sess *session.Session) {
	if a.store == nil || sess == nil {
		return
	}
	a.sess = sess
	a.messages = sess.Messages
	a.loadGoalFromSession()           // a resumed session carries its own stop-condition
	a.titled = len(sess.Messages) > 0 // a session with history keeps its name
	if a.todos != nil {
		// Replace (not merge) so the previous session's tasks don't linger.
		a.todos.Set(tools.LatestTodosFromMessages(sess.Messages))
	}
	if a.plan != nil {
		// Restore the resumed session's plan artifact (nil clears the panel).
		// Sync, not Set: this is a host-side mirror of persisted state, and Set
		// would fire the plan_write notify (applyDraftPlan) and demote a resumed
		// approved/done plan back to draft.
		a.plan.Sync(toToolPlan(sess.Plan))
	}
	// planRanATurn is in-memory only; a persisted, not-yet-done plan survived from
	// a prior turn, so it has effectively already "run a turn". Mark it so the next
	// top-level turn can retire an executed/approved plan (or discard a stale draft)
	// instead of re-injecting it forever. Without this, a resumed session (reopened
	// session / server restart) keeps showing an approved+executed plan in follow-ups,
	// because both retirement gates bail while planRanATurn is false.
	if sess.Plan != nil && len(sess.Plan.Steps) > 0 && sess.Plan.Status != session.PlanDone {
		a.planRanATurn = true
		// Baseline the abandonment tracker (see trackPlanAbandon) at the restored
		// progress, so the first post-resume turn measures NEW progress rather than
		// counting the carried-over resolved todos as if this turn had made them.
		a.planResolvedMark = a.resolvedTodoCount()
		a.planIdleTurns = 0
	}
}

// SetSandbox configures the OS-level bash sandbox on the underlying tool
// registry, so it can be toggled at runtime (e.g. via /sandbox).
func (a *Agent) SetSandbox(enabled, network bool) {
	if a.tools != nil {
		a.tools.SetSandbox(enabled, network)
	}
}

// History returns the number of messages currently retained.
func (a *Agent) History() int { return len(a.messages) }

// Messages returns the current conversation history.
func (a *Agent) Messages() []llm.Message { return a.messages }

// AttachTodoStore wires the task list backing the todo_write tool. Updates
// from the model are surfaced as TodosUpdated events during a Run. When the
// agent resumed a session with prior history, the store is seeded with that
// session's last task list so a reopened session restores its todo panel.
func (a *Agent) AttachTodoStore(s *tools.TodoStore) {
	a.todos = s
	if s != nil && len(a.messages) > 0 {
		if restored := tools.LatestTodosFromMessages(a.messages); len(restored) > 0 {
			s.Set(restored)
		}
	}
}

// AttachHooks wires the configured hook set. Errors from hooks are reported
// via the logger inside the set, never bubbled up.
func (a *Agent) AttachHooks(h *hooks.Set) { a.hooks = h }

// AttachLSP wires a language-server manager. After each successful
// write_file/edit_file the manager is asked for diagnostics, which are
// appended to the tool output so the model sees errors immediately. Also
// (re-)registers the lsp_definition/lsp_references/lsp_hover tools against
// the new manager, so a `/lsp reload` picks up freshly started servers.
func (a *Agent) AttachLSP(m *lsp.Manager) {
	a.lsp = m
	a.tools.RegisterLSP(m)
}

// LSP returns the attached language-server manager (nil if none), so callers
// hot-reloading servers can close the previous one.
func (a *Agent) LSP() *lsp.Manager { return a.lsp }

// AttachFormatter wires a code-formatter manager. After each successful
// write_file/edit_file/multi_edit/apply_patch the changed files are formatted
// in place (before LSP diagnostics run) so changes land already formatted.
func (a *Agent) AttachFormatter(m *format.Manager) { a.format = m }

// AttachPermissions wires persisted allow/deny rules. They are consulted in
// approveToolCall before the approval policy: deny blocks, allow skips the
// prompt, no match falls through.
func (a *Agent) AttachPermissions(p *permissions.Rules) { a.perms = p }

// Permissions returns the agent's live permission rules (may be nil), so hosts
// can display and edit them (e.g. the /permissions command).
func (a *Agent) Permissions() *permissions.Rules { return a.perms }

// SetPlanMode toggles read-only planning mode. When enabled, the mutating tools
// (write_file, edit_file, multi_edit, apply_patch, bash, task) are hidden from
// the model and any call to them returns an error so the model can't sneak
// around the gate, and planModeContext injects a read-only steering preface so
// the model knows to investigate and propose rather than attempt changes.
func (a *Agent) SetPlanMode(on bool) {
	if on {
		a.active.Store(config.PlanAgent())
	} else {
		a.active.Store(config.BuildAgent())
	}
}

// PlanMode reports whether the read-only plan profile is active.
func (a *Agent) PlanMode() bool { return a.activeAgent().Name == config.AgentPlan }

// activeAgent returns the current profile, defaulting to build when unset.
func (a *Agent) activeAgent() *config.Agent {
	if p := a.active.Load(); p != nil {
		return p
	}
	return config.BuildAgent()
}

// ActiveAgent returns the current agent profile (build/plan). Never nil.
func (a *Agent) ActiveAgent() *config.Agent { return a.activeAgent() }

// SetAgent switches the active profile by name. A primary-capable attached
// persona of that name wins (opencode's defaults→user precedence: an on-disk
// build.md/plan.md overrides the Go built-in, so users can customize them);
// otherwise the built-in ("build"/"plan") is used. Returns false for an unknown
// name, leaving the current profile unchanged.
func (a *Agent) SetAgent(name string) bool {
	for _, persona := range a.personas {
		if strings.EqualFold(persona.Name, name) {
			if prof := persona.ToAgent(); prof != nil {
				a.active.Store(prof)
				return true
			}
			// A subagent-only persona can't be a primary profile; fall back to
			// a built-in of the same name if one exists ("plan"/"build").
			break
		}
	}
	if p := config.BuiltinAgent(name); p != nil {
		a.active.Store(p)
		return true
	}
	return false
}

// AgentNames returns the names of every selectable primary agent profile: the
// built-ins plus any primary-capable attached personas, deduped (an on-disk
// build/plan agent shows once). Used by the UI to list and complete switches.
func (a *Agent) AgentNames() []string {
	names := []string{config.AgentBuild, config.AgentPlan}
	seen := map[string]bool{config.AgentBuild: true, config.AgentPlan: true}
	for _, p := range a.personas {
		if p.PrimaryCapable() && !seen[strings.ToLower(p.Name)] {
			names = append(names, p.Name)
			seen[strings.ToLower(p.Name)] = true
		}
	}
	return names
}

// SetInteractive marks whether a real user can answer approval/question prompts
// on this agent. Hosts with a live UI (TUI, web session) set it true; headless
// runs and sub-agents leave it false, so high-stakes always-confirm actions
// (git commit/push, leaving plan mode) are denied rather than auto-run.
func (a *Agent) SetInteractive(v bool) { a.interactive = v }

// SetQuestionGroups marks whether this agent's surface can render several
// ask_user questions in one panel (the web tabs UI). The web session sets it
// true so the questions[] form is answered all at once; the TUI and sub-agents
// leave it false and are asked one question at a time.
func (a *Agent) SetQuestionGroups(v bool) { a.groupedQuestions = v }

// MaybeAutoPlan auto-enters read-only plan mode for a build-mode user turn that
// looks like a large or multi-step change, when the AutoPlan setting is on. It
// returns true (after flipping plan mode on) when it switched, so the caller can
// tell the user. It is a no-op when AutoPlan is off, when already in plan mode,
// when an approved plan is still executing, or when the task doesn't look large
// — and /build always overrides it for the next turn.
func (a *Agent) MaybeAutoPlan(userText string) bool {
	// Only auto-switch from the default build agent — never clobber a custom
	// profile the user explicitly selected, and (via PlanMode) never re-plan
	// when already planning.
	if a.cfg == nil || !a.cfg.AutoPlan || a.activeAgent().Name != config.AgentBuild {
		return false
	}
	if a.PlanApproved() {
		return false // an approved plan is mid-execution; don't re-plan
	}
	if !looksLikeLargeTask(userText) {
		return false
	}
	a.active.Store(config.PlanAgent())
	return true
}

// looksLikeLargeTask is a conservative, dependency-free heuristic for whether a
// user request is a large/multi-step change worth planning first. It favors
// precision over recall (better to under-trigger than to force planning on a
// small ask): it matches a few unambiguous "big change" phrases, or an explicit
// multi-item checklist of requirements.
func looksLikeLargeTask(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	for _, kw := range []string{
		"refactor", "rewrite", "re-write", "redesign", "re-architect", "rearchitect",
		"migrate", "overhaul", "from scratch", "across the codebase", "across the project",
		"entire codebase", "whole codebase", "restructure", "end-to-end", "end to end",
	} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	// An explicit multi-item checklist (≥3 lines with ≥2 bullet/number markers)
	// usually describes a multi-step effort worth a plan.
	if strings.Count(s, "\n") >= 3 {
		markers := strings.Count(s, "\n- ") + strings.Count(s, "\n* ") +
			strings.Count(s, "\n1.") + strings.Count(s, "\n2.") + strings.Count(s, "\n3.")
		if markers >= 2 {
			return true
		}
	}
	return false
}

// Config returns the agent's live configuration. Mutating its fields (model,
// reasoning, approval policy, …) changes how subsequent turns behave; callers
// must only do so while no turn is running.
func (a *Agent) Config() *config.Config { return a.cfg }

// Registry returns the agent's tool registry, so hosts can register or reload
// dynamic tools (e.g. MCP servers) on a live agent. Mutate only while idle.
func (a *Agent) Registry() *tools.Registry { return a.tools }

// planModeBlocked is the set of mutating tool names hidden in plan mode. It is
// the canonical list — runToolCall consults it for defense-in-depth so even a
// model that "knows" the tool's name (from history or a fresh sub-agent) can't
// invoke it while plan mode is on.
var planModeBlocked = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"multi_edit":  true,
	"apply_patch": true,
	"bash":        true,
	"task":        true,
	"delegate":    true, // out-of-process worker that can edit/run — must not execute a pending draft
	"remember":    true, // appends to AGENTS.md — a file write
}

// visibleTools returns the tool schemas visible to the model this turn. Tools
// the active profile DENIES are stripped (so a read-only "plan" agent never sees
// write_file/edit_file/.../task); a profile that only ASKS for a tool (e.g. plan
// + bash) keeps it visible and gates it at call time. Independently, while a
// drafted plan is awaiting approval the mutating built-ins are stripped too, so
// the model refines/awaits approval instead of executing the plan it just drafted.
func (a *Agent) visibleTools() []llm.Tool {
	all := a.gateToolsets(a.tools.Schemas())
	prof := a.activeAgent()
	draftGate := a.hasDraftPlan()
	if prof.Name == config.AgentBuild && !draftGate {
		return all
	}
	out := make([]llm.Tool, 0, len(all))
	for _, t := range all {
		if prof.Rule(t.Name) == config.PermDeny {
			continue
		}
		if draftGate && planModeBlocked[t.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// planModeContext returns a per-turn steering preface (carried in the synthetic
// message by withCheckpoint) when plan mode is on, or "" otherwise. Hiding the
// mutating tools (visibleTools) and rejecting them (approveToolCall) makes plan
// mode *mechanically* read-only, but the static system prompt never changes, so
// without this the model still behaves like build mode — it tries to edit, gets
// rejected, and apologizes. This tells the model what plan mode means so it
// investigates and proposes instead of attempting mutations. It is keyed on the
// planMode flag, independent of whether a plan artifact exists (planContext).
func (a *Agent) planModeContext() string {
	if !a.PlanMode() {
		return ""
	}
	return "# Plan mode — read-only\n\n" +
		"Plan mode is ON, so you are operating read-only this turn. The write_file, " +
		"edit_file, multi_edit, apply_patch, and task tools are DISABLED and any call " +
		"to them will be rejected. The bash tool requires explicit user approval, so " +
		"use it only for read-only inspection (and expect a confirmation prompt). Do " +
		"NOT modify files or spawn sub-agents. Instead, investigate with read-only tools " +
		"(read_file, grep, glob, list_dir, …) and propose a concrete, ordered plan. " +
		"Record the plan with the `plan_write` tool — that is how it's shown to the user " +
		"(inline, for review and approval); NEVER write the plan to a file (e.g. PLAN.md). " +
		"When the user is ready for you to make changes, tell them to run /build to leave " +
		"plan mode."
}

// Todos returns a snapshot of the current task list (or nil if unattached).
func (a *Agent) Todos() []tools.Todo {
	if a.todos == nil {
		return nil
	}
	return a.todos.Items()
}

// ListModels fetches the models available on the active route from the provider.
func (a *Agent) ListModels(ctx context.Context) ([]config.ModelInfo, error) {
	return a.client.ListModels(ctx)
}

// Usage fetches the current quota and usage data from the provider. When a
// project token is configured, project-scoped data is returned; otherwise
// personal-account data is returned.
func (a *Agent) Usage(ctx context.Context) (*llm.UsageResponse, error) {
	return a.client.Usage(ctx)
}

// CheckAuth verifies the agent can authenticate with the provider (used by the
// /doctor diagnostic). It returns nil on success.
func (a *Agent) CheckAuth(ctx context.Context) error {
	return a.client.CheckAuth(ctx)
}

// noThinkStreamer is implemented by clients that can run a request with extended
// thinking disabled (the real *llm.Client). Auxiliary calls assert against it
// and fall back to a normal stream when it's absent (e.g. test fakes).
type noThinkStreamer interface {
	StreamMessagesNoThink(ctx context.Context, system string, messages []llm.Message, tools []llm.Tool, h llm.Handler) (*llm.Response, error)
}

// preTurnNoticer is an optional client capability: a one-line notice to show
// before a turn starts (the Ollama backend uses it to warn that the selected
// model must load into memory first, so a slow first response is explained).
// Clients that don't implement it produce no notice.
type preTurnNoticer interface {
	PreTurnNotice(ctx context.Context) string
}

// addAuxCost adds usd to turnAuxCostBits via a lock-free CAS loop (usd may be
// 0, e.g. on a failed aux call — skipped as a no-op).
func (a *Agent) addAuxCost(usd float64) {
	if usd == 0 {
		return
	}
	for {
		old := a.turnAuxCostBits.Load()
		next := math.Float64bits(math.Float64frombits(old) + usd)
		if a.turnAuxCostBits.CompareAndSwap(old, next) {
			return
		}
	}
}

// auxCostUSD returns the accumulated real cost of this turn's streamAux calls.
func (a *Agent) auxCostUSD() float64 { return math.Float64frombits(a.turnAuxCostBits.Load()) }

// resetAuxCost zeroes the aux-cost accumulator; call at the start of a turn.
func (a *Agent) resetAuxCost() { a.turnAuxCostBits.Store(0) }

// streamAux runs a cheap, tool-less auxiliary model call (the goal judge,
// history summarization, session titling) with extended thinking OFF when the
// client supports it — those calls want a quick answer, not deliberation, and
// running them at the session's reasoning level adds latency and cost for no
// gain. Falls back to a normal stream for clients that don't implement it.
// Every call's REAL spend is folded into turnAuxCostBits (see addAuxCost) so it
// shows up in the turn's displayed/persisted cost instead of being silently
// dropped.
func (a *Agent) streamAux(ctx context.Context, system string, messages []llm.Message) (*llm.Response, error) {
	// aux calls want a cheap quick answer, not the (pricier) session/fast model —
	// and may run on a different provider than the session (Self-review provider).
	auxModel, auxProv, _ := config.AuxModelRoute(a.cfg.Model, a.cfg.LLMProvider)
	ctx = llm.WithModel(ctx, auxModel)
	if auxProv != "" {
		ctx = llm.WithProvider(ctx, auxProv)
	}
	var resp *llm.Response
	var err error
	if ar := config.AuxReasoning(); ar != "" {
		// The user pinned a Self-review reasoning level — honor it instead of the
		// default of forcing reasoning OFF for the auxiliary call.
		resp, err = a.client.StreamMessages(llm.WithEffort(ctx, ar), system, messages, nil, llm.Handler{})
	} else if nt, ok := a.client.(noThinkStreamer); ok {
		resp, err = nt.StreamMessagesNoThink(ctx, system, messages, nil, llm.Handler{})
	} else {
		resp, err = a.client.StreamMessages(ctx, system, messages, nil, llm.Handler{})
	}
	if resp != nil {
		a.addAuxCost(resp.Usage.CostUSD(auxModel) + resp.RetryUsage.CostUSD(auxModel))
	}
	return resp, err
}

// SessionID returns the active session id, or "" if persistence is disabled.
func (a *Agent) SessionID() string {
	if a.sess == nil {
		return ""
	}
	return a.sess.ID
}

// Run processes one user message to completion, emitting events along the way.
// emit may be invoked concurrently from multiple goroutines while parallel-safe
// tool calls execute, so it must be safe for concurrent use (the TUI's emit is
// a channel send).
func (a *Agent) Run(ctx context.Context, userInput string, emit func(Event)) error {
	return a.RunWith(ctx, userInput, nil, emit)
}

// buildUserMessage assembles a user turn from prompt text plus any attachment
// blocks. The text leads; attachments follow. An all-empty turn falls back to an
// empty text block so the message is never contentless.
func buildUserMessage(userInput string, attachments []llm.ContentBlock) llm.Message {
	var content []llm.ContentBlock
	if strings.TrimSpace(userInput) != "" {
		content = append(content, llm.TextBlock(userInput))
	}
	content = append(content, attachments...)
	if len(content) == 0 {
		content = append(content, llm.TextBlock(""))
	}
	return llm.Message{Role: llm.RoleUser, Content: content}
}

// RunWith is Run with attachment content blocks (images, documents, or
// extracted-file text) appended to the user turn after the prompt text. A turn
// with attachments but no text is allowed (e.g. "describe this image").
func (a *Agent) RunWith(ctx context.Context, userInput string, attachments []llm.ContentBlock, emit func(Event)) error {
	turnStart := time.Now()
	// A sub-agent's RunWith arrives with its persona identity already stamped on
	// ctx (by the task tool); the top-level user turn doesn't. Capture that here,
	// before the messaging block below stamps the orchestrator's own identity, so
	// pre-turn notices fire only for the user's turn, not each delegation.
	topLevel := agentIdentityFrom(ctx) == ""
	// turnUsage accumulates token usage across every model call this turn (the
	// initial answer plus any tool-loop rounds), so the persisted turn footer's
	// cost reflects the whole answering turn, not just the final call.
	var turnUsage llm.Usage
	// turnCostUSD is the REAL accumulated $ cost of this turn, priced per call at
	// whichever model actually served it (session model, or the cheaper
	// FastModel when AdaptiveModel/CheapReads downshifted a round) — unlike
	// turnUsage.CostUSD(a.cfg.Model), which would price every token at the
	// session model's rate regardless of which model was actually billed.
	// Incremented per round below, plus streamAux's aux calls (auxCostUSD) and
	// any delegated sub-agents' own cost (foldSubUsage) at turn end.
	var turnCostUSD float64
	// subAcct collects the token usage of any sub-agents delegated this turn (the
	// `task` tool, threaded via ctx). foldSubUsage merges it into turnUsage at
	// turn end so a delegation's tokens count toward the session totals and the
	// per-turn footer. Idempotent guard: fold exactly once.
	subAcct := &subUsageAcct{}
	ctx = withSubUsage(ctx, subAcct)
	// taskAcct collects each `task` delegation's footer (duration · cost · model
	// · calls), drained into the session at turn end (recordTaskMetas) so the web
	// UI can replay each task card's footer when a saved session reopens.
	taskAcct := &taskMetaAcct{}
	ctx = withTaskMetas(ctx, taskAcct)
	// Thread the shared agent message board (Option C-1) so the orchestrator and
	// any sub-agents it spawns can talk to each other this turn (post_message /
	// read_messages). Create it only if absent: the outermost RunWith makes it
	// (and marks itself board admin, so it can read the whole board), and every
	// descendant sub-agent's RunWith inherits the SAME board, so the whole
	// delegation tree shares one board. Skipped entirely when messaging is
	// disabled. Stamp the orchestrator's identity only when unset, so a
	// sub-agent's persona name (set by the task tool before it runs) is never
	// clobbered.
	if agentMessagingEnabled() && messageBoardFrom(ctx) == nil {
		ctx = withMessageBoard(ctx, &messageBoard{})
		ctx = withBoardAdmin(ctx, true)
		if agentIdentityFrom(ctx) == "" {
			ctx = withAgentIdentity(ctx, a.activeAgent().Name)
		}
		// Thread a board emitter pinned to THIS turn's UI sink, set once at the
		// root and never overwritten, so even a sub-agent's post_message (running
		// in its isolated loop) surfaces live in the UI — the per-agent emit
		// closures don't forward arbitrary events. Posts are also persisted at
		// turn end (recordBoardMessages) for replay on reopen.
		ctx = withBoardEmitter(ctx, func(m boardMessage) {
			emit(BoardMessagePosted{Seq: m.Seq, From: m.From, To: m.To, Text: m.Text})
		})
	}
	// board is the shared message board for this turn (nil when messaging is
	// disabled); recordBoardMessages drains it into the session at turn end.
	board := messageBoardFrom(ctx)
	subFolded := false
	foldSubUsage := func() {
		if subFolded {
			return
		}
		subFolded = true
		in, out, cacheRead, cost := subAcct.totals()
		turnUsage.InputTokens += in
		turnUsage.OutputTokens += out
		turnUsage.CacheReadInputTokens += cacheRead
		turnCostUSD += cost // real cost at each sub-agent's own model, not re-priced at a.cfg.Model
	}
	if a.snapshots != nil {
		a.snapshots.startTurn()
	}

	// Reload persisted allow/deny rules so edits made between turns (via
	// /permissions, the web UI's Tools menu, or a hand-edited permissions.json)
	// take effect on the next turn rather than only on restart.
	if a.perms != nil {
		a.perms = permissions.Load(a.cfg.WorkingDir)
	}

	// Plumb todo updates from the tool back to the TUI for the duration of
	// this turn. Cleared on return so callbacks don't fire after Run exits.
	if a.todos != nil {
		a.todos.SetNotify(func(items []tools.Todo) { emit(TodosUpdated{Items: items}) })
		defer a.todos.SetNotify(nil)
	}

	// Plumb plan drafts from the plan_write tool back to the host: sync the
	// draft onto the session (so it persists) and surface it as a PlanUpdated
	// event. A new draft replaces any prior plan and resets it to draft status.
	if a.plan != nil {
		a.plan.SetNotify(func(p *tools.Plan) {
			a.applyDraftPlan(p)
			emit(PlanUpdated{Plan: a.Plan()})
		})
		defer a.plan.SetNotify(nil)
	}

	// Retire a stale plan the user has moved past, at the START of a fresh
	// top-level turn (so a just-drafted/just-approved plan still gets its
	// presentation/execution turn first — see planRanATurn):
	//   - an approved plan that already executed and has no step still pending
	//     (retireExecutedPlan), even if carried out without a tracked todo list; and
	//   - an unapproved DRAFT that already sat through a turn while NOT in plan mode
	//     (retireStaleDraft) — otherwise its execution gate keeps bash/edit disabled
	//     and its "awaiting approval" context haunts every later, unrelated answer.
	// Plan-mode drafts (active refinement) are preserved.
	if topLevel && (a.retireExecutedPlan() || a.retireStaleDraft()) {
		emit(PlanUpdated{Plan: a.Plan()})
	}

	if a.hooks != nil {
		_ = a.hooks.Run(ctx, hooks.PreTurn, hooks.Context{WorkingDir: a.cfg.WorkingDir})
		defer func() {
			_ = a.hooks.Run(ctx, hooks.PostTurn, hooks.Context{WorkingDir: a.cfg.WorkingDir})
		}()
	}

	// "@ultrathink" turns this turn into a deep dive. Detect it on the raw prompt,
	// then strip the trigger token so the model gets a clean request.
	ultra := config.IsUltrathink(userInput)
	if ultra {
		userInput = config.StripUltrathink(userInput)
	}

	// Expand @persona / @worker / @workflow mentions into a dispatch directive
	// (top-level turn only — a sub-agent's prompt is already fully formed). The
	// model still drives the task/delegate/run_workflow call.
	if topLevel {
		userInput = a.expandTurnMentions(userInput)
	}

	a.messages = append(a.messages, buildUserMessage(userInput, attachments))

	// Before the first model call of a user turn, tell the user if the backend
	// (Ollama) still has to load the selected model — otherwise the slow first
	// response looks like a hang. Only on the top-level turn; best-effort and quick.
	if topLevel {
		if pn, ok := a.client.(preTurnNoticer); ok {
			if msg := pn.PreTurnNotice(llm.WithModel(ctx, a.cfg.Model)); msg != "" {
				emit(Info{Text: msg})
			}
		}
	}

	// Fresh read-dedup state per turn (a new turn may legitimately re-read files
	// whose earlier results were summarized/pruned away).
	a.turnReadCalls = map[string]bool{}
	// Fresh friction tally per turn — the "auto" reasoning ramp escalates on
	// THIS turn's tool failures, not on history.
	a.turnFriction.Store(0)
	// Fresh aux-cost tally per turn — see turnAuxCostBits/addAuxCost/auxCostUSD.
	a.resetAuxCost()
	// Proactive difficulty routing: obviously-hard requests skip the cheap
	// FastModel first attempt (under AdaptiveModel) and start on the strong model,
	// avoiding a wasted weak round + escalation lag. A missed "hard" is still
	// caught by the reactive friction escalation below; a false "hard" only spends
	// the strong model on an easy turn (no quality loss). See turnLooksHard.
	a.turnHard = turnLooksHard(userInput)
	a.turnThinkHard.Store(0) // and a fresh model-driven "heavy thinking" tally

	if err := a.maybeCompact(ctx, emit); err != nil {
		text := "compaction skipped: " + err.Error()
		emit(Info{Text: text})
		a.noteInfo(text)
	}

	// The turn runs up to MaxAgentSteps tool rounds, then auto-continues that
	// many more up to AutoContinue times (so long tasks finish without a manual
	// "continue"); the product is a hard ceiling against runaway loops.
	soft := a.cfg.MaxAgentSteps
	if soft < 1 {
		soft = 1
	}
	ceiling := soft * (1 + max(a.cfg.AutoContinue, 0))

	// nudges counts how many times the goal judge has rejected a stop this turn,
	// bounding the keep-going loop so a judge/model disagreement can't spin.
	nudges := 0
	// verifyAttempts counts how many times the post-turn verification command
	// failed and was fed back to the model this turn, bounded by
	// cfg.VerifyMaxRetries so a persistent failure can't loop forever.
	verifyAttempts := 0
	// reviewAttempts bounds the self-review feedback to one cycle per turn (see
	// maybeSelfReview / maxSelfReviewFeedback).
	reviewAttempts := 0

	// Runaway-cost guard: warn (once each) when this single turn crosses a soft
	// model-call count or dollar budget, so a long tool loop can't quietly burn
	// money. Advisory only — the step ceiling is the hard stop.
	warnCalls, warnCost := turnWarnThresholds()
	var warnedCalls, warnedCost bool
	// Hard runaway stop (opt-in): end the turn cleanly once it crosses these
	// limits, rather than only warning and running to the step ceiling.
	maxCalls, maxCost := turnHardLimits()

	// Claude-Code-style in-prompt thinking escalation: "think hard"/"think
	// harder"/"megathink" raise the reasoning effort for THIS turn only. Carried
	// on ctx so every model call in the turn (including tool rounds) uses it; ""
	// leaves the configured level untouched.
	turnEffort := a.reasoningKeywordEffort(userInput, emit)

	// "auto" reasoning: when no keyword applies, predict this turn's effort from
	// the request + session state (off for simple turns, up to high for hard ones;
	// see autoReasoningEffort). autoMode also gates the in-turn ramp below.
	autoMode := turnEffort == "" && config.IsAuto(a.cfg.ReasoningLevel)
	if autoMode {
		turnEffort = a.autoReasoningEffort(userInput)
	}

	// Track tool-round count so the NEXT "auto" turn can react to this turn's
	// difficulty (see autoReasoningEffort).
	rounds := 0
	defer func() { a.lastTurnHard = int(a.turnFriction.Load()) >= autoFrictionMedium }()

	// "@ultrathink" is the deepest mode: max thinking, plus — when sub-agents are
	// available — a directive (appended to the system prompt for this turn only,
	// no history pollution) telling the orchestrator to decompose the request and
	// fan out parallel sub-agents via the task tool, then synthesize.
	directive := ""
	if ultra {
		if eff := a.ultrathinkEffort(); eff != "" {
			turnEffort = eff // ultrathink always maxes thinking
		}
		if a.tools.Has("task") {
			directive = ultrathinkDirective
			emit(Info{Text: "ultrathink: deep-diving by spawning parallel sub-agents (max thinking)."})
		} else {
			emit(Info{Text: "ultrathink: maximum thinking for this turn."})
		}
	} else if a.cfg.AutoDelegate && a.tools.Has("task") && !a.PlanMode() {
		// Right-size delegation to the investigation's scope (config.ClassifyInvestigation):
		// read a narrow, single-target question inline (a sub-agent round-trip would cost
		// more than reading the one file), delegate a normal one to a single explore agent,
		// and fan a broad one out to parallel explore agents. Skipped in plan mode (task is
		// blocked there) and under ultrathink (handled above, which already fans out).
		switch config.ClassifyInvestigation(userInput) {
		case config.InvestigationBroad:
			directive = autoDelegateBroadDirective
			emit(Info{Text: "Broad investigation — fanning out parallel explore sub-agents to cover it. (MOONY_AUTO_DELEGATE=0 to disable.)"})
		case config.InvestigationNormal:
			directive = autoDelegateDirective
			emit(Info{Text: "Investigation detected — delegating to the read-only explore sub-agent to keep this turn small. (MOONY_AUTO_DELEGATE=0 to disable.)"})
		case config.InvestigationNarrow:
			// A single named target — the main agent reads it inline (no sub-agent round-trip).
		}
	}
	// The directive (decided once from the user's turn) and the volatile session
	// preface (checkpoint / plan / plan-mode steering) are combined into the system
	// prompt per STEP inside the loop below — not once here — because plan state can
	// change mid-turn (e.g. exit_plan flips plan→build within this same turn, so the
	// read-only steering must drop on the next step). appendTurnContext carries it
	// all in one uncached system suffix, so re-evaluating it per step is cache-neutral
	// (the cached system base and message-history prefix are untouched).

	// effortShown tracks the effort last applied this turn, so an in-turn ramp is
	// announced once (when it actually rises) rather than every step.
	effortShown := ""
	// modelEscalationShown ensures the one-time "escalating to <model>" notice (for
	// adaptive model routing) fires only on the transition, not every step after.
	modelEscalationShown := false
	// cheapStreakActive tracks whether the turn is currently running on the
	// cheaper FastModel via AdaptiveModel/CheapReads, so the "using <model> for
	// this round" notice below fires once per streak (not every round in it) but
	// fires again if a later round dips back down — lets you actually verify
	// MOONY_CHEAP_READS/MOONY_ADAPTIVE_MODEL are engaging, without spamming a line
	// per round.
	cheapStreakActive := false
	// lastStepPassive records whether the previous step only digested a routine,
	// benign tool batch (see passiveToolBatch) — if so, dynamic effort routing
	// lowers the NEXT model call's reasoning floor. False on the first step (the
	// model is responding to the user's prompt) and after any error/feedback.
	lastStepPassive := false
	// lastStepReadOnly is set when the previous step consisted entirely of
	// idempotent read tools (read_file/grep/glob/find_symbol/…) with no new
	// friction — used by the CheapReads routing to send pure-exploration rounds
	// to the cheaper FastModel.
	lastStepReadOnly := false
	// readBytesThisTurn accumulates the size of read-style tool output produced
	// this turn. Once it crosses AutoOffloadReadBytes we flip on the explore
	// delegation directive (below) so further investigation runs in a throwaway
	// sub-agent context instead of continuing to bill this turn on every round.
	readBytesThisTurn := 0
	for step := 0; step < ceiling; step++ {
		rounds = step + 1
		if err := ctx.Err(); err != nil {
			return err
		}
		// Notice each time the turn extends past a soft boundary.
		if step > 0 && step%soft == 0 {
			emit(Info{Text: fmt.Sprintf("Auto-continuing (%d/%d) past %d steps…", step/soft, a.cfg.AutoContinue, step)})
		}
		emit(StepUpdate{Step: step + 1, Max: ceiling})

		// Elide tool results that have scrolled out of the recent window so a
		// long tool loop stops re-sending stale Read/Grep/Bash output verbatim
		// on every step (the dominant cost of long turns). Keeps the most recent
		// rounds intact; older ones become a short stub in place.
		a.pruneStaleToolResults(a.cfg.ToolResultKeepRounds)

		// A single turn can run many tool-call steps (especially under
		// AutoContinue); maybeCompact only runs once at RunTurn's start, so a
		// long-running turn could otherwise blow past CompactThresholdTokens
		// mid-flight and never compact until the *next* turn starts. Re-check
		// here each step (step 0 was already covered above); estTokens is cheap
		// and maybeCompact itself no-ops below the threshold, so this only pays
		// the summarizer-call cost when actually needed.
		if step > 0 {
			if err := a.maybeCompact(ctx, emit); err != nil {
				text := "compaction skipped: " + err.Error()
				emit(Info{Text: text})
				a.noteInfo(text)
			}
		}

		// Re-evaluate the per-turn system suffix each step so a mid-turn plan/mode
		// change (e.g. exit_plan) is reflected immediately. Cache-neutral: the suffix
		// is uncached (see appendTurnContext), so the cached prefix is unaffected, and
		// across steps where nothing changed it's byte-identical.
		turnSystem := appendTurnContext(a.system, directive, a.turnContextPreface())

		// In-turn ratchet: on "auto", raise the reasoning effort as the turn proves
		// hard (more tool rounds), so the CURRENT turn gets more thinking when it
		// needs it — not just the next one. Up-only (HigherReasoning never lowers the
		// predicted base and the floor only rises with rounds), so thinking is never
		// toggled back off mid-turn.
		effort := turnEffort
		if autoMode && !ultra {
			ramp := a.rampedAutoEffort(turnEffort, int(a.turnFriction.Load()))
			// Model-driven floor (#2): if adaptive thinking has been engaging heavily
			// this turn, raise the floor for the rest of it — the model's own read of
			// the task's difficulty, complementing the failure-driven friction ramp.
			ramp = config.NormalizeReasoningFor(a.cfg.Model, config.HigherReasoning(ramp, a.modelThinkFloor()))
			// Announce only genuine friction-driven rises, comparing the RAMPED
			// value (not the passive-dipped one) so a routine dip returning to base
			// isn't mislabeled as "hitting tool errors".
			if effortShown != "" && config.ReasoningRank(ramp) > config.ReasoningRank(effortShown) {
				emit(Info{Text: fmt.Sprintf("auto: this turn is hitting friction — raising reasoning to %s.", ramp)})
			}
			effortShown = ramp
			effort = ramp
			// Dynamic effort routing: a step that only digests a routine/benign tool
			// batch (edits, a listing, a todo update — no errors) is a passive
			// continuation, so lower its reasoning floor to save tokens. The prompt
			// prediction (new request) and the friction ramp (tool errors) above
			// still elevate effort on active steps.
			if a.cfg.DynamicEffort && lastStepPassive {
				effort = a.passiveEffort(ramp)
			}
		}

		// Adaptive model routing (opt-in): run the cheaper fast model until the turn
		// shows sustained friction (tool errors / rejected stops), then escalate to
		// the session model for the rest of the turn — easy turns cost less, hard
		// ones still get the strong model. Sticky (turnFriction only rises within a
		// turn) and a no-op when FastModel == the session model (already cheap, or a
		// non-hosted backend) or on an ultrathink turn.
		turnModel := a.cfg.Model
		turnProvider := "" // "" = inherit the session provider; set on a cross-provider downshift
		var downshiftReason string
		if a.cfg.AdaptiveModel && !ultra {
			if fast, fastProv, differs := config.FastModelRoute(a.cfg.Model, a.cfg.LLMProvider); differs {
				underFriction := int(a.turnFriction.Load()) >= autoFrictionMedium
				switch {
				case a.turnHard:
					// obviously-hard turn: start on the strong model, no notice.
				case underFriction:
					if !modelEscalationShown {
						modelEscalationShown = true
						emit(Info{Text: fmt.Sprintf("auto: escalating to %s for the rest of this turn (it's hitting friction).", a.cfg.Model)})
					}
				default:
					turnModel = fast
					turnProvider = fastProv
					downshiftReason = "adaptive model"
				}
			}
		}
		// CheapReads routing (opt-in, MOONY_CHEAP_READS=1): if the previous step was
		// entirely idempotent read tools and the turn isn't hard / under friction,
		// run this round on the cheaper FastModel — pure exploration rarely needs
		// the strong model. The strong model is restored the moment the model starts
		// writing, runs bash, or encounters any friction. Composes with AdaptiveModel
		// (either alone can downgrade; friction/hard/ultrathink override both).
		if a.cfg.CheapReads && !ultra && !a.turnHard && lastStepReadOnly {
			if fast, fastProv, differs := config.FastModelRoute(a.cfg.Model, a.cfg.LLMProvider); differs {
				if int(a.turnFriction.Load()) < autoFrictionMedium {
					turnModel = fast
					turnProvider = fastProv
					if downshiftReason == "" {
						downshiftReason = "cheap reads"
					} else {
						downshiftReason = "adaptive model + cheap reads"
					}
				}
			}
		}
		// Surface a one-shot notice per downshift streak so MOONY_ADAPTIVE_MODEL /
		// MOONY_CHEAP_READS engaging (or not) is directly observable, instead of a
		// silent model switch you can only infer from the footer's cost/model.
		if downshiftReason != "" {
			if !cheapStreakActive {
				cheapStreakActive = true
				// Name the provider, the (full, unmangled) model id, and the reasoning
				// level this round actually uses — so a cross-provider / cross-reasoning
				// fast tier is visible, not just "using <model>". Provider defaults to
				// the session provider; reasoning to the Fast level, else the turn's.
				prov := turnProvider
				if prov == "" {
					prov = a.cfg.LLMProvider
				}
				provLabel := prov
				if p, ok := provider.Lookup(prov); ok {
					provLabel = p.Label
				}
				reasoning := config.FastReasoning()
				if reasoning == "" {
					reasoning = effort
				}
				if reasoning == "" {
					reasoning = "default"
				}
				text := fmt.Sprintf("auto: using %s on %s (reasoning: %s) for this round (%s).", turnModel, provLabel, reasoning, downshiftReason)
				emit(Info{Text: text})
				a.noteInfo(text) // persist so the routing note replays on reload
			}
		} else {
			cheapStreakActive = false
		}

		// The reasoning level ACTUALLY applied to this round's model call — the
		// recordTurnMeta counterpart of turnModel. Must mirror the callCtx
		// derivation just below, or a downshifted round's persisted footer
		// misleadingly shows the main model's configured reasoning instead of the
		// Fast tier's.
		turnReasoning := effort
		if turnReasoning == "" {
			turnReasoning = a.cfg.ReasoningLevel
		}
		if downshiftReason != "" {
			if fr := config.FastReasoning(); fr != "" {
				turnReasoning = fr
			}
		}

		// Thread the turn's model + effort. On a downshifted round, also route to
		// the Fast tier's provider (if cross-provider) and apply the Fast reasoning
		// level; both are no-ops when unset, so a non-downshifted round is
		// byte-identical to before.
		callCtx := llm.WithEffort(ctx, effort)
		if downshiftReason != "" {
			if turnProvider != "" {
				callCtx = llm.WithProvider(callCtx, turnProvider)
			}
			callCtx = llm.WithEffort(callCtx, config.FastReasoning()) // "" => no-op, keeps `effort`
		}
		resp, err := a.client.StreamMessages(llm.WithModel(callCtx, turnModel), turnSystem, a.messages, a.visibleTools(), llm.Handler{
			OnTextDelta:     func(s string) { emit(TextDelta{Text: s}) },
			OnThinkingDelta: func(s string) { emit(ThinkingDelta{Text: s}) },
			OnRetry: func(err error) {
				// Transient stream failure (e.g. a read timeout); the client is
				// re-sending. Drop the partial output and tell the user.
				emit(StreamReset{})
				emit(Info{Text: "Connection interrupted (" + streamErrReason(err) + ") — retrying…"})
			},
		})
		if err != nil {
			return err
		}
		a.messages = append(a.messages, resp.AssistantMessage())
		// Empty-response notice: some provider routes (e.g. an unconfigured backend
		// behind an OpenAI-compatible gateway) return HTTP 200 with no content at
		// all, which would otherwise end the turn silently and look like a freeze.
		// Surface it so a dead model/route is obvious and actionable.
		if !responseHasAnswer(resp) {
			emit(Info{Text: fmt.Sprintf("The model returned an empty response (no content) — %q or its provider route may be unavailable. Try a different model with /model.", turnModel)})
		}
		// Model-driven difficulty signal (#2): when adaptive thinking engaged heavily
		// on this step (a thinking block present AND a large output), count it so the
		// auto floor rises for the rest of the turn (see modelThinkFloor). Only in
		// auto mode (ultra is already max); gated on both signals because thinking
		// tokens aren't reported separately (they fold into OutputTokens), so a bare
		// token count would misread a big edit as heavy reasoning.
		if autoMode && !ultra && responseThought(resp) && resp.Usage.OutputTokens >= autoThinkTokens {
			a.turnThinkHard.Add(1)
		}
		a.debugReasoning(rounds, turnModel, turnEffort, effort, lastStepPassive, responseThought(resp), resp.Usage.OutputTokens)
		// Cost covers the whole turn INCLUDING tokens billed by any failed,
		// retried stream attempts (RetryUsage) — so the per-turn footer and the
		// runaway-cost guard reflect true spend on a flaky connection, not just
		// the successful calls. The context-size indicators use resp.Usage (the
		// final call) and are unaffected.
		turnUsage.InputTokens += resp.Usage.InputTokens + resp.RetryUsage.InputTokens
		turnUsage.OutputTokens += resp.Usage.OutputTokens + resp.RetryUsage.OutputTokens
		// Price THIS round at turnModel — whichever model actually served it
		// (the session model, or the cheaper FastModel when AdaptiveModel/
		// CheapReads downshifted this round) — rather than pricing every round's
		// tokens at a.cfg.Model at the end. Without this, a downshifted round's
		// cheaper tokens were overstated at the pricier session-model rate.
		turnCostUSD += resp.Usage.CostUSD(turnModel) + resp.RetryUsage.CostUSD(turnModel)
		// Cache-hit telemetry: sum the cached-input portion across calls so the
		// per-turn footer can show how much of the re-sent context was served
		// from the prompt cache (the key signal for why a long turn is or isn't
		// expensive). Cached tokens are a subset of InputTokens, already counted
		// above — these fields are display-only.
		turnUsage.CacheReadInputTokens += resp.Usage.CacheReadInputTokens
		turnUsage.CacheCreationInputTokens += resp.Usage.CacheCreationInputTokens

		// Calibrate the bytes/4 token estimate against the provider's reported
		// input_tokens for the messages just sent (all but the assistant reply we
		// appended above), so compaction triggers track real context size. See
		// calibrateTokens.
		if n := len(a.messages); n > 1 {
			a.calibrateTokens(resp.Usage.InputTokens, a.messages[:n-1])
		}

		// Runaway-cost guard (advisory): warn once when this turn crosses the soft
		// call-count or cost budget. rounds is the number of model calls so far.
		if warnCalls > 0 && !warnedCalls && rounds >= warnCalls {
			warnedCalls = true
			emit(Info{Text: fmt.Sprintf("Heads up: this turn has made %d model calls (it will keep going up to %d). Interrupt to stop, or lower the cap with MOONY_MAX_STEPS / MOONY_AUTO_CONTINUE.", rounds, ceiling)})
		}
		if warnCost > 0 && !warnedCost {
			if cost := turnCostUSD + a.auxCostUSD(); cost >= warnCost {
				warnedCost = true
				emit(Info{Text: fmt.Sprintf("Heads up: this turn has cost ~$%.2f so far (%d calls). Interrupt to stop, switch to a cheaper model with /model, or lower /reasoning.", cost, rounds)})
			}
		}

		// Hard runaway stop (opt-in): end the turn cleanly once it crosses the
		// configured hard call/cost limit, instead of only warning and running on
		// to the step ceiling. The session can be resumed with "continue".
		if reason := a.turnHardStopReason(maxCalls, maxCost, rounds, turnCostUSD+a.auxCostUSD()); reason != "" {
			emit(Info{Text: reason})
			foldSubUsage() // include delegated sub-agents' tokens in this turn's totals + footer
			finalCostUSD := turnCostUSD + a.auxCostUSD()
			a.recordTurnMeta(turnStart, turnUsage, finalCostUSD, rounds, turnModel, turnReasoning)
			a.recordTaskMetas(taskAcct)
			a.recordBoardMessages(board)
			emit(TurnDone{Usage: resp.Usage, TurnUsage: turnUsage, CostUSD: finalCostUSD, Model: turnModel, Reasoning: turnReasoning})
			a.persist()
			a.maybeAutoTitle(ctx, emit)
			a.maybeCheckpoint(ctx, emit)
			return nil
		}

		// Accrue usage toward the goal's token budget; stop the moment it's spent.
		a.addGoalTokens(resp.Usage)
		if a.goalBudgetExceeded() {
			emit(Info{Text: fmt.Sprintf("Goal token budget reached (%d/%d) — stopping. Raise it with /goal budget <n>, remove it with /goal budget off, or send a message to keep going.", a.goalTokensUsed, a.goalBudgetTokens)})
			foldSubUsage() // include delegated sub-agents' tokens in this turn's totals + footer
			finalCostUSD := turnCostUSD + a.auxCostUSD()
			a.recordTurnMeta(turnStart, turnUsage, finalCostUSD, rounds, turnModel, turnReasoning)
			a.recordTaskMetas(taskAcct)
			a.recordBoardMessages(board)
			emit(TurnDone{Usage: resp.Usage, TurnUsage: turnUsage, CostUSD: finalCostUSD, Model: turnModel, Reasoning: turnReasoning})
			a.persist()
			a.maybeAutoTitle(ctx, emit)
			a.maybeCheckpoint(ctx, emit)
			return nil
		}

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			// With a goal set, don't stop on the model's say-so alone: ask the
			// judge whether the stop-condition is truly met. If not, inject its
			// feedback as a synthetic turn and keep going (bounded per turn so a
			// judge/model standoff can't loop forever).
			if a.Goal() != "" {
				a.ensureGoalCriteria(ctx, emit) // propose criteria on the first check
				switch {
				case a.goalBlocked:
					// Already blocked: stop nudging until the goal is amended.
					emit(Info{Text: "Goal is marked BLOCKED (repeatedly unmet without progress) — stopping. Refine it with /goal <new>, adjust /goal criteria, or /goal clear."})
				case nudges < maxGoalNudges:
					emit(Info{Text: "Goal not confirmed yet — checking the stop condition…"})
					verdict := a.evaluateGoal(ctx)
					if !verdict.met {
						nudges++
						a.turnFriction.Add(1) // a rejected stop is friction: the turn isn't converging
						emit(Info{Text: "Stop condition not met; continuing. " + verdict.feedback})
						a.messages = append(a.messages, goalNudgeMessage(verdict.feedback))
						a.persist()
						lastStepPassive = false  // digesting rejection feedback is an active step
						lastStepReadOnly = false // writing feedback is never pure-read
						continue
					}
					a.noteGoalMet()
					emit(Info{Text: "Stop condition satisfied."})
				default:
					// Hit the per-turn nudge cap without the judge confirming the
					// goal. Count it toward the blocked state and stop for now.
					if a.noteGoalGaveUp() {
						emit(Info{Text: fmt.Sprintf("Goal still not met after %d turns of trying — marking it BLOCKED and stopping. Refine it with /goal <new>, adjust /goal criteria, or /goal clear.", blockedThreshold)})
					} else {
						emit(Info{Text: fmt.Sprintf("Goal still not confirmed after %d checks — stopping for now. Send a message (e.g. \"continue\") to keep working toward it, or /goal clear to drop the stop condition.", maxGoalNudges)})
					}
					a.persist()
				}
			}
			// Verify the turn's edits (the configured build/test); on failure feed
			// the errors back and keep going so the model fixes them before the
			// turn finishes (bounded by cfg.VerifyMaxRetries). No-op when verify is
			// off or nothing was edited this turn.
			if a.maybeVerify(ctx, emit, &verifyAttempts) {
				lastStepPassive = false  // feeding build/test failures back is an active step
				lastStepReadOnly = false // verify feedback is not a pure-read step
				continue
			}
			// Self-review the turn's own diff for introduced bugs (opt-in,
			// MOONY_SELF_REVIEW); feed any finding back once so the model fixes it
			// before finishing. No-op when disabled or nothing was edited this turn.
			if a.maybeSelfReview(ctx, emit, &reviewAttempts) {
				lastStepPassive = false  // feeding a review finding back is an active step
				lastStepReadOnly = false // review feedback is not a pure-read step
				continue
			}
			foldSubUsage() // include delegated sub-agents' tokens in this turn's totals + footer
			finalCostUSD := turnCostUSD + a.auxCostUSD()
			a.recordTurnMeta(turnStart, turnUsage, finalCostUSD, rounds, turnModel, turnReasoning)
			a.recordTaskMetas(taskAcct)
			a.recordBoardMessages(board)
			emit(TurnDone{Usage: resp.Usage, TurnUsage: turnUsage, CostUSD: finalCostUSD, Model: turnModel, Reasoning: turnReasoning})
			a.persist()
			// An approved plan whose mirrored todos are all resolved is finished:
			// mark it done so its "carry it out" context stops being re-injected.
			if a.maybeCompletePlan() {
				emit(PlanUpdated{Plan: a.Plan()})
			} else if p := a.Plan(); p != nil && len(p.Steps) > 0 && p.Status != session.PlanDone {
				// A plan (draft or approved) survived this turn; a LATER turn may
				// retire it — a finished approved plan, or a draft the user moved past.
				// An approved plan the user has stopped driving is retired here once
				// it's gone idle (no step progress) across the grace window.
				ranBefore := a.planRanATurn
				a.planRanATurn = true
				if a.trackPlanAbandon(ranBefore) {
					emit(PlanUpdated{Plan: a.Plan()})
				}
			}
			a.maybeAutoTitle(ctx, emit)      // name the session after its first turn
			a.maybeCheckpoint(ctx, emit)     // refresh the distilled state snapshot
			a.maybeRefreshProfile(ctx, emit) // refresh the cross-session project profile (opt-in)
			return nil
		}

		frictionBefore := a.turnFriction.Load()
		results := a.runToolCalls(ctx, emit, calls)
		a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: results})
		// Auto-offload big reads: if this turn keeps reading files directly and we
		// aren't already delegating, flip on the explore-delegation directive for
		// subsequent steps so continued investigation runs in a throwaway sub-agent
		// context (its bulky reads never re-bill this turn). Gated by AutoDelegate,
		// needs the task tool, and is skipped in plan mode and under ultrathink
		// (which already fans out via a directive set at turn start).
		if directive == "" && a.cfg.AutoDelegate && a.cfg.AutoOffloadReadBytes > 0 &&
			!ultra && !a.PlanMode() && a.tools.Has("task") {
			readIDs := readToolCallIDs(calls)
			for _, r := range results {
				if r.Type == llm.BlockToolResult && readIDs[r.ToolUseID] {
					readBytesThisTurn += len(r.Content)
				}
			}
			if readBytesThisTurn >= a.cfg.AutoOffloadReadBytes {
				directive = autoDelegateDirective
				emit(Info{Text: fmt.Sprintf("Read ~%d KB directly this turn — steering further investigation to the read-only explore sub-agent to keep this context small. (MOONY_AUTO_DELEGATE=0 to disable.)", readBytesThisTurn/1024)})
			}
		}
		// Classify the step just run for dynamic effort routing on the NEXT model
		// call: passive iff it used only continuation tools and added no friction
		// (no tool errors / non-zero bash exits this step).
		lastStepPassive = passiveToolBatch(calls) && a.turnFriction.Load() == frictionBefore
		lastStepReadOnly = readOnlyToolBatch(calls) && a.turnFriction.Load() == frictionBefore
		a.persist()
	}

	return stepCeilingError{ceiling: ceiling}
}

// errStepCeiling marks the "ran out of tool steps" stop. Callers can detect it
// with errors.Is — notably the task tool, so a delegated sub-agent that can't be
// resumed with "continue" returns its partial work instead of hard-failing.
var errStepCeiling = errors.New("step ceiling reached")

// stepCeilingError is the turn-ceiling stop. It carries the user-facing message
// (unchanged) while matching errStepCeiling via errors.Is.
type stepCeilingError struct{ ceiling int }

func (e stepCeilingError) Error() string {
	return fmt.Sprintf("stopped after %d tool steps without finishing — send another message (e.g. \"continue\") to resume, or raise the cap with MOONY_MAX_STEPS / MOONY_AUTO_CONTINUE", e.ceiling)
}

func (stepCeilingError) Is(target error) bool { return target == errStepCeiling }

// autoFrictionMedium and autoFrictionHigh are the per-turn friction counts (tool
// failures: execution errors, non-zero bash exits, goal-judge rejections) at
// which an "auto" turn lifts its reasoning floor to medium and high respectively.
// Friction — not raw tool-round count — is the difficulty signal: a turn doing
// many cheap reads/greps isn't hard, but one repeatedly hitting errors is. The
// first isolated failure (friction 1) doesn't escalate; sustained trouble does.
const (
	autoFrictionMedium = 2
	autoFrictionHigh   = 4
)

// autoThinkTokens is the per-step OUTPUT-token count above which — combined with a
// thinking block being present — a step counts as "heavy thinking" for the
// model-driven auto floor (see turnThinkHard / modelThinkFloor). Output tokens are
// only a proxy (the API folds thinking into OutputTokens rather than reporting it
// separately), so the threshold is deliberately generous and gated on a thinking
// block; tune it against MOONY_REASONING_DEBUG logs.
const autoThinkTokens = 1500
