package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrateProjectDirFresh(t *testing.T) {
	dir := t.TempDir()
	migrated, both, err := MigrateProjectDir(dir)
	if err != nil || migrated || both {
		t.Fatalf("no legacy dir: got migrated=%v both=%v err=%v", migrated, both, err)
	}
}

func TestMigrateProjectDirRenames(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyProjectDir)
	if err := os.MkdirAll(filepath.Join(legacy, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "permissions.json"), []byte(`{"allow":["bash:ls"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	migrated, both, err := MigrateProjectDir(dir)
	if err != nil || !migrated || both {
		t.Fatalf("got migrated=%v both=%v err=%v", migrated, both, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectDir, "permissions.json")); err != nil {
		t.Errorf("permissions.json did not move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectDir, "agents")); err != nil {
		t.Errorf("agents/ did not move: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir should be gone after a rename")
	}
}

// Both present: refuse to merge. Unioning two permissions.json allow-lists would
// grant rules the user never approved.
func TestMigrateProjectDirBothPresentIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyProjectDir)
	newer := filepath.Join(dir, ProjectDir)
	for _, d := range []string{legacy, newer} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, "permissions.json"), []byte(`{"allow":["bash:legacy"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "permissions.json"), []byte(`{"allow":["bash:new"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	migrated, both, err := MigrateProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("must not migrate when both dirs exist")
	}
	if !both {
		t.Error("must report that both dirs exist")
	}
	got, _ := os.ReadFile(filepath.Join(newer, "permissions.json"))
	if string(got) != `{"allow":["bash:new"]}` {
		t.Errorf("the new dir was modified: %s", got)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Error("the legacy dir must be left in place, not deleted")
	}
}

// Reads fall back to the legacy dir so an un-migrated checkout keeps working.
func TestProjectPathFallsBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, LegacyProjectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	got := ProjectPath(dir, "permissions.json")
	want := filepath.Join(dir, LegacyProjectDir, "permissions.json")
	if got != want {
		t.Errorf("ProjectPath = %q, want the legacy path %q", got, want)
	}
	// Writes always target the current name.
	if wnew := ProjectPathNew(dir, "permissions.json"); wnew != filepath.Join(dir, ProjectDir, "permissions.json") {
		t.Errorf("ProjectPathNew = %q, want the new path", wnew)
	}
}

func TestProjectPathPrefersNewDir(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{LegacyProjectDir, ProjectDir} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := ProjectPath(dir, "mcp.json")
	if want := filepath.Join(dir, ProjectDir, "mcp.json"); got != want {
		t.Errorf("ProjectPath = %q, want %q (the new dir must win)", got, want)
	}
}

// The hazard that makes this more than a rename: git stores ABSOLUTE worktree
// paths, so moving the directory strands worktrees as "prunable" until repaired.
func TestMigrateRepairsGitWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	run("worktree", "add", "-q", filepath.Join(LegacyProjectDir, "worktrees", "wt"), "-b", "wtbranch")

	migrated, _, err := MigrateProjectDir(dir)
	if err != nil || !migrated {
		t.Fatalf("migrate: migrated=%v err=%v", migrated, err)
	}

	repaired := RepairWorktrees(dir)
	if len(repaired) != 1 {
		t.Fatalf("expected 1 repaired worktree, got %v", repaired)
	}

	// git must now track the NEW path, and not consider it prunable.
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list: %v\n%s", err, out)
	}
	listing := string(out)
	if !strings.Contains(listing, filepath.Join(ProjectDir, "worktrees", "wt")) {
		t.Errorf("git does not track the new worktree path:\n%s", listing)
	}
	if strings.Contains(listing, "prunable") {
		t.Errorf("the worktree is still prunable after repair:\n%s", listing)
	}
	if strings.Contains(listing, filepath.Join(LegacyProjectDir, "worktrees")) {
		t.Errorf("git still references the legacy path:\n%s", listing)
	}
}

// A repo with no worktrees, or no git, must not error.
func TestRepairWorktreesNoop(t *testing.T) {
	if got := RepairWorktrees(t.TempDir()); got != nil {
		t.Errorf("expected no repairs, got %v", got)
	}
	if got := RepairWorktrees(""); got != nil {
		t.Errorf("empty dir: expected nil, got %v", got)
	}
}

// The copy fallback (used when os.Rename fails, e.g. across devices) must produce a
// COMPLETE dir before anything can see it, and must preserve symlinks rather than
// dereferencing them. It stages into a temp dir and renames that into place, so a
// concurrent process never observes a half-populated config dir — which ProjectPath
// would immediately prefer over the intact legacy dir.
func TestCopyFallbackStagesAndPreservesSymlinks(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, LegacyProjectDir)
	if err := os.MkdirAll(filepath.Join(legacy, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "permissions.json"), []byte(`{"allow":["bash:ls"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "agents", "a.md"), []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("permissions.json", filepath.Join(legacy, "link.json")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	staging, err := os.MkdirTemp(base, "."+ProjectDir+".migrating-")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyDir(legacy, staging); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "agents", "a.md")); err != nil {
		t.Errorf("nested file not copied: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(staging, "link.json"))
	if err != nil {
		t.Fatalf("symlink not copied: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was dereferenced instead of recreated")
	}
	// Staging must be renameable into place (the atomic publish step).
	if err := os.Rename(staging, filepath.Join(base, ProjectDir)); err != nil {
		t.Fatalf("publishing the staged dir failed: %v", err)
	}
}

// Two processes starting in the same project must not corrupt it or double-report.
// Exactly one should observe migrated=true; the rest must see a clean no-op.
func TestMigrateProjectDirConcurrent(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyProjectDir)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "permissions.json"), []byte(`{"allow":["bash:ls"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			migrated, _, err := MigrateProjectDir(dir)
			results[i], errs[i] = migrated, err
		}(i)
	}
	close(start)
	wg.Wait()

	migrations := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if results[i] {
			migrations++
		}
	}
	if migrations != 1 {
		t.Errorf("expected exactly 1 migration, got %d", migrations)
	}
	// The data must be intact and in the new location exactly once.
	got, err := os.ReadFile(filepath.Join(dir, ProjectDir, "permissions.json"))
	if err != nil {
		t.Fatalf("config lost: %v", err)
	}
	if string(got) != `{"allow":["bash:ls"]}` {
		t.Errorf("config corrupted: %s", got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy dir should be gone")
	}
}
