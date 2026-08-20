// Verification script: verify right-alignment of user-meta, msg-attachments, and user-body in web UI
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const PORT = 9346;
const RELAY_PORT = 8799;
const TUNNEL_PORT = 8789;
const CHROME = process.env.CHROME_PATH || (process.platform === "win32"
  ? "C:/Program Files/Google/Chrome/Application/chrome.exe"
  : "google-chrome");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "user-align-prof-"));

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
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`,
  "--window-size=1280,800", "about:blank",
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

  // 1. Render user message in .pair-q
  await evaluate(`(() => {
    const tr = document.getElementById("transcript");
    tr.innerHTML = \`
      <div class="pair pair-active" data-pair="0">
        <div class="pair-q">
          <div class="msg msg-user">
            <div class="user-body md break-words"><p>Short user prompt here</p></div>
            <div class="msg-attachments flex flex-wrap justify-end gap-2 mt-1.5">
              <div class="attach-chip" style="width: 120px; height: 28px;">Attachment 1</div>
            </div>
            <div class="user-meta flex items-center justify-end gap-1.5 mt-1 text-faint text-[11px] font-mono">
              <span class="meta-time">10:45 AM</span>
              <button class="meta-copy-btn" style="width: 20px; height: 20px;">C</button>
            </div>
          </div>
        </div>
        <div class="pair-a"><div class="msg assistant"><p>Response line</p></div></div>
      </div>
    \`;
  })()`);
  await sleep(100);

  const pairQData = await evaluate(`(() => {
    const pairQ = document.querySelector(".pair-q");
    const msgUser = document.querySelector(".pair-q .msg-user");
    const userBody = document.querySelector(".pair-q .user-body");
    const attachments = document.querySelector(".pair-q .msg-attachments");
    const userMeta = document.querySelector(".pair-q .user-meta");
    const metaBtn = document.querySelector(".pair-q .meta-copy-btn");
    const attachChip = document.querySelector(".pair-q .attach-chip");

    const qRect = pairQ.getBoundingClientRect();
    const bodyRect = userBody.getBoundingClientRect();
    const metaRect = userMeta.getBoundingClientRect();
    const btnRect = metaBtn.getBoundingClientRect();
    const chipRect = attachChip.getBoundingClientRect();

    // Compute padding-right of pairQ
    const qPadRight = parseFloat(getComputedStyle(pairQ).paddingRight) || 0;
    const contentRightEdge = qRect.right - qPadRight;

    return {
      qRectRight: qRect.right,
      contentRightEdge,
      userBody: {
        left: bodyRect.left,
        right: bodyRect.right,
        width: bodyRect.width,
        gapFromRightEdge: contentRightEdge - bodyRect.right
      },
      userMeta: {
        left: metaRect.left,
        right: metaRect.right,
        btnRight: btnRect.right,
        gapFromRightEdge: contentRightEdge - btnRect.right
      },
      attachments: {
        chipRight: chipRect.right,
        gapFromRightEdge: contentRightEdge - chipRect.right
      }
    };
  })()`);

  console.log("1. Pinned header .pair-q alignment metrics:\n", JSON.stringify(pairQData, null, 2));

  // In .pair-q, user-body right edge, meta copy button, and attach chip should all sit right at contentRightEdge (within 2px tolerance)
  if (Math.abs(pairQData.userBody.gapFromRightEdge) > 2) {
    throw new Error(`.user-body is not right-aligned in .pair-q (gap: ${pairQData.userBody.gapFromRightEdge}px)`);
  }
  if (Math.abs(pairQData.userMeta.gapFromRightEdge) > 2) {
    throw new Error(`.user-meta items are not right-aligned in .pair-q (gap: ${pairQData.userMeta.gapFromRightEdge}px)`);
  }
  if (Math.abs(pairQData.attachments.gapFromRightEdge) > 2) {
    throw new Error(`.msg-attachments items are not right-aligned in .pair-q (gap: ${pairQData.attachments.gapFromRightEdge}px)`);
  }

  // 2. Render user message in scrolling transcript
  await evaluate(`(() => {
    const tr = document.getElementById("transcript");
    tr.innerHTML = \`
      <div class="msg msg-user">
        <div class="user-body md break-words"><p>Another prompt in regular transcript</p></div>
        <div class="msg-attachments flex flex-wrap justify-end gap-2 mt-1.5">
          <div class="attach-chip" style="width: 140px; height: 28px;">Document.pdf</div>
        </div>
        <div class="user-meta flex items-center justify-end gap-1.5 mt-1 text-faint text-[11px] font-mono">
          <span class="meta-time">11:00 AM</span>
          <button class="meta-copy-btn" style="width: 20px; height: 20px;">C</button>
        </div>
      </div>
    \`;
  })()`);
  await sleep(100);

  const trData = await evaluate(`(() => {
    const tr = document.getElementById("transcript");
    const msgUser = document.querySelector("#transcript .msg-user");
    const userBody = document.querySelector("#transcript .user-body");
    const attachments = document.querySelector("#transcript .msg-attachments");
    const userMeta = document.querySelector("#transcript .user-meta");
    const metaBtn = document.querySelector("#transcript .meta-copy-btn");
    const attachChip = document.querySelector("#transcript .attach-chip");

    const trRect = tr.getBoundingClientRect();
    const bodyRect = userBody.getBoundingClientRect();
    const metaRect = userMeta.getBoundingClientRect();
    const btnRect = metaBtn.getBoundingClientRect();
    const chipRect = attachChip.getBoundingClientRect();

    const msgUserRect = msgUser.getBoundingClientRect();
    const contentRightEdge = msgUserRect.right;

    return {
      trRectRight: trRect.right,
      contentRightEdge,
      userBody: {
        left: bodyRect.left,
        right: bodyRect.right,
        gapFromRightEdge: contentRightEdge - bodyRect.right
      },
      userMeta: {
        btnRight: btnRect.right,
        gapFromRightEdge: contentRightEdge - btnRect.right
      },
      attachments: {
        chipRight: chipRect.right,
        gapFromRightEdge: contentRightEdge - chipRect.right
      }
    };
  })()`);

  console.log("2. Standard transcript alignment metrics:\n", JSON.stringify(trData, null, 2));

  if (Math.abs(trData.userBody.gapFromRightEdge) > 2) {
    throw new Error(`.user-body is not right-aligned in #transcript (gap: ${trData.userBody.gapFromRightEdge}px)`);
  }
  if (Math.abs(trData.userMeta.gapFromRightEdge) > 2) {
    throw new Error(`.user-meta items are not right-aligned in #transcript (gap: ${trData.userMeta.gapFromRightEdge}px)`);
  }
  if (Math.abs(trData.attachments.gapFromRightEdge) > 2) {
    throw new Error(`.msg-attachments items are not right-aligned in #transcript (gap: ${trData.attachments.gapFromRightEdge}px)`);
  }

  console.log("ALL USER MESSAGE ELEMENTS RIGHT-ALIGNMENT TESTS PASSED!");

} finally {
  chrome.kill();
  relay.kill();
}
