package config

import (
	"errors"
	"strings"
	"testing"
)

func TestModelFetchNote(t *testing.T) {
	authErr := errors.New(`anthropic endpoint: 403 Forbidden: {"message":"Caller not in authorized group","request_id":"x"}`)
	if !NotInGenAIGroup(authErr) {
		t.Fatal("should detect not-in-group")
	}
	note := ModelFetchNote(authErr)
	if strings.Contains(note, "403") || strings.Contains(note, "Forbidden") {
		t.Errorf("note must not leak the raw 403: %q", note)
	}
	if !strings.Contains(note, GenAIOnboardingURL) {
		t.Errorf("note should point to onboarding: %q", note)
	}

	other := errors.New("connection refused")
	if NotInGenAIGroup(other) {
		t.Error("should not misclassify a generic error")
	}
	if !strings.Contains(ModelFetchNote(other), "connection refused") {
		t.Error("generic errors should still surface the cause")
	}
}

func TestTurnErrorNoteRegion(t *testing.T) {
	regionErr := errors.New(`floodgate returned 403 Forbidden: {"message":"This model is unavailable in your region. Check the regional restrictions at https://genai.apple.com/policies/models/#model-regional-restrictions.","type":"error"}`)
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
	// Region restriction is not misclassified as an onboarding problem.
	if NotInGenAIGroup(regionErr) {
		t.Error("regional restriction is not a group/onboarding error")
	}
}
