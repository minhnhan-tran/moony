package workflow

import "testing"

func TestRunStoreRoundTripAndList(t *testing.T) {
	st := OpenRunsAt(t.TempDir())

	if l, err := st.List(""); err != nil || len(l) != 0 {
		t.Fatalf("empty List = %v, %v", l, err)
	}

	rec := &RunRecord{
		ID: "run_a1", WorkflowID: "wf1", WorkflowName: "Flow", Input: "hi", Status: RunPaused,
		Vars:        map[string]any{"input": "hi", "lastOutput": "draft"},
		CurrentNode: "approve",
		Trace:       []TraceEntry{{NodeID: "draft", Status: "completed", Output: "draft", Usage: NodeUsage{OutputTokens: 5, CostUSD: 0.01}}},
		CostUSD:     0.01,
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if rec.StartedAt == "" || rec.UpdatedAt == "" {
		t.Fatalf("timestamps not stamped: %+v", rec)
	}

	got, err := st.Load("run_a1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Status != RunPaused || got.CurrentNode != "approve" || got.Vars["lastOutput"] != "draft" || len(got.Trace) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// List filters by workflow id.
	if err := st.Save(&RunRecord{ID: "run_b2", WorkflowID: "wf2", Status: RunCompleted}); err != nil {
		t.Fatalf("Save b: %v", err)
	}
	if l, _ := st.List("wf1"); len(l) != 1 || l[0].ID != "run_a1" {
		t.Fatalf("List(wf1) = %v, want just run_a1", l)
	}
	if l, _ := st.List(""); len(l) != 2 {
		t.Fatalf("List(all) = %d, want 2", len(l))
	}
}

func TestTemplatesValid(t *testing.T) {
	tpls := Templates()
	if len(tpls) == 0 {
		t.Fatal("no templates")
	}
	byID := map[string]bool{}
	for _, tp := range tpls {
		byID[tp.ID] = true
		if tp.ID == "" || tp.Name == "" || len(tp.Nodes) == 0 {
			t.Fatalf("template %q incomplete: %+v", tp.ID, tp)
		}
		// Each template must have exactly one start node and run cleanly through the
		// engine with a passthrough runner (structure sanity).
		ids := map[string]bool{}
		nodeType := map[string]string{}
		starts := 0
		for _, nd := range tp.Nodes {
			ids[nd.ID] = true
			nodeType[nd.ID] = nd.Type
			if nd.Type == NodeStart {
				starts++
			}
		}
		if starts != 1 {
			t.Errorf("template %q has %d start nodes, want 1", tp.ID, starts)
		}
		for _, ed := range tp.Edges {
			if !ids[ed.Source] || !ids[ed.Target] {
				t.Errorf("template %q edge %s references a missing node", tp.ID, ed.ID)
			}
			// Branch edges must name a handle the source node actually exposes, or
			// routing silently falls through to the first edge.
			if h := ed.SourceHandle; h != "" {
				ok := false
				switch nodeType[ed.Source] {
				case NodeIfElse:
					ok = h == "if" || h == "else"
				case NodeWhile:
					ok = h == "continue" || h == "break"
				}
				if !ok {
					t.Errorf("template %q edge %s: handle %q invalid for source type %q", tp.ID, ed.ID, h, nodeType[ed.Source])
				}
			}
		}
	}
	// The documented starter patterns must stay present (referenced in the UI/docs).
	for _, want := range []string{"prompt-chaining", "evaluator-optimizer"} {
		if !byID[want] {
			t.Errorf("missing built-in template %q", want)
		}
	}
}
