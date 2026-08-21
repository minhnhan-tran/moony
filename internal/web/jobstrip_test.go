package web

import (
	"regexp"
	"strings"
	"testing"
)

// The background-jobs strip is plain DOM built in app-core.js, so these read the shipped
// asset. They exist because two things about it are easy to break silently and both look
// like the same user-visible complaint ("there's no stop button"):
//
//   - the per-row Stop control, which is the ONLY way to kill a job from the strip, and
//   - the folding in stripRows, which must never hide a RUNNING job (a folded-away job
//     cannot be stopped at all).
func TestJobsStripRowStopsRunningJobs(t *testing.T) {
	src := readAsset(t, "static/app-core.js")

	row := jobstripFunc(t, src, "stripRow")
	if !strings.Contains(row, `j.running`) {
		t.Fatal("stripRow no longer branches on j.running — the Stop/Restart split is gone")
	}
	if !strings.Contains(row, `stripIconBtn("stop.fill"`) {
		t.Error("stripRow does not build a stop.fill button: a running job cannot be stopped from the strip")
	}
	if !strings.Contains(row, "/kill") {
		t.Error("stripRow's stop button is not wired to the jobs kill endpoint")
	}
	// The kill route the button posts to has to exist on the server.
	srvGo, err := readSource("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	if !strings.Contains(srvGo, "POST /api/sessions/{id}/jobs/{job}/kill") {
		t.Error("the jobs kill route is gone from the web server — the strip's stop button would 404")
	}
}

// One row per background job: a command's finished runs collapse into its newest run, so
// restarting a dev server three times leaves one row rather than three, but every running
// run keeps a row of its own.
func TestJobsStripFoldsFinishedRunsButNeverRunningOnes(t *testing.T) {
	src := readAsset(t, "static/app-core.js")

	rows := jobstripFunc(t, src, "stripRows")
	for _, want := range []string{"j.running ? g.running : g.finished", "g.running.length", "g.finished.length"} {
		if !strings.Contains(rows, want) {
			t.Errorf("stripRows no longer contains %q — the running/finished split is the thing keeping a live job visible", want)
		}
	}
	// Every running run must be pushed as its own row.
	if !strings.Contains(rows, "g.running.forEach") {
		t.Error("stripRows does not emit a row per running run — a running job could be folded away and become unstoppable")
	}

	render := jobstripFunc(t, src, "renderJobsStrip")
	if !strings.Contains(render, "stripRows(list)") {
		t.Fatal("renderJobsStrip does not render from stripRows — the strip is back to one row per run")
	}
	if !strings.Contains(render, "stripRow(r.job, r.folded)") {
		t.Error("renderJobsStrip does not pass the folded count to stripRow — the \"+N\" hint is lost")
	}
	if strings.Contains(render, "for (const j of list) jobsStrip.appendChild(stripRow(j))") {
		t.Error("renderJobsStrip still loops the raw job list")
	}
	// The header must count rows, not runs, or it disagrees with what is on screen.
	if !strings.Contains(render, "${rows.length}") {
		t.Error("the strip header no longer counts the rows it shows")
	}
}

// The runtime clock used to write cell[i] from stripJobs[i]. Folding breaks that
// correspondence, so the cells carry their job id and are looked up by it.
func TestJobsStripClockIsKeyedByJobID(t *testing.T) {
	src := readAsset(t, "static/app-core.js")

	if !strings.Contains(jobstripFunc(t, src, "stripRow"), "rt.dataset.job = j.id") {
		t.Error("the runtime cell does not carry its job id")
	}
	clock := jobstripFunc(t, src, "syncStripClock")
	if !strings.Contains(clock, "cell.dataset.job") {
		t.Error("syncStripClock does not look the job up by id — it can print one job's runtime on another's row")
	}
	if regexp.MustCompile(`cells\[i\]`).MatchString(clock) {
		t.Error("syncStripClock still indexes time cells positionally")
	}
}

// The strip is pinned between the status bar and the transcript, so its rows must be capped
// and scrollable: a machine running several dev servers / watch scripts would otherwise grow
// it until it pushes the transcript (and on a short window the composer) off screen. The
// header must stay OUTSIDE the scroll box so the "N running" count doesn't scroll away.
func TestJobsStripRowsAreCappedAndScrollable(t *testing.T) {
	css := readAsset(t, "static/app.css")
	rule := cssRule(t, css, ".jobstrip-list")
	if !strings.Contains(rule, "max-height:") {
		t.Error(".jobstrip-list has no max-height — the strip can grow without bound")
	}
	if !strings.Contains(rule, "overflow-y: auto") {
		t.Error(".jobstrip-list does not scroll — capping it without a scrollbar hides jobs entirely")
	}

	src := readAsset(t, "static/app-core.js")
	render := jobstripFunc(t, src, "renderJobsStrip")
	if !strings.Contains(render, `el("div", "jobstrip-list")`) {
		t.Fatal("renderJobsStrip does not build the .jobstrip-list scroll box — the max-height rule applies to nothing")
	}
	if !strings.Contains(render, "listEl.appendChild(stripRow(") {
		t.Error("rows are not appended to the scroll box, so they are not capped")
	}
	if strings.Contains(render, "jobsStrip.appendChild(stripRow(") {
		t.Error("renderJobsStrip still appends rows straight to #jobsStrip, bypassing the cap")
	}
	if !strings.Contains(render, "jobsStrip.appendChild(head)") {
		t.Error("the head must stay a direct child of #jobsStrip so the job count doesn't scroll away")
	}
}

// cssRule returns the declaration block of a selector, so a test asserts against one rule
// rather than the whole stylesheet.
func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	css = strings.ReplaceAll(css, "\r\n", "\n")
	start := strings.Index(css, "\n"+selector+" {")
	if start < 0 {
		t.Fatalf("CSS rule %s not found in app.css", selector)
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatalf("unterminated CSS rule %s", selector)
	}
	return css[start : start+end]
}

// jobstripFunc returns the source of a top-level function declaration, so a test asserts
// against one function instead of the whole 5k-line asset.
func jobstripFunc(t *testing.T, src, name string) string {
	t.Helper()
	// The working tree's line endings depend on core.autocrlf, so normalise before
	// looking for a function body rather than trusting how git checked the file out.
	src = strings.ReplaceAll(src, "\r\n", "\n")
	start := strings.Index(src, "\nfunction "+name+"(")
	if start < 0 {
		t.Fatalf("function %s() not found in app-core.js", name)
	}
	end := strings.Index(src[start+1:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s()", name)
	}
	return src[start : start+1+end]
}

func TestJobsStripRuntimeFreezesForFinishedAndKilledJobs(t *testing.T) {
	src := readAsset(t, "static/app-core.js")

	clk := jobstripFunc(t, src, "syncStripClock")
	if !strings.Contains(clk, "j.running") {
		t.Error("syncStripClock must check j.running before updating a time cell so finished/killed jobs do not count up")
	}

	rt := jobstripFunc(t, src, "stripRuntime")
	if !strings.Contains(rt, "j.ended") {
		t.Error("stripRuntime must use j.ended when available for non-running jobs")
	}
}
