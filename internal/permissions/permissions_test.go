package permissions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestCheckDefaultWhenNoRules(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if got := r.Check("write_file", ""); got != Default {
		t.Fatalf("want Default, got %v", got)
	}
}

func TestCheckNilRules(t *testing.T) {
	var r *Rules
	if got := r.Check("write_file", ""); got != Default {
		t.Fatalf("nil rules should be Default, got %v", got)
	}
}

func TestAllowToolName(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := Load(dir)
	if err := r.Add("write_file", false, Project); err != nil {
		t.Fatal(err)
	}
	if got := r.Check("write_file", ""); got != Allow {
		t.Fatalf("want Allow, got %v", got)
	}
	if got := r.Check("edit_file", ""); got != Default {
		t.Fatalf("unrelated tool should be Default, got %v", got)
	}
}

func TestDenyToolName(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("bash", true, Project); err != nil {
		t.Fatal(err)
	}
	if got := r.Check("bash", "ls"); got != Deny {
		t.Fatalf("want Deny, got %v", got)
	}
}

func TestDenyWinsOverAllow(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	// Allow at project, deny at global — deny must win regardless of scope.
	if err := r.Add("write_file", false, Project); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("write_file", true, Global); err != nil {
		t.Fatal(err)
	}
	if got := r.Check("write_file", ""); got != Deny {
		t.Fatalf("deny must win, got %v", got)
	}
}

func TestBashPrefixRule(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("bash:git ", false, Project); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("bash:rm -rf", true, Project); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		cmd  string
		want Decision
	}{
		{"git status", Allow},
		{"  git push", Allow}, // leading whitespace trimmed
		{"gitx", Default},     // "git " prefix requires the trailing space
		{"rm -rf /tmp/x", Deny},
		{"ls -la", Default},
	}
	for _, c := range cases {
		if got := r.Check("bash", c.cmd); got != c.want {
			t.Errorf("Check(bash, %q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestBashRuleIgnoredForOtherTools(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("bash:git ", true, Project); err != nil {
		t.Fatal(err)
	}
	// A bash: rule must not match a non-bash tool even if names overlap.
	if got := r.Check("write_file", "git "); got != Default {
		t.Fatalf("bash rule should not affect write_file, got %v", got)
	}
}

func TestAddRemovePersists(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := Load(dir)
	if err := r.Add("write_file", false, Project); err != nil {
		t.Fatal(err)
	}
	// Reload from disk to confirm persistence.
	if got := Load(dir).Check("write_file", ""); got != Allow {
		t.Fatalf("rule did not persist, got %v", got)
	}
	if err := r.Remove("write_file", Project); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir).Check("write_file", ""); got != Default {
		t.Fatalf("rule not removed, got %v", got)
	}
}

func TestAddMovesBetweenAllowAndDeny(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := Load(dir)
	if err := r.Add("bash", false, Project); err != nil { // allow
		t.Fatal(err)
	}
	if err := r.Add("bash", true, Project); err != nil { // now deny
		t.Fatal(err)
	}
	// Should be a single deny rule, no lingering allow.
	if got := r.Check("bash", "ls"); got != Deny {
		t.Fatalf("want Deny after switch, got %v", got)
	}
	allow := r.Allowed()
	for _, a := range allow {
		if a == "bash" {
			t.Fatal("bash should no longer be in the allow list")
		}
	}
}

func TestAddEmptyRuleRejected(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("   ", false, Project); err == nil {
		t.Fatal("expected error for empty rule")
	}
}

func TestProjectFileLocation(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := Load(dir)
	if err := r.Add("write_file", false, Project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.ProjectDir, "permissions.json")); err != nil {
		t.Fatalf("project permissions.json not written: %v", err)
	}
}

func TestEnsureGlobalDefaultsSeedsFileEditTools(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	// With no file yet, nothing is pre-approved.
	if got := Load(dir).Check("write_file", ""); got != Default {
		t.Fatalf("before seeding, want Default, got %v", got)
	}

	EnsureGlobalDefaults()

	r := Load(dir)
	for _, tool := range []string{"write_file", "edit_file", "multi_edit", "apply_patch"} {
		if got := r.Check(tool, ""); got != Allow {
			t.Errorf("after seeding, %s should be Allow, got %v", tool, got)
		}
	}
	// Riskier tools are intentionally left to the approval policy.
	if got := r.Check("bash", "ls"); got != Default {
		t.Errorf("bash should not be pre-approved by the default seed, got %v", got)
	}
}

func TestEnsureGlobalDefaultsNeverOverwrites(t *testing.T) {
	isolate(t)
	dir := t.TempDir()

	// User removes write_file from the global allow; a later EnsureGlobalDefaults
	// must not re-seed over their choice.
	if err := Load(dir).Add("edit_file", false, Global); err != nil { // create the file
		t.Fatal(err)
	}
	EnsureGlobalDefaults()
	if got := Load(dir).Check("write_file", ""); got != Default {
		t.Errorf("EnsureGlobalDefaults must not overwrite an existing file (write_file became %v)", got)
	}
}

func TestBashAllowRejectsCommandSmuggling(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("bash:git ", false, Project); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd  string
		want Decision
	}{
		{"git status", Allow},             // simple allowed command
		{"git status && git log", Allow},  // every segment covered by the allow prefix
		{"git status; rm -rf ~", Default}, // smuggled 2nd command must NOT ride the allow
		{"git log | sh", Default},         // piped-in command not covered
		{"git commit && curl x | sh", Default},
		{"echo $(git status)", Default},     // command substitution isn't vettable
		{"git diff > /etc/passwd", Default}, // redirection isn't vettable
		{"git status\nrm -rf ~", Default},   // newline separates commands
	}
	for _, c := range cases {
		if got := r.Check("bash", c.cmd); got != c.want {
			t.Errorf("Check(bash, %q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestBashDenyResistsChainingEvasion(t *testing.T) {
	isolate(t)
	r := Load(t.TempDir())
	if err := r.Add("bash:rm -rf", true, Project); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd  string
		want Decision
	}{
		{"rm -rf /", Deny},
		{"true; rm -rf /", Deny},         // chained after a harmless command
		{"echo hi && rm -rf /tmp", Deny}, // chained with &&
		{"ls | rm -rf .", Deny},          // after a pipe
		{"echo hi\nrm -rf /tmp", Deny},   // after a newline
		{"ls -la", Default},              // unrelated command
	}
	for _, c := range cases {
		if got := r.Check("bash", c.cmd); got != c.want {
			t.Errorf("Check(bash, %q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestMalformedRulesFileFlagged(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	pdir := filepath.Join(dir, config.ProjectDir)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "permissions.json"), []byte("{ not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := Load(dir); !r.Malformed() {
		t.Fatal("a permissions file that exists but fails to parse must set Malformed()")
	}
	// A clean, absent project file must NOT be flagged as malformed.
	if r := Load(t.TempDir()); r.Malformed() {
		t.Fatal("absent permissions files must not be flagged as malformed")
	}
}
