package workflow

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	st := OpenAt(t.TempDir())
	wf := &Workflow{
		ID:   "wf_test1",
		Name: "My Flow",
		Nodes: []Node{
			{ID: "n1", Type: NodeStart, Position: Position{X: 10, Y: 20}, Data: NodeData{Label: "Start"}},
			{ID: "n2", Type: NodeAgent, Position: Position{X: 200, Y: 20}, Data: NodeData{Label: "Agent", Instructions: "do it", Tools: []string{"read_file"}}},
			{ID: "n3", Type: NodeEnd, Position: Position{X: 400, Y: 20}},
		},
		Edges: []Edge{
			{ID: "e1", Source: "n1", Target: "n2"},
			{ID: "e2", Source: "n2", Target: "n3", SourceHandle: "out"},
		},
	}
	if err := st.Save(wf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if wf.CreatedAt == "" || wf.UpdatedAt == "" {
		t.Fatalf("Save did not stamp timestamps: %+v", wf)
	}

	got, err := st.Load("wf_test1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "My Flow" || len(got.Nodes) != 3 || len(got.Edges) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Nodes[1].Data.Instructions != "do it" || len(got.Nodes[1].Data.Tools) != 1 {
		t.Fatalf("node data not preserved: %+v", got.Nodes[1])
	}
	if got.Edges[1].SourceHandle != "out" {
		t.Fatalf("edge handle not preserved: %+v", got.Edges[1])
	}
}

func TestStoreListSortedAndDelete(t *testing.T) {
	st := OpenAt(t.TempDir())

	// Empty (missing) dir → empty list, no error.
	if l, err := st.List(); err != nil || len(l) != 0 {
		t.Fatalf("empty List = %v, %v", l, err)
	}

	for _, id := range []string{"wf_a", "wf_b"} {
		if err := st.Save(&Workflow{ID: id, Name: id}); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	l, err := st.List()
	if err != nil || len(l) != 2 {
		t.Fatalf("List = %v (%d), %v", l, len(l), err)
	}

	if err := st.Delete("wf_a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Load("wf_a"); err != ErrNotFound {
		t.Fatalf("Load after delete = %v, want ErrNotFound", err)
	}
	if err := st.Delete("wf_a"); err != ErrNotFound {
		t.Fatalf("re-Delete = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsUnsafeID(t *testing.T) {
	st := OpenAt(t.TempDir())
	for _, bad := range []string{"", "../escape", "a/b", "a.b", "with space", "tab\t"} {
		if err := st.Save(&Workflow{ID: bad, Name: "x"}); err == nil {
			t.Fatalf("Save(%q) should have failed", bad)
		}
		if _, err := st.Load(bad); err != ErrNotFound {
			t.Fatalf("Load(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	// No stray files should have escaped the store directory.
	if entries, _ := filepath.Glob(filepath.Join(st.dir, "*")); len(entries) != 0 {
		t.Fatalf("unexpected files created: %v", entries)
	}
}
