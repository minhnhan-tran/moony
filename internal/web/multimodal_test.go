package web

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/attach"
	"github.com/pysty1995/moony/internal/llm"
)

func TestReplayMessagesUserAttachments(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{
			llm.TextBlock("what are these?"),
			llm.ImageBlock("image/png", "QUJD"),
			llm.DocumentBlock("application/pdf", "QUJD", "spec.pdf"),
			llm.TextBlock("--- attached file: data.xlsx ---\nA1 B1 C1"),
		},
	}}
	out := replayMessages(msgs, nil, nil, nil, nil)
	if len(out) == 0 {
		t.Fatal("no events")
	}
	u := out[0]
	if u.Type != "user" || u.Text != "what are these?" {
		t.Fatalf("unexpected user event: %+v", u)
	}
	if len(u.Attachments) != 3 {
		t.Fatalf("want 3 attachment chips (image, pdf, file), got %d: %+v", len(u.Attachments), u.Attachments)
	}
	var kinds []string
	for _, a := range u.Attachments {
		kinds = append(kinds, a.Kind)
	}
	if strings.Join(kinds, ",") != "image,document,file" {
		t.Fatalf("attachment kinds = %v", kinds)
	}
	if !strings.HasPrefix(u.Attachments[0].DataURI, "data:image/png;base64,") {
		t.Fatalf("image chip should carry a data URI: %q", u.Attachments[0].DataURI)
	}
	if u.Attachments[2].Name != "data.xlsx" {
		t.Fatalf("extracted-file chip should keep the name, got %q", u.Attachments[2].Name)
	}
}

func TestAttachWireFromAttachment(t *testing.T) {
	img := attachWireFromAttachment(attach.Attachment{Kind: attach.KindImage, Name: "a.png", MediaType: "image/png", Data: []byte("ABC")})
	if img.Kind != "image" || !strings.HasPrefix(img.DataURI, "data:image/png;base64,") {
		t.Fatalf("image wire wrong: %+v", img)
	}
	doc := attachWireFromAttachment(attach.Attachment{Kind: attach.KindDocument, Name: "b.pdf"})
	if doc.Kind != "document" || doc.DataURI != "" {
		t.Fatalf("document wire wrong: %+v", doc)
	}
	file := attachWireFromAttachment(attach.Attachment{Kind: attach.KindText, Name: "c.docx"})
	if file.Kind != "file" {
		t.Fatalf("file wire wrong: %+v", file)
	}
}

func TestStripDataURI(t *testing.T) {
	if got := stripDataURI("data:image/png;base64,QUJD"); got != "QUJD" {
		t.Errorf("stripDataURI data URI = %q", got)
	}
	if got := stripDataURI("QUJD"); got != "QUJD" {
		t.Errorf("stripDataURI raw = %q", got)
	}
}

func TestExtractedFileName(t *testing.T) {
	if n, ok := extractedFileName("--- attached file: report.docx ---\nbody"); !ok || n != "report.docx" {
		t.Errorf("extractedFileName = %q, %v", n, ok)
	}
	if _, ok := extractedFileName("just some text"); ok {
		t.Error("plain text should not match")
	}
}

func TestToWireWorkerActivity(t *testing.T) {
	w := toWire(agent.WorkerActivity{ID: "call-7", Worker: "explorer", Kind: "tool", Text: "reading main.go"})
	if w.Type != "worker" {
		t.Fatalf("type = %q, want worker", w.Type)
	}
	if w.Tool != "explorer" || w.ToolID != "call-7" || w.Summary != "tool" || w.Text != "reading main.go" {
		t.Fatalf("unexpected worker wire event: %+v", w)
	}
}

// A forwarded sub-agent tool event carries parentId + agent so the browser can
// nest it under the parent task card.
func TestToWireNestedSubAgentTool(t *testing.T) {
	s := toWire(agent.ToolStart{ID: "p1/s1", Name: "glob", Input: `{"pattern":"*.go"}`, ParentID: "p1", Agent: "explore"})
	if s.Type != "tool_start" || s.ToolID != "p1/s1" || s.ParentID != "p1" || s.Agent != "explore" || s.Tool != "glob" {
		t.Fatalf("nested tool_start wire = %+v", s)
	}
	e := toWire(agent.ToolEnd{ID: "p1/s1", Name: "glob", Output: "main.go", ParentID: "p1", Agent: "explore"})
	if e.Type != "tool_end" || e.ToolID != "p1/s1" || e.ParentID != "p1" || e.Agent != "explore" {
		t.Fatalf("nested tool_end wire = %+v", e)
	}
}

func TestCommandCatalogIncludesWorkers(t *testing.T) {
	found := false
	for _, c := range commandCatalog(t.TempDir()) {
		if c.Name == "/workers" {
			found = true
		}
	}
	if !found {
		t.Fatal("/workers should be in the web command palette")
	}
}
