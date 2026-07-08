package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
)

func rwMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(text)}}
}

func TestRewindRevertsFilesAndTrimsHistory(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
		sess:      &session.Session{},
	}

	// Turn 1: edit f -> v1, add 2 messages.
	a.snapshots.startTurn()
	a.SnapshotPath(f)
	os.WriteFile(f, []byte("v1"), 0o644)
	a.messages = append(a.messages, rwMsg("u1"), rwMsg("a1"))
	a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{MsgCount: len(a.messages), SnapshotTurn: a.snapshots.turnCount()})

	// Turn 2: edit f -> v2, add 2 more messages.
	a.snapshots.startTurn()
	a.SnapshotPath(f)
	os.WriteFile(f, []byte("v2"), 0o644)
	a.messages = append(a.messages, rwMsg("u2"), rwMsg("a2"))
	a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{MsgCount: len(a.messages), SnapshotTurn: a.snapshots.turnCount()})

	// Rewind 1 turn: file back to v1, history back to 2 messages / 1 turn.
	out, err := a.Rewind(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); string(got) != "v1" {
		t.Errorf("after rewind 1: file = %q, want v1 (out=%q)", got, out)
	}
	if len(a.messages) != 2 {
		t.Errorf("after rewind 1: %d messages, want 2", len(a.messages))
	}
	if len(a.sess.TurnMetas) != 1 {
		t.Errorf("after rewind 1: %d turn metas, want 1", len(a.sess.TurnMetas))
	}

	// Rewind again: back to the original state (v0, no history).
	if _, err := a.Rewind(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); string(got) != "v0" {
		t.Errorf("after rewind 2: file = %q, want v0", got)
	}
	if len(a.messages) != 0 {
		t.Errorf("after rewind 2: %d messages, want 0", len(a.messages))
	}
	if len(a.sess.TurnMetas) != 0 {
		t.Errorf("after rewind 2: %d turn metas, want 0", len(a.sess.TurnMetas))
	}
}

func TestRewindMultipleTurnsAtOnce(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "g.txt")
	os.WriteFile(f, []byte("orig"), 0o644)
	created := filepath.Join(dir, "new.txt") // created in turn 2

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
		sess:      &session.Session{},
	}
	// Turn 1: edit g.
	a.snapshots.startTurn()
	a.SnapshotPath(f)
	os.WriteFile(f, []byte("t1"), 0o644)
	a.messages = append(a.messages, rwMsg("u1"))
	a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{MsgCount: len(a.messages), SnapshotTurn: a.snapshots.turnCount()})
	// Turn 2: create new.txt, edit g again.
	a.snapshots.startTurn()
	a.SnapshotPath(created)
	os.WriteFile(created, []byte("created"), 0o644)
	a.SnapshotPath(f)
	os.WriteFile(f, []byte("t2"), 0o644)
	a.messages = append(a.messages, rwMsg("u2"))
	a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{MsgCount: len(a.messages), SnapshotTurn: a.snapshots.turnCount()})

	// Rewind both turns in one shot.
	if _, err := a.Rewind(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); string(got) != "orig" {
		t.Errorf("g.txt = %q, want orig (earliest prior across both turns)", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Errorf("new.txt should be removed after rewinding its creating turn")
	}
	if len(a.messages) != 0 || len(a.sess.TurnMetas) != 0 {
		t.Errorf("history not fully trimmed: %d msgs, %d turns", len(a.messages), len(a.sess.TurnMetas))
	}
}

func TestRewindNothing(t *testing.T) {
	a := &Agent{snapshots: &snapshotStore{}, sess: &session.Session{}}
	out, err := a.Rewind(context.Background(), 1)
	if err != nil || out != "Nothing to rewind." {
		t.Errorf("unexpected: out=%q err=%v", out, err)
	}
}

// TestRewindIgnoresDriftedSnapshotCounter guards the v4.0.1 fix: an earlier
// errored/cancelled turn bumps the snapshot turn counter (RunWith calls
// startTurn before the model call) but records no TurnMeta, so the snapshot
// turn id runs AHEAD of the TurnMetas index. Rewind must still revert only the
// intended turns' files by mapping the kept turn to its recorded SnapshotTurn —
// not by reusing the TurnMetas count as a snapshot-turn threshold (which would
// revert one turn too many).
func TestRewindIgnoresDriftedSnapshotCounter(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "f1.txt")
	f2 := filepath.Join(dir, "f2.txt")
	os.WriteFile(f1, []byte("orig1"), 0o644)
	os.WriteFile(f2, []byte("orig2"), 0o644)

	a := &Agent{
		cfg:       &config.Config{WorkingDir: dir},
		snapshots: &snapshotStore{},
		sess:      &session.Session{},
	}
	record := func() {
		a.sess.TurnMetas = append(a.sess.TurnMetas, session.TurnMeta{
			MsgCount: len(a.messages), SnapshotTurn: a.snapshots.turnCount(),
		})
	}

	// An earlier turn that errored/was cancelled: it bumps the snapshot turn
	// counter but records no TurnMeta. Now recorded turns' snapshot ids sit one
	// ahead of their TurnMetas index — the drift the fix must tolerate.
	a.snapshots.startTurn()

	// Recorded turn A: edits f1 (snapshot id 2, TurnMetas index 0).
	a.snapshots.startTurn()
	a.SnapshotPath(f1)
	os.WriteFile(f1, []byte("edited1"), 0o644)
	a.messages = append(a.messages, rwMsg("u1"), rwMsg("a1"))
	record()

	// Recorded turn B: edits f2 (snapshot id 3, TurnMetas index 1).
	a.snapshots.startTurn()
	a.SnapshotPath(f2)
	os.WriteFile(f2, []byte("edited2"), 0o644)
	a.messages = append(a.messages, rwMsg("u2"), rwMsg("a2"))
	record()

	// Rewind 1 must undo ONLY turn B (f2), never turn A's f1.
	if _, err := a.Rewind(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f1); string(got) != "edited1" {
		t.Errorf("f1 (kept turn A) must NOT be reverted; got %q (the drift bug)", got)
	}
	if got, _ := os.ReadFile(f2); string(got) != "orig2" {
		t.Errorf("f2 (turn B) should revert to orig2; got %q", got)
	}
	if len(a.sess.TurnMetas) != 1 {
		t.Errorf("expected 1 turn meta after rewind, got %d", len(a.sess.TurnMetas))
	}
}
