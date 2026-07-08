package workflow

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDictionaryWorkflowSuccess(t *testing.T) {
	ctx := context.Background()
	wf := NewDictionaryWorkflow()

	// We'll use 1ms delay multiplier to make tests fast
	nodeFunc := GetDictionaryNodeFunc(1 * time.Millisecond)

	var events []RunEvent
	emit := func(ev RunEvent) {
		events = append(events, ev)
	}

	// Use default clock
	now := func() int64 {
		return time.Now().UnixNano()
	}

	err := Run(ctx, wf, "learning", nodeFunc, emit, now)
	if err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	// Find the final aggregator output
	var finalOutput string
	for _, ev := range events {
		if ev.NodeID == "aggregator" && ev.Type == "node_completed" {
			finalOutput = ev.Output
		}
	}

	if finalOutput == "" {
		t.Fatal("Could not find finish output from aggregator node")
	}

	// Assert the translations are aggregated correctly
	if !strings.Contains(finalOutput, "aprendizaje") {
		t.Errorf("Expected Spanish translation 'aprendizaje', got: %s", finalOutput)
	}
	if !strings.Contains(finalOutput, "apprentissage") {
		t.Errorf("Expected French translation 'apprentissage', got: %s", finalOutput)
	}
	if !strings.Contains(finalOutput, "lernen") {
		t.Errorf("Expected German translation 'lernen', got: %s", finalOutput)
	}
}

func TestDictionaryWorkflowFallback(t *testing.T) {
	ctx := context.Background()
	wf := NewDictionaryWorkflow()
	nodeFunc := GetDictionaryNodeFunc(0) // No delay

	var events []RunEvent
	emit := func(ev RunEvent) {
		events = append(events, ev)
	}

	err := Run(ctx, wf, "awesome-agent", nodeFunc, emit, nil)
	if err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	var finalOutput string
	for _, ev := range events {
		if ev.NodeID == "aggregator" && ev.Type == "node_completed" {
			finalOutput = ev.Output
		}
	}

	// Assert fallbacks are correct
	if !strings.Contains(finalOutput, "awesome-agent-es") {
		t.Errorf("Expected fallback Spanish translation 'awesome-agent-es', got: %s", finalOutput)
	}
	if !strings.Contains(finalOutput, "awesome-agent-fr") {
		t.Errorf("Expected fallback French translation 'awesome-agent-fr', got: %s", finalOutput)
	}
	if !strings.Contains(finalOutput, "awesome-agent-de") {
		t.Errorf("Expected fallback German translation 'awesome-agent-de', got: %s", finalOutput)
	}
}

func TestDictionaryWorkflowParallelismAndEvents(t *testing.T) {
	ctx := context.Background()
	wf := NewDictionaryWorkflow()

	// Use 15ms multiplier so we can measure distinct time overlapping reliably
	nodeFunc := GetDictionaryNodeFunc(15 * time.Millisecond)

	var events []RunEvent
	emit := func(ev RunEvent) {
		events = append(events, ev)
	}

	start := time.Now()
	err := Run(ctx, wf, "hello", nodeFunc, emit, nil)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Workflow failed: %v", err)
	}

	// Combined sequential sleep would be:
	// Spanish: 15 * 150 = 2.25s
	// French:  15 * 250 = 3.75s
	// German:  15 * 100 = 1.50s
	// Total: 7.5 seconds sequential
	// With parallel execution, the total time should be close to the longest branch: 3.75 seconds.
	// Since we used a multiplier of 15ms, let's verify it took less than 5 seconds (5000ms),
	// which is significantly lower than the 7.5s sequential sum!
	if duration > 5*time.Second {
		t.Errorf("Expected parallel execution to take < 5s, took %v (likely ran sequentially)", duration)
	}

	// Check parallel event tracing structure
	forkEvents := 0
	parallelGroupCount := 0
	branchMap := make(map[int]bool)

	for _, ev := range events {
		if ev.NodeID == "fork" {
			forkEvents++
		}
		if ev.GroupID == "fork" {
			parallelGroupCount++
			if ev.NodeID == "translate_es" || ev.NodeID == "translate_fr" || ev.NodeID == "translate_de" {
				branchMap[ev.Branch] = true
			}
		}
	}

	if forkEvents == 0 {
		t.Error("Expected to find events for parallel fork node")
	}
	if parallelGroupCount == 0 {
		t.Error("Expected to find events carrying parallel GroupID 'fork'")
	}
	if len(branchMap) < 3 {
		t.Errorf("Expected events from 3 distinct branches (0, 1, 2) in parallel group, got: %v", branchMap)
	}
}
