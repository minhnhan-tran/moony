package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/agent"
)

// makeLines returns a string of n numbered lines, for exercising the
// collapse-by-size heuristic.
func makeLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "line %d", i+1)
	}
	return b.String()
}

func TestLineCount(t *testing.T) {
	cases := map[string]int{
		"":            0,
		"one":         1,
		"a\nb":        2,
		"a\nb\n":      2, // trailing newline ignored
		"a\nb\nc\n\n": 3, // only the final trailing newline is trimmed
	}
	for in, want := range cases {
		if got := lineCount(in); got != want {
			t.Errorf("lineCount(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestEffectiveCollapsedHeuristic(t *testing.T) {
	tr := &transcript{width: 80}

	// Short output (≤ threshold) stays expanded in auto mode.
	short := entry{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(3)}
	if tr.effectiveCollapsed(short) {
		t.Error("short output should not auto-collapse")
	}

	// Long output (> threshold) auto-collapses.
	long := entry{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(toolCollapseThreshold + 5)}
	if !tr.effectiveCollapsed(long) {
		t.Error("long output should auto-collapse")
	}

	// Explicit overrides win over the heuristic both ways.
	long.collapse = collapseOpen
	if tr.effectiveCollapsed(long) {
		t.Error("collapseOpen should force expanded")
	}
	short.collapse = collapseClosed
	if !tr.effectiveCollapsed(short) {
		t.Error("collapseClosed should force collapsed")
	}

	// A tool with no body yet is never "collapsed".
	running := entry{kind: entryTool, toolName: "bash"}
	if tr.effectiveCollapsed(running) {
		t.Error("a body-less tool should not be collapsed")
	}
}

func TestRenderToolCollapsedVsExpanded(t *testing.T) {
	tr := &transcript{width: 80}
	out := makeLines(20)
	e := entry{kind: entryTool, toolName: "grep", toolInput: `{"pattern":"foo"}`, toolDone: true, toolOutput: out}

	// Auto (collapsed because 20 > threshold): body hidden, summary shown.
	collapsed := tr.renderTool(e)
	if strings.Contains(collapsed, "line 1") {
		t.Errorf("collapsed tool should hide its body, got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "(+20 lines)") {
		t.Errorf("collapsed tool should show a line-count summary, got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "▸") {
		t.Errorf("collapsed tool should show a ▸ chevron, got:\n%s", collapsed)
	}

	// Forced open: body shown, chevron flips.
	e.collapse = collapseOpen
	expanded := tr.renderTool(e)
	if !strings.Contains(expanded, "line 1") || !strings.Contains(expanded, "line 20") {
		t.Errorf("expanded tool should show its full body, got:\n%s", expanded)
	}
	if !strings.Contains(expanded, "▾") {
		t.Errorf("expanded tool should show a ▾ chevron, got:\n%s", expanded)
	}
}

func TestRenderToolNoBodyNoChevron(t *testing.T) {
	tr := &transcript{width: 80}
	// A completed tool with empty output has nothing to toggle.
	e := entry{kind: entryTool, toolName: "list_dir", toolInput: `{"path":"."}`, toolDone: true, toolOutput: ""}
	out := tr.renderTool(e)
	if strings.ContainsAny(out, "▸▾") {
		t.Errorf("body-less tool should have no chevron, got:\n%s", out)
	}
}

// TestPrettyArgs checks the dict is rendered as indented key/value lines, with a
// multi-line value (the prompt) broken onto its own indented lines, and the
// optional model appended to the agent line.
func TestPrettyArgs(t *testing.T) {
	in := `{"agent":"explore","prompt":"line one\nline two"}`
	want := "agent: explore\nprompt:\n  line one\n  line two"
	if got := prettyArgs(in, ""); got != want {
		t.Errorf("prettyArgs mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
	// With a model, it's appended to the agent line (shortModel strips the
	// vendor prefix: anthropic.claude-opus-4-8 → claude-opus-4-8).
	wantM := "agent: explore  ·  claude-opus-4-8\nprompt:\n  line one\n  line two"
	if got := prettyArgs(in, "anthropic.claude-opus-4-8"); got != wantM {
		t.Errorf("prettyArgs with model mismatch:\n got:\n%s\nwant:\n%s", got, wantM)
	}
	// Non-object / empty input yields no body.
	if got := prettyArgs("not json", ""); got != "" {
		t.Errorf("invalid input should yield empty, got %q", got)
	}
	if got := prettyArgs("{}", ""); got != "" {
		t.Errorf("empty object should yield empty, got %q", got)
	}
}

// TestRenderTaskDictIndented verifies the task tool shows its input dict as an
// indented multi-line body (not clipped onto one line), with the agent's model
// appended to the agent line and a concise header.
func TestRenderTaskDictIndented(t *testing.T) {
	tr := &transcript{width: 80}
	e := entry{kind: entryTool, toolName: "task", toolModel: "anthropic.claude-opus-4-8",
		toolInput: `{"agent":"explore","prompt":"step one\nstep two"}`}

	out := tr.renderTool(e)
	// Header carries the concise sub-agent name.
	if !strings.Contains(out, "task") || !strings.Contains(out, "explore") {
		t.Errorf("task header should name the sub-agent, got:\n%s", out)
	}
	// The dict is shown as indented key/value lines, not a one-line JSON blob.
	if strings.Contains(out, `{"agent"`) {
		t.Errorf("task input should not be shown as raw one-line JSON, got:\n%s", out)
	}
	// The model is appended to the agent line.
	if !strings.Contains(out, "agent: explore  ·  claude-opus-4-8") {
		t.Errorf("task body should show the agent with its model, got:\n%s", out)
	}
	for _, want := range []string{"prompt:", "step one", "step two"} {
		if !strings.Contains(out, want) {
			t.Errorf("task body should contain %q, got:\n%s", want, out)
		}
	}
	// A short dict is collapsible (has a chevron) and expanded by default.
	if !strings.ContainsAny(out, "▸▾") {
		t.Errorf("task with a dict body should be collapsible, got:\n%s", out)
	}
}

func TestToggleCollapse(t *testing.T) {
	tr := &transcript{width: 80}
	tr.add(entry{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(3)})

	// Short body starts expanded; toggling collapses it, toggling again expands.
	tr.toggleCollapse(0)
	if !tr.effectiveCollapsed(tr.entries[0]) {
		t.Error("first toggle should collapse a short body")
	}
	tr.toggleCollapse(0)
	if tr.effectiveCollapsed(tr.entries[0]) {
		t.Error("second toggle should expand it again")
	}
}

func TestToggleAll(t *testing.T) {
	tr := &transcript{width: 80}
	tr.add(entry{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(3)})  // short → expanded
	tr.add(entry{kind: entryTool, toolName: "bash", toolDone: true, toolOutput: makeLines(20)}) // long → collapsed
	tr.add(entry{kind: entryUser, text: "ignored"})

	// One is collapsed (the long one), so toggleAll expands everything.
	tr.toggleAll()
	for i, e := range tr.entries {
		if e.kind == entryTool && tr.effectiveCollapsed(e) {
			t.Errorf("entry %d should be expanded after toggleAll", i)
		}
	}
	// Now all are expanded, so toggleAll collapses everything.
	tr.toggleAll()
	for i, e := range tr.entries {
		if e.kind == entryTool && !tr.effectiveCollapsed(e) {
			t.Errorf("entry %d should be collapsed after second toggleAll", i)
		}
	}
}

func TestRenderRecordsToolSpans(t *testing.T) {
	tr := &transcript{width: 80}
	tr.add(entry{kind: entryUser, text: "hello"})                                               // 2 lines (label + body)
	tr.add(entry{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(20)}) // collapsible
	tr.add(entry{kind: entryTool, toolName: "noop", toolDone: true, toolOutput: ""})            // body-less → no span

	rendered := tr.render(-1)

	if len(tr.toolSpans) != 1 {
		t.Fatalf("expected exactly 1 collapsible tool span, got %d", len(tr.toolSpans))
	}
	sp := tr.toolSpans[0]
	if sp.idx != 1 {
		t.Errorf("span should point at entry index 1, got %d", sp.idx)
	}

	// The recorded span must line up with where the tool actually rendered.
	lines := strings.Split(rendered, "\n")
	if sp.start < 0 || sp.start >= len(lines) {
		t.Fatalf("span start %d out of range (%d lines)", sp.start, len(lines))
	}
	if !strings.Contains(lines[sp.start], "grep") {
		t.Errorf("line at span start should contain the tool name, got %q", lines[sp.start])
	}

	// toolAt should map any line within the span back to the entry.
	if idx, ok := tr.toolAt(sp.start); !ok || idx != 1 {
		t.Errorf("toolAt(%d) = (%d,%v), want (1,true)", sp.start, idx, ok)
	}
	if _, ok := tr.toolAt(sp.end + 100); ok {
		t.Error("toolAt past the end should miss")
	}
}

func TestClickTogglesToolUnderCursor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 40)

	// A single long-output tool entry so it sits at the top of the viewport.
	m.trans.entries = []entry{{kind: entryTool, toolName: "grep", toolInput: `{"pattern":"x"}`, toolDone: true, toolOutput: makeLines(20)}}
	m.refresh()

	if len(m.trans.toolSpans) != 1 {
		t.Fatalf("expected 1 tool span, got %d", len(m.trans.toolSpans))
	}
	// Auto-collapsed to start (20 > threshold).
	if !m.trans.effectiveCollapsed(m.trans.entries[0]) {
		t.Fatal("tool should start auto-collapsed")
	}

	// Click on the tool's header row: screen Y = headerHeight + span.start - YOffset.
	headerH := lipgloss.Height(m.headerView())
	clickY := headerH + m.trans.toolSpans[0].start - m.viewport.YOffset
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: clickY})
	m = next.(Model)

	if m.trans.effectiveCollapsed(m.trans.entries[0]) {
		t.Error("clicking the tool line should have expanded it")
	}
}

func TestClickOutsideViewportIgnored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 40)
	m.trans.entries = []entry{{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(20)}}
	m.refresh()

	// A click in the header band (Y=0) must not toggle anything.
	if m.toggleToolAtClick(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: 0}) {
		t.Error("a click in the header should be ignored")
	}
}

func TestCtrlOTogglesAllTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 40)
	m.trans.entries = []entry{
		{kind: entryTool, toolName: "grep", toolDone: true, toolOutput: makeLines(3)},
		{kind: entryTool, toolName: "bash", toolDone: true, toolOutput: makeLines(20)},
	}
	m.refresh()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = next.(Model)
	for i, e := range m.trans.entries {
		if e.kind == entryTool && m.trans.effectiveCollapsed(e) {
			t.Errorf("entry %d should be expanded after ctrl+o", i)
		}
	}
}

func TestCompleteToolByID(t *testing.T) {
	tr := &transcript{width: 80}
	tr.add(entry{kind: entryTool, toolName: "grep", toolID: "A"})
	tr.add(entry{kind: entryTool, toolName: "read_file", toolID: "B"})

	// Empty id never matches.
	if tr.completeTool("", "x", false) {
		t.Error("empty id should not match")
	}
	// Unknown id never matches.
	if tr.completeTool("Z", "x", false) {
		t.Error("unknown id should not match")
	}
	// Matching id fills the right entry.
	if !tr.completeTool("B", "B-out", false) {
		t.Fatal("expected to match entry B")
	}
	if tr.entries[1].toolOutput != "B-out" || !tr.entries[1].toolDone {
		t.Errorf("entry B not completed correctly: %+v", tr.entries[1])
	}
	if tr.entries[0].toolDone {
		t.Error("entry A should still be open")
	}
	// A second call for the same id won't re-match a finished entry.
	if tr.completeTool("B", "again", false) {
		t.Error("a finished entry should not match again")
	}
}

// TestToolEndMatchesByIDOutOfOrder is the parallel-execution case: two tools
// start, then finish in the opposite order. Each result must land on its own
// entry, not the most-recently-started one.
func TestToolEndMatchesByIDOutOfOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)
	m.state = stateBusy

	send := func(ev agent.Event) {
		next, _ := m.Update(streamMsg{ev: ev})
		m = next.(Model)
	}

	send(agent.ToolStart{ID: "A", Name: "grep"})
	send(agent.ToolStart{ID: "B", Name: "read_file"})
	if m.toolsInFlight != 2 {
		t.Fatalf("toolsInFlight = %d, want 2", m.toolsInFlight)
	}
	if got := m.statusView(); !strings.Contains(got, "running 2 tools") {
		t.Errorf("status bar should report 2 running tools, got:\n%s", got)
	}

	// Finish B first, then A.
	send(agent.ToolEnd{ID: "B", Name: "read_file", Output: "B-out"})
	send(agent.ToolEnd{ID: "A", Name: "grep", Output: "A-out"})

	got := map[string]string{}
	for _, e := range m.trans.entries {
		if e.kind == entryTool {
			got[e.toolID] = e.toolOutput
		}
	}
	if got["A"] != "A-out" {
		t.Errorf("entry A output = %q, want A-out", got["A"])
	}
	if got["B"] != "B-out" {
		t.Errorf("entry B output = %q, want B-out", got["B"])
	}
	if m.toolsInFlight != 0 {
		t.Errorf("toolsInFlight = %d after both finished, want 0", m.toolsInFlight)
	}
}
