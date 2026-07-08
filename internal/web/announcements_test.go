package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/announce"
)

// With auth disabled (nil provider = local mode) writes are open and admin=true.
func TestAnnouncementsHandlerLocalMode(t *testing.T) {
	store := announce.OpenAt("")
	h := AnnouncementsHandler(store)

	// GET empty.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/announcements", nil))
	if rec.Code != 200 {
		t.Fatalf("GET code %d", rec.Code)
	}
	var got struct {
		Announcements []announce.Announcement `json:"announcements"`
		Admin         bool                    `json:"admin"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Admin {
		t.Error("local mode should report admin=true")
	}
	if len(got.Announcements) != 0 {
		t.Errorf("want empty, got %d", len(got.Announcements))
	}

	// POST adds.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/announcements", strings.NewReader(`{"title":"Hello","body":"World","level":"warn"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST code %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.List()) != 1 || store.List()[0].Title != "Hello" {
		t.Fatalf("post did not add: %+v", store.List())
	}
	id := store.List()[0].ID

	// DELETE removes.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/announcements?id="+id, nil))
	if rec.Code != 200 {
		t.Fatalf("DELETE code %d", rec.Code)
	}
	if len(store.List()) != 0 {
		t.Errorf("delete failed: %+v", store.List())
	}
}

// POST with empty title+body is rejected.
func TestAnnouncementsHandlerRejectsEmpty(t *testing.T) {
	h := AnnouncementsHandler(announce.OpenAt(""))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/announcements", strings.NewReader(`{"title":"","body":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty, got %d", rec.Code)
	}
}
