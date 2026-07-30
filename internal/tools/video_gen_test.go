package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

type fakeVideoGen struct {
	gotModel  string
	gotPrompt string
	gotOpts   llm.VideoOptions
	out       []llm.GeneratedVideo
	err       error
}

func (f *fakeVideoGen) GenerateVideo(_ context.Context, model, prompt string, refs []llm.ImageRef, opts llm.VideoOptions) ([]llm.GeneratedVideo, error) {
	f.gotModel, f.gotPrompt, f.gotOpts = model, prompt, opts
	return f.out, f.err
}

func TestVideoGenToolSavesAndReturnsVideo(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeVideoGen{out: []llm.GeneratedVideo{{MediaType: "video/mp4", Data: []byte("MP4DATA")}}}
	tool := NewVideoGenTool(NewRegistry(dir), fake, "gcp:veo-3.0-fast-generate-001")

	in, _ := json.Marshal(videoGenInput{Prompt: "a cat walking", OutputPath: "assets/cat.mp4"})
	text, vids, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), in)
	if err != nil {
		t.Fatalf("ExecuteVideos: %v", err)
	}
	if fake.gotModel != "gcp:veo-3.0-fast-generate-001" || fake.gotPrompt != "a cat walking" {
		t.Errorf("forwarded model=%q prompt=%q", fake.gotModel, fake.gotPrompt)
	}
	// Surfaced to the UI.
	if len(vids) != 1 || string(vids[0].Data) != "MP4DATA" || vids[0].MediaType != "video/mp4" {
		t.Errorf("videos = %+v, want one mp4", vids)
	}
	// Written into the workspace at output_path.
	got, err := os.ReadFile(filepath.Join(dir, "assets/cat.mp4"))
	if err != nil || string(got) != "MP4DATA" {
		t.Errorf("saved file = %q, %v; want MP4DATA", got, err)
	}
	// Text (for the model) reports the path, NOT the bytes.
	if text == "" || !filepath.IsAbs(dir) {
		t.Error("expected a descriptive text result")
	}
}

// TestVideoGenToolForwardsOptions checks the aspect_ratio / resolution /
// duration_seconds params are parsed and threaded into llm.VideoOptions, and
// that an invalid aspect_ratio is rejected before any generation call.
func TestVideoGenToolForwardsOptions(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeVideoGen{out: []llm.GeneratedVideo{{MediaType: "video/mp4", Data: []byte("X")}}}
	tool := NewVideoGenTool(NewRegistry(dir), fake, "m").(*videoGenTool)

	in, _ := json.Marshal(videoGenInput{Prompt: "hi", AspectRatio: "9:16", Resolution: "1080p", DurationSeconds: 6})
	if _, _, err := tool.ExecuteVideos(context.Background(), in); err != nil {
		t.Fatalf("ExecuteVideos: %v", err)
	}
	want := llm.VideoOptions{AspectRatio: "9:16", Resolution: "1080p", DurationSeconds: 6}
	if fake.gotOpts != want {
		t.Errorf("forwarded opts = %+v, want %+v", fake.gotOpts, want)
	}

	// An unsupported aspect ratio fails fast, without calling the generator.
	fake.gotOpts = llm.VideoOptions{}
	bad, _ := json.Marshal(videoGenInput{Prompt: "hi", AspectRatio: "4:3"})
	if _, _, err := tool.ExecuteVideos(context.Background(), bad); err == nil {
		t.Error("expected an error for aspect_ratio 4:3")
	}
	if (fake.gotOpts != llm.VideoOptions{}) {
		t.Error("generator must not be called when aspect_ratio is invalid")
	}
}

// TestVideoGenToolDefaultPath verifies a default output path is used when
// output_path is omitted.
func TestVideoGenToolDefaultPath(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeVideoGen{out: []llm.GeneratedVideo{{MediaType: "video/mp4", Data: []byte("X")}}}
	tool := NewVideoGenTool(NewRegistry(dir), fake, "m")
	in, _ := json.Marshal(videoGenInput{Prompt: "hi"})
	if _, vids, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), in); err != nil || len(vids) != 1 {
		t.Fatalf("ExecuteVideos default path: vids=%d err=%v", len(vids), err)
	}
	// A default generated-video-*.mp4 landed in the workspace.
	matches, _ := filepath.Glob(filepath.Join(dir, "generated-video-*.mp4"))
	if len(matches) != 1 {
		t.Errorf("want one default-named video, found %v", matches)
	}
}

// TestVideoGenToolFetchesURL: when the model returns a URL instead of bytes, the
// tool downloads it, saves it, and still surfaces the clip.
func TestVideoGenToolFetchesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("REMOTEMP4"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	fake := &fakeVideoGen{out: []llm.GeneratedVideo{{MediaType: "video/mp4", URL: srv.URL + "/clip.mp4"}}}
	tool := NewVideoGenTool(NewRegistry(dir), fake, "m")
	in, _ := json.Marshal(videoGenInput{Prompt: "x", OutputPath: "clip.mp4"})
	_, vids, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), in)
	if err != nil {
		t.Fatalf("ExecuteVideos: %v", err)
	}
	if len(vids) != 1 {
		t.Fatalf("want one video, got %d", len(vids))
	}
	got, err := os.ReadFile(filepath.Join(dir, "clip.mp4"))
	if err != nil || string(got) != "REMOTEMP4" {
		t.Errorf("downloaded+saved = %q, %v; want REMOTEMP4", got, err)
	}
}

func TestVideoGenToolErrors(t *testing.T) {
	tool := NewVideoGenTool(NewRegistry(t.TempDir()), &fakeVideoGen{err: errors.New("boom")}, "m")
	if _, _, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), json.RawMessage(`{"prompt":"  "}`)); err == nil {
		t.Error("expected error for empty prompt")
	}
	if _, _, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), json.RawMessage(`{"prompt":"x"}`)); err == nil {
		t.Error("expected generator error to surface")
	}
	if _, _, err := tool.(*videoGenTool).ExecuteVideos(context.Background(), json.RawMessage(`{"prompt":"x","reference_images":["../escape.png"]}`)); err == nil {
		t.Error("expected path-escape rejection for reference_images")
	}
}

func TestVideoGenToolMutates(t *testing.T) {
	if !NewVideoGenTool(NewRegistry(t.TempDir()), &fakeVideoGen{}, "m").Mutates() {
		t.Error("generate_video should be mutating (slow billable external call + workspace write)")
	}
}

// TestVideoGenToolIsVideoProducerNotImageProducer: videos must flow via the
// video path (UI only), never as ImageProducer (which would feed bytes to the
// model).
func TestVideoGenToolIsVideoProducerNotImageProducer(t *testing.T) {
	tool := NewVideoGenTool(NewRegistry(t.TempDir()), &fakeVideoGen{}, "m")
	if _, ok := tool.(VideoProducer); !ok {
		t.Error("generate_video must implement VideoProducer")
	}
	if _, ok := tool.(ImageProducer); ok {
		t.Error("generate_video must NOT implement ImageProducer (video is not fed to the model)")
	}
}
