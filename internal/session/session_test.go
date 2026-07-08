package session

import (
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

func TestListByDir(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New("m", "/dir/a")
	a.Messages = []llm.Message{llm.UserText("in a")}
	b := New("m", "/dir/b")
	b.Messages = []llm.Message{llm.UserText("in b")}
	a2 := New("m", "/dir/a")
	a2.Messages = []llm.Message{llm.UserText("also a")}
	for _, s := range []*Session{a, b, a2} {
		if err := store.Save(s); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	got, err := store.ListByDir("/dir/a")
	if err != nil {
		t.Fatalf("ListByDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByDir(/dir/a) = %d sessions, want 2", len(got))
	}
	for _, s := range got {
		if s.WorkingDir != "/dir/a" {
			t.Errorf("got session for dir %q, want /dir/a", s.WorkingDir)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sess := New("test-model", "/work/dir")
	sess.Messages = []llm.Message{
		llm.UserText("build me a thing"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.Messages))
	}
	if got.Title != "build me a thing" {
		t.Errorf("title = %q, want derived from first user message", got.Title)
	}

	latest, err := store.Latest("/work/dir")
	if err != nil || latest == nil {
		t.Fatalf("latest: %v (sess=%v)", err, latest)
	}
	if latest.ID != sess.ID {
		t.Errorf("latest id = %s, want %s", latest.ID, sess.ID)
	}

	// A different working directory should have no latest session.
	other, err := store.Latest("/somewhere/else")
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Errorf("expected no session for unrelated dir, got %s", other.ID)
	}
}

// TestStoreRoundTripWithToolImage guards the C1 fix at the persistence layer: a
// session whose history contains a tool_result with image content (e.g. a browser
// screenshot) must Save and Load without error. Before the ContentBlock
// UnmarshalJSON fix, Load failed ("cannot unmarshal array into … content of type
// string") and the entire session became unopenable.
func TestStoreRoundTripWithToolImage(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sess := New("test-model", "/work/dir")
	sess.Messages = []llm.Message{
		llm.UserText("screenshot the page"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "tu_1", Name: "browser", Input: json.RawMessage(`{"action":"snapshot"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			llm.ToolResultBlocks("tu_1", []llm.ContentBlock{
				llm.TextBlock("captured"),
				llm.ImageBlock("image/png", "QUJD"),
			}, false),
		}},
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("load (the C1 regression): %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(got.Messages))
	}
	res := got.Messages[2].Content[0]
	if len(res.ContentList) != 2 {
		t.Fatalf("tool_result ContentList not restored: got %d, want 2", len(res.ContentList))
	}
	if img := res.ContentList[1]; img.Source == nil || img.Source.Data != "QUJD" {
		t.Errorf("image bytes not restored: %+v", img)
	}
}

func TestStoreDelete(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := New("m", "/work")
	sess.Messages = []llm.Message{llm.UserText("hi")}
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load(sess.ID); err == nil {
		t.Error("session still loadable after delete")
	}
	// Delete is idempotent: removing a missing session is not an error.
	if err := store.Delete(sess.ID); err != nil {
		t.Errorf("second delete should be a no-op, got %v", err)
	}
	// Path-traversal ids are rejected.
	if err := store.Delete("../escape"); err == nil {
		t.Error("expected an error for an id with path separators")
	}
}
