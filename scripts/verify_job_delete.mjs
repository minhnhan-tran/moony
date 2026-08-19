// Automated verification for job deletion in jobstrip-line and manage modal
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9335;
const RELAY_PORT = 8790;
const TUNNEL_PORT = 8780;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "jobs-del-prof-"));

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
  // Wait for relay
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${RELAY_PORT}/`);
      if (res.ok) break;
    } catch { /* wait */ }
    await sleep(150);
  }

  // Connect CDP
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

  // 1. Populate mock jobs and expand the strip
  await evaluate(`(() => {
    stripJobs = [
      { id: "job_1", command: "npm test", running: true, started_at: Date.now() - 5000, exit_code: null },
      { id: "job_2", command: "go build ./...", running: false, started_at: Date.now() - 30000, finished_at: Date.now() - 10000, exit_code: 0 }
    ];
    stripCollapsed = false;
    renderJobsStrip();
  })()`);
  await sleep(100);

  // 2. Verify delete button (trash icon) exists on both rows
  const deleteButtons = await evaluate(`document.querySelectorAll(".jobstrip-line .jobstrip-btn.danger").length`);
  check("delete button exists on each job row in jobstrip-line", deleteButtons === 2, `count=${deleteButtons}`);

  const deleteBtnTitle = await evaluate(`document.querySelector(".jobstrip-line .jobstrip-btn.danger").title`);
  check("delete button has correct title/aria-label", deleteBtnTitle === "Delete this job", deleteBtnTitle);

  // 3. Expand output for job_1
  await evaluate(`(() => {
    stripExpanded = "job_1";
    stripTails.set("job_1", { cursor: 10, text: "Running tests..." });
    renderJobsStrip();
  })()`);
  await sleep(100);

  const logOpen = await evaluate(`!!document.querySelector(".jobstrip-log")`);
  check("log pane is open for job_1", logOpen);

  // 4. Click delete button on job_1
  await evaluate(`(() => {
    const firstRowDelBtn = document.querySelector(".jobstrip-line .jobstrip-btn.danger");
    firstRowDelBtn.click();
  })()`);
  await sleep(100);

  const remainingRows = await evaluate(`document.querySelectorAll(".jobstrip-row").length`);
  check("job_1 deleted; 1 job remaining in strip", remainingRows === 1, `count=${remainingRows}`);

  const remainingCommand = await evaluate(`document.querySelector(".jobstrip-cmd").textContent`);
  check("remaining job is job_2", remainingCommand.includes("go build ./..."), remainingCommand);

  const logClosed = await evaluate(`!document.querySelector(".jobstrip-log")`);
  check("expanded log closed on delete", logClosed);

  // 5. Delete job_2 (the last job)
  await evaluate(`(() => {
    const lastDelBtn = document.querySelector(".jobstrip-line .jobstrip-btn.danger");
    lastDelBtn.click();
  })()`);
  await sleep(100);

  const stripHidden = await evaluate(`document.querySelector("#jobsStrip").classList.contains("hidden")`);
  check("jobsStrip is hidden after deleting all jobs", stripHidden);

  // 6. Test delete button in manage modal background jobs list
  const manageDelExists = await evaluate(`(() => {
    const list = [{ id: "job_3", command: "ls -la", running: false, started_at: Date.now() }];
    const row = jobRow(list[0], list);
    const del = Array.from(row.querySelectorAll("button")).find(b => b.textContent === "Delete");
    return !!del;
  })()`);
  check("manage modal jobRow has Delete button", manageDelExists);

} catch (err) {
  steps.push("ERROR " + (err && err.stack ? err.stack : err));
  process.exitCode = 1;
} finally {
  console.log(steps.join("\n"));
  chrome.kill();
  relay.kill();
}
