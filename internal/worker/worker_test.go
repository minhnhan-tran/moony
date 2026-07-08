package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeWorkers(t *testing.T, dir, body string) {
	t.Helper()
	sqe := filepath.Join(dir, ".sqe")
	if err := os.MkdirAll(sqe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sqe, "workers.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectWorkers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeWorkers(t, dir, `{
	  "workers": [
	    {"name": "explorer", "backend": "optimus", "description": "read-only research"},
	    {"name": "claude", "backend": "claude", "model": "anthropic.claude-sonnet-4-6"},
	    {"name": "off", "backend": "claude", "disabled": true},
	    {"name": "bad", "backend": "nonsense"},
	    {"name": "", "backend": "claude"}
	  ]
	}`)

	cfgs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Disabled workers are now included (the Manager decides what's active);
	// only invalid/nameless ones are dropped.
	if len(cfgs) != 3 {
		t.Fatalf("want 3 workers (explorer, claude, off; skip bad/nameless), got %d: %+v", len(cfgs), cfgs)
	}
	if cfgs[0].Name != "explorer" || cfgs[1].Name != "claude" || cfgs[2].Name != "off" {
		t.Fatalf("unexpected order/names: %+v", cfgs)
	}
	if !cfgs[2].Disabled {
		t.Fatalf("the 'off' worker should be marked disabled")
	}
}

func TestManagerConfigsEnabledOnlyAndAll(t *testing.T) {
	off := false
	m := New(t.TempDir(), []Config{
		{Name: "on1", Backend: BackendOptimus},
		{Name: "off1", Backend: BackendClaude, Disabled: true},
		{Name: "on2", Backend: BackendClaude, ReadOnly: &off},
	})
	if got := len(m.Configs()); got != 2 {
		t.Fatalf("Configs() should return enabled-only (2), got %d", got)
	}
	if got := len(m.All()); got != 3 {
		t.Fatalf("All() should return every worker (3), got %d", got)
	}
}

func TestManagerDispatchDisabled(t *testing.T) {
	m := New(t.TempDir(), []Config{{Name: "w", Backend: BackendClaude, Disabled: true}})
	if _, err := m.Dispatch(context.Background(), "w", "task", nil); err == nil {
		t.Fatal("dispatch to a disabled worker should error")
	}
}

func TestSetEnabledAndSetModelPersist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeWorkers(t, dir, `{
	  "workers": [
	    {"name": "explorer", "backend": "optimus", "model": "old/model"}
	  ]
	}`)

	cfgs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(dir, cfgs)

	// Disable, then confirm it persists to workers.json and reloads disabled.
	if err := m.SetEnabled("explorer", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if len(m.Configs()) != 0 {
		t.Fatal("disabled worker should drop out of Configs()")
	}
	reloaded, _ := Load(dir)
	if len(reloaded) != 1 || !reloaded[0].Disabled {
		t.Fatalf("disable did not persist: %+v", reloaded)
	}

	// Re-enable + change the model; both persist.
	if err := m.SetEnabled("explorer", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if err := m.SetModel("explorer", "new/model"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	reloaded, _ = Load(dir)
	if len(reloaded) != 1 || reloaded[0].Disabled || reloaded[0].Model != "new/model" {
		t.Fatalf("enable+model did not persist: %+v", reloaded)
	}
}

func TestSetEnabledUnknown(t *testing.T) {
	m := New(t.TempDir(), []Config{{Name: "w", Backend: BackendClaude}})
	if err := m.SetEnabled("nope", false); err == nil {
		t.Fatal("expected error for unknown worker")
	}
}

func TestReadOnlyDefault(t *testing.T) {
	if !(Config{}).readOnly() {
		t.Fatal("readOnly should default to true")
	}
	f := false
	if (Config{ReadOnly: &f}).readOnly() {
		t.Fatal("explicit readOnly:false should be honored")
	}
}

// fakeSession records prompts and returns a canned result.
type fakeSession struct {
	prompts []string
	closed  bool
}

func (f *fakeSession) Prompt(_ context.Context, task string, onEvent func(Event)) (Result, error) {
	f.prompts = append(f.prompts, task)
	onEvent(Event{Kind: "tool", Text: "did a thing"})
	return Result{Text: "answer to: " + task, SessionID: "s1", OK: true}, nil
}
func (f *fakeSession) Close() error { f.closed = true; return nil }

func TestManagerDispatchReusesSession(t *testing.T) {
	// Stub the backend opener so no real process is spawned.
	fake := &fakeSession{}
	opens := 0
	orig := open
	open = func(_ context.Context, _ Config, _ Options) (Session, error) { opens++; return fake, nil }
	t.Cleanup(func() { open = orig })

	m := New(t.TempDir(), []Config{{Name: "w", Backend: BackendOptimus}})
	if m.Empty() {
		t.Fatal("manager should not be empty")
	}

	var toolEvents int
	onEvent := func(e Event) {
		if e.Kind == "tool" {
			toolEvents++
		}
	}
	r1, err := m.Dispatch(context.Background(), "w", "first", onEvent)
	if err != nil || !r1.OK || r1.Text != "answer to: first" {
		t.Fatalf("dispatch 1: %+v err=%v", r1, err)
	}
	r2, err := m.Dispatch(context.Background(), "w", "second", onEvent)
	if err != nil || r2.Text != "answer to: second" {
		t.Fatalf("dispatch 2: %+v err=%v", r2, err)
	}
	if opens != 1 {
		t.Fatalf("session should be opened once and reused, opened %d times", opens)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("want 2 prompts on the same session, got %v", fake.prompts)
	}
	if toolEvents != 2 {
		t.Fatalf("want 2 tool events streamed, got %d", toolEvents)
	}
}

func TestManagerResetAndCloseAll(t *testing.T) {
	fake := &fakeSession{}
	opens := 0
	orig := open
	open = func(_ context.Context, _ Config, _ Options) (Session, error) { opens++; return fake, nil }
	t.Cleanup(func() { open = orig })

	m := New(t.TempDir(), []Config{{Name: "w", Backend: BackendClaude}})
	_, _ = m.Dispatch(context.Background(), "w", "x", nil)
	m.Reset("w")
	if !fake.closed {
		t.Fatal("Reset should close the session")
	}
	// After reset, the next dispatch opens a fresh session.
	_, _ = m.Dispatch(context.Background(), "w", "y", nil)
	if opens != 2 {
		t.Fatalf("want a new session after reset, opens=%d", opens)
	}
	m.CloseAll()
}

func TestManagerUnknownWorker(t *testing.T) {
	m := New(t.TempDir(), nil)
	if _, err := m.Dispatch(context.Background(), "nope", "t", nil); err == nil {
		t.Fatal("expected error for unknown worker")
	}
}

func TestNilManager(t *testing.T) {
	var m *Manager
	if !m.Empty() {
		t.Fatal("nil manager should be empty")
	}
	m.CloseAll() // must not panic
	if m.Configs() != nil {
		t.Fatal("nil manager Configs should be nil")
	}
}

// ---- claude backend (pure bits) --------------------------------------------

func TestParseClaudeJSON(t *testing.T) {
	r, ok := parseClaudeJSON([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"the answer","session_id":"abc"}`))
	if !ok || r.Result != "the answer" || r.SessionID != "abc" || r.IsError {
		t.Fatalf("happy parse wrong: %+v ok=%v", r, ok)
	}
	r, ok = parseClaudeJSON([]byte("noise\n{\"type\":\"result\",\"result\":\"x\",\"session_id\":\"z\"}\n"))
	if !ok || r.SessionID != "z" {
		t.Fatalf("line-scan parse wrong: %+v ok=%v", r, ok)
	}
	if _, ok := parseClaudeJSON([]byte("not json at all")); ok {
		t.Fatal("garbage should not parse")
	}
}

func TestClaudeBuildArgsReadOnlyAndResume(t *testing.T) {
	s := &claudeSession{opts: Options{ReadOnly: true, Model: "m1", Agent: "explore"}, bin: "claude"}
	args := s.buildArgs("do it")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p do it", "--output-format json", "--permission-mode plan", "--disallowedTools Edit Write Bash", "--model m1", "--agents explore"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--resume") {
		t.Error("no --resume before a session id exists")
	}
	// After a turn sets the session id, the next invocation resumes it.
	s.sessionID = "sess-123"
	if !strings.Contains(strings.Join(s.buildArgs("again"), " "), "--resume sess-123") {
		t.Error("expected --resume with the stored session id")
	}
}

func TestClaudeBuildArgsWritable(t *testing.T) {
	s := &claudeSession{opts: Options{ReadOnly: false}, bin: "claude"}
	if strings.Contains(strings.Join(s.buildArgs("x"), " "), "plan") {
		t.Error("non-read-only worker should not force plan mode")
	}
}

func TestGeminiBuildArgs(t *testing.T) {
	// Read-only → plan mode, with the model threaded through.
	s := &geminiSession{opts: Options{ReadOnly: true, Model: "gemini-2.5-pro"}, bin: "gemini"}
	joined := strings.Join(s.buildArgs("do it"), " ")
	for _, want := range []string{"-p do it", "-o json", "--approval-mode plan", "-m gemini-2.5-pro"} {
		if !strings.Contains(joined, want) {
			t.Errorf("read-only args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "yolo") {
		t.Error("read-only worker should not use yolo mode")
	}
	// Writable → yolo mode; no model when unset.
	w := &geminiSession{opts: Options{ReadOnly: false}, bin: "gemini"}
	wj := strings.Join(w.buildArgs("x"), " ")
	if !strings.Contains(wj, "--approval-mode yolo") {
		t.Errorf("writable worker should use yolo mode: %s", wj)
	}
	if strings.Contains(wj, "plan") || strings.Contains(wj, "-m ") {
		t.Errorf("writable worker leaked plan or empty model: %s", wj)
	}
}

func TestParseGeminiJSON(t *testing.T) {
	// Success: a response field.
	r, ok := parseGeminiJSON([]byte(`{"response":"the answer","stats":{}}`))
	if !ok || r.Response != "the answer" || r.Error != nil {
		t.Fatalf("response parse wrong: %+v ok=%v", r, ok)
	}
	// Error envelope.
	r, ok = parseGeminiJSON([]byte(`{"error":{"type":"AuthError","message":"no token","code":401}}`))
	if !ok || r.Error == nil || r.Error.Message != "no token" {
		t.Fatalf("error parse wrong: %+v ok=%v", r, ok)
	}
	// Surrounded by log noise → line scan recovers it.
	r, ok = parseGeminiJSON([]byte("loading…\n{\"response\":\"x\"}\n"))
	if !ok || r.Response != "x" {
		t.Fatalf("noisy parse wrong: %+v ok=%v", r, ok)
	}
	// Non-JSON → not parsed (caller falls back to raw stdout).
	if _, ok := parseGeminiJSON([]byte("not json at all")); ok {
		t.Error("non-JSON should not parse")
	}
}

// ---- concurrency ------------------------------------------------------------

// blockingSession records the high-water mark of concurrent Prompt calls, so
// tests can prove same-worker dispatches serialize and different-worker ones run
// in parallel.
type blockingSession struct {
	active  *int32
	maxSeen *int32
	delay   time.Duration
}

func (b *blockingSession) Prompt(_ context.Context, task string, _ func(Event)) (Result, error) {
	n := atomic.AddInt32(b.active, 1)
	for { // track the max concurrency observed
		old := atomic.LoadInt32(b.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(b.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(b.delay)
	atomic.AddInt32(b.active, -1)
	return Result{Text: "answer to: " + task, OK: true}, nil
}
func (b *blockingSession) Close() error { return nil }

// TestDispatchSerializesSameWorker verifies concurrent dispatches to ONE worker
// never run its (single, stateful) session's Prompt at the same time.
func TestDispatchSerializesSameWorker(t *testing.T) {
	var active, maxSeen int32
	orig := open
	open = func(_ context.Context, _ Config, _ Options) (Session, error) {
		return &blockingSession{active: &active, maxSeen: &maxSeen, delay: 20 * time.Millisecond}, nil
	}
	t.Cleanup(func() { open = orig })

	m := New(t.TempDir(), []Config{{Name: "w", Backend: BackendClaude}})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Dispatch(context.Background(), "w", "task", nil); err != nil {
				t.Errorf("dispatch: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("same-worker dispatches must serialize, but observed %d concurrent prompts", maxSeen)
	}
}

// TestDispatchParallelDifferentWorkers verifies dispatches to DISTINCT workers
// run concurrently (each worker has its own session and lock).
func TestDispatchParallelDifferentWorkers(t *testing.T) {
	var active, maxSeen int32
	orig := open
	open = func(_ context.Context, _ Config, _ Options) (Session, error) {
		return &blockingSession{active: &active, maxSeen: &maxSeen, delay: 40 * time.Millisecond}, nil
	}
	t.Cleanup(func() { open = orig })

	m := New(t.TempDir(), []Config{
		{Name: "w1", Backend: BackendClaude},
		{Name: "w2", Backend: BackendClaude},
		{Name: "w3", Backend: BackendClaude},
	})
	var wg sync.WaitGroup
	for _, name := range []string{"w1", "w2", "w3"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Dispatch(context.Background(), name, "task", nil); err != nil {
				t.Errorf("dispatch %s: %v", name, err)
			}
		}()
	}
	wg.Wait()
	if maxSeen < 2 {
		t.Fatalf("distinct workers should dispatch concurrently, but max observed concurrency was %d", maxSeen)
	}
}

func workerNames(cfgs []Config) map[string]Config {
	m := map[string]Config{}
	for _, c := range cfgs {
		m[c.Name] = c
	}
	return m
}

func TestAddRemoveWorker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()
	m := New(wd, nil)

	// Add (global scope) — present in memory, enabled, and persisted.
	path, err := m.Add("claude-fast", BackendClaude, "anthropic/claude-x", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("Add returned no path")
	}
	if c, ok := workerNames(m.All())["claude-fast"]; !ok || c.Backend != BackendClaude || c.Model != "anthropic/claude-x" || !c.Enabled() {
		t.Fatalf("in-memory worker wrong: %+v ok=%v", c, ok)
	}
	if _, ok := workerNames(mustLoad(t, wd))["claude-fast"]; !ok {
		t.Fatal("Add did not persist to workers.json")
	}

	// Duplicate name and bad backend are rejected.
	if _, err := m.Add("claude-fast", BackendClaude, "", "", true); err == nil {
		t.Error("adding a duplicate name should error")
	}
	if _, err := m.Add("nope", "gpt", "", "", true); err == nil {
		t.Error("an unsupported backend should error")
	}

	// gemini is a supported backend.
	if _, err := m.Add("gem", BackendGemini, "", "", true); err != nil {
		t.Fatalf("gemini should be accepted: %v", err)
	}
	if c, ok := workerNames(m.All())["gem"]; !ok || c.Backend != BackendGemini {
		t.Fatalf("gemini worker wrong: %+v ok=%v", c, ok)
	}

	// Remove — gone from memory and from the file.
	if err := m.Remove("claude-fast"); err != nil {
		t.Fatal(err)
	}
	if _, ok := workerNames(m.All())["claude-fast"]; ok {
		t.Error("worker still in memory after Remove")
	}
	if _, ok := workerNames(mustLoad(t, wd))["claude-fast"]; ok {
		t.Error("worker still in workers.json after Remove")
	}
	if err := m.Remove("claude-fast"); err == nil {
		t.Error("removing an unknown worker should error")
	}
}

func mustLoad(t *testing.T, wd string) []Config {
	t.Helper()
	cfgs, err := Load(wd)
	if err != nil {
		t.Fatal(err)
	}
	return cfgs
}
