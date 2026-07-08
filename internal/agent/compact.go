package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/llm"
)

// estimateTokens roughly approximates token count as bytes/4 across all message
// content. It is intentionally cheap and used for the /context breakdown display;
// the compaction decision path uses the calibrated estTokens instead.
func estimateTokens(messages []llm.Message) int {
	return llm.MessageChars(messages) / 4
}

// estTokens predicts the real input-token count of the request these messages
// would form, via the agent's calibrated TokenEstimator (see calibrateTokens).
// This reflects the provider's actual accounting — system prompt, tool schemas,
// checkpoint, and the true chars-per-token of the content — rather than a blind
// bytes/4 guess of the conversation alone. Falls back to bytes/4 until the first
// responses calibrate it.
func (a *Agent) estTokens(messages []llm.Message) int {
	return a.tokEst.Estimate(llm.MessageChars(messages))
}

// calibrateTokens folds a real provider usage report into the token estimator:
// the characters of the messages we sent vs. the input_tokens the provider
// billed. It only learns from sizeable contexts (so a near-empty history isn't
// dominated by noise); the estimator itself rejects anomalous ratios and fits
// slope (chars/token) + intercept (fixed system+tools cost) online.
func (a *Agent) calibrateTokens(actualInputTokens int, sent []llm.Message) {
	chars := llm.MessageChars(sent)
	if actualInputTokens <= 0 || chars/4 < 2000 {
		return
	}
	a.tokEst.Observe(chars, actualInputTokens)
}

// Compact unconditionally summarizes older history into a single synthetic
// exchange, preserving tool_use/tool_result pairing by only cutting at genuine
// user-text boundaries. Returns the number of messages folded into the summary.
// Unlike maybeCompact, it ignores the threshold so a user-triggered /compact
// always tries to summarize.
func (a *Agent) Compact(ctx context.Context) (int, error) {
	cut := a.compactBoundary()
	if cut <= 0 {
		return 0, nil
	}
	summary, err := a.summarize(ctx, a.messages[:cut])
	if err != nil {
		return 0, err
	}
	head := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.TextBlock("[Summary of earlier conversation, compacted to save context]\n\n" + summary),
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock("Understood. I'll continue with that context in mind."),
		}},
	}
	a.messages = append(head, a.messages[cut:]...)
	a.onCompacted() // compaction discarded history: refresh the checkpoint, avoid a watermark stall
	a.persist()
	return cut, nil
}

// maybeCompact summarizes older history into a single synthetic exchange when
// the estimated context size exceeds the configured threshold. It preserves
// tool_use/tool_result pairing by only cutting at genuine user-text boundaries.
func (a *Agent) maybeCompact(ctx context.Context, emit func(Event)) error {
	if a.cfg.CompactThresholdTokens <= 0 {
		return nil
	}
	if a.estTokens(a.messages) < a.cfg.CompactThresholdTokens {
		return nil
	}

	// Cache-preserving first resort: elide stale tool-result bodies before
	// falling back to summarization. Pruning is far cheaper — it spends no
	// summarizer model call, keeps the message count/structure intact, and
	// leaves the read/search dedup caches in place (summarize's onCompacted
	// clears them). Crucially it avoids collapsing the history prefix into a
	// synthetic summary, which would invalidate the provider prompt cache for the
	// whole conversation. If trimming alone drops us back under the threshold, we
	// skip summarization entirely.
	if reclaimed := a.pruneStaleToolResults(a.cfg.ToolResultKeepRounds); reclaimed > 0 {
		if a.estTokens(a.messages) < a.cfg.CompactThresholdTokens {
			text := fmt.Sprintf("Trimmed ~%d KB of stale tool output to stay within the context window (no summary needed).", reclaimed/1024)
			emit(Info{Text: text})
			a.noteInfo(text)
			a.persist()
			return nil
		}
	}

	cut := a.compactBoundary()
	if cut <= 0 {
		return nil
	}

	// Summarizing is a real (potentially multi-second) LLM call, unlike the cheap
	// prune above — tell the user it's happening now, not just once it's done, so
	// a quiet turn doesn't look stalled.
	startText := fmt.Sprintf("Compacting %d earlier messages into a summary to stay within the context window…", cut)
	emit(Info{Text: startText})
	a.noteInfo(startText)

	summary, err := a.summarize(ctx, a.messages[:cut])
	if err != nil {
		return err
	}

	head := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.TextBlock("[Summary of earlier conversation, compacted to save context]\n\n" + summary),
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock("Understood. I'll continue with that context in mind."),
		}},
	}
	dropped := cut
	a.messages = append(head, a.messages[cut:]...)
	a.onCompacted() // compaction discarded history: refresh the checkpoint, avoid a watermark stall
	doneText := fmt.Sprintf("Compacted %d earlier messages into a summary to stay within the context window.", dropped)
	emit(Info{Text: doneText})
	a.noteInfo(doneText)
	a.persist()
	return nil
}

// elidedStubPrefix marks a tool_result body that pruneStaleToolResults has
// already replaced, so re-pruning a later step is a no-op.
const elidedStubPrefix = "[earlier tool output elided"

// toolResultMinPruneBytes is the smallest tool_result body worth eliding; tiny
// outputs aren't worth a stub (and the stub itself has a size).
const toolResultMinPruneBytes = 600

// messageHasToolResult reports whether a message carries any tool_result block
// (i.e. it's a tool-round result carrier, not a genuine user turn).
func messageHasToolResult(m llm.Message) bool {
	for _, c := range m.Content {
		if c.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
}

// toolResultSize estimates the byte size a tool_result block contributes to the
// re-sent context (the string body plus any image/text blocks in ContentList).
func toolResultSize(c llm.ContentBlock) int {
	size := len(c.Content)
	for _, sub := range c.ContentList {
		size += len(sub.Text)
		if sub.Source != nil {
			size += len(sub.Source.Data)
		}
	}
	return size
}

// pruneStaleToolResults replaces the bodies of tool_result blocks that have
// scrolled out of the recent window with a short stub, so large Read/Grep/Bash
// outputs from earlier in the turn stop being re-sent verbatim on every
// subsequent model call — the dominant cost of long tool loops. The most recent
// keepRounds result-carrying messages keep all their results intact; older ones
// are elided in place and permanently (like compaction, but per-result and
// without a summarizer call). Read-style results (read_file/grep/glob/list_dir/
// git_*) — the biggest and most cheaply re-runnable — use a TIGHTER window
// (readResultKeepRounds, default 3) so they're elided sooner than other tool
// output. Returns the estimated bytes reclaimed.
func (a *Agent) pruneStaleToolResults(keepRounds int) int {
	if keepRounds <= 0 {
		return 0
	}
	// Indices of messages that carry tool_result blocks, oldest first.
	var carriers []int
	for i := range a.messages {
		if messageHasToolResult(a.messages[i]) {
			carriers = append(carriers, i)
		}
	}
	// General cutoff: messages before this get ALL results elided.
	cutoff := 0
	if len(carriers) > keepRounds {
		cutoff = carriers[len(carriers)-keepRounds]
	}
	// Read cutoff (tighter): messages before this also get read-style results
	// elided, even within the general window. Never earlier than cutoff.
	readCutoff := cutoff
	if rk := readResultKeepRounds(); rk > 0 && len(carriers) > rk {
		if rc := carriers[len(carriers)-rk]; rc > readCutoff {
			readCutoff = rc
		}
	}
	if readCutoff == 0 {
		return 0
	}

	// Map each tool_use ID to its tool name (to label the stub) and its read-dedup
	// key (name+input). Eliding a result must also drop the matching entry from
	// the read-dedup set, so a later re-read of an *elided* result is allowed to
	// re-execute (re-serving the data) instead of being short-circuited to a stub
	// the model can no longer see — the prune/dedup re-read treadmill.
	names := map[string]string{}
	keys := map[string]string{}
	inputs := map[string]json.RawMessage{}
	for i := range a.messages {
		if a.messages[i].Role != llm.RoleAssistant {
			continue
		}
		for _, c := range a.messages[i].Content {
			if c.Type == llm.BlockToolUse {
				names[c.ID] = c.Name
				keys[c.ID] = c.Name + "\x00" + string(c.Input)
				inputs[c.ID] = c.Input
			}
		}
	}

	reclaimed := 0
	// Current goal (latest user request) → terms for relevance-based retention.
	terms := goalTerms(lastUserText(a.messages))
	for i := 0; i < readCutoff; i++ {
		m := &a.messages[i]
		for j := range m.Content {
			c := &m.Content[j]
			if c.Type != llm.BlockToolResult {
				continue
			}
			if strings.HasPrefix(c.Content, elidedStubPrefix) {
				continue // already elided
			}
			name := names[c.ToolUseID]
			// Beyond the general window (cutoff), only read-style results are
			// elided early; other tool output survives to keepRounds.
			if i >= cutoff && !idempotentReadTools[name] {
				continue
			}
			size := toolResultSize(*c)
			if size < toolResultMinPruneBytes {
				continue
			}
			if name == "" {
				name = "tool"
			}
			// Relevance-based retention: a result still related to the current goal
			// keeps a bounded head+tail excerpt rather than a bare stub, so useful
			// context outlives pure-recency elision. Gate on RUNE count (matching
			// relevantExcerpt's own unit) so multi-byte content can't slip past a
			// byte-length gate and return the whole body unshrunk.
			orig := c.Content
			bareStub := fmt.Sprintf("%s to save context — %d bytes from %s; re-run the tool if you need it again]", elidedStubPrefix, size, name)
			stub := bareStub
			if utf8.RuneCountInString(orig) > relevantExcerptHead+relevantExcerptTail+200 &&
				relevanceScore(orig, terms) >= relevanceKeepThreshold {
				ex := relevantExcerpt(orig, relevantExcerptHead, relevantExcerptTail)
				excerptStub := fmt.Sprintf("%s to save context — kept part still relevant to your request; re-run %s for the rest]\n%s", elidedStubPrefix, name, ex)
				// Only prefer the excerpt if it actually shrinks the message; never
				// let a stub grow the original (which would make reclaim negative).
				if len(excerptStub) < len(orig) {
					stub = excerptStub
				}
			}
			c.Content = stub
			c.ContentList = nil
			c.IsError = false
			reclaimed += size - len(c.Content)
			// Now that this read's result is gone, let a re-read re-execute: drop
			// its dedup entry so dedupReadCalls won't short-circuit the recovery.
			if a.turnReadCalls != nil {
				delete(a.turnReadCalls, keys[c.ToolUseID])
			}
			// Same for the cross-turn search cache (grep/glob/list_dir): drop the
			// elided search so an identical repeat re-runs instead of hitting the
			// "results are already above" stub, which now points at this elision
			// marker. No-op for non-search tools (key absent).
			if a.tools != nil {
				a.tools.DropSearch(name, inputs[c.ToolUseID])
				// And the read-dedup cache for an elided read_file, for the same
				// reason — its "already above" stub would point at this marker.
				if name == "read_file" {
					a.tools.DropRead(inputs[c.ToolUseID])
				}
			}
		}
	}
	return reclaimed
}

// readResultKeepRounds is how many of the most recent result-carrying messages
// keep their read-style output (read_file/grep/glob/list_dir/git_*) intact
// before it's elided — tighter than the general ToolResultKeepRounds, since
// reads are the largest and most cheaply re-run output. Default 3; override with
// SQE_READ_RESULT_KEEP (0 disables the tighter window, falling back to the
// general one).
func readResultKeepRounds() int {
	if v := strings.TrimSpace(os.Getenv("SQE_READ_RESULT_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 3
}

// isGenuineUser reports whether a message is a real user turn (text content)
// rather than a tool_result carrier. These are safe compaction boundaries.
func isGenuineUser(m llm.Message) bool {
	if m.Role != llm.RoleUser {
		return false
	}
	for _, c := range m.Content {
		if c.Type == llm.BlockToolResult {
			return false
		}
	}
	return true
} // compactBoundary returns the earliest genuine-user index whose suffix fits
// within the keep budget, so everything before it can be summarized.
func (a *Agent) compactBoundary() int {
	keep := a.cfg.CompactKeepTokens
	for i := 0; i < len(a.messages); i++ {
		if !isGenuineUser(a.messages[i]) {
			continue
		}
		if a.estTokens(a.messages[i:]) <= keep {
			return i
		}
	}
	return 0
}

// summarize asks the model (no tools) to condense a slice of history into a
// compact context summary.
func (a *Agent) summarize(ctx context.Context, prefix []llm.Message) (string, error) {
	const sys = "You compress conversations between a user and a coding agent. " +
		"Produce a concise but complete summary that preserves: the user's goals, " +
		"key decisions, files created or modified (with paths), important findings, " +
		"commands run and their outcomes, and any unfinished tasks. Use short bullet points."

	transcript := serialize(prefix)
	msgs := []llm.Message{
		llm.UserText("Summarize the following conversation so it can serve as context for continuing the work:\n\n" + transcript),
	}

	resp, err := a.streamAux(ctx, sys, msgs)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, b := range resp.Blocks {
		if b.Type == llm.BlockText {
			out.WriteString(b.Text)
		}
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", fmt.Errorf("empty summary")
	}
	return summary, nil
}

// serialize renders messages as a plain-text transcript for summarization,
// capping each block so the payload stays bounded.
func serialize(messages []llm.Message) string {
	const perBlock = 2000
	var b strings.Builder
	for _, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					b.WriteString("USER: " + clip(c.Text, perBlock) + "\n")
				case llm.BlockToolResult:
					b.WriteString("TOOL_RESULT: " + clip(c.Content, perBlock) + "\n")
				}
			}
		case llm.RoleAssistant:
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					b.WriteString("ASSISTANT: " + clip(c.Text, perBlock) + "\n")
				case llm.BlockToolUse:
					b.WriteString("ASSISTANT_TOOL_CALL: " + c.Name + " " + clip(string(c.Input), 400) + "\n")
				}
			}
		}
	}
	return b.String()
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
