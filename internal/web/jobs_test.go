package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// jobsFixture creates a real session and returns the server plus that session's id.
func jobsFixture(t *testing.T, policy string) (*Server, string) {
	t.Helper()
	srv, key := newTestServer(t, &fakeStreamer{}, policy, false)
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}
	if created.ID == "" {
		t.Fatal("no session id returned")
	}
	return srv, created.ID
}

func doJSON(t *testing.T, srv *Server, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestJobsListEmpty(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	rec, out := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	jobs, ok := out["jobs"].([]any)
	if !ok {
		t.Fatalf("no jobs array in %s", rec.Body.String())
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want 0", len(jobs))
	}
	// The policy tells the UI whether starting a job will prompt; without it the panel
	// can't warn the user in advance.
	if out["policy"] != config.ApprovalAuto {
		t.Errorf("policy = %v, want %q", out["policy"], config.ApprovalAuto)
	}
}

// The start route executes a command, so it must reject anything but POST — a GET would
// otherwise be triggerable by a link or an <img> tag.
func TestJobRoutesRejectWrongMethod(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/sessions/" + id + "/jobs/1/kill"},
		{"PUT", "/api/sessions/" + id + "/jobs/1/kill"},
		{"DELETE", "/api/sessions/" + id + "/jobs"},
	} {
		rec, _ := doJSON(t, srv, tc.method, tc.path, "")
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s returned 200; a mutating route must not accept it", tc.method, tc.path)
		}
	}
}

func TestJobStartValidation(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	t.Run("missing command", func(t *testing.T) {
		rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":""}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("destructive commands are refused outright", func(t *testing.T) {
		// Under `auto` nothing prompts, so if the destructive gate were missing this
		// would actually run `rm -rf /`.
		rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"rm -rf /"}`)
		if rec.Code == http.StatusOK {
			t.Fatal("a destructive command must never start from the browser")
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "destructive") {
			t.Errorf("expected a destructive refusal, got %q", rec.Body.String())
		}
	})
}

func TestJobTailUnknownJob(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	rec, _ := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs/nope/tail", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestJobKillUnknownJob(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/nope/kill", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// A bad cursor must resynchronize the client, not blank the panel with a 400.
func TestJobTailToleratesBadCursor(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	// Start a job so there is something to tail.
	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"echo hi"}`)
	if rec.Code != http.StatusOK {
		t.Skipf("could not start a job (status %d): %s", rec.Code, rec.Body.String())
	}
	jobID, _ := out["id"].(string)
	if jobID == "" {
		t.Skip("no job id returned")
	}
	for _, cursor := range []string{"abc", "-5", "999999", ""} {
		rec, _ := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs/"+jobID+"/tail?cursor="+cursor, "")
		if rec.Code != http.StatusOK {
			t.Errorf("cursor=%q: status = %d, want 200", cursor, rec.Code)
		}
	}
}

// Unknown sessions must 404 rather than panic on a nil agent.
func TestJobRoutesUnknownSession(t *testing.T) {
	srv, _ := jobsFixture(t, config.ApprovalAuto)
	for _, path := range []string{
		"/api/sessions/does-not-exist/jobs",
		"/api/sessions/does-not-exist/jobs/1/tail",
	} {
		rec, _ := doJSON(t, srv, "GET", path, "")
		if rec.Code == http.StatusOK {
			t.Errorf("%s returned 200 for an unknown session", path)
		}
	}
}

// Structural guard: the read paths must go through the VIEW api. If a future edit
// switches them to the model's consuming read, an open panel would silently swallow
// output the model never sees — a correctness bug with no visible symptom in the UI.
func TestJobReadsUseNonConsumingView(t *testing.T) {
	src := readWebSource(t, "jobs.go")
	if !strings.Contains(src, "JobTail(") {
		t.Error("the tail handler must use JobTail (absolute-offset, non-consuming)")
	}
	// Check for CALLS, not mentions: the file's comments legitimately explain why
	// bash_output's cursor must not be touched.
	for _, forbidden := range []string{".snapshot()", ".Snapshot()", ".Read(", "advanceRead"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("jobs.go must not call %s: that consumes the model's output", forbidden)
		}
	}
	// The start path must delegate to the gated agent method, never to the registry.
	if !strings.Contains(src, "StartBackgroundJobApproved(") {
		t.Error("the start handler must use StartBackgroundJobApproved (approval-gated)")
	}
	if strings.Contains(src, "Registry().StartBackgroundJob(") {
		t.Error("the web layer must not start jobs directly: that bypasses every approval gate")
	}
	// The concurrency claim must be held for the whole prompt.
	if !strings.Contains(src, "claimForJobPrompt()") {
		t.Error("the start handler must claim the approval channel before prompting")
	}
	if !strings.Contains(src, "defer sess.releaseJobPrompt()") {
		t.Error("the claim must be released via defer, or a failed start wedges the session")
	}
}

// End-to-end through the real handler chain: start a job, poll until it finishes, and
// tail its output incrementally. This is the flow the panel drives.
func TestJobLifecycleEndToEnd(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs",
		`{"command":"printf 'alpha\\n'; printf 'beta\\n'"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: status %d body %s", rec.Code, rec.Body.String())
	}
	jobID, _ := out["id"].(string)
	if jobID == "" {
		t.Fatalf("no job id in %s", rec.Body.String())
	}

	// Poll for completion, accumulating output the way the UI does.
	var log strings.Builder
	cursor := 0
	finished := false
	for i := 0; i < 100; i++ {
		rec, tail := doJSON(t, srv, "GET",
			"/api/sessions/"+id+"/jobs/"+jobID+"/tail?cursor="+strconv.Itoa(cursor), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("tail: status %d", rec.Code)
		}
		if s, _ := tail["output"].(string); s != "" {
			log.WriteString(s)
		}
		if c, ok := tail["cursor"].(float64); ok {
			cursor = int(c)
		}
		if running, _ := tail["running"].(bool); !running {
			finished = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !finished {
		t.Fatal("job never finished")
	}
	got := log.String()
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q is missing %q", got, want)
		}
	}
	// Output must not be duplicated: each poll advances the cursor, so a line must
	// appear exactly once even across many polls.
	if n := strings.Count(got, "alpha"); n != 1 {
		t.Errorf("%q appeared %d times, want exactly 1 — the cursor is not advancing", "alpha", n)
	}

	// The list view must now show it as finished.
	_, list := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs", "")
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	j, _ := jobs[0].(map[string]any)
	if running, _ := j["running"].(bool); running {
		t.Error("the finished job still reports running")
	}
	if state, _ := j["state"].(string); state != "done" {
		t.Errorf("state = %q, want done", state)
	}
}

// A UI tail must not consume what the model reads. This is the invariant at the HTTP
// layer: after the panel has tailed everything, bash_output must still return it.
func TestJobTailDoesNotStarveBashOutput(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	_, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"printf 'payload\\n'"}`)
	jobID, _ := out["id"].(string)
	if jobID == "" {
		t.Skip("could not start a job")
	}

	// Let it finish, and have the "panel" read the whole log.
	var seen string
	for i := 0; i < 100; i++ {
		_, tail := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs/"+jobID+"/tail?cursor=0", "")
		seen, _ = tail["output"].(string)
		if running, _ := tail["running"].(bool); !running && strings.Contains(seen, "payload") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(seen, "payload") {
		t.Fatalf("panel never saw the output, got %q", seen)
	}

	// Now the MODEL reads via the store's consuming path: it must still get the output.
	sess := srv.openSession(id)
	if sess == nil || sess.agent == nil {
		t.Fatal("session vanished")
	}
	store := sess.agent.Registry().Jobs()
	info, ok := store.JobTail(jobID, 0)
	if !ok {
		t.Fatal("job missing from the store")
	}
	if !strings.Contains(info.Output, "payload") {
		t.Error("the store no longer has the output — the UI tail consumed it")
	}
}

// Regression: a UI-started job that needed approval must not leave the session wedged.
//
// approve() sets stateBusy (it normally means "the turn is resuming"), so a release that
// only recognised stateJobPrompt/stateApproval left the session busy forever — blocking
// every later turn AND every later job, with no way back except a restart.
func TestJobPromptReleasesSessionFromEveryState(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	sess := srv.openSession(id)
	if sess == nil {
		t.Fatal("no session")
	}

	for _, start := range []string{stateJobPrompt, stateApproval, stateBusy} {
		sess.mu.Lock()
		sess.state = start
		sess.mu.Unlock()

		sess.releaseJobPrompt()

		sess.mu.Lock()
		got := sess.state
		sess.mu.Unlock()
		if got != stateIdle {
			t.Errorf("from %q: state = %q, want idle — the session is wedged", start, got)
		}
	}
}

// After a full start-with-approval cycle the session must accept another job.
func TestSessionUsableAfterApprovedJobStart(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	for i := 0; i < 2; i++ {
		rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"printf x"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("start %d: status %d body %s — the session did not return to idle", i+1, rec.Code, rec.Body.String())
		}
	}
}

// The keyboard route must exist, refuse unknown jobs, and refuse to be the thing that
// smuggles a destructive command past the start gate.
func TestJobInputRoute(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_nope/input", `{"data":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown job: status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no such background job") {
		t.Errorf("unhelpful error for an unknown job: %s", rec.Body.String())
	}

	if rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"sleep 5","tty":true}`); rec.Code != http.StatusOK {
		t.Fatalf("start: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Same classifier as a start: a job's stdin is an execution path too, and it is
	// applied even where the host gave the job pipes instead of a terminal.
	rec, _ = doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_1/input", `{"data":"rm -rf /"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "destructive") {
		t.Fatalf("destructive input: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Chunking must not get past it: the store assembles the line first. The opening
	// chunk is harmless on its own, so only the assembled line can explain a refusal.
	if rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_1/input", `{"data":"rm -","enter":false}`); rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "destructive") {
		t.Fatalf("harmless chunk refused: %s", rec.Body.String())
	}
	rec, _ = doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_1/input", `{"data":"rf /","enter":false}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "destructive") {
		t.Fatalf("split destructive input got through: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Oversized input is refused rather than streamed into the process.
	big := `{"data":"` + strings.Repeat("x", 5000) + `"}`
	if rec, _ := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_1/input", big); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized input: status = %d, want 400", rec.Code)
	}

	rec, _ = doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_1/input", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: status = %d, want 400", rec.Code)
	}

	rec, _ = doJSON(t, srv, "POST", "/api/sessions/nope/jobs/bg_1/input", `{"data":"hi"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: status = %d, want 404", rec.Code)
	}
}

// A UI-started TTY job has to actually run under a terminal, or the panel's keyboard
// and the unbuffered output it promises are fiction.
func TestJobStartTTYFlagReachesTheJob(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)
	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"sleep 5","tty":true}`)
	if rec.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("start: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	_, list := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs", "")
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1: %v", len(jobs), list)
	}
	j, _ := jobs[0].(map[string]any)
	if j["tty"] != true {
		t.Errorf("job not marked as a TTY job: %v", j)
	}
}

func TestJobDeleteRoute(t *testing.T) {
	srv, id := jobsFixture(t, config.ApprovalAuto)

	// Start a job
	rec, out := doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"printf hello"}`)
	if rec.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("start: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Unknown job deletion returns 404
	rec, _ = doJSON(t, srv, "DELETE", "/api/sessions/"+id+"/jobs/bg_nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job delete: status = %d, want 404", rec.Code)
	}

	// Delete existing job via DELETE
	rec, out = doJSON(t, srv, "DELETE", "/api/sessions/"+id+"/jobs/bg_1", "")
	if rec.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("delete job: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify job is no longer listed
	_, list := doJSON(t, srv, "GET", "/api/sessions/"+id+"/jobs", "")
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs after deletion, got %d", len(jobs))
	}

	// Also test POST /delete alias
	rec, _ = doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs", `{"command":"printf world"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start second job: status = %d", rec.Code)
	}
	rec, out = doJSON(t, srv, "POST", "/api/sessions/"+id+"/jobs/bg_2/delete", "")
	if rec.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("delete job via POST: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The jobs UI is spread across four static files that have to agree with each other and
// with the routes above; nothing else in the suite would notice a rename. Reading the
// EMBEDDED copies is deliberate — that's what the relay actually serves.
func TestJobsUIWiring(t *testing.T) {
	read := func(name string) string {
		b, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	for _, want := range []struct{ file, needle, why string }{
		{"index.html", `id="jobsBtn"`, "the jobs pill needs its button"},
		{"index.html", `id="jobsPill"`, "the button lives in its own floating pill, not inside #pager"},
		{"index.html", `id="jobsBtnCount"`, "the button needs its running-count badge"},
		{"app-state.js", `$("#jobsBtn")`, "the button must be bound like the other pager controls"},
		{"app-state.js", `$("#jobsPill")`, "the pill carries visibility, so it must be bound too"},
		{"app-core.js", `openConfigModal("jobs")`, "clicking it must open the jobs panel"},
		{"app-core.js", `function refreshJobsBadge`, "the badge needs its poll"},
		{"app-core.js", `data.jobs`, "the badge must read the {jobs,policy} envelope, not a bare array"},
		{"app-manage.js", `/input`, "the panel's keyboard posts to the input route"},
		{"app-manage.js", `j.tty`, "the keyboard is only offered for TTY jobs"},
		{"app.css", `.pager-jobs`, "the button needs its styles"},
		{"app.css", `.jobs-pill`, "the pill needs its own chrome now that it is not inside the pager"},
	} {
		if !strings.Contains(read(want.file), want.needle) {
			t.Errorf("%s is missing %q: %s", want.file, want.needle, want.why)
		}
	}
}
