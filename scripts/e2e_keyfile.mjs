// e2e keyfile driver: drives real Chrome over CDP through
// register -> enrol keyfile (download) -> logout -> keyfile login -> revoke ->
// keyfile login refused. Run: node scripts/e2e_keyfile.mjs http://127.0.0.1:8788
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const BASE = process.argv[2] || "http://127.0.0.1:8788";
const CHROME = "C:/Program Files/Google/Chrome/Application/chrome.exe";
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "kf-prof-"));
const downloads = fs.mkdtempSync(path.join(os.tmpdir(), "kf-dl-"));
const PORT = 9333;

const chrome = spawn(CHROME, [
  "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run",
  `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, id = 0;
const pending = new Map();
const logs = [];

async function connect() {
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
          } else if (msg.method === "Log.entryAdded") {
            logs.push("log/" + msg.params.entry.level + ": " + msg.params.entry.text);
          }
        };
        return;
      }
    } catch { /* chrome not up yet */ }
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

function keyfileOnDisk() {
  const files = fs.readdirSync(downloads).filter((f) => f.endsWith(".json"));
  if (!files.length) return null;
  return path.join(downloads, files[0]);
}

try {
  await connect();
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Log.enable");
  await send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: downloads });
  // The revoke button asks for confirmation; auto-accept it.
  await send("Page.addScriptToEvaluateOnNewDocument", { source: "window.confirm = () => true;" });

  // 1. register
  await navigate(BASE + "/auth/register");
  await evaluate(`(() => {
    document.querySelector("#username").value = "erika";
    document.querySelector("#password").value = "correct horse battery";
    document.querySelector("#confirm").value = "correct horse battery";
    document.querySelector("form").submit();
  })()`);
  await sleep(1500);
  const afterRegister = await evaluate("location.pathname");
  check("register lands on the app", !afterRegister.startsWith("/auth/register"), afterRegister);

  // 2. account page shows the keyfile UI, enrol one
  await navigate(BASE + "/auth/password");
  check("account page has the enrol form", await evaluate(`!!document.querySelector("#keyfile_enrol")`));
  check("keyfile.js loaded", await evaluate(`!!document.querySelector('script[src="/auth/keyfile.js"]')`));
  check("no keys listed yet", await evaluate(`document.querySelectorAll("[data-key-id]").length`) === 0);
  await evaluate(`(() => {
    document.querySelector("#key_label").value = "e2e laptop";
    document.querySelector("#key_pass").value = "passphrase-for-e2e";
    document.querySelector("#key_pass2").value = "passphrase-for-e2e";
    document.querySelector("#keyfile_enrol button[type=submit]").click();
  })()`);
  for (let i = 0; i < 60 && !keyfileOnDisk(); i++) await sleep(250);
  const file = keyfileOnDisk();
  check("keyfile downloaded", !!file, file || "no file appeared");
  const status = await evaluate(`document.querySelector("#keyfile_enrol_status").textContent`);
  check("enrol reported success", /Keyfile downloaded/.test(status), status);
  if (file) {
    const doc = JSON.parse(fs.readFileSync(file, "utf8"));
    check("keyfile is the expected format", doc.type === "moony-keyfile" && doc.v === 1 &&
      doc.kdf?.iterations >= 200000 && !!doc.key && !!doc.kdf.salt && !!doc.cipher.iv,
      JSON.stringify({ v: doc.v, iters: doc.kdf?.iterations, account: doc.account }));
    check("keyfile carries no private key in the clear",
      !JSON.stringify(doc).includes("PRIVATE") && !("d" in doc));
  }
  await navigate(BASE + "/auth/password");
  check("key is listed after enrolment", await evaluate(`document.querySelectorAll("[data-key-id]").length`) === 1);

  // 3. log out, then log in with the keyfile
  await navigate(BASE + "/auth/logout");
  await navigate(BASE + "/auth/login");
  check("login page offers keyfile login", await evaluate(`!!document.querySelector("#keyfile_login")`));
  const node = await send("DOM.getDocument");
  const input = await send("DOM.querySelector", { nodeId: node.root.nodeId, selector: "#keyfile_file" });
  await send("DOM.setFileInputFiles", { files: [file], nodeId: input.nodeId });
  await evaluate(`(() => {
    document.querySelector("#username").value = "erika";
    document.querySelector("#keyfile_pass").value = "passphrase-for-e2e";
    document.querySelector("#keyfile_login button[type=submit]").click();
  })()`);
  await sleep(3000);
  const afterKeyfile = await evaluate("location.pathname");
  check("keyfile login left the login page", !afterKeyfile.startsWith("/auth/login"), afterKeyfile);
  await navigate(BASE + "/auth/password");
  const who = await evaluate(`(document.body.dataset.account || "")`);
  check("keyfile login produced a session for the right account", who === "local:erika", who);

  // 4. wrong passphrase is refused
  await navigate(BASE + "/auth/logout");
  await navigate(BASE + "/auth/login");
  const node2 = await send("DOM.getDocument");
  const input2 = await send("DOM.querySelector", { nodeId: node2.root.nodeId, selector: "#keyfile_file" });
  await send("DOM.setFileInputFiles", { files: [file], nodeId: input2.nodeId });
  await evaluate(`(() => {
    document.querySelector("#username").value = "erika";
    document.querySelector("#keyfile_pass").value = "not-the-passphrase";
    document.querySelector("#keyfile_login button[type=submit]").click();
  })()`);
  await sleep(2500);
  const badStatus = await evaluate(`document.querySelector("#keyfile_login_status").textContent`);
  check("wrong passphrase is reported, not accepted", /Wrong passphrase/.test(badStatus), badStatus);

  // 5. log back in with the keyfile, revoke it, then it must not work again
  const node3 = await send("DOM.getDocument");
  const input3 = await send("DOM.querySelector", { nodeId: node3.root.nodeId, selector: "#keyfile_file" });
  await send("DOM.setFileInputFiles", { files: [file], nodeId: input3.nodeId });
  await evaluate(`(() => {
    document.querySelector("#keyfile_pass").value = "passphrase-for-e2e";
    document.querySelector("#keyfile_login button[type=submit]").click();
  })()`);
  await sleep(3000);
  await navigate(BASE + "/auth/password");
  await evaluate(`document.querySelector("[data-key-id]").click()`);
  await sleep(1500);
  const revokeStatus = await evaluate(`document.querySelector("#keyfile_keys_status").textContent`);
  check("revoke reported success", /revoked/i.test(revokeStatus), revokeStatus);
  await navigate(BASE + "/auth/password");
  check("key list is empty after revoke", await evaluate(`document.querySelectorAll("[data-key-id]").length`) === 0);

  await navigate(BASE + "/auth/logout");
  await navigate(BASE + "/auth/login");
  const node4 = await send("DOM.getDocument");
  const input4 = await send("DOM.querySelector", { nodeId: node4.root.nodeId, selector: "#keyfile_file" });
  await send("DOM.setFileInputFiles", { files: [file], nodeId: input4.nodeId });
  await evaluate(`(() => {
    document.querySelector("#username").value = "erika";
    document.querySelector("#keyfile_pass").value = "passphrase-for-e2e";
    document.querySelector("#keyfile_login button[type=submit]").click();
  })()`);
  await sleep(2500);
  const revokedStatus = await evaluate(`document.querySelector("#keyfile_login_status").textContent`);
  check("a revoked keyfile cannot log in", /not accepted|invalid/i.test(revokedStatus), revokedStatus);
  check("still on the login page", (await evaluate("location.pathname")).startsWith("/auth/login"));
} catch (err) {
  steps.push("ERROR " + (err && err.stack ? err.stack : err));
  process.exitCode = 1;
} finally {
  console.log(steps.join("\n"));
  const noisy = logs.filter((l) => /error|Error|refused|failed/.test(l));
  if (noisy.length) console.log("\nbrowser log:\n" + noisy.join("\n"));
  chrome.kill();
}
