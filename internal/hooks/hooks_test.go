package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func TestLoadFileMissing(t *testing.T) {
	got, err := loadFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("missing file should return nil map, got %+v", got)
	}
}

func TestLoadFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFile(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRunFiresCommandsAndPropagatesEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	proj := t.TempDir()
	out := filepath.Join(proj, "out.txt")
	cfgDir := filepath.Join(proj, config.ProjectDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"pre_tool": ["printf '%s\\n%s\\n' \"$MN_TOOL_NAME\" \"$MN_TOOL_INPUT\" > ` + out + `"]}`
	if err := os.WriteFile(filepath.Join(cfgDir, "hooks.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := New(proj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Empty() {
		t.Fatal("hooks should be loaded")
	}
	_ = set.Run(context.Background(), PreTool, Context{
		WorkingDir: proj,
		ToolName:   "bash",
		ToolInput:  `{"command":"echo hi"}`,
	})

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook didn't write file: %v", err)
	}
	if string(got) != "bash\n{\"command\":\"echo hi\"}\n" {
		t.Errorf("env not forwarded: got %q", got)
	}
}

func TestUnknownEventsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, config.ProjectDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"unknown_event": ["true"], "pre_turn": ["true"]}`
	if err := os.WriteFile(filepath.Join(cfgDir, "hooks.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), ".config"))
	set, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := set.hooks[Event("unknown_event")]; ok {
		t.Errorf("unknown event should be skipped, got %v", got)
	}
	if len(set.hooks[PreTurn]) != 1 {
		t.Errorf("known event should still load, got %v", set.hooks)
	}
}
