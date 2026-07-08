package agent

// RunScoped runs a single, isolated agent turn with a custom system prompt, an
// optional tool allowlist, and a model override — capturing the final assistant
// text plus token usage/cost — without persistence or interactive approval. It's
// the reusable building block behind the Agent Builder's workflow execution: each
// "agent" node in a workflow is one RunScoped call. The implementation mirrors the
// `task` tool's sub-agent path (a bare scoped Agent over the shared LLM client),
// which is the proven way to run an agent loop and harvest just its output.

import (
	"context"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/tools"
)

// ScopedOptions configures a RunScoped call. All fields are optional.
type ScopedOptions struct {
	System     string   // system prompt; empty → the default build prompt for the registry
	Tools      []string // tool allowlist by name; empty → all default tools
	Skills     []string // Agent Skills to make loadable (the `skill` tool); empty → none
	Model      string   // model id override; empty → base.Model
	WorkingDir string   // tool root; empty → base.WorkingDir

	// ApprovalPolicy overrides the base config's policy for this scoped run
	// ("prompt" | "safe" | "auto"); empty → inherit base. Use "prompt" together
	// with OnApproval to make a workflow agent node pause for mutating tools.
	ApprovalPolicy string

	// OnApproval, when non-nil, is consulted for each tool-approval request instead
	// of auto-approving: it receives the tool name, a human summary, and a diff (if
	// any) and returns true to approve. Nil → headless auto-approve (the prior
	// behavior). When set, the run is treated as interactive.
	OnApproval func(tool, summary, diff string) bool

	// OnQuestion, when non-nil, answers an ask_user question: it receives the
	// question, the options (label/description pairs), and whether multiple may be
	// chosen, and returns the chosen label(s). Nil → the question is dismissed
	// (empty answer), matching the old headless behavior (which would otherwise
	// hang, as ask_user was previously unhandled here).
	OnQuestion func(question string, options []QuestionOption, multiple bool) []string
}

// ScopedResult is the outcome of a RunScoped call.
type ScopedResult struct {
	Text    string    // the final assistant text (trimmed)
	Usage   llm.Usage // summed token usage across the turn (incl. auto-continue)
	CostUSD float64   // summed dollar cost across the turn
}

// RunScoped executes one turn and returns its text + usage. onDelta, when
// non-nil, receives streamed assistant text chunks as they arrive (for live
// preview); it may be called concurrently if the agent runs tools in parallel, so
// it must be safe for concurrent use. Approval requests are auto-approved (a
// headless run can't prompt); ctx cancellation stops the turn.
func RunScoped(ctx context.Context, client llm.Streamer, base *config.Config, opts ScopedOptions, prompt string, onDelta func(string)) (ScopedResult, error) {
	// Derive a scoped config from the base, overriding model + working dir.
	cfg := *base
	if opts.Model != "" {
		cfg.Model = opts.Model
	}
	if opts.WorkingDir != "" {
		cfg.WorkingDir = opts.WorkingDir
	}
	if strings.TrimSpace(opts.ApprovalPolicy) != "" {
		cfg.ApprovalPolicy = config.NormalizeApprovalPolicy(opts.ApprovalPolicy)
	}

	// Build an isolated registry: only the allowlisted tools, or all defaults.
	var registry *tools.Registry
	if len(opts.Tools) > 0 {
		allowed := make(map[string]bool, len(opts.Tools))
		for _, n := range opts.Tools {
			allowed[n] = true
		}
		registry = tools.NewRegistry(cfg.WorkingDir)
		tools.RegisterAllowed(registry, cfg.MaxToolOutputBytes, allowed)
	} else {
		registry = tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	}

	system := opts.System
	if strings.TrimSpace(system) == "" {
		system = buildSystemPrompt(&cfg, registry)
	}

	a := &Agent{
		cfg:             &cfg,
		client:          client,
		tools:           registry,
		system:          system,
		alwaysAllow:     map[string]bool{},
		enabledToolsets: map[string]bool{},
	}
	// A run with an approval hook can answer interactive prompts (e.g. the
	// git-commit hard gate), so mark it interactive; a purely headless run (no
	// hook) stays non-interactive and such gates are denied rather than auto-run.
	if opts.OnApproval != nil {
		a.interactive = true
	}

	// Make the selected Agent Skills loadable via the `skill` tool. Filter the
	// connector's discovered skills down to the requested names.
	if len(opts.Skills) > 0 {
		want := make(map[string]bool, len(opts.Skills))
		for _, n := range opts.Skills {
			want[n] = true
		}
		var chosen []skills.Skill
		for _, sk := range skills.Load(cfg.WorkingDir) {
			if want[sk.Name] {
				chosen = append(chosen, sk)
			}
		}
		a.AttachSkills(chosen)
	}

	var text strings.Builder
	var res ScopedResult
	emit := func(ev Event) {
		switch e := ev.(type) {
		case TextDelta:
			text.WriteString(e.Text)
			if onDelta != nil {
				onDelta(e.Text)
			}
		case TurnDone:
			u := e.TurnUsage
			if u.InputTokens == 0 && u.OutputTokens == 0 {
				u = e.Usage
			}
			res.Usage.InputTokens += u.InputTokens
			res.Usage.OutputTokens += u.OutputTokens
			res.Usage.CacheReadInputTokens += u.CacheReadInputTokens
			res.Usage.CacheCreationInputTokens += u.CacheCreationInputTokens
			res.CostUSD += e.CostUSD
		case ApprovalRequest:
			// With an approval hook, ask the host (which surfaces it to the user and
			// blocks); otherwise headless auto-approve (the run is user-initiated).
			if opts.OnApproval != nil {
				if opts.OnApproval(e.Tool, e.Summary, e.Diff) {
					e.Reply <- Approve
				} else {
					e.Reply <- Deny
				}
			} else {
				e.Reply <- Approve
			}
		case QuestionRequest:
			// With a question hook, put the question to the user and return their
			// choice; otherwise dismiss it (empty answer). Previously this event was
			// unhandled, so an ask_user call inside a node hung until ctx cancel.
			if opts.OnQuestion != nil {
				e.Reply <- opts.OnQuestion(e.Question, e.Options, e.Multiple)
			} else {
				e.Reply <- nil
			}
		}
	}

	err := a.Run(ctx, prompt, emit)
	res.Text = strings.TrimSpace(text.String())
	return res, err
}
