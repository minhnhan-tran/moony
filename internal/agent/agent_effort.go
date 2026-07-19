// agent_effort.go — turn-context preface, output capping, and reasoning-effort routing.

package agent

import (
	"fmt"
	"log"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// turnContextPreface bundles the volatile session context carried to the model
// each turn — the distilled checkpoint, an approved/draft plan, and plan-mode
// steering — into one block, or "" when there's nothing to carry. It is NOT a
// directive (no marker); appendTurnContext positions it within the per-turn
// system suffix. The content is not stored in a.messages, so the distilled state
// is always in context (surviving compaction and resume) without polluting the
// real history.
//
// It rides in the uncached system suffix (see appendTurnContext) rather than as a
// front-of-history message: a synthetic head message used to sit ahead of the
// cached conversation prefix, so a checkpoint refresh (every checkpointEvery
// messages) shifted that prefix and invalidated the whole message cache on the
// refresh turn. As a system suffix it's peeled off uncached and the message
// prefix is never disturbed.
func (a *Agent) turnContextPreface() string {
	var parts []string
	if cp := a.checkpointContext(); cp != "" {
		parts = append(parts, cp)
	}
	if pm := a.planModeContext(); pm != "" {
		parts = append(parts, pm)
	}
	if pc := a.planContext(); pc != "" {
		parts = append(parts, pc)
	}
	return strings.Join(parts, "\n\n")
}

// appendTurnContext appends this turn's volatile content — an optional directive
// (ultrathink/investigation) plus the optional checkpoint/plan/plan-mode preface
// — to the stable base system prompt as a single uncached suffix carrying exactly
// ONE directive marker, so applyPromptCache's splitSystemDirective peels the whole
// suffix off and the cached base/history prefix is never invalidated by it.
//
// The one-marker invariant matters because the split anchors on the marker
// nearest the end: a directive (which already starts with its marker) leads, with
// the markerless preface riding under it; when there is no directive the preface
// leads with llm.ContextDirectiveMarker. Returns base unchanged when nothing is
// volatile this turn.
func appendTurnContext(base, directive, preface string) string {
	var suffix string
	switch {
	case directive != "" && preface != "":
		suffix = directive + "\n\n" + preface
	case directive != "":
		suffix = directive
	case preface != "":
		suffix = llm.ContextDirectiveMarker + "\n\n" + preface
	default:
		return base
	}
	return base + llm.SystemDirectiveSeparator + suffix
}

// jsonShrinkInputFactor bounds when capOutput attempts JSON shrinking. A payload
// more than this many times the byte budget is clipped directly rather than fully
// parsed: it won't fit even compacted, and decoding the whole tree (pruneJSON)
// would allocate proportional to the — possibly very large — input.
const jsonShrinkInputFactor = 16

// capOutput limits tool output bytes sent back to the model. When output is over
// budget it first tries to shrink valid JSON — losslessly via whitespace removal,
// then by dropping empty/null fields — so structured payloads often fit with no
// real data lost. If it still doesn't fit, it clips on a line boundary (never
// mid-line or mid-UTF-8-rune; see clipTo) and appends a marker, so the model
// doesn't burn tokens parsing a broken tail.
func capOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	orig := len(s)
	// Skip JSON handling for payloads pathologically larger than the budget (see
	// jsonShrinkInputFactor) — they can't fit even compacted, so clip directly.
	if len(s) <= max*jsonShrinkInputFactor {
		if compact, ok := compactJSON(s); ok {
			if len(compact) <= max {
				return compact // lossless: only insignificant whitespace removed
			}
			if pruned, ok := pruneJSON(compact); ok && len(pruned) <= max {
				return pruned + "\n... [empty/null JSON fields omitted to fit the output budget]"
			}
			s = compact // still over budget — clip the smaller compact form
		}
	}
	// Over budget and not (fully) JSON-shrinkable. For a real budget, keep the
	// HEAD and TAIL and elide the middle: command/test output puts the outcome
	// (the final error, a summary line, the exit status) at the END, which a
	// head-only clip discards. Below minHeadTailBytes the elision marker's
	// overhead isn't worth the little a split would preserve, so clip the head.
	if max >= minHeadTailBytes {
		return headTailClip(s, max)
	}
	return clipTo(s, max) + fmt.Sprintf("\n... [tool output truncated to %d of %d bytes]", max, orig)
}

// minHeadTailBytes is the smallest byte budget for which capOutput keeps a
// head+tail slice rather than a plain head clip.
const minHeadTailBytes = 1024

// headTailClip fits s into max bytes by keeping its head AND tail and eliding the
// middle, with a marker naming how much was dropped. Unlike a plain head clip it
// preserves the END of the output, which for command/test output is usually
// where the result lives. Cuts snap to line boundaries (via clipTo/clipToTail) so
// lines aren't sliced mid-token, and stay UTF-8-safe. The marker fits within
// markerReserve, so the result never exceeds max. Assumes len(s) > max.
func headTailClip(s string, max int) string {
	const markerReserve = 128 // headroom the marker line is kept under
	budget := max - markerReserve
	// Favor the head slightly (setup/context) over the tail (outcome).
	head := budget * 6 / 10
	tail := budget - head
	headPart := clipTo(s, head)
	tailPart := clipToTail(s, tail)
	elided := len(s) - len(headPart) - len(tailPart)
	if elided <= 0 {
		// Snapping consumed the middle; nothing meaningful left to elide.
		return clipTo(s, max) + fmt.Sprintf("\n... [tool output truncated to %d of %d bytes]", max, len(s))
	}
	sep := ""
	if !strings.HasSuffix(headPart, "\n") {
		sep = "\n"
	}
	marker := fmt.Sprintf("... [%d of %d bytes elided from the middle — showing head + tail]\n", elided, len(s))
	return headPart + sep + marker + tailPart
}

// clipToTail is clipTo's mirror: it keeps the LAST n bytes of s, snapped forward
// to a line boundary so it doesn't begin mid-line, and trimmed of any leading
// partial UTF-8 sequence. Returns "" for n <= 0 and s unchanged when it fits.
func clipToTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	// No newline; advance to the next rune start so we don't emit a partial rune.
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// buildSystemPrompt assembles the agent's behavioral instructions.
// ultrathinkDirective is appended to the system prompt for a turn whose prompt
// contains "ultrathink". It turns the turn into a deep dive: the orchestrator
// decomposes the request and fans out parallel sub-agents (via the task tool) to
// investigate each facet independently, then synthesizes their findings — rather
// than answering from a single pass.
const ultrathinkDirective = llm.UltrathinkDirectiveMarker + ` (the user asked you to think as deeply as possible):
- Treat this as a deep investigation, not a quick answer. Be thorough and rigorous.
- First decompose the request into the distinct, independent facets worth investigating (e.g. different files/subsystems, hypotheses, approaches, or sources).
- Then DELEGATE: use the ` + "`task`" + ` tool to spawn a sub-agent per facet, running as many in PARALLEL as are independent (a single message with multiple task calls). Use the read-only ` + "`explore`" + ` agent for "how/where does X work" facets, and other personas (planner, code-reviewer, …) where they fit.
- Wait for the sub-agents, then SYNTHESIZE their findings into one comprehensive, well-supported answer (or plan). Reconcile disagreements and call out uncertainty.
- Prefer breadth via parallel sub-agents over doing everything yourself in one long pass.`

// autoDelegateDirective is injected for a NORMAL-scope read-heavy investigation
// (config.ClassifyInvestigation == InvestigationNormal) to force the file reading
// into the explore sub-agent's OWN context window instead of this turn's — where
// it would be re-sent (and re-billed) on every tool round. Unlike ultrathink it
// mandates no fan-out and no max thinking; it just says "delegate the reading,
// answer from the summary." (A narrow, single-target investigation skips delegation
// and is read inline; a broad one uses autoDelegateBroadDirective.)
const autoDelegateDirective = llm.InvestigationDirectiveMarker + ` (this request is a read-heavy investigation):
- Your FIRST action MUST be to delegate the investigation to the read-only ` + "`explore`" + ` sub-agent via the task tool — a single call, or several in PARALLEL (one message, multiple task calls) for independent facets. Give it the full question and the context it needs (it does not see this conversation).
- Base your answer on what it returns. The sub-agent reads files in its own context and returns a focused summary with path:line references, so this turn stays small and cheap.
- Do NOT read files directly first. Use read_file/grep only to confirm a specific detail the sub-agent surfaced, or right before you edit a file.`

// autoDelegateBroadDirective is injected for a BROAD investigation
// (config.ClassifyInvestigation == InvestigationBroad) — one spanning many files or
// subsystems, where a single explore pass is a bottleneck. It keeps the same
// delegate-the-reading rule but tells the model to DECOMPOSE the question and fan
// the facets out to PARALLEL explore sub-agents, then synthesize.
const autoDelegateBroadDirective = llm.InvestigationDirectiveMarker + ` (this is a BROAD, read-heavy investigation spanning multiple files/subsystems):
- First DECOMPOSE the request into its independent facets (e.g. different subsystems, files, layers, or hypotheses).
- Then DELEGATE the reading: spawn one read-only ` + "`explore`" + ` sub-agent PER facet, running them in PARALLEL (a single message with multiple task calls). Give each the full sub-question and the context it needs (it does not see this conversation).
- Base your answer on what they return and SYNTHESIZE across them — reconcile overlaps and call out gaps. Do NOT read files directly first; use read_file/grep only to confirm a specific detail, or right before you edit a file.`

// ultrathinkEffort returns the maximum reasoning effort the model supports (for
// an @ultrathink turn), or "" when the model/provider has no extended thinking.
func (a *Agent) ultrathinkEffort() string {
	eff := config.NormalizeReasoningFor(a.cfg.Model, config.ReasoningMax)
	if !config.ReasoningActive(a.cfg.LLMProvider, a.cfg.Model, eff) {
		return ""
	}
	return eff
}

// streamErrReason condenses a transient stream error into a short phrase for the
// retry notice (the raw "read tcp …" detail is noise to the user).
func streamErrReason(err error) string {
	if err == nil {
		return "network error"
	}
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "timed out") || strings.Contains(m, "timeout"):
		return "timed out"
	case strings.Contains(m, "connection reset"):
		return "connection reset"
	case strings.Contains(m, "broken pipe"):
		return "broken pipe"
	case strings.Contains(m, "eof") || strings.Contains(m, "closed"):
		return "connection closed"
	default:
		return "network error"
	}
}

// autoReasoningEffort resolves the "auto" reasoning level to a concrete effort
// for this turn — predicted from the request and session state, no extra model
// call. config.PromptReasoningHint reads the user's message (off for trivial /
// mechanical turns, medium — a generous ceiling — for other real requests, high for
// clear reasoning/debugging/design cues); plan mode or an active goal lifts it to at
// least low; and a hard previous turn (the cross-turn signal) lifts it to high.
// The result is clamped to the model and returns "off" to disable thinking this
// turn (carried as the per-call effort override), so it never falls back to the
// configured level.
func (a *Agent) autoReasoningEffort(userInput string) string {
	level := config.PromptReasoningHint(userInput)
	if a.PlanMode() || a.Goal() != "" {
		level = config.HigherReasoning(level, config.ReasoningLow)
	}
	if a.lastTurnHard {
		level = config.HigherReasoning(level, config.ReasoningHigh)
	}
	eff := config.NormalizeReasoningFor(a.cfg.Model, level)
	if !config.ReasoningActive(a.cfg.LLMProvider, a.cfg.Model, eff) {
		return config.ReasoningOff // off baseline or a non-thinking model: no thinking this turn
	}
	return eff
}

// rampedAutoEffort raises the per-turn "auto" effort as a turn proves hard —
// measured by FRICTION (tools that failed, non-zero bash exits, goal-judge
// rejections), not by how many tool rounds have elapsed. So a long but mechanical
// read/grep loop never inflates reasoning, while a turn that keeps hitting errors
// gets more thinking. The predicted base is the floor, lifted to at least medium
// at autoFrictionMedium friction events and to high at autoFrictionHigh. It never
// lowers the base (HigherReasoning), so within a turn the effort only ratchets UP
// — thinking is never toggled back off mid-turn (safe for adaptive thinking,
// where mixed thinking/no-thinking history is normal). Clamped to the model;
// "off" when the model can't think.
func (a *Agent) rampedAutoEffort(base string, friction int) string {
	floor := config.ReasoningOff
	switch {
	case friction >= autoFrictionHigh:
		floor = config.ReasoningHigh
	case friction >= autoFrictionMedium:
		floor = config.ReasoningMedium
	}
	eff := config.NormalizeReasoningFor(a.cfg.Model, config.HigherReasoning(base, floor))
	if !config.ReasoningActive(a.cfg.LLMProvider, a.cfg.Model, eff) {
		return config.ReasoningOff
	}
	return eff
}

// passiveStepTools are tools whose successful result is a routine continuation
// signal — a mutation acknowledgment, a directory listing, a task/plan update, a
// commit — rather than content the model must reason hard about. A step that
// digests ONLY these (with no errors) is "passive": dynamic effort routing lowers
// its reasoning floor. Content tools (read_file/grep/glob/find_symbol/bash/web/
// task, git_diff/…) are deliberately excluded, since the model often reasons about
// what they return — including on a turn's final answer.
var passiveStepTools = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"multi_edit":  true,
	"apply_patch": true,
	"list_dir":    true,
	"todo_write":  true,
	"plan_write":  true,
	"git_commit":  true,
}

// passiveToolBatch reports whether every call in a step is a passive-continuation
// tool (see passiveStepTools). An empty batch is not passive.
func passiveToolBatch(calls []llm.ContentBlock) bool {
	if len(calls) == 0 {
		return false
	}
	for _, c := range calls {
		if !passiveStepTools[c.Name] {
			return false
		}
	}
	return true
}

// passiveEffort caps a step's reasoning at a passive floor (low) without ever
// raising it — a routine continuation step needs little thinking. "off" is
// returned unchanged (already minimal), and the result is clamped to the model.
func (a *Agent) passiveEffort(base string) string {
	floor := config.NormalizeReasoningFor(a.cfg.Model, config.ReasoningLow)
	eff := config.LowerReasoning(base, floor)
	if !config.ReasoningActive(a.cfg.LLMProvider, a.cfg.Model, eff) {
		return config.ReasoningOff
	}
	return eff
}

// modelThinkFloor is the auto-reasoning floor implied by the model's OWN thinking
// this turn (see turnThinkHard): the more steps on which adaptive thinking engaged
// heavily, the higher the floor. Folded in via HigherReasoning, so — like the
// friction ramp — it only ratchets up and never toggles thinking back off mid-turn.
// Off until the first heavy-thinking step; medium after one, high after two.
func (a *Agent) modelThinkFloor() string {
	switch n := a.turnThinkHard.Load(); {
	case n >= 2:
		return config.ReasoningHigh
	case n >= 1:
		return config.ReasoningMedium
	default:
		return config.ReasoningOff
	}
}

// responseThought reports whether the model emitted a thinking block on this call
// — i.e. adaptive thinking chose to reason. Reliable regardless of the thinking
// display mode (the block is present even when its text is omitted).
func responseThought(resp *llm.Response) bool {
	if resp == nil {
		return false
	}
	for _, b := range resp.Blocks {
		if b.Type == llm.BlockThinking || b.Type == llm.BlockRedactedThinking {
			return true
		}
	}
	return false
}

// responseHasAnswer reports whether a model response carries something the turn
// can act on or show — a tool call, or a text block with non-whitespace content.
// A truly empty completion (or thinking only) returns false, which the turn loop
// treats as "the provider returned nothing" and surfaces to the user.
func responseHasAnswer(resp *llm.Response) bool {
	if resp == nil {
		return false
	}
	for _, b := range resp.Blocks {
		switch b.Type {
		case llm.BlockToolUse:
			return true
		case llm.BlockText:
			if strings.TrimSpace(b.Text) != "" {
				return true
			}
		}
	}
	return false
}

// debugReasoning logs the per-step auto-reasoning decision when MN_REASONING_DEBUG
// is set — the inputs (predicted base, friction, model-think tally, passive) and
// the resolved effort plus the model's observed signal (thinking present, output
// tokens) — so the thresholds (autoFriction*, autoThinkTokens, the prediction cues)
// can be tuned against real turns instead of guessed. No-op otherwise.
func (a *Agent) debugReasoning(round int, model, base, effort string, passive, thought bool, outTokens int) {
	if !reasoningDebugEnabled() {
		return
	}
	log.Printf("reasoning: round=%d model=%s base=%s effort=%s friction=%d thinkHard=%d passive=%v thought=%v out_tok=%d",
		round, model, base, effort, a.turnFriction.Load(), a.turnThinkHard.Load(), passive, thought, outTokens)
}

func reasoningDebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_REASONING_DEBUG"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// reasoningKeywordEffort detects an in-prompt thinking escalator (e.g.
// "ultrathink") in the user's message and returns the reasoning effort to use
// for this turn — clamped to what the model supports, and only when it actually
// raises the configured level and the model can think. Returns "" otherwise. It
// emits a one-line notice when it escalates, so the keyword's effect is visible.
func (a *Agent) reasoningKeywordEffort(userInput string, emit func(Event)) string {
	kw := config.ReasoningKeyword(userInput)
	if kw == "" {
		return ""
	}
	effort := config.HigherReasoning(a.cfg.ReasoningLevel, config.NormalizeReasoningFor(a.cfg.Model, kw))
	if config.ReasoningRank(effort) <= config.ReasoningRank(a.cfg.ReasoningLevel) {
		return "" // the prompt asked for no more than the configured level
	}
	if !config.ReasoningActive(a.cfg.LLMProvider, a.cfg.Model, effort) {
		return "" // this model/provider doesn't support extended thinking
	}
	emit(Info{Text: "Extended thinking raised to " + config.ReasoningDescription(effort) + " for this turn."})
	return effort
}
