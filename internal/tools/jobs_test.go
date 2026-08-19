package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond up to d, returning whether it became true.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestJobStoreRunToCompletion(t *testing.T) {
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `printf 'hello'`, jobOpts{})

	if !waitFor(2*time.Second, func() bool { return j.status() == jobDone }) {
		t.Fatal("job did not reach done state")
	}

	out, state, exit, _ := j.snapshot()
	if state != jobDone || exit != 0 {
		t.Fatalf("want done/0, got %s/%d", state, exit)
	}
	if out != "hello" {
		t.Fatalf("want output %q, got %q", "hello", out)
	}
	// A second read returns no new output (cursor advanced).
	out2, _, _, _ := j.snapshot()
	if out2 != "" {
		t.Fatalf("second snapshot should be empty, got %q", out2)
	}
}

func TestJobStoreFailingCommand(t *testing.T) {
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `echo oops >&2; exit 3`, jobOpts{})

	if !waitFor(2*time.Second, func() bool { return j.status() == jobFailed }) {
		t.Fatal("job did not reach failed state")
	}
	out, _, exit, _ := j.snapshot()
	if exit != 3 {
		t.Fatalf("want exit 3, got %d", exit)
	}
	if !strings.Contains(out, "oops") {
		t.Fatalf("want stderr captured, got %q", out)
	}
}

func TestJobStoreKill(t *testing.T) {
	s := newJobStore()
	j := s.start(sandboxSetting{}, []string{t.TempDir()}, `sleep 30`, jobOpts{})

	// Should be running.
	if j.status() != jobRunning {
		t.Fatalf("want running, got %s", j.status())
	}
	j.cancel()
	if !waitFor(2*time.Second, func() bool { return j.status() == jobKilled }) {
		t.Fatal("job did not reach killed state after cancel")
	}
}

func TestJobStoreKillAll(t *testing.T) {
	s := newJobStore()
	j1 := s.start(sandboxSetting{}, []string{t.TempDir()}, `sleep 30`, jobOpts{})
	j2 := s.start(sandboxSetting{}, []string{t.TempDir()}, `sleep 30`, jobOpts{})

	s.KillAll()

	for _, j := range []*job{j1, j2} {
		if !waitFor(2*time.Second, func() bool { return j.status() == jobKilled }) {
			t.Fatalf("job %s not killed by KillAll", j.id)
		}
	}
}

func TestJobStoreKillAllNil(t *testing.T) {
	var s *JobStore
	s.KillAll() // must not panic
}

func TestJobStoreIncrementalReads(t *testing.T) {
	s := newJobStore()
	dir := t.TempDir()
	// Emit, pause, emit. Polling snapshot() between the two writes should yield
	// the chunks incrementally rather than all at once.
	j := s.start(sandboxSetting{}, []string{dir}, `printf 'one'; sleep 0.3; printf 'two'`, jobOpts{})

	var collected strings.Builder
	sawIncremental := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, state, _, _ := j.snapshot()
		if out != "" {
			collected.WriteString(out)
			// If we got "one" before "two" arrived, reads are incremental.
			if collected.String() == "one" {
				sawIncremental = true
			}
		}
		if state != jobRunning && collected.Len() >= len("onetwo") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if collected.String() != "onetwo" {
		t.Fatalf("want total output %q, got %q", "onetwo", collected.String())
	}
	if !sawIncremental {
		t.Fatal("expected to observe the first chunk before the second")
	}
}

func TestBashBackgroundFlagStartsJob(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDefaults(10000)
	bt := &bashTool{r: r, maxOutputBytes: 10000}

	in, _ := json.Marshal(map[string]any{"command": "sleep 5", "background": true})
	out, err := bt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("background bash: %v", err)
	}
	if !strings.Contains(out, "Started background job bg_1") {
		t.Fatalf("unexpected output: %q", out)
	}
	if jobs := r.Jobs().list(); len(jobs) != 1 {
		t.Fatalf("want 1 job registered, got %d", len(jobs))
	}
	r.Jobs().KillAll()
}

func TestBashOutputUnknownJob(t *testing.T) {
	r := NewRegistry(t.TempDir())
	tool := &bashOutputTool{r: r, max: 10000}
	in, _ := json.Marshal(map[string]any{"job_id": "bg_999"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for unknown job")
	}
}

func TestBashKillUnknownJob(t *testing.T) {
	r := NewRegistry(t.TempDir())
	tool := &bashKillTool{r: r}
	in, _ := json.Marshal(map[string]any{"job_id": "bg_999"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for unknown job")
	}
}

func TestBashOutputAndKillRoundTrip(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDefaults(10000)
	bt := &bashTool{r: r, maxOutputBytes: 10000}

	in, _ := json.Marshal(map[string]any{"command": "sleep 30", "background": true})
	if _, err := bt.Execute(context.Background(), in); err != nil {
		t.Fatalf("start: %v", err)
	}

	out := &bashOutputTool{r: r, max: 10000}
	oin, _ := json.Marshal(map[string]any{"job_id": "bg_1"})
	res, err := out.Execute(context.Background(), oin)
	if err != nil {
		t.Fatalf("bash_output: %v", err)
	}
	if !strings.Contains(res, "running") {
		t.Fatalf("want running status, got %q", res)
	}

	kill := &bashKillTool{r: r}
	kres, err := kill.Execute(context.Background(), oin)
	if err != nil {
		t.Fatalf("bash_kill: %v", err)
	}
	if !strings.Contains(kres, "Killed job bg_1") {
		t.Fatalf("unexpected kill result: %q", kres)
	}

	// Wait for the kill to actually land, then a second kill reports finished.
	j, _ := r.Jobs().get("bg_1")
	if !waitFor(2*time.Second, func() bool { return j.status() == jobKilled }) {
		t.Fatal("job not killed")
	}
	kres2, _ := kill.Execute(context.Background(), oin)
	if !strings.Contains(kres2, "already finished") {
		t.Fatalf("want already-finished message, got %q", kres2)
	}
}

func TestJobStoreDeleteJob(t *testing.T) {
	s := newJobStore()
	dir := t.TempDir()
	j := s.start(sandboxSetting{}, []string{dir}, `sleep 30`, jobOpts{})

	if len(s.Jobs()) != 1 {
		t.Fatalf("want 1 job, got %d", len(s.Jobs()))
	}

	// Deleting non-existent job returns false
	if s.DeleteJob("bg_999") {
		t.Fatal("expected false for unknown job delete")
	}

	// Deleting running job terminates process and removes from store
	if !s.DeleteJob(j.id) {
		t.Fatal("expected true when deleting job")
	}

	if len(s.Jobs()) != 0 {
		t.Fatalf("want 0 jobs after delete, got %d", len(s.Jobs()))
	}

	if _, ok := s.Job(j.id); ok {
		t.Fatal("job should not exist in store after delete")
	}
}

func TestRegistryDeleteJob(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDefaults(10000)
	bt := &bashTool{r: r, maxOutputBytes: 10000}

	in, _ := json.Marshal(map[string]any{"command": "printf 'done'", "background": true})
	if _, err := bt.Execute(context.Background(), in); err != nil {
		t.Fatalf("start: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		jobs := r.Jobs().Jobs()
		return len(jobs) > 0 && !jobs[0].Running
	}) {
		t.Fatal("job did not finish")
	}

	if !r.DeleteJob("bg_1") {
		t.Fatal("expected DeleteJob to return true")
	}

	if len(r.Jobs().Jobs()) != 0 {
		t.Fatalf("want 0 jobs after deletion, got %d", len(r.Jobs().Jobs()))
	}
}
