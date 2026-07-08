package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestBuildUserMessageTextOnly(t *testing.T) {
	m := buildUserMessage("hello", nil)
	if m.Role != llm.RoleUser || len(m.Content) != 1 || m.Content[0].Text != "hello" {
		t.Fatalf("unexpected message: %+v", m)
	}
}

func TestBuildUserMessageWithAttachments(t *testing.T) {
	atts := []llm.ContentBlock{llm.ImageBlock("image/png", "QUJD")}
	m := buildUserMessage("describe", atts)
	if len(m.Content) != 2 || m.Content[0].Type != llm.BlockText || m.Content[1].Type != llm.BlockImage {
		t.Fatalf("want text + image, got %+v", m.Content)
	}
}

func TestBuildUserMessageAttachmentsNoText(t *testing.T) {
	atts := []llm.ContentBlock{llm.ImageBlock("image/png", "QUJD")}
	m := buildUserMessage("   ", atts)
	if len(m.Content) != 1 || m.Content[0].Type != llm.BlockImage {
		t.Fatalf("blank text should drop the text block, got %+v", m.Content)
	}
}

func TestBuildUserMessageEmptyFallback(t *testing.T) {
	m := buildUserMessage("", nil)
	if len(m.Content) != 1 || m.Content[0].Type != llm.BlockText {
		t.Fatalf("empty turn should fall back to an empty text block, got %+v", m.Content)
	}
}

func TestRunWithRecordsAttachmentInHistory(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{{
		Blocks:     []llm.ContentBlock{llm.TextBlock("ok")},
		StopReason: "end_turn",
	}}}
	reg := tools.NewRegistry(t.TempDir())
	a := New(&config.Config{Model: "m", MaxTokens: 64, MaxAgentSteps: 2, ApprovalPolicy: config.ApprovalAuto, WorkingDir: t.TempDir()}, fake, reg, nil, nil)

	atts := []llm.ContentBlock{llm.ImageBlock("image/png", "QUJD")}
	if err := a.RunWith(context.Background(), "look", atts, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// The first message is the user turn with text + image.
	msgs := a.Messages()
	if len(msgs) == 0 || msgs[0].Role != llm.RoleUser {
		t.Fatalf("no user message recorded: %+v", msgs)
	}
	var hasImage bool
	for _, c := range msgs[0].Content {
		if c.Type == llm.BlockImage {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("user turn should retain the image block: %+v", msgs[0].Content)
	}
}
