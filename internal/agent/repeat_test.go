package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
)

func TestLooksLikeRepeatedAsk(t *testing.T) {
	msgs := func(texts ...string) []llm.Message {
		var out []llm.Message
		for _, s := range texts {
			out = append(out, llm.UserText(s))
		}
		return out
	}

	cases := []struct {
		name    string
		cur     string
		history []llm.Message
		want    bool
	}{
		{"literal repeat", "check again the issue", msgs("check again the issue"), true},
		{"literal repeat twice back", "check again the issue",
			msgs("check again the issue", "why did that happen"), true},
		{"reordered/rephrased repeat", "the issue, check it again", msgs("check again the issue"), true},
		{"unrelated first turn", "check again the issue", nil, false},
		{"unrelated prior turn", "check again the issue", msgs("please reorder the persona fields"), false},
		{"only weak/stopword overlap", "check again the issue", msgs("please add a new feature"), false},
		{"single shared term is not enough", "check the config", msgs("check the readme"), false},
		{"empty current input", "", msgs("check again the issue"), false},
		{"too far back (beyond lookback)", "check again the issue",
			append(msgs("check again the issue"),
				msgs("filler one", "filler two", "filler three", "filler four", "filler five", "filler six")...), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeRepeatedAsk(tc.cur, tc.history); got != tc.want {
				t.Errorf("looksLikeRepeatedAsk(%q) = %v, want %v", tc.cur, got, tc.want)
			}
		})
	}
}

// TestAdaptiveModelEscalatesOnRepeatedAsk is the fix for the reported gap:
// turnHard/turnFriction are both purely per-turn, so previously a user
// re-asking the same question many times (e.g. because a FastModel-routed pass
// didn't actually resolve it) never escalated model strength — every fresh
// turn started the ramp over from scratch. A closely repeated turn must now
// force the strong model even with no in-turn friction at all.
func TestAdaptiveModelEscalatesOnRepeatedAsk(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("here's what I found")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("here's what I found, take 2")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("take 3")}},
	}}
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, nil)
	// adaptiveAgent pins MN_FAST_MODEL, so the fast tier now differs from the
	// strong session model — the premise these tests rely on.
	if got := config.FastModel(adaptiveSessionModel, provider.Ollama); got != adaptiveFastModel {
		t.Fatalf("test premise broken: FastModel(%s) = %s, want %s", adaptiveSessionModel, got, adaptiveFastModel)
	}

	if err := ag.Run(context.Background(), "check the draft plan bug", func(Event) {}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if fake.sentModels[0] != adaptiveFastModel {
		t.Fatalf("turn 1 model = %q, want the fast model %q (no reason to start hard yet)", fake.sentModels[0], adaptiveFastModel)
	}

	if err := ag.Run(context.Background(), "check again the draft plan bug", func(Event) {}); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if got := fake.sentModels[len(fake.sentModels)-1]; got != adaptiveSessionModel {
		t.Errorf("turn 2 (repeat) model = %q, want the strong session model %q", got, adaptiveSessionModel)
	}

	// A third, unrelated turn must go back to starting cheap — the repeat
	// escalation is per-turn, not a permanent ratchet.
	if err := ag.Run(context.Background(), "please rename this variable", func(Event) {}); err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	if got := fake.sentModels[len(fake.sentModels)-1]; got != adaptiveFastModel {
		t.Errorf("turn 3 (unrelated) model = %q, want the fast model %q again", got, adaptiveFastModel)
	}
}

func TestLooksLikeCorrection(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Strong corrective/retry phrasing — the common pushback that shares no
		// vocabulary with the original question, so the lexical detector misses it.
		{"still broken", true},
		{"still not working", true},
		{"that didn't work", true},
		{"it doesn't work", true},
		{"not fixed", true},
		{"same issue", true},
		{"same error as before", true},
		{"try again", true},
		{"check it again", true},
		{"recheck the logic", true},
		{"re-check the routing", true},
		{"you missed something", true},
		{"that's wrong", true},
		{"it's still happening", true},
		{"the bug keeps happening", true},
		// Terse weak markers only count on a short message.
		{"nope", true},
		{"again?", true},
		{"still?", true},
		{"wrong", true},
		// Not corrections — fresh requests, so no escalation from this signal.
		{"add a login page", false},
		{"refactor the parser for clarity", false},
		{"implement the export feature", false},
		{"", false},
		// A long, specific instruction that merely contains "again" is a new task,
		// not pushback — left to normal (friction-based) escalation.
		{"run the migration again after adding the index and updating the docs", false},
	}
	for _, tc := range cases {
		if got := looksLikeCorrection(tc.in); got != tc.want {
			t.Errorf("looksLikeCorrection(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestAdaptiveModelEscalatesOnCorrection is the core fix for the reported case:
// a terse pushback ("still broken") shares NO significant vocabulary with the
// prior question, so looksLikeRepeatedAsk (lexical overlap) cannot catch it — yet
// it clearly means the previous (cheap) pass didn't resolve the issue. The
// intent-based looksLikeCorrection must escalate the turn to the strong model.
func TestAdaptiveModelEscalatesOnCorrection(t *testing.T) {
	// Guard the premise: the lexical detector genuinely does NOT fire here, so the
	// escalation below can only come from the correction detector.
	if looksLikeRepeatedAsk("still broken", []llm.Message{llm.UserText("check the draft plan bug")}) {
		t.Fatal("premise broken: lexical repeat should NOT match a no-overlap pushback")
	}

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("here's what I found")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("looking again")}},
	}}
	ag := adaptiveAgent(t, fake, true, adaptiveSessionModel, nil)
	// adaptiveAgent pins MN_FAST_MODEL, so the fast tier now differs from the
	// strong session model — the premise these tests rely on.
	if got := config.FastModel(adaptiveSessionModel, provider.Ollama); got != adaptiveFastModel {
		t.Fatalf("test premise broken: FastModel(%s) = %s, want %s", adaptiveSessionModel, got, adaptiveFastModel)
	}

	if err := ag.Run(context.Background(), "check the draft plan bug", func(Event) {}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if fake.sentModels[0] != adaptiveFastModel {
		t.Fatalf("turn 1 model = %q, want the fast model %q", fake.sentModels[0], adaptiveFastModel)
	}

	// Terse correction with zero lexical overlap → must still escalate to strong.
	if err := ag.Run(context.Background(), "still broken", func(Event) {}); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if got := fake.sentModels[len(fake.sentModels)-1]; got != adaptiveSessionModel {
		t.Errorf("turn 2 (correction) model = %q, want the strong session model %q", got, adaptiveSessionModel)
	}
}
