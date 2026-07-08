package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// the browser tool must satisfy both the base Tool interface and ImageProducer
// (so screenshots reach the model).
var (
	_ Tool          = (*browserTool)(nil)
	_ ImageProducer = (*browserTool)(nil)
)

func browserExec(t *testing.T, bt *browserTool, in string) (string, []Image, error) {
	t.Helper()
	return bt.ExecuteImages(context.Background(), json.RawMessage(in))
}

// TestBrowserInputValidation checks that bad input is rejected before any
// attempt to launch Chrome, so the tool is safe to exercise without a browser.
func TestBrowserInputValidation(t *testing.T) {
	bt := &browserTool{r: NewRegistry(t.TempDir())}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"missing action", `{}`, "action is required"},
		{"unknown action", `{"action":"fly"}`, "unknown browser action"},
		{"navigate no url", `{"action":"navigate"}`, "navigate requires a url"},
		{"click no selector", `{"action":"click"}`, "click requires a selector"},
		{"type no selector", `{"action":"type","text":"hi"}`, "type requires a selector"},
		{"type no text", `{"action":"type","selector":"#x"}`, "type requires text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := browserExec(t, bt, c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
	if bt.started {
		t.Error("validation errors must not launch Chrome")
	}
}

// TestBrowserReadConsoleEmpty checks read_console works (and doesn't launch
// Chrome) before any navigation.
func TestBrowserReadConsoleEmpty(t *testing.T) {
	bt := &browserTool{r: NewRegistry(t.TempDir())}
	out, imgs, err := browserExec(t, bt, `{"action":"read_console"}`)
	if err != nil {
		t.Fatal(err)
	}
	if imgs != nil {
		t.Errorf("read_console should not return images, got %d", len(imgs))
	}
	if !strings.Contains(out, "no console output") {
		t.Errorf("empty console = %q, want a 'no console output' note", out)
	}
	if bt.started {
		t.Error("read_console must not launch Chrome")
	}
}

// TestBrowserConsoleBuffer exercises the console buffer's append/take/cap logic
// directly (no browser needed).
func TestBrowserConsoleBuffer(t *testing.T) {
	bt := &browserTool{r: NewRegistry(t.TempDir())}
	bt.appendConsole("[log] hello")
	bt.appendConsole("[error] boom")
	bt.appendConsole("") // ignored
	got := bt.takeConsole()
	if !strings.Contains(got, "hello") || !strings.Contains(got, "boom") {
		t.Errorf("console = %q, want both messages", got)
	}
	if again := bt.takeConsole(); !strings.Contains(again, "no console output") {
		t.Errorf("console should be cleared after a read, got %q", again)
	}
	// Cap: never grows without bound.
	for i := 0; i < 700; i++ {
		bt.appendConsole("x")
	}
	bt.consoleMu.Lock()
	n := len(bt.console)
	bt.consoleMu.Unlock()
	if n > 500 {
		t.Errorf("console buffer = %d, want it capped at 500", n)
	}
}

// TestBrowserRegistered confirms the browser tool is in the default set and
// reaped by Close (its closer registration is the teardown path for Chrome).
func TestBrowserRegistered(t *testing.T) {
	r := Default(t.TempDir(), 1<<16)
	found := false
	for _, n := range r.Names() {
		if n == "browser" {
			found = true
		}
	}
	if !found {
		t.Fatal("browser tool not registered in the default set")
	}
	// Close must be safe even though the browser never started.
	r.Close()
}
