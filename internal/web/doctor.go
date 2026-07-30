package web

// Doctor panel — a per-session diagnostics endpoint powering the Config modal's
// "Doctor" tab. It runs the shared environment checks (doctor.Run: runtime,
// GoEnv, endpoints, config files, MCP/LSP config, hooks, subagents, sandbox,
// netrc) and augments them with checks that need the live session: an embeddings
// smoke test plus the actual running/connected state of LSP + MCP servers (not
// just whether they are configured). Returns a flat JSON list the UI renders as status rows.

import (
	"net/http"

	"github.com/pysty1995/moony/internal/doctor"
	"github.com/pysty1995/moony/internal/mcp"
)

// doctorCheck is one row in the Doctor panel's JSON response.
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "warn" | "fail"
	Detail string `json:"detail"`
}

func doctorStatusString(s doctor.Status) string {
	switch s {
	case doctor.StatusOK:
		return "ok"
	case doctor.StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// handleDoctor runs the diagnostics for a session and returns them as JSON.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	cfg := sess.agent.Config()

	out := []doctorCheck{}
	add := func(c doctor.Check) {
		out = append(out, doctorCheck{Name: c.Name, Status: doctorStatusString(c.Status), Detail: c.Detail})
	}

	// Shared environment/config checks.
	for _, c := range doctor.Run(ctx, cfg, sess.agent.CheckAuth).Checks {
		add(c)
	}
	// Live checks that need the running session.
	add(doctor.CheckEmbeddings(ctx, cfg, sess.agent.EmbedClient()))
	add(lspLiveCheck(sess))
	add(mcpLiveCheck(sess, cfg.WorkingDir))

	writeJSON(w, map[string]any{"checks": out})
}

// lspLiveCheck reports which configured language servers are actually running
// right now (Manager.Running()), complementing doctor's config-only LSP check.
func lspLiveCheck(sess *session) doctor.Check {
	running := sess.agent.LSP().Running()
	if len(running) == 0 {
		return doctor.Check{Name: "lsp (live)", Status: doctor.StatusOK, Detail: "no language servers running"}
	}
	detail := "running: "
	for i, n := range running {
		if i > 0 {
			detail += ", "
		}
		detail += n
	}
	return doctor.Check{Name: "lsp (live)", Status: doctor.StatusOK, Detail: detail}
}

// mcpLiveCheck reports the live connection state of each configured MCP server
// (connected/failed/not-connected), complementing doctor's config-only check.
func mcpLiveCheck(sess *session, workingDir string) doctor.Check {
	cfgs, err := mcp.LoadConfigs(workingDir)
	if err != nil {
		return doctor.Check{Name: "mcp (live)", Status: doctor.StatusFail, Detail: err.Error()}
	}
	if len(cfgs) == 0 {
		return doctor.Check{Name: "mcp (live)", Status: doctor.StatusOK, Detail: "no servers configured"}
	}
	var failed int
	detail := ""
	for _, c := range cfgs {
		st := sess.mcpConnStatus(c.Name)
		var word string
		switch {
		case st.connected:
			word = "connected"
		case st.failed:
			failed++
			word = "failed"
		default:
			word = "idle"
		}
		if detail != "" {
			detail += ", "
		}
		detail += c.Name + " (" + word + ")"
	}
	status := doctor.StatusOK
	if failed > 0 {
		status = doctor.StatusWarn
	}
	return doctor.Check{Name: "mcp (live)", Status: status, Detail: detail}
}
