package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// checkpointAgent builds a store-backed agent wired to a scripted streamer, so
// checkpointing (which needs a store + session) can be exercised. Auto-title is
// disabled so the only post-turn model call is the checkpoint.
func checkpointAgent(t *testing.T, fake *scriptedStreamer) (*Agent, *session.Store) {
	t.Helper()
	t.Setenv("MN_AUTO_TITLE", "0")
	isolateConfig(t)
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	return New(cfg, fake, tools.NewRegistry(dir), store, nil), store
}

func TestCheckpointEveryParsing(t *testing.T) {
	cases := []struct {
		env         string
		wantN       int
		wantEnabled bool
	}{
		{"", defaultCheckpointEvery, true},
		{"0", 0, false},
		{"5", 5, true},
		{"-3", defaultCheckpointEvery, true}, // invalid → default
		{"junk", defaultCheckpointEvery, true},
	}
	for _, c := range cases {
		t.Setenv("MN_CHECKPOINT_EVERY", c.env)
		n, enabled := checkpointEvery()
		if n != c.wantN || enabled != c.wantEnabled {
			t.Errorf("checkpointEvery(env=%q) = (%d,%v), want (%d,%v)", c.env, n, enabled, c.wantN, c.wantEnabled)
		}
	}
}

// TestAutoCheckpointWritesAfterThreshold drives enough turns to cross the
// checkpoint interval and verifies a checkpoint is distilled, persisted, and
// reflected in the message-count watermark.
func TestAutoCheckpointWritesAfterThreshold(t *testing.T) {
	t.Setenv("MN_CHECKPOINT_EVERY", "2") // every 2 messages

	// First turn: user + assistant = 2 messages, crossing the interval, so a
	// checkpoint call follows. Turn 1 reply, then the checkpoint distillation.
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("did the work")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Goal: build X\nState: in progress")}},
	}}
	ag, store := checkpointAgent(t, fake)

	var infoSawCheckpoint bool
	err := ag.Run(context.Background(), "build feature X", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(strings.ToLower(e.Text), "checkpoint") {
			infoSawCheckpoint = true
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (turn + checkpoint)", fake.calls)
	}
	if !infoSawCheckpoint {
		t.Error("expected an Info event noting the checkpoint update")
	}
	if cp := ag.Checkpoint(); !strings.Contains(cp, "Goal: build X") {
		t.Errorf("checkpoint = %q, want it to contain the distilled state", cp)
	}
	// Persisted to disk with the watermark set to the message count.
	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Checkpoint, "Goal: build X") {
		t.Errorf("persisted checkpoint = %q", saved.Checkpoint)
	}
	if saved.CheckpointMsgs != len(ag.Messages()) {
		t.Errorf("CheckpointMsgs = %d, want %d", saved.CheckpointMsgs, len(ag.Messages()))
	}
}

// TestAutoCheckpointDisabled verifies MN_CHECKPOINT_EVERY=0 turns off the
// automatic checkpoint so no extra model call happens.
func TestAutoCheckpointDisabled(t *testing.T) {
	t.Setenv("MN_CHECKPOINT_EVERY", "0")
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag, _ := checkpointAgent(t, fake)

	if err := ag.Run(context.Background(), "hi", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("model calls = %d, want 1 (no checkpoint when disabled)", fake.calls)
	}
	if ag.Checkpoint() != "" {
		t.Errorf("expected no checkpoint, got %q", ag.Checkpoint())
	}
}

// TestCheckpointInjectedIntoSystem verifies the checkpoint is appended to the
// system the model receives, so it survives compaction and resume.
func TestCheckpointInjectedIntoSystem(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolateConfig(t)
	cfg := &config.Config{Model: "m", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir}
	sess := session.New("m", dir)
	sess.Checkpoint = "Goal: ship the thing\nNext: write tests"
	ag := New(cfg, &scriptedStreamer{}, tools.NewRegistry(dir), store, sess)

	// The checkpoint rides in the per-turn context preface, NOT the message history.
	preface := ag.turnContextPreface()
	if !strings.Contains(preface, "Session checkpoint") || !strings.Contains(preface, "ship the thing") {
		t.Fatalf("preface should embed the checkpoint content, got %q", preface)
	}
	// appendTurnContext carries it as a single uncached system suffix led by the
	// context marker, leaving the base system prompt as the cacheable prefix.
	sys := appendTurnContext(ag.system, "", preface)
	if !strings.HasPrefix(sys, ag.system) {
		t.Error("the base system prompt must remain the prefix (so it stays cacheable)")
	}
	if !strings.Contains(sys, llm.SystemDirectiveSeparator+llm.ContextDirectiveMarker) {
		t.Errorf("system suffix should carry the context marker %q", llm.ContextDirectiveMarker)
	}

	// With no checkpoint (and no plan), there's no preface and the system is unchanged.
	ag.ClearCheckpoint()
	if p := ag.turnContextPreface(); p != "" {
		t.Errorf("no checkpoint → empty preface, got %q", p)
	}
	if appendTurnContext(ag.system, "", "") != ag.system {
		t.Error("with nothing volatile this turn, the system prompt must be unchanged")
	}
}

// TestAppendTurnContextSingleMarker guards the one-marker invariant the cache
// split depends on: a directive leads (with the markerless preface under it), and
// the directive's own marker is the only one — so splitSystemDirective peels the
// whole suffix off (directive + checkpoint), never folding either into the cached
// base.
func TestAppendTurnContextSingleMarker(t *testing.T) {
	base := "you are sqe"
	preface := "# Session checkpoint\n\nGoal: x"
	directive := llm.UltrathinkDirectiveMarker + " (think hard)"

	got := appendTurnContext(base, directive, preface)
	want := base + llm.SystemDirectiveSeparator + directive + "\n\n" + preface
	if got != want {
		t.Fatalf("directive+preface:\n got %q\nwant %q", got, want)
	}
	// The only directive marker is the ultrathink one (the preface carries none),
	// so the appended suffix splits off whole.
	if strings.Count(got, llm.ContextDirectiveMarker) != 0 {
		t.Error("preface riding under a directive must NOT add a second marker")
	}
}

// TestCheckpointSurvivesResume verifies a checkpoint persisted on one session is
// restored (and re-injected) when that session is resumed.
func TestCheckpointSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolateConfig(t)
	cfg := &config.Config{Model: "m", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir}

	// Session saved with a checkpoint.
	saved := session.New("m", dir)
	saved.Messages = []llm.Message{llm.UserText("earlier work")}
	saved.Checkpoint = "Goal: migrate the parser\nState: half done"
	saved.CheckpointMsgs = 1
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	ag := New(cfg, &scriptedStreamer{}, tools.NewRegistry(dir), store, nil)
	reloaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	ag.Resume(reloaded)

	if cp := ag.Checkpoint(); !strings.Contains(cp, "migrate the parser") {
		t.Errorf("resumed checkpoint = %q, want the saved state", cp)
	}
	if pre := ag.turnContextPreface(); !strings.Contains(pre, "migrate the parser") {
		t.Error("resumed agent should re-inject the checkpoint into the per-turn context")
	}
}

// TestWriteCheckpointManual verifies the manual /checkpoint refresh path.
func TestWriteCheckpointManual(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Goal: do X\nNext: review")}},
	}}
	ag, _ := checkpointAgent(t, fake)
	ag.messages = []llm.Message{llm.UserText("some history")}

	out, err := ag.WriteCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if !strings.Contains(out, "Goal: do X") {
		t.Errorf("status = %q, want it to include the new checkpoint", out)
	}
	if !strings.Contains(ag.Checkpoint(), "Goal: do X") {
		t.Errorf("checkpoint not stored, got %q", ag.Checkpoint())
	}

	// With no history, it's a no-op note (no model call).
	ag.messages = nil
	ag.ClearCheckpoint()
	out, err = ag.WriteCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("WriteCheckpoint(empty): %v", err)
	}
	if !strings.Contains(out, "Nothing to checkpoint") {
		t.Errorf("empty-history status = %q", out)
	}
}

// TestCheckpointWatermarkResetAfterCompaction is the C1/stale-checkpoint
// regression test: compaction discards the history the checkpoint was distilled
// from, so onCompacted resets the watermark to 0 — both avoiding the auto-
// checkpoint stall (len(messages) could never reach a pre-compaction watermark)
// and forcing a prompt re-distill from the trimmed history.
func TestCheckpointWatermarkResetAfterCompaction(t *testing.T) {
	t.Setenv("MN_CHECKPOINT_EVERY", "2")
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Goal: keep going")}},
	}}
	ag, _ := checkpointAgent(t, fake)

	// Simulate a long session checkpointed at message 40, then compacted to 10.
	ag.sess.CheckpointMsgs = 40
	ag.messages = make([]llm.Message, 10)

	ag.onCompacted()
	if ag.sess.CheckpointMsgs != 0 {
		t.Fatalf("CheckpointMsgs = %d after compaction, want 0 (forces a fresh re-distill)", ag.sess.CheckpointMsgs)
	}

	// The post-compaction history already exceeds the interval, so a refresh
	// must fire on the next turn rather than stalling.
	ag.messages[0] = llm.UserText("post-compact work")
	var refreshed bool
	ag.maybeCheckpoint(context.Background(), func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(strings.ToLower(e.Text), "checkpoint") {
			refreshed = true
		}
	})
	if !refreshed {
		t.Error("automatic checkpoint should fire after compaction reset the watermark")
	}
	if fake.calls != 1 {
		t.Errorf("model calls = %d, want 1 (the checkpoint distillation)", fake.calls)
	}
}

// TestWriteCheckpointUsesAuxStream verifies the checkpoint distillation goes
// through the no-thinking auxiliary path (like compact/title/goal), not a raw
// thinking-enabled StreamMessages call.
func TestWriteCheckpointUsesAuxStream(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Goal: x")}},
	}}
	ag, _ := checkpointAgent(t, fake)
	ag.messages = []llm.Message{llm.UserText("history")}

	if _, err := ag.WriteCheckpoint(context.Background()); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if fake.noThinkCalls != 1 {
		t.Errorf("checkpoint distillation should use the no-thinking auxiliary stream (noThinkCalls=%d, want 1)", fake.noThinkCalls)
	}
}

// TestCheckpointReachesModelButNotHistory is the end-to-end wiring test: a
// pre-set checkpoint must reach the model in the system prompt of a live Run,
// while never being stored in a.messages or the message list sent to the model.
func TestCheckpointReachesModelButNotHistory(t *testing.T) {
	t.Setenv("MN_CHECKPOINT_EVERY", "0") // disable auto-refresh so we count one turn cleanly
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok")}},
	}}
	ag, _ := checkpointAgent(t, fake)
	ag.sess.Checkpoint = "Goal: wire the parser\nNext: add tests"

	before := ag.History()
	if err := ag.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.sent) == 0 {
		t.Fatal("model was never called")
	}
	// The checkpoint must reach the model in the SYSTEM prompt (uncached suffix)…
	if !strings.Contains(fake.sentSystem[0], "wire the parser") {
		t.Errorf("checkpoint should reach the model via the system prompt, got:\n%s", fake.sentSystem[0])
	}
	// …and NOT in the message list the model receives.
	if mt := messagesText(fake.sent[0]); strings.Contains(mt, "wire the parser") {
		t.Errorf("checkpoint must not be injected into the message list, got:\n%s", mt)
	}
	// The checkpoint must not leak into persisted history (only the real user turn
	// + assistant reply should be added).
	for _, m := range ag.Messages() {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "wire the parser") {
				t.Error("checkpoint must not be stored in a.messages (it is transient per turn)")
			}
		}
	}
	if ag.History() != before+2 {
		t.Errorf("history grew by %d, want 2 (user + assistant); checkpoint must not be persisted into messages", ag.History()-before)
	}
}

// TestClipTextTailKeepsRecent guards the transcript-cap direction: the checkpoint
// must distill from the most RECENT context, not the oldest.
func TestClipTextTailKeepsRecent(t *testing.T) {
	s := "OLDEST" + strings.Repeat("x", 50) + "NEWEST"
	got := clipTextTail(s, 10)
	if !strings.Contains(got, "NEWEST") {
		t.Errorf("clipTextTail should keep the recent tail, got %q", got)
	}
	if strings.Contains(got, "OLDEST") {
		t.Errorf("clipTextTail should drop the old head, got %q", got)
	}
	if clipTextTail("short", 100) != "short" {
		t.Error("clipTextTail should return short input unchanged")
	}
}

// TestWriteCheckpointNilClient verifies the nil-model guard (no panic).
func TestWriteCheckpointNilClient(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolateConfig(t)
	cfg := &config.Config{Model: "m", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir}
	ag := New(cfg, nil, tools.NewRegistry(dir), store, session.New("m", dir))
	ag.messages = []llm.Message{llm.UserText("history")}

	out, err := ag.WriteCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("WriteCheckpoint with nil client should not error, got %v", err)
	}
	if !strings.Contains(out, "No model") {
		t.Errorf("status = %q, want a no-model note", out)
	}
}
