package web

import (
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/workflow"
)

// resolve delivers text to a specific pause; wait returns it. Distinct pause ids
// for one run must not collide.
func TestApprovalHubTextAndKeying(t *testing.T) {
	h := &approvalHub{pending: map[string]chan pauseResolution{}}
	done := make(chan struct{})

	got1 := make(chan pauseResolution, 1)
	got2 := make(chan pauseResolution, 1)
	go func() { got1 <- h.wait("run1", "p1", done) }()
	go func() { got2 <- h.wait("run1", "p2", done) }()

	// Let both waiters register.
	deadline := time.After(2 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.pending)
		h.mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("waiters never registered")
		case <-time.After(time.Millisecond):
		}
	}

	if !h.resolve("run1", "p2", pauseResolution{approved: true, text: "hello"}) {
		t.Fatal("resolve p2 returned false")
	}
	select {
	case r := <-got2:
		if !r.approved || r.text != "hello" {
			t.Fatalf("p2 = %+v, want approved hello", r)
		}
	case <-time.After(time.Second):
		t.Fatal("p2 waiter did not return")
	}

	// p1 is still pending and independent.
	if !h.resolve("run1", "p1", pauseResolution{approved: false, text: "no"}) {
		t.Fatal("resolve p1 returned false")
	}
	select {
	case r := <-got1:
		if r.approved || r.text != "no" {
			t.Fatalf("p1 = %+v, want !approved no", r)
		}
	case <-time.After(time.Second):
		t.Fatal("p1 waiter did not return")
	}

	// Resolving an unknown pause is a no-op.
	if h.resolve("run1", "ghost", pauseResolution{approved: true}) {
		t.Error("resolving an unknown pause should return false")
	}
}

// A closed done channel unblocks a waiter with a non-approval (so the run unwinds).
func TestApprovalHubCancelOnDone(t *testing.T) {
	h := &approvalHub{pending: map[string]chan pauseResolution{}}
	done := make(chan struct{})
	close(done)
	r := h.wait("run", "p1", done)
	if r.approved {
		t.Errorf("cancelled wait = %+v, want !approved", r)
	}
}

// runUserInput threads the resumed text through as the node's output; runApproval
// reject fails. Both run via a runner with a live emit + run id (HITL active).
func TestRunUserInputAndApprovalResume(t *testing.T) {
	mkRunner := func() *workflowRunner {
		return &workflowRunner{
			runID: "runX",
			emit:  func(workflow.RunEvent) {},
			done:  make(chan struct{}),
		}
	}
	node := workflow.Node{ID: "n1", Type: workflow.NodeUserInput, Data: workflow.NodeData{Message: "Name?"}}
	in := workflow.NodeInput{Node: node, Input: "ignored", Vars: map[string]any{}}

	// user-input: resume with text → that text is the output.
	wr := mkRunner()
	go func() {
		// Block until the pause registers in the hub, then deliver the
		// resolution exactly once. This avoids the race where resolve() is
		// called before wait() has inserted the channel into pending.
		deadline := time.After(2 * time.Second)
		for {
			workflowApprovals.mu.Lock()
			_, ok := workflowApprovals.pending[pauseKey("runX", "p1")]
			workflowApprovals.mu.Unlock()
			if ok {
				workflowApprovals.resolve("runX", "p1", pauseResolution{approved: true, text: "Ada"})
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	out, err := wr.runUserInput(node.Data, in, nil)
	if err != nil {
		t.Fatalf("runUserInput: %v", err)
	}
	if out.Text != "Ada" {
		t.Errorf("output = %q, want Ada", out.Text)
	}

	// approval reject → error.
	wr2 := mkRunner()
	an := workflow.Node{ID: "a1", Type: workflow.NodeApproval, Data: workflow.NodeData{Message: "ok?"}}
	go func() {
		// Same registration-wait pattern: deliver the rejection only after
		// wait() has inserted the channel into pending.
		deadline := time.After(2 * time.Second)
		for {
			workflowApprovals.mu.Lock()
			_, ok := workflowApprovals.pending[pauseKey("runX", "p1")]
			workflowApprovals.mu.Unlock()
			if ok {
				workflowApprovals.resolve("runX", "p1", pauseResolution{approved: false})
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	if _, err := wr2.runApproval(an.Data, workflow.NodeInput{Node: an, Vars: map[string]any{}}, nil); err == nil {
		t.Error("rejected approval should error")
	}
}

// Without a live emit/runID (e.g. evals), HITL nodes auto-resolve so unattended
// runs complete instead of hanging.
func TestHITLAutoResolvesWhenInactive(t *testing.T) {
	wr := &workflowRunner{} // no emit, no runID
	node := workflow.Node{ID: "n1", Type: workflow.NodeUserInput, Data: workflow.NodeData{}}
	out, err := wr.runUserInput(node.Data, workflow.NodeInput{Node: node, Input: "passthrough", Vars: map[string]any{}}, nil)
	if err != nil || out.Text != "passthrough" {
		t.Fatalf("inactive user-input = (%q, %v), want passthrough", out.Text, err)
	}
	out2, err2 := wr.runApproval(node.Data, workflow.NodeInput{Node: node, Input: "x", Vars: map[string]any{}}, nil)
	if err2 != nil || out2.Text != "x" {
		t.Fatalf("inactive approval = (%q, %v), want x", out2.Text, err2)
	}
}
