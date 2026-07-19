package tui

import (
	"strings"
	"testing"
	"time"
)

func TestStreamMarkdownEnabled(t *testing.T) {
	t.Setenv("MN_STREAM_MARKDOWN", "")
	if !streamMarkdownEnabled() {
		t.Error("live streaming markdown should be enabled by default")
	}
	for _, v := range []string{"0", "off", "false", "no", "raw", "OFF"} {
		t.Setenv("MN_STREAM_MARKDOWN", v)
		if streamMarkdownEnabled() {
			t.Errorf("%q should disable live streaming markdown", v)
		}
	}
	t.Setenv("MN_STREAM_MARKDOWN", "yes")
	if !streamMarkdownEnabled() {
		t.Error("unknown values should leave it enabled")
	}
}

// TestStreamingBodyThrottle proves the in-progress render is cached within the
// throttle window and refreshed once it elapses.
func TestStreamingBodyThrottle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MN_STREAM_MARKDOWN", "") // default on
	m := sized(t, newTestModel(t), 100, 30)
	tr := &m.trans
	if tr.renderer == nil {
		t.Fatal("expected a markdown renderer after sizing")
	}
	tr.entries = []entry{{kind: entryAssistant, text: "# Title"}}

	first := tr.streamingBody(&tr.entries[0])

	// Mutating the text and calling again immediately is within the throttle
	// window, so the cached render must come back unchanged.
	tr.entries[0].text = "# Title — now with much more content streamed in"
	second := tr.streamingBody(&tr.entries[0])
	if first != second {
		t.Errorf("render within the throttle window should be cached and unchanged")
	}

	// Once the interval elapses, the next call reflects the new text.
	tr.lastStreamRender = time.Now().Add(-streamRenderInterval(len(tr.entries[0].text)) - time.Millisecond)
	third := tr.streamingBody(&tr.entries[0])
	if third == second {
		t.Errorf("after the throttle interval the render should refresh to the new text")
	}
}

// TestStreamingBodyUnchangedTextSkipsRender verifies that when the throttle
// window has elapsed but the text hasn't grown (e.g. a refresh triggered by a
// non-text event mid-turn), streamingBody reuses the cache instead of paying
// for another Glamour pass.
func TestStreamingBodyUnchangedTextSkipsRender(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MN_STREAM_MARKDOWN", "")
	m := sized(t, newTestModel(t), 100, 30)
	tr := &m.trans
	tr.entries = []entry{{kind: entryAssistant, text: "# Title"}}

	_ = tr.streamingBody(&tr.entries[0])
	// Force the throttle window open, but leave the text (and width) unchanged.
	before := tr.lastStreamRender.Add(-time.Hour)
	tr.lastStreamRender = before
	_ = tr.streamingBody(&tr.entries[0])
	if !tr.lastStreamRender.Equal(before) {
		t.Error("unchanged text should not trigger a re-render (lastStreamRender moved)")
	}
}

// TestStreamRenderInterval checks the adaptive throttle grows with reply size
// and is clamped to the max.
func TestStreamRenderInterval(t *testing.T) {
	if got := streamRenderInterval(0); got != streamRenderMinInterval {
		t.Errorf("short reply: interval = %v, want %v", got, streamRenderMinInterval)
	}
	if got := streamRenderInterval(streamRenderStepBytes); got != 2*streamRenderMinInterval {
		t.Errorf("one step of growth: interval = %v, want %v", got, 2*streamRenderMinInterval)
	}
	if got := streamRenderInterval(1 << 20); got != streamRenderMaxInterval {
		t.Errorf("huge reply: interval = %v, want clamp %v", got, streamRenderMaxInterval)
	}
}

// TestStreamingBodyWidthChangeRefreshes ensures a resize invalidates the cache
// even within the throttle window.
func TestStreamingBodyWidthChangeRefreshes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MN_STREAM_MARKDOWN", "")
	m := sized(t, newTestModel(t), 100, 30)
	tr := &m.trans
	tr.entries = []entry{{kind: entryAssistant, text: "some streamed text that wraps"}}

	_ = tr.streamingBody(&tr.entries[0])
	if tr.entries[0].streamCacheW != tr.width {
		t.Fatalf("streamCacheW = %d, want %d", tr.entries[0].streamCacheW, tr.width)
	}
	// Simulate a narrower transcript; the cache width no longer matches so it
	// must re-render despite being inside the throttle window.
	tr.width = 40
	tr.streamingBody(&tr.entries[0])
	if tr.entries[0].streamCacheW != 40 {
		t.Errorf("a width change should re-render; streamCacheW = %d, want 40", tr.entries[0].streamCacheW)
	}
}

// TestStreamingBodyRawWhenDisabled verifies the opt-out renders raw wrapped
// text (no markdown) while streaming.
func TestStreamingBodyRawWhenDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MN_STREAM_MARKDOWN", "0")
	m := sized(t, newTestModel(t), 100, 30)
	tr := &m.trans
	tr.entries = []entry{{kind: entryAssistant, text: "**bold** _italic_"}}

	got := tr.streamingBody(&tr.entries[0])
	want := indent(tr.wrap("**bold** _italic_"), 2)
	if got != want {
		t.Errorf("disabled live markdown should stream raw wrapped text\n got=%q\nwant=%q", got, want)
	}
}

// TestStreamingRenderViaRender exercises the full transcript render with a
// streaming index, confirming the streaming cache gets populated.
func TestStreamingRenderViaRender(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MN_STREAM_MARKDOWN", "")
	m := sized(t, newTestModel(t), 100, 30)
	tr := &m.trans
	tr.entries = []entry{{kind: entryAssistant, text: "## Heading\n\nbody text"}}

	out := tr.render(0) // entry 0 is streaming
	if out == "" {
		t.Fatal("render produced no output")
	}
	if tr.entries[0].streamCache == "" {
		t.Error("streaming render should populate the stream cache")
	}
	// The final (non-streaming) render path must recompute in full, never
	// reusing the throttled stream cache.
	finalOut := tr.render(-1)
	if tr.entries[0].rendered == "" {
		t.Error("completed render should populate the full markdown cache")
	}
	if finalOut == "" {
		t.Fatal("final render produced no output")
	}
}

// TestMarkdownHeadingsHaveNoHashes guards the fix for glamour rendering ATX
// headings with literal "## "/"### " prefixes.
func TestMarkdownHeadingsHaveNoHashes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GLAMOUR_STYLE", "") // ensure the tweaked dark/light config is used
	m := sized(t, newTestModel(t), 100, 30)

	out := m.trans.markdown("## Heading Two\n\nsome text\n\n### Heading Three\n")
	if strings.Contains(out, "##") {
		t.Errorf("rendered headings must not contain literal hashes:\n%q", out)
	}
	for _, word := range []string{"Heading", "Two", "Three"} {
		if !strings.Contains(out, word) {
			t.Errorf("heading text %q missing from render:\n%q", word, out)
		}
	}
}

func TestMarkdownStyleConfig(t *testing.T) {
	t.Setenv("GLAMOUR_STYLE", "")
	cfg, ok := markdownStyleConfig()
	if !ok {
		t.Fatal("expected the tweaked config to be used for the default style")
	}
	prefixes := []string{cfg.H1.Prefix, cfg.H2.Prefix, cfg.H3.Prefix, cfg.H4.Prefix, cfg.H5.Prefix, cfg.H6.Prefix}
	for i, p := range prefixes {
		if p != "" {
			t.Errorf("H%d prefix should be cleared, got %q", i+1, p)
		}
	}

	// A custom GLAMOUR_STYLE falls back to the named style (no tweak).
	t.Setenv("GLAMOUR_STYLE", "dracula")
	if _, ok := markdownStyleConfig(); ok {
		t.Error("a custom GLAMOUR_STYLE should not use the tweaked config")
	}
}
