package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/dap"
)

// dap_start must be registered alongside the other DAP tools.
func TestDAPStartIsRegistered(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDAP(&dap.SessionHolder{})

	var found bool
	for _, n := range r.Names() {
		if n == "dap_start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dap_start is not registered; got %v", r.Names())
	}
	// Starting an adapter runs an arbitrary command, so it must be gated by
	// approval exactly as bash is.
	if !r.Mutates("dap_start") {
		t.Error("dap_start must be Mutates()==true so it goes through approval")
	}
}

func TestDAPStartRequiresCommand(t *testing.T) {
	r := NewRegistry(t.TempDir())
	tool := &dapStartTool{r: r, holder: &dap.SessionHolder{}}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("an empty command must be rejected")
	}
}

// End-to-end through the tool: start a real adapter process and confirm the
// session is live afterwards.
func TestDAPStartEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "dapstub.py")
	if err := os.WriteFile(script, []byte(stubAdapter), 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(dir)
	holder := &dap.SessionHolder{}
	tool := &dapStartTool{r: r, holder: holder}

	in, _ := json.Marshal(map[string]any{"command": "python3 " + script})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("dap_start: %v", err)
	}
	if !strings.Contains(out, "Started debug adapter") {
		t.Errorf("unexpected output: %s", out)
	}
	sess, err := holder.Get()
	if err != nil || sess == nil {
		t.Fatalf("no session after dap_start: %v", err)
	}
	defer sess.Close()

	// The session must be usable, not merely constructed.
	resp, err := sess.Send("threads", nil, 5e9)
	if err != nil {
		t.Fatalf("send after dap_start: %v", err)
	}
	if !resp.Success {
		t.Error("threads request failed")
	}
}

const stubAdapter = `#!/usr/bin/env python3
import json, sys
def serve(rd, wr):
    while True:
        line = rd.readline()
        if not line: return
        line = line.decode() if isinstance(line, bytes) else line
        if not line.strip().lower().startswith("content-length"): continue
        n = int(line.split(":")[1].strip()); rd.readline()
        body = rd.read(n)
        if isinstance(body, bytes): body = body.decode()
        msg = json.loads(body)
        resp = {"seq":0,"type":"response","request_seq":msg.get("seq",0),
                "success":True,"command":msg.get("command",""),"body":{}}
        out = json.dumps(resp)
        wr.write(f"Content-Length: {len(out)}\r\n\r\n{out}"); wr.flush()
serve(sys.stdin.buffer, sys.stdout)
`
