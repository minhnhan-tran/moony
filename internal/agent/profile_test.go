package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestProfileSlug(t *testing.T) {
	a := profileSlug("/Users/x/proj")
	if a != profileSlug("/Users/x/proj") {
		t.Error("slug must be stable for the same path")
	}
	if a == profileSlug("/Users/y/proj") {
		t.Error("different paths must yield different slugs")
	}
	if strings.ContainsAny(a, "/ ") {
		t.Errorf("slug %q must be filesystem-safe (no / or space)", a)
	}
}

func TestProjectProfileInjectedIntoSystem(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	path, err := projectProfilePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Purpose: a test repo.\nLayout: cmd/ holds the binary."), 0o644); err != nil {
		t.Fatal(err)
	}

	on := &config.Config{WorkingDir: dir, ProjectProfile: true}
	sys := buildSystemPrompt(on, tools.NewRegistry(dir))
	if !strings.Contains(sys, "Project profile (learned") || !strings.Contains(sys, "a test repo") {
		t.Errorf("profile should be injected when ProjectProfile is on:\n%s", sys)
	}
	off := &config.Config{WorkingDir: dir, ProjectProfile: false}
	if strings.Contains(buildSystemPrompt(off, tools.NewRegistry(dir)), "a test repo") {
		t.Error("profile must NOT be injected when ProjectProfile is off")
	}
}

func TestMaybeRefreshProfileWritesThenThrottles(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Purpose: widget maker.\nEntry points: cmd/widget.")}},
	}}
	cfg := &config.Config{Model: "m", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir, ProjectProfile: true}
	ag := New(cfg, fake, tools.NewRegistry(dir), nil, nil)
	for i := 0; i < profileMinMessages; i++ {
		ag.messages = append(ag.messages, llm.UserText("work"))
	}

	ag.maybeRefreshProfile(context.Background(), func(Event) {})
	if got := loadProjectProfile(dir); !strings.Contains(got, "widget maker") {
		t.Fatalf("profile should be written from the session, got %q", got)
	}
	after := fake.calls

	// A profile written moments ago is fresh, so a second refresh is a no-op.
	ag.maybeRefreshProfile(context.Background(), func(Event) {})
	if fake.calls != after {
		t.Errorf("refresh should be throttled when the profile is fresh (calls %d → %d)", after, fake.calls)
	}
}

func TestMaybeRefreshProfileSkips(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	fake := &scriptedStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("x")}}}}

	// Flag off → never refreshes (no aux call, no file).
	off := New(&config.Config{Model: "m", MaxToolOutputBytes: 1 << 20, WorkingDir: dir, ProjectProfile: false}, fake, tools.NewRegistry(dir), nil, nil)
	for i := 0; i < profileMinMessages; i++ {
		off.messages = append(off.messages, llm.UserText("work"))
	}
	off.maybeRefreshProfile(context.Background(), func(Event) {})
	if loadProjectProfile(dir) != "" || fake.calls != 0 {
		t.Error("with ProjectProfile off, nothing should be written and no aux call made")
	}

	// Too few messages → skip (a trivial session taught nothing durable).
	on := New(&config.Config{Model: "m", MaxToolOutputBytes: 1 << 20, WorkingDir: dir, ProjectProfile: true}, fake, tools.NewRegistry(dir), nil, nil)
	on.messages = []llm.Message{llm.UserText("tiny")}
	on.maybeRefreshProfile(context.Background(), func(Event) {})
	if loadProjectProfile(dir) != "" || fake.calls != 0 {
		t.Error("a trivial session (too few messages) should not be profiled")
	}
}
