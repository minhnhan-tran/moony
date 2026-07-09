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
let asstLabeled = false;                 // has this answer's "moony" label shown yet?
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
let upgradeStartVer = "";                // terminal version when Upgrade was clicked (to detect it came back)
let authUser = null;                     // signed-in user via the auth provider (/auth/me), when auth is on
let commandList = [];                    // slash commands for the palette
let paletteItems = [], paletteIdx = 0;   // current palette matches + highlighted row
let mentionList = [];                    // @-mention candidates (agents + workers)
let mentionItems = [], mentionIdx = 0;   // current @-mention matches + highlighted row
let curStep = 0, maxStep = 0;            // turn progress (tool-call loop)

const $ = (s, r = document) => r.querySelector(s);
const el = (tag, cls, txt) => { const e = document.createElement(tag); if (cls) e.className = cls; if (txt != null) e.textContent = txt; return e; };

// Icons are Material Symbols (Google), rendered by an <app-icon name="…"> custom
// element. Call sites still use the legacy SF-Symbol names; SF_TO_MATERIAL maps
// each to its Material Symbols ligature, which the element writes as its text so
// the Material Symbols font renders the glyph. Unmapped names fall back to "help".
const SF_TO_MATERIAL = {
  "alarm.fill": "alarm", "antenna.radiowaves.left.and.right": "sensors",
  "arrow.clockwise": "refresh", "arrow.counterclockwise": "refresh",
  "arrow.down": "arrow_downward", "arrow.down.circle": "arrow_circle_down",
  "arrow.left": "arrow_back", "arrow.right": "arrow_forward",
  "arrow.right.to.line": "east", "arrow.triangle.2.circlepath": "autorenew",
  "arrow.triangle.branch": "call_split", "arrow.trianglehead.branch": "call_split",
  "arrow.turn.down.right": "subdirectory_arrow_right", "arrow.up": "arrow_upward",
  "arrow.up.left.and.arrow.down.right": "open_in_full", "arrow.up.to.line": "north",
  "arrow.uturn.left": "undo", "arrow.uturn.right": "redo",
  "backward.end.fill": "skip_previous", "backward.fill": "fast_rewind",
  "battery.100": "battery_full", "bell.fill": "notifications", "bolt.fill": "bolt",
  "book.closed.fill": "book", "book.fill": "book", "books.vertical.fill": "library_books",
  "brain": "psychology", "brain.head.profile": "psychology",
  "bubble.left.and.bubble.right": "forum", "bubble.left.fill": "chat",
  "building.2.fill": "apartment", "calendar": "calendar_month",
  "calendar.badge.plus": "edit_calendar", "camera.fill": "photo_camera",
  "cart.fill": "shopping_cart", "chart.bar.fill": "bar_chart",
  "chart.line.downtrend.xyaxis": "trending_down", "chart.line.uptrend.xyaxis": "trending_up",
  "checklist": "checklist", "checkmark": "check", "checkmark.circle": "check_circle",
  "checkmark.circle.fill": "check_circle", "checkmark.shield.fill": "verified_user",
  "checkmark.square.fill": "check_box", "chevron.down": "expand_more",
  "chevron.left.forwardslash.chevron.right": "code", "chevron.right": "chevron_right",
  "chevron.up": "expand_less", "circle": "circle", "circle.dotted": "circle",
  "circle.fill": "circle", "circle.lefthalf.filled": "contrast", "clock": "schedule",
  "clock.arrow.circlepath": "history", "cloud.fill": "cloud", "cpu": "memory",
  "creditcard.fill": "credit_card", "curlybraces": "data_object", "desktopcomputer": "computer",
  "doc.on.clipboard": "content_paste", "doc.on.doc": "content_copy", "doc.text": "description",
  "dollarsign.circle.fill": "paid", "drop.fill": "water_drop", "envelope.fill": "mail",
  "exclamationmark": "priority_high", "exclamationmark.circle": "error",
  "exclamationmark.circle.fill": "error", "exclamationmark.triangle.fill": "warning",
  "eye.fill": "visibility", "flame.fill": "local_fire_department", "flowchart": "schema",
  "folder": "folder", "folder.fill": "folder", "forward.end.fill": "skip_next",
  "forward.fill": "fast_forward", "gauge.with.dots.needle.33percent": "speed",
  "gearshape": "settings", "gearshape.fill": "settings", "gift.fill": "card_giftcard",
  "globe": "public", "graduationcap.fill": "school", "hammer.fill": "build",
  "hand.raised.fill": "pan_tool", "hand.thumbsdown.fill": "thumb_down",
  "hand.thumbsup.fill": "thumb_up", "heart.fill": "favorite", "hourglass": "hourglass_empty",
  "house.fill": "home", "info.circle.fill": "info", "internaldrive.fill": "storage",
  "iphone": "smartphone", "key.fill": "key", "ladybug.fill": "bug_report",
  "laptopcomputer": "laptop", "lightbulb.fill": "lightbulb", "link": "link",
  "lock.fill": "lock", "lock.open.fill": "lock_open", "magnifyingglass": "search",
  "map.fill": "map", "mappin": "place", "megaphone.fill": "campaign", "minus": "remove",
  "moon.fill": "dark_mode", "moon.stars.fill": "dark_mode", "nosign": "block",
  "note.text": "note", "paintbrush.fill": "brush", "paintbrush.pointed.fill": "brush",
  "paperclip": "attach_file", "paperplane": "send", "paperplane.fill": "send",
  "pause.fill": "pause", "pencil": "edit", "person.2.fill": "group", "person.fill": "person",
  "person.text.rectangle": "badge", "phone.fill": "call", "photo": "image",
  "pin.fill": "push_pin", "play.fill": "play_arrow", "plus": "add",
  "plus.circle": "add_circle", "plus.circle.fill": "add_circle", "powerplug.fill": "power",
  "puzzlepiece.extension": "extension", "questionmark": "question_mark",
  "questionmark.bubble.fill": "quiz", "questionmark.circle": "help",
  "questionmark.circle.fill": "help", "rectangle.3.group": "dashboard",
  "rectangle.portrait.and.arrow.right": "logout", "scissors": "content_cut",
  "shield.fill": "shield", "shippingbox": "inventory_2", "shippingbox.fill": "inventory_2",
  "sidebar.left": "menu", "slash.circle": "block", "slider.horizontal.3": "tune",
  "smallcircle.filled.circle": "radio_button_checked", "sparkle": "auto_awesome",
  "sparkles": "auto_awesome", "speaker.wave.2.fill": "volume_up", "square": "crop_square",
  "square.and.arrow.down": "download", "square.and.arrow.up": "upload",
  "square.and.pencil": "edit_square", "square.fill": "square", "star.fill": "star",
  "stop.fill": "stop", "sun.max.fill": "light_mode", "tag.fill": "label",
  "target": "my_location", "timer": "timer", "trash": "delete", "trash.fill": "delete",
  "tray.full": "inbox", "trophy.fill": "emoji_events", "video.fill": "videocam",
  "wifi": "wifi", "wrench.and.screwdriver.fill": "handyman", "wrench.fill": "build",
  "xmark": "close", "xmark.circle": "cancel", "xmark.circle.fill": "cancel",
};

// MATERIAL_CP maps a Material Symbols name to its codepoint (hex). The vendored
// font (/static/fonts/material-symbols.woff2) is subset to exactly these glyphs,
// with layout/ligature tables stripped — so <app-icon> renders by codepoint, not
// by ligature. Regenerate both together (see scripts note) if icons are added.
const MATERIAL_CP = { "add": "e145", "add_circle": "e990", "alarm": "e855", "apartment": "ea40", "arrow_back": "e5c4", "arrow_circle_down": "f181", "arrow_downward": "e5db", "arrow_forward": "e5c8", "arrow_upward": "e5d8", "attach_file": "e226", "auto_awesome": "e65f", "autorenew": "e863", "badge": "ea67", "bar_chart": "e26b", "battery_full": "e1a5", "block": "f08c", "bolt": "ea0b", "book": "e86e", "brush": "e3ae", "bug_report": "e868", "build": "f8cd", "calendar_month": "ebcc", "call": "f0d4", "call_split": "e0b6", "campaign": "ef49", "cancel": "e888", "card_giftcard": "e8f6", "chat": "e0c9", "check": "e668", "check_box": "e9de", "check_circle": "f0be", "checklist": "e6b1", "chevron_right": "e5cc", "circle": "ef4a", "close": "e5cd", "cloud": "f15c", "code": "e86f", "computer": "e31e", "content_copy": "e14d", "content_cut": "e14e", "content_paste": "e14f", "contrast": "eb37", "credit_card": "e8a1", "crop_square": "e3c6", "dark_mode": "e51c", "dashboard": "e871", "data_object": "ead3", "delete": "e92e", "description": "e873", "download": "f090", "east": "f1df", "edit": "f097", "edit_calendar": "e742", "edit_square": "f88d", "emoji_events": "ea23", "error": "f8b6", "expand_less": "e5ce", "expand_more": "e5cf", "extension": "e87b", "fast_forward": "e01f", "fast_rewind": "e020", "favorite": "e87e", "folder": "e2c7", "forum": "e8af", "group": "ea21", "handyman": "f10b", "help": "e8fd", "history": "e8b3", "home": "e9b2", "hourglass_empty": "e88b", "image": "e3f4", "inbox": "e156", "info": "e88e", "inventory_2": "e1a1", "key": "e73c", "label": "e893", "laptop": "e31e", "library_books": "e02f", "light_mode": "e518", "lightbulb": "e90f", "link": "e250", "local_fire_department": "ef55", "lock": "e899", "lock_open": "e898", "logout": "e9ba", "mail": "e159", "map": "e55b", "memory": "e322", "menu": "e5d2", "my_location": "e55c", "north": "f1e0", "note": "e674", "notifications": "e7f5", "open_in_full": "f1ce", "paid": "f041", "pan_tool": "e925", "pause": "e034", "person": "f0d3", "photo_camera": "e412", "place": "f1db", "play_arrow": "e037", "power": "e63c", "priority_high": "e645", "psychology": "ea4a", "public": "e80b", "push_pin": "f10d", "question_mark": "eb8b", "quiz": "f04c", "radio_button_checked": "e837", "redo": "e15a", "refresh": "e5d5", "remove": "e15b", "schedule": "efd6", "schema": "e4fd", "school": "e80c", "search": "ef7a", "send": "e163", "sensors": "e51e", "settings": "e8b8", "shield": "e9e0", "shopping_cart": "e8cc", "skip_next": "e044", "skip_previous": "e045", "smartphone": "e7ba", "speed": "e9e4", "square": "eb36", "star": "f09a", "stop": "e047", "storage": "e1db", "subdirectory_arrow_right": "e5da", "thumb_down": "f578", "thumb_up": "f577", "timer": "e425", "trending_down": "e8e3", "trending_up": "e8e5", "tune": "e429", "undo": "e166", "upload": "f09b", "verified_user": "f013", "videocam": "e04b", "visibility": "e8f4", "volume_up": "e050", "warning": "f083", "water_drop": "e798", "wifi": "e63e" };

// AppIcon renders a Material Symbols glyph from a legacy SF-Symbol `name`, keeping
// the name→glyph mapping in one place so call sites (and the dynamic
// setAttribute("name", …) swaps) are unchanged. It writes the glyph's codepoint
// as its text content (the vendored font carries no ligatures) and toggles the
// FILL axis for ".fill" names.
class AppIcon extends HTMLElement {
  static get observedAttributes() { return ["name"]; }
  connectedCallback() { this._render(); }
  attributeChangedCallback() { this._render(); }
  _render() {
    const n = this.getAttribute("name") || "";
    const cp = MATERIAL_CP[SF_TO_MATERIAL[n] || "help"] || MATERIAL_CP["help"];
    this.textContent = cp ? String.fromCodePoint(parseInt(cp, 16)) : "";
    this.style.fontVariationSettings = `'FILL' ${n.indexOf(".fill") !== -1 ? 1 : 0}, 'wght' 400, 'GRAD' 0, 'opsz' 24`;
  }
}
if (!customElements.get("app-icon")) customElements.define("app-icon", AppIcon);

// sym builds an icon element. Size it via font-size (inline style or a text-*
// class) and color it via the inherited `color`.
const sym = (name, cls) => { const s = document.createElement("app-icon"); s.setAttribute("name", name); if (cls) s.className = cls; return s; };
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

