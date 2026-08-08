package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/awsdeploy"
)

// fakeDeployer stands in for the real thing: these tests must not touch AWS.
type fakeDeployer struct {
	opts     awsdeploy.Options
	log      *deployJob
	block    chan struct{} // when set, Deploy waits on it
	failWith error
	status   awsdeploy.Status
	seen     *awsdeploy.Options
}

func (f *fakeDeployer) Deploy(ctx context.Context) (awsdeploy.Status, error) {
	if f.seen != nil {
		*f.seen = f.opts
	}
	if f.log != nil {
		f.log.Write([]byte("deploying " + f.opts.Domain + "\n"))
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return f.status, ctx.Err()
		}
	}
	return f.status, f.failWith
}

func (f *fakeDeployer) Delete(ctx context.Context) error {
	if f.log != nil {
		f.log.Write([]byte("deleting\n"))
	}
	return f.failWith
}

func (f *fakeDeployer) Status(ctx context.Context) (awsdeploy.Status, error) {
	return f.status, f.failWith
}

func newDeployServer(t *testing.T, f *fakeDeployer) *Server {
	t.Helper()
	s := &Server{}
	s.deployFn = func(o awsdeploy.Options, log *deployJob) deployTarget {
		f.opts, f.log = o, log
		return f
	}
	return s
}

func TestDeployStatusReportsTheStack(t *testing.T) {
	f := &fakeDeployer{status: awsdeploy.Status{
		Stack: "moony-relay", StackStatus: "UPDATE_COMPLETE", Region: "ap-southeast-1",
		Outputs: map[string]string{"WebUrl": "https://relay.example.com"},
	}}
	s := newDeployServer(t, f)

	rec := httptest.NewRecorder()
	s.handleDeployStatus(rec, httptest.NewRequest("GET", "/api/deploy/aws", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["running"] != false {
		t.Errorf("running = %v, want false", got["running"])
	}
	// The CLI may genuinely be absent on a CI box; the stack is only read when it is
	// present, so only assert the shape in that case.
	if got["cli"] == true {
		st, _ := got["status"].(map[string]any)
		if st == nil || st["stackStatus"] != "UPDATE_COMPLETE" {
			t.Errorf("status not reported: %v", got["status"])
		}
	}
}

func TestDeployRunRejectsUnknownAction(t *testing.T) {
	s := newDeployServer(t, &fakeDeployer{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/deploy/aws", strings.NewReader(`{"action":"rm -rf"}`))
	s.handleDeployRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// One deploy at a time: two concurrent runs would fight over the same change set.
func TestDeployRunIsSingleFlight(t *testing.T) {
	block := make(chan struct{})
	f := &fakeDeployer{block: block}
	s := newDeployServer(t, f)
	// Bypass the LookPath check by driving the runner directly, which is what the
	// handler does once past its guards.
	if !s.deploy.start("deploy", func() {}) {
		t.Fatal("could not claim an idle runner")
	}
	rec := httptest.NewRecorder()
	s.handleDeployRun(rec, httptest.NewRequest("POST", "/api/deploy/aws", strings.NewReader(`{"action":"deploy"}`)))
	if rec.Code != http.StatusConflict && rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 409 (or 412 when the AWS CLI is absent)", rec.Code)
	}
	close(block)
	s.deploy.finish(nil)
}

// The log endpoint is incremental: the panel polls with the offset it last saw.
func TestDeployLogIsIncremental(t *testing.T) {
	s := newDeployServer(t, &fakeDeployer{})
	s.deploy.start("deploy", func() {})
	s.deploy.Write([]byte("first\n"))

	read := func(offset string) (string, int, bool) {
		rec := httptest.NewRecorder()
		s.handleDeployLog(rec, httptest.NewRequest("GET", "/api/deploy/aws/log?offset="+offset, nil))
		var got struct {
			Data    string `json:"data"`
			Offset  int    `json:"offset"`
			Running bool   `json:"running"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Data, got.Offset, got.Running
	}

	data, off, running := read("0")
	if !strings.Contains(data, "first") || !running {
		t.Fatalf("first read: data=%q running=%v", data, running)
	}
	if data2, _, _ := read(itoa(off)); data2 != "" {
		t.Fatalf("nothing new should be returned at the head, got %q", data2)
	}
	s.deploy.Write([]byte("second\n"))
	data3, _, _ := read(itoa(off))
	if data3 != "second\n" {
		t.Fatalf("incremental read = %q, want just the new line", data3)
	}
	// A nonsense offset must not panic or slice out of range.
	if _, _, _ = read("99999"); false {
		t.Fatal("unreachable")
	}
	if _, _, _ = read("-5"); false {
		t.Fatal("unreachable")
	}

	s.deploy.finish(errors.New("boom"))
	data4, _, running4 := read("0")
	if running4 || !strings.Contains(data4, "FAILED: boom") {
		t.Fatalf("after failure: running=%v data=%q", running4, data4)
	}
}

func TestDeployCancelWithNothingRunning(t *testing.T) {
	s := newDeployServer(t, &fakeDeployer{})
	rec := httptest.NewRecorder()
	s.handleDeployCancel(rec, httptest.NewRequest("POST", "/api/deploy/aws/cancel", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestDeployJobCapsItsLog(t *testing.T) {
	var j deployJob
	j.start("deploy", func() {})
	chunk := strings.Repeat("x", 64<<10)
	for i := 0; i < 8; i++ {
		j.Write([]byte(chunk))
	}
	data, _, _, _, _, _ := j.snapshot(0)
	if len(data) > maxDeployLog+len("... earlier output dropped ...\n")+1 {
		t.Fatalf("log grew to %d bytes; the cap is %d", len(data), maxDeployLog)
	}
	if !strings.Contains(data, "earlier output dropped") {
		t.Error("the trim marker is missing, so a reader cannot tell output was dropped")
	}
}

// The panel's fields must reach the deployer; a typo here silently deploys defaults.
func TestDeployRequestMapsToOptions(t *testing.T) {
	req := deployRequest{
		Action: "deploy", Stack: " s ", Region: " r ", Domain: " d ", Email: " e ",
		EnvParameter: " /p ", WebCIDR: " 1.2.3.4/32 ", TunnelCIDR: " 5.6.7.8/32 ",
		InstanceType: " t4g.medium ", WithDownloads: true,
	}
	got := req.options()
	want := awsdeploy.Options{
		Stack: "s", Region: "r", Domain: "d", Email: "e", EnvParameter: "/p",
		WebCIDR: "1.2.3.4/32", TunnelCIDR: "5.6.7.8/32", InstanceType: "t4g.medium",
		WithDownloads: true,
	}
	if got != want {
		t.Fatalf("options = %+v\nwant     %+v", got, want)
	}
}

// The UI has to actually reference the endpoints and be registered as a tab, or the
// panel is dead code.
func TestDeployUIWiring(t *testing.T) {
	core := staticDeployFile(t, "app-core.js")
	manage := staticDeployFile(t, "app-manage.js")
	for _, needle := range []string{`key: "deploy"`, "renderDeployPanel()", "stopDeployPolling()"} {
		if !strings.Contains(core, needle) {
			t.Errorf("app-core.js is missing %q", needle)
		}
	}
	for _, needle := range []string{
		"/api/deploy/aws", "/api/deploy/aws/log?offset=", "/api/deploy/aws/cancel",
		"async function renderDeployPanel", "function stopDeployPolling",
	} {
		if !strings.Contains(manage, needle) {
			t.Errorf("app-manage.js is missing %q", needle)
		}
	}
	// The no-auth deploy must stay behind a confirmation.
	if !strings.Contains(manage, "NO authentication") {
		t.Error("the panel no longer warns about deploying without authentication")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// staticDeployFile reads an embedded UI file so the wiring test fails when the panel
// and its endpoints drift apart.
func staticDeployFile(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
