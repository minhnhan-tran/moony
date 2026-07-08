package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// selfReviewAgent builds a minimal agent for the self-review loop: a working dir,
// SelfReview enabled, a scripted reviewer reply, and (when edited) a snapshot of a
// file the current turn changed so reviewableDiff has something to show.
func selfReviewAgent(t *testing.T, verdict string, edited bool) *Agent {
	t.Helper()
	dir := t.TempDir()
	snaps := &snapshotStore{turn: 1}
	if edited {
		p := filepath.Join(dir, "x.go")
		if err := os.WriteFile(p, []byte("package x\nfunc Foo() int { return 2 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		snaps.entries = []fileSnapshot{{turn: 1, path: p, prior: []byte("package x\nfunc Foo() int { return 1 }\n")}}
	}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock(verdict)}},
	}}
	return &Agent{
		cfg:       &config.Config{WorkingDir: dir, SelfReview: true, Model: "test-model", MaxToolOutputBytes: 16000},
		client:    fake,
		snapshots: snaps,
	}
}

func TestParseReview(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"LGTM", true},
		{"LGTM.", true},
		{"lgtm\nlooks fine", true},
		{"", true},                     // empty/garbled → don't block
		{"I think this is fine", true}, // no ISSUES, no bullets → ok
		{"ISSUES\n- x.go:3 — off-by-one", false},
		{"issues:\n- a.go:1 — nil deref", false},
		{"- x.go:3 — wrong variable used", false}, // bullets without a header still count
	}
	for _, c := range cases {
		if ok, _ := parseReview(c.in); ok != c.wantOK {
			t.Errorf("parseReview(%q) ok=%v, want %v", c.in, ok, c.wantOK)
		}
	}
}

// TestReviewableDiff confirms the per-turn diff shows the changed file's before
// and after, and is empty when the turn edited nothing.
func TestReviewableDiff(t *testing.T) {
	a := selfReviewAgent(t, "LGTM", true)
	d := a.reviewableDiff()
	if !strings.Contains(d, "x.go") || !strings.Contains(d, "return 1") || !strings.Contains(d, "return 2") {
		t.Errorf("diff should show x.go's old and new lines, got:\n%s", d)
	}
	if d := selfReviewAgent(t, "LGTM", false).reviewableDiff(); d != "" {
		t.Errorf("no edits should yield an empty diff, got %q", d)
	}
}

// TestReviewableDiffCapsLargeFile: a single very large changed file is clipped so
// the review payload stays bounded near maxReviewDiffBytes (no overshoot by a full
// file diff) and is marked truncated.
func TestReviewableDiffCapsLargeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.go")
	big := strings.Repeat("a fairly representative line of source code here\n", 1000) // ~49KB
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	snaps := &snapshotStore{turn: 1}
	snaps.entries = []fileSnapshot{{turn: 1, path: p, missing: true}} // created this turn
	a := &Agent{cfg: &config.Config{WorkingDir: dir, MaxToolOutputBytes: 16000}, snapshots: snaps}

	d := a.reviewableDiff()
	if len(d) > maxReviewDiffBytes+128 {
		t.Errorf("reviewableDiff = %d bytes, want bounded near %d (one huge file must be clipped)", len(d), maxReviewDiffBytes)
	}
	if !strings.HasSuffix(d, "[diff truncated for review]") {
		t.Errorf("expected a truncation marker at the end of the %d-byte review diff", len(d))
	}
}

// TestMaybeSelfReviewGating: skipped when disabled or when nothing was edited.
func TestMaybeSelfReviewGating(t *testing.T) {
	off := selfReviewAgent(t, "ISSUES\n- x.go:2 — bug", true)
	off.cfg.SelfReview = false
	var n int
	if off.maybeSelfReview(context.Background(), func(Event) {}, &n) {
		t.Error("disabled self-review should not continue the loop")
	}

	noEdit := selfReviewAgent(t, "ISSUES\n- x.go:2 — bug", false)
	n = 0
	if noEdit.maybeSelfReview(context.Background(), func(Event) {}, &n) {
		t.Error("self-review should be skipped when nothing was edited this turn")
	}
}

// TestMaybeSelfReviewClean: a LGTM finishes the turn (no continue, no message).
func TestMaybeSelfReviewClean(t *testing.T) {
	a := selfReviewAgent(t, "LGTM", true)
	var n int
	var evs []Event
	if a.maybeSelfReview(context.Background(), func(e Event) { evs = append(evs, e) }, &n) {
		t.Error("a clean review should not continue the loop")
	}
	if n != 0 || len(a.messages) != 0 {
		t.Errorf("clean review: attempts=%d messages=%d, want 0/0", n, len(a.messages))
	}
	if !strings.Contains(strings.Join(collectInfo(evs), " "), "no issues") {
		t.Errorf("expected a 'no issues' notice, got %v", collectInfo(evs))
	}
}

// TestMaybeSelfReviewFlagsThenCaps: a flagged review feeds the findings back once
// (continue + synthetic message + friction), then the one-cycle cap stops it.
func TestMaybeSelfReviewFlagsThenCaps(t *testing.T) {
	a := selfReviewAgent(t, "ISSUES\n- x.go:2 — off-by-one in the bound", true)
	var n int

	if !a.maybeSelfReview(context.Background(), func(Event) {}, &n) {
		t.Fatal("a flagged review should continue the loop")
	}
	if n != 1 || len(a.messages) != 1 {
		t.Fatalf("after review: attempts=%d messages=%d, want 1/1", n, len(a.messages))
	}
	if !strings.Contains(a.messages[0].Content[0].Text, "off-by-one") {
		t.Errorf("feedback should carry the reviewer's findings, got %q", a.messages[0].Content[0].Text)
	}
	if a.turnFriction.Load() != 1 {
		t.Errorf("a flagged review should count as friction, got %d", a.turnFriction.Load())
	}

	before := len(a.messages)
	if a.maybeSelfReview(context.Background(), func(Event) {}, &n) {
		t.Fatal("once the one-cycle cap is reached, self-review must stop the loop")
	}
	if len(a.messages) != before {
		t.Errorf("no feedback should be appended after the cap, messages went %d→%d", before, len(a.messages))
	}
}
