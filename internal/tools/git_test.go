package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitRepo creates a temp git repo with one empty commit and a local identity.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitExec(t, dir, "init", "-q")
	gitExec(t, dir, "config", "user.email", "t@example.com")
	gitExec(t, dir, "config", "user.name", "Test")
	gitExec(t, dir, "config", "commit.gpgsign", "false")
	gitExec(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestGitInfo(t *testing.T) {
	dir := gitRepo(t)
	branch, dirty, ok := GitInfo(dir)
	if !ok || branch == "" || dirty != 0 {
		t.Fatalf("clean repo GitInfo = (%q, %d, %v), want a branch, 0 dirty, ok", branch, dirty, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, dirty, _ := GitInfo(dir); dirty == 0 {
		t.Error("expected dirty > 0 after writing a file")
	}
	// Outside a repo: not a repo.
	if _, _, ok := GitInfo(t.TempDir()); ok {
		t.Error("a non-repo dir should report isRepo=false")
	}
}

// TestGitOverviewFor covers the read-only git modal data: changes (with status
// codes), recent commits, working-tree + commit diffs, and the non-repo case.
func TestGitOverviewFor(t *testing.T) {
	dir := gitRepo(t)
	// Commit a tracked file, then modify it and add an untracked one.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "tracked.txt")
	gitExec(t, dir, "commit", "-q", "-m", "add tracked")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ov := GitOverviewFor(dir, 100000)
	if !ov.IsRepo || ov.Branch == "" {
		t.Fatalf("overview: isRepo=%v branch=%q", ov.IsRepo, ov.Branch)
	}
	var sawMod, sawUntracked bool
	for _, c := range ov.Changes {
		if c.Path == "tracked.txt" && c.Label == "Modified" {
			sawMod = true
		}
		if c.Path == "new.txt" && c.Status == "??" {
			sawUntracked = true
		}
	}
	if !sawMod || !sawUntracked {
		t.Errorf("changes missing modified/untracked entries: %+v", ov.Changes)
	}
	if len(ov.Commits) < 2 || ov.Commits[0].Subject != "add tracked" {
		t.Errorf("recent commits wrong: %+v", ov.Commits)
	}
	// Working-tree diff of the modified file shows the change.
	if d, err := GitDiffFor(dir, false, "tracked.txt", 100000); err != nil || !strings.Contains(d, "+v2") {
		t.Errorf("GitDiffFor tracked.txt = %q, err=%v", d, err)
	}
	// Commit diff for the most recent commit.
	if d, err := GitCommitDiffFor(dir, ov.Commits[0].Hash, 100000); err != nil || !strings.Contains(d, "tracked.txt") {
		t.Errorf("GitCommitDiffFor = %q, err=%v", d, err)
	}
	// A bad hash is rejected (no arg injection).
	if _, err := GitCommitDiffFor(dir, "; rm -rf /", 1000); err == nil {
		t.Error("GitCommitDiffFor should reject a non-hex hash")
	}
	// Non-repo overview.
	if ov := GitOverviewFor(t.TempDir(), 1000); ov.IsRepo {
		t.Error("non-repo overview should have isRepo=false")
	}
}

func TestGitTools(t *testing.T) {
	dir := gitRepo(t)
	reg := NewRegistry(dir)
	ctx := context.Background()
	const max = 1 << 20

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// git_status surfaces the new file.
	st, err := (&gitStatusTool{r: reg, max: max}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(st, "a.txt") {
		t.Fatalf("git_status = %q, err=%v", st, err)
	}

	// Stage it, then commit via the tool (only staged changes).
	gitExec(t, dir, "add", "a.txt")
	if _, err := (&gitCommitTool{r: reg, max: max}).Execute(ctx, json.RawMessage(`{"message":"add a.txt"}`)); err != nil {
		t.Fatalf("git_commit: %v", err)
	}
	if _, dirty, _ := GitInfo(dir); dirty != 0 {
		t.Errorf("expected a clean tree after commit, got dirty=%d", dirty)
	}

	// git_log shows the commit.
	lg, err := (&gitLogTool{r: reg, max: max}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(lg, "add a.txt") {
		t.Fatalf("git_log = %q, err=%v", lg, err)
	}

	// git_commit requires a message.
	if _, err := (&gitCommitTool{r: reg, max: max}).Execute(ctx, json.RawMessage(`{"message":""}`)); err == nil {
		t.Error("git_commit should reject an empty message")
	}

	// git_branch creates and switches.
	if _, err := (&gitBranchTool{r: reg}).Execute(ctx, json.RawMessage(`{"name":"feature","create":true}`)); err != nil {
		t.Fatalf("git_branch: %v", err)
	}
	if br, _, _ := GitInfo(dir); br != "feature" {
		t.Errorf("branch = %q, want feature", br)
	}

	// Read tools don't mutate; commit/branch do.
	if (&gitStatusTool{}).Mutates() || (&gitDiffTool{}).Mutates() || (&gitLogTool{}).Mutates() {
		t.Error("read git tools must not be classified as mutating")
	}
	if !(&gitCommitTool{}).Mutates() || !(&gitBranchTool{}).Mutates() {
		t.Error("git_commit and git_branch must be classified as mutating")
	}
}

// TestGitWriteOps covers Phase 2 light writes: stage, unstage, discard (tracked
// revert + untracked delete), and the path guard that prevents whole-tree ops.
func TestGitWriteOps(t *testing.T) {
	dir := gitRepo(t)
	// Commit a file so we can modify+discard it.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "f.txt")
	gitExec(t, dir, "commit", "-q", "-m", "add f")

	// Modify tracked + create untracked.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage then unstage f.txt.
	if err := GitStage(dir, "f.txt"); err != nil {
		t.Fatalf("GitStage: %v", err)
	}
	if c := changeFor(GitOverviewFor(dir, 1000), "f.txt"); c == nil || !c.Staged {
		t.Errorf("f.txt not staged: %+v", c)
	}
	if err := GitUnstage(dir, "f.txt"); err != nil {
		t.Fatalf("GitUnstage: %v", err)
	}
	if c := changeFor(GitOverviewFor(dir, 1000), "f.txt"); c == nil || c.Staged {
		t.Errorf("f.txt still staged after unstage: %+v", c)
	}

	// Discard tracked modification → reverts to v1.
	if err := GitDiscard(dir, "f.txt"); err != nil {
		t.Fatalf("GitDiscard tracked: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(got) != "v1\n" {
		t.Errorf("discard didn't revert f.txt, got %q", got)
	}
	// Discard untracked → deletes the file.
	if err := GitDiscard(dir, "u.txt"); err != nil {
		t.Fatalf("GitDiscard untracked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "u.txt")); !os.IsNotExist(err) {
		t.Errorf("discard didn't delete untracked u.txt (stat err=%v)", err)
	}

	// Path guard: "." / "" / abs / escape are rejected (no whole-tree clean).
	for _, bad := range []string{"", ".", "/etc/passwd", "..", "../x", "a/../../x"} {
		if err := GitDiscard(dir, bad); err == nil {
			t.Errorf("GitDiscard(%q) should be rejected by the path guard", bad)
		}
	}
}

// changeFor returns the change entry for path in an overview, or nil.
func changeFor(ov GitOverview, path string) *GitChange {
	for i := range ov.Changes {
		if ov.Changes[i].Path == path {
			return &ov.Changes[i]
		}
	}
	return nil
}

// TestGitCommitPush covers Phase 3 publish ops: commit (staged + stage-all),
// the empty-message guard, hasRemote detection, and push to a local bare remote
// (including first-push upstream creation and ahead/behind bookkeeping).
func TestGitCommitPush(t *testing.T) {
	dir := gitRepo(t)

	// No remote yet.
	if gitHasRemote(dir) {
		t.Error("fresh repo should have no remote")
	}
	if ov := GitOverviewFor(dir, 1000); ov.HasRemote {
		t.Error("overview HasRemote should be false without a remote")
	}

	// Empty message is rejected.
	if _, err := GitCommitChanges(dir, "   ", false); err == nil {
		t.Error("GitCommitChanges should reject an empty message")
	}

	// Stage-all commit: modify a tracked file, commit with all=true (no manual add).
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "f.txt")
	gitExec(t, dir, "commit", "-q", "-m", "add f")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GitCommitChanges(dir, "edit f", true); err != nil {
		t.Fatalf("GitCommitChanges all=true: %v", err)
	}
	if _, dirty, _ := GitInfo(dir); dirty != 0 {
		t.Errorf("tree not clean after stage-all commit, dirty=%d", dirty)
	}

	// Staged-only commit: stage a new file, commit with all=false.
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "g.txt")
	if _, err := GitCommitChanges(dir, "add g", false); err != nil {
		t.Fatalf("GitCommitChanges all=false: %v", err)
	}

	// Push with no remote configured is rejected.
	if _, err := GitPush(dir); err == nil {
		t.Error("GitPush should fail with no remote configured")
	}

	// Add a local bare remote and push (first push creates upstream).
	remote := t.TempDir()
	gitExec(t, remote, "init", "-q", "--bare")
	gitExec(t, dir, "remote", "add", "origin", remote)
	if !gitHasRemote(dir) {
		t.Fatal("gitHasRemote should be true after adding origin")
	}
	if _, err := GitPush(dir); err != nil {
		t.Fatalf("GitPush first push: %v", err)
	}
	// After push, upstream is set and we're not ahead.
	ahead, _, upstream, ok := GitAheadBehind(dir)
	if !ok || upstream == "" {
		t.Errorf("expected an upstream after push, ok=%v upstream=%q", ok, upstream)
	}
	if ahead != 0 {
		t.Errorf("expected ahead=0 right after push, got %d", ahead)
	}

	// A new commit makes us ahead by one; a second push clears it.
	if err := os.WriteFile(filepath.Join(dir, "h.txt"), []byte("h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "h.txt")
	gitExec(t, dir, "commit", "-q", "-m", "add h")
	if ahead, _, _, _ := GitAheadBehind(dir); ahead != 1 {
		t.Errorf("expected ahead=1 before second push, got %d", ahead)
	}
	if _, err := GitPush(dir); err != nil {
		t.Fatalf("GitPush second push: %v", err)
	}
	if ahead, _, _, _ := GitAheadBehind(dir); ahead != 0 {
		t.Errorf("expected ahead=0 after second push, got %d", ahead)
	}
}
