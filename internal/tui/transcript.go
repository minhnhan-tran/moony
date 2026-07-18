package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// gutterBar is the vertical accent drawn to the left of each message label.
const gutterBar = "▌ "

// entryKind classifies a transcript entry.
type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryThinking
	entryTool
	entryError
	entryInfo
	entryBanner // pre-styled block rendered verbatim (e.g. the welcome box)
	entryRaw    // pre-styled, left-aligned block; indented but NOT re-styled
)

// collapseState controls how a tool entry's body (diff/output) is shown.
// collapseAuto defers to the size heuristic; the others are explicit user
// overrides set by a click or the toggle-all key.
type collapseState int8

const (
	collapseAuto collapseState = iota
	collapseOpen
	collapseClosed
)

// Tool body kinds — what a tool entry has available to show beneath its header.
const (
	bodyNone = iota
	bodyDiff
	bodyOutput
)

const (
	// toolCollapseThreshold is the body height (in lines) above which a tool's
	// output/diff auto-collapses to a one-line summary. Shorter bodies stay
	// inline.
	toolCollapseThreshold = 8
	// toolBodyMaxLines caps how much of a body is drawn when expanded, so a
	// pathologically large output can't flood the viewport (it still scrolls).
	toolBodyMaxLines = 200
)

// entry is a single item in the conversation transcript.
type entry struct {
	kind entryKind
	text string

	// Tool-specific fields.
	toolName   string
	toolInput  string
	toolOutput string
	toolDiff   string
	toolErr    bool
	toolDone   bool
	// toolModel is set for the `task` tool: the delegated sub-agent's model, so
	// the rendered args block can show which model the delegation runs on.
	toolModel string
	// toolID is the model's tool_use id, used to match a ToolEnd event back to
	// its ToolStart entry — essential when calls run concurrently and finish
	// out of order.
	toolID string

	// collapse is the user's expand/collapse override for this tool entry's
	// body. The zero value (collapseAuto) follows the size heuristic.
	collapse collapseState

	// rendered caches the markdown-rendered body (assistant entries only) so we
	// don't re-run Glamour on every refresh — the dominant cost per turn while
	// streaming, since the entire transcript is re-rendered for each token.
	// renderedW is the width it was rendered at; a width change invalidates it.
	rendered  string
	renderedW int

	// streamCache holds the most recent live-markdown render of an in-progress
	// assistant entry. It's refreshed at most once per the (adaptive) stream
	// interval (see streamingBody) so a fast token stream stays smooth;
	// streamCacheW tracks the width it was rendered at and streamCacheLen the
	// byte length of the text it covered (streaming is append-only, so an
	// unchanged length means there's nothing new to show). Distinct from
	// rendered so the final, non-streaming render is always recomputed in full.
	streamCache    string
	streamCacheW   int
	streamCacheLen int

	// side marks an entry that belongs to a `/btw` side-question exchange.
	// Side entries are answered out-of-band (via Agent.AskSide) and never
	// enter the main conversation history; they're rendered dimmed and
	// labelled so it's visually clear they're a parenthetical.
	side bool
}

// toolSpan records the visual line range a collapsible tool entry occupies in
// the most recent render, so a mouse click can be hit-tested back to an entry.
type toolSpan struct {
	idx        int // index into transcript.entries
	start, end int // inclusive line range within the rendered transcript
}

// transcript renders the running conversation to a string for the viewport.
type transcript struct {
	entries  []entry
	renderer *glamour.TermRenderer
	width    int

	// userLabel is the speaker label for the human's messages (the
	// username when known; falls back to "You").
	userLabel string

	// toolSpans is rebuilt on every render: one entry per collapsible tool,
	// mapping its line range for click hit-testing (see toolAt).
	toolSpans []toolSpan

	// lastStreamRender is when the in-progress assistant entry was last rendered
	// through Glamour, used to throttle live-markdown rendering while streaming.
	lastStreamRender time.Time
}

// add appends an entry and returns its index.
func (t *transcript) add(e entry) int {
	t.entries = append(t.entries, e)
	return len(t.entries) - 1
}

// completeTool fills in the result of the running tool entry whose toolID
// matches id and returns true. It returns false when id is empty or no matching
// open entry exists, so the caller can fall back to positional matching. This
// is what lets concurrently-running tools finish out of order without their
// outputs landing on the wrong entry.
func (t *transcript) completeTool(id, output string, isErr bool) bool {
	if id == "" {
		return false
	}
	for i := range t.entries {
		e := &t.entries[i]
		if e.kind == entryTool && !e.toolDone && e.toolID == id {
			e.toolOutput = output
			e.toolErr = isErr
			e.toolDone = true
			return true
		}
	}
	return false
}

// render produces the full transcript text wrapped to width. streamingIdx, if
// >= 0, is rendered as raw text (markdown rendering is deferred until a turn
// completes to keep streaming smooth). Non-streaming assistant entries are
// rendered through Glamour once and cached on the entry until the width
// changes — without this cache, every text-delta refresh re-renders every
// historical message, which is the main source of perceived scroll lag.
//
// As a side effect it rebuilds toolSpans, recording the visual line range of
// each collapsible tool entry so mouse clicks can be hit-tested (see toolAt).
func (t *transcript) render(streamingIdx int) string {
	t.toolSpans = t.toolSpans[:0]
	var b strings.Builder
	line := 0 // index of the next visual line to be written
	for i := range t.entries {
		if i > 0 {
			b.WriteString("\n\n")
			line += 2 // the separator ends the prior line and adds one blank
		}
		s := t.renderEntry(i, streamingIdx)
		start := line
		b.WriteString(s)
		line += strings.Count(s, "\n") // end line of this entry (inclusive)
		if t.entries[i].kind == entryTool {
			if kind, _ := toolBody(t.entries[i]); kind != bodyNone {
				t.toolSpans = append(t.toolSpans, toolSpan{idx: i, start: start, end: line})
			}
		}
	}
	return b.String()
}

// renderEntry renders a single transcript entry (without surrounding
// separators).
func (t *transcript) renderEntry(i, streamingIdx int) string {
	e := &t.entries[i]
	var b strings.Builder
	switch e.kind {
	case entryUser:
		label := t.userLabel
		if label == "" {
			label = "You"
		}
		if e.side {
			label = "↪ " + label + " (side)"
		}
		b.WriteString(userLabelStyle.Render(gutterBar + label))
		b.WriteByte('\n')
		body := bodyStyle.Render(t.wrap(e.text))
		if e.side {
			body = thinkingTextStyle.Render(t.wrap(e.text))
		}
		b.WriteString(indent(body, 2))
	case entryAssistant:
		alabel := "moony"
		if e.side {
			alabel = "↪ side note"
		}
		b.WriteString(assistantLabelStyle.Render(gutterBar + alabel))
		b.WriteByte('\n')
		if i == streamingIdx {
			b.WriteString(t.streamingBody(e))
		} else {
			if e.rendered == "" || e.renderedW != t.width {
				e.rendered = t.markdown(e.text)
				e.renderedW = t.width
			}
			if e.side {
				// Dim side-note bodies so they read as parenthetical.
				b.WriteString(thinkingTextStyle.Render(t.wrap(e.text)))
			} else {
				b.WriteString(e.rendered)
			}
		}
	case entryThinking:
		b.WriteString(thinkingLabelStyle.Render(gutterBar + "thinking"))
		b.WriteByte('\n')
		b.WriteString(indent(thinkingTextStyle.Render(t.wrap(e.text)), 2))
	case entryTool:
		b.WriteString(t.renderTool(*e))
	case entryError:
		b.WriteString(errorStyle.Render(gutterBar + "error"))
		b.WriteByte('\n')
		b.WriteString(indent(errorStyle.Render(t.wrap(e.text)), 2))
	case entryInfo:
		b.WriteString(indent(infoStyle.Render(e.text), 2))
	case entryRaw:
		// Already-styled, left-aligned block (e.g. the /context breakdown's
		// color-coded bar). Indent it but do NOT run it through a style — that
		// would clobber the embedded ANSI colors and the visualization would
		// render as flat dim text.
		b.WriteString(indent(e.text, 2))
	case entryBanner:
		// Pre-styled multi-line block (the welcome box). We center it
		// horizontally within the transcript width so it sits in the
		// middle of the screen rather than hugging the left edge.
		b.WriteString(centerBlock(e.text, t.width))
	}
	return b.String()
}

// renderTool formats a tool call and, depending on the entry's collapse state,
// either its (possibly truncated) body or a one-line "(+N lines)" summary. A
// chevron marks collapsible entries: ▾ expanded, ▸ collapsed.
func (t *transcript) renderTool(e entry) string {
	var b strings.Builder
	icon := "◍" // running
	if e.toolDone {
		if e.toolErr {
			icon = "✗"
		} else {
			icon = "✓"
		}
	}

	kind, raw := toolBody(e)
	collapsible := kind != bodyNone
	collapsed := t.effectiveCollapsed(e)

	if collapsible {
		chev := "▾ "
		if collapsed {
			chev = "▸ "
		}
		b.WriteString(toolMetaStyle.Render(chev))
	}
	b.WriteString(toolLabelStyle.Render(fmt.Sprintf("%s %s", icon, e.toolName)))
	if summary := toolSummary(e.toolName, e.toolInput); summary != "" {
		b.WriteByte(' ')
		b.WriteString(toolMetaStyle.Render(summary))
	}

	if !collapsible {
		return b.String()
	}
	if collapsed {
		n := lineCount(raw)
		noun := "lines"
		if n == 1 {
			noun = "line"
		}
		b.WriteByte(' ')
		b.WriteString(toolMetaStyle.Render(fmt.Sprintf("(+%d %s)", n, noun)))
		return b.String()
	}

	// Expanded: show the diff (for write/edit) or the output preview.
	b.WriteByte('\n')
	switch kind {
	case bodyDiff:
		b.WriteString(indent(renderDiff(raw, toolBodyMaxLines), 2))
	case bodyOutput:
		b.WriteString(indent(toolOutputStyle.Render(previewLines(raw, toolBodyMaxLines)), 2))
	}
	return b.String()
}

// toolBody reports what a tool entry can show beneath its header: a diff (for
// write/edit), command/tool output (once done), or nothing yet. It returns the
// body kind and the raw (uncolorized, untruncated) content. The task tool is
// special-cased: its input is a structured dict (description, subagent_type, a
// multi-line prompt), so it's rendered as an indented key/value block rather
// than clipped onto the one-line header — with the sub-agent's result appended
// once the task finishes.
func toolBody(e entry) (kind int, raw string) {
	if e.toolName == "task" {
		body := prettyArgs(e.toolInput, e.toolModel)
		if e.toolDone && strings.TrimSpace(e.toolOutput) != "" {
			if body != "" {
				body += "\n\n"
			}
			body += e.toolOutput
		}
		if strings.TrimSpace(body) != "" {
			return bodyOutput, body
		}
		return bodyNone, ""
	}
	if strings.TrimSpace(e.toolDiff) != "" {
		return bodyDiff, e.toolDiff
	}
	if e.toolDone && strings.TrimSpace(e.toolOutput) != "" {
		return bodyOutput, e.toolOutput
	}
	return bodyNone, ""
}

// lineCount returns the number of lines in s (ignoring a single trailing
// newline). Empty string is zero lines.
func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// effectiveCollapsed reports whether a tool entry's body should currently be
// hidden, honoring an explicit user override or, in auto mode, the size
// heuristic (collapse bodies taller than toolCollapseThreshold).
func (t *transcript) effectiveCollapsed(e entry) bool {
	kind, raw := toolBody(e)
	if kind == bodyNone {
		return false
	}
	switch e.collapse {
	case collapseOpen:
		return false
	case collapseClosed:
		return true
	default:
		return lineCount(raw) > toolCollapseThreshold
	}
}

// toggleCollapse flips a tool entry between expanded and collapsed, recording an
// explicit user override so it sticks regardless of the size heuristic.
func (t *transcript) toggleCollapse(idx int) {
	if idx < 0 || idx >= len(t.entries) {
		return
	}
	if t.effectiveCollapsed(t.entries[idx]) {
		t.entries[idx].collapse = collapseOpen
	} else {
		t.entries[idx].collapse = collapseClosed
	}
}

// toolAt returns the entry index of the collapsible tool occupying the given
// visual line in the most recent render, or (0,false) if none.
func (t *transcript) toolAt(line int) (int, bool) {
	for _, sp := range t.toolSpans {
		if line >= sp.start && line <= sp.end {
			return sp.idx, true
		}
	}
	return 0, false
}

// toggleAll expands every collapsible tool when any is currently collapsed,
// otherwise collapses them all — a single keyboard shortcut for show/hide all.
func (t *transcript) toggleAll() {
	anyCollapsed := false
	for i := range t.entries {
		if t.entries[i].kind == entryTool && t.effectiveCollapsed(t.entries[i]) {
			anyCollapsed = true
			break
		}
	}
	target := collapseClosed
	if anyCollapsed {
		target = collapseOpen
	}
	for i := range t.entries {
		if t.entries[i].kind == entryTool {
			t.entries[i].collapse = target
		}
	}
}

// renderDiff colorizes a unified diff (green additions, red deletions) and caps
// the number of lines shown.
func renderDiff(diff string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case strings.HasPrefix(ln, "+"):
			b.WriteString(diffAddStyle.Render(ln))
		case strings.HasPrefix(ln, "-"):
			b.WriteString(diffDelStyle.Render(ln))
		case strings.HasPrefix(ln, " ⋮"):
			b.WriteString(diffMetaStyle.Render(ln))
		default:
			b.WriteString(diffContextStyle.Render(ln))
		}
	}
	if truncated {
		b.WriteString("\n")
		b.WriteString(diffMetaStyle.Render(" ⋮ (diff truncated)"))
	}
	return b.String()
}

// wrap hard-wraps plain text to the transcript width.
func (t *transcript) wrap(s string) string {
	w := t.width - 2 // leave room for the 2-space body indent
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// markdown renders s as terminal markdown, falling back to wrapped plain text.
func (t *transcript) markdown(s string) string {
	if t.renderer == nil {
		return indent(t.wrap(s), 2)
	}
	out, err := t.renderer.Render(s)
	if err != nil {
		return indent(t.wrap(s), 2)
	}
	// Glamour pads with surrounding blank lines; trim them so the body sits
	// directly under the speaker label.
	return strings.Trim(out, "\n")
}

// Live-markdown render pacing while an assistant reply streams. Re-parsing the
// whole partial message through Glamour on every token would be O(n²) over the
// turn, so streamingBody throttles it. The interval is adaptive: short replies
// stay snappy at streamRenderMinInterval, but as the message grows (each render
// costs more) the interval scales up toward streamRenderMaxInterval so total
// render work stays roughly bounded regardless of reply length.
const (
	streamRenderMinInterval = 60 * time.Millisecond
	streamRenderMaxInterval = 300 * time.Millisecond
	// streamRenderStepBytes is how many bytes of accumulated reply add one
	// streamRenderMinInterval to the throttle (capped at the max). ~2 KB ≈ a
	// few paragraphs, so ordinary answers never slow down.
	streamRenderStepBytes = 2048
)

// streamRenderInterval returns the throttle window for a partial reply of n
// bytes: streamRenderMinInterval for short text, scaling linearly up to
// streamRenderMaxInterval as the message grows.
func streamRenderInterval(n int) time.Duration {
	d := streamRenderMinInterval * time.Duration(1+n/streamRenderStepBytes)
	if d > streamRenderMaxInterval {
		return streamRenderMaxInterval
	}
	return d
}

// streamingBody renders the in-progress assistant entry. With live markdown on
// (the default), it renders the partial text through Glamour, throttled to an
// adaptive interval (see streamRenderInterval) and cached on the entry; the
// completed turn always gets a final, full render via the non-streaming path.
// Set MOONY_STREAM_MARKDOWN=0 to stream raw text instead (rendered as markdown
// only once the turn completes).
func (t *transcript) streamingBody(e *entry) string {
	if !streamMarkdownEnabled() {
		return indent(t.wrap(e.text), 2)
	}
	// Streaming is append-only: if neither the text length nor the width has
	// changed since the last render, there's nothing new to show — reuse the
	// cache and skip Glamour entirely (this also absorbs refreshes triggered by
	// non-text events mid-turn, e.g. status/spinner updates).
	if e.streamCache != "" && e.streamCacheW == t.width && len(e.text) == e.streamCacheLen {
		return e.streamCache
	}
	now := time.Now()
	widthChanged := e.streamCacheW != t.width
	due := now.Sub(t.lastStreamRender) >= streamRenderInterval(len(e.text))
	if e.streamCache == "" || widthChanged || due {
		e.streamCache = t.markdown(e.text)
		e.streamCacheW = t.width
		e.streamCacheLen = len(e.text)
		t.lastStreamRender = now
	}
	return e.streamCache
}

// streamMarkdownEnabled reports whether the assistant's reply is rendered as
// markdown live while streaming (default), or shown as raw text until the turn
// completes. Disable with MOONY_STREAM_MARKDOWN=0 (or off/false/no/raw) for
// terminals where live re-rendering feels jumpy.
func streamMarkdownEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOONY_STREAM_MARKDOWN"))) {
	case "0", "off", "false", "no", "raw":
		return false
	}
	return true
}

// indent prefixes every line of s with n spaces.
// centerBlock pads each line of s on the left so the block is horizontally
// centered within `width`. ANSI sequences in s are honored via lipgloss.Width
// for measurement, so colored content is centered correctly. When s is wider
// than width, it's returned unchanged so nothing visibly clips.
func centerBlock(s string, width int) string {
	if width <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	maxW := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > maxW {
			maxW = w
		}
	}
	pad := (width - maxW) / 2
	if pad <= 0 {
		return s
	}
	prefix := strings.Repeat(" ", pad)
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func indent(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

// toolSummary produces a friendly one-line description of a tool call from its
// JSON input, picking the most relevant argument(s) per tool.
func toolSummary(name, input string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil || len(args) == 0 {
		return summarizeInput(input)
	}
	str := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	var s string
	switch name {
	case "read_file", "write_file", "edit_file", "list_dir":
		s = str("path")
	case "bash":
		s = str("command")
	case "grep":
		if p := str("pattern"); p != "" {
			s = fmt.Sprintf("%q", p)
			if where := str("path"); where != "" && where != "." {
				s += " in " + where
			}
			if inc := str("include"); inc != "" {
				s += " (" + inc + ")"
			}
		}
	case "glob":
		s = str("pattern")
		if where := str("path"); where != "" && where != "." {
			s += " in " + where
		}
	case "task":
		// The full dict (agent, model, prompt) is shown indented in the body
		// (see toolBody); keep the header to the concise sub-agent name.
		s = str("agent")
	}
	if strings.TrimSpace(s) == "" {
		return summarizeInput(input)
	}
	return clip(strings.ReplaceAll(s, "\n", " "), 80)
}

// summarizeInput produces a compact one-line summary of a tool's JSON input.
func summarizeInput(input string) string {
	s := strings.TrimSpace(input)
	if s == "" || s == "{}" {
		return ""
	}
	return clip(strings.ReplaceAll(s, "\n", " "), 80)
}

// prettyArgs renders a tool's JSON input as readable, indented key/value lines
// instead of one clipped line: scalar values inline ("key: value"), and
// multi-line string values (e.g. a task prompt) on their own lines indented
// beneath a "key:" header. Well-known keys are ordered first for stability, the
// rest alphabetically. When model is non-empty (the task tool), it's appended to
// the "agent" line so the block shows which model the delegation runs on.
// Returns "" when input isn't a non-empty JSON object.
func prettyArgs(input, model string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil || len(args) == 0 {
		return ""
	}
	if model != "" {
		if a, ok := args["agent"].(string); ok {
			args["agent"] = a + "  ·  " + shortModel(model)
		}
	}
	priority := []string{"description", "agent", "subagent_type", "command", "prompt"}
	seen := map[string]bool{}
	var order []string
	for _, k := range priority {
		if _, ok := args[k]; ok {
			order = append(order, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range args {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)

	var b strings.Builder
	for _, k := range order {
		val := valueString(args[k])
		if strings.Contains(val, "\n") {
			b.WriteString(k + ":\n")
			b.WriteString(indent(strings.TrimRight(val, "\n"), 2))
			b.WriteByte('\n')
		} else {
			b.WriteString(k + ": " + val + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// valueString renders a decoded JSON value for prettyArgs: strings verbatim
// (so multi-line text keeps its line breaks), everything else as compact JSON.
func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	default:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x)
	}
}

// clip truncates s to max runes, appending an ellipsis when shortened.
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// clipPath truncates a path to max runes from the LEFT, keeping the trailing
// segment (the filename) visible: "internal/tui/tui.go" → "…tui/tui.go". Used
// by the sidebar's Modified-files section where the filename matters most.
func clipPath(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return "…" + string(r[len(r)-(max-1):])
}

// previewLines returns up to n lines of s with an elision marker if truncated.
func previewLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	kept := lines[:n]
	return strings.Join(kept, "\n") + fmt.Sprintf("\n… (+%d more lines)", len(lines)-n)
}
