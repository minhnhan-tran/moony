// Verification script: verify token usage and context percent on loading session history
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9338;
const RELAY_PORT = 8793;
const TUNNEL_PORT = 8783;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "hist-tok-test-prof-"));

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

  // 1. Simulate initial status event with hydrated session values
  await evaluate(`(() => {
    handle({
      type: "status",
      status: {
        tokensIn: 35000,
        tokensOut: 1700,
        contextTokens: 25000,
        contextWindow: 200000,
        contextPercent: 13,
        model: "anthropic.claude-sonnet-4-6",
        provider: "Anthropic"
      }
    });
  })()`);

  // 2. Replay turn_meta events from history
  await evaluate(`(() => {
    handle({
      type: "turn_meta",
      meta: {
        inTokens: 10000,
        outTokens: 500,
        cacheReadTokens: 2000,
        calls: 1,
        time: new Date().toISOString()
      }
    });
    handle({
      type: "turn_meta",
      meta: {
        inTokens: 25000,
        outTokens: 1200,
        cacheReadTokens: 5000,
        calls: 2,
        time: new Date().toISOString()
      }
    });
    renderStatusBar();
  })()`);

  // 3. Inspect status bar contents
  const statsText = await evaluate(`document.getElementById("stats")?.textContent || ""`);
  console.log("Stats text content:", statsText);

  const ctxBar = await evaluate(`(() => {
    const el = document.querySelector(".stat-context");
    if (!el) return null;
    return {
      text: el.textContent,
      fillWidth: el.querySelector(".stat-bar-fill")?.style.width || el.querySelector("span[style*='width']")?.style.width
    };
  })()`);
  console.log("Context bar info:", JSON.stringify(ctxBar, null, 2));

  const ctxRows = await evaluate(`JSON.stringify(contextInfoRows(lastStatus))`);
  console.log("Context detail rows:", ctxRows);

  if (!statsText.includes("35k") && !statsText.includes("35,000") && !statsText.includes("35.0k")) {
    throw new Error(`Expected stats to contain ~35k input tokens, got: "${statsText}"`);
  }
  if (!statsText.includes("1.7k") && !statsText.includes("1,700") && !statsText.includes("1.70k")) {
    throw new Error(`Expected stats to contain ~1.7k output tokens, got: "${statsText}"`);
  }
  if (!ctxBar || !ctxBar.text.includes("13%")) {
    throw new Error(`Expected context bar to show 13%, got: ${JSON.stringify(ctxBar)}`);
  }

  console.log("ALL CHECKS PASSED!");

} finally {
  chrome.kill();
  relay.kill();
}
