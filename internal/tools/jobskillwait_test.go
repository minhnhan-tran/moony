package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// KillAll must not return while a watcher goroutine can still write the project's
// state dir. It previously did: the watcher recorded the job's final state (and
// persisted jobs.json) after KillAll returned, which recreated .moony/ under a
// directory the caller had already begun deleting — surfacing as a flaky
// "TempDir RemoveAll cleanup: ... directory not empty" in unrelated tests.
func TestKillAllWaitsForPersistence(t *testing.T) {
	for i := 0; i < 25; i++ {
		dir := t.TempDir()
		// Exercise the same path as the real failure: through the Registry's bash
		// tool, which is what persists jobs.json into the project dir.
		r := NewRegistry(dir)
		r.RegisterDefaults(10000)
		bt := &bashTool{r: r, maxOutputBytes: 10000}
		in, _ := json.Marshal(map[string]any{"command": "sleep 5", "background": true})
		if _, err := bt.Execute(context.Background(), in); err != nil {
			t.Fatalf("background bash: %v", err)
		}
		r.Jobs().KillAll()

		// Emulate what t.TempDir() cleanup does immediately after a test returns.
		// If KillAll can return while a watcher is still about to persist, this
		// RemoveAll races that write and the directory reappears.
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove project dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, config.ProjectDir)); err == nil {
			t.Fatalf("iteration %d: %s was recreated after KillAll returned "+
				"(a watcher goroutine persisted state too late)", i, config.ProjectDir)
		}
	}
}

// A job that fails to start has no watcher goroutine, so KillAll must not block
// waiting for one.
func TestKillAllDoesNotBlockOnUnstartedJob(t *testing.T) {
	dir := t.TempDir()
	store := newJobStore()
	store.start(sandboxSetting{}, []string{dir}, "definitely-not-a-real-binary-xyz", jobOpts{})
	_ = dir
	done := make(chan struct{})
	go func() { store.KillAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("KillAll blocked on a job whose watcher never ran")
	}
}
