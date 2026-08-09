package dap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Proves Spawn really speaks DAP to a child process, over BOTH transports.
func TestSpawnAgainstRealAdapter(t *testing.T) {
	adapter := writeFakeAdapter(t)
	for _, tc := range []struct{ name, cmd string }{
		{"stdio", "python3 " + adapter},
		{"socket", "python3 " + adapter + " --listen=127.0.0.1:47119"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Spawn(context.Background(), SpawnOptions{Command: tc.cmd})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			defer s.Close()
			// The handshake already succeeded; confirm the session is usable.
			resp, err := s.Send("threads", nil, 5e9)
			if err != nil {
				t.Fatalf("Send after spawn: %v", err)
			}
			if !resp.Success {
				t.Errorf("threads failed: %s", resp.Message)
			}
			t.Logf("%s transport OK", tc.name)
		})
	}
}

// Close must terminate the adapter: a debugging session that leaks a daemon per
// invocation would quietly accumulate processes.
func TestSpawnClosesAdapterProcess(t *testing.T) {
	adapter := writeFakeAdapter(t)
	before := countAdapters(t, adapter)
	s, err := Spawn(context.Background(), SpawnOptions{Command: "python3 " + adapter})
	if err != nil {
		t.Fatal(err)
	}
	if during := countAdapters(t, adapter); during <= before {
		t.Fatalf("adapter did not start (before=%d during=%d)", before, during)
	}
	if err := s.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	// Give the process a moment to die.
	for i := 0; i < 40; i++ {
		if countAdapters(t, adapter) <= before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("adapter process still running after Close (leaked)")
}

func countAdapters(t *testing.T, adapter string) int {
	t.Helper()
	// The bracket trick keeps the grep itself out of the match.
	base := filepath.Base(adapter)
	pattern := "[" + base[:1] + "]" + base[1:]
	out, _ := exec.Command("sh", "-c", "ps ax | grep -c '"+pattern+"'").Output()
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// writeFakeAdapter drops a minimal DAP-speaking script into a temp dir. Using a
// real child process (rather than an in-process pipe) is the point: it exercises
// the actual spawn, handshake and teardown path.
func writeFakeAdapter(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dapstub.py")
	if err := os.WriteFile(path, []byte(fakeAdapterSource), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const fakeAdapterSource = `#!/usr/bin/env python3
"""A minimal DAP adapter: answers ` + "`" + `initialize` + "`" + `, then echoes success for anything.
Supports stdio (default) and --listen=HOST:PORT (socket mode)."""
import json, sys, socket, argparse

def serve(rd, wr):
    while True:
        line = rd.readline()
        if not line:
            return
        line = line.decode() if isinstance(line, bytes) else line
        if not line.strip().lower().startswith("content-length"):
            continue
        n = int(line.split(":")[1].strip())
        rd.readline()  # blank separator
        body = rd.read(n)
        if isinstance(body, bytes):
            body = body.decode()
        msg = json.loads(body)
        resp = {"seq": 0, "type": "response", "request_seq": msg.get("seq", 0),
                "success": True, "command": msg.get("command", ""), "body": {}}
        out = json.dumps(resp)
        payload = f"Content-Length: {len(out)}\r\n\r\n{out}"
        if hasattr(wr, "sendall"):
            wr.sendall(payload.encode())
        else:
            wr.write(payload); wr.flush()

ap = argparse.ArgumentParser()
ap.add_argument("--listen", default="")
a = ap.parse_args()
if a.listen:
    host, port = a.listen.rsplit(":", 1)
    srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((host, int(port))); srv.listen(1)
    conn, _ = srv.accept()
    serve(conn.makefile("rb"), conn)
else:
    serve(sys.stdin.buffer, sys.stdout)
`
