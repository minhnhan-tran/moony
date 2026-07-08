package llm

import (
	"encoding/json"
	"regexp"
	"testing"
)

// bedrockToolName is the constraint the gateway's Bedrock models enforce on tool
// names; every wire name we produce must satisfy it.
var bedrockToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestWireToolNameValidNamesUnchanged(t *testing.T) {
	for _, name := range []string{"read_file", "bash", "git_status", "web_fetch", "task", "a-b_c"} {
		if got := wireToolName(name); got != name {
			t.Errorf("wireToolName(%q) = %q, want unchanged", name, got)
		}
	}
}

func TestWireToolNameSanitizesAndStaysValid(t *testing.T) {
	names := []string{
		"mcp.browser-agent.run_task",
		"mcp.browser-agent.browser_screenshot",
		"mcp.demo.read_resource",
		"weird name!@#$%",
	}
	seen := map[string]string{}
	for _, name := range names {
		w := wireToolName(name)
		if !bedrockToolName.MatchString(w) {
			t.Errorf("wireToolName(%q) = %q, which violates %s", name, w, bedrockToolName)
		}
		if w == name {
			t.Errorf("wireToolName(%q) was not sanitized", name)
		}
		if prev, ok := seen[w]; ok {
			t.Errorf("collision: %q and %q both map to %q", prev, name, w)
		}
		seen[w] = name
		if w2 := wireToolName(name); w2 != w {
			t.Errorf("wireToolName(%q) not deterministic: %q vs %q", name, w, w2)
		}
	}
}

func TestWireToolNameLongNameTruncated(t *testing.T) {
	long := "mcp.server." // build a >64 char dotted name
	for i := 0; i < 80; i++ {
		long += "x"
	}
	w := wireToolName(long)
	if !bedrockToolName.MatchString(w) {
		t.Errorf("long name produced invalid wire name %q", w)
	}
	if len(w) > maxToolNameLen {
		t.Errorf("wire name length = %d, want <= %d", len(w), maxToolNameLen)
	}
}

func TestPrepareAndRestoreToolNames(t *testing.T) {
	tools := []Tool{
		{Name: "read_file", InputSchema: json.RawMessage(`{}`)},
		{Name: "mcp.browser-agent.run_task", InputSchema: json.RawMessage(`{}`)},
	}
	// History contains an assistant tool_use that called the dotted MCP tool.
	messages := []Message{
		UserText("go"),
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockToolUse, ID: "t1", Name: "mcp.browser-agent.run_task", Input: json.RawMessage(`{"x":1}`)},
		}},
		{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("t1", "ok", false)}},
	}

	wireTools, wireMsgs, toOrig := prepareToolNames(tools, messages)

	// Every wire tool name is valid for the gateway.
	for _, wt := range wireTools {
		if !bedrockToolName.MatchString(wt.Name) {
			t.Errorf("wire tool name %q invalid", wt.Name)
		}
	}
	// The history tool_use was rewritten to match the renamed definition.
	got := wireMsgs[1].Content[0].Name
	if !bedrockToolName.MatchString(got) {
		t.Errorf("history tool_use name %q invalid", got)
	}
	if got != wireToolName("mcp.browser-agent.run_task") {
		t.Errorf("history tool_use name = %q, want %q", got, wireToolName("mcp.browser-agent.run_task"))
	}
	// Inputs were not mutated.
	if messages[1].Content[0].Name != "mcp.browser-agent.run_task" {
		t.Error("prepareToolNames mutated the caller's messages")
	}
	if tools[1].Name != "mcp.browser-agent.run_task" {
		t.Error("prepareToolNames mutated the caller's tools")
	}

	// A streamed response referencing the wire name must be mapped back.
	resp := &Response{Blocks: []ContentBlock{
		{Type: BlockToolUse, ID: "t2", Name: got, Input: json.RawMessage(`{}`)},
		{Type: BlockText, Text: "done"},
	}}
	restoreToolNames(resp, toOrig)
	if resp.Blocks[0].Name != "mcp.browser-agent.run_task" {
		t.Errorf("restored name = %q, want %q", resp.Blocks[0].Name, "mcp.browser-agent.run_task")
	}
}

// TestSuffixlessToolNameAlias covers the tolerant reverse-lookup: a model that
// emits the sanitized name WITHOUT the collision-suffix (e.g. it underscored a
// dotted name from the prompt) still resolves to the canonical tool.
func TestSuffixlessToolNameAlias(t *testing.T) {
	tools := []Tool{
		{Name: "read_file", InputSchema: json.RawMessage(`{}`)},
		{Name: "mcp.codebase-memory.search_graph", InputSchema: json.RawMessage(`{}`)},
	}
	_, _, toOrig := prepareToolNames(tools, nil)

	const canonical = "mcp.codebase-memory.search_graph"
	// The proper hashed wire name maps back.
	if w := wireToolName(canonical); toOrig[w] != canonical {
		t.Errorf("wire name %q did not map back to %q", w, canonical)
	}
	// And so does the suffix-less form a confused model might emit.
	const noHash = "mcp_codebase-memory_search_graph"
	if toOrig[noHash] != canonical {
		t.Errorf("suffix-less %q = %q, want %q", noHash, toOrig[noHash], canonical)
	}
	// A response using the suffix-less name restores to the canonical for dispatch.
	resp := &Response{Blocks: []ContentBlock{{Type: BlockToolUse, ID: "t", Name: noHash}}}
	restoreToolNames(resp, toOrig)
	if resp.Blocks[0].Name != canonical {
		t.Errorf("restored = %q, want %q", resp.Blocks[0].Name, canonical)
	}
}

// TestSuffixlessAliasSkipsAmbiguous: when two distinct names sanitize to the same
// suffix-less form, no alias is added (it would mis-route).
func TestSuffixlessAliasSkipsAmbiguous(t *testing.T) {
	tools := []Tool{
		{Name: "mcp.a.b", InputSchema: json.RawMessage(`{}`)}, // -> mcp_a_b
		{Name: "mcp.a_b", InputSchema: json.RawMessage(`{}`)}, // -> mcp_a_b too
	}
	_, _, toOrig := prepareToolNames(tools, nil)
	if orig, ok := toOrig["mcp_a_b"]; ok {
		t.Errorf("ambiguous suffix-less form should not be aliased; got %q", orig)
	}
}

func TestPrepareToolNamesNoTools(t *testing.T) {
	msgs := []Message{UserText("hi")}
	wt, wm, toOrig := prepareToolNames(nil, msgs)
	if wt != nil || toOrig != nil {
		t.Errorf("no-tools case should be a no-op, got tools=%v map=%v", wt, toOrig)
	}
	if len(wm) != 1 || wm[0].Content[0].Text != "hi" {
		t.Errorf("messages should pass through unchanged, got %+v", wm)
	}
}
