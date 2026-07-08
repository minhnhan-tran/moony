package web

import (
	"encoding/json"
	"net/http"

	"github.com/pysty1995/moony/internal/announce"
)

// Announcements API: maintainer-authored update banners shown in the web UI and
// TUI. With no built-in authentication, every caller can read and author them —
// deploy the relay behind your own access control if you need to restrict who
// can post. It's a standalone handler mounted on the relay so the whole fleet
// sees the same announcements.

type announcementsAPI struct {
	store *announce.Store
}

// AnnouncementsHandler returns the Announcements API as a self-contained handler.
// store may be nil (treated as empty).
func AnnouncementsHandler(store *announce.Store) http.Handler {
	h := &announcementsAPI{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/announcements", h.get)
	mux.HandleFunc("POST /api/announcements", h.add)
	mux.HandleFunc("DELETE /api/announcements", h.remove)
	return mux
}

// GET /api/announcements -> {"announcements":[…], "admin":true}. The list is
// public; admin is always true since there's no identity gate anymore (the SPA
// uses it to decide whether to show the authoring controls).
func (h *announcementsAPI) get(w http.ResponseWriter, r *http.Request) {
	list := []announce.Announcement{}
	if h.store != nil {
		list = h.store.List()
	}
	writeJSON(w, map[string]any{"announcements": list, "admin": true})
}

// POST /api/announcements {"title":"…","body":"…","level":"info|warn|critical"}
func (h *announcementsAPI) add(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "announcements unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := h.store.Add(body.Title, body.Body, body.Level, ""); err != nil {
		http.Error(w, "title or body required", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"announcements": h.store.List(), "admin": true})
}

// DELETE /api/announcements?id=… (or {"id":"…"} body) — remove one.
func (h *announcementsAPI) remove(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "announcements unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id = body.ID
	}
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := h.store.Remove(id); err != nil {
		http.Error(w, "remove failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"announcements": h.store.List(), "admin": true})
}
