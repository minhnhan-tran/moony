package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A background job belongs to the project, so every registry working in that project —
// i.e. every session, and every sub-agent — must see the same jobs. Before this, each
// registry made its own store and a job was reachable only from the session that
// started it; evicting that session left the child process running with nothing left
// that could list or stop it.
func TestJobStoreIsSharedPerProject(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	a, b := NewRegistry(root), NewRegistry(root)
	if a.Jobs() != b.Jobs() {
		t.Fatal("two registries in the same project got different job stores")
	}

	other := NewRegistry(t.TempDir())
	if other.Jobs() == a.Jobs() {
		t.Error("registries in different projects share a job store — one project would see another's jobs")
	}

	// A relative path, a trailing separator and (on Windows) a different case must all
	// land on the same store, or "the same project" quietly becomes two.
	if same := NewRegistry(root + string(filepath.Separator)); same.Jobs() != a.Jobs() {
		t.Error("a trailing separator produced a second store for one project")
	}
	if runtime.GOOS == "windows" {
		if same := NewRegistry(strings.ToUpper(root)); same.Jobs() != a.Jobs() {
			t.Error("a differently-cased path produced a second store for one project on Windows")
		}
	}
}

// A store with no project root is private and unpersisted: there is nothing to attach
// it to, and sharing one global store between rootless registries would leak jobs
// between unrelated callers.
func TestRootlessRegistriesDoNotShareJobs(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	if NewRegistry("").Jobs() == NewRegistry("").Jobs() {
		t.Error("two rootless registries share a job store")
	}
}

// The history has to survive the process. A store built for the same project later
// reads back what ran, what it printed, and how it ended.
func TestJobHistoryIsPersistedAndRestored(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	store := JobStoreFor(root)

	j := &job{id: "bg_1", command: "npm run dev", started: time.Now().Add(-time.Minute), state: jobRunning}
	j.buf.WriteString("listening on :3000\n")
	store.add(j)
	j.setState(jobDone, 0, "")

	if _, err := os.Stat(filepath.Join(root, ".moony", jobsFileName)); err != nil {
		t.Fatalf("no history file written: %v", err)
	}

	// A fresh process: drop the cached store and load the project again.
	resetJobStores()
	back := JobStoreFor(root)
	jobs := back.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("restored %d jobs, want 1", len(jobs))
	}
	got := jobs[0]
	if got.ID != "bg_1" || got.Command != "npm run dev" {
		t.Errorf("restored the wrong job: %+v", got)
	}
	if got.Running {
		t.Error("a restored job must not claim to be running")
	}
	tail, ok := back.JobTail("bg_1", 0)
	if !ok {
		t.Fatal("JobTail could not find the restored job")
	}
	if !strings.Contains(tail.Output, "listening on :3000") {
		t.Errorf("the restored output tail is missing: %q", tail.Output)
	}

	// Ids must not be handed out twice.
	if id := back.newID(); id == "bg_1" {
		t.Error("a new job reused a restored id")
	}
}

// A job that was still running when the process exited did not survive it: its process
// was a child. It must come back as ended, with a reason, rather than as a live row
// offering a Stop button for a pid that is gone.
func TestRestoredRunningJobIsReportedAsEnded(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	store := JobStoreFor(root)
	j := &job{id: "bg_3", command: "tail -f log", started: time.Now(), state: jobRunning}
	store.add(j) // add() persists while the state is still "running"

	resetJobStores()
	jobs := JobStoreFor(root).Jobs()
	if len(jobs) != 1 {
		t.Fatalf("restored %d jobs, want 1", len(jobs))
	}
	if jobs[0].Running {
		t.Fatal("a job restored from a previous process is still marked running")
	}
	if jobs[0].Err == "" {
		t.Error("no explanation attached to a job the restart stopped")
	}
	// And it must be inert: nothing to stop, nothing to type into.
	if err := JobStoreFor(root).SendInput("bg_3", "hello", true, nil); err == nil {
		t.Error("SendInput to a restored job should fail, not write to a dead pty")
	}
}

// Long-dead jobs are dropped on load: opening a project should not resurrect a wall of
// rows from last week.
func TestOldJobsAreNotRestored(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	store := JobStoreFor(root)
	old := &job{id: "bg_1", command: "old", started: time.Now().Add(-72 * time.Hour), state: jobDone}
	old.ended = time.Now().Add(-48 * time.Hour)
	fresh := &job{id: "bg_2", command: "fresh", started: time.Now().Add(-time.Hour), state: jobDone}
	fresh.ended = time.Now().Add(-time.Minute)
	store.add(old)
	store.add(fresh)

	resetJobStores()
	jobs := JobStoreFor(root).Jobs()
	if len(jobs) != 1 || jobs[0].ID != "bg_2" {
		t.Fatalf("expected only the recent job to be restored, got %+v", jobs)
	}
}

// Registry.Close may only kill the jobs that registry started. The store is shared, so
// closing one session's registry (or a sub-agent's, which gets its own) must not stop
// another session's dev server.
func TestRegistryCloseLeavesOtherRegistriesJobsAlone(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	mine, theirs := NewRegistry(root), NewRegistry(root)

	ours := &job{id: "bg_1", command: "mine", started: time.Now(), state: jobRunning}
	ours.cancel = func() { ours.setState(jobKilled, -1, "killed") }
	other := &job{id: "bg_2", command: "theirs", started: time.Now(), state: jobRunning}
	other.cancel = func() { other.setState(jobKilled, -1, "killed") }
	mine.Jobs().add(ours)
	mine.Jobs().add(other)
	mine.noteOwnedJob("bg_1")
	theirs.noteOwnedJob("bg_2")

	mine.Close()

	if state := jobStateOf(ours); state != jobKilled {
		t.Errorf("the closing registry's own job was left running (state %q)", state)
	}
	if state := jobStateOf(other); state != jobRunning {
		t.Errorf("another registry's job was killed by an unrelated Close (state %q)", state)
	}
}

// The process-exit reaper is the one thing that may kill everything: these children
// have no other owner.
func TestKillAllProjectJobsReapsEveryProject(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	var killed []string
	for _, root := range []string{t.TempDir(), t.TempDir()} {
		store := JobStoreFor(root)
		id := "bg_1"
		j := &job{id: id, command: "dev " + root, started: time.Now(), state: jobRunning}
		name := root
		j.cancel = func() { killed = append(killed, name); j.setState(jobKilled, -1, "killed") }
		store.add(j)
	}

	KillAllProjectJobs()

	if len(killed) != 2 {
		t.Fatalf("reaped %d jobs across projects, want 2", len(killed))
	}
}

func jobStateOf(j *job) jobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// resetJobStores drops every cached store. Test-only, and defined here so it is not
// compiled into the shipped binary: it exists so one test's project root cannot hand its
// jobs to the next test that happens to use the same directory.
func resetJobStores() {
	jobStoresMu.Lock()
	jobStores = map[string]*JobStore{}
	jobStoresMu.Unlock()
}

// The store used to die with its registry; it now lives as long as the process and is
// shared by every session of the project, so it has to forget old runs or a long-lived
// daemon accumulates every job it ever ran, each holding its output buffer.
func TestFinishedJobsAreTrimmedButRunningOnesAreKept(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	store := JobStoreFor(t.TempDir())
	live := &job{id: "bg_1", command: "dev server", started: time.Now(), state: jobRunning}
	store.add(live)
	for i := 2; i <= maxPersistedJobs+20; i++ {
		j := &job{id: "bg_" + strconv.Itoa(i), command: "one off", started: time.Now(), state: jobDone}
		store.add(j)
	}

	jobs := store.Jobs()
	if len(jobs) > maxPersistedJobs {
		t.Errorf("store holds %d jobs, want at most %d", len(jobs), maxPersistedJobs)
	}
	// The running job must never be dropped: the store is the only thing that can stop it.
	found := false
	for _, j := range jobs {
		if j.ID == "bg_1" {
			found = true
		}
	}
	if !found {
		t.Error("the running job was trimmed away — it would keep running with nothing able to stop it")
	}
}

// Every job's reaper calls save() when it exits, so two jobs finishing at once race on
// the history file. With a shared temp name they interleave and a reader can see a
// truncated file; this drives that path hard and insists the result stays parseable.
func TestConcurrentSavesLeaveAValidHistory(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	store := JobStoreFor(root)
	for i := 1; i <= 8; i++ {
		store.add(&job{id: "bg_" + strconv.Itoa(i), command: "job", started: time.Now(), state: jobRunning})
	}

	var wg sync.WaitGroup
	for _, j := range store.list() {
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				j.setState(jobDone, 0, "")
			}
		}(j)
	}
	wg.Wait()

	blob, err := os.ReadFile(filepath.Join(root, ".moony", jobsFileName))
	if err != nil {
		t.Fatalf("history unreadable: %v", err)
	}
	var file jobsFile
	if err := json.Unmarshal(blob, &file); err != nil {
		t.Fatalf("history is not valid JSON after concurrent saves: %v\n%s", err, blob)
	}
	if len(file.Jobs) != 8 {
		t.Errorf("history holds %d jobs, want 8", len(file.Jobs))
	}
	// No temp files left behind.
	leftovers, _ := filepath.Glob(filepath.Join(root, ".moony", "jobs-*.json"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left in the project state dir: %v", leftovers)
	}
}

// The history file records command lines and output tails, so it must not be world
// readable, and .moony must be ignored by git.
func TestHistoryFileIsPrivateAndGitIgnored(t *testing.T) {
	resetJobStores()
	t.Cleanup(resetJobStores)

	root := t.TempDir()
	store := JobStoreFor(root)
	store.add(&job{id: "bg_1", command: "deploy --token hunter2", started: time.Now(), state: jobDone})

	info, err := os.Stat(filepath.Join(root, ".moony", jobsFileName))
	if err != nil {
		t.Fatalf("no history file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("history file mode is %v, want no group/other access", info.Mode().Perm())
	}

	ignore, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(ignore), "/.moony/") {
		t.Error(".moony/ is not gitignored — job command lines and output could be committed")
	}
}
