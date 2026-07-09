package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestIsGenuineUser(t *testing.T) {
	genuine := llm.UserText("hello")
	if !isGenuineUser(genuine) {
		t.Error("plain user text should be a genuine user message")
	}
	toolResult := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
		llm.ToolResultBlock("id1", "output", false),
	}}
	if isGenuineUser(toolResult) {
		t.Error("tool_result carrier should not be a genuine user message")
	}
}

// TestEstTokensUncalibrated: before any calibration, estTokens is the raw bytes/4
// estimate (no overhead added).
func TestEstTokensUncalibrated(t *testing.T) {
	a := &Agent{cfg: &config.Config{}}
	msgs := []llm.Message{llm.UserText(strings.Repeat("x", 4000))} // raw = 1000
	if got := a.estTokens(msgs); got != 1000 {
		t.Errorf("uncalibrated estTokens = %d, want raw 1000", got)
	}
}

// TestCalibrateTokens covers the online linear fit (slope=chars/token,
// intercept=fixed overhead): the size floor and anomaly guards, and that two
// spread-apart observations recover slope and intercept so estTokens tracks
// real usage.
func TestCalibrateTokens(t *testing.T) {
	msgs := func(chars int) []llm.Message { return []llm.Message{llm.UserText(strings.Repeat("x", chars))} }
	a := &Agent{cfg: &config.Config{}}

	a.calibrateTokens(2000, msgs(4000))  // chars/4=1000 < 2000 floor → ignored
	a.calibrateTokens(40000, msgs(8000)) // chars/token=0.2 < 0.5 → anomalous, ignored
	a.calibrateTokens(0, msgs(8000))     // non-positive actual → ignored
	if got := a.estTokens(msgs(8000)); got != 2000 {
		t.Fatalf("after rejected reports estTokens should stay bytes/4=2000, got %d", got)
	}

	// Two valid, spread-apart observations imply slope 0.25 and intercept 1000
	// (line through (8000,3000) and (16000,5000)).
	a.calibrateTokens(3000, msgs(8000))
	if got := a.estTokens(msgs(8000)); got != 2000 {
		t.Fatalf("one observation is not enough to fit; estTokens should stay 2000, got %d", got)
	}
	a.calibrateTokens(5000, msgs(16000))
	if got := a.estTokens(msgs(8000)); got < 2998 || got > 3002 {
		t.Errorf("estTokens(8000) = %d, want ~3000 (0.25*8000+1000)", got)
	}
	if got := a.estTokens(msgs(24000)); got < 6996 || got > 7004 {
		t.Errorf("estTokens(24000) = %d, want ~7000 (0.25*24000+1000)", got)
	}
	if cpt := a.tokEst.CharsPerToken(); cpt < 3.9 || cpt > 4.1 {
		t.Errorf("CharsPerToken = %v, want ~4", cpt)
	}
}

func TestCompactBoundaryPicksGenuineUser(t *testing.T) {
	big := strings.Repeat("x", 800)
	small := "next"

	msgs := []llm.Message{
		llm.UserText(big), // 0 genuine, large
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: json.RawMessage(`"` + big + `"`)},
		}}, // 1
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("t1", big, false)}}, // 2
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(big)}},               // 3
		llm.UserText(small), // 4 genuine, small
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(small)}}, // 5
	}

	a := &Agent{
		cfg:      &config.Config{CompactKeepTokens: 100},
		messages: msgs,
	}
	cut := a.compactBoundary()
	if cut != 4 {
		t.Fatalf("boundary = %d, want 4 (the recent genuine user message)", cut)
	}
	if !isGenuineUser(a.messages[cut]) {
		t.Errorf("boundary must land on a genuine user message")
	}
}

func TestCompactBoundaryKeepsAllWhenBudgetLarge(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("a"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("b")}},
	}
	a := &Agent{
		cfg:      &config.Config{CompactKeepTokens: 1_000_000},
		messages: msgs,
	}
	if cut := a.compactBoundary(); cut != 0 {
		t.Fatalf("boundary = %d, want 0 (nothing to compact)", cut)
	}
}

// toolRound returns the assistant tool_use + user tool_result pair for one round.
func toolRound(id, name, body string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: id, Name: name},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock(id, body, false)}},
	}
}

func TestPruneStaleToolResultsElidesOld(t *testing.T) {
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", big)...)
	msgs = append(msgs, toolRound("t2", "grep", big)...)
	msgs = append(msgs, toolRound("t3", "bash", big)...)
	msgs = append(msgs, toolRound("t4", "read", big)...)
	// Tool-result carriers are at indices 2, 4, 6, 8.

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	reclaimed := a.pruneStaleToolResults(2)
	if reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0", reclaimed)
	}

	// The two oldest results (t1, t2) are elided; the two newest (t3, t4) intact.
	if got := a.messages[2].Content[0].Content; !strings.HasPrefix(got, elidedStubPrefix) || !strings.Contains(got, "read") {
		t.Errorf("oldest result not elided/named: %q", got)
	}
	if got := a.messages[4].Content[0].Content; !strings.HasPrefix(got, elidedStubPrefix) || !strings.Contains(got, "grep") {
		t.Errorf("second result not elided/named: %q", got)
	}
	if got := a.messages[6].Content[0].Content; got != big {
		t.Errorf("recent result should be intact, got len %d", len(got))
	}
	if got := a.messages[8].Content[0].Content; got != big {
		t.Errorf("newest result should be intact, got len %d", len(got))
	}

	// Idempotent: a second pass reclaims nothing and doesn't double-elide.
	before := a.messages[2].Content[0].Content
	if again := a.pruneStaleToolResults(2); again != 0 {
		t.Errorf("second prune reclaimed = %d, want 0", again)
	}
	if a.messages[2].Content[0].Content != before {
		t.Errorf("idempotent prune changed an already-elided result")
	}
}

// Relevance-based retention: an elided old result still related to the current
// goal keeps a bounded excerpt; an unrelated one gets a bare stub.
func TestPruneStaleToolResultsRelevanceRetention(t *testing.T) {
	relevant := "tokenizer " + strings.Repeat("y", 1500) + " embedding cache vectors"
	irrelevant := strings.Repeat("z", 1500)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", relevant)...)   // idx 2 (oldest)
	msgs = append(msgs, toolRound("t2", "read", irrelevant)...) // idx 4
	msgs = append(msgs, toolRound("t3", "bash", "recent")...)   // idx 6
	msgs = append(msgs, toolRound("t4", "read", "recent2")...)  // idx 8
	msgs = append(msgs, llm.UserText("fix the tokenizer and embedding cache"))

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	a.pruneStaleToolResults(2) // elide t1, t2

	got1 := a.messages[2].Content[0].Content
	if !strings.HasPrefix(got1, elidedStubPrefix) || !strings.Contains(got1, "still relevant") || !strings.Contains(got1, "tokenizer") {
		t.Errorf("relevant old result should keep an excerpt; got prefix: %.140q", got1)
	}
	got2 := a.messages[4].Content[0].Content
	if !strings.HasPrefix(got2, elidedStubPrefix) || strings.Contains(got2, "still relevant") {
		t.Errorf("irrelevant old result should be a bare stub; got: %.140q", got2)
	}
}

// Multi-byte content must not slip past the size gate and grow the message: the
// stub is always strictly shorter than the original (reclaim stays non-negative).
func TestPruneStaleToolResultsMultibyteNeverGrows(t *testing.T) {
	// ~450 CJK runes = ~1350 bytes: byte length clears a byte gate but rune
	// length (450) is under the rune threshold (1200), so the old byte-gated code
	// would have returned the whole body and grown the message.
	body := "令牌 " + strings.Repeat("字", 450) + " 令牌"
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", body)...)
	msgs = append(msgs, toolRound("t2", "bash", "recent")...)
	msgs = append(msgs, llm.UserText("令牌 relevant"))

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	a.pruneStaleToolResults(1)

	got := a.messages[2].Content[0].Content
	if len(got) >= len(body) {
		t.Errorf("stub (%d bytes) must be shorter than original (%d bytes)", len(got), len(body))
	}
	if !strings.HasPrefix(got, elidedStubPrefix) {
		t.Errorf("expected an elided stub, got %.80q", got)
	}
}

func TestPruneStaleToolResultsKeepsWhenFewCarriers(t *testing.T) {
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", big)...)
	msgs = append(msgs, toolRound("t2", "grep", big)...)

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	if reclaimed := a.pruneStaleToolResults(4); reclaimed != 0 {
		t.Fatalf("reclaimed = %d, want 0 (fewer carriers than keepRounds)", reclaimed)
	}
	if a.messages[2].Content[0].Content != big {
		t.Errorf("result should be intact when nothing is stale")
	}
}

func TestPruneStaleToolResultsSkipsSmall(t *testing.T) {
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", "tiny")...) // stale but below min size
	msgs = append(msgs, toolRound("t2", "grep", big)...)

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	if reclaimed := a.pruneStaleToolResults(1); reclaimed != 0 {
		t.Fatalf("reclaimed = %d, want 0 (stale result below min prune size)", reclaimed)
	}
	if a.messages[2].Content[0].Content != "tiny" {
		t.Errorf("small stale result should be left alone, got %q", a.messages[2].Content[0].Content)
	}
}

func TestPruneStaleToolResultsDisabled(t *testing.T) {
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("t1", "read", big)...)
	msgs = append(msgs, toolRound("t2", "grep", big)...)
	msgs = append(msgs, toolRound("t3", "bash", big)...)

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	if reclaimed := a.pruneStaleToolResults(0); reclaimed != 0 {
		t.Fatalf("reclaimed = %d, want 0 (pruning disabled)", reclaimed)
	}
	if a.messages[2].Content[0].Content != big {
		t.Errorf("nothing should be elided when keepRounds=0")
	}
}

// TestPruneInvalidatesDedupOnElide is the regression test for the prune/dedup
// re-read treadmill: when pruning elides a read's result, that read's entry must
// be dropped from turnReadCalls so a later re-read re-executes (re-serving the
// data) instead of being short-circuited to a stub the model can no longer see.
// A still-present read's entry must remain, so its re-read stays deduped.
func TestPruneInvalidatesDedupOnElide(t *testing.T) {
	big := strings.Repeat("x", 800)
	readMsg := func(id, input string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
				{Type: llm.BlockToolUse, ID: id, Name: "read_file", Input: json.RawMessage(input)},
			}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock(id, big, false)}},
		}
	}
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, readMsg("r1", `{"path":"old.go"}`)...)    // stale → elided
	msgs = append(msgs, readMsg("r2", `{"path":"recent.go"}`)...) // kept intact

	oldKey := "read_file\x00" + `{"path":"old.go"}`
	recentKey := "read_file\x00" + `{"path":"recent.go"}`
	a := &Agent{
		cfg:           &config.Config{},
		messages:      msgs,
		turnReadCalls: map[string]bool{oldKey: true, recentKey: true},
	}

	if reclaimed := a.pruneStaleToolResults(1); reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0 (the older read should be elided)", reclaimed)
	}
	if a.turnReadCalls[oldKey] {
		t.Error("elided read's dedup entry should be removed so a re-read re-executes")
	}
	if !a.turnReadCalls[recentKey] {
		t.Error("intact read's dedup entry should remain so its re-read stays deduped")
	}
}

// TestPruneDropsSearchCacheOnElide is the end-to-end guard for the interaction
// between the cross-turn search-dedup cache and pruneStaleToolResults: once a
// grep's result is elided from history, an identical repeat must RE-RUN
// (returning the body) instead of hitting the "results are already above" dedup
// stub — which would now point at the elision marker. It primes the real grep
// tool through the registry, elides its result via prune, then re-runs it.
func TestPruneDropsSearchCacheOnElide(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := 0; i < 60; i++ {
		body.WriteString("needle on this line\n") // exceed searchCacheMinBytes
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)
	ctx := context.Background()
	input := json.RawMessage(`{"pattern":"needle"}`)

	// Prime the cache: first grep returns the body and stores it; an immediate
	// repeat is then a dedup stub.
	if first, err := reg.Execute(ctx, "grep", input); err != nil || !strings.Contains(first, "big.txt") {
		t.Fatalf("prime grep = %q (err=%v)", first, err)
	}
	if out, _ := reg.Execute(ctx, "grep", input); !strings.Contains(out, "identical") {
		t.Fatalf("repeat grep should be a stub before elision, got:\n%s", out)
	}

	// History: the grep is the oldest of 4 read-style carriers, so the tighter read
	// window (readResultKeepRounds=3) elides exactly it.
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "g1", Name: "grep", Input: input}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("g1", big, false)}},
	}
	msgs = append(msgs, toolRound("r1", "read", big)...)
	msgs = append(msgs, toolRound("r2", "read", big)...)
	msgs = append(msgs, toolRound("r3", "read", big)...)

	a := &Agent{cfg: &config.Config{}, messages: msgs, tools: reg}
	if reclaimed := a.pruneStaleToolResults(4); reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0 (the grep result should be elided)", reclaimed)
	}
	if got := a.messages[2].Content[0].Content; !strings.HasPrefix(got, elidedStubPrefix) {
		t.Fatalf("grep result should have been elided, got: %q", got)
	}

	// The crux: the elided grep must now RE-RUN, not return the dedup stub.
	out, err := reg.Execute(ctx, "grep", input)
	if err != nil {
		t.Fatalf("re-grep: %v", err)
	}
	if strings.Contains(out, "identical") || !strings.Contains(out, "big.txt") {
		t.Errorf("after elision the grep should re-run with fresh results, got:\n%s", out)
	}
}

// TestPruneDropsReadCacheOnElide is the read-cache analog of
// TestPruneDropsSearchCacheOnElide: once a read_file result is elided from
// history, an identical re-read must re-send the body (DropRead invalidated the
// read-dedup cache) instead of returning the "already above" stub pointing at the
// elision marker.
func TestPruneDropsReadCacheOnElide(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("alpha beta gamma on this line\n", 40)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	reg.RegisterDefaults(1 << 20)
	ctx := context.Background()
	input := json.RawMessage(`{"path":"f.txt"}`)

	// Prime: first read returns the body and caches; an immediate repeat is the stub.
	if first, err := reg.Execute(ctx, "read_file", input); err != nil || !strings.Contains(first, "alpha") {
		t.Fatalf("prime read = %q (err=%v)", first, err)
	}
	if out, _ := reg.Execute(ctx, "read_file", input); !strings.Contains(out, "unchanged") {
		t.Fatalf("repeat read should be the dedup stub before elision, got:\n%s", out)
	}

	// History: the read is the oldest of 4 read-style carriers → elided by the
	// tighter read window (readResultKeepRounds=3).
	bigx := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "rd1", Name: "read_file", Input: input}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("rd1", bigx, false)}},
	}
	msgs = append(msgs, toolRound("r1", "grep", bigx)...)
	msgs = append(msgs, toolRound("r2", "grep", bigx)...)
	msgs = append(msgs, toolRound("r3", "grep", bigx)...)

	a := &Agent{cfg: &config.Config{}, messages: msgs, tools: reg}
	if reclaimed := a.pruneStaleToolResults(4); reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0 (the read result should be elided)", reclaimed)
	}
	if got := a.messages[2].Content[0].Content; !strings.HasPrefix(got, elidedStubPrefix) {
		t.Fatalf("read result should have been elided, got: %q", got)
	}

	// The crux: the elided read must now RE-READ (body), not return the stub.
	out, err := reg.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if strings.Contains(out, "unchanged") || !strings.Contains(out, "alpha") {
		t.Errorf("after elision the read should re-send the body, got:\n%s", out)
	}
}

// TestPruneReadResultsTighterWindow verifies read-style results are elided on a
// tighter window (readResultKeepRounds, default 3) than other tool output: with
// keepRounds=8 and 5 carriers, no general pruning happens, but a read beyond the
// 3-deep read window is elided while a bash result in the same range survives.
func TestPruneReadResultsTighterWindow(t *testing.T) {
	t.Setenv("MOONY_READ_RESULT_KEEP", "") // default 3
	big := strings.Repeat("x", 800)
	msgs := []llm.Message{llm.UserText("start")}
	msgs = append(msgs, toolRound("b1", "bash", big)...)      // idx 2 — not a read tool
	msgs = append(msgs, toolRound("r1", "read_file", big)...) // idx 4 — read, beyond the 3-window
	msgs = append(msgs, toolRound("r2", "read_file", big)...) // idx 6 — within the window
	msgs = append(msgs, toolRound("r3", "read_file", big)...) // idx 8
	msgs = append(msgs, toolRound("r4", "read_file", big)...) // idx 10

	a := &Agent{cfg: &config.Config{}, messages: msgs}
	if reclaimed := a.pruneStaleToolResults(8); reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0 (oldest read beyond the tighter window)", reclaimed)
	}
	// bash survives — the general window is 8, only reads use the tighter window.
	if got := a.messages[2].Content[0].Content; got != big {
		t.Errorf("bash result should survive the general window, got len %d", len(got))
	}
	// oldest read (beyond 3) is elided.
	if got := a.messages[4].Content[0].Content; !strings.HasPrefix(got, elidedStubPrefix) {
		t.Errorf("oldest read beyond the tighter window should be elided, got %q", got)
	}
	// the 3 most recent reads are intact.
	for _, idx := range []int{6, 8, 10} {
		if got := a.messages[idx].Content[0].Content; got != big {
			t.Errorf("recent read at %d should be intact, got len %d", idx, len(got))
		}
	}
}

// TestCompactClearsReadCache covers the read-dedup-vs-compaction fix: after
// Compact() summarizes earlier messages away, a repeat read_file must re-send
// the body — the cached "already above in this conversation" stub would otherwise
// point at content compaction just discarded, leaving the model unable to see it.
func TestCompactClearsReadCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.Default(dir, 1<<20)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("compacted summary")}},
	}}
	// Messages shaped so compactBoundary lands at index 4 (the recent small
	// genuine-user turn), mirroring TestCompactBoundaryPicksGenuineUser.
	big, small := strings.Repeat("x", 800), "next"
	a := &Agent{
		cfg:    &config.Config{CompactKeepTokens: 100},
		client: fake,
		tools:  reg,
		messages: []llm.Message{
			llm.UserText(big),
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{toolUse("t1", "bash", `"`+big+`"`)}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("t1", big, false)}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(big)}},
			llm.UserText(small),
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(small)}},
		},
	}
	ctx := context.Background()
	args := json.RawMessage(`{"path":"f.txt"}`)

	// Prime the cache: first read returns the body, the repeat returns a stub.
	if out, err := reg.Execute(ctx, "read_file", args); err != nil || !strings.Contains(out, "alpha") {
		t.Fatalf("first read = %q (err=%v); want the body", out, err)
	}
	if out, _ := reg.Execute(ctx, "read_file", args); strings.Contains(out, "alpha") || !strings.Contains(out, "unchanged") {
		t.Fatalf("repeat read should be the unchanged-stub, got %q", out)
	}

	// Compaction discards the earlier read output → must clear the cache.
	if cut, err := a.Compact(ctx); err != nil || cut == 0 {
		t.Fatalf("Compact: cut=%d err=%v; want a compaction", cut, err)
	}

	// The repeat read must now re-send the body, not the stale stub.
	out, err := reg.Execute(ctx, "read_file", args)
	if err != nil || !strings.Contains(out, "alpha") {
		t.Fatalf("post-compaction read = %q (err=%v); want the body re-sent", out, err)
	}
}

// TestMaybeCompactFiresMidTurn simulates the per-step loop in RunTurn: after
// RunTurn's start-of-turn maybeCompact call, a long single turn can keep
// growing (e.g. via appended tool results) without ever returning to the top
// of RunTurn. The fix re-checks maybeCompact once per step (step > 0) so such
// a turn still compacts instead of only checking again at the *next* turn.
func TestMaybeCompactFiresMidTurn(t *testing.T) {
	cfg := &config.Config{CompactThresholdTokens: 100, CompactKeepTokens: 700}
	fake := &scriptedStreamer{turns: []llm.Response{
		{Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "summary of earlier work"}}},
	}}
	a := &Agent{cfg: cfg, client: fake, messages: []llm.Message{llm.UserText("hi")}}

	// Below threshold: a step-0-style call (mirroring RunTurn's start-of-turn
	// check) must be a no-op.
	noop := func(Event) {}
	if err := a.maybeCompact(context.Background(), noop); err != nil {
		t.Fatalf("maybeCompact below threshold: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("expected no summarize call below threshold, got %d", fake.calls)
	}

	// Grow the history mid-"turn" (as tool results accumulate across steps)
	// past the threshold, the way a long tool-calling turn would without ever
	// returning to RunTurn's top.
	big := strings.Repeat("x", 2000)
	for i := 0; i < 5; i++ {
		a.messages = append(a.messages, llm.UserText(big))
	}
	before := len(a.messages)

	// This mirrors the step>0 mid-turn call added in RunTurn's loop.
	if err := a.maybeCompact(context.Background(), noop); err != nil {
		t.Fatalf("maybeCompact mid-turn: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected one summarize call once over threshold, got %d", fake.calls)
	}
	if len(a.messages) >= before {
		t.Fatalf("expected history to shrink after mid-turn compaction: before=%d after=%d", before, len(a.messages))
	}
}
