package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/domains"
	"github.com/pysty1995/moony/internal/permissions"
)

// access.go serves the global allow/deny stores behind the web UI's Tools and
// Domains menus:
//
//   - Tools  -> command permission rules (internal/permissions, global scope):
//               which tools/bash-prefixes run without an approval prompt (allow)
//               or are forbidden (deny). The running agent reloads these between
//               turns, so edits apply on the next message.
//   - Domains -> web_fetch allow/deny (internal/domains): web_fetch reads them
//               per call, so edits apply immediately.
//
// Both are global (~/.config/moony/), so these endpoints aren't
// session-scoped.

// accessLists is the JSON shape both panels read and refresh from.
type accessLists struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// ---- Tools: command permission rules (global scope) ------------------------

func (s *Server) handleGetPermissions(w http.ResponseWriter, _ *http.Request) {
	allow, deny := permissions.Load(s.cfg.WorkingDir).Lists(permissions.Global)
	writeJSON(w, accessLists{Allow: nonNil(allow), Deny: nonNil(deny)})
}

func (s *Server) handleAddPermission(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule string `json:"rule"`
		Deny bool   `json:"deny"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Rule) == "" {
		http.Error(w, "rule is required", http.StatusBadRequest)
		return
	}
	if err := permissions.Load(s.cfg.WorkingDir).Add(in.Rule, in.Deny, permissions.Global); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetPermissions(w, r)
}

func (s *Server) handleRemovePermission(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rule string `json:"rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Rule) == "" {
		http.Error(w, "rule is required", http.StatusBadRequest)
		return
	}
	if err := permissions.Load(s.cfg.WorkingDir).Remove(in.Rule, permissions.Global); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetPermissions(w, r)
}

// ---- Domains: web_fetch allow/deny -----------------------------------------

func (s *Server) handleGetDomains(w http.ResponseWriter, _ *http.Request) {
	l := domains.Load()
	writeJSON(w, accessLists{Allow: nonNil(l.Allowed()), Deny: nonNil(l.Denied())})
}

func (s *Server) handleAddDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain string `json:"domain"`
		Deny   bool   `json:"deny"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Domain) == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	if err := domains.Load().Add(in.Domain, in.Deny); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetDomains(w, r)
}

func (s *Server) handleRemoveDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Domain) == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	if err := domains.Load().Remove(in.Domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetDomains(w, r)
}
