package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// fakeStreamer replays a single text response, satisfying llm.Streamer so the
// headless path can be exercised without the network.
type fakeStreamer struct{ text string }

func (f *fakeStreamer) StreamMessages(_ context.Context, _ string, _ []llm.Message, _ []llm.Tool, h llm.Handler) (*llm.Response, error) {
	if h.OnTextDelta != nil {
		h.OnTextDelta(f.text)
	}
	return &llm.Response{
		Blocks:     []llm.ContentBlock{llm.TextBlock(f.text)},
		StopReason: "end_turn",
	}, nil
}

func (f *fakeStreamer) ListModels(context.Context) ([]config.ModelInfo, error) { return nil, nil }
func (f *fakeStreamer) Usage(context.Context) (*llm.UsageResponse, error)      { return nil, nil }
func (f *fakeStreamer) CheckAuth(context.Context) error                        { return nil }

func newFakeAgent(t *testing.T, reply string) *agent.Agent {
	t.Helper()
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      4,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         t.TempDir(),
	}
	reg := tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	return agent.New(cfg, &fakeStreamer{text: reply}, reg, nil, nil)
}

func TestRunHeadlessText(t *testing.T) {
	ag := newFakeAgent(t, "hello from headless")
	cleaned := false
	code := runHeadless(context.Background(), ag, "say hi", false, func() { cleaned = true })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !cleaned {
		t.Fatal("cleanup was not called")
	}
}

func TestRunHeadlessJSON(t *testing.T) {
	ag := newFakeAgent(t, "structured reply")
	// Capture the JSON written to stdout.
	out := captureStdout(t, func() {
		if code := runHeadless(context.Background(), ag, "go", true, func() {}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
	var got struct {
		OK       bool   `json:"ok"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if !got.OK {
		t.Fatal("ok should be true")
	}
	if got.Response != "structured reply" {
		t.Fatalf("response = %q", got.Response)
	}
}

func TestRunHeadlessEmptyPrompt(t *testing.T) {
	ag := newFakeAgent(t, "unused")
	if code := runHeadless(context.Background(), ag, "   ", false, func() {}); code != 2 {
		t.Fatalf("empty prompt exit code = %d, want 2", code)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("a\nb\nc"); got != "a" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("solo"); got != "solo" {
		t.Fatalf("firstLine = %q", got)
	}
}

func TestSummarizeInputTruncates(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := summarizeInput(long)
	if len([]rune(got)) > 121 { // 120 + ellipsis
		t.Fatalf("summarizeInput too long: %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("long input should be ellipsized")
	}
}
