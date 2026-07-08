package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageBlockJSONAnthropic(t *testing.T) {
	b := ImageBlock("image/png", "QUJD")
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"type":"image"`, `"source"`, `"type":"base64"`, `"media_type":"image/png"`, `"data":"QUJD"`} {
		if !strings.Contains(s, want) {
			t.Errorf("image block JSON missing %s: %s", want, s)
		}
	}
	// A plain text block must not emit a source field.
	if td, _ := json.Marshal(TextBlock("hi")); strings.Contains(string(td), "source") {
		t.Errorf("text block should not have a source: %s", td)
	}
}

// TestToolResultBlocksJSON verifies a tool_result with image content serializes
// `content` as an ARRAY of blocks (text + image) — the form the Anthropic API
// needs so the model can see e.g. a browser screenshot — while a plain
// tool_result keeps `content` as a string.
func TestToolResultBlocksJSON(t *testing.T) {
	b := ToolResultBlocks("tu_1", []ContentBlock{TextBlock("snapshot"), ImageBlock("image/png", "QUJD")}, false)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"type":"tool_result"`, `"tool_use_id":"tu_1"`, `"content":[`, `"type":"text"`, `"text":"snapshot"`, `"type":"image"`, `"media_type":"image/png"`} {
		if !strings.Contains(s, want) {
			t.Errorf("tool_result(blocks) JSON missing %s:\n%s", want, s)
		}
	}
	// A plain (string-content) tool_result must still serialize content as a string.
	plain, _ := json.Marshal(ToolResultBlock("tu_2", "done", false))
	if !strings.Contains(string(plain), `"content":"done"`) {
		t.Errorf("plain tool_result should keep a string content: %s", plain)
	}
}

// TestContentBlockRoundTrip guards the C1 fix: a tool_result whose content is an
// array of blocks (text + image) must survive marshal→unmarshal so a saved
// session that used an image-returning tool (e.g. a browser screenshot) can be
// reopened. Before the fix this failed with "cannot unmarshal array into …
// content of type string", making the whole session unloadable.
func TestContentBlockRoundTrip(t *testing.T) {
	orig := []Message{
		UserText("look at this"),
		{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockToolUse, ID: "tu_1", Name: "browser", Input: json.RawMessage(`{}`)}}},
		{Role: RoleUser, Content: []ContentBlock{
			ToolResultBlocks("tu_1", []ContentBlock{TextBlock("snapshot"), ImageBlock("image/png", "QUJD")}, false),
			ToolResultBlock("tu_2", "plain text result", true),
		}},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal (the C1 regression): %v", err)
	}

	// The image tool_result's array content must come back as ContentList, with
	// the image bytes intact.
	imgResult := back[2].Content[0]
	if imgResult.Type != BlockToolResult || imgResult.ToolUseID != "tu_1" {
		t.Fatalf("unexpected image tool_result block: %+v", imgResult)
	}
	if len(imgResult.ContentList) != 2 {
		t.Fatalf("ContentList not restored: got %d blocks, want 2", len(imgResult.ContentList))
	}
	if imgResult.Content != "" {
		t.Errorf("array-content block should not populate the string Content, got %q", imgResult.Content)
	}
	img := imgResult.ContentList[1]
	if img.Type != BlockImage || img.Source == nil || img.Source.Data != "QUJD" || img.Source.MediaType != "image/png" {
		t.Errorf("image block not restored: %+v", img)
	}

	// The plain string tool_result must round-trip as a string, not a list.
	plainResult := back[2].Content[1]
	if plainResult.Content != "plain text result" || plainResult.ContentList != nil || !plainResult.IsError {
		t.Errorf("plain tool_result not restored: %+v", plainResult)
	}

	// A second pass must be stable (idempotent round-trip).
	data2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(data2) != string(data) {
		t.Errorf("round-trip not stable:\n first: %s\nsecond: %s", data, data2)
	}
}

func TestDocumentBlockJSONAnthropic(t *testing.T) {
	b := DocumentBlock("application/pdf", "QUJD", "report.pdf")
	data, _ := json.Marshal(b)
	s := string(data)
	for _, want := range []string{`"type":"document"`, `"media_type":"application/pdf"`, `"title":"report.pdf"`} {
		if !strings.Contains(s, want) {
			t.Errorf("document block JSON missing %s: %s", want, s)
		}
	}
}

func TestToOpenAIMessagesImageBecomesParts(t *testing.T) {
	msgs := []Message{{
		Role: RoleUser,
		Content: []ContentBlock{
			TextBlock("what is this?"),
			ImageBlock("image/png", "QUJD"),
		},
	}}
	out := toOpenAIMessages("", msgs)
	if len(out) != 1 {
		t.Fatalf("want 1 message, got %d", len(out))
	}
	parts, ok := out[0].Content.([]oaiContentPart)
	if !ok {
		t.Fatalf("user content should be a parts array, got %T", out[0].Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,QUJD") {
		t.Fatalf("image part should carry a data URI, got %q", parts[1].ImageURL.URL)
	}
}

func TestToOpenAIMessagesTextOnlyStaysString(t *testing.T) {
	out := toOpenAIMessages("", []Message{UserText("hello")})
	if len(out) != 1 {
		t.Fatalf("want 1 message, got %d", len(out))
	}
	if _, ok := out[0].Content.(string); !ok {
		t.Fatalf("text-only content should stay a string, got %T", out[0].Content)
	}
}

func TestToOpenAIMessagesDocumentNoted(t *testing.T) {
	msgs := []Message{{
		Role: RoleUser,
		Content: []ContentBlock{
			TextBlock("read this"),
			DocumentBlock("application/pdf", "QUJD", "spec.pdf"),
		},
	}}
	out := toOpenAIMessages("", msgs)
	// No image → content is a plain string carrying the document note.
	s, ok := out[0].Content.(string)
	if !ok {
		t.Fatalf("want string content, got %T", out[0].Content)
	}
	if !strings.Contains(s, "spec.pdf") {
		t.Fatalf("document should be noted by name on the OpenAI route: %q", s)
	}
}
