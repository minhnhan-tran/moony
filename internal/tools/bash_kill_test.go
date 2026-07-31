//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A timed-out or interrupted bash command must kill the whole process TREE.
// exec.CommandContext's default cancel only SIGKILLs the direct child (`sh`), so
// without an explicit process group the real work (a build, a server, a sleep)
// keeps running after the tool returns — and can also hold the output pipe open
// and wedge Wait.
func TestBashTimeoutKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	tool := &bashTool{r: r, maxOutputBytes: 4096}

	// The grandchild outlives the shell and touches a marker well after our
	// timeout; if the tree was killed the marker never appears.
	marker := filepath.Join(dir, "still-alive")
	cmd := "sh -c 'sleep 4; touch " + marker + "' & sleep 30"

	in, _ := json.Marshal(map[string]any{"command": cmd, "timeout_sec": 1})
	start := time.Now()
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected a timeout error")
	}
	if el := time.Since(start); el > 8*time.Second {
		t.Fatalf("Execute took %s — Wait hung past the timeout", el)
	}

	// Give the grandchild past its sleep. A killed group never gets there.
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("grandchild survived the timeout and kept running (process tree not killed)")
	}
}

// Same guarantee for context cancellation (what interrupting a turn does).
func TestBashCancelKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	tool := &bashTool{r: r, maxOutputBytes: 4096}

	marker := filepath.Join(dir, "still-alive")
	cmd := "sh -c 'sleep 4; touch " + marker + "' & sleep 30"
	in, _ := json.Marshal(map[string]any{"command": cmd})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _ = tool.Execute(ctx, in)
	if el := time.Since(start); el > 8*time.Second {
		t.Fatalf("Execute took %s after cancel — Wait hung", el)
	}

	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("grandchild survived cancellation (process tree not killed)")
	}
}

// Guard against the fix regressing into "kills our own process group": the tool
// must still return normal output for a quick command.
func TestBashStillRunsNormally(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	tool := &bashTool{r: NewRegistry(dir), maxOutputBytes: 4096}
	in, _ := json.Marshal(map[string]any{"command": "echo hello-from-bash"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello-from-bash") {
		t.Errorf("output = %q", out)
	}
}
