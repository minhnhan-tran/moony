package agent

import (
	"encoding/json"
	"testing"
)

// TestBenignBashNonzeroExit: grep/diff/test-style non-zero exits are benign control
// flow and must NOT count as auto-reasoning friction, while real build/test/command
// invocations still do (a non-zero exit there is genuine friction).
func TestBenignBashNonzeroExit(t *testing.T) {
	cmd := func(s string) json.RawMessage { b, _ := json.Marshal(map[string]string{"command": s}); return b }

	benign := []string{
		`grep foo bar.go`,
		`rg TODO`,
		`/usr/bin/grep -n x file`,
		`diff a b`,
		`test -f missing`,
		`[ -d nope ]`,
	}
	for _, c := range benign {
		if !benignBashNonzeroExit(cmd(c)) {
			t.Errorf("benignBashNonzeroExit(%q) = false, want true (benign non-zero exit)", c)
		}
	}

	real := []string{
		`make test`,
		`go build ./...`,
		`npm run lint`,
		`./scripts/publish-release.sh`,
		`cd x && grep foo`, // leading token is cd → not excluded
	}
	for _, c := range real {
		if benignBashNonzeroExit(cmd(c)) {
			t.Errorf("benignBashNonzeroExit(%q) = true, want false (should count as friction)", c)
		}
	}
}
