package web

// The LSP "marketplace" endpoints powering the LSP user-menu panel: list the
// catalog + configured servers with live state, install a catalog server
// (registering it in lsp.json and, when the binary is missing, running the
// vetted installer in the background), toggle enable/disable, and remove.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
)

// lspCatalogItem is one row in the marketplace panel: a curated catalog entry or
// a custom (already-configured) server, with its install/enable/run state.
type lspCatalogItem struct {
	Name       string   `json:"name"`
	Language   string   `json:"language"`
	Desc       string   `json:"desc,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Install    string   `json:"install,omitempty"` // human-readable install command
	Installed  bool     `json:"installed"`         // present in lsp.json
	Enabled    bool     `json:"enabled"`           // manage enable state
	Running    bool     `json:"running"`           // currently handshaked
	OnPath     bool     `json:"onPath"`            // binary resolvable on PATH
	CanInstall bool     `json:"canInstall"`        // has a server-side installer
	Custom     bool     `json:"custom,omitempty"`  // configured but not in the catalog
}

// handleLspCatalog returns the marketplace: every catalog server plus any
// configured-but-not-cataloged ("custom") server, with live state.
func (s *Server) handleLspCatalog(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mgmt := manage.Load(wd)
	cfgs, _ := lsp.LoadConfigs(wd)
	configured := map[string][]string{} // name -> extensions
	for _, c := range cfgs {
		configured[c.Name] = c.Extensions
	}
	running := map[string]bool{}
	// Running state comes from the session's agent; the live mirror has no web
	// agent, so it shows installed/enabled without live running state.
	if sess != nil && sess.agent != nil {
		for _, n := range sess.agent.LSP().Running() {
			running[n] = true
		}
	}

	out := []lspCatalogItem{}
	seen := map[string]bool{}
	for _, e := range lsp.Catalog() {
		seen[e.Name] = true
		_, installed := configured[e.Name]
		out = append(out, lspCatalogItem{
			Name: e.Name, Language: e.Language, Desc: e.Desc, Extensions: e.Extensions, Install: e.Install,
			Installed: installed, Enabled: mgmt.LSPEnabled(e.Name), Running: running[e.Name],
			OnPath: lsp.BinaryInstalled(e.Command), CanInstall: len(e.InstallArgv()) > 0,
		})
	}
	for _, c := range cfgs {
		if seen[c.Name] {
			continue
		}
		out = append(out, lspCatalogItem{
			Name: c.Name, Language: "custom", Desc: c.Command, Extensions: c.Extensions,
			Installed: true, Enabled: mgmt.LSPEnabled(c.Name), Running: running[c.Name],
			OnPath: lsp.BinaryInstalled(c.Command), Custom: true,
		})
	}
	writeJSON(w, out)
}

// lspInstallRun runs an install argv and returns its combined output. Indirected
// so tests can stub it (the real one runs package managers like npm/go/brew).
var lspInstallRun = func(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("no install command")
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// handleLspInstall installs a catalog server: if its binary is already on PATH
// it just registers it (global lsp.json) and reloads; otherwise it runs the
// vetted installer in the background (progress + result go to the session
// transcript) and registers on success. Only catalog argvs are ever executed.
func (s *Server) handleLspInstall(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	entry, ok := lsp.CatalogEntryByName(body.Name)
	if !ok {
		http.Error(w, "unknown language server", http.StatusBadRequest)
		return
	}
	wd := sess.agent.Config().WorkingDir
	register := func() error {
		if _, err := lsp.AddServer(entry.Name, entry.Command, entry.Args, entry.Extensions, true, wd); err != nil {
			return err
		}
		s.connectLSP(wd, sess) // reload so the newly-registered server starts
		return nil
	}

	// Binary already present: register synchronously and we're done.
	if lsp.BinaryInstalled(entry.Command) {
		if err := register(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"installed": true})
		return
	}
	if len(entry.InstallArgv()) == 0 {
		http.Error(w, fmt.Sprintf("no installer for %s — install %q manually, then it'll be detected", entry.Name, entry.Command), http.StatusBadRequest)
		return
	}

	// Background install (package managers can be slow); avoid a long-held request.
	sess.publishEvent(wireEvent{Type: "info", Text: fmt.Sprintf("Installing %s — running: %s …", entry.Name, entry.Install)})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := lspInstallRun(ctx, entry.InstallArgv())
		if err != nil {
			sess.publishEvent(wireEvent{Type: "error", Text: fmt.Sprintf("Install of %s failed: %v\n%s", entry.Name, err, lastLines(out, 8))})
			return
		}
		if !lsp.BinaryInstalled(entry.Command) {
			sess.publishEvent(wireEvent{Type: "error", Text: fmt.Sprintf("Ran the installer for %s but %q still isn't on PATH — you may need to restart the terminal or fix PATH.", entry.Name, entry.Command)})
			return
		}
		if err := register(); err != nil {
			sess.publishEvent(wireEvent{Type: "error", Text: "Installed " + entry.Name + " but couldn't register it: " + err.Error()})
			return
		}
		sess.publishEvent(wireEvent{Type: "info", Text: fmt.Sprintf("Installed %s — language server is ready.", entry.Name)})
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"installing": true})
}

// handleLspToggle enables/disables a server (global scope, matching install) and
// reloads so the change takes effect immediately.
func (s *Server) handleLspToggle(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	wd := sess.agent.Config().WorkingDir
	if err := manage.Load(wd).SetLSP(body.Name, body.Enabled, manage.Global); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.connectLSP(wd, sess)
	writeJSON(w, map[string]any{"ok": true})
}

// handleLspRemove unregisters a server from lsp.json and reloads.
func (s *Server) handleLspRemove(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wd := sess.agent.Config().WorkingDir
	if _, err := lsp.RemoveServer(body.Name, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.connectLSP(wd, sess)
	writeJSON(w, map[string]any{"ok": true})
}

// lastLines returns the last n non-empty lines of s, for compact error tails.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
