package tools

// ask_user lets the model pause and ask the human a multiple-choice question
// when it is genuinely blocked on a decision only the user can make (e.g. an
// ambiguous requirement or a fork in approach). Unlike other tools it has no
// inline result — it must round-trip to the UI and block for the user's pick.
// The Tool interface has no emit channel, so the host (agent) stashes an "asker"
// callback in the context (see WithUserAsker), exactly as the delegate tool gets
// its live-progress emitter via withWorkerActivity. The asker presents the
// question, blocks until the user answers, and returns the chosen option
// label(s). When no asker is wired (headless runs, sub-agents, tests) the tool
// returns an error so the model falls back to proceeding on its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// maxAskOptions caps how many choices a single ask_user call may present, so the
// UI list stays scannable and the model is nudged toward a focused question.
const maxAskOptions = 10

// maxAskQuestions caps how many questions one ask_user call may pose in a single
// multi-question form, so a runaway call can't spawn an unbounded prompt chain.
const maxAskQuestions = 8

// AskOption is one selectable choice presented to the user.
type AskOption struct {
	// Label is the short choice text shown as the option and returned as the
	// answer when picked.
	Label string `json:"label"`
	// Description is optional secondary text clarifying the choice.
	Description string `json:"description,omitempty"`
}

// AskRequest is a question plus its options, handed to the host's asker.
type AskRequest struct {
	Question string
	Options  []AskOption
	// Multiple allows the user to select more than one option.
	Multiple bool
	// AllowText lets the user type a free-text answer in addition to (or instead
	// of) picking an option. When set, Options may be empty (a pure text prompt).
	AllowText bool
}

// UserAsker presents a question to the user and blocks until they answer,
// returning the chosen option label(s). An empty result means the user
// dismissed the question without choosing. An error aborts the ask (e.g. the
// context was cancelled).
type UserAsker func(ctx context.Context, req AskRequest) ([]string, error)

// userAskCtxKey carries the per-turn asker callback through the context into the
// ask_user tool's Execute (the Tool interface has no emit channel).
type userAskCtxKey struct{}

// WithUserAsker returns ctx carrying the asker the ask_user tool will use. The
// host (agent) wires this per tool call, alongside the worker-activity emitter.
func WithUserAsker(ctx context.Context, fn UserAsker) context.Context {
	return context.WithValue(ctx, userAskCtxKey{}, fn)
}

// userAskerFrom returns the asker stashed in ctx, or nil when none is wired
// (headless, sub-agent, or test runs).
func userAskerFrom(ctx context.Context) UserAsker {
	if fn, ok := ctx.Value(userAskCtxKey{}).(UserAsker); ok {
		return fn
	}
	return nil
}

// UserBatchAsker presents SEVERAL questions to the user at once (the ask_user
// questions[] form) and blocks until they answer them all, returning one
// answer-slice per question in the same order. An empty inner slice means that
// question was dismissed. It lets a surface that can render a grouped panel (the
// web tabs UI) collect every answer in a single round-trip; a surface without
// one simply leaves it unset and the tool asks each question in turn instead.
type UserBatchAsker func(ctx context.Context, reqs []AskRequest) ([][]string, error)

// userBatchAskCtxKey carries the per-turn batch asker through the context.
type userBatchAskCtxKey struct{}

// WithUserBatchAsker returns ctx carrying the batch asker the ask_user tool uses
// for its questions[] form. The host (agent) wires this alongside WithUserAsker.
func WithUserBatchAsker(ctx context.Context, fn UserBatchAsker) context.Context {
	return context.WithValue(ctx, userBatchAskCtxKey{}, fn)
}

// userBatchAskerFrom returns the batch asker stashed in ctx, or nil when none is
// wired (surfaces without a grouped panel, headless, or tests).
func userBatchAskerFrom(ctx context.Context) UserBatchAsker {
	if fn, ok := ctx.Value(userBatchAskCtxKey{}).(UserBatchAsker); ok {
		return fn
	}
	return nil
}

// askUserTool implements the ask_user capability. It holds no state — the asker
// comes from the context per call.
type askUserTool struct{}

func (t *askUserTool) Name() string { return "ask_user" }

func (t *askUserTool) Description() string {
	return "Ask the user a multiple-choice question and wait for their answer. " +
		"Use this ONLY when you are genuinely blocked on a decision that only the " +
		"user can make — an ambiguous requirement, a meaningful fork in approach, " +
		"or a preference that materially changes the work — and you cannot make a " +
		"reasonable default choice yourself. Do NOT use it for routine confirmation " +
		"(mutating tools already prompt for approval) or to avoid doing research you " +
		"can do with other tools. Offer 2–5 concrete options; keep labels short. " +
		"Set multiple=true only when several options can be chosen together. Set " +
		"allow_text=true to let the user type a free-text answer instead of (or in " +
		"addition to) picking an option — useful for open-ended questions; with " +
		"allow_text you may pass fewer or even no options. To ask SEVERAL things at " +
		"once, pass a `questions` array (each item has the same shape) — they're " +
		"answered in turn and returned together, saving a round-trip per question. " +
		"The tool returns what the user picked or typed so you can continue."
}

func (t *askUserTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"question": {
				"type": "string",
				"description": "The question to ask the user. Be specific and self-contained."
			},
			"options": {
				"type": "array",
				"description": "The choices to offer (2-5 recommended). Each has a short label and an optional description.",
				"items": {
					"type": "object",
					"properties": {
						"label":       {"type": "string", "description": "Short choice text shown to the user and returned when picked."},
						"description": {"type": "string", "description": "Optional clarifying detail for this choice."}
					},
					"required": ["label"]
				}
			},
			"multiple": {
				"type": "boolean",
				"description": "Allow selecting more than one option. Defaults to false (single choice)."
			},
			"allow_text": {
				"type": "boolean",
				"description": "Let the user type a free-text answer instead of (or in addition to) picking an option. When true, options may be empty. Defaults to false."
			},
			"questions": {
				"type": "array",
				"description": "Ask several questions in ONE call (answered in turn, all returned together) instead of a round-trip per question. Each item is a full question (same shape as the top-level fields). When provided, the top-level question/options are ignored.",
				"items": {
					"type": "object",
					"properties": {
						"question":   {"type": "string", "description": "The question to ask. Be specific and self-contained."},
						"options":    {"type": "array", "items": {"type": "object", "properties": {"label": {"type": "string"}, "description": {"type": "string"}}, "required": ["label"]}, "description": "Choices to offer for this question."},
						"multiple":   {"type": "boolean", "description": "Allow selecting more than one option for this question."},
						"allow_text": {"type": "boolean", "description": "Let the user type a free-text answer for this question."}
					},
					"required": ["question"]
				}
			}
		}
	}`)
}

// Mutates is false: asking a question changes nothing and never needs approval.
func (t *askUserTool) Mutates() bool { return false }

// askQuestionSpec is one question as decoded from the tool input — shared by the
// single-question fields (embedded) and each entry of the questions[] array.
type askQuestionSpec struct {
	Question  string      `json:"question"`
	Options   []AskOption `json:"options"`
	Multiple  bool        `json:"multiple"`
	AllowText bool        `json:"allow_text"`
}

// errAskUnavailable is returned when no asker is wired (headless, sub-agent, or
// test runs) so the model falls back to proceeding on its own.
var errAskUnavailable = fmt.Errorf("ask_user is unavailable in this environment; choose a sensible default and proceed, explaining your assumption")

func (t *askUserTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		askQuestionSpec
		Questions []askQuestionSpec `json:"questions"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}

	// Multi-question form: pose several questions in one call and return every
	// answer together — so the model spends one tool round-trip instead of one per
	// question. A surface that can render a grouped panel (the web tabs UI) answers
	// them all at once via the batch asker; otherwise we ask each in turn, reusing
	// the single-question prompt UI. The top-level question/options are ignored
	// when `questions` is present.
	if len(in.Questions) > 0 {
		if len(in.Questions) > maxAskQuestions {
			return "", fmt.Errorf("too many questions (%d); ask at most %d in one call", len(in.Questions), maxAskQuestions)
		}
		if batch := userBatchAskerFrom(ctx); batch != nil {
			return t.askGroup(ctx, batch, in.Questions)
		}
		asker := userAskerFrom(ctx)
		if asker == nil {
			return "", errAskUnavailable
		}
		var b strings.Builder
		for i, q := range in.Questions {
			line, err := t.askOne(ctx, asker, q.Question, q.Options, q.Multiple, q.AllowText)
			if err != nil {
				return "", fmt.Errorf("question %d: %w", i+1, err)
			}
			fmt.Fprintf(&b, "For %q: %s\n", strings.TrimSpace(q.Question), line)
		}
		return strings.TrimSpace(b.String()), nil
	}

	// Single-question form.
	asker := userAskerFrom(ctx)
	if asker == nil {
		return "", errAskUnavailable
	}
	return t.askOne(ctx, asker, in.Question, in.Options, in.Multiple, in.AllowText)
}

// askGroup validates every question, asks them all at once via the batch asker
// (one grouped panel, one round-trip), and formats each answer keyed by its
// question — using the same per-answer wording as the single-question form.
func (t *askUserTool) askGroup(ctx context.Context, batch UserBatchAsker, specs []askQuestionSpec) (string, error) {
	reqs := make([]AskRequest, 0, len(specs))
	seens := make([]map[string]bool, 0, len(specs))
	for i, q := range specs {
		opts, seen, err := validateQuestion(q.Question, q.Options, q.AllowText)
		if err != nil {
			return "", fmt.Errorf("question %d: %w", i+1, err)
		}
		reqs = append(reqs, AskRequest{Question: strings.TrimSpace(q.Question), Options: opts, Multiple: q.Multiple, AllowText: q.AllowText})
		seens = append(seens, seen)
	}
	answers, err := batch(ctx, reqs)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, q := range specs {
		var picked []string
		if i < len(answers) {
			picked = cleanPicked(answers[i])
		}
		fmt.Fprintf(&b, "For %q: %s\n", strings.TrimSpace(q.Question), formatAnswer(picked, seens[i], q.AllowText))
	}
	return strings.TrimSpace(b.String()), nil
}

// askOne validates one question's options, presents it (blocking for the user's
// answer), and formats the result — selected / answered / combined / dismissed.
// Shared by the single-question path and the sequential multi-question fallback.
func (t *askUserTool) askOne(ctx context.Context, asker UserAsker, question string, rawOptions []AskOption, multiple, allowText bool) (string, error) {
	opts, seen, err := validateQuestion(question, rawOptions, allowText)
	if err != nil {
		return "", err
	}
	chosen, err := asker(ctx, AskRequest{Question: strings.TrimSpace(question), Options: opts, Multiple: multiple, AllowText: allowText})
	if err != nil {
		return "", err
	}
	return formatAnswer(cleanPicked(chosen), seen, allowText), nil
}

// validateQuestion checks one question's options — capped, non-empty unique
// labels — and returns the cleaned options plus the set of offered labels (used
// when formatting to tell chosen options apart from typed free-text). With
// allowText a question needs no options (a pure text prompt); otherwise at least
// one is required.
func validateQuestion(question string, rawOptions []AskOption, allowText bool) ([]AskOption, map[string]bool, error) {
	if strings.TrimSpace(question) == "" {
		return nil, nil, fmt.Errorf("question is required")
	}
	opts := make([]AskOption, 0, len(rawOptions))
	seen := make(map[string]bool, len(rawOptions))
	for _, o := range rawOptions {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			continue
		}
		if seen[label] {
			return nil, nil, fmt.Errorf("duplicate option label %q", label)
		}
		seen[label] = true
		opts = append(opts, AskOption{Label: label, Description: strings.TrimSpace(o.Description)})
	}
	if len(opts) == 0 && !allowText {
		return nil, nil, fmt.Errorf("at least one option with a non-empty label is required (or set allow_text)")
	}
	if len(opts) > maxAskOptions {
		return nil, nil, fmt.Errorf("too many options (%d); provide at most %d", len(opts), maxAskOptions)
	}
	return opts, seen, nil
}

// cleanPicked drops any blank entries the host may have returned.
func cleanPicked(chosen []string) []string {
	picked := make([]string, 0, len(chosen))
	for _, c := range chosen {
		if c = strings.TrimSpace(c); c != "" {
			picked = append(picked, c)
		}
	}
	return picked
}

// formatAnswer renders one question's result — dismissed / selected / answered /
// combined. With allowText it splits the picks into chosen options (labels we
// offered, tracked in seen) and typed free-text, so a combined answer is reported
// faithfully instead of the typed part being mislabeled as a selection.
func formatAnswer(picked []string, seen map[string]bool, allowText bool) string {
	if len(picked) == 0 {
		return "The user dismissed the question without answering. Proceed with a sensible default and briefly note the assumption, or ask again only if truly necessary."
	}
	if !allowText {
		return "The user selected: " + strings.Join(picked, ", ")
	}
	var sel, typed []string
	for _, p := range picked {
		if seen[p] {
			sel = append(sel, p)
		} else {
			typed = append(typed, p)
		}
	}
	switch {
	case len(typed) == 0:
		return "The user selected: " + strings.Join(sel, ", ")
	case len(sel) == 0:
		return "The user answered: " + strings.Join(typed, ", ")
	default:
		return "The user selected: " + strings.Join(sel, ", ") + "; and answered: " + strings.Join(typed, ", ")
	}
}
