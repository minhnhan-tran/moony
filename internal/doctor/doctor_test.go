package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// find returns the check with the given name, or nil.
func find(r *Report, name string) *Check {
	for i := range r.Checks {
		if r.Checks[i].Name == name {
			return &r.Checks[i]
		}
	}
	return nil
}

func TestCheckHooks(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		dir := t.TempDir()
		r := &Report{}
		checkHooks(r, &config.Config{WorkingDir: dir})
		c := find(r, "hooks config")
		if c == nil || c.Status != StatusOK || !strings.Contains(c.Detail, "no hooks") {
			t.Fatalf("unexpected: %+v", c)
		}
	})

	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.ProjectDir, "hooks.json"),
			`{"post_turn":["echo done"],"pre_tool":["./audit.sh"]}`)
		r := &Report{}
		checkHooks(r, &config.Config{WorkingDir: dir})
		c := find(r, "hooks config")
		if c == nil || c.Status != StatusOK {
			t.Fatalf("expected OK, got %+v", c)
		}
		if !strings.Contains(c.Detail, "2 hook(s)") || !strings.Contains(c.Detail, "post_turn") {
			t.Fatalf("detail missing counts/events: %q", c.Detail)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.ProjectDir, "hooks.json"), `{not json`)
		r := &Report{}
		checkHooks(r, &config.Config{WorkingDir: dir})
		c := find(r, "hooks config")
		if c == nil || c.Status != StatusFail {
			t.Fatalf("expected Fail, got %+v", c)
		}
	})
}

func TestCheckSubagentsAndCommands(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		dir := t.TempDir()
		r := &Report{}
		checkSubagentsAndCommands(r, &config.Config{WorkingDir: dir})
		if c := find(r, "sub-agents"); c == nil || c.Status != StatusOK {
			t.Fatalf("sub-agents: %+v", c)
		}
		if c := find(r, "custom commands"); c == nil || c.Status != StatusOK {
			t.Fatalf("custom commands: %+v", c)
		}
	})

	t.Run("persona with unknown tool warns", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.ProjectDir, "agents", "explore.md"),
			"---\ntools: read_file, frobnicate\n---\nYou explore.")
		writeFile(t, filepath.Join(dir, config.ProjectDir, "commands", "review.md"),
			"---\ndescription: review\n---\n$ARGUMENTS")
		r := &Report{}
		checkSubagentsAndCommands(r, &config.Config{WorkingDir: dir})

		sa := find(r, "sub-agents")
		if sa == nil || sa.Status != StatusWarn || !strings.Contains(sa.Detail, "frobnicate") {
			t.Fatalf("expected warn naming frobnicate, got %+v", sa)
		}
		cc := find(r, "custom commands")
		if cc == nil || cc.Status != StatusOK || !strings.Contains(cc.Detail, "/review") {
			t.Fatalf("expected /review listed, got %+v", cc)
		}
	})

	t.Run("known tools do not warn", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.ProjectDir, "agents", "ro.md"),
			"---\ntools: read_file, glob, grep, mcp.fs.read, task\n---\nRead only.")
		r := &Report{}
		checkSubagentsAndCommands(r, &config.Config{WorkingDir: dir})
		if sa := find(r, "sub-agents"); sa == nil || sa.Status != StatusOK {
			t.Fatalf("expected OK, got %+v", sa)
		}
	})
}

func TestCheckSandbox(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		r := &Report{}
		checkSandbox(r, &config.Config{SandboxMode: config.SandboxOff})
		c := find(r, "bash sandbox")
		if c == nil || c.Status != StatusOK || !strings.Contains(c.Detail, "off") {
			t.Fatalf("unexpected: %+v", c)
		}
	})

	t.Run("on", func(t *testing.T) {
		r := &Report{}
		checkSandbox(r, &config.Config{SandboxMode: config.SandboxWorkdir, SandboxNetwork: true})
		c := find(r, "bash sandbox")
		if c == nil {
			t.Fatal("missing check")
		}
		// On macOS with sandbox-exec present it's OK; elsewhere (or without the
		// tool) it's a warning. Either way it must not silently pass as "off".
		if runtime.GOOS != "darwin" && c.Status != StatusWarn {
			t.Fatalf("expected warn on non-darwin, got %+v", c)
		}
		if strings.Contains(c.Detail, "off") {
			t.Fatalf("enabled sandbox reported as off: %+v", c)
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
