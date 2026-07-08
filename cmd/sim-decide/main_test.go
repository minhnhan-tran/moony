package main

import (
	"strings"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"action":"stop"}`: `{"action":"stop"}`,
		"here you go:\n```json\n{\"action\":\"message\"}\n```": `{"action":"message"}`,
		`prose {"a":1} trailing`:                               `{"a":1}`,
		`no json here`:                                         ``,
		`}{`:                                                   ``, // closing before opening
	}
	for in, want := range cases {
		if got := extractJSONObject(in); got != want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseActionValid(t *testing.T) {
	a, err := parseAction("sure —\n{\"action\":\"answerGroup\",\"groups\":[[\"Postgres\"],[\"redis\",\"60s TTL\"]]}\ndone")
	if err != nil {
		t.Fatalf("parseAction: %v", err)
	}
	if a.Action != "answerGroup" || len(a.Groups) != 2 || a.Groups[1][1] != "60s TTL" {
		t.Fatalf("parsed wrong: %+v", a)
	}
}

func TestParseActionRejectsUnknown(t *testing.T) {
	if _, err := parseAction(`{"action":"frobnicate"}`); err == nil {
		t.Fatal("expected an error for an unknown action")
	}
	if _, err := parseAction(`totally not json`); err == nil {
		t.Fatal("expected an error when there is no JSON object")
	}
}

// The decision prompt must carry the persona, the goal, and — crucially — the
// pending question with its options, so the model can answer in character.
func TestBuildUserPromptQuestion(t *testing.T) {
	req := request{
		Persona: "terse backend engineer",
		Goal:    "add /health",
		History: []turn{{Role: "user", Text: "hi"}, {Role: "agent", Text: "which db?"}},
		Pending: pending{Kind: "question", Questions: []qspec{{Question: "Which database?", Options: []string{"Postgres", "MySQL"}, Multiple: false, AllowText: true}}},
	}
	p := buildUserPrompt(req)
	for _, want := range []string{"terse backend engineer", "add /health", "Which database?", "Postgres | MySQL", `action "answer"`, "allowText=true"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
}

// A grouped ask must steer the model to answerGroup and list every question.
func TestBuildUserPromptGroup(t *testing.T) {
	req := request{
		Pending: pending{Kind: "question", Questions: []qspec{
			{Question: "db?", Options: []string{"pg"}},
			{Question: "cache?", AllowText: true},
		}},
	}
	p := buildUserPrompt(req)
	for _, want := range []string{`"answerGroup"`, "Q1: db?", "Q2: cache?", "2 QUESTIONS"} {
		if !strings.Contains(p, want) {
			t.Errorf("group prompt missing %q\n---\n%s", want, p)
		}
	}
}

func TestBuildUserPromptIdle(t *testing.T) {
	p := buildUserPrompt(request{Persona: "x", Goal: "y"})
	if !strings.Contains(p, "IDLE") || !strings.Contains(p, `action "message"`) {
		t.Errorf("idle prompt should invite a message, got:\n%s", p)
	}
}
