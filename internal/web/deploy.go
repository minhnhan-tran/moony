package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/awsdeploy"
)

// Deploy panel: put this connector's relay on AWS from the browser.
//
// No extra privilege gate. Reaching this API already means being inside a session that
// can run shell commands on this machine, so a deploy is not an escalation - and the
// AWS credentials involved are the ones sitting in this user's own CLI configuration.
// What it must not do is let two deploys of the same stack run at once, or block a
// request for the five minutes a deploy takes; hence the single-flight runner below.

// maxDeployLog caps the retained output. A deploy prints a few kilobytes; anything
// larger means something is wrong, and the browser polls this into memory.
const maxDeployLog = 256 << 10

// deployJob is the one in-flight deployment, if any.
type deployJob struct {
	mu      sync.Mutex
	running bool
	action  string
	started time.Time
	log     []byte
	failure string
	cancel  context.CancelFunc
}

func (j *deployJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, p...)
	if len(j.log) > maxDeployLog {
		// Keep the tail: the end of a failed deploy is the part that says why.
		j.log = append([]byte("... earlier output dropped ...\n"), j.log[len(j.log)-maxDeployLog/2:]...)
	}
	return len(p), nil
}

func (j *deployJob) snapshot(offset int) (data string, next int, running bool, action, failure string, started time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if offset < 0 || offset > len(j.log) {
		offset = 0
	}
	return string(j.log[offset:]), len(j.log), j.running, j.action, j.failure, j.started
}

// start claims the runner. Reports false when a deploy is already in flight.
func (j *deployJob) start(action string, cancel context.CancelFunc) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running, j.action, j.started, j.failure, j.cancel = true, action, time.Now(), "", cancel
	j.log = append(j.log[:0:0], []byte(fmt.Sprintf("=== %s at %s ===\n", action, j.started.Format(time.RFC3339)))...)
	return true
}

func (j *deployJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.cancel = false, nil
	if err != nil {
		j.failure = err.Error()
		j.log = append(j.log, []byte("\nFAILED: "+err.Error()+"\n")...)
	} else {
		j.log = append(j.log, []byte("\ndone\n")...)
	}
}

func (j *deployJob) stop() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running || j.cancel == nil {
		return false
	}
	j.cancel()
	return true
}

// deployRequest is the panel's form.
type deployRequest struct {
	Action        string `json:"action"` // deploy | delete
	Stack         string `json:"stack"`
	Region        string `json:"region"`
	Domain        string `json:"domain"`
	Email         string `json:"email"`
	EnvParameter  string `json:"envParameter"`
	WebCIDR       string `json:"webCidr"`
	TunnelCIDR    string `json:"tunnelCidr"`
	InstanceType  string `json:"instanceType"`
	WithDownloads bool   `json:"withDownloads"`
}

func (r deployRequest) options() awsdeploy.Options {
	return awsdeploy.Options{
		Stack:         strings.TrimSpace(r.Stack),
		Region:        strings.TrimSpace(r.Region),
		Domain:        strings.TrimSpace(r.Domain),
		Email:         strings.TrimSpace(r.Email),
		EnvParameter:  strings.TrimSpace(r.EnvParameter),
		WebCIDR:       strings.TrimSpace(r.WebCIDR),
		TunnelCIDR:    strings.TrimSpace(r.TunnelCIDR),
		InstanceType:  strings.TrimSpace(r.InstanceType),
		WithDownloads: r.WithDownloads,
	}
}

// handleDeployStatus reports whether a deploy is possible here, the stack's state, and
// whether one is running.
func (s *Server) handleDeployStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"cli": true}
	if _, err := exec.LookPath("aws"); err != nil {
		resp["cli"] = false
		resp["hint"] = "the AWS CLI is not installed on this machine; install it and run `aws login`"
	}

	data, next, running, action, failure, started := s.deploy.snapshot(0)
	resp["running"], resp["action"], resp["logOffset"] = running, action, next
	resp["log"], resp["failure"] = data, failure
	if !started.IsZero() {
		resp["started"] = started.Format(time.RFC3339)
	}

	if resp["cli"] == true && !running {
		// Bounded: this is three AWS calls and the panel waits on it.
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		st, err := s.deployer(awsdeploy.Options{Stack: r.URL.Query().Get("stack")}, nil).Status(ctx)
		if err != nil {
			resp["error"] = err.Error()
		}
		resp["status"] = st
	}
	writeJSON(w, resp)
}

// handleDeployRun starts a deploy or a delete and returns immediately: the work outlives
// the request, and the panel follows it through the log endpoint.
func (s *Server) handleDeployRun(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "deploy"
	}
	if action != "deploy" && action != "delete" {
		http.Error(w, "action must be deploy or delete", http.StatusBadRequest)
		return
	}
	if _, err := exec.LookPath("aws"); err != nil {
		http.Error(w, "the AWS CLI is not installed on this machine", http.StatusPreconditionFailed)
		return
	}

	// Detached from the request: a browser navigating away must not kill a half-created
	// stack. Bounded so a wedged AWS call cannot leak the runner forever.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	if !s.deploy.start(action, cancel) {
		cancel()
		http.Error(w, "a deploy is already running", http.StatusConflict)
		return
	}

	opts := req.options()
	go func() {
		defer cancel()
		d := s.deployer(opts, &s.deploy)
		var err error
		if action == "delete" {
			err = d.Delete(ctx)
		} else {
			_, err = d.Deploy(ctx)
		}
		s.deploy.finish(err)
	}()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"started": true, "action": action})
}

// handleDeployLog serves the running deploy's output from an offset, so the panel can
// poll cheaply instead of holding a stream open.
func (s *Server) handleDeployLog(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	data, next, running, action, failure, _ := s.deploy.snapshot(offset)
	writeJSON(w, map[string]any{
		"data": data, "offset": next, "running": running, "action": action, "failure": failure,
	})
}

// handleDeployCancel stops waiting on AWS. Whatever the stack is doing continues; this
// only abandons the client side, which is the honest thing to advertise.
func (s *Server) handleDeployCancel(w http.ResponseWriter, r *http.Request) {
	if !s.deploy.stop() {
		http.Error(w, "nothing is running", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"cancelled": true})
}

// deployer builds a Deployer, or the test double when one is installed.
func (s *Server) deployer(opts awsdeploy.Options, log *deployJob) deployTarget {
	if s.deployFn != nil {
		return s.deployFn(opts, log)
	}
	if log == nil {
		return awsdeploy.New(opts, nil)
	}
	return awsdeploy.New(opts, log)
}

// deployTarget is the slice of awsdeploy.Deployer the handlers use, so tests can stand
// in for it without touching AWS.
type deployTarget interface {
	Deploy(ctx context.Context) (awsdeploy.Status, error)
	Delete(ctx context.Context) error
	Status(ctx context.Context) (awsdeploy.Status, error)
}
