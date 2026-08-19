// Verification script: verify clearance between floating pills (pager, quickActions, jobsPill) and transcript scrollbar
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9342;
const RELAY_PORT = 8797;
const TUNNEL_PORT = 8787;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "pills-clearance-prof-"));

// Build relay first to ensure fresh static assets
const relay = spawn("./bin/relay.exe", [
  "-addr", `127.0.0.1:${RELAY_PORT}`,
  "-tunnel", `127.0.0.1:${TUNNEL_PORT}`
], {
  env: { ...process.env, MN_AUTH: "off" },
  stdio: "ignore",
});

const chrome = spawn(CHROME, [
  "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run",
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, id = 0;
const pending = new Map();

async function connect() {
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${RELAY_PORT}/`);
      if (res.ok) break;
    } catch { /* wait */ }
    await sleep(150);
  }

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

try {
  await connect();
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Page.navigate", { url: `http://127.0.0.1:${RELAY_PORT}/` });
  await sleep(1000);

  // 1. Populate pairs and show all 3 floating pills (#pager, #quickActions, #jobsPill)
  await evaluate(`(() => {
    const tr = document.getElementById("transcript");
    tr.innerHTML = Array.from({ length: 21 }, (_, i) => \`
      <div class="pair \${i === 20 ? 'pair-active' : ''}" data-pair="\${i}">
        <div class="pair-q"><div class="msg user">Question \${i + 1}</div></div>
        <div class="pair-a"><div class="msg assistant">\${Array.from({ length: 20 }, (_, j) => \`<p>Response line \${j}</p>\`).join('')}</div></div>
      </div>
    \`).join('');

    pairs = Array.from({ length: 21 }, (_, i) => ({
      root: document.querySelector(".pair[data-pair='" + i + "']"),
      answer: document.querySelector(".pair[data-pair='" + i + "'] .pair-a"),
      q: document.querySelector(".pair[data-pair='" + i + "'] .pair-q"),
      text: "Question " + (i + 1)
    }));
    activePairIdx = 20;
    renderPager();

    quickActionList = [{ label: "Review", prompt: "review" }];
    renderQuickActionMenu();

    jobsPill.hidden = false;
    if (jobsBtnCount) { jobsBtnCount.hidden = false; jobsBtnCount.textContent = "1"; }
    syncFloatingPills();
  })()`);
  await sleep(150);

  const test1 = await evaluate(`(() => {
    const trWrap = document.querySelector(".transcript-wrap");
    const pager = document.getElementById("pager");
    const qa = document.getElementById("quickActions");
    const jobs = document.getElementById("jobsPill");
    const activePairA = document.querySelector(".pair.pair-active .pair-a");

    const wrapRect = trWrap.getBoundingClientRect();
    const pagerRect = pager.getBoundingClientRect();
    const qaRect = qa.getBoundingClientRect();
    const jobsRect = jobs.getBoundingClientRect();
    const scrollbarWidth = activePairA ? (activePairA.offsetWidth - activePairA.clientWidth) : 10;

    return {
      wrapRight: wrapRect.right,
      scrollbarWidth,
      pager: {
        right: pagerRect.right,
        offsetFromWrapRight: wrapRect.right - pagerRect.right,
        clearanceFromScrollbar: wrapRect.right - scrollbarWidth - pagerRect.right,
        hidden: pager.hidden,
        width: pagerRect.width
      },
      qa: {
        right: qaRect.right,
        offsetFromWrapRight: wrapRect.right - qaRect.right,
        hidden: qa.hidden,
        width: qaRect.width
      },
      jobs: {
        right: jobsRect.right,
        offsetFromWrapRight: wrapRect.right - jobsRect.right,
        hidden: jobsPill.hidden,
        width: jobsRect.width
      }
    };
  })()`);

  console.log("1. All 3 pills active metrics:\n", JSON.stringify(test1, null, 2));

  // Assertions for test 1
  if (test1.pager.hidden) throw new Error("Expected pager to be visible");
  if (test1.pager.offsetFromWrapRight < 32) {
    throw new Error(`Pager offset from wrap right is ${test1.pager.offsetFromWrapRight}px, want >= 32px`);
  }
  if (test1.pager.clearanceFromScrollbar < 14) {
    throw new Error(`Clearance from scrollbar is ${test1.pager.clearanceFromScrollbar}px, want >= 14px`);
  }
  if (test1.qa.offsetFromWrapRight <= test1.pager.offsetFromWrapRight) {
    throw new Error("Quick actions should be shifted to the left of pager");
  }
  if (test1.jobs.offsetFromWrapRight <= test1.qa.offsetFromWrapRight) {
    throw new Error("Jobs pill should be shifted to the left of quick actions");
  }

  // 2. Hide pager -> quickActions becomes rightmost pill
  await evaluate(`(() => {
    pager.hidden = true;
    syncFloatingPills();
  })()`);
  await sleep(50);

  const test2 = await evaluate(`(() => {
    const trWrap = document.querySelector(".transcript-wrap");
    const qa = document.getElementById("quickActions");
    const jobs = document.getElementById("jobsPill");
    const wrapRect = trWrap.getBoundingClientRect();
    const qaRect = qa.getBoundingClientRect();
    const jobsRect = jobs.getBoundingClientRect();
    return {
      qaOffset: wrapRect.right - qaRect.right,
      jobsOffset: wrapRect.right - jobsRect.right
    };
  })()`);
  console.log("2. Pager hidden, QA is rightmost:\n", JSON.stringify(test2, null, 2));
  if (test2.qaOffset < 32) throw new Error(`QA offset is ${test2.qaOffset}px, want >= 32px`);
  if (test2.jobsOffset <= test2.qaOffset) throw new Error("Jobs pill should be shifted left of QA");

  // 3. Hide quickActions -> jobs becomes rightmost pill
  await evaluate(`(() => {
    quickActions.hidden = true;
    syncFloatingPills();
  })()`);
  await sleep(50);

  const test3 = await evaluate(`(() => {
    const trWrap = document.querySelector(".transcript-wrap");
    const jobs = document.getElementById("jobsPill");
    const wrapRect = trWrap.getBoundingClientRect();
    const jobsRect = jobs.getBoundingClientRect();
    return {
      jobsOffset: wrapRect.right - jobsRect.right
    };
  })()`);
  console.log("3. QA hidden, Jobs is rightmost:\n", JSON.stringify(test3, null, 2));
  if (test3.jobsOffset < 32) throw new Error(`Jobs offset is ${test3.jobsOffset}px, want >= 32px`);

  console.log("ALL FLOATING PILLS SCROLLBAR CLEARANCE TESTS PASSED!");

} finally {
  chrome.kill();
  relay.kill();
}
