package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSandboxDisabledIsPassthrough(t *testing.T) {
	name, args := sandboxCommand(sandboxSetting{enabled: false, network: true}, []string{"/work"}, "sh", "-c", "echo hi")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" {
		t.Fatalf("disabled sandbox changed the command: %s %v", name, args)
	}
}

func TestSandboxProfileContent(t *testing.T) {
	p := sandboxProfile([]string{"/work/dir"}, true) // network allowed
	for _, want := range []string{"(version 1)", "(allow default)", "(deny file-write*)", "(allow file-write*", `(subpath "/work/dir")`} {
		if !strings.Contains(p, want) {
			t.Errorf("profile missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "(deny network*)") {
		t.Errorf("network should be allowed:\n%s", p)
	}
	if p := sandboxProfile([]string{"/work/dir"}, false); !strings.Contains(p, "(deny network*)") {
		t.Errorf("network=false should deny network:\n%s", p)
	}
	// Multi-root: every root is writable.
	if mp := sandboxProfile([]string{"/work/a", "/work/b"}, true); !strings.Contains(mp, `(subpath "/work/a")`) || !strings.Contains(mp, `(subpath "/work/b")`) {
		t.Errorf("multi-root profile must allow writes under every root:\n%s", mp)
	}
}

func TestSandboxConfigEnvFallback(t *testing.T) {
	r := NewRegistry(t.TempDir())
	t.Setenv("MOONY_SANDBOX", "on")
	t.Setenv("MOONY_SANDBOX_NET", "0")
	if got := r.sandboxConfig(); !got.enabled || got.network {
		t.Errorf("env fallback = %+v, want enabled w/o network", got)
	}
	// Explicit config overrides the environment.
	r.SetSandbox(false, true)
	if got := r.sandboxConfig(); got.enabled {
		t.Errorf("SetSandbox(false) should override env: %+v", got)
	}
}

func TestSandboxCommandWrapsWhenEnabled(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	sbexec, err := exec.LookPath("sandbox-exec")
	if err != nil {
		t.Skip("sandbox-exec not available")
	}
	name, args := sandboxCommand(sandboxSetting{enabled: true, network: true}, []string{"/work"}, "sh", "-c", "echo hi")
	if name != sbexec {
		t.Fatalf("name = %q, want %q", name, sbexec)
	}
	if len(args) != 5 || args[0] != "-p" || args[2] != "sh" || args[3] != "-c" || args[4] != "echo hi" {
		t.Fatalf("unexpected wrapped args: %v", args)
	}
	if !strings.Contains(args[1], "(deny file-write*)") {
		t.Errorf("profile arg missing write denial: %q", args[1])
	}
}

func TestBwrapArgsContent(t *testing.T) {
	args := bwrapArgs([]string{"/work/dir"}, true) // network allowed
	for _, want := range []string{"--ro-bind", "/", "--die-with-parent", "--tmpfs", "/dev/shm"} {
		if !contains(args, want) {
			t.Errorf("bwrap args missing %q: %v", want, args)
		}
	}
	if contains(args, "--unshare-net") {
		t.Errorf("network should be allowed: %v", args)
	}
	if a := bwrapArgs([]string{"/work/dir"}, false); !contains(a, "--unshare-net") {
		t.Errorf("network=false should unshare net: %v", a)
	}
	// A workspace root that exists gets bind-mounted read-write over the ro-bind.
	work := t.TempDir()
	a := bwrapArgs([]string{work}, true)
	found := false
	for i := 0; i < len(a)-2; i++ {
		if a[i] == "--bind" && a[i+1] == canonical(work) && a[i+2] == canonical(work) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --bind %s %s in %v", work, work, a)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestSandboxCommandLinuxWrapsWhenEnabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is Linux-only")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not available")
	}
	name, args := sandboxCommandLinux(sandboxSetting{enabled: true, network: true}, []string{"/work"}, "sh", "-c", "echo hi")
	if name != bwrap {
		t.Fatalf("name = %q, want %q", name, bwrap)
	}
	if len(args) < 3 || args[len(args)-3] != "sh" || args[len(args)-2] != "-c" || args[len(args)-1] != "echo hi" {
		t.Fatalf("unexpected wrapped args: %v", args)
	}
}

// TestSandboxConfinesWrites actually runs a command through the bash tool under
// the sandbox and verifies writes in the working dir succeed while writes
// outside the allowlist are blocked. It self-skips when sandbox-exec can't run
// in the environment (e.g. a nested sandbox) or no writable out-of-tree path
// exists to probe.
func TestSandboxConfinesWrites(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	if err := exec.Command("sandbox-exec", "-p", "(version 1)(allow default)", "sh", "-c", "true").Run(); err != nil {
		t.Skipf("sandbox-exec cannot run in this environment: %v", err)
	}

	work := t.TempDir()
	reg := NewRegistry(work)
	reg.SetSandbox(true, true)
	bt := &bashTool{r: reg, maxOutputBytes: 1 << 20}

	// Allowed: writing inside the working directory.
	if _, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"echo hi > ok.txt"}`)); err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "ok.txt")); err != nil {
		t.Errorf("expected ok.txt written inside the working dir: %v", err)
	}

	// Denied: a world-writable path outside the allowlist. Skip if we can't
	// write there without the sandbox (so the assertion is meaningful).
	probe := filepath.Join("/Users/Shared", "sqe_sandbox_probe.txt")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Skipf("no writable out-of-tree path to probe denial: %v", err)
	}
	os.Remove(probe)

	cmd := `echo blocked > ` + probe
	if _, err := bt.Execute(context.Background(), json.RawMessage(`{"command":`+strconv.Quote(cmd)+`}`)); err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	if _, err := os.Stat(probe); err == nil {
		os.Remove(probe)
		t.Error("sandbox did not block a write outside the working directory")
	}
}
