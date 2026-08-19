// Package web serves a browser UI for moony. It drives the same agent
// core as the terminal UI (via agent.Run + the Event stream) and broadcasts
// those events to connected browsers over SSE.
//
// The Server is the headless, multi-project mode: it's backed by the on-disk
// session store, lists projects (working directories) and their saved sessions,
// and opens/creates persisted sessions on demand — one agent per session, rooted
// at that session's project. (MirrorServer, by contrast, mirrors a single live
// terminal.)
//
// Auth note: this server has no built-in authentication. Bind to loopback for
// single-user local use; exposing it on a routable address shares full control
// of the agent (shell + file edits) with anyone who can reach it. Put it behind
// your own reverse-proxy auth (e.g. an SSO-terminating proxy or a firewall/VPN)
// if you need to expose it beyond localhost.
package web

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/attach"
	"github.com/pysty1995/moony/internal/awsdeploy"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

// staticFS embeds the browser UI plus its vendored assets (tailwind.css,
// marked.min.js). They're served locally rather than from a CDN because
// some networks block third-party CDNs.
//
//go:embed static
var staticFS embed.FS

// AgentFactory builds an agent rooted at workingDir, loaded with sess's history
// and wired to persist back to it.
type AgentFactory func(workingDir string, sess *sessionpkg.Session) *agent.Agent

// Server hosts persisted, project-scoped chat sessions and the browser UI.
type Server struct {
	store     sessionpkg.Backend
	launchDir string         // the project to show first (the dir the server was started in)
	cfg       *config.Config // labels new sessions and drives the header/status snapshot
	newAgent  AgentFactory

	mu       sync.Mutex
	sessions map[string]*session // live sessions, keyed by their on-disk id

	// When set (AttachLiveMirror), the running terminal's sessions are driven
	// through the bridge hub instead of store-backed agents. The hub tracks
	// which session is ACTIVE in the terminal; opening any OTHER session of the
	// terminal's project (s.launchDir) in the browser switches the terminal to
	// it (hub.Switch) and then mirrors it. Sessions in other projects stay
	// store-backed and independent, since the single-project TUI can't host them.
	mirror *MirrorServer

	// restart, when set, is invoked after the /update command successfully
	// self-updates the binary in place. The headless daemon wires this to exit
	// the process so its service manager (launchd KeepAlive / systemd
	// Restart=always) relaunches the new build. Nil for the terminal-hosted
	// server (the TUI handles /update on its live session and re-execs itself),
	// in which case /update self-updates and advises a manual restart.
	restart func()

	// logf sinks the server's operational log lines (currently the per-server MCP
	// connect trace). Defaults to os.Stderr for the headless daemon, where that's
	// the service log. The terminal-hosted server (app.BuildServer on the relay
	// path) runs in the SAME process as the alt-screen TUI, so it MUST redirect
	// this away from os.Stderr (to the TUI, or a no-op) — otherwise these lines
	// paint over the Bubble Tea frame and garble the UI until a resize repaints.
	logf func(string)

	// metrics holds the process-wide HTTP/session counters exported by /metrics
	// and summarized by /healthz. Created lazily in Handler().
	metrics *metrics

	// deploy is the single in-flight AWS deployment started from the Deploy panel,
	// and its retained output. One at a time: two deploys of one stack would fight
	// over the same CloudFormation change set.
	deploy deployJob

	// deployFn, when set, replaces the real deployer. Tests use it so the panel can
	// be exercised without an AWS account.
	deployFn func(awsdeploy.Options, *deployJob) deployTarget
}

// SetLogf redirects the server's operational log lines. Pass nil to silence them.
// The TUI-embedded server uses this so MCP connect traces never corrupt the
// alt-screen UI; the headless daemon leaves the os.Stderr default.
func (s *Server) SetLogf(fn func(string)) {
	if fn == nil {
		fn = func(string) {}
	}
	s.logf = fn
}

// SetUpdateRestart wires the callback invoked after /update replaces the binary
// in place, so a headless host can relaunch on the new build. Setting it also
// makes /update appear in the browser command palette. Safe to leave unset.
func (s *Server) SetUpdateRestart(fn func()) { s.restart = fn }

// NewServer returns a store-backed, multi-project server. launchDir is the
// project surfaced first; cfg supplies the model for new sessions plus the
// header/status fields shown in the browser; factory builds a per-session agent
// rooted at the session's working dir.
func NewServer(store sessionpkg.Backend, launchDir string, cfg *config.Config, f AgentFactory) *Server {
	return &Server{store: store, launchDir: launchDir, cfg: cfg, newAgent: f, sessions: map[string]*session{}, logf: defaultServerLog}
}

// AttachLiveMirror binds the running terminal (carried by the bridge hub) into
// this multi-project server. Opening any session of the terminal's project in
// the browser switches the terminal to it and drives that live in-terminal
// conversation bidirectionally, while sessions in other projects stay
// store-backed. A no-op with a nil hub.
//
// liveID is unused beyond signalling intent; the active session is read live
// from the hub (hub.ActiveSessionID), so it tracks terminal-side switches too.
func (s *Server) AttachLiveMirror(hub *bridge.Hub, liveID string) {
	if hub == nil {
		return
	}
	s.mirror = NewMirrorServer(hub)
	// When the terminal switches its live session to X, drop any store-backed
	// instance of X so the same session never runs both in the terminal and as
	// an independent parallel agent (they'd share one on-disk session file).
	hub.OnActiveChange(s.evictStoreBacked)
}

// activeID is the terminal's currently-active session id, or "" when no live
// mirror is attached.
func (s *Server) activeID() string {
	if s.mirror == nil {
		return ""
	}
	return s.mirror.hub.ActiveSessionID()
}

// routeToHub reports whether requests for id should be driven through the live
// terminal (the bridge hub) rather than a store-backed agent. Only the
// terminal's CURRENTLY-ACTIVE session routes to the hub; every other session —
// including other sessions of the terminal's own project — runs as an
// independent store-backed agent, so multiple agents can run in parallel on the
// same project. Returns false when no live mirror is attached.
func (s *Server) routeToHub(id string) bool {
	return s.mirror != nil && id != "" && id == s.activeID()
}

// catalogWD resolves the working directory for a read-only marketplace/catalog
// request, supporting BOTH the live terminal-mirror session (which has no
// web-side agent — its project is s.launchDir) and store-backed sessions (the
// agent's working dir). Returns ok=false when the session is unknown. The
// returned sess is the store-backed session, or nil for the live mirror, so
// callers can skip an agent re-attach when there's no web agent to re-attach to.
func (s *Server) catalogWD(r *http.Request) (wd string, sess *session, ok bool) {
	if s.routeToHub(r.PathValue("id")) {
		return s.launchDir, nil, true
	}
	sess = s.lookup(r)
	if sess == nil || sess.agent == nil {
		return "", nil, false
	}
	return sess.agent.Config().WorkingDir, sess, true
}

// evictStoreBacked closes any store-backed instance of id (cancelling an
// in-flight turn) and drops it from the live map, so a session that is live in
// the terminal never also runs as an independent agent against the same on-disk
// file. Safe to call when nothing is open for id.
func (s *Server) evictStoreBacked(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	live := s.sessions[id]
	if live != nil {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if live != nil {
		live.interrupt()
		live.closeBoard() // also tear down its Task Board worktrees
	}
}

// EvictAllLive drops every in-memory session, interrupting any running turn and
// tearing down its Task Board worktrees. Used on shutdown / working-directory
// change so nothing keeps running against a stale root and no worktrees leak.
func (s *Server) EvictAllLive() {
	s.mu.Lock()
	live := make([]*session, 0, len(s.sessions))
	for id, sess := range s.sessions {
		live = append(live, sess)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	for _, sess := range live {
		sess.interrupt()
		sess.closeBoard()
	}
}

// serveLive routes a request for the terminal's currently-active session through
// the hub-backed mirror (the live in-terminal conversation). Other sessions —
// even in the terminal's project — fall through to their own store-backed agent,
// so they run in parallel. Returns true when it handled the request.
func (s *Server) serveLive(w http.ResponseWriter, r *http.Request, fn func(*MirrorServer, http.ResponseWriter, *http.Request)) bool {
	id := r.PathValue("id")
	if !s.routeToHub(id) {
		return false
	}
	// The live (active) session must not also be running store-backed.
	s.evictStoreBacked(id)
	fn(s.mirror, w, r)
	return true
}

type sessionInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type projectInfo struct {
	Key      string `json:"key"`
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
	// Notes is the user's free-form scratchpad for this project, sent with the
	// list so the tree can badge projects that have one without a request per row.
	Notes string `json:"notes,omitempty"`
}

// laptopWire describes a machine in the laptop tree. The standalone server is a
// single laptop with an empty Base (its API is addressed directly); the relay
// returns one entry per connected connector with Base "/l/{host}".
type laptopWire struct {
	Host string `json:"host"`
	Name string `json:"name"`
	Base string `json:"base"`
}

type savedSessionInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Live    bool   `json:"live"`
	Running bool   `json:"running,omitempty"` // an agent is mid-turn (live terminal busy, or a store-backed parallel session)
	State   string `json:"state,omitempty"`   // precise live state: "busy" or "awaiting_approval" (omitted when idle) — drives the sidebar attention indicator + notifications
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /api/laptops", s.handleLaptops)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/projects/{key}/sessions", s.handleProjectSessions)
	mux.HandleFunc("POST /api/projects/{key}/sessions", s.handleCreateProjectSession)
	mux.HandleFunc("DELETE /api/projects/{key}/sessions", s.handleClearProjectSessions)
	mux.HandleFunc("POST /api/projects/open", s.handleOpenDir)
	mux.HandleFunc("POST /api/projects/{key}/notes", s.handleSaveProjectNotes)
	mux.HandleFunc("GET /api/fs", s.handleListDir)
	mux.HandleFunc("GET /api/encryption", s.handleEncryption)
	mux.HandleFunc("GET /api/deploy/aws", s.handleDeployStatus)
	mux.HandleFunc("POST /api/deploy/aws", s.handleDeployRun)
	mux.HandleFunc("GET /api/deploy/aws/log", s.handleDeployLog)
	mux.HandleFunc("POST /api/deploy/aws/cancel", s.handleDeployCancel)
	// Quick actions: user-editable preset prompts behind the composer's split
	// button (project-scoped .moony/quickactions.json, global fallback).
	// Cross-session transcript search (a "where did we discuss X" jump).
	mux.HandleFunc("GET /api/search", s.handleSearch)
	// Stateless fill-in-the-middle inline completion for editor clients
	// (Copilot-style ghost text). Not a chat session; runs on the fast model.
	mux.HandleFunc("POST /api/complete", s.handleComplete)
	mux.HandleFunc("GET /api/quickactions", s.handleGetQuickActions)
	mux.HandleFunc("POST /api/quickactions", s.handleAddQuickAction)
	mux.HandleFunc("PUT /api/quickactions", s.handleUpdateQuickAction)
	mux.HandleFunc("DELETE /api/quickactions", s.handleRemoveQuickAction)
	mux.HandleFunc("POST /api/quickactions/move", s.handleMoveQuickAction)
	mux.HandleFunc("POST /api/quickactions/reset", s.handleResetQuickActions)
	// Task Board: queue prompts that run as parallel headless sub-processes, each
	// in its own throwaway git worktree, with Apply/Discard diff review.
	// CODEBASE_MAP.md: read it, regenerate its structural section, or save the
	// hand-authored pinned section — without sending the agent a message.
	// Read-only file browser/viewer over the session's working tree (sandboxed to
	// the project root, ignore-aware).
	// Per-session environment diagnostics for the Config modal's Doctor tab.
	mux.HandleFunc("GET /api/sessions/{id}/history", s.handleHistory)
	mux.HandleFunc("GET /api/sessions/{id}/doctor", s.handleDoctor)
	// Background jobs. The reads use the store's view API, which never advances the
	// cursor bash_output consumes, so an open panel can't starve the model of output.
	mux.HandleFunc("GET /api/sessions/{id}/jobs", s.handleJobs)
	mux.HandleFunc("GET /api/sessions/{id}/jobs/{job}/tail", s.handleJobTail)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/{job}/kill", s.handleJobKill)
	mux.HandleFunc("DELETE /api/sessions/{id}/jobs/{job}", s.handleJobDelete)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/{job}/delete", s.handleJobDelete)
	// Keyboard for TTY jobs: no approval prompt (the job was gated at start) but the
	// destructive-command refusal still applies.
	mux.HandleFunc("POST /api/sessions/{id}/jobs/{job}/input", s.handleJobInput)
	// The only job route that executes anything; gated by the same approval +
	// destructive-command checks as a model-issued bash call.
	mux.HandleFunc("POST /api/sessions/{id}/jobs", s.handleJobStart)
	mux.HandleFunc("GET /api/sessions/{id}/files", s.handleFileTree)
	mux.HandleFunc("GET /api/sessions/{id}/file", s.handleViewFile)
	mux.HandleFunc("GET /api/sessions/{id}/codebasemap", s.handleCodebaseMap)
	mux.HandleFunc("POST /api/sessions/{id}/codebasemap", s.handleCodebaseMap)
	mux.HandleFunc("PUT /api/sessions/{id}/codebasemap", s.handleCodebaseMap)
	mux.HandleFunc("GET /api/sessions/{id}/taskboard", s.handleListTasks)
	mux.HandleFunc("POST /api/sessions/{id}/taskboard/add", s.handleTaskAdd)
	mux.HandleFunc("POST /api/sessions/{id}/taskboard/apply", s.handleTaskApply)
	mux.HandleFunc("POST /api/sessions/{id}/taskboard/discard", s.handleTaskDiscard)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/sessions/{id}/message", s.handleMessage)
	mux.HandleFunc("POST /api/sessions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/sessions/{id}/answer", s.handleAnswer)
	mux.HandleFunc("POST /api/sessions/{id}/interrupt", s.handleInterrupt)
	mux.HandleFunc("POST /api/sessions/{id}/queue", s.handleQueue)
	mux.HandleFunc("GET /api/sessions/{id}/persona", s.handlePersona)
	mux.HandleFunc("POST /api/sessions/{id}/persona", s.handlePersona)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /api/sessions/{id}/pin", s.handlePinSession)
	mux.HandleFunc("GET /api/sessions/{id}/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/sessions/{id}/skills/catalog", s.handleSkillsCatalog)
	mux.HandleFunc("POST /api/sessions/{id}/skills/install", s.handleSkillInstall)
	mux.HandleFunc("POST /api/sessions/{id}/skills/toggle", s.handleSkillToggle)
	mux.HandleFunc("POST /api/sessions/{id}/skills/remove", s.handleSkillRemove)
	mux.HandleFunc("GET /api/sessions/{id}/mcps", s.handleListMCPs)
	mux.HandleFunc("GET /api/sessions/{id}/mcps/catalog", s.handleMcpsCatalog)
	mux.HandleFunc("POST /api/sessions/{id}/mcps/install", s.handleMcpInstall)
	mux.HandleFunc("POST /api/sessions/{id}/mcps/toggle", s.handleMcpToggle)
	mux.HandleFunc("POST /api/sessions/{id}/mcps/remove", s.handleMcpRemove)
	mux.HandleFunc("GET /api/sessions/{id}/lsp", s.handleListLSP)
	mux.HandleFunc("GET /api/sessions/{id}/lsp/catalog", s.handleLspCatalog)
	mux.HandleFunc("POST /api/sessions/{id}/lsp/install", s.handleLspInstall)
	mux.HandleFunc("POST /api/sessions/{id}/lsp/toggle", s.handleLspToggle)
	mux.HandleFunc("POST /api/sessions/{id}/lsp/remove", s.handleLspRemove)
	mux.HandleFunc("GET /api/sessions/{id}/memory", s.handleListMemory)
	mux.HandleFunc("POST /api/sessions/{id}/memory/save", s.handleSaveMemory)
	mux.HandleFunc("POST /api/sessions/{id}/memory/replace-section", s.handleReplaceMemorySection)
	mux.HandleFunc("POST /api/sessions/{id}/memory/delete", s.handleDeleteMemory)
	mux.HandleFunc("GET /api/sessions/{id}/git", s.handleGitOverview)
	mux.HandleFunc("GET /api/sessions/{id}/git/diff", s.handleGitDiff)
	mux.HandleFunc("GET /api/sessions/{id}/git/commit", s.handleGitCommitDiff)
	mux.HandleFunc("POST /api/sessions/{id}/git/{action}", s.handleGitWrite)
	mux.HandleFunc("GET /api/sessions/{id}/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/sessions/{id}/agents/catalog", s.handleAgentsCatalog)
	mux.HandleFunc("GET /api/sessions/{id}/agents/models", s.handleAgentModels)
	mux.HandleFunc("GET /api/sessions/{id}/agents/options", s.handleAgentOptions)
	mux.HandleFunc("POST /api/sessions/{id}/agents/install", s.handleAgentInstall)
	mux.HandleFunc("POST /api/sessions/{id}/agents/toggle", s.handleAgentToggle)
	mux.HandleFunc("POST /api/sessions/{id}/agents/model", s.handleAgentSetModel)
	mux.HandleFunc("POST /api/sessions/{id}/agents/provider", s.handleAgentSetProvider)
	mux.HandleFunc("POST /api/sessions/{id}/agents/reasoning", s.handleAgentSetReasoning)
	mux.HandleFunc("POST /api/sessions/{id}/agents/remove", s.handleAgentRemove)
	mux.HandleFunc("GET /api/sessions/{id}/workers", s.handleListWorkers)
	mux.HandleFunc("POST /api/sessions/{id}/workers/install", s.handleWorkerInstall)
	mux.HandleFunc("GET /api/sessions/{id}/plugins/catalog", s.handlePluginsCatalog)
	mux.HandleFunc("GET /api/sessions/{id}/plugins/marketplaces", s.handlePluginsMarketplaces)
	mux.HandleFunc("POST /api/sessions/{id}/plugins/marketplace/add", s.handlePluginMarketplaceAdd)
	mux.HandleFunc("POST /api/sessions/{id}/plugins/marketplace/remove", s.handlePluginMarketplaceRemove)
	mux.HandleFunc("POST /api/sessions/{id}/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("POST /api/sessions/{id}/plugins/remove", s.handlePluginRemove)
	mux.HandleFunc("GET /api/sessions/{id}/schedules", s.handleListSchedules)
	mux.HandleFunc("POST /api/sessions/{id}/schedules/add", s.handleScheduleAdd)
	mux.HandleFunc("POST /api/sessions/{id}/schedules/toggle", s.handleScheduleToggle)
	mux.HandleFunc("POST /api/sessions/{id}/schedules/remove", s.handleScheduleRemove)
	mux.HandleFunc("POST /api/sessions/{id}/schedules/run", s.handleScheduleRun)
	// Agent Builder: visual workflow graphs. Laptop-scoped (not session-scoped) —
	// workflows are global on the machine, so the relay tunnels /l/{host}/api/workflows
	// to the connector. Method-specific so they don't collide with the "GET /" SPA
	// catch-all (a method-less mount would).
	mux.HandleFunc("GET /api/workflows", s.handleListWorkflows)
	mux.HandleFunc("POST /api/workflows", s.handleCreateWorkflow)
	mux.HandleFunc("GET /api/workflows/templates", s.handleWorkflowTemplates)
	mux.HandleFunc("GET /api/workflows/capabilities", s.handleWorkflowCapabilities)
	mux.HandleFunc("GET /api/workflows/models", s.handleWorkflowModels)
	mux.HandleFunc("GET /api/workflows/runtime", s.handleWorkflowRuntime)
	mux.HandleFunc("POST /api/workflows/runtime", s.handleSetWorkflowRuntime)
	mux.HandleFunc("POST /api/workflows/agents", s.handleCreateAgent)
	mux.HandleFunc("POST /api/workflows/skills", s.handleCreateSkill)
	mux.HandleFunc("POST /api/workflows/generate", s.handleGenerateCapability)
	mux.HandleFunc("GET /api/workflows/{wid}", s.handleGetWorkflow)
	mux.HandleFunc("PUT /api/workflows/{wid}", s.handleSaveWorkflow)
	mux.HandleFunc("DELETE /api/workflows/{wid}", s.handleDeleteWorkflow)
	mux.HandleFunc("POST /api/workflows/{wid}/run", s.handleRunWorkflow)
	mux.HandleFunc("POST /api/workflows/{wid}/resume", s.handleResumeWorkflow)
	mux.HandleFunc("POST /api/workflows/{wid}/eval", s.handleEvalWorkflow)
	mux.HandleFunc("GET /api/workflows/{wid}/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/workflows/{wid}/runs/{runId}", s.handleGetRun)
	mux.HandleFunc("POST /api/workflows/{wid}/runs/{runId}/resume", s.handleResumeRun)
	// Global allow/deny stores (Tools = command permissions, Domains = web_fetch).
	mux.HandleFunc("GET /api/permissions", s.handleGetPermissions)
	mux.HandleFunc("POST /api/permissions", s.handleAddPermission)
	mux.HandleFunc("DELETE /api/permissions", s.handleRemovePermission)
	mux.HandleFunc("GET /api/domains", s.handleGetDomains)
	mux.HandleFunc("POST /api/domains", s.handleAddDomain)
	mux.HandleFunc("DELETE /api/domains", s.handleRemoveDomain)

	// Observability: /healthz (liveness JSON) and /metrics (Prometheus text
	// exposition). Both are read-only aggregates and carry no transcript content;
	// route labels collapse opaque path segments (see routeLabel/looksLikeID) so
	// session ids never reach a metric label. Auth is the relay's job: it treats
	// /healthz as public so probes work without a login, while /metrics goes
	// through the normal gate like any other path.
	if s.metrics == nil {
		s.metrics = newMetrics()
	}
	mux.HandleFunc("GET /healthz", s.metrics.handleHealthz)
	mux.HandleFunc("GET /metrics", s.metrics.handleMetrics)
	// Count/observe every request from the outside in, so 4xx/5xx and probe hits
	// are recorded too rather than only the requests that reach a handler.
	return s.metrics.middleware(mux, accessLogEnabled())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := indexHTML()
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The shell carries the stamp, so it must never be the cached thing.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(page)
}

// SPAHandler serves the web UI shell — index.html at / and the vendored assets
// under /static/ — from the embedded files, with no agent or session state. The
// relay serves the app with this while routing per-laptop API calls
// (/l/{host}/...) to the connectors, so the page loads even with no connectors.
func SPAHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", withGzip(cacheStatic(http.FileServerFS(staticFS))))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page, err := indexHTML()
		if err != nil {
			http.Error(w, "ui unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(page)
	})
	return mux
}

// cacheStatic marks embedded assets (css/js/fonts) as cacheable. There's no
// build-hash in the filenames, so a deploy of new static assets needs the
// documented hard-refresh — the moderate max-age just cuts repeat-load
// bandwidth within a session instead of caching indefinitely. Cache-Control
// is only added for a 200 response so a 404 (unknown asset) isn't cached.
// Vary is set unconditionally, not just on the compressed path, so a shared
// cache keys correctly on Accept-Encoding even for pass-through responses
// (Range requests, or a client that doesn't send "gzip").
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		immutable := isStampedAsset(r.URL.RawQuery) || assetIsFingerprinted(r.URL.Path)
		next.ServeHTTP(&cacheableWriter{ResponseWriter: w, immutable: immutable}, r)
	})
}

type cacheableWriter struct {
	http.ResponseWriter
	wroteHeader bool
	// immutable is set for requests carrying the current build stamp (?v=…) or an
	// already content-addressed filename: those URLs change when the bytes do, so
	// they can be cached hard instead of re-validated every hour.
	immutable bool
}

func (c *cacheableWriter) WriteHeader(status int) {
	if !c.wroteHeader {
		c.wroteHeader = true
		if status == http.StatusOK {
			if c.immutable {
				c.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				// Unstamped asset: a client that hard-navigates straight to it must
				// still notice a rebuild, so keep this short and revalidated.
				c.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
			}
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *cacheableWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// withGzip compresses static asset responses when the client accepts it.
// Range requests are passed through uncompressed: http.FileServerFS/ServeContent
// slices Range responses out of the original (uncompressed) content, and
// transcoding a partial byte range through gzip on the fly would produce a
// truncated, undecodable stream while still claiming a matching Content-Range.
// HEAD is also passed through: there's no body to compress, and forcing the
// gzip wrapper would only strip the Content-Length HEAD is meant to report.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// acceptsGzip reports whether an Accept-Encoding header lists gzip without
// explicitly disabling it via a zero quality value ("gzip;q=0").
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		name, params, _ := strings.Cut(part, ";")
		if strings.TrimSpace(name) != "gzip" {
			continue
		}
		params = strings.ReplaceAll(params, " ", "")
		if strings.Contains(params, "q=0") && !strings.Contains(params, "q=0.") {
			continue // explicitly disabled, not just an unweighted/low preference
		}
		return true
	}
	return false
}

// gzipResponseWriter pipes response bytes through gzip and strips the
// upstream Content-Length (set by ServeContent for the uncompressed size,
// which would otherwise mismatch the compressed byte count actually sent).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if !g.wroteHeader {
		g.wroteHeader = true
		g.Header().Del("Content-Length")
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	return g.gz.Write(b)
}

// handleLaptops returns the single laptop this server runs on, with an empty
// Base so the browser addresses its API directly (the relay is what fans
// multiple laptops out under /l/{host}). This lets the same laptop-tree UI work
// against the standalone server and the relay alike.
func (s *Server) handleLaptops(w http.ResponseWriter, r *http.Request) {
	host := config.MachineName()
	writeJSON(w, []laptopWire{{Host: host, Name: host, Base: ""}})
}

// handleEncryption reports whether session history is encrypted at rest, for the
// topbar indicator. Not session-scoped: it reflects the store backing this server.
func (s *Server) handleEncryption(w http.ResponseWriter, r *http.Request) {
	mode := strings.TrimSpace(os.Getenv("MN_SESSION_ENCRYPT"))
	if mode == "" {
		mode = "auto"
	}
	writeJSON(w, map[string]any{
		"encrypted": s.store.Encrypted(),
		"mode":      mode,
	})
}

// handleListProjects returns the distinct project working dirs that have saved
// sessions, plus the launch dir, most-recently-active first.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	counts := map[string]int{}
	var order []string // List() is sorted newest-first, so first-seen == most recent
	for _, sess := range all {
		if _, ok := counts[sess.WorkingDir]; !ok {
			order = append(order, sess.WorkingDir)
		}
		counts[sess.WorkingDir]++
	}

	// Read the notes store once for the whole list rather than per project. A
	// failure here is not fatal: the tree is still useful without notes, so fall
	// back to an empty map instead of failing the request.
	notes, err := config.LoadProjectNotes()
	if err != nil {
		notes = nil
	}

	out := make([]projectInfo, 0, len(order)+1)
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		// Notes are keyed by canonical (absolute, cleaned) directory; look them up
		// that way so a project listed by a relative path still shows its notes.
		n := notes[dir]
		if n == "" {
			if abs, err := filepath.Abs(dir); err == nil {
				n = notes[filepath.Clean(abs)]
			}
		}
		out = append(out, projectInfo{Key: projectKey(dir), Dir: dir, Name: filepath.Base(dir), Sessions: counts[dir], Notes: n})
	}
	add(s.launchDir) // current project first
	for _, dir := range order {
		add(dir)
	}
	writeJSON(w, out)
}

// handleProjectSessions lists the saved sessions for a project.
func (s *Server) handleProjectSessions(w http.ResponseWriter, r *http.Request) {
	dir, err := decodeProjectKey(r.PathValue("key"))
	if err != nil {
		http.Error(w, "bad project key", http.StatusBadRequest)
		return
	}
	sessions, err := s.store.ListByDir(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]savedSessionInfo, 0, len(sessions))
	listed := map[string]bool{}
	for _, sess := range sessions {
		title := sess.Title
		if title == "" {
			title = "(untitled)"
		}
		listed[sess.ID] = true
		st := s.sessionState(sess.ID)
		out = append(out, savedSessionInfo{
			ID:      sess.ID,
			Title:   title,
			Detail:  sess.UpdatedAt.Format("Jan 2 15:04"),
			Live:    s.isLive(sess.ID),
			Running: st != "",
			State:   st,
		})
	}
	// Surface the live terminal session here even if it isn't persisted yet (a
	// fresh TUI run with no saved turns), so it's always reachable in its project.
	if live := s.activeID(); live != "" && dir == s.launchDir && !listed[live] {
		st := s.sessionState(live)
		out = append([]savedSessionInfo{{
			ID:      live,
			Title:   "terminal session",
			Detail:  "live",
			Live:    true,
			Running: st != "",
			State:   st,
		}}, out...)
	}
	writeJSON(w, out)
}

// handleCreateProjectSession creates and persists a new session in a project,
// opens it live, and returns its id.
func (s *Server) handleCreateProjectSession(w http.ResponseWriter, r *http.Request) {
	dir, err := decodeProjectKey(r.PathValue("key"))
	if err != nil {
		http.Error(w, "bad project key", http.StatusBadRequest)
		return
	}
	sess := sessionpkg.New(s.cfg.Model, dir)
	if err := s.store.Save(sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.openSession(sess.ID) == nil {
		http.Error(w, "could not open the new session", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessionInfo{ID: sess.ID, Title: "(untitled)", State: stateIdle})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.serveLive(w, r, (*MirrorServer).handleEvents) {
		return
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Track the live stream count for /metrics. Guarded because Handler() (which
	// creates the metrics) may not have run in a unit test that calls the handler
	// directly.
	if s.metrics != nil {
		s.metrics.sseOpen()
		defer s.metrics.sseClose()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if s.metrics != nil {
		atomic.AddInt64(&s.metrics.replayServed, 1)
	}

	id, ch, history := sess.subscribe()
	defer sess.unsubscribe(id)

	// Publish background-job updates only while someone is watching this session.
	sess.startJobWatch()
	defer sess.releaseJobWatch()

	// Cap the initial replay to the most recent turns so a large persisted
	// transcript doesn't stream + render in full on connect; earlier turns load on
	// demand from /history. "?turns=N" overrides (0 or "all" = replay everything,
	// the pre-pagination behavior).
	replayN := defaultReplayTurns
	if q := strings.TrimSpace(r.URL.Query().Get("turns")); q != "" {
		if q == "all" {
			replayN = 0 // explicit opt-in to the full transcript ("Load full history")
		} else if n, err := strconv.Atoi(q); err == nil && n >= 0 {
			replayN = min(n, maxReplayTurns)
		}
	}
	replayHist, dropped := tailReplay(history, replayN)
	if dropped > 0 {
		// Tell the browser how many older turns exist so it can offer "Load earlier
		// history". FirstTurn (== dropped) is the absolute index of the earliest turn
		// we DID replay, i.e. the paging cursor.
		writeSSE(w, wireEvent{Type: "history_start", Dropped: dropped, FirstTurn: dropped})
	}
	for _, ev := range replayHist {
		writeSSE(w, ev)
	}
	flusher.Flush()

	writeSSE(w, wireEvent{Type: "history_end"}) // marks the history/live boundary (client renders replayed thinking blocks collapsed)
	flusher.Flush()

	// Seed the jobs strip with the CURRENT list. The watcher only publishes on change, so
	// without this a viewer who connects while a job is already running sees nothing until
	// that job's state or output next changes — which for a quiet long-running job could
	// be never.
	if jobs := sessionJobWires(sess); len(jobs) > 0 {
		writeSSE(w, wireEvent{Type: "jobs", Jobs: &jobs})
		flusher.Flush()
	}

	ka := time.NewTicker(sseKeepalive)
	defer ka.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-ka.C:
			writeSSEKeepalive(w)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// isPickerMenuCommand reports whether text is a read-only model/provider/reasoning
// picker that merely OPENS a menu (or prints the current value) — safe to run
// while a turn is active. The value-setting forms (with an argument) are excluded
// so a mid-turn config change still goes through the normal queue/idle gate.
func isPickerMenuCommand(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/models", "/model", "/provider", "/providers", "/reasoning", "/think", "/thinking":
		return len(fields) == 1 // bare = open the picker; with an arg it mutates config
	}
	return false
}

// handleHistory is the paged, one-shot counterpart to handleEvents: it returns a
// window of OLDER turns as JSON so the client can prepend earlier history without
// reconnecting and re-streaming the whole transcript.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.metrics != nil {
		atomic.AddInt64(&s.metrics.historyServed, 1)
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	before := atoiDefault(r.URL.Query().Get("before"), 0)
	// Bound the page size: this is a read of in-memory history, but an unbounded
	// turns= would let one request serialize an entire long transcript to JSON.
	n := min(atoiDefault(r.URL.Query().Get("turns"), defaultReplayTurns), maxReplayTurns)
	events, fromTurn, remaining := rangeReplay(sess.historySnapshot(), before, n)
	if events == nil {
		events = []wireEvent{}
	}
	writeJSON(w, map[string]any{"events": events, "fromTurn": fromTurn, "remaining": remaining})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	// The live terminal session is driven through the hub (it handles its own
	// slash commands, approvals, etc.), so forward straight to the mirror.
	if s.serveLive(w, r, (*MirrorServer).handleMessage) {
		return
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Text        string `json:"text"`
		Attachments []struct {
			Name      string `json:"name"`
			MediaType string `json:"mediaType"`
			Data      string `json:"data"` // base64 (optionally a data: URI)
		} `json:"attachments"`
		// Optional one-turn model pin from a quick action; reverted when the turn
		// ends (see session.applyTurnOverride).
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	ov := turnOverride{
		Model:     strings.TrimSpace(body.Model),
		Provider:  strings.TrimSpace(body.Provider),
		Reasoning: strings.TrimSpace(body.Reasoning),
	}
	// Validate the pin BEFORE accepting the turn. Both fields degrade silently if
	// applied blind — an unknown provider id routes to a backend that doesn't
	// exist, and NormalizeReasoning maps anything unrecognized to "off", so a typo
	// in a saved quick action would quietly disable thinking instead of erroring.
	if ov.Provider != "" {
		if _, ok := provider.Lookup(ov.Provider); !ok {
			http.Error(w, "unknown provider "+ov.Provider, http.StatusBadRequest)
			return
		}
	}
	if ov.Reasoning != "" && config.NormalizeReasoning(ov.Reasoning) == config.ReasoningOff &&
		!strings.EqualFold(ov.Reasoning, config.ReasoningOff) {
		http.Error(w, "unknown reasoning level "+ov.Reasoning, http.StatusBadRequest)
		return
	}
	if text == "" && len(body.Attachments) == 0 {
		http.Error(w, "missing text", http.StatusBadRequest)
		return
	}

	// /btw is a concurrent side-question: it must run in ANY state (alongside an
	// active turn), so intercept it before the busy-queue and idle-only command
	// gates below — otherwise it would be queued while busy or, when drained,
	// rejected as an unknown command (it isn't a turn-starting command).
	if fields := strings.Fields(text); len(fields) > 0 && fields[0] == "/btw" {
		sess.askSide(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Model/provider/reasoning PICKERS (the menu-opening forms) are read-only and
	// must open in ANY state — otherwise clicking the model legend while a turn is
	// answering queues "/models" as a chat message instead of showing the list.
	// Run them immediately, like /btw. The value-setting forms (/model <id>, …)
	// fall through to the busy-queue / idle gate so a config change can't race the
	// running turn.
	if isPickerMenuCommand(text) {
		s.runCommand(sess, text)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Resolve any posted attachments into model content blocks + display chips
	// BEFORE the busy-queue gate, so a message sent mid-turn keeps its images/docs
	// (they ride along in the queue instead of being silently dropped).
	if len(body.Attachments) > maxAttachmentsPerTurn {
		http.Error(w, fmt.Sprintf("too many attachments (max %d)", maxAttachmentsPerTurn), http.StatusBadRequest)
		return
	}
	var blocks []llm.ContentBlock
	var wires []attachWire
	for _, in := range body.Attachments {
		data, err := base64.StdEncoding.DecodeString(stripDataURI(in.Data))
		if err != nil {
			http.Error(w, "invalid attachment encoding for "+in.Name, http.StatusBadRequest)
			return
		}
		a, err := attach.FromBytes(in.Name, in.MediaType, data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		blocks = append(blocks, a.Block())
		wires = append(wires, attachWireFromAttachment(a))
	}

	// If a turn is already running, queue the message — WITH its attachments —
	// for later instead of rejecting it (the web mirror of the terminal's
	// busy-time queue). Atomic with the running-turn state, so a turn finishing
	// right now makes enqueueIfBusy return false and we fall through to dispatch
	// immediately. Attachment-only messages queue too.
	if (text != "" || len(blocks) > 0) && sess.enqueueIfBusyWith(text, blocks, wires, ov) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Slash commands and the "#" quick-memory shortcut are handled by the server,
	// not sent to the model. They only run while the session is idle.
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "#") {
		if sess.snapshot().State != stateIdle {
			http.Error(w, "a turn is already running", http.StatusConflict)
			return
		}
		if strings.HasPrefix(text, "#") {
			note := strings.TrimSpace(text[1:])
			out, err := sess.agent.Remember(note, "", false)
			if err != nil {
				out = "memory failed: " + err.Error()
			}
			sess.publishEvent(wireEvent{Type: "info", Text: out})
		} else {
			s.runCommand(sess, text)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Auto-enter read-only plan mode for a large/multi-step task (when enabled),
	// so the agent drafts a plan for approval before touching files.
	if sess.agent.MaybeAutoPlan(text) {
		sess.publishEvent(wireEvent{Type: "info", Text: "This looks like a sizable change — switched to plan mode (read-only) to draft a plan first. Review it, then approve to build (or /build to skip planning)."})
		sess.publishEvent(s.statusEvent(nil, sess))
	}
	// Additively capture a standalone directive to project memory; the prompt is
	// still sent to the model below.
	if msg, ok := sess.agent.AutoCaptureRule(text); ok {
		sess.publishEvent(wireEvent{Type: "info", Text: msg})
	}

	if !sess.startWith(text, blocks, wires, ov) {
		http.Error(w, "a turn is already running", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// drainQueue dispatches queued messages (FIFO) after a turn completes. Non-turn
// items (slash commands, "#" memory notes) return to idle without starting a
// turn, so it keeps dispatching until the queue empties or one item actually
// starts a new turn — at which point that turn's completion resumes the drain.
// (Without the loop a queued non-turn item would strand the rest of the queue.)
func (s *Server) drainQueue(sess *session) {
	for {
		m, ok := sess.dequeueIfIdle()
		if !ok {
			return
		}
		if s.dispatchText(sess, m) {
			return // a turn (or menu/approval) started; resume draining on its completion
		}
	}
}

// dispatchText runs one queued message through the same paths as a freshly
// typed one — a "#" memory note, a slash command, or a model prompt (with any
// carried attachment blocks) — and reports whether it started a turn (so the
// drain loop knows to pause). Only reached while idle (the queue only drains
// then).
func (s *Server) dispatchText(sess *session, m queuedMsg) (started bool) {
	text := strings.TrimSpace(m.text)
	if text == "" && len(m.blocks) == 0 {
		return false
	}
	if strings.HasPrefix(text, "#") {
		note := strings.TrimSpace(text[1:])
		out, err := sess.agent.Remember(note, "", false)
		if err != nil {
			out = "memory failed: " + err.Error()
		}
		sess.publishEvent(wireEvent{Type: "info", Text: out})
		return false
	}
	if strings.HasPrefix(text, "/") {
		s.runCommand(sess, text)
		// Some commands (e.g. /plan, /build) kick off a turn; detect that so the
		// drain pauses until it finishes.
		return sess.snapshot().State != stateIdle
	}
	if text != "" && sess.agent.MaybeAutoPlan(text) {
		sess.publishEvent(wireEvent{Type: "info", Text: "This looks like a sizable change — switched to plan mode (read-only) to draft a plan first. Review it, then approve to build (or /build to skip planning)."})
		sess.publishEvent(s.statusEvent(nil, sess))
	}
	if text != "" {
		if msg, ok := sess.agent.AutoCaptureRule(text); ok {
			sess.publishEvent(wireEvent{Type: "info", Text: msg})
		}
	}
	return sess.startWith(m.text, m.blocks, m.wires, m.ov)
}

// maxAttachmentsPerTurn bounds how many files one message may carry.
const maxAttachmentsPerTurn = 20

// stripDataURI removes a leading "data:<type>;base64," prefix if present, so the
// handler accepts both raw base64 and full data URIs from the browser.
func stripDataURI(s string) string {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if s.serveLive(w, r, (*MirrorServer).handleApprove) {
		return
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Feedback string `json:"feedback"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var d agent.Decision
	switch body.Decision {
	case "approve":
		d = agent.Approve
	case "always":
		d = agent.ApproveAlways
	default:
		d = agent.Decision{Type: agent.DenyType, Feedback: body.Feedback}
	}
	if !sess.approve(d) {
		http.Error(w, "no approval pending", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	if s.serveLive(w, r, (*MirrorServer).handleAnswer) {
		return
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Labels []string   `json:"labels"`
		Groups [][]string `json:"groups"` // one label-slice per question for the grouped (questions[]) tabs UI
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !sess.answerAny(body.Labels, body.Groups) {
		http.Error(w, "no question pending", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if s.serveLive(w, r, (*MirrorServer).handleInterrupt) {
		return
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	sess.interrupt()
	w.WriteHeader(http.StatusNoContent)
}

// handleQueue manages a web-native session's busy-time message queue. Body:
// {"action":"clear"|"pop"|"remove", "index":N, "stash":"draft"}. "pop" removes
// the most recent item and returns it (for Esc-edit in the composer), stashing
// any in-progress draft onto the front first. "remove" drops the item at index.
// Only web-native sessions have a queue, so a live terminal-mirror session 404s
// (its composer never offers queueing).
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if s.serveLive(w, r, (*MirrorServer).handleQueue) {
		return // the terminal's live session manages its queue through the hub
	}
	sess := s.lookup(r)
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Action string `json:"action"`
		Index  int    `json:"index"`
		Stash  string `json:"stash"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch body.Action {
	case "clear":
		n := sess.clearQueue()
		writeJSON(w, map[string]any{"cleared": n})
	case "pop":
		msg, ok := sess.popQueued(body.Stash)
		// Return the popped message's per-turn pin too, so editing a queued quick
		// action and re-sending it still runs on the model it was configured with
		// instead of silently falling back to the session's.
		writeJSON(w, map[string]any{
			"text":      msg.text,
			"model":     msg.ov.Model,
			"provider":  msg.ov.Provider,
			"reasoning": msg.ov.Reasoning,
			"ok":        ok,
		})
	case "remove":
		ok := sess.removeQueuedAt(body.Index)
		writeJSON(w, map[string]any{"ok": ok})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// errSessionBusy means a session can't be deleted because a turn is in progress
// (deleting then would let the agent re-persist it right after deletion).
var errSessionBusy = errors.New("session is busy")

// evictForDelete deletes a session from disk after evicting any idle live copy.
// It returns errSessionBusy when a turn is running, so callers can refuse (single
// delete) or skip (bulk clear).
func (s *Server) evictForDelete(id string) error {
	s.mu.Lock()
	live := s.sessions[id]
	if live != nil {
		if live.snapshot().State != stateIdle {
			s.mu.Unlock()
			return errSessionBusy
		}
		delete(s.sessions, id) // evict so no further turns can start on it
	}
	s.mu.Unlock()
	if live != nil {
		live.closeBoard() // remove its worktrees before the session file goes away
	}
	return s.store.Delete(id)
}

// handleDelete removes a single saved session. A session with a turn in progress
// must be interrupted before it can be deleted. Deleting the session the terminal
// is currently on is allowed: the terminal is first moved to the project's next
// most-recent session (or a fresh one when none remain), the session is deleted,
// and the id moved to is returned so the browser follows along.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "" && id == s.activeID() {
		s.deleteActiveSession(w, id)
		return
	}
	switch err := s.evictForDelete(id); {
	case errors.Is(err, errSessionBusy):
		http.Error(w, "Stop the running turn before deleting this session.", http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	live := s.sessions[id]
	s.mu.Unlock()

	var err error
	if live != nil {
		err = live.agent.SetTitle(body.Title)
	} else {
		err = s.store.Rename(id, body.Title)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "title": body.Title})
}

func (s *Server) handlePinSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	live := s.sessions[id]
	s.mu.Unlock()

	var err error
	if live != nil {
		err = live.agent.SetPinned(body.Pinned)
	} else {
		err = s.store.SetPinned(id, body.Pinned)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "pinned": body.Pinned})
}

// deleteActiveSession deletes the session the terminal is currently on. The
// terminal would re-persist a session it's still sitting on after the next turn,
// so it must first be moved off: we switch it to the project's next most-recent
// session (minting a fresh empty one when this was the last), then delete the
// old session. Switching is only possible while idle, so a mid-turn terminal is
// refused. On success it responds with {"switchedTo": <id>} so the browser opens
// that session too.
func (s *Server) deleteActiveSession(w http.ResponseWriter, id string) {
	if s.mirror == nil {
		http.Error(w, "no live terminal session to delete", http.StatusConflict)
		return
	}
	if s.mirror.hub.State() != bridge.StateIdle {
		http.Error(w, "Stop the running turn before deleting this session.", http.StatusConflict)
		return
	}
	target := s.nextSessionAfter(id)
	if target == "" {
		http.Error(w, "could not prepare a session to switch to", http.StatusInternalServerError)
		return
	}
	// Move the terminal off the doomed session first; Switch is rejected if a
	// turn slipped in between the idle check and now, so honor that as busy.
	if !s.mirror.hub.Switch(target) {
		http.Error(w, "Stop the running turn before deleting this session.", http.StatusConflict)
		return
	}
	if err := s.store.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"switchedTo": target})
}

// nextSessionAfter returns the id the terminal should land on once its current
// session (id) is deleted: the project's most-recent other session, or a freshly
// created empty session when id was the last one in the project.
func (s *Server) nextSessionAfter(id string) string {
	if sessions, err := s.store.ListByDir(s.launchDir); err == nil {
		for _, sess := range sessions { // most-recently-updated first
			if sess.ID != id {
				return sess.ID
			}
		}
	}
	// Nothing else to switch to — mint a fresh session so the terminal (which
	// can't sit on a deleted session) has somewhere to land.
	fresh := sessionpkg.New(s.cfg.Model, s.launchDir)
	if err := s.store.Save(fresh); err != nil {
		return ""
	}
	return fresh.ID
}

// handleClearProjectSessions deletes every saved session in a project. Sessions
// with a turn in progress are skipped (they can't be safely deleted mid-turn);
// the JSON response reports how many were deleted versus skipped.
// handleSaveProjectNotes replaces the notes for one project. The body is
// {"notes": "..."}; an empty string removes the entry rather than storing a blank,
// so a cleared scratchpad leaves no residue in the store and no @notes badge.
func (s *Server) handleSaveProjectNotes(w http.ResponseWriter, r *http.Request) {
	dir, err := decodeProjectKey(r.PathValue("key"))
	if err != nil || dir == "" {
		http.Error(w, "bad project key", http.StatusBadRequest)
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProjectNotesBytes)).Decode(&body); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	notes := strings.TrimSpace(body.Notes)
	if err := config.SetProjectNotes(dir, notes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "notes": notes})
}

// maxProjectNotesBytes caps a notes payload. Notes are hand-written prose, so this
// is generous; the point is only to stop an unbounded body being buffered.
const maxProjectNotesBytes = 1 << 20 // 1 MiB

func (s *Server) handleClearProjectSessions(w http.ResponseWriter, r *http.Request) {
	dir, err := decodeProjectKey(r.PathValue("key"))
	if err != nil {
		http.Error(w, "bad project key", http.StatusBadRequest)
		return
	}
	sessions, err := s.store.ListByDir(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deleted, skipped := 0, 0
	for _, sess := range sessions {
		if err := s.evictForDelete(sess.ID); err != nil {
			skipped++
			continue
		}
		deleted++
	}
	writeJSON(w, map[string]int{"deleted": deleted, "skipped": skipped})
}

// lookup returns the live session for the request's id, opening it from disk
// (loading its history) if it isn't live yet.
func (s *Server) lookup(r *http.Request) *session {
	return s.openSession(r.PathValue("id"))
}

// openSession returns the live session for id, lazily loading it from the store
// (and seeding its history) on first access. Returns nil for unknown ids.
func (s *Server) openSession(id string) *session {
	s.mu.Lock()
	if live := s.sessions[id]; live != nil {
		s.mu.Unlock()
		return live
	}
	s.mu.Unlock()

	sess, err := s.store.Load(id)
	if err != nil {
		return nil
	}
	title := sess.Title
	if title == "" {
		title = "(untitled)"
	}
	live := newSession(id, title, s.newAgent(sess.WorkingDir, sess))
	// Let chat turns invoke saved workflows via the run_workflow tool (@workflow).
	s.attachWorkflowTool(live)
	// After each turn, re-emit the status so token/cost/context counters update,
	// then re-fetch the authoritative usage quota so the status bar's "used"
	// figure reflects the turn that just billed (it's otherwise fetched only once
	// at session open). fetchQuota is async and re-publishes status on success,
	// mirroring the TUI's post-turn refreshQuotaCmd.
	live.onTurnDone = func() {
		live.publishEvent(s.statusEvent(sess, live))
		s.fetchQuota(sess, live)
	}
	// When the provider changes, clear stale quota from the old provider and
	// immediately re-fetch from the new one so the status bar never shows
	// figures that belong to a different endpoint/account.
	live.refreshQuota = func() {
		live.clearQuota()
		live.publishEvent(s.statusEvent(sess, live))
		s.fetchQuota(sess, live) // fetchQuota already runs the fetch in its own goroutine
	}
	// After a clean turn, drain the next queued message (FIFO) — the web-side
	// mirror of the terminal's message queue.
	live.onIdle = func() { s.drainQueue(live) }
	// Seed the header/status snapshot and command palette first, then the
	// replayed transcript, and finally the session's current state, so the
	// browser populates its header, status bar, and slash-command autocomplete on
	// connect AND learns whether this session is idle or busy. The trailing state
	// event is essential: the single-pane UI carries one global "busy/idle" state,
	// so without an authoritative state on connect, switching from a busy session
	// to this (freshly-loaded, idle) one would leave the composer disabled — you
	// couldn't ask here while another session was working.
	seed := []wireEvent{s.statusEvent(sess, live), s.commandsEvent(live)}
	events := append(seed, replayMessages(sess.Messages, sess.TurnMetas, sess.TaskMetas, sess.BoardMessages, sess.InfoNotes)...)
	// Seed the session's plan artifact (if any) so the browser restores its plan
	// panel on connect, mirroring how the transcript replay restores todos.
	if sess.Plan != nil {
		events = append(events, wireEvent{Type: "plan", Plan: planWireFrom(sess.Plan)})
	}
	// The trailing state event is authoritative on connect; use stateWire so it
	// carries CanQueue=true (a web-native session). Without it, a viewer that
	// connects while a turn is already running would have queueing disabled (and
	// thus a locked composer) until the turn ended.
	events = append(events, stateWire(live.snapshot().State))
	live.seedHistory(events)

	s.mu.Lock()
	if existing := s.sessions[id]; existing != nil { // lost a race; reuse the winner
		s.mu.Unlock()
		return existing
	}
	s.sessions[id] = live
	s.mu.Unlock()

	// Best-effort: fetch the authoritative usage quota once, then refresh the
	// status. Never blocks opening the session.
	s.fetchQuota(sess, live)
	// Connect this session's enabled MCP servers in the background (subprocess
	// spawns + handshakes shouldn't block opening the session).
	go s.connectMCP(sess.WorkingDir, live)
	return live
}

// fetchQuota asks the provider for the current 24h usage/quota in the background and,
// on success, records it on the session and re-emits the status snapshot.
func (s *Server) fetchQuota(sess *sessionpkg.Session, live *session) {
	if live == nil || live.agent == nil {
		return
	}
	go func() {
		resp, err := live.agent.Usage(context.Background())
		if err != nil || resp == nil {
			// No usable reading — but the scheduled return to the primary provider
			// must NOT depend on one. Usage() errors on every backend without a quota
			// endpoint (Ollama, LM Studio, llama.cpp), which is exactly where the
			// fallback chain lands, so bailing here would strand the session on the
			// fallback forever. Drive the time-based restore anyway.
			if note := live.agent.MaybeQuotaRestore(); note != "" {
				s.recordQuotaNote(sess, live, note)
			}
			return
		}
		for id, mdl := range resp.Quota.Models {
			config.OverlayRuntimePricing(id, mdl.Pricing.InputTokenCost, mdl.Pricing.OutputTokenCost, mdl.Pricing.CacheReadTokenCost, mdl.Pricing.CacheWriteTokenCost)
		}
		for id, mdl := range resp.Usage.Models {
			config.OverlayRuntimePricing(id, mdl.Pricing.InputTokenCost, mdl.Pricing.OutputTokenCost, mdl.Pricing.CacheReadTokenCost, mdl.Pricing.CacheWriteTokenCost)
		}
		// Label the quota source for the status-bar tooltip: the gateway's project
		// name when a project token is in use, else "personal".
		quotaName := strings.TrimSpace(resp.ProjectName)
		if quotaName == "" {
			// Only the SQE gateway reports a project name; for every other provider the
			// useful label is which provider answered, so the tooltip explains where the
			// figure came from rather than claiming "personal" for all of them.
			pid := ""
			if c := live.agent.Config(); c != nil {
				pid = strings.TrimSpace(c.LLMProvider)
			}
			if pid != "" && provider.Normalize(pid) != provider.SQEAnthropic {
				quotaName = provider.Label(provider.Normalize(pid))
			} else {
				quotaName = "personal"
			}
		}
		live.setQuotaFull(resp.Usage.Spend, resp.Quota.Budget.Spend,
			resp.Balance, resp.BalanceSet, quotaName,
			resp.Usage.InputTokens+resp.Usage.OutputTokens,
			resp.Quota.Budget.InputTokens+resp.Quota.Budget.OutputTokens,
			resp.Quota.Rates.Tokens, resp.Quota.Rates.TokensPeriod)
		// Apply the quota-fallback policy to the fresh numbers. Runs before the
		// status event below so a switch is reflected in the snapshot we publish.
		if note := live.agent.MaybeQuotaFallback(agent.QuotaSnapshot{
			Provider: live.agent.Config().LLMProvider,
			Spend:    resp.Usage.Spend,
			Budget:   resp.Quota.Budget.Spend,
			ResetAt:  agent.ParseQuotaResetTime(resp.Quota.Budget.ResetTime),
			// Prepaid providers report credit left instead of money spent; without
			// these two the policy sees 0/0 and can never fire for them.
			Balance:    resp.Balance,
			BalanceSet: resp.BalanceSet,
		}); note != "" {
			s.recordQuotaNote(sess, live, note)
		}
		live.publishEvent(s.statusEvent(sess, live))
	}()
}

// recordQuotaNote surfaces a quota-fallback notice and persists it into the
// transcript, so the reason the provider changed survives a reload instead of
// living only in a transient event.
//
// The note is queued (NoteInfo) rather than written directly: this runs on the
// background usage-poll goroutine, and the transcript is owned by the turn
// goroutine. When no turn is in flight we drain it immediately; otherwise the
// in-flight turn's own flush picks it up.
func (s *Server) recordQuotaNote(sess *sessionpkg.Session, live *session, note string) {
	if live == nil || live.agent == nil || note == "" {
		return
	}
	live.agent.NoteInfo(note)
	// Persist it now when the session is idle, so the note survives even if no further
	// turn ever runs. flushNotesIfIdle does the idle check under the session lock — an
	// isRunning() test here would be TOCTOU, since a turn can start between the check
	// and the flush.
	live.flushNotesIfIdle()
	live.publishEvent(wireEvent{Type: "info", Text: note})
	live.publishEvent(s.statusEvent(sess, live))
}

func (s *Server) isLive(id string) bool {
	if id != "" && id == s.activeID() {
		return true // the terminal's currently-active (hub-backed) session
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}

// isRunning reports whether session id currently has an agent mid-turn: the live
// terminal session (hub busy) or an open store-backed parallel session that's
// busy. Drives the sidebar's "running" indicator so concurrent agents are visible.
func (s *Server) isRunning(id string) bool {
	return s.sessionState(id) != ""
}

// sessionState returns the precise live state of session id — "busy" or
// "awaiting_approval" — or "" when it's idle or not open. It drives the sidebar's
// attention indicators (a parallel session blocked on approval needs you) and the
// web UI's cross-session notifications. Mirrors the live terminal session through
// the hub and store-backed parallel sessions through their own state.
func (s *Server) sessionState(id string) string {
	if id == "" {
		return ""
	}
	if s.mirror != nil && id == s.activeID() {
		if st := s.mirror.hub.State(); st != bridge.StateIdle {
			return st
		}
		return ""
	}
	s.mu.Lock()
	live := s.sessions[id]
	s.mu.Unlock()
	if live == nil {
		return ""
	}
	if st := live.snapshot().State; st != stateIdle {
		return st
	}
	return ""
}

// projectKey encodes a working dir into a URL-path-safe key.
func projectKey(dir string) string { return base64.RawURLEncoding.EncodeToString([]byte(dir)) }

func decodeProjectKey(key string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(key)
	return string(b), err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, ev wireEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// sseKeepalive is how often an idle event stream emits a keepalive comment so a
// proxy / load balancer (and the relay tunnel) don't reap an idle connection —
// which otherwise makes the browser EventSource reconnect, very visible when
// viewing an idle session while others run. A var so tests can shrink it.
var sseKeepalive = 20 * time.Second

// writeSSEKeepalive writes an SSE comment line, ignored by EventSource clients.
func writeSSEKeepalive(w http.ResponseWriter) { _, _ = io.WriteString(w, ": keepalive\n\n") }

// ActiveTurns reports how many live sessions are mid-turn, plus one of their labels.
//
// Used by the daemon to decide whether an update may proceed: both re-exec paths
// destroy in-flight work (syscall.Exec replaces the process image, so a turn dies with
// no chance to persist its tail), and an unattended daemon has nobody watching to
// notice or resend. A turn costs minutes at worst; losing one costs real work.
func (s *Server) ActiveTurns() (int, string) { return s.activeTurnsExcept(nil) }

// activeTurnsExcept is ActiveTurns ignoring one session — the one that requested the
// update, which is itself mid-turn precisely because it is running /update.
func (s *Server) activeTurnsExcept(skip *session) (int, string) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess != skip {
			sessions = append(sessions, sess)
		}
	}
	mirror := s.mirror
	s.mu.Unlock()

	n, name := 0, ""
	for _, sess := range sessions {
		label := sess.activeTurnLabel()
		if label == "" {
			continue
		}
		n++
		if name == "" {
			name = label
		}
	}
	// The mirrored terminal session lives in the bridge hub, not s.sessions.
	if mirror != nil && mirror.hub != nil && mirror.hub.State() != bridge.StateIdle {
		n++
		if name == "" {
			name = "the terminal session"
		}
	}
	return n, name
}
