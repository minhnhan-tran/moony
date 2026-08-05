package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// A provider rejecting a turn for context overflow is recoverable: nothing was
// generated, so shrinking the history and re-running the step gets the user their
// answer instead of a wall of instructions. compactAndRetry decides whether that is
// worth doing, and must be honest about it — reporting true without shrinking would
// make the caller spin against the same wall.
func TestCompactAndRetryPrunesStaleToolOutputFirst(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{llm.UserText("start")}
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		msgs = append(msgs, toolRound(id, "read", big)...)
	}
	a := &Agent{
		cfg:      &config.Config{ToolResultKeepRounds: 3, CompactKeepTokens: 8000},
		messages: msgs,
	}
	before := len(a.messages)

	var events []string
	ok := a.compactAndRetry(context.Background(),
		errors.New("prompt is too long: 203297 tokens > 200000 maximum"),
		func(e Event) {
			if info, isInfo := e.(Info); isInfo {
				events = append(events, info.Text)
			}
		})
	if !ok {
		t.Fatal("expected a retry after reclaiming stale tool output")
	}
	// Pruning must NOT drop messages: it elides bodies in place, which keeps the
	// history prefix — and therefore the provider's prompt cache — intact.
	if len(a.messages) != before {
		t.Errorf("message count = %d, want it unchanged at %d (prune elides in place)", len(a.messages), before)
	}
	if len(events) == 0 {
		t.Error("the user must be told why the turn stalled and that it is retrying")
	} else if !strings.Contains(strings.ToLower(events[0]), "retry") {
		t.Errorf("notice should mention retrying: %q", events[0])
	}
}

// Unrelated failures must not trigger a compaction: it would waste a summarizer call
// and hide the real error.
func TestCompactAndRetryIgnoresUnrelatedErrors(t *testing.T) {
	a := &Agent{cfg: &config.Config{}, messages: []llm.Message{llm.UserText("hi")}}
	for _, err := range []error{
		nil,
		errors.New("connection reset by peer"),
		errors.New("rate limit reached for requests"),
		errors.New(`{"error":{"code":"insufficient_quota"}}`),
	} {
		if a.compactAndRetry(context.Background(), err, func(Event) {}) {
			t.Errorf("must not compact for %v", err)
		}
	}
}

// The provider's count is authoritative; adopting it makes the cheap threshold path
// fire BEFORE we hit the wall again, instead of overflowing every turn.
func TestCompactAndRetryCalibratesFromProviderCount(t *testing.T) {
	// The history must be big enough that the provider's reported count is PLAUSIBLE
	// for it: the estimator rejects samples implying <0.5 chars/token as garbage, so a
	// tiny history paired with a 200K count is (correctly) ignored.
	big := strings.Repeat("x", 120000)
	msgs := []llm.Message{llm.UserText("start")}
	for _, id := range []string{"t1", "t2", "t3"} {
		msgs = append(msgs, toolRound(id, "read", big)...)
	}
	a := &Agent{cfg: &config.Config{ToolResultKeepRounds: 2, CompactKeepTokens: 8000}, messages: msgs}

	// Measure against a FIXED sample: compactAndRetry elides bodies in a.messages in
	// place, so re-estimating a.messages afterwards would compare different content
	// and say nothing about the estimator.
	probe := []llm.Message{llm.UserText(strings.Repeat("y", 40000))}
	before := a.estTokens(probe)

	// The estimator needs two effective observations before it will fit (a single
	// sample can't identify a trend), and it rejects ratios outside 0.5–8 chars/token
	// as garbage. So seed one plausible observation first, then let the overflow supply
	// the second — which is exactly the real sequence: normal turns calibrate as they
	// go, and the rejection refines it.
	a.calibrateTokens(llm.MessageChars(msgs)/2, msgs)

	a.compactAndRetry(context.Background(),
		errors.New("prompt is too long: 203297 tokens > 200000 maximum"),
		func(Event) {})

	// The provider's numbers imply far more tokens per character than the chars/4
	// default, so the same sample must now be estimated higher.
	after := a.estTokens(probe)
	if after <= before {
		t.Errorf("estimate not recalibrated upward: before=%d after=%d", before, after)
	}
}

// A keep-window larger than the model's whole limit is meaningless — compactBoundary
// would retain more than can ever be sent, so nothing shrinks and the retry is futile.
func TestCompactAndRetryShrinksOversizedKeepWindow(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{llm.UserText("start")}
	for _, id := range []string{"t1", "t2"} {
		msgs = append(msgs, toolRound(id, "read", big)...)
	}
	a := &Agent{cfg: &config.Config{ToolResultKeepRounds: 1, CompactKeepTokens: 500000}, messages: msgs}

	a.compactAndRetry(context.Background(),
		errors.New("prompt is too long: 203297 tokens > 200000 maximum"),
		func(Event) {})

	if a.cfg.CompactKeepTokens > 100000 {
		t.Errorf("CompactKeepTokens = %d, want it clamped to at most half the model's limit",
			a.cfg.CompactKeepTokens)
	}
}

// Nothing left to shrink must report false rather than looping: a single turn bigger
// than the whole window can't be compacted, and failing with the provider's message is
// the honest outcome.
func TestCompactAndRetryGivesUpWhenNothingToShrink(t *testing.T) {
	a := &Agent{
		cfg:      &config.Config{ToolResultKeepRounds: 1, CompactKeepTokens: 8000},
		messages: []llm.Message{llm.UserText("one enormous message")},
	}
	if a.compactAndRetry(context.Background(),
		errors.New("prompt is too long: 999999 tokens > 200000 maximum"),
		func(Event) {}) {
		t.Error("expected false: there is nothing to prune and nothing to summarize")
	}
}

func TestCompactAndRetryNilSafe(t *testing.T) {
	var a *Agent
	if a.compactAndRetry(context.Background(), errors.New("prompt is too long"), func(Event) {}) {
		t.Error("a nil agent must report false, not panic")
	}
	b := &Agent{} // no cfg
	if b.compactAndRetry(context.Background(), errors.New("prompt is too long"), func(Event) {}) {
		t.Error("a cfg-less agent must report false, not panic")
	}
}

// The retry must be bounded, or a conversation that can't shrink far enough would
// retry forever.
func TestMaxCompactRetriesIsBounded(t *testing.T) {
	if maxCompactRetries <= 0 || maxCompactRetries > 3 {
		t.Errorf("maxCompactRetries = %d, want a small positive bound", maxCompactRetries)
	}
}

// Every compaction path must remap the persisted anchors. They are message-count
// offsets, and compaction makes the history shorter — so a path that skips the remap
// leaves footers/notes pointing at the wrong messages, or past the end, on replay.
func TestCompactPathsRemapAnchors(t *testing.T) {
	src := readAgentSource(t, "compact_overflow.go")
	i := strings.Index(src, "func (a *Agent) compactAtBoundary")
	if i < 0 {
		t.Fatal("compactAtBoundary not found")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "remapAnchorsForCompaction(") {
		t.Error("compactAtBoundary must remap anchors after shortening the history")
	}
	if !strings.Contains(body, "onCompacted()") {
		t.Error("compactAtBoundary must call onCompacted so the checkpoint is refreshed")
	}
	// Compact (the /compact command) must go through the shared path rather than
	// re-implementing it — that divergence is exactly how the missing remap arose.
	cs := readAgentSource(t, "compact.go")
	k := strings.Index(cs, "func (a *Agent) Compact(ctx context.Context)")
	if k < 0 {
		t.Fatal("Compact not found")
	}
	cbody := cs[k:]
	if j := strings.Index(cbody[1:], "\nfunc "); j >= 0 {
		cbody = cbody[:j]
	}
	if !strings.Contains(cbody, "compactAtBoundary(") {
		t.Error("Compact must delegate to compactAtBoundary so both paths behave identically")
	}
}

// readAgentSource reads a file from this package's directory, for the structural
// assertions above (which can't be observed from the built types).
func readAgentSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The recovery is only useful if the turn loop actually invokes it. Guard the wiring:
// it must retry the SAME round (step--), reset any partial UI output, and be bounded
// by compactRetries.
func TestTurnLoopWiresCompactRetry(t *testing.T) {
	src := readAgentSource(t, "agent.go")
	if !strings.Contains(src, "compactRetries := 0") {
		t.Error("the turn loop must declare a compactRetries counter")
	}
	i := strings.Index(src, "a.compactAndRetry(ctx, err, emit)")
	if i < 0 {
		t.Fatal("the turn loop never calls compactAndRetry, so overflow recovery is dead code")
	}
	// Bounded by the counter, in the same condition.
	head := src[max0(i-120):i]
	if !strings.Contains(head, "compactRetries < maxCompactRetries") {
		t.Error("the call must be gated on compactRetries < maxCompactRetries")
	}
	tail := src[i:min0(i+400, len(src))]
	if !strings.Contains(tail, "compactRetries++") {
		t.Error("the counter must be incremented, or the bound never takes effect")
	}
	if !strings.Contains(tail, "StreamReset{}") {
		t.Error("a partial stream from the failed round must be cleared before retrying")
	}
	if !strings.Contains(tail, "step--") {
		t.Error("the retry must redo the SAME round, not consume one from the budget")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func min0(a, b int) int {
	if a < b {
		return a
	}
	return b
}
