package web

import (
	"os"
	"strings"
	"testing"
)

// A quota-fallback notice is produced on the background usage-poll goroutine, but the
// transcript belongs to the turn goroutine. The note must therefore be QUEUED
// (agent.NoteInfo) and only flushed while the session is idle, with the idle check held
// under the session lock — an isRunning()-style check followed by a flush is TOCTOU,
// since a turn can start in between.
//
// These are source assertions because the failure they guard against is a data race in
// a specific call path, which a unit test can't observe without a live turn.
func TestQuotaNoteIsQueuedNotWrittenDirectly(t *testing.T) {
	srv := readWebSource(t, "server.go")

	// recordQuotaNote must queue, not write.
	if !strings.Contains(srv, "live.agent.NoteInfo(note)") {
		t.Error("recordQuotaNote must queue the note via NoteInfo (it runs off the turn goroutine)")
	}
	// It must not reach into the unexported, turn-owned writer.
	if strings.Contains(srv, "live.agent.noteInfo(") {
		t.Error("recordQuotaNote must not call noteInfo directly from the poll goroutine")
	}
	// The flush must go through the lock-holding helper, not a bare idle check.
	if !strings.Contains(srv, "live.flushNotesIfIdle()") {
		t.Error("recordQuotaNote must flush via flushNotesIfIdle (idle check under the session lock)")
	}
	if strings.Contains(srv, "!s.isRunning(sess.ID) {\n\t\tlive.agent.FlushNotes()") {
		t.Error("flushing after a bare isRunning() check is TOCTOU: a turn can start in between")
	}
}

func TestFlushNotesIfIdleChecksStateUnderLock(t *testing.T) {
	src := readWebSource(t, "session.go")
	i := strings.Index(src, "func (s *session) flushNotesIfIdle()")
	if i < 0 {
		t.Fatal("flushNotesIfIdle not found")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j]
	}
	lock := strings.Index(body, "s.mu.Lock()")
	check := strings.Index(body, "s.state != stateIdle")
	flush := strings.Index(body, "s.agent.FlushNotes()")
	if lock < 0 || check < 0 || flush < 0 {
		t.Fatalf("expected lock/idle-check/flush in flushNotesIfIdle, got:\n%s", body)
	}
	// The lock must be taken BEFORE the check, and the flush must happen while still
	// holding it (i.e. after the check, with no intervening Unlock).
	if !(lock < check && check < flush) {
		t.Error("flushNotesIfIdle must lock, then check idle, then flush while still holding the lock")
	}
	// The unlock must not happen before the flush. `defer s.mu.Unlock()` textually
	// precedes it but runs at return, so only a NON-deferred early unlock is a bug.
	for _, line := range strings.Split(body[:flush], "\n") {
		l := strings.TrimSpace(line)
		if l == "s.mu.Unlock()" {
			t.Error("a non-deferred Unlock before the flush reopens the TOCTOU window")
		}
	}
	if !strings.Contains(body, "defer s.mu.Unlock()") {
		t.Error("expected the lock to be released via defer, after the flush")
	}
}

func readWebSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// status.provider is the wire FAMILY (ProviderLabel(cfg.Provider)), so an SQE Anthropic
// session reports itself as "Anthropic". The quota popover names the provider to the
// user, so it must read llmProvider — otherwise it blames the wrong backend AND claims it
// has no usage endpoint, when SQE Anthropic is the one provider that does report quota.
func TestQuotaInfoRowsUsesLLMProvider(t *testing.T) {
	js := mustReadStatic(t, "app-core.js")
	start := strings.Index(js, "function quotaInfoRows")
	if start < 0 {
		t.Fatal("quotaInfoRows not found in app-core.js")
	}
	end := strings.Index(js[start:], "\nfunction ")
	if end < 0 {
		end = len(js) - start
	}
	body := js[start : start+end]

	if !strings.Contains(body, "s.llmProvider") {
		t.Error("quotaInfoRows must name the provider via s.llmProvider")
	}
	// s.provider would be the wire family. Allow the word inside comments, but not as a
	// property read on the status object.
	for _, bad := range []string{"providerLabel(s.provider)", "s.provider ?", "s.provider)"} {
		if strings.Contains(body, bad) {
			t.Errorf("quotaInfoRows reads the wire-family label (%q); use s.llmProvider", bad)
		}
	}
}
