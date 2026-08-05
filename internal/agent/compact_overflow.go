package agent

import (
	"context"
	"fmt"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// maxCompactRetries caps how many times one turn may shrink-and-retry after the
// provider rejects it for context overflow. Two is enough for the realistic cases
// (prune stale tool output, then summarize); beyond that the conversation genuinely
// doesn't fit and the user needs a bigger model.
const maxCompactRetries = 2

// compactAndRetry recovers a turn the provider REFUSED for exceeding the context
// window, by shrinking the history and reporting that a retry is worthwhile.
//
// The threshold-based path (maybeCompact) is an estimate, and estimates lose: with an
// unusually dense payload — a big pasted blob, images, or a provider that simply
// counts differently — we sail past the real limit without ever tripping the
// threshold, so nothing compacts and the turn just fails. Here the provider has told
// us the authoritative number, so we compact regardless of the estimate and
// recalibrate the estimator from it.
//
// Safe to retry because a rejected request never reached the model: nothing was
// generated, nothing was billed, and the conversation is intact.
//
// Returns true only when the history actually got smaller — retrying without progress
// would spin against the same wall.
func (a *Agent) compactAndRetry(ctx context.Context, err error, emit func(Event)) bool {
	if a == nil || a.cfg == nil {
		return false
	}
	used, max, ok := config.PromptTooLong(err)
	if !ok {
		return false
	}
	// Trust the provider's count over our estimate for the rest of the session, so
	// the cheap threshold path starts firing BEFORE we hit the wall again.
	if used > 0 {
		a.calibrateTokens(used, a.messages)
	}
	// Shrink the keep-window when the provider's own numbers say the retained tail
	// alone wouldn't fit: compactBoundary keeps CompactKeepTokens of recent turns,
	// which is meaningless if that budget is itself over the limit.
	if max > 0 && a.cfg.CompactKeepTokens > max/2 {
		a.cfg.CompactKeepTokens = max / 2
	}

	before := len(a.messages)

	// Cheapest first: eliding stale tool-result bodies often frees enough on its own,
	// costs no summarizer call, and (unlike summarizing) keeps the history prefix —
	// and therefore the provider's prompt cache — intact.
	keepRounds := a.cfg.ToolResultKeepRounds
	if keepRounds > 1 {
		keepRounds = 1 // the usual budget is what overflowed; keep only the latest round
	}
	if reclaimed := a.pruneStaleToolResults(keepRounds); reclaimed > 0 {
		note := fmt.Sprintf("Context overflowed the model's limit — trimmed ~%d KB of stale tool output and retrying.", reclaimed/1024)
		emit(Info{Text: note})
		a.noteInfo(note)
		a.persist()
		return true
	}

	cut := a.compactBoundary()
	if cut <= 0 {
		// Nothing summarizable: a single turn is larger than the whole window, so no
		// amount of compaction helps and the user needs a bigger model. Falling
		// through to the error is the honest outcome.
		return false
	}
	startText := fmt.Sprintf("Context overflowed the model's limit — compacting %d earlier messages and retrying…", cut)
	emit(Info{Text: startText})
	a.noteInfo(startText)

	if _, cerr := a.compactAtBoundary(ctx, cut); cerr != nil {
		return false
	}
	if len(a.messages) >= before {
		return false // no progress; don't spin
	}
	return true
}

// compactAtBoundary summarizes a.messages[:cut] into a synthetic exchange, the shared
// core of /compact and the overflow recovery above.
func (a *Agent) compactAtBoundary(ctx context.Context, cut int) (int, error) {
	if cut <= 0 {
		return 0, nil
	}
	summary, err := a.summarize(ctx, a.messages[:cut])
	if err != nil {
		return 0, err
	}
	head := compactedHead(summary)
	a.messages = append(head, a.messages[cut:]...)
	// Anchors are message-count offsets into the history, and compaction just made it
	// shorter: without this, persisted footers/notes point at the wrong messages (or
	// past the end) when the transcript is replayed.
	a.remapAnchorsForCompaction(cut, len(head))
	a.onCompacted() // compaction discarded history: refresh the checkpoint, avoid a watermark stall
	a.persist()
	return cut, nil
}

// compactedHead builds the synthetic user/assistant exchange that stands in for the
// summarized history. Shared so every compaction path produces an identical marker —
// the transcript renderer and the user-turn counter both key off this text.
func compactedHead(summary string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.TextBlock("[Summary of earlier conversation, compacted to save context]\n\n" + summary),
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock("Understood. I'll continue with that context in mind."),
		}},
	}
}
