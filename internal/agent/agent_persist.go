// agent_persist.go — session persistence and per-turn metadata recording.

package agent

import (
	"time"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
)

// persist saves the current history to the session store (best effort).
func (a *Agent) persist() {
	if a.store == nil || a.sess == nil {
		return
	}
	a.sess.Messages = a.messages
	a.saveGoalToSession()
	_ = a.store.Save(a.sess)
}

// recordTaskMetas persists this turn's `task` sub-agent delegation footers
// (keyed by the task tool-call id) so the web UI can replay each task card's
// per-delegation footer (duration · cost · model · calls) when the session is
// reopened. A no-op when persistence is disabled or nothing was delegated.
// Called once per completed turn, alongside recordTurnMeta.
func (a *Agent) recordTaskMetas(acct *taskMetaAcct) {
	if a.sess == nil || acct == nil {
		return
	}
	for _, m := range acct.drain() {
		a.sess.TaskMetas = append(a.sess.TaskMetas, session.TaskMeta{
			ParentID:   m.ParentID,
			Agent:      m.Agent,
			Model:      m.Model,
			CostUSD:    m.CostUSD,
			DurationMS: m.DurationMS,
			Calls:      m.Calls,
			InTokens:   m.InTokens,
			OutTokens:  m.OutTokens,
		})
	}
}

// recordBoardMessages persists this turn's shared message-board posts to the
// session (anchored by the current message count) so the web UI can replay the
// multi-agent conversation when the session is reopened — including sub-agent
// posts, which never enter the transcript and would otherwise be lost. The
// board is per-turn, so it holds only this turn's messages; a no-op when
// persistence is disabled or nothing was posted. Called once per completed
// turn, alongside recordTurnMeta. Only the top-level orchestrator runs this
// (sub-agents have no session), so the whole tree's posts are captured once.
func (a *Agent) recordBoardMessages(board *messageBoard) {
	if a.sess == nil || board == nil {
		return
	}
	msgs := board.read(0, "", "") // snapshot all of this turn's posts (a copy)
	if len(msgs) == 0 {
		return
	}
	count := len(a.messages)
	for _, m := range msgs {
		a.sess.BoardMessages = append(a.sess.BoardMessages, session.BoardMessage{
			Seq:      m.Seq,
			From:     m.From,
			To:       m.To,
			Text:     m.Text,
			Time:     m.Time,
			MsgCount: count,
		})
	}
}

// noteInfo persists a single status/notice line into session history,
// anchored to the current message count, so it survives a reload and replays
// in place (see session.InfoNote). Called immediately at emit time (unlike
// recordBoardMessages/recordTaskMetas, which batch at turn end) so a two-pass
// self-review cycle's notices anchor to their own correct position in the
// transcript rather than all clustering at the turn's final count. A no-op
// when persistence is disabled.
func (a *Agent) noteInfo(text string) {
	if a.sess == nil {
		return
	}
	a.sess.InfoNotes = append(a.sess.InfoNotes, session.InfoNote{
		Text:     text,
		Time:     time.Now(),
		MsgCount: len(a.messages),
	})
}

// recordTurnMeta appends a per-turn footer record (timestamp, elapsed time,
// estimated cost, model, reasoning level, model-call count) to the session,
// anchored to the current message count so the web UI can replay the turn's
// footer when the session is reopened. A no-op when persistence is disabled.
// Called once per completed turn, just before the TurnDone event is emitted.
// usage is the turn's total token usage (all model calls, for the token-count
// fields); cost is the REAL accumulated turn cost (see turnCostUSD in
// RunWith — priced per call at whichever model actually served it, including
// any AdaptiveModel/CheapReads downshifts, folded sub-agent delegations, and
// aux calls, rather than usage.CostUSD(a.cfg.Model) pricing every token at the
// session model's rate); calls is the number of model calls (loop steps) made;
// model is the LAST round's actual serving model (turnModel in RunWith) — NOT
// always a.cfg.Model, since an AdaptiveModel/CheapReads downshift can mean the
// configured main model never ran a single round this turn. Recording the
// session model here would mislabel the cost (and the round(s) that earned it)
// against a model that was never called.
func (a *Agent) recordTurnMeta(start time.Time, usage llm.Usage, cost float64, calls int, model, reasoning string) {
	if a.sess == nil {
		return
	}
	// Capture the current file-undo snapshot turn id so /rewind can map a kept
	// turn back to the right snapshot boundary even if the counter drifted (an
	// errored/cancelled turn bumps it without recording a meta).
	snapTurn := 0
	if a.snapshots != nil {
		snapTurn = a.snapshots.turnCount()
	}
	a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{
		Time:            time.Now(),
		DurationMS:      time.Since(start).Milliseconds(),
		CostUSD:         cost,
		Model:           model,
		Reasoning:       reasoning,
		MsgCount:        len(a.messages),
		Calls:           calls,
		InTokens:        usage.InputTokens,
		OutTokens:       usage.OutputTokens,
		CacheReadTokens: usage.CacheReadInputTokens,
		SnapshotTurn:    snapTurn,
	})
}
