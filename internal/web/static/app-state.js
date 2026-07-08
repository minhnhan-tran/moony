// app-state.js — shared top-level state + small DOM helpers + confirmBox.
// NOTE: this and its sibling app-*.js files are CLASSIC (non-module) scripts that
// share ONE global scope. They MUST load in order (state → render → core → manage);
// see index.html. Do not add `type=module` or the cross-file references break.

// Browser logic for the SQE Code Agent web UI. Extracted from index.html
// and served from the embedded static dir at /static/app.js. The tag sits at
// the end of <body> with `defer`, so it runs after the markup it wires up.

"use strict";
let current = null, es = null, state = "idle";
let httpSessions = [], projectSessions = null; // sidebar: server sessions vs the terminal project's saved sessions
let laptops = null;                              // [{host,name,base}] in tree mode; null in mirror mode
let expandedLaptops = new Set();                 // hosts whose project list is expanded
let laptopProjects = {};                         // host -> [projects] (lazy-loaded)
let expandedProjects = new Set();                // "host\nkey" of projects whose session list is expanded
let projectSessionsCache = {};                   // "host\nkey" -> its saved sessions (lazy-loaded)
let currentLaptop = null, currentProject = null; // focused laptop host + project key
let apiBase = "";                                // API path prefix for the focused laptop ("" or "/l/{host}")
let laptopPollTimer = null;                      // tree-mode poll that reflects connector connect/disconnect live
let menuLoadingTimer = null;                      // safety timeout that clears a stuck #menu loader if no menu event arrives
let asstBody = null, asstRaw = "";       // current streaming assistant block
let asstLabeled = false;                 // has this answer's "sqe code agent" label shown yet?
let turnTodos = null;                    // per-question todo card recorded in the transcript (history)
let phaseTimes = {};                     // per-turn phase timing (index → {start, dur}); client-side, live turns only
let toolGroup = null;                    // current run of consecutive tool calls → one "N tool calls" disclosure
let workerGroup = null;                  // current run of delegated-worker progress lines → one "N worker updates" disclosure
let turnStartMs = 0, turnActive = false; // turn timing for the per-answer meta footer
let lastTurnCost = 0;                    // this turn's answering cost (from turn_done), for the footer
let lastTurnModel = "";                  // this turn's actual serving model (from turn_done); differs from the status model on an adaptive/cheap-reads downshift
let lastTurnIn = 0, lastTurnOut = 0, lastTurnCacheRead = 0; // this turn's total tokens + cached-input subset (from turn_done), for the footer
let cumTurnIn = 0, cumTurnOut = 0; // running session totals summed from per-turn meta (replayed + live), so the status bar matches the turn-meta footers even after a reopen
let stickBottom = true;                  // auto-scroll only when the user is already at the bottom
let thinkBody = null;                    // current streaming thinking block
let tools = {};                          // toolId -> { el, hd, pre }
let lastStatus = null;                   // latest status-bar snapshot from the terminal
let usage = { skill: {}, agent: {}, worker: {}, mcp: {}, builtin: {} }; // per-session usage counts (category → name → uses), rendered in #mcp
let boardMsgs = []; // shared agent message-board posts this session (from → to: text), rendered in #agentmsgs
let relayVersion = "";                   // this relay's build version (GET /api/relay); "" in mirror/standalone mode
let relayDownloads = false;              // relay offers CLI downloads (/download + /install.sh)
let announcements = [];                  // maintainer announcement banners (GET /api/relay)
let isRelayAdmin = false;                // current user may author announcements (MOONY_ADMIN_USERS)
let versionWarnDismissed = false;        // user closed the "terminal is behind" banner this page load
let upgrading = false;                   // an Upgrade (remote /update) is in flight
let sqeProxyRefreshing = false;          // a provider-access membership-refresh POST is in flight (dedupes a burst of denials)
let sqeProxyRefreshAt = 0;               // ms timestamp of the last auto-refresh (cooldown guard against retry loops)
let sqeProxyGatePrimed = false;          // we've primed the relay's group-gate store this page-session (refresh records membership server-side); reset on a provider switch
let sqeProxyPrimePromise = null;         // in-flight background gate-prime (POST /api/refresh-membership) promise, so send() awaits/dedupes it rather than blocking on or re-firing its own
let lastUserSend = null;                 // {text, attachments} of the last turn THIS browser sent, for transparent resend after a membership refresh
let suppressNextUserEcho = false;        // set when transparently resending a turn after a provider-access refresh, so its user-bubble echo + the failed turn's footer aren't duplicated
let lastProviderSwitch = "";             // the provider id the user last tried to switch to (for retry after a membership refresh)
let upgradeStartVer = "";                // terminal version when Upgrade was clicked (to detect it came back)
let authUser = null;                     // signed-in user via the auth provider (/auth/me), when auth is on
let commandList = [];                    // slash commands for the palette
let paletteItems = [], paletteIdx = 0;   // current palette matches + highlighted row
let mentionList = [];                    // @-mention candidates (agents + workers)
let mentionItems = [], mentionIdx = 0;   // current @-mention matches + highlighted row
let curStep = 0, maxStep = 0;            // turn progress (tool-call loop)

const $ = (s, r = document) => r.querySelector(s);
const el = (tag, cls, txt) => { const e = document.createElement(tag); if (cls) e.className = cls; if (txt != null) e.textContent = txt; return e; };
// sym builds an SF Symbol icon element. Size it via font-size (inline style or
// a text-* class) and color it via the inherited `color`.
const sym = (name, cls) => { const s = document.createElement("sf-symbol"); s.setAttribute("name", name); if (cls) s.className = cls; return s; };
// themeColor reads a palette token (e.g. "--c-brand") from the active theme and
// returns it as an rgb() string, so colors set via inline styles in JS follow
// the light/dark switch. The tokens are space-separated channels (see app.css);
// callers re-read on each render (and on a theme toggle we re-render).
const themeColor = (name) => `rgb(${getComputedStyle(document.documentElement).getPropertyValue(name).trim() || "0 0 0"})`;

// confirmBox is a promise-based replacement for the browser-native confirm():
// it renders an in-app modal styled to match the UI and resolves to true when
// the user accepts, false otherwise (cancel / backdrop click / Escape). Opts:
// { title, okText, cancelText, danger } — danger styles the accept button red.
// Use as: if (!(await confirmBox("Delete this?"))) return;
function confirmBox(message, opts = {}) {
  return new Promise((resolve) => {
    const { title = "Confirm", okText = "OK", cancelText = "Cancel", danger = false } = opts;
    const back = el("div", "msgbox-back");
    const box = el("div", "msgbox");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-modal", "true");
    const h = el("div", "msgbox-title", title);
    const body = el("div", "msgbox-body", message);
    const row = el("div", "msgbox-actions");
    const cancel = el("button", "msgbox-btn", cancelText);
    const ok = el("button", "msgbox-btn msgbox-ok" + (danger ? " msgbox-danger" : ""), okText);
    row.append(cancel, ok);
    box.append(h, body, row);
    back.append(box);
    let done = false;
    const close = (val) => {
      if (done) return; done = true;
      document.removeEventListener("keydown", onKey, true);
      back.remove();
      resolve(val);
    };
    const onKey = (e) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(false); }
      else if (e.key === "Enter") { e.preventDefault(); e.stopPropagation(); close(true); }
    };
    cancel.addEventListener("click", () => close(false));
    ok.addEventListener("click", () => close(true));
    back.addEventListener("mousedown", (e) => { if (e.target === back) close(false); });
    document.addEventListener("keydown", onKey, true);
    document.body.appendChild(back);
    ok.focus();
  });
}

// alertBox is an in-app replacement for window.alert: a single-OK modal in the
// same msgbox-back chrome as confirmBox (so backend errors and notices match the
// UI instead of a jarring browser dialog). Returns a Promise that resolves when
// dismissed (OK / backdrop / Escape / Enter). Opts: { title, okText, danger }.
function alertBox(message, opts = {}) {
  return new Promise((resolve) => {
    const { title = "Notice", okText = "OK", danger = false } = opts;
    const back = el("div", "msgbox-back");
    const box = el("div", "msgbox");
    box.setAttribute("role", "alertdialog");
    box.setAttribute("aria-modal", "true");
    const h = el("div", "msgbox-title", title);
    const body = el("div", "msgbox-body", message);
    const row = el("div", "msgbox-actions");
    const ok = el("button", "msgbox-btn msgbox-ok" + (danger ? " msgbox-danger" : ""), okText);
    row.append(ok);
    box.append(h, body, row);
    back.append(box);
    let done = false;
    const close = () => {
      if (done) return; done = true;
      document.removeEventListener("keydown", onKey, true);
      back.remove();
      resolve();
    };
    const onKey = (e) => {
      if (e.key === "Escape" || e.key === "Enter") { e.preventDefault(); e.stopPropagation(); close(); }
    };
    ok.addEventListener("click", close);
    back.addEventListener("mousedown", (e) => { if (e.target === back) close(); });
    document.addEventListener("keydown", onKey, true);
    document.body.appendChild(back);
    ok.focus();
  });
}

const transcript = $("#transcript"), statusEl = $("#status"), statsEl = $("#stats"), approval = $("#approval");
const questionEl = $("#question");
const hdrVersionEl = $("#hdrVersion"), hdrMetaEl = $("#hdrMeta"), hdrEncryptEl = $("#hdrEncrypt");
const hdrMenuEl = $("#hdrMenu"); // shared body-level picker for header/composer tokens (approval, reasoning)
const todosEl = $("#todos"), filesEl = $("#files"), mcpEl = $("#mcp");
const agentMsgsEl = $("#agentmsgs");
const planEl = $("#plan");
const versionWarnEl = $("#versionWarn"), versionWarnTextEl = $("#versionWarnText");
const versionWarnUpgradeEl = $("#versionWarnUpgrade"), versionWarnDownloadEl = $("#versionWarnDownload");
const versionWarnIconEl = $("#versionWarnIcon"), versionWarnArtEl = $("#versionWarnArt");
let todosExpanded = false; // user has manually expanded a fully-completed todo list
let planExpanded = false; // user has manually expanded the plan step list
let currentPlan = null; // latest plan artifact; the Tasks bar stays hidden while a draft plan awaits the user's confirmation to execute
let draftPlanEl = null; // inline transcript block showing a DRAFT plan in full while it awaits approval (see renderDraftPlanInline); the compact #plan bar is hidden until it's approved
let draftPlanExpanded = false; // sticky expand state for the inline draft plan; default COLLAPSED (just a one-line summary until the user expands)
let lastTodos = []; // most recent todos payload, re-rendered when the plan's approval flips so the bar reveals on confirm
const input = $("#input"), sendBtn = $("#send"), paletteEl = $("#palette"), menuEl = $("#menu");
const composerAgentEl = $("#composerAgent"); // primary-agent name notched into the composer border
if (composerAgentEl) {
  // Clicking (or Enter/Space) the legend opens the /agent picker, mirroring the
  // header badge. Wired once; renderComposerAgent only updates its text/title.
  const switchAgent = () => { if (composerAgentEl.textContent) sendText("/agent"); };
  composerAgentEl.addEventListener("click", switchAgent);
  composerAgentEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); switchAgent(); }
  });
}
const composerMetaEl = $("#composerMeta"); // model · reasoning notched into the composer's top-right
if (composerMetaEl) {
  // Clicking (or Enter/Space) opens the model picker (/models). Show a
  // jumping-Claude loader in #menu immediately — fetching models can be slow
  // (it hits the model endpoints / the relay proxy) — replaced by the menu when ready.
  const pickModel = () => {
    if (!composerMetaEl.childElementCount) return;
    if (typeof showMenuLoading === "function") showMenuLoading("Loading models…");
    sendText("/models");
  };
  composerMetaEl.addEventListener("click", pickModel);
  composerMetaEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pickModel(); }
  });
}
const mentionsEl = $("#mentions");
const scrollBtn = $("#scrollBtn");
const imageInput = $("#imageInput"), docInput = $("#docInput"), attachBtn = $("#attachBtn"), attachMenu = $("#attachMenu"), attachStrip = $("#attachStrip");
const queueStrip = $("#queueStrip");
// queueCapable: the current session supports queuing messages while busy (set
// from the "state" event's canQueue flag — web-native sessions only, not the
// relay terminal-mirror). queuedMsgs mirrors the server's pending queue.
let queueCapable = false;
let queuedMsgs = [];
const userAvatarEl = $("#userAvatar"), userMenuEl = $("#userMenu");
const themeToggleEl = $("#themeToggle");
const soundToggleEl = $("#soundToggle");
// soundEnabled: play a short chime when a turn finishes in this browser session.
// Persisted per-browser in localStorage; defaults ON (matches the TUI chime).
let soundEnabled = localStorage.getItem("sqe-sound") !== "off";
let pendingAttachments = []; // staged for the next message: {name, mediaType, data(base64), kind, dataUri?}

// --- markdown rendering -----------------------------------------------------
// marked parses; a small DOM-based sanitizer (mirroring the team's core.js
// sanitizeHtml) strips dangerous tags/attrs. No external sanitizer dependency,
// so nothing here needs a CDN.
const SAN_DROP_TAGS = new Set(["SCRIPT", "STYLE", "IFRAME", "OBJECT", "EMBED", "LINK", "META", "BASE", "FORM", "INPUT", "BUTTON", "TEXTAREA", "SELECT", "OPTION"]);
const SAN_URL_ATTRS = new Set(["href", "src", "xlink:href", "action", "formaction", "background", "cite", "poster"]);

