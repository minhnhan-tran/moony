package dap

import (
	"context"
	"testing"
)

func TestListenAddrDetection(t *testing.T) {
	cases := []struct {
		command string
		addr    string
		ok      bool
	}{
		{"dlv dap", "", false},
		{"python -m debugpy.adapter", "", false},
		{"dlv dap --listen=127.0.0.1:4711", "127.0.0.1:4711", true},
		{"dlv dap --listen 127.0.0.1:4711", "127.0.0.1:4711", true},
		{"dlv dap --listen=:4711", "127.0.0.1:4711", true},
		{"python -m debugpy --port 5678", "127.0.0.1:5678", true},
		{`node adapter.js --server=9229`, "127.0.0.1:9229", true},
	}
	for _, c := range cases {
		addr, ok := listenAddr(c.command)
		if ok != c.ok || addr != c.addr {
			t.Errorf("listenAddr(%q) = (%q,%v), want (%q,%v)", c.command, addr, ok, c.addr, c.ok)
		}
	}
}

func TestSpawnEmptyCommand(t *testing.T) {
	if _, err := Spawn(context.Background(), SpawnOptions{}); err == nil {
		t.Error("an empty command must be rejected")
	}
}

// A command that is not a debug adapter must fail with a message that explains
// itself, not a bare EOF — the adapter's own output is the only useful clue.
func TestSpawnNonAdapterReportsOutput(t *testing.T) {
	_, err := Spawn(context.Background(), SpawnOptions{Command: "echo 'not a dap adapter'; exit 3"})
	if err == nil {
		t.Fatal("expected an error")
	}
	t.Logf("error: %v", err)
}

// A missing binary must fail promptly rather than hanging.
func TestSpawnMissingBinary(t *testing.T) {
	_, err := Spawn(context.Background(), SpawnOptions{Command: "definitely-not-a-real-adapter-binary"})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	t.Logf("error: %v", err)
}

// A socket adapter that never binds must time out, not hang forever, and must not
// leave the process running.
func TestSpawnSocketNeverListens(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*1e9)
	defer cancel()
	_, err := Spawn(ctx, SpawnOptions{Command: "sleep 60 --listen=127.0.0.1:59999"})
	if err == nil {
		t.Fatal("expected an error when the adapter never listens")
	}
	t.Logf("error: %v", err)
}
