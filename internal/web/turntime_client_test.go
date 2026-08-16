package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The client must never fabricate a timestamp for history it cannot date.
//
// These tests execute the REAL functions lifted out of app-render.js / app-core.js in node,
// rather than asserting on source text: a source-text assertion passes forever once the
// wording drifts, and it cannot tell whether the code actually behaves correctly. Behaviour
// tests survive refactoring; grep assertions do not.
//
// Skipped when node isn't installed, so the suite stays green on a bare machine.

// jsFuncRe lifts a top-level `function name(...) { ... }` up to the first line-start "}".
func jsFuncRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?ms)^function ` + regexp.QuoteMeta(name) + `\(.*?^\}`)
}

func liftJSFunc(t *testing.T, src, name string) string {
	t.Helper()
	m := jsFuncRe(name).FindString(src)
	if m == "" {
		t.Fatalf("could not lift function %s() out of the source", name)
	}
	return m
}

// runNode evaluates a script and returns its stdout, or skips if node is unavailable.
func runNode(t *testing.T, script string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping client behaviour test")
	}
	f := filepath.Join(t.TempDir(), "t.mjs")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// tsToMs must return 0 — not NaN — for a missing or unparseable stamp. 0 is falsy so callers
// can fall back with `|| Date.now()`; NaN would poison the arithmetic and render "NaNms".
func TestTsToMsNeverReturnsNaN(t *testing.T) {
	src := readAsset(t, "static/app-render.js")
	script := liftJSFunc(t, src, "tsToMs") + `
const cases = ["", null, undefined, "not-a-date", "2026-01-02T03:04:05Z"];
for (const c of cases) {
  const v = tsToMs(c);
  if (Number.isNaN(v)) { console.log("NAN:" + String(c)); process.exit(0); }
}
if (tsToMs("") !== 0 || tsToMs("garbage") !== 0) { console.log("NOTZERO"); process.exit(0); }
if (tsToMs("2026-01-02T03:04:05Z") !== Date.parse("2026-01-02T03:04:05Z")) { console.log("BADPARSE"); process.exit(0); }
console.log("OK");`
	if got := runNode(t, script); got != "OK" {
		t.Errorf("tsToMs misbehaved: %s", got)
	}
}

// userMetaFooter must draw NO clock when the send time is unknown (null). Defaulting to
// new Date() dates a months-old question to the page load — and presents it as fact.
func TestUserMetaFooterOmitsClockWhenUndatable(t *testing.T) {
	src := readAsset(t, "static/app-render.js")
	script := `
// Minimal DOM: el() records appended text so we can see whether a clock was drawn.
function el(tag, cls, txt) {
  return { tag, cls, txt: txt || "", kids: [], appendChild(k) { this.kids.push(k); },
           get text() { return this.txt + this.kids.map(k => k.text).join(" "); } };
}
function sym() { return el("app-icon"); }
const navigator = {};
` + liftJSFunc(t, src, "fmtDateTime") + `
` + liftJSFunc(t, src, "userMetaFooter") + `
const unknown = userMetaFooter("hello", null);
const known = userMetaFooter("hello", new Date("2026-01-02T03:04:00Z"));
const clockRe = /\d{4}-\d{2}-\d{2} \d{2}:\d{2}/;
if (clockRe.test(unknown.text)) { console.log("FABRICATED:" + unknown.text); process.exit(0); }
if (!clockRe.test(known.text)) { console.log("LOSTKNOWN:" + known.text); process.exit(0); }
console.log("OK");`
	if got := runNode(t, script); got != "OK" {
		t.Errorf("userMetaFooter misbehaved: %s", got)
	}
}

// renderTurnMeta must omit BOTH the clock and the duration when the turn's start is unknown
// (turnStartMs === 0), keeping only what IS known, and must render no footer at all when
// nothing is known. Stamping now / "0ms" reports the page-load instant and a zero-length
// turn as fact.
func TestRenderTurnMetaOmitsUnknownTime(t *testing.T) {
	render := readAsset(t, "static/app-render.js")
	script := `
function el(tag, cls, txt) {
  return { tag, cls, txt: txt || "", kids: [], classList: { add() {} },
           appendChild(k) { this.kids.push(k); },
           get text() { return this.txt + this.kids.map(k => k.text).join(" "); } };
}
let appended = [];
function appendMsg(m) { appended.push(m); }
function scrollBottom() {}
function fmtUSD(v) { return "$" + v.toFixed(3); }
function fmtDur(ms) { return ms + "ms"; }
function prettyModel(m) { return m; }
function tokenMetaParts() { return []; }
` + liftJSFunc(t, render, "fmtDateTime") + `
` + liftJSFunc(t, render, "turnMetaLine") + `
` + liftJSFunc(t, render, "renderTurnMeta") + `

const clockRe = /\d{4}-\d{2}-\d{2} \d{2}:\d{2}/;
let turnActive, turnStartMs, lastStatus, lastTurnCost, lastTurnModel, lastTurnReasoning,
    lastTurnIn, lastTurnOut, lastTurnCacheRead, curStep;
function reset() {
  appended = []; turnActive = true; lastStatus = {}; lastTurnCost = 0; lastTurnModel = "";
  lastTurnReasoning = ""; lastTurnIn = 0; lastTurnOut = 0; lastTurnCacheRead = 0; curStep = 0;
}
// 1. Undatable turn that still has metrics: no clock, no duration, but cost/calls kept.
reset(); turnStartMs = 0; lastTurnCost = 0.5; curStep = 40;
renderTurnMeta();
if (appended.length !== 1) { console.log("NOFOOTER1"); process.exit(0); }
let txt = appended[0].text;
if (clockRe.test(txt)) { console.log("FABRICATED_CLOCK:" + txt); process.exit(0); }
if (/\dms\b/.test(txt)) { console.log("FABRICATED_DUR:" + txt); process.exit(0); }
if (!txt.includes("40 calls") || !txt.includes("$0.500")) { console.log("LOSTKNOWN:" + txt); process.exit(0); }

// 2. Undatable turn with nothing known at all: no footer line whatsoever.
reset(); turnStartMs = 0;
renderTurnMeta();
if (appended.length !== 0) { console.log("EMPTYFOOTER:" + appended[0].text); process.exit(0); }

// 3. A live turn still gets its clock AND a true elapsed duration.
reset(); turnStartMs = Date.now() - 5000;
renderTurnMeta();
txt = appended[0].text;
if (!clockRe.test(txt)) { console.log("LOSTCLOCK:" + txt); process.exit(0); }
const m = txt.match(/(\d+)ms/);
if (!m || Number(m[1]) < 4000) { console.log("BADELAPSED:" + txt); process.exit(0); }
console.log("OK");`
	if got := runNode(t, script); got != "OK" {
		t.Errorf("renderTurnMeta misbehaved: %s", got)
	}
}

// A wiring guard: the behaviour tests above evaluate tsToMs in isolation, so they would all
// still pass if app-core.js stopped calling it and went back to an unconditional Date.now().
// This asserts the seeding expression is actually wired to the server stamp.
func TestTurnTimerSeededFromServerStamp(t *testing.T) {
	src := readAsset(t, "static/app-core.js")
	// The seeding assignment, not the reset sites (`turnStartMs = 0` when clearing state).
	i := strings.Index(src, "turnStartMs = ev.replay")
	if i < 0 {
		t.Fatal("turnStartMs is not seeded from the server stamp: no `turnStartMs = ev.replay ...` assignment in app-core.js, so a client reloading mid-turn measures time since page load")
	}
	line := src[i:]
	if j := strings.IndexByte(line, '\n'); j > 0 {
		line = line[:j]
	}
	if !strings.Contains(line, "tsToMs(ev.time)") {
		t.Errorf("turnStartMs is not seeded from the server stamp; got %q", line)
	}
	if !strings.Contains(line, "|| Date.now()") {
		t.Errorf("a LIVE turn with no server stamp must still fall back to the local clock; got %q", line)
	}

	// The footer must be fed the SANITIZED turnStartMs, not a second raw parse of ev.time.
	// Parsing ev.time again bypasses tsToMs, so a live event with no stamp loses its clock
	// and an unparseable stamp renders "NaN-NaN-NaN". Found in review, so it is pinned here.
	if strings.Contains(src, "userMetaFooter(ev.text || \"\", ev.time ?") {
		t.Error("userMetaFooter is fed a raw parse of ev.time, bypassing tsToMs: a live event with no stamp loses its clock and a bad stamp renders NaN")
	}
	if !strings.Contains(src, "userMetaFooter(ev.text || \"\", turnStartMs ?") {
		t.Error("userMetaFooter is not fed the sanitized turnStartMs")
	}
}
