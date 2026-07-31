package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// withGlobalConfigDir points GlobalConfigDir at a temp dir for the test so
// USER.md / global AGENTS.md writes don't touch the real ~/.config.
func withGlobalConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// GlobalConfigDir resolves to <XDG_CONFIG_HOME>/moony.
	return filepath.Join(dir, "moony")
}

func TestParseMemoryScope(t *testing.T) {
	cases := []struct {
		arg   string
		note  string
		cat   string
		scope MemoryScope
	}{
		{"just a note", "just a note", "", ScopeProject},
		{"--global shared rule", "shared rule", "", ScopeGlobal},
		{"-g shared", "shared", "", ScopeGlobal},
		{"--user I like terse replies", "I like terse replies", "", ScopeUser},
		{"-u prefers dark mode", "prefers dark mode", "", ScopeUser},
		{"--user --preference tabs over spaces", "tabs over spaces", "preference", ScopeUser},
	}
	for _, c := range cases {
		note, cat, scope := ParseMemoryScope(c.arg)
		if note != c.note || cat != c.cat || scope != c.scope {
			t.Errorf("ParseMemoryScope(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.arg, note, cat, scope, c.note, c.cat, c.scope)
		}
	}
	// ParseMemoryFlags stays back-compatible (global bool).
	if _, _, g := ParseMemoryFlags("--global x"); !g {
		t.Error("ParseMemoryFlags should still report global=true for --global")
	}
	if _, _, g := ParseMemoryFlags("--user x"); g {
		t.Error("ParseMemoryFlags should report global=false for --user (user is not global-AGENTS)")
	}
}

func TestRememberUserWritesUserProfile(t *testing.T) {
	gdir := withGlobalConfigDir(t)
	wd := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: wd}, nil, tools.NewRegistry(wd), nil, nil)

	if _, err := a.RememberUser("prefers concise, no preamble", "style"); err != nil {
		t.Fatalf("RememberUser: %v", err)
	}

	// Written to <gdir>/USER.md, NOT the project or global AGENTS.md.
	userPath := filepath.Join(gdir, config.UserProfileFilename)
	data, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("USER.md not written: %v", err)
	}
	if !strings.Contains(string(data), "prefers concise, no preamble") {
		t.Errorf("USER.md missing the note:\n%s", string(data))
	}
	if _, err := os.Stat(filepath.Join(wd, "AGENTS.md")); err == nil {
		t.Error("RememberUser must not write the project AGENTS.md")
	}
	// A user-scope change is 'global' for refresh (fans out to all sessions).
}

func TestUserProfileInSystemPrompt(t *testing.T) {
	gdir := withGlobalConfigDir(t)
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gdir, config.UserProfileFilename),
		[]byte("## Style\n- Wants terse answers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: wd}, nil, tools.NewRegistry(wd), nil, nil)

	sp := a.system
	if !strings.Contains(sp, "User profile") {
		t.Error("system prompt should label the user-profile section 'User profile'")
	}
	if !strings.Contains(sp, "Wants terse answers") {
		t.Errorf("system prompt should include USER.md content; got:\n%s", sp)
	}
}

func TestMemoryFileForScopes(t *testing.T) {
	gdir := withGlobalConfigDir(t)
	wd := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: wd}, nil, tools.NewRegistry(wd), nil, nil)

	proj, _ := a.memoryFileFor(ScopeProject)
	glob, _ := a.memoryFileFor(ScopeGlobal)
	user, _ := a.memoryFileFor(ScopeUser)
	if proj != filepath.Join(wd, "AGENTS.md") {
		t.Errorf("project path = %q", proj)
	}
	if glob != filepath.Join(gdir, "AGENTS.md") {
		t.Errorf("global path = %q", glob)
	}
	if user != filepath.Join(gdir, config.UserProfileFilename) {
		t.Errorf("user path = %q", user)
	}
}

// The context breakdown strips userProfilePrompt()+agentsMDPrompt() off the end of
// the system prompt to size the "System prompt" segment. If that stripping breaks,
// the user profile gets counted twice and the breakdown silently misreports.
func TestContextStatsSeparatesUserProfileFromSystem(t *testing.T) {
	gdir := withGlobalConfigDir(t)
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	note := "- Wants terse answers with no preamble whatsoever, always\n"
	if err := os.WriteFile(filepath.Join(gdir, config.UserProfileFilename),
		[]byte("## Style\n"+strings.Repeat(note, 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	wd := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: wd}, nil, tools.NewRegistry(wd), nil, nil)

	var profile, system *ContextSegment
	for i, seg := range a.ContextBreakdown().Segments {
		switch seg.Label {
		case "User profile":
			profile = &a.ContextBreakdown().Segments[i]
		case "System":
			system = &a.ContextBreakdown().Segments[i]
		}
	}
	if profile == nil {
		t.Fatal("no 'User profile' segment in the context breakdown")
	}
	if profile.Tokens <= 0 {
		t.Errorf("user profile segment = %d tokens, want > 0", profile.Tokens)
	}
	if system == nil {
		t.Fatal("no System segment in the context breakdown")
	}
	// The profile is big here, so double-counting it would show up as the system
	// segment being at least as large as the whole prompt.
	if system.Tokens >= len(a.system)/4 {
		t.Errorf("system segment (%d) still includes the appended profile/memory files (whole prompt ≈ %d tokens)",
			system.Tokens, len(a.system)/4)
	}
}

// recall is registered against a single sink, so its note list must span all three
// scopes — otherwise a remember_user note is written but never findable.
func TestRecallSeesAllMemoryScopes(t *testing.T) {
	gdir := withGlobalConfigDir(t)
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	wd := t.TempDir()
	a := New(&config.Config{Model: "test", WorkingDir: wd}, nil, tools.NewRegistry(wd), nil, nil)

	if _, err := a.Remember("uses make test to verify", "convention", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Remember("never force-push to main", "rule", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RememberUser("prefers terse answers with no preamble", "style"); err != nil {
		t.Fatal(err)
	}

	var joined string
	for _, n := range a.allMemoryNotes() {
		joined += n.Section + "|" + n.Text + "\n"
	}
	for _, want := range []string{"make test", "force-push", "terse answers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("recall notes missing %q; got:\n%s", want, joined)
		}
	}
	// The user note must be tagged as user scope and filed under Style.
	if !strings.Contains(joined, "user · Style|prefers terse answers with no preamble") {
		t.Errorf("user note not tagged/filed as expected; got:\n%s", joined)
	}
}
