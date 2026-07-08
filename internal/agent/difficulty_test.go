package agent

import "testing"

func TestTurnLooksHard(t *testing.T) {
	hard := []string{
		"Please refactor the auth package to remove the duplication",
		"why does the relay drop announcements after a rollout?",
		"debug this race condition in the worker pool",
		"implement a persistent embedding cache",
		"optimize search_code so it doesn't re-embed everything",
		"```go\nfunc x(){}\n```", // fenced code
	}
	for _, s := range hard {
		if !turnLooksHard(s) {
			t.Errorf("expected hard: %q", s)
		}
	}
	easy := []string{
		"", "commit and push", "bump the version", "run the tests",
		"thanks!", "yes do it", "rename relay web to mirror web",
	}
	for _, s := range easy {
		if turnLooksHard(s) {
			t.Errorf("expected easy: %q", s)
		}
	}
}

func TestTurnLooksHardManyPaths(t *testing.T) {
	if !turnLooksHard("update a.go b.go and c.go") {
		t.Error("three file references should be hard")
	}
}
