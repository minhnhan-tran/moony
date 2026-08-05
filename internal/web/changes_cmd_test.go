package web

import (
	"strings"
	"testing"
)

// /changes must be discoverable: the agent has had TurnChangesDiff all along, but
// with no catalog entry there is no way for a user to find or invoke it.
func TestCommandCatalogOffersChanges(t *testing.T) {
	cat := commandCatalog("")
	var found *cmdWire
	for i := range cat {
		if cat[i].Name == "/changes" {
			found = &cat[i]
			break
		}
	}
	if found == nil {
		t.Fatal("/changes is missing from the command catalog, so the feature is unreachable from the UI")
	}
	if found.Desc == "" {
		t.Error("/changes needs a description; the palette shows it")
	}
}

// The alias must be handled by the same switch arm, or typing the habitual "/diff"
// yields "Unknown command" for a feature that exists.
func TestChangesAliasIsDispatched(t *testing.T) {
	src := readWebSource(t, "commands.go")
	i := strings.Index(src, `case "/changes", "/diff":`)
	if i < 0 {
		t.Fatal(`expected a combined case "/changes", "/diff" arm`)
	}
	// It must actually call the agent, not just echo something.
	arm := src[i:]
	if j := strings.Index(arm, "\n\tcase "); j > 0 {
		arm = arm[:j]
	}
	if !strings.Contains(arm, "TurnChangesDiff(") {
		t.Error("the /changes arm must call agent.TurnChangesDiff")
	}
	// An empty turn must say so rather than emitting an empty code fence.
	if !strings.Contains(arm, "No files changed") {
		t.Error("the /changes arm must handle the no-changes case explicitly")
	}
	// The diff has to be fenced, or the client renders it as prose with no +/- colour.
	if !strings.Contains(arm, "```diff") {
		t.Error("the diff must be fenced as ```diff for the client to highlight it")
	}
}

// The catalog is delivered to the client in the "commands" SSE event, so /changes must
// be present there — a registry entry the client never receives is still invisible.
func TestCommandsEventCarriesChanges(t *testing.T) {
	srv, id := jobsFixture(t, "auto")
	sess := srv.openSession(id)
	if sess == nil {
		t.Fatal("no session")
	}
	ev := srv.commandsEvent(sess)
	if ev.Type != "commands" {
		t.Fatalf("event type = %q, want commands", ev.Type)
	}
	var found bool
	for _, c := range ev.Commands {
		if c.Name == "/changes" {
			found = true
			break
		}
	}
	if !found {
		t.Error("/changes is not in the commands event, so the palette never shows it")
	}
}
