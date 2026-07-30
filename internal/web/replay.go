package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
	sess "github.com/pysty1995/moony/internal/session"
)

// replayMessages converts a saved session's conversation history into the wire
// events a browser renders, so opening a persisted session shows its prior
// transcript. Tool calls are paired with their results by tool_use id (a
// tool_start with the input, then a tool_end with the output). metas are the
// session's per-turn footer records, replayed as "turn_meta" events after the
// turn they belong to (anchored by message count). infoNotes are persisted
// status lines (currently self-review's) replayed as plain "info" events,
// anchored the same way and emitted just before that position's turn_meta so
// live ordering (notice, then TurnDone) is preserved.
func replayMessages(messages []llm.Message, metas []sess.TurnMeta, taskMetas []sess.TaskMeta, boardMsgs []sess.BoardMessage, infoNotes []sess.InfoNote) []wireEvent {
	type result struct {
		out   string
		isErr bool
		imgs  []string
	}
	results := map[string]result{}
	for _, m := range messages {
		for _, c := range m.Content {
			if c.Type == llm.BlockToolResult {
				r := result{out: c.Content, isErr: c.IsError}
				// Tool results that returned images (e.g. a browser screenshot)
				// carry their text + image blocks in ContentList. Pull the text
				// for the body and each image as a data URI for inline display.
				for _, b := range c.ContentList {
					switch b.Type {
					case llm.BlockText:
						if r.out == "" {
							r.out = b.Text
						}
					case llm.BlockImage:
						if b.Source != nil {
							r.imgs = append(r.imgs, "data:"+b.Source.MediaType+";base64,"+b.Source.Data)
						}
					}
				}
				results[c.ToolUseID] = r
			}
		}
	}

	// Index per-turn footers by the message count recorded when each turn
	// finished, so we can emit a "turn_meta" event right after that message.
	metaByCount := map[int]sess.TurnMeta{}
	for _, tm := range metas {
		metaByCount[tm.MsgCount] = tm
	}
	// Index per-delegation footers by the task tool-call id, so we can replay a
	// "task_meta" event right after the task call's tool_end — restoring each
	// task card's footer (duration · cost · model · calls).
	taskMetaByID := map[string]sess.TaskMeta{}
	for _, tm := range taskMetas {
		taskMetaByID[tm.ParentID] = tm
	}
	// Index shared message-board posts by the message count recorded when their
	// turn finished, so we can replay them as "board_message" events after that
	// turn — restoring the multi-agent conversation, including sub-agent posts
	// that never entered the transcript.
	boardByCount := map[int][]sess.BoardMessage{}
	for _, bm := range boardMsgs {
		boardByCount[bm.MsgCount] = append(boardByCount[bm.MsgCount], bm)
	}
	// Index persisted status notices (self-review's) by the message count
	// recorded when they were emitted, so they replay right after the message
	// they followed live.
	infoByCount := map[int][]sess.InfoNote{}
	for _, n := range infoNotes {
		infoByCount[n.MsgCount] = append(infoByCount[n.MsgCount], n)
	}

	var out []wireEvent
	lastUserIdx := -1 // index in out of the most recent "user" event, to backfill its time
	for i, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			// One "user" event per turn, carrying the prompt text plus chips for
			// any image/document/extracted-file attachments.
			var promptText strings.Builder
			var atts []attachWire
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					if name, ok := extractedFileName(c.Text); ok {
						atts = append(atts, attachWire{Name: name, Kind: "file"})
					} else if c.Text != "" {
						if promptText.Len() > 0 {
							promptText.WriteString("\n")
						}
						promptText.WriteString(c.Text)
					}
				case llm.BlockImage:
					if c.Source != nil {
						atts = append(atts, attachWire{Name: "image", Kind: "image", DataURI: "data:" + c.Source.MediaType + ";base64," + c.Source.Data})
					}
				case llm.BlockDocument:
					name := c.Title
					if name == "" {
						name = "document"
					}
					atts = append(atts, attachWire{Name: name, Kind: "document"})
				}
			}
			if promptText.Len() > 0 || len(atts) > 0 {
				out = append(out, wireEvent{Type: "user", Text: promptText.String(), Attachments: atts})
				lastUserIdx = len(out) - 1
			}
		case llm.RoleAssistant:
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					if c.Text != "" {
						out = append(out, wireEvent{Type: "text", Text: c.Text})
					}
				case llm.BlockThinking:
					if c.Thinking != "" {
						out = append(out, wireEvent{Type: "thinking", Text: c.Thinking})
					}
				case llm.BlockRedactedThinking:
					out = append(out, wireEvent{Type: "thinking", Text: "[redacted thinking]"})
				case llm.BlockToolUse:
					out = append(out, wireEvent{Type: "tool_start", Tool: c.Name, ToolID: c.ID, Input: string(c.Input)})
					r := results[c.ID]
					out = append(out, wireEvent{Type: "tool_end", Tool: c.Name, ToolID: c.ID, Output: r.out, IsError: r.isErr, Images: r.imgs})
					// Replay the task card's per-delegation footer, anchored to this
					// task call's id, so a reopened session shows it like a live run.
					if tm, ok := taskMetaByID[c.ID]; ok {
						out = append(out, wireEvent{Type: "task_meta", ParentID: tm.ParentID, Agent: tm.Agent,
							Meta: &turnMetaWire{
								DurationMS: tm.DurationMS,
								CostUSD:    tm.CostUSD,
								Model:      tm.Model,
								Calls:      tm.Calls,
								InTokens:   tm.InTokens,
								OutTokens:  tm.OutTokens,
							}})
					}
				}
			}
		}
		// Replay any persisted status notices that followed this message live
		// (e.g. self-review's), before that position's turn footer.
		for _, n := range infoByCount[i+1] {
			out = append(out, wireEvent{Type: "info", Text: n.Text})
		}
		// After the last message of a completed turn, replay its footer.
		if tm, ok := metaByCount[i+1]; ok {
			ts := tm.Time.Format(time.RFC3339)
			// Stamp the user message that opened this turn with the turn's time, so
			// its footer shows when it was sent rather than the reopen time.
			if lastUserIdx >= 0 {
				out[lastUserIdx].Time = ts
				lastUserIdx = -1
			}
			out = append(out, wireEvent{Type: "turn_meta", Meta: &turnMetaWire{
				Time:            ts,
				DurationMS:      tm.DurationMS,
				CostUSD:         tm.CostUSD,
				Model:           tm.Model,
				Reasoning:       tm.Reasoning,
				Calls:           tm.Calls,
				InTokens:        tm.InTokens,
				OutTokens:       tm.OutTokens,
				CacheReadTokens: tm.CacheReadTokens,
			}})
		}
		// Replay this turn's shared message-board posts (anchored by message
		// count), so the Agent Messages panel is restored on reopen.
		for _, bm := range boardByCount[i+1] {
			out = append(out, wireEvent{Type: "board_message", Board: &boardMsgWire{
				Seq:  bm.Seq,
				From: bm.From,
				To:   bm.To,
				Text: bm.Text,
			}})
		}
	}

	return out
}

// extractedFileName recognizes the header that attach.Attachment.Block() puts on
// text-extracted Office files ("--- attached file: NAME ---") and returns NAME,
// so a reopened session shows a file chip instead of dumping the extracted text.
func extractedFileName(text string) (string, bool) {
	const pre = "--- attached file: "
	if !strings.HasPrefix(text, pre) {
		return "", false
	}
	rest := text[len(pre):]
	if i := strings.Index(rest, " ---"); i >= 0 {
		return rest[:i], true
	}
	return "", false
}

// defaultReplayTurns bounds how many trailing turns a freshly-opened session
// replays on connect, so a huge persisted transcript doesn't stream+render in
// full up front. Earlier turns load on demand when the user clicks "Load
// earlier history", which reconnects the event stream with "?turns=all".
const defaultReplayTurns = 30

// maxReplayTurns caps a caller-supplied ?turns= page size on /events and
// /history, so one request can't ask the server to serialize an arbitrarily large
// slice of transcript. ("turns=all" on /events is a separate, explicit opt-in to
// the full history behind the "Load full history" banner.)
const maxReplayTurns = 500

// splitReplay divides a replayed event stream into the leading preamble (any
// events before the first "user" question — e.g. the seeded status/commands
// snapshot) and the list of "real" turns, each beginning at a "user" event.
// Slices are views into events (shared backing storage), in chronological
// order. Trailing non-turn events (a plan artifact, queue updates) ride along
// in the final turn, which is always kept, so nothing is lost.
func splitReplay(events []wireEvent) (preamble []wireEvent, turns [][]wireEvent) {
	firstUser := -1
	for i, ev := range events {
		if ev.Type == "user" {
			firstUser = i
			break
		}
	}
	if firstUser == -1 {
		return events, nil // no questions yet: it's all preamble
	}
	preamble = events[:firstUser]
	start := firstUser
	for i := firstUser + 1; i < len(events); i++ {
		if events[i].Type == "user" {
			turns = append(turns, events[start:i])
			start = i
		}
	}
	turns = append(turns, events[start:])
	return preamble, turns
}

// tailReplay caps a replayed event stream to the most recent n turns: it keeps
// the preamble (always) plus the last n turns, flattened, and reports how many
// earlier turns were dropped (0 when everything fit) so the client can offer to
// load them. n <= 0 keeps everything.
func tailReplay(events []wireEvent, n int) (kept []wireEvent, dropped int) {
	if n <= 0 {
		return events, 0
	}
	preamble, turns := splitReplay(events)
	if len(turns) <= n {
		return events, 0
	}
	dropped = len(turns) - n
	kept = append(kept, preamble...)
	for _, t := range turns[dropped:] {
		kept = append(kept, t...)
	}
	return kept, dropped
}

// rangeReplay returns the wire events for the up-to-n turns immediately BEFORE
// absolute turn index `before` (0-based from the start of the session), for the
// incremental "load earlier" pager. It returns only those turns' events (no
// preamble/seeded events), the absolute index of the first returned turn
// (fromTurn), and how many turns still remain older than fromTurn (remaining),
// so the client knows whether to keep offering "load earlier".
//
// `before` is clamped to [0, len(turns)]; n<=0 yields nothing. Absolute turn
// indexes are stable cursors because replay only ever appends new turns.
func rangeReplay(events []wireEvent, before, n int) (out []wireEvent, fromTurn, remaining int) {
	_, turns := splitReplay(events)
	if before < 0 {
		before = 0
	}
	if before > len(turns) {
		before = len(turns)
	}
	if n <= 0 || before == 0 {
		return nil, before, before
	}
	start := before - n
	if start < 0 {
		start = 0
	}
	for _, t := range turns[start:before] {
		out = append(out, t...)
	}
	return out, start, start
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// atoiDefault parses a non-negative int query param, returning def when absent,
// malformed or negative.
func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return def
	}
	return n
}
