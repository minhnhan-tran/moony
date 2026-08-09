package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
)

// The browser hides "always allow" based on this flag, so it has to survive the
// agent -> wire -> JSON hop.
func TestApprovalNoAlwaysReachesTheWire(t *testing.T) {
	ev := toWire(agent.ApprovalRequest{
		Tool:                 "bash",
		Summary:              "git commit -m x",
		AlwaysDisabled:       true,
		AlwaysDisabledReason: "git commits and pushes are confirmed every time",
	})
	if ev.Type != "approval" {
		t.Fatalf("want an approval event, got %q", ev.Type)
	}
	if !ev.NoAlways {
		t.Error("NoAlways should be set so the UI can hide the button")
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"noAlways":true`) {
		t.Errorf("the flag should be serialized for the browser: %s", b)
	}
	if !strings.Contains(string(b), "confirmed every time") {
		t.Errorf("the reason should be serialized: %s", b)
	}

	// An ordinary approval must NOT carry it, or "always allow" disappears
	// everywhere and the setting becomes unusable.
	plain := toWire(agent.ApprovalRequest{Tool: "write_file", Summary: "a.txt"})
	if plain.NoAlways {
		t.Error("an ordinary approval should still offer 'always allow'")
	}
	if pb, _ := json.Marshal(plain); strings.Contains(string(pb), "noAlways") {
		t.Errorf("the flag should be omitted when unset: %s", pb)
	}
}

// The front-end must actually consume the flag; a rename on either side would
// silently restore the confusing button.
func TestApprovalUIHonorsNoAlways(t *testing.T) {
	js, err := os.ReadFile("static/app-core.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "ev.noAlways") {
		t.Error("app-core.js should read ev.noAlways from the approval event")
	}
	if !strings.Contains(src, "b-always") {
		t.Error("app-core.js should target the .b-always button")
	}
	html, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "b-always") {
		t.Error("index.html should still define the .b-always button")
	}
}
