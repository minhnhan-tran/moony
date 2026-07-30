package web

import "testing"

// ev is a tiny wireEvent constructor for the pagination tests.
func ev(typ string) wireEvent { return wireEvent{Type: typ} }

// events with a leading preamble (status/commands) then three turns, each a
// "user" question followed by an assistant "text".
func sampleReplay() []wireEvent {
	return []wireEvent{
		ev("state"), ev("commands"), // preamble (no "user" yet)
		ev("user"), ev("text"), // turn 1
		ev("user"), ev("text"), ev("tool_start"), ev("tool_end"), // turn 2
		ev("user"), ev("text"), // turn 3
	}
}

func TestSplitReplay(t *testing.T) {
	pre, turns := splitReplay(sampleReplay())
	if len(pre) != 2 {
		t.Fatalf("preamble len = %d, want 2", len(pre))
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if len(turns[1]) != 4 {
		t.Errorf("turn 2 len = %d, want 4 (user+text+tool_start+tool_end)", len(turns[1]))
	}
	// No "user" event at all → everything is preamble, zero turns.
	pre, turns = splitReplay([]wireEvent{ev("state"), ev("info")})
	if len(pre) != 2 || len(turns) != 0 {
		t.Errorf("no-user case: preamble=%d turns=%d, want 2 and 0", len(pre), len(turns))
	}
	// Empty input.
	if pre, turns = splitReplay(nil); pre != nil || turns != nil {
		t.Errorf("nil input should yield nil, nil")
	}
}

func TestTailReplay(t *testing.T) {
	all := sampleReplay()

	// Keep last 2 of 3 turns: drop 1, keep preamble + turns 2 and 3.
	kept, dropped := tailReplay(all, 2)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	// preamble(2) + turn2(4) + turn3(2) = 8
	if len(kept) != 8 {
		t.Errorf("kept len = %d, want 8", len(kept))
	}
	if kept[0].Type != "state" {
		t.Errorf("kept[0] = %q, want the preamble to lead", kept[0].Type)
	}

	// n >= total turns → nothing dropped, full stream returned.
	if kept, dropped = tailReplay(all, 3); dropped != 0 || len(kept) != len(all) {
		t.Errorf("n==turns: dropped=%d len=%d, want 0 and %d", dropped, len(kept), len(all))
	}
	if kept, dropped = tailReplay(all, 99); dropped != 0 || len(kept) != len(all) {
		t.Errorf("n>turns: dropped=%d, want 0", dropped)
	}

	// n <= 0 means keep everything (the "?turns=all" / pre-pagination path).
	if kept, dropped = tailReplay(all, 0); dropped != 0 || len(kept) != len(all) {
		t.Errorf("n==0: dropped=%d, want 0 and full stream", dropped)
	}
}

func TestRangeReplay(t *testing.T) {
	all := sampleReplay() // 3 turns: t0=user+text, t1=user+text+tool_start+tool_end, t2=user+text

	// From the end (before==3), page 2 older turns → turns 1 and 2 (indices 1,2),
	// fromTurn=1, remaining=1 (turn 0 still older).
	out, fromTurn, remaining := rangeReplay(all, 3, 2)
	if fromTurn != 1 || remaining != 1 {
		t.Fatalf("before=3 n=2: fromTurn=%d remaining=%d, want 1 and 1", fromTurn, remaining)
	}
	// turn1(4) + turn2(2) = 6 events, no preamble.
	if len(out) != 6 || out[0].Type != "user" {
		t.Errorf("before=3 n=2: got %d events (first=%q), want 6 starting at a user turn", len(out), out[0].Type)
	}

	// Continue paging: before=1, n=30 → the remaining turn 0, fromTurn=0, remaining=0.
	out, fromTurn, remaining = rangeReplay(all, 1, 30)
	if fromTurn != 0 || remaining != 0 || len(out) != 2 {
		t.Errorf("before=1: fromTurn=%d remaining=%d len=%d, want 0,0,2", fromTurn, remaining, len(out))
	}

	// Boundary: before==0 (already at the start) yields nothing more.
	if out, fromTurn, remaining = rangeReplay(all, 0, 30); out != nil || fromTurn != 0 || remaining != 0 {
		t.Errorf("before=0: want no events and remaining 0, got len=%d from=%d rem=%d", len(out), fromTurn, remaining)
	}

	// n<=0 yields nothing (remaining reflects how many are older than `before`).
	if out, _, remaining = rangeReplay(all, 2, 0); out != nil || remaining != 2 {
		t.Errorf("n=0: want no events and remaining 2, got len=%d rem=%d", len(out), remaining)
	}

	// before beyond the end clamps to the turn count.
	if _, fromTurn, _ = rangeReplay(all, 99, 1); fromTurn != 2 {
		t.Errorf("before>turns: fromTurn=%d, want 2 (clamped: last turn of 3)", fromTurn)
	}
}
