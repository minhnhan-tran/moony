package web

// The worker "install engine" endpoint: when a worker's backend CLI isn't on
// PATH, run the vetted install one-liner on THIS machine (the terminal the web
// UI is attached to, directly or via the relay tunnel) so the engine becomes
// usable without leaving the browser. Mirrors the LSP marketplace installer.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/pysty1995/moony/internal/worker"
)

// workerInstallRun runs an install one-liner through a login shell and returns
// its combined output. Indirected so tests can stub it (the real one runs
// curl|bash / npm against Apple-internal endpoints).
var workerInstallRun = func(ctx context.Context, command string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-lc", command).CombinedOutput()
	return string(out), err
}

// handleWorkerInstall installs a worker's engine CLI on this machine: if it's
// already on PATH it just re-probes readiness; otherwise it runs the backend's
// vetted install one-liner in the background (progress + result stream to the
// session transcript) and re-probes on success so the row flips to "ready".
// Only canned, backend-specific commands run — the client supplies just a
// worker name, never a command.
func (s *Server) handleWorkerInstall(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	mgr := sess.agent.Workers()
	if mgr == nil {
		http.Error(w, "workers are unavailable in this session", http.StatusBadRequest)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	engine, command, ok := mgr.InstallPlanFor(body.Name)
	if !ok {
		http.Error(w, "no installer for that worker", http.StatusBadRequest)
		return
	}

	// Already on PATH: nothing to install — just refresh so the badge is current.
	if worker.Installed(engine) {
		mgr.RefreshReadiness()
		writeJSON(w, map[string]any{"installed": true})
		return
	}

	// Background install (curl|bash / npm can be slow); avoid a long-held request.
	sess.publishEvent(wireEvent{Type: "info", Text: fmt.Sprintf("Installing %s — running: %s …", engine, command)})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		out, err := workerInstallRun(ctx, command)
		if err != nil {
			sess.publishEvent(wireEvent{Type: "error", Text: fmt.Sprintf("Install of %s failed: %v\n%s", engine, err, lastLines(out, 8))})
			return
		}
		mgr.RefreshReadiness() // bust the PATH cache + re-probe so "missing" can flip
		if !worker.Installed(engine) {
			sess.publishEvent(wireEvent{Type: "error", Text: fmt.Sprintf("Ran the installer but %q still isn't on PATH — you may need to restart the terminal or fix PATH.", engine)})
			return
		}
		sess.publishEvent(wireEvent{Type: "info", Text: fmt.Sprintf("Installed %s — the worker engine is ready.", engine)})
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"installing": true})
}
