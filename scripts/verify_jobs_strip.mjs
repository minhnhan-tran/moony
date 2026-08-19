// Automated verification for collapsible/expandable jobsStrip
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9334;
const RELAY_PORT = 8789;
const TUNNEL_PORT = 8779;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "jobs-prof-"));

// Start relay
const relay = spawn("./bin/relay.exe", [
  "-addr", `127.0.0.1:${RELAY_PORT}`,
  "-tunnel", `127.0.0.1:${TUNNEL_PORT}`
], {
  env: { ...process.env, MN_AUTH: "off" },
  stdio: "ignore",
});

// Start headless Chrome
const chrome = spawn(CHROME, [
  "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run",
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, id = 0;
const pending = new Map();
const logs = [];

async function connect() {
  // Wait for relay to be up
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${RELAY_PORT}/`);
      if (res.ok) break;
    } catch { /* wait */ }
    await sleep(150);
  }

  // Connect to Chrome CDP
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/json/list`);
      const tabs = await res.json();
      const page = tabs.find((t) => t.type === "page");
      if (page) {
        ws = new WebSocket(page.webSocketDebuggerUrl);
        await new Promise((res2, rej) => { ws.onopen = res2; ws.onerror = rej; });
        ws.onmessage = (ev) => {
          const msg = JSON.parse(ev.data);
          if (msg.id && pending.has(msg.id)) {
            const { resolve, reject } = pending.get(msg.id);
            pending.delete(msg.id);
            msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
          } else if (msg.method === "Runtime.consoleAPICalled") {
            logs.push("console." + msg.params.type + ": " +
              msg.params.args.map((a) => a.value ?? a.description ?? a.type).join(" "));
          }
        };
        return;
      }
    } catch { /* wait */ }
    await sleep(200);
  }
  throw new Error("could not attach to Chrome");
}

function send(method, params = {}) {
  const msgId = ++id;
  return new Promise((resolve, reject) => {
    pending.set(msgId, { resolve, reject });
    ws.send(JSON.stringify({ id: msgId, method, params }));
  });
}

async function evaluate(expr) {
  const r = await send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error("page threw: " + JSON.stringify(r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}

async function navigate(url) {
  await send("Page.navigate", { url });
  for (let i = 0; i < 100; i++) {
    await sleep(100);
    const ready = await evaluate("document.readyState");
    if (ready === "complete") { await sleep(150); return; }
  }
  throw new Error("navigation timed out: " + url);
}

const steps = [];
function check(name, cond, detail = "") {
  steps.push(`${cond ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
  if (!cond) process.exitCode = 1;
}

try {
  await connect();
  await send("Page.enable");
  await send("Runtime.enable");

  await navigate(`http://127.0.0.1:${RELAY_PORT}/`);
  await sleep(1000);

  // 1. Initial state without jobs: #jobsStrip is hidden
  const initHidden = await evaluate(`document.querySelector("#jobsStrip").classList.contains("hidden")`);
  check("initially jobsStrip is hidden", initHidden);

  // 2. Feed mock jobs into stripJobs and render
  await evaluate(`(() => {
    stripJobs = [
      { id: "job_1", command: "npm test", running: true, started_at: Date.now() - 5000, exit_code: null },
      { id: "job_2", command: "go build ./...", running: false, started_at: Date.now() - 30000, finished_at: Date.now() - 10000, exit_code: 0 }
    ];
    renderJobsStrip();
  })()`);

  // 3. Verify default collapsed state
  const isShown = await evaluate(`!document.querySelector("#jobsStrip").classList.contains("hidden")`);
  check("jobsStrip is visible when jobs present", isShown);

  const isCollapsed = await evaluate(`document.querySelector("#jobsStrip").classList.contains("is-collapsed")`);
  check("jobsStrip defaults to collapsed (has is-collapsed class)", isCollapsed);

  const ariaExpanded = await evaluate(`document.querySelector(".jobstrip-head").getAttribute("aria-expanded")`);
  check("header has aria-expanded='false' when collapsed", ariaExpanded === "false");

  const headRole = await evaluate(`document.querySelector(".jobstrip-head").getAttribute("role")`);
  check("header has role='button'", headRole === "button");

  const headTabIndex = await evaluate(`document.querySelector(".jobstrip-head").tabIndex`);
  check("header has tabIndex=0 for keyboard focus", headTabIndex === 0);

  const chevCollapsed = await evaluate(`document.querySelector(".jobstrip-head-chev app-icon").getAttribute("name")`);
  check("header shows chevron.right when collapsed", chevCollapsed === "chevron.right");

  const listCollapsed = await evaluate(`getComputedStyle(document.querySelector("#jobsStripList")).display === "none"`);
  check("job list is hidden via CSS when collapsed", listCollapsed);

  const headText = await evaluate(`document.querySelector(".jobstrip-head-text").textContent`);
  check("header shows summary text", headText.includes("Background jobs: 2 (1 running)"), headText);

  // 4. Click header to expand
  await evaluate(`document.querySelector(".jobstrip-head").click()`);
  await sleep(100);

  const isExpandedNow = await evaluate(`!document.querySelector("#jobsStrip").classList.contains("is-collapsed")`);
  check("clicking header expands strip (is-collapsed removed)", isExpandedNow);

  const ariaExpandedTrue = await evaluate(`document.querySelector(".jobstrip-head").getAttribute("aria-expanded")`);
  check("header has aria-expanded='true' when expanded", ariaExpandedTrue === "true");

  const chevExpanded = await evaluate(`document.querySelector(".jobstrip-head-chev app-icon").getAttribute("name")`);
  check("header shows chevron.down when expanded", chevExpanded === "chevron.down");

  const listVisible = await evaluate(`getComputedStyle(document.querySelector("#jobsStripList")).display !== "none"`);
  check("job list is visible when expanded", listVisible);

  const rowCount = await evaluate(`document.querySelectorAll(".jobstrip-row").length`);
  check("correct number of job rows rendered", rowCount === 2, `count=${rowCount}`);

  // 5. Click header again to collapse
  await evaluate(`document.querySelector(".jobstrip-head").click()`);
  await sleep(100);

  const isCollapsedAgain = await evaluate(`document.querySelector("#jobsStrip").classList.contains("is-collapsed")`);
  check("clicking header again collapses strip", isCollapsedAgain);

  const ariaExpandedFalseAgain = await evaluate(`document.querySelector(".jobstrip-head").getAttribute("aria-expanded")`);
  check("header has aria-expanded='false' again", ariaExpandedFalseAgain === "false");

  // 6. Keyboard navigation (Enter & Space) with focus preservation
  await evaluate(`(() => {
    const head = document.querySelector(".jobstrip-head");
    head.focus();
    head.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  })()`);
  await sleep(100);

  const expandedByEnter = await evaluate(`!document.querySelector("#jobsStrip").classList.contains("is-collapsed")`);
  check("pressing Enter toggles to expanded", expandedByEnter);

  const focusPreserved = await evaluate(`document.activeElement === document.querySelector(".jobstrip-head")`);
  check("focus is preserved on header after keyboard toggle", focusPreserved);

  await evaluate(`(() => {
    const head = document.querySelector(".jobstrip-head");
    head.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
  })()`);
  await sleep(100);

  const collapsedBySpace = await evaluate(`document.querySelector("#jobsStrip").classList.contains("is-collapsed")`);
  check("pressing Space toggles back to collapsed", collapsedBySpace);

  // 7. Reset jobs strip
  await evaluate(`resetJobsStrip()`);
  const afterResetHidden = await evaluate(`document.querySelector("#jobsStrip").classList.contains("hidden")`);
  check("resetJobsStrip hides the strip", afterResetHidden);

} catch (err) {
  steps.push("ERROR " + (err && err.stack ? err.stack : err));
  process.exitCode = 1;
} finally {
  console.log(steps.join("\n"));
  chrome.kill();
  relay.kill();
}
