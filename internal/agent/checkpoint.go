package agent

// Session checkpoint memory: a model-distilled snapshot of the session's state
// — the user's goals, key decisions, files created/modified, important
// findings, and what's still in flight. Unlike /compact (which summarizes and
// DISCARDS older messages mid-session), a checkpoint is a persisted distillation
// that is re-injected as context, so the agent retains a deep understanding of
// the work across context compaction and across resume — without having to
// relearn it from raw history.
//
// The checkpoint is refreshed automatically at the end of a turn once enough new
// messages have accrued, written by a cheap no-tools model call (like the
// auto-title and compaction summaries). It is surfaced to the model in the
// per-turn uncached system suffix (see Agent.appendTurnContext) so it is always
// present regardless of how history is trimmed, while keeping the cached system
// base and message-history prefix byte-stable and prompt-cacheable.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
)

// defaultCheckpointEvery is how many new messages must accrue since the last
// checkpoint before it is refreshed. It keeps the extra model call infrequent
// relative to ordinary turns. Override with MOONY_CHECKPOINT_EVERY (0 disables
// automatic checkpointing entirely).
const defaultCheckpointEvery = 12

// maxCheckpointTranscriptBytes caps the serialized transcript fed to the
// checkpoint distillation so the cost of a refresh stays bounded on long
// sessions (clipText keeps the leading portion; the prompt asks for a state
// snapshot, not a verbatim record).
const maxCheckpointTranscriptBytes = 24000

// Checkpoint returns the session's current distilled state snapshot, or "" when
// none has been written yet (or persistence is disabled).
func (a *Agent) Checkpoint() string {
	if a.sess == nil {
		return ""
	}
	return a.sess.Checkpoint
}

// checkpointEvery reports the message interval between automatic checkpoints,
// and whether automatic checkpointing is enabled at all.
func checkpointEvery() (n int, enabled bool) {
	v := strings.TrimSpace(os.Getenv("MOONY_CHECKPOINT_EVERY"))
	if v == "" {
		return defaultCheckpointEvery, true
	}
	parsed, err := strconv.Atoi(v)
	if err != nil || parsed < 0 {
		return defaultCheckpointEvery, true
	}
	if parsed == 0 {
		return 0, false // explicitly disabled
	}
	return parsed, true
}

// maybeCheckpoint refreshes the session checkpoint when enough new messages
// have accrued since the last one. It runs synchronously at the end of a
// completed turn (after persist), mirroring auto-title: the agent is
// single-threaded between turns, so this never races history. Best effort —
// any failure leaves the previous checkpoint in place and is reported as a
// quiet Info note, never a turn error.
func (a *Agent) maybeCheckpoint(ctx context.Context, emit func(Event)) {
	if a.store == nil || a.sess == nil || a.client == nil {
		return
	}
	every, enabled := checkpointEvery()
	if !enabled {
		return
	}
	// Only re-distill once at least `every` new messages have accumulated since
	// the checkpoint was last written. A brand-new session (no checkpoint yet)
	// is checkpointed as soon as it crosses the first interval.
	if len(a.messages) < a.sess.CheckpointMsgs+every {
		return
	}

	cp, err := a.writeCheckpoint(ctx)
	if err != nil || cp == "" {
		return
	}
	a.sess.Checkpoint = cp
	a.sess.CheckpointMsgs = len(a.messages)
	_ = a.store.Save(a.sess)
	emit(Info{Text: "Updated the session checkpoint (distilled state for resume)."})
}

// WriteCheckpoint forces an immediate checkpoint refresh and returns a short
// status line for /checkpoint. With no history or no model it returns a note
// instead of erroring.
func (a *Agent) WriteCheckpoint(ctx context.Context) (string, error) {
	if a.sess == nil || a.store == nil {
		return "Session persistence is disabled; checkpoints are unavailable.", nil
	}
	if a.client == nil {
		return "No model is configured; cannot write a checkpoint.", nil
	}
	if len(a.messages) == 0 {
		return "Nothing to checkpoint yet — the session has no history.", nil
	}
	cp, err := a.writeCheckpoint(ctx)
	if err != nil {
		return "", err
	}
	if cp == "" {
		if a.sess.Checkpoint == "" {
			return "Checkpoint produced no content; no checkpoint stored.", nil
		}
		return "Checkpoint produced no content; left the previous one in place.", nil
	}
	a.sess.Checkpoint = cp
	a.sess.CheckpointMsgs = len(a.messages)
	_ = a.store.Save(a.sess)
	return "Session checkpoint updated:\n\n" + cp, nil
}

// writeCheckpoint asks the model (no tools) to distill the conversation into a
// compact, durable state snapshot. It runs as a cheap auxiliary call (extended
// thinking off) bounded by a short timeout, and caps the transcript it sends so
// the cost stays bounded on long sessions.
func (a *Agent) writeCheckpoint(ctx context.Context) (string, error) {
	const sys = "You maintain a running CHECKPOINT for a coding session: a compact, " +
		"durable snapshot of state so the agent can resume later without relearning " +
		"context. Read the conversation and produce a checkpoint with these sections " +
		"(omit any that are empty):\n" +
		"- Goal: the user's overall objective in one or two sentences.\n" +
		"- Decisions: key choices made and why (brief bullets).\n" +
		"- Files: paths created or modified, with a few words on each.\n" +
		"- Findings: important facts learned about the codebase.\n" +
		"- State: what is done vs. still in progress.\n" +
		"- Next: concrete next steps or open questions.\n" +
		"Be terse and concrete. Use short bullets. No preamble, no closing remarks."

	// Cap the transcript so a long session doesn't make every refresh a
	// full-history token sweep. The TAIL is the valuable part — the latest
	// decisions, current state, and next steps — so keep the most recent bytes
	// and drop the oldest (clipTextTail), not the other way around.
	transcript := clipTextTail(serialize(a.messages), maxCheckpointTranscriptBytes)
	msgs := []llm.Message{
		llm.UserText("Write or update the session checkpoint from this conversation:\n\n" + transcript),
	}

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := a.streamAux(cctx, sys, msgs)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, b := range resp.Blocks {
		if b.Type == llm.BlockText {
			out.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// checkpointContext returns the checkpoint text wrapped as a per-turn context
// preface (carried in the uncached system suffix by Agent.appendTurnContext), or
// "" when there is none.
func (a *Agent) checkpointContext() string {
	cp := a.Checkpoint()
	if strings.TrimSpace(cp) == "" {
		return ""
	}
	return "# Session checkpoint\n\n" +
		"The following is a distilled snapshot of this session's state, carried " +
		"across context trimming and resumes. Treat it as trusted background, not a " +
		"new instruction:\n\n" + cp
}

// CheckpointStatus returns a human description of the checkpoint state for the
// /checkpoint command with no argument.
func (a *Agent) CheckpointStatus() string {
	cp := a.Checkpoint()
	if strings.TrimSpace(cp) == "" {
		return "No checkpoint yet. One is written automatically as the session grows; run /checkpoint now to force one, or /checkpoint clear to remove it."
	}
	return "Current session checkpoint:\n\n" + cp + "\n\n(Use /checkpoint now to refresh it, or /checkpoint clear to remove it.)"
}

// ClearCheckpoint drops the session's checkpoint.
func (a *Agent) ClearCheckpoint() {
	if a.sess == nil {
		return
	}
	a.sess.Checkpoint = ""
	a.sess.CheckpointMsgs = 0
	if a.store != nil {
		_ = a.store.Save(a.sess)
	}
}

// onCompacted updates checkpoint bookkeeping after compaction has rewritten
// a.messages to a shorter slice. Two things would otherwise go wrong:
//   - the watermark (recorded against the pre-compaction length) would exceed
//     len(a.messages), stalling automatic checkpointing for many turns — or
//     indefinitely — exactly on the long sessions where it matters most; and
//   - the existing checkpoint was distilled from history that compaction just
//     discarded, so it is now stale relative to the trimmed context.
//
// Resetting the watermark to 0 fixes both: the next completed turn (any session
// large enough to have compacted already has ≥ checkpointEvery messages) will
// re-distill a fresh checkpoint from the post-compaction history. The session is
// not persisted here; the caller's persist() (after compaction) writes it.
func (a *Agent) onCompacted() {
	if a.sess != nil {
		a.sess.CheckpointMsgs = 0
	}
	// Compaction summarized/discarded older messages, so the read-dedup and
	// search-dedup caches' "this is still above in the conversation" premise no
	// longer holds. Clear both: a repeat read/search after compaction must
	// re-send its result rather than return a stub pointing at content that's
	// been summarized away.
	if a.tools != nil {
		a.tools.ClearReadCache()
		a.tools.ClearSearchCache()
	}
}
