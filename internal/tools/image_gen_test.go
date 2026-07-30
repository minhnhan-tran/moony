package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

type fakeImageGen struct {
	gotModel  string
	gotPrompt string
	gotRefs   []llm.ImageRef
	out       []llm.GeneratedImage
	err       error
}

func (f *fakeImageGen) GenerateImage(_ context.Context, model, prompt string, refs []llm.ImageRef) ([]llm.GeneratedImage, error) {
	f.gotModel, f.gotPrompt, f.gotRefs = model, prompt, refs
	return f.out, f.err
}

func TestImageGenToolSavesAndReturnsInline(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeImageGen{out: []llm.GeneratedImage{{MediaType: "image/png", Data: []byte("PNGDATA")}}}
	tool := NewImageGenTool(NewRegistry(dir), fake, "gemini-3.1-flash-image")

	in, _ := json.Marshal(imageGenInput{Prompt: "a red circle", OutputPath: "assets/circle.png"})
	text, imgs, err := tool.(*imageGenTool).ExecuteImages(context.Background(), in)
	if err != nil {
		t.Fatalf("ExecuteImages: %v", err)
	}
	// Prompt/model forwarded.
	if fake.gotModel != "gemini-3.1-flash-image" || fake.gotPrompt != "a red circle" {
		t.Errorf("forwarded model=%q prompt=%q", fake.gotModel, fake.gotPrompt)
	}
	// Returned inline for the transcript / model to see.
	if len(imgs) != 1 || string(imgs[0].Data) != "PNGDATA" || imgs[0].MediaType != "image/png" {
		t.Errorf("inline images = %+v, want one PNG", imgs)
	}
	// Written into the workspace at output_path.
	got, err := os.ReadFile(filepath.Join(dir, "assets/circle.png"))
	if err != nil || string(got) != "PNGDATA" {
		t.Errorf("saved file = %q, %v; want PNGDATA", got, err)
	}
	if text == "" {
		t.Error("expected a descriptive text result")
	}
}

func TestImageGenToolTempWhenNoOutputPath(t *testing.T) {
	fake := &fakeImageGen{out: []llm.GeneratedImage{{MediaType: "image/png", Data: []byte("X")}}}
	tool := NewImageGenTool(NewRegistry(t.TempDir()), fake, "m")
	in, _ := json.Marshal(imageGenInput{Prompt: "hi"})
	if _, imgs, err := tool.(*imageGenTool).ExecuteImages(context.Background(), in); err != nil || len(imgs) != 1 {
		t.Fatalf("ExecuteImages temp path: imgs=%d err=%v", len(imgs), err)
	}
}

func TestImageGenToolErrors(t *testing.T) {
	tool := NewImageGenTool(NewRegistry(t.TempDir()), &fakeImageGen{err: errors.New("boom")}, "m")
	// Empty prompt rejected before any call.
	if _, _, err := tool.(*imageGenTool).ExecuteImages(context.Background(), json.RawMessage(`{"prompt":"  "}`)); err == nil {
		t.Error("expected error for empty prompt")
	}
	// Generator error surfaced.
	if _, _, err := tool.(*imageGenTool).ExecuteImages(context.Background(), json.RawMessage(`{"prompt":"x"}`)); err == nil {
		t.Error("expected generator error to surface")
	}
	// Reference image outside the workspace is rejected.
	if _, _, err := tool.(*imageGenTool).ExecuteImages(context.Background(), json.RawMessage(`{"prompt":"x","reference_images":["../escape.png"]}`)); err == nil {
		t.Error("expected path-escape rejection for reference_images")
	}
}

func TestImageGenToolMutates(t *testing.T) {
	if !NewImageGenTool(NewRegistry(t.TempDir()), &fakeImageGen{}, "m").Mutates() {
		t.Error("generate_image should be mutating (billable external call + workspace write)")
	}
}
