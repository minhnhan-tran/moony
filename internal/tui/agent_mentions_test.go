package tui

import (
	"testing"
)

// NOTE: @persona/@worker/@workflow dispatch-directive expansion moved to the
// agent core (internal/agent: expandMentionDirective), shared by the TUI and web
// sessions, and is tested there. The TUI keeps only the @-mention dropdown
// completer helpers below.

func TestCompleteMention(t *testing.T) {
	names := []string{"explore", "export", "claude"}
	cases := []struct {
		in       string
		wantVal  string
		wantDone bool
	}{
		{"@cl", "@claude ", true},                     // unique prefix -> full name + space
		{"review @cl", "review @claude ", true},       // mid-text trailing token
		{"@ex", "@exp", true},                         // ambiguous -> common prefix
		{"@Cl", "@claude ", true},                     // case-insensitive
		{"@zzz", "@zzz", true},                        // no match: consume Tab, unchanged
		{"@claude done", "@claude done", false},       // finished token (space after)
		{"no mention here", "no mention here", false}, // no @
		{"email@host", "email@host", false},           // @ not at a mention boundary
		{"see @cmd/x", "see @cmd/x", false},           // path-like token
	}
	for _, tc := range cases {
		gotVal, gotDone := completeMention(tc.in, names)
		if gotVal != tc.wantVal || gotDone != tc.wantDone {
			t.Errorf("completeMention(%q) = (%q, %v), want (%q, %v)", tc.in, gotVal, gotDone, tc.wantVal, tc.wantDone)
		}
	}
}

func TestTrailingMention(t *testing.T) {
	cases := []struct {
		in          string
		wantPartial string
		wantOK      bool
	}{
		{"@", "", true},             // bare @ at start → empty partial (show all)
		{"@ex", "ex", true},         // partial after @
		{"hey @expl", "expl", true}, // mid-text trailing token
		{"(@wo", "wo", true},        // ( is a valid boundary
		{"@claude done", "", false}, // space after → token finished
		{"plain text", "", false},   // no @
		{"email@host", "", false},   // @ not at a mention boundary
		{"see @cmd/x", "", false},   // path-like token
	}
	for _, tc := range cases {
		_, partial, ok := trailingMention(tc.in)
		if ok != tc.wantOK || (ok && partial != tc.wantPartial) {
			t.Errorf("trailingMention(%q) = (%q, %v), want (%q, %v)", tc.in, partial, ok, tc.wantPartial, tc.wantOK)
		}
	}
}
