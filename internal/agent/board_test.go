package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// TestMessageBoardPostRead covers sequence numbering and the since/from/audience
// filters of the shared board store.
func TestMessageBoardPostRead(t *testing.T) {
	b := &messageBoard{}
	if got := b.post("build", "", "hello all"); got != 1 {
		t.Fatalf("first post seq = %d, want 1", got)
	}
	if got := b.post("explore", "build", "found it"); got != 2 {
		t.Fatalf("second post seq = %d, want 2", got)
	}
	b.post("reviewer", "explore", "nice")

	// since paging: only messages after #1.
	if got := b.read(1, "", ""); len(got) != 2 || got[0].Seq != 2 {
		t.Fatalf("read(since=1) = %+v, want messages #2,#3", got)
	}
	// from filter.
	if got := b.read(0, "explore", ""); len(got) != 1 || got[0].From != "explore" {
		t.Fatalf("read(from=explore) = %+v, want only explore's message", got)
	}
	// audience filter: build sees its broadcast + the message addressed to it,
	// but not the one addressed to explore.
	got := b.read(0, "", "build")
	if len(got) != 2 {
		t.Fatalf("read(audience=build) = %+v, want broadcast + msg to build", got)
	}
	for _, m := range got {
		if m.To != "" && !strings.EqualFold(m.To, "build") {
			t.Errorf("audience=build leaked a message addressed to %q", m.To)
		}
	}
}

// TestMessageBoardNilSafe confirms the store tolerates a nil receiver (no board
// threaded on ctx) without panicking.
func TestMessageBoardNilSafe(t *testing.T) {
	var b *messageBoard
	if got := b.post("x", "", "y"); got != 0 {
		t.Errorf("nil post seq = %d, want 0", got)
	}
	if got := b.read(0, "", ""); got != nil {
		t.Errorf("nil read = %+v, want nil", got)
	}
}

// TestMessageBoardConcurrentPosts proves parallel sub-agents can post safely and
// every message lands with a unique sequence number (run under -race).
func TestMessageBoardConcurrentPosts(t *testing.T) {
	b := &messageBoard{}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b.post(fmt.Sprintf("agent%d", i), "", fmt.Sprintf("msg %d", i))
		}(i)
	}
	wg.Wait()
	all := b.read(0, "", "")
	if len(all) != n {
		t.Fatalf("got %d messages, want %d", len(all), n)
	}
	seen := map[int]bool{}
	for _, m := range all {
		if seen[m.Seq] {
			t.Fatalf("duplicate seq %d", m.Seq)
		}
		seen[m.Seq] = true
	}
}

// TestPostMessageToolAttributesIdentity confirms post_message stamps the sender
// from ctx and that read_messages reads it back, formatted.
func TestPostMessageToolAttributesIdentity(t *testing.T) {
	board := &messageBoard{}
	ctx := withMessageBoard(context.Background(), board)
	ctx = withAgentIdentity(ctx, "explore")

	out, err := postMessageTool{}.Execute(ctx, json.RawMessage(`{"to":"build","text":"three callers in foo.go"}`))
	if err != nil {
		t.Fatalf("post_message error: %v", err)
	}
	if !strings.Contains(out, `from "explore"`) || !strings.Contains(out, `to "build"`) {
		t.Errorf("post_message result = %q, want it to name sender explore and addressee build", out)
	}

	// build reads it back.
	ctxBuild := withAgentIdentity(withMessageBoard(context.Background(), board), "build")
	read, err := readMessagesTool{}.Execute(ctxBuild, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("read_messages error: %v", err)
	}
	if !strings.Contains(read, "explore → build") || !strings.Contains(read, "three callers in foo.go") {
		t.Errorf("read_messages = %q, want explore's message to build", read)
	}
}

// TestReadMessagesMineOnly confirms mine_only drops broadcasts, keeping only
// messages addressed specifically to the reader.
func TestReadMessagesMineOnly(t *testing.T) {
	board := &messageBoard{}
	board.post("build", "", "everyone listen") // broadcast
	board.post("build", "explore", "just you") // addressed to explore
	board.post("build", "reviewer", "not you") // addressed to someone else

	ctx := withAgentIdentity(withMessageBoard(context.Background(), board), "explore")
	out, err := readMessagesTool{}.Execute(ctx, json.RawMessage(`{"mine_only":true}`))
	if err != nil {
		t.Fatalf("read_messages error: %v", err)
	}
	if !strings.Contains(out, "just you") {
		t.Errorf("mine_only result = %q, want the message addressed to explore", out)
	}
	if strings.Contains(out, "everyone listen") || strings.Contains(out, "not you") {
		t.Errorf("mine_only result = %q, should exclude broadcasts and messages to others", out)
	}
}

// TestBoardToolsRequireBoard confirms the tools fail clearly when no board is on
// ctx (e.g. invoked outside a delegation turn).
func TestBoardToolsRequireBoard(t *testing.T) {
	if _, err := (postMessageTool{}).Execute(context.Background(), json.RawMessage(`{"text":"hi"}`)); err == nil {
		t.Error("post_message without a board should error")
	}
	if _, err := (readMessagesTool{}).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("read_messages without a board should error")
	}
}

// TestRecordBoardMessages confirms a turn's board posts are persisted to the
// session (anchored by message count) so they can be replayed on reopen.
func TestRecordBoardMessages(t *testing.T) {
	a := &Agent{
		sess:     &session.Session{},
		messages: make([]llm.Message, 3),
	}
	board := &messageBoard{}
	board.post("build", "", "kick off")
	board.post("explore", "build", "found it")
	a.recordBoardMessages(board)

	if len(a.sess.BoardMessages) != 2 {
		t.Fatalf("got %d persisted board messages, want 2", len(a.sess.BoardMessages))
	}
	bm := a.sess.BoardMessages[1]
	if bm.From != "explore" || bm.To != "build" || bm.Text != "found it" {
		t.Errorf("persisted message = %+v, want explore→build 'found it'", bm)
	}
	if bm.MsgCount != 3 {
		t.Errorf("MsgCount = %d, want 3 (len messages at turn end)", bm.MsgCount)
	}
	// nil board and nil session are no-ops (must not panic).
	a.recordBoardMessages(nil)
	(&Agent{}).recordBoardMessages(board)
}

// TestBoardToolsParallelSafe confirms the board tools are treated as
// parallel-safe (non-mutating, mutex-guarded, append-only), so they can run
// alongside parallel task delegations.
func TestBoardToolsParallelSafe(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	registerBoardTools(reg, nil)
	a := newParallelTestAgent(reg)
	for _, name := range []string{"post_message", "read_messages"} {
		if !a.parallelSafe(name) {
			t.Errorf("%s should be parallel-safe", name)
		}
	}
}

// TestRegisterBoardToolsAllowlist confirms L1: a nil allowlist registers both
// board tools, while a restricted allowlist only registers the named ones (so a
// sandboxed persona doesn't silently gain messaging).
func TestRegisterBoardToolsAllowlist(t *testing.T) {
	// nil allow → both tools present.
	full := tools.NewRegistry(t.TempDir())
	registerBoardTools(full, nil)
	if full.Get("post_message") == nil || full.Get("read_messages") == nil {
		t.Error("nil allowlist should register both board tools")
	}

	// allowlist with only read_messages → post_message excluded.
	restricted := tools.NewRegistry(t.TempDir())
	registerBoardTools(restricted, map[string]bool{"read_messages": true})
	if restricted.Get("post_message") != nil {
		t.Error("post_message should be excluded when not in the allowlist")
	}
	if restricted.Get("read_messages") == nil {
		t.Error("read_messages should be registered when in the allowlist")
	}

	// empty (non-nil) allowlist → neither.
	none := tools.NewRegistry(t.TempDir())
	registerBoardTools(none, map[string]bool{})
	if none.Get("post_message") != nil || none.Get("read_messages") != nil {
		t.Error("an empty allowlist should register no board tools")
	}
}

// TestReadMessagesAdminSeesAll confirms L2: the orchestrator (board admin) reads
// directed messages between sub-agents, while a sub-agent does not.
func TestReadMessagesAdminSeesAll(t *testing.T) {
	board := &messageBoard{}
	board.post("explore", "reviewer", "secret handoff") // sub-agent → sub-agent

	// A non-admin agent ("build") must NOT see a message addressed to someone else.
	subCtx := withAgentIdentity(withMessageBoard(context.Background(), board), "build")
	out, err := readMessagesTool{}.Execute(subCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if strings.Contains(out, "secret handoff") {
		t.Errorf("non-admin read leaked a directed message: %q", out)
	}

	// The orchestrator (board admin) sees everything.
	adminCtx := withBoardAdmin(subCtx, true)
	out, err = readMessagesTool{}.Execute(adminCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("admin read error: %v", err)
	}
	if !strings.Contains(out, "secret handoff") {
		t.Errorf("orchestrator (admin) should see all messages, got: %q", out)
	}
}

// TestMessageBoardLimits confirms L4: an over-long message is truncated, and the
// board refuses posts beyond maxBoardMessages (post returns 0).
func TestMessageBoardLimits(t *testing.T) {
	b := &messageBoard{}
	big := strings.Repeat("x", maxBoardMessageBytes+5000)
	b.post("a", "", big)
	got := b.read(0, "", "")
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if len(got[0].Text) > maxBoardMessageBytes+len("… [truncated]")+4 {
		t.Errorf("message not truncated: len=%d", len(got[0].Text))
	}
	if !strings.HasSuffix(got[0].Text, "[truncated]") {
		t.Errorf("truncated message should be marked, got suffix %q", got[0].Text[len(got[0].Text)-20:])
	}

	// Fill to the cap, then confirm the next post is refused.
	full := &messageBoard{}
	for i := 0; i < maxBoardMessages; i++ {
		if seq := full.post("a", "", "m"); seq == 0 {
			t.Fatalf("post %d unexpectedly refused", i)
		}
	}
	if seq := full.post("a", "", "overflow"); seq != 0 {
		t.Errorf("post beyond cap should return 0, got %d", seq)
	}
	// The tool surfaces the full board as an error.
	ctx := withAgentIdentity(withMessageBoard(context.Background(), full), "a")
	if _, err := (postMessageTool{}).Execute(ctx, json.RawMessage(`{"text":"nope"}`)); err == nil {
		t.Error("post_message on a full board should error")
	}
}
