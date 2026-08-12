package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunningTargetHelper is not a test: it is the process that keeps the update
// target executable *running* while apply() tries to replace it.
func TestRunningTargetHelper(t *testing.T) {
	if os.Getenv("MOONY_RUNNING_TARGET_HELPER") != "1" {
		t.Skip("helper process, not a test")
	}
	time.Sleep(60 * time.Second)
}

// The existing apply() tests write a plain file as the target, which no OS has any
// reason to protect. The real target is the *running* moony, and Windows refuses to
// let a running image be overwritten: apply() ended in os.Rename(staged, exePath),
// which failed there with "Access is denied" after the download and checksum had
// already succeeded. That is invisible to a test whose target is an inert file, so
// this one runs the target first.
func TestApplyReplacesARunningExecutable(t *testing.T) {
	content := "NEW BINARY"
	sum := sha256.Sum256([]byte(content))
	asset := AssetName()
	serve(t, map[string]string{
		"/download/" + asset:   content,
		"/download/SHA256SUMS": hex.EncodeToString(sum[:]) + "  " + asset + "\n",
	})

	// A real, runnable executable to stand in for the installed moony: this test
	// binary, copied so the copy can be replaced.
	dir := t.TempDir()
	exe := filepath.Join(dir, "target")
	if _, err := os.Stat(os.Args[0] + ".exe"); err == nil {
		exe += ".exe"
	} else if strings.HasSuffix(strings.ToLower(os.Args[0]), ".exe") {
		exe += ".exe"
	}
	copyExe(t, exe, os.Args[0])

	cmd := exec.Command(exe, "-test.run=TestRunningTargetHelper", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "MOONY_RUNNING_TARGET_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not run the target: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	time.Sleep(500 * time.Millisecond) // let the image actually map

	if err := apply(context.Background(), exe); err != nil {
		t.Fatalf("apply over a running executable: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("installed binary = %q, want %q", got, content)
	}

	// The staging file must not be left behind next to the executable.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".moony-update*")); len(leftovers) > 0 {
		t.Errorf("staging file left behind: %v", leftovers)
	}

	// Windows parks the outgoing image beside the new one because a live image
	// cannot delete itself; CleanupStaleBackups must remove it once it exits.
	parked, _ := filepath.Glob(exe + backupSuffix + "*")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	CleanupStaleBackups(exe)
	for _, p := range parked {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("parked image %s survived CleanupStaleBackups", p)
		}
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("cleanup removed the installed binary: %v", err)
	}
}

func copyExe(t *testing.T, dst, src string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
