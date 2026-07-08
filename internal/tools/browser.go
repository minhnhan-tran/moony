package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// browserTool drives a headless Chrome over the DevTools protocol for web-app
// debugging: navigate, click, type, screenshot, read the console, and read the
// page's text/HTML. One Chrome is launched lazily on first use and reused across
// calls, so navigation and page state persist between actions (one browser per
// session). It requires a Chrome/Chromium binary on the machine; without one,
// every action returns a clear install hint instead of failing obscurely.
//
// screenshot returns the PNG to the model as an image block (so it can actually
// see the page) and also writes it to a temp file the human can open.
type browserTool struct {
	r *Registry

	mu          sync.Mutex // serializes browser ops and guards the lifecycle below
	started     bool
	ctx         context.Context
	ctxCancel   context.CancelFunc
	allocCancel context.CancelFunc

	consoleMu sync.Mutex // guards console; held only by the async CDP listener + readers
	console   []string
}

func (t *browserTool) Name() string { return "browser" }

func (t *browserTool) Description() string {
	return "Drive a headless browser for web-app debugging. Actions: " +
		"navigate (load a url), click (CSS selector), type (selector+text), " +
		"screenshot (capture the page — returned as an image you can see), " +
		"read_console (console logs + page errors since the last read), " +
		"get_text / get_html (visible text or HTML of the page or a selector). " +
		"The page persists between calls. Requires Chrome/Chromium installed."
}

func (t *browserTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["navigate", "click", "type", "screenshot", "read_console", "get_text", "get_html"], "description": "The browser action to perform."},
			"url": {"type": "string", "description": "For navigate: the URL to load."},
			"selector": {"type": "string", "description": "CSS selector for click/type, or the element to read for get_text/get_html (defaults to the whole page)."},
			"text": {"type": "string", "description": "For type: the text to enter into the selected element."}
		},
		"required": ["action"]
	}`)
}

// Mutates: browser actions can navigate anywhere and click/submit things, so the
// whole tool is gated behind approval (consistent with other side-effecting tools).
func (t *browserTool) Mutates() bool { return true }

type browserInput struct {
	Action   string `json:"action"`
	URL      string `json:"url"`
	Selector string `json:"selector"`
	Text     string `json:"text"`
}

// Execute satisfies the Tool interface; it runs the action and drops any image
// (the text result still names the saved screenshot file). The agent calls
// ExecuteImages, which forwards screenshots to the model.
func (t *browserTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	out, _, err := t.ExecuteImages(ctx, input)
	return out, err
}

// browserActionTimeout bounds a single browser action so a hung page can't wedge
// the turn.
const browserActionTimeout = 45 * time.Second

func (t *browserTool) ExecuteImages(ctx context.Context, input json.RawMessage) (string, []Image, error) {
	var in browserInput
	if err := decode(input, &in); err != nil {
		return "", nil, err
	}
	in.Action = strings.TrimSpace(in.Action)
	// Validate up front so bad input never launches Chrome.
	switch in.Action {
	case "navigate":
		if strings.TrimSpace(in.URL) == "" {
			return "", nil, fmt.Errorf("navigate requires a url")
		}
	case "click", "type":
		if strings.TrimSpace(in.Selector) == "" {
			return "", nil, fmt.Errorf("%s requires a selector", in.Action)
		}
		if in.Action == "type" && in.Text == "" {
			return "", nil, fmt.Errorf("type requires text")
		}
	case "screenshot", "read_console", "get_text", "get_html":
		// no required fields
	case "":
		return "", nil, fmt.Errorf("action is required")
	default:
		return "", nil, fmt.Errorf("unknown browser action %q", in.Action)
	}

	// read_console doesn't need the page; serve it from the buffer.
	if in.Action == "read_console" {
		return t.takeConsole(), nil, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureStarted(); err != nil {
		return "", nil, err
	}

	runCtx, cancel := context.WithTimeout(t.ctx, browserActionTimeout)
	defer cancel()
	// Honor turn cancellation (e.g. the user interrupts) by cancelling the action.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()

	switch in.Action {
	case "navigate":
		var title string
		if err := chromedp.Run(runCtx, chromedp.Navigate(in.URL), chromedp.Title(&title)); err != nil {
			return "", nil, fmt.Errorf("navigate %q: %w", in.URL, err)
		}
		return fmt.Sprintf("Navigated to %s\nTitle: %s", in.URL, strings.TrimSpace(title)), nil, nil
	case "click":
		if err := chromedp.Run(runCtx, chromedp.Click(in.Selector, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("click %q: %w", in.Selector, err)
		}
		return "Clicked " + in.Selector, nil, nil
	case "type":
		if err := chromedp.Run(runCtx, chromedp.SendKeys(in.Selector, in.Text, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("type into %q: %w", in.Selector, err)
		}
		return fmt.Sprintf("Typed into %s", in.Selector), nil, nil
	case "get_text":
		sel := in.Selector
		if strings.TrimSpace(sel) == "" {
			sel = "body"
		}
		var text string
		if err := chromedp.Run(runCtx, chromedp.Text(sel, &text, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("get_text %q: %w", sel, err)
		}
		return text, nil, nil
	case "get_html":
		sel := in.Selector
		if strings.TrimSpace(sel) == "" {
			sel = "html"
		}
		var html string
		if err := chromedp.Run(runCtx, chromedp.OuterHTML(sel, &html, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("get_html %q: %w", sel, err)
		}
		return html, nil, nil
	case "screenshot":
		var buf []byte
		if err := chromedp.Run(runCtx, chromedp.CaptureScreenshot(&buf)); err != nil {
			return "", nil, fmt.Errorf("screenshot: %w", err)
		}
		path := saveScreenshot(buf)
		note := "Captured a screenshot of the current page."
		if path != "" {
			note += " Saved to " + path
		}
		return note, []Image{{MediaType: "image/png", Data: buf}}, nil
	}
	return "", nil, fmt.Errorf("unknown browser action %q", in.Action)
}

// ensureStarted launches Chrome on first use and attaches the console listener.
// Caller must hold t.mu.
func (t *browserTool) ensureStarted() error {
	if t.started {
		return nil
	}
	if _, err := chromeExecPath(); err != nil {
		return err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	bctx, bcancel := chromedp.NewContext(allocCtx)
	// Start the browser so a target exists before we attach the listener.
	if err := chromedp.Run(bctx); err != nil {
		bcancel()
		allocCancel()
		return fmt.Errorf("launch chrome: %w (is Google Chrome / Chromium installed?)", err)
	}
	chromedp.ListenTarget(bctx, func(ev interface{}) {
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			t.appendConsole(formatConsole(string(e.Type), e.Args))
		case *cdpruntime.EventExceptionThrown:
			if e.ExceptionDetails != nil {
				t.appendConsole("[error] " + e.ExceptionDetails.Text)
			}
		}
	})
	t.ctx, t.ctxCancel, t.allocCancel, t.started = bctx, bcancel, allocCancel, true
	t.r.addCloser(t.close) // reaped on registry shutdown so Chrome isn't orphaned
	return nil
}

// close tears down the browser. Safe to call more than once.
func (t *browserTool) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return
	}
	t.started = false
	if t.ctxCancel != nil {
		t.ctxCancel()
	}
	if t.allocCancel != nil {
		t.allocCancel()
	}
}

func (t *browserTool) appendConsole(line string) {
	if line == "" {
		return
	}
	t.consoleMu.Lock()
	// Bound the buffer so a chatty page can't grow it without limit.
	if len(t.console) >= 500 {
		t.console = t.console[len(t.console)-499:]
	}
	t.console = append(t.console, line)
	t.consoleMu.Unlock()
}

// takeConsole returns the buffered console messages and clears the buffer.
func (t *browserTool) takeConsole() string {
	t.consoleMu.Lock()
	lines := t.console
	t.console = nil
	t.consoleMu.Unlock()
	if len(lines) == 0 {
		return "(no console output since the last read)"
	}
	return strings.Join(lines, "\n")
}

// formatConsole renders a console.* call as "[level] arg arg …".
func formatConsole(level string, args []*cdpruntime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == nil {
			continue
		}
		if len(a.Value) > 0 {
			parts = append(parts, strings.Trim(string(a.Value), `"`))
		} else if a.Description != "" {
			parts = append(parts, a.Description)
		}
	}
	return "[" + level + "] " + strings.Join(parts, " ")
}

// saveScreenshot writes the PNG to a temp file and returns its path (or "" on
// failure — the image still goes to the model regardless).
func saveScreenshot(png []byte) string {
	dir := filepath.Join(os.TempDir(), "moony-screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, fmt.Sprintf("shot-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return ""
	}
	return path
}

// chromeExecPath finds a Chrome/Chromium binary, returning a helpful error when
// none is installed so the tool degrades gracefully instead of failing obscurely.
func chromeExecPath() (string, error) {
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		} {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("the browser tool needs Google Chrome or Chromium installed (none found in PATH or the usual locations)")
}
