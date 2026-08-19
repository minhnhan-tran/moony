// Verification script: verify collapsible/expandable queueStrip
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9339;
const RELAY_PORT = 8794;
const TUNNEL_PORT = 8784;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "queue-strip-test-prof-"));

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

  // 1. Initially when no messages are queued, #queueStrip is hidden
  const initialHidden = await evaluate(`document.getElementById("queueStrip")?.classList.contains("hidden")`);
  console.log("1. Initial queueStrip hidden:", initialHidden);
  if (!initialHidden) throw new Error("Expected queueStrip to be hidden initially");

  // 2. Add 3 queued messages
  await evaluate(`(() => {
    queuedMsgs = ["Fix bug in parser", "Refactor session store", "Add tests for auth"];
    queueCollapsed = true;
    renderQueueStrip();
  })()`);

  const step2 = await evaluate(`(() => {
    const qs = document.getElementById("queueStrip");
    const head = qs?.querySelector(".queue-head");
    const list = qs?.querySelector(".queue-list");
    return {
      hidden: qs?.classList.contains("hidden"),
      isCollapsed: qs?.classList.contains("is-collapsed"),
      headText: head?.textContent || "",
      ariaExpanded: head?.getAttribute("aria-expanded"),
      hasList: !!list
    };
  })()`);
  console.log("2. Populated queuedMsgs (default collapsed):", step2);
  if (step2.hidden) throw new Error("Expected queueStrip to be visible");
  if (!step2.isCollapsed) throw new Error("Expected queueStrip to have is-collapsed class");
  if (step2.ariaExpanded !== "false") throw new Error("Expected aria-expanded='false'");
  if (step2.hasList) throw new Error("Expected .queue-list to not be rendered when collapsed");
  if (!step2.headText.includes("Queued (3)")) throw new Error(`Expected header to include 'Queued (3)', got: ${step2.headText}`);

  // 3. Click header to expand
  await evaluate(`document.querySelector(".queue-head").click()`);
  const step3 = await evaluate(`(() => {
    const qs = document.getElementById("queueStrip");
    const head = qs?.querySelector(".queue-head");
    const list = qs?.querySelector(".queue-list");
    const rows = Array.from(list?.querySelectorAll(".queue-row") || []).map(r => ({
      idx: r.querySelector(".queue-idx")?.textContent,
      text: r.querySelector(".queue-text")?.textContent,
      hasRemoveBtn: !!r.querySelector(".queue-remove")
    }));
    return {
      isCollapsed: qs?.classList.contains("is-collapsed"),
      ariaExpanded: head?.getAttribute("aria-expanded"),
      rowCount: rows.length,
      rows
    };
  })()`);
  console.log("3. Expanded queueStrip:", step3);
  if (step3.isCollapsed) throw new Error("Expected queueStrip to not be collapsed after click");
  if (step3.ariaExpanded !== "true") throw new Error("Expected aria-expanded='true'");
  if (step3.rowCount !== 3) throw new Error(`Expected 3 rows, got ${step3.rowCount}`);

  // 4. Remove item 2 ("Refactor session store")
  await evaluate(`document.querySelectorAll(".queue-remove")[1].click()`);
  const step4 = await evaluate(`(() => {
    const qs = document.getElementById("queueStrip");
    const head = qs?.querySelector(".queue-head");
    const list = qs?.querySelector(".queue-list");
    const rows = Array.from(list?.querySelectorAll(".queue-row") || []).map(r => r.querySelector(".queue-text")?.textContent);
    return {
      queueLength: queuedMsgs.length,
      headText: head?.textContent || "",
      rows
    };
  })()`);
  console.log("4. After removing 2nd item:", step4);
  if (step4.queueLength !== 2) throw new Error(`Expected queuedMsgs.length == 2, got ${step4.queueLength}`);
  if (!step4.headText.includes("Queued (2)")) throw new Error(`Expected header text to say Queued (2)`);
  if (step4.rows.includes("Refactor session store")) throw new Error("Item was not removed properly");

  // 5. Test Keyboard toggle (Enter and Space)
  await evaluate(`(() => {
    const head = document.querySelector(".queue-head");
    head.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  })()`);
  const step5a = await evaluate(`queueCollapsed`);
  console.log("5a. After pressing Enter:", { queueCollapsed: step5a });
  if (!step5a) throw new Error("Expected Enter to collapse queue");

  await evaluate(`(() => {
    const head = document.querySelector(".queue-head");
    head.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
  })()`);
  const step5b = await evaluate(`queueCollapsed`);
  console.log("5b. After pressing Space:", { queueCollapsed: step5b });
  if (step5b) throw new Error("Expected Space to expand queue");

  // 6. Clear all items -> auto-hides
  await evaluate(`(() => {
    queuedMsgs = [];
    renderQueueStrip();
  })()`);
  const step6 = await evaluate(`document.getElementById("queueStrip")?.classList.contains("hidden")`);
  console.log("6. After clearing all items (hidden):", step6);
  if (!step6) throw new Error("Expected queueStrip to be hidden when empty");

  console.log("ALL QUEUE STRIP E2E TESTS PASSED!");

} finally {
  chrome.kill();
  relay.kill();
}
