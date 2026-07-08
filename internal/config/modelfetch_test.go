package config

import (
	"errors"
	"strings"
	"testing"
)

func TestModelFetchNote(t *testing.T) {
	// A generic fetch error surfaces its cause and falls back to the built-in list.
	other := errors.New("connection refused")
	if !strings.Contains(ModelFetchNote(other), "connection refused") {
		t.Error("generic errors should still surface the cause")
	}

	// A region-restriction error is hidden behind the friendly message.
	regionErr := errors.New(`403 Forbidden: {"message":"This model is unavailable in your region"}`)
	note := ModelFetchNote(regionErr)
	if strings.Contains(note, "403") || strings.Contains(note, "Forbidden") {
		t.Errorf("note must not leak the raw 403: %q", note)
	}
	if !strings.Contains(note, regionRestrictedNote) {
		t.Errorf("note should surface the region-restricted message: %q", note)
	}
}

func TestTurnErrorNoteRegion(t *testing.T) {
	regionErr := errors.New(`the provider returned 403 Forbidden: {"message":"This model is unavailable in your region. Check the regional restrictions in the provider's model policy.","type":"error"}`)
	if !ModelRegionRestricted(regionErr) {
		t.Fatal("should detect regional restriction")
	}
	note := TurnErrorNote(regionErr)
	if strings.Contains(note, "403") || strings.Contains(note, "Forbidden") || strings.Contains(note, "request_id") {
		t.Errorf("note must not leak the raw 403: %q", note)
	}
	if !strings.Contains(note, regionRestrictedNote) {
		t.Errorf("note should surface the region-restricted message: %q", note)
	}
	// Unrecognized turn errors pass through unchanged.
	if got := TurnErrorNote(errors.New("boom")); got != "boom" {
		t.Errorf("generic turn error should pass through, got %q", got)
	}
}
