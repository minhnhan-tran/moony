// app-core.js — API/auth/session loading, the SSE event stream, the sidebar/laptop
// tree, attention, palette, and the main turn loop. Shares global scope; loads after render.

const api = (path) => apiBase + path;

// --- session-expiry guard ---------------------------------------------------
// When the auth provider is on (MN_AUTH), an expired OIDC session makes the
// server answer API calls with 401 JSON (a full-page navigation gets a 302 to
// /auth/login, but our fetch/SSE calls don't). This SPA stays open long after
// login — well past the session TTL — so a stale page would otherwise just see
// its calls fail silently. We wrap fetch so ANY 401 bounces the browser to
// /auth/login, which redirects on to the auth provider's login (and back to
// where we were via ?next=). Auth-off builds never return 401, so this is inert.
let authBouncing = false; // one redirect even if many calls 401 at once
function bounceToLogin() {
  if (authBouncing) return;
  authBouncing = true;
  location.href = "/auth/login?next=" + encodeURIComponent(location.pathname + location.search);
}
const _fetch = window.fetch.bind(window);
window.fetch = async (input, init) => {
  const resp = await _fetch(input, init);
  if (resp.status === 401) bounceToLogin();
  return resp;
};

const laptopByHost = (host) => (laptops || []).find((l) => l.host === host) || null;
const laptopBase = (host) => { const l = laptopByHost(host); return l ? l.base : ""; };
// pkey namespaces a project key by laptop, so two laptops with the same project
// key don't collide in the expanded/cache maps.
const pkey = (host, key) => host + "\n" + key;

async function loadSessions() {
  // Mirror-mode only: in tree mode the laptop tree (renderLaptopTree) owns the
  // sidebar and /api/sessions isn't served, so skip it (setState calls this on
  // every state change). Guard the response too — a 404's HTML body isn't JSON,
  // so an unguarded r.json() would throw an uncaught SyntaxError.
  if (laptops) return;
  let sessions = [];
  try {
    const r = await fetch("/api/sessions");
    if (r.ok) sessions = await r.json();
  } catch (_) { /* endpoint missing / non-JSON body — degrade to no sessions */ }
  httpSessions = Array.isArray(sessions) ? sessions : [];
  renderSessionList();
  // Auto-open the lone session (e.g. the terminal mirror's live stream) so the
  // page is live immediately. Guarded by !current to avoid re-entrancy.
  if (!current && httpSessions.length === 1) selectSession(httpSessions[0].id);
}

// loadRelayVersion records the relay's build version (GET /api/relay) so the
// header can warn when the connected terminal is behind it. The endpoint is
// relay-only: in mirror/standalone mode it 404s and we leave relayVersion "".
async function loadRelayVersion() {
  try {
    const r = await fetch("/api/relay");
    if (r.ok) {
      const j = await r.json();
      relayVersion = (j && j.version) || "";
      relayDownloads = !!(j && j.downloads);
      announcements = (j && Array.isArray(j.announcements)) ? j.announcements : [];
      isRelayAdmin = !!(j && j.admin);
      // Front-end asset skew: index.html/app-*.js/app.css are served static from
      // the relay's embedded FS, so a running page keeps executing the OLD assets
      // after the relay is redeployed/self-updated (the SSE stream reconnects,
      // but the JS/CSS don't re-fetch). Capture the version this page loaded
      // against; if a later poll reports a newer relay, prompt a reload.
      if (relayVersion) {
        if (!relayVersionAtLoad) relayVersionAtLoad = relayVersion;
        else if (versionLt(relayVersionAtLoad, relayVersion)) showReloadPrompt(relayVersion);
      }
    }
  } catch (_) { /* no relay in front → no version skew to report */ }
  renderAnnouncementBar();
  // Reveal the "Download terminal app" menu item only when the relay serves it.
  const dl = $("#umDownload");
  if (dl) dl.style.display = relayDownloads ? "" : "none";
  setHeaderVersion(); // show the version in the header immediately — no session/laptop needed
}

// showReloadPrompt reveals the "new version — reload" banner once the relay has
// moved ahead of the assets this page is running. Idempotent (repeated polls
// just refresh the target version) and stays until the user reloads or dismisses
// it — we never auto-reload out from under active work.
//
// Suppressed while the "your terminal is behind" banner is up: a relay deploy
// puts BOTH the page assets and the local CLI behind at once, and two stacked
// version banners read as redundant. The terminal one wins because its action
// (Upgrade) isn't reachable any other way; the reload is remembered in
// reloadPromptVersion and surfaced by refreshReloadBanner() when it clears.
function showReloadPrompt(version) {
  const bar = $("#reloadBanner");
  if (!bar || bar.dataset.dismissed === "1") return;
  reloadPromptVersion = version;
  if (terminalBannerVisible()) { bar.classList.add("hidden"); return; }
  const txt = $("#reloadBannerText");
  if (txt) txt.textContent = "A new version (v" + version + ") is available. Reload to update.";
  bar.classList.remove("hidden");
}

// terminalBannerVisible reports whether the "terminal is behind" banner is on
// screen, so the reload banner can stand down behind it.
function terminalBannerVisible() {
  return !!versionWarnEl && !versionWarnEl.classList.contains("hidden");
}

// refreshReloadBanner re-evaluates the held-back reload prompt. Called whenever
// the terminal banner's visibility changes, so dismissing or resolving it
// surfaces the reload notice instead of losing it.
function refreshReloadBanner() {
  if (reloadPromptVersion) showReloadPrompt(reloadPromptVersion);
}

// --- session encryption-at-rest indicator ---------------------------------

// loadEncryptionStatus fetches whether session history is encrypted at rest and
// renders a lock glyph in the topbar (GET /api/encryption). Best-effort: on any
// error the indicator is simply hidden.
async function loadEncryptionStatus() {
  if (!hdrEncryptEl) return;
  try {
    const r = await fetch(api("/api/encryption")); // connector-scoped: prefix the focused laptop path so the relay proxies it (bare "/api/encryption" 404s at the relay)
    if (!r.ok) throw new Error("status " + r.status);
    const j = await r.json();
    hdrEncryptEl.innerHTML = "";
    const on = !!j.encrypted;
    const icon = sym(on ? "lock.fill" : "lock.open");
    icon.style.fontSize = "13px";
    hdrEncryptEl.appendChild(icon);
    hdrEncryptEl.classList.toggle("text-ok", on);
    hdrEncryptEl.classList.toggle("text-faint", !on);
    hdrEncryptEl.title = on
      ? `Session history encrypted at rest (AES-256-GCM, mode: ${j.mode || "auto"})`
      : "Session history NOT encrypted (plaintext) — set MN_SESSION_ENCRYPT / a key in Persona → environment";
  } catch {
    hdrEncryptEl.innerHTML = "";
    hdrEncryptEl.title = "";
  }
}

// --- maintainer announcement banners --------------------------------------

// dismissed announcement ids (per browser) so a banner doesn't nag after the
// user closes it; the full history stays available in the Announcement panel.
function dismissedAnnouncements() {
  try { return new Set(JSON.parse(localStorage.getItem("announce.dismissed") || "[]")); }
  catch (_) { return new Set(); }
}
function dismissAnnouncement(id) {
  const s = dismissedAnnouncements(); s.add(id);
  try { localStorage.setItem("announce.dismissed", JSON.stringify([...s])); } catch (_) {}
  renderAnnouncementBar();
}

// announceBodyEl renders an announcement body as sanitized markdown (maintainers
// may use lists, links, code, emphasis…), reusing the transcript's renderMD
// pipeline (which sanitizes, so admin-authored HTML can't inject script).
function announceBodyEl(text) {
  const b = el("div", "announce-body md");
  b.innerHTML = renderMD(text);
  return b;
}

// renderAnnouncementBar paints #announceBar (in the main column, like versionWarn)
// with the newest undismissed announcements, each with a dismiss (×).
// syncAdminMenuVisibility shows #umAdmin (Announcements + Members) for anyone
// who's an admin by either flag: isRelayAdmin (MN_ADMIN_USERS, announcement
// authoring) or authUser.is_admin (MN_AUTH_GROUP_DSID, member management) —
// the two tabs inside stay independently useful even if only one flag is set.
function syncAdminMenuVisibility() {
  const um = $("#umAdmin");
  // Hide via inline display (like #umDownload) — NOT the .hidden class, which
  // loses to .user-menu-item's display:flex (app.css loads after tailwind.css),
  // so a class toggle would leave the item visible for non-admins.
  if (um) um.style.display = (isRelayAdmin || (authUser && authUser.is_admin)) ? "" : "none";
}

function renderAnnouncementBar() {
  syncAdminMenuVisibility();
  const bar = $("#announceBar");
  if (!bar) return;
  const dismissed = dismissedAnnouncements();
  const visible = (announcements || []).filter((a) => a && a.id && !dismissed.has(a.id));
  bar.innerHTML = "";
  if (!visible.length) { bar.classList.add("hidden"); return; }
  bar.classList.remove("hidden");
  for (const a of visible) {
    const card = el("div", "announce-item announce-" + (a.level || "info"));
    const main = el("div", "announce-main");
    if (a.title) main.appendChild(el("div", "announce-title", a.title));
    if (a.body) main.appendChild(announceBodyEl(a.body));
    const meta = [];
    if (a.created) { const d = new Date(a.created); if (!isNaN(d)) meta.push(d.toLocaleString()); }
    if (meta.length) main.appendChild(el("div", "announce-meta", meta.join(" · ")));
    card.appendChild(main);
    const x = el("button", "announce-close"); x.title = "Dismiss"; x.textContent = "×";
    x.onclick = () => dismissAnnouncement(a.id);
    card.appendChild(x);
    bar.appendChild(card);
  }
}

// openAnnouncements shows the Announcement panel (userMenu → Announcement):
// the maintainer update feed, plus an inline compose/delete UI for admins.
function renderAnnouncementsPanel(body) {
  if (!body) return;
  body.innerHTML = "";
  const list = el("div", "announce-list");
  const items = announcements || [];
  if (!items.length) {
    list.appendChild(el("div", "announce-empty", "No announcements."));
  }
  for (const a of items) {
    if (!a || !a.id) continue;
    const card = el("div", "announce-item announce-" + (a.level || "info"));
    const main = el("div", "announce-main");
    if (a.title) main.appendChild(el("div", "announce-title", a.title));
    if (a.body) main.appendChild(announceBodyEl(a.body));
    const meta = [];
    if (a.created) { const d = new Date(a.created); if (!isNaN(d)) meta.push(d.toLocaleString()); }
    if (meta.length) main.appendChild(el("div", "announce-meta", meta.join(" · ")));
    card.appendChild(main);
    if (isRelayAdmin) {
      const del = el("button", "announce-btn"); del.title = "Delete for everyone"; del.textContent = "delete";
      del.onclick = () => deleteAnnouncement(a.id);
      const actions = el("div", "announce-actions"); actions.appendChild(del);
      card.appendChild(actions);
    }
    list.appendChild(card);
  }
  body.appendChild(list);
  if (isRelayAdmin) body.appendChild(announceComposer());
}

// announceComposer builds the admin-only "new announcement" control.
function announceComposer() {
  const wrap = el("div", "announce-compose");
  const toggle = el("button", "announce-btn announce-new"); toggle.textContent = "＋ New announcement";
  const form = el("div", "announce-form hidden");
  const title = el("input", "announce-input"); title.placeholder = "Title";
  const body = el("textarea", "announce-input announce-textarea"); body.placeholder = "Details (optional)";
  const level = el("select", "announce-input");
  for (const [v, label] of [["info", "Info"], ["warn", "Warning"], ["critical", "Critical"]]) {
    const o = el("option", null, label); o.value = v; level.appendChild(o);
  }
  const post = el("button", "announce-btn announce-post"); post.textContent = "Post";
  post.onclick = async () => {
    if (!title.value.trim() && !body.value.trim()) return;
    await postAnnouncement({ title: title.value.trim(), body: body.value.trim(), level: level.value });
    title.value = ""; body.value = "";
    form.classList.add("hidden");
  };
  toggle.onclick = () => { form.classList.toggle("hidden"); if (!form.classList.contains("hidden")) title.focus(); };
  form.append(title, body, level, post);
  wrap.append(toggle, form);
  return wrap;
}

async function postAnnouncement(payload) {
  try {
    const r = await fetch("/api/announcements", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    if (!r.ok) { alert("Post failed: " + (await r.text())); return; }
  } catch (e) { alert("Post failed: " + e); return; }
  await loadRelayVersion();
  renderAnnouncementBar();
  renderAnnouncementsPanel(manageBody); // refresh the open panel
}

async function deleteAnnouncement(id) {
  try {
    const r = await fetch("/api/announcements?id=" + encodeURIComponent(id), { method: "DELETE" });
    if (!r.ok) { alert("Delete failed: " + (await r.text())); return; }
  } catch (e) { alert("Delete failed: " + e); return; }
  await loadRelayVersion();
  renderAnnouncementBar();
  renderAnnouncementsPanel(manageBody);
}

// boot decides which mode the page runs in. The relay and the standalone server
// expose GET /api/laptops; the terminal mirror does not (404). With laptops we
// render a tree (laptop > project > sessions); otherwise we fall back to the
// single-stream mirror behavior.
async function boot() {
  initSearch();
  await loadRelayVersion();
  // Live-refresh the announcement banner (and relay version / admin flag) so a
  // maintainer notice posted mid-session appears without a page reload. Dismissed
  // ids are remembered locally, so re-rendering never re-nags.
  setInterval(loadRelayVersion, 60000);
  let resp;
  try {
    resp = await fetch("/api/laptops");
  } catch (_) { resp = null; }
  // If auth is enabled and the session lapsed, bounce to login.
  if (resp && resp.status === 401) { bounceToLogin(); return; }
  if (resp && resp.ok) {
    laptops = await resp.json();
    renderLaptopTree();
    // Auto-expand the first *online* laptop, then its first project — and if that
    // project has exactly one saved session, open it so the page is immediately
    // live. (Offline laptops can't be browsed, so skip them.)
    const lap = (laptops || []).find((l) => l.online !== false);
    if (lap) {
      await toggleLaptop(lap.host, true);
      const projs = laptopProjects[lap.host] || [];
      if (projs.length) {
        await toggleProject(lap.host, projs[0].key, true);
        const ss = projectSessionsCache[pkey(lap.host, projs[0].key)] || [];
        if (!current && ss.length === 1) { focusLaptop(lap.host); currentProject = projs[0].key; selectSession(ss[0].id); }
      }
    }
    startLaptopPolling(); // reflect TUIs connecting/disconnecting without a manual refresh
    return;
  }
  // Mirror mode: the terminal owns sessions; we just stream the one it exposes.
  loadSessions();
}

// startLaptopPolling keeps the laptop tree in sync with the relay's live set of
// connected connectors. The relay learns of a TUI connecting/disconnecting
// instantly, but the browser only fetched /api/laptops once at boot — so a TUI
// that connected later needed a manual refresh to appear. We re-fetch on a short
// interval and whenever the tab regains focus (for immediacy). Tree mode only;
// mirror mode (laptops === null) has no /api/laptops.
function startLaptopPolling() {
  if (laptopPollTimer) return;
  const tick = () => { refreshLaptops(); refreshExpandedSessions(); };
  laptopPollTimer = setInterval(tick, 3000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) tick(); });
}

// refreshExpandedSessions re-fetches the session lists of the currently-expanded
// projects so the per-session "running" dots (parallel agents) stay live. It
// re-renders the tree only when a session's running/live state actually changed,
// to avoid resetting the sidebar's scroll on every poll.
async function refreshExpandedSessions() {
  if (!laptops) return; // tree mode only
  let changed = false;
  for (const pk of [...expandedProjects]) {
    const i = pk.indexOf("\n");
    if (i < 0) continue;
    const host = pk.slice(0, i), key = pk.slice(i + 1);
    const before = runningSig(projectSessionsCache[pk]);
    await loadProjectSessions(host, key);
    if (runningSig(projectSessionsCache[pk]) !== before) changed = true;
  }
  updateAttention(); // badge the tab + notify on finished / needs-approval transitions
  if (changed) renderLaptopTree();
}

// runningSig fingerprints a session list by id + running/attention/live state, so
// we only re-render when an agent starts/finishes or starts needing approval.
function runningSig(list) {
  return (list || []).map((s) => s.id + (s.running ? "1" : "0") + (s.state || "") + (s.live ? "L" : "")).join(",");
}

// ---------- Cross-session attention ----------
// Parallel agents run in the background, so a session can finish or block on an
// approval while you're looking at another one. We badge the tab title with the
// number of sessions awaiting approval, and (when the tab is in the background)
// raise an OS notification on the transition — so you don't have to babysit each
// session. State comes from the same 3s session-list poll that drives the tree's
// running/approval dots.
let sessPrevState = {};            // session id -> last seen state ("busy"/"awaiting_approval"/"")
let notifyAllowed = false;         // OS Notification permission granted
const baseTitle = document.title;  // tab title with no pending attention

// maybeRequestNotify asks for OS-notification permission. Browsers require a user
// gesture, so it's called from clicks (selecting/expanding a session).
function maybeRequestNotify() {
  if (!("Notification" in window)) return;
  if (Notification.permission === "granted") { notifyAllowed = true; return; }
  if (Notification.permission === "denied") return;
  try { Notification.requestPermission().then((p) => { notifyAllowed = p === "granted"; }); } catch (_) {}
}

// notifyAttention raises an OS notification, but only when the tab is hidden — if
// you're looking at the app, the tree badge and title are enough.
function notifyAttention(title, body) {
  if (!notifyAllowed || !document.hidden) return;
  try { new Notification(title, { body, tag: "sqe:" + title + ":" + body }); } catch (_) {}
}

// updateAttention scans every known (expanded-project) session, fires a
// notification when one you're NOT viewing newly needs approval or finishes, and
// badges the tab title with the count still awaiting approval.
function updateAttention() {
  let needApproval = 0;
  const seen = {};
  for (const pk in projectSessionsCache) {
    for (const s of projectSessionsCache[pk] || []) {
      seen[s.id] = true;
      const st = s.state || "";
      const prev = sessPrevState[s.id];
      if (st === "awaiting_approval" || st === "awaiting_question") needApproval++;
      // Only notify on a real transition (prev known) and not for the session
      // currently on screen (its approval/question panel already shows it).
      if (prev !== undefined && prev !== st && s.id !== current) {
        if (st === "awaiting_approval") notifyAttention("Approval needed", (s.title || "A session") + " is waiting for you");
        else if (st === "awaiting_question") notifyAttention("Input needed", (s.title || "A session") + " has a question for you");
        else if ((prev === "busy" || prev === "awaiting_approval" || prev === "awaiting_question") && st === "") notifyAttention("Turn finished", (s.title || "A session") + " is done");
      }
      sessPrevState[s.id] = st;
    }
  }
  for (const id in sessPrevState) if (!seen[id]) delete sessPrevState[id]; // prune vanished sessions
  document.title = needApproval > 0 ? `(${needApproval}) ${baseTitle}` : baseTitle;
}

// refreshLaptops re-fetches the connected-laptop list and, only when the set of
// hosts actually changed, re-renders the tree (expansion/selection live in
// separate state, so they survive). It prunes state for laptops that dropped and
// tears down the conversation view if the focused laptop disconnected.
async function refreshLaptops() {
  if (!laptops) return; // mirror mode
  let resp;
  try { resp = await fetch("/api/laptops"); } catch (_) { return; }
  if (resp.status === 401) { bounceToLogin(); return; }
  if (!resp.ok) return;
  const next = await resp.json().catch(() => null);
  if (!Array.isArray(next)) return;

  // Key on host + online so an online↔offline transition (same host) re-renders.
  const sig = (arr) => arr.map((l) => l.host + (l.online === false ? ":0" : ":1")).join("\u0000");
  if (sig(laptops) === sig(next)) {
    laptops = next;
    // Versions can change without the host set changing (a terminal self-updated
    // and reconnected) — refresh the version banner from the live poll so a stale
    // "behind"/"Upgrading…" clears on its own.
    updateVersionWarning(currentTermVersion());
    return;
  }
  // The connector set changed — often a reconnect after a deploy. Re-fetch the
  // relay's own version too so the comparison target is current.
  loadRelayVersion();

  // "live" = laptops attached right now; offline ones are listed but can't be
  // browsed, so prune their expanded state and drop focus if one went offline.
  const live = new Set(next.filter((l) => l.online !== false).map((l) => l.host));
  for (const h of [...expandedLaptops]) if (!live.has(h)) expandedLaptops.delete(h);
  for (const h of Object.keys(laptopProjects)) if (!live.has(h)) delete laptopProjects[h];
  for (const k of [...expandedProjects]) if (!live.has(k.split("\n")[0])) expandedProjects.delete(k);
  for (const k of Object.keys(projectSessionsCache)) if (!live.has(k.split("\n")[0])) delete projectSessionsCache[k];
  // If the laptop we're viewing dropped, close its (now-dead) stream and reset.
  if (currentLaptop && !live.has(currentLaptop)) { clearView(); currentLaptop = null; currentProject = null; apiBase = ""; }

  const hadOnline = laptops.some((l) => l.online !== false);
  laptops = next;
  renderLaptopTree();
  updateVersionWarning(currentTermVersion()); // refresh the banner from the live laptop poll

  // First online TUI just appeared (and nothing focused) → expand it, like boot().
  if (!hadOnline && !currentLaptop) {
    const firstOnline = next.find((l) => l.online !== false);
    if (firstOnline) toggleLaptop(firstOnline.host, true);
  }
}

// focusLaptop marks a laptop as the one whose API calls should target (used when
// selecting a session under it).
function focusLaptop(host) {
  currentLaptop = host;
  apiBase = laptopBase(host);
}

// loadAuthUser reveals the signed-in identity + Sign out in the account menu
// when the auth provider is on (GET /auth/me). When auth is disabled the
// endpoint isn't routed (404), so the menu just stays as-is.
async function loadAuthUser() {
  try {
    const r = await fetch("/auth/me", { headers: { Accept: "application/json" } });
    if (!r.ok) return;
    const u = await r.json();
    if (!u || !u.accountname) return; // auth off / anonymous → leave the host-based menu as-is
    authUser = u; // make the avatar reflect the signed-in user, not the host
    const nameEl = $("#authUser"), divEl = $("#authDivider"), outEl = $("#umLogout"), connEl = $("#umConnect");
    if (nameEl) { nameEl.textContent = u.display_name || u.accountname || "signed in"; nameEl.style.display = ""; }
    if (divEl) divEl.style.display = "";
    if (outEl) outEl.style.display = "";
    if (connEl) connEl.style.display = ""; // "Connect a machine" → /connect (per-user relay token)
    // Admin-only Admin menu item (Announcements + Members tabs): show it for
    // admins (server reports is_admin via /auth/me). The endpoints are also
    // admin-gated server-side. Re-synced here since authUser just changed.
    syncAdminMenuVisibility();
    updateAvatar(lastStatus); // refresh now (and updateAvatar keeps preferring authUser on every status)
  } catch (_) { /* auth off or offline — leave the menu unchanged */ }
}

// buildHostRow makes the sidebar's root row for MIRROR mode (a single live
// terminal session, no laptop list). It shows the terminal machine and carries
// a green "live" accent + pulsing dot when the relay is connected. Returns null
// until a status event (which carries the host) arrives. In tree mode the
// laptop rows play this role instead, so it isn't used there.
function buildHostRow() {
  const s = lastStatus;
  if (!s || !s.host) return null;
  const relay = s.relay || "";
  const connected = relay === "connected";
  const row = el("div", "host-row px-3 py-2.5 border-b border-edge flex items-center gap-2" + (connected ? " is-connected" : ""));
  row.id = "hostRow";
  row.appendChild(sym("laptopcomputer", "shrink-0 " + (connected ? "text-ok" : "text-faint")));
  row.appendChild(el("div", "host-name truncate flex-1 text-[13px] font-semibold " + (connected ? "text-ok" : "text-ink"), s.host));
  if (relay) {
    const dot = sym("circle.fill", "host-dot shrink-0 " + (connected ? "text-ok" : "text-warn"));
    dot.title = "relay " + relay;
    row.appendChild(dot);
  }
  return row;
}

// refreshHostRow updates the mirror-mode host row in place on a status change.
// In tree mode (laptops set) the laptop rows carry the host, so it's a no-op.
function refreshHostRow() {
  if (laptops) return;
  const box = $("#sessions");
  if (!box) return;
  const existing = $("#hostRow");
  const fresh = buildHostRow();
  if (existing && fresh) existing.replaceWith(fresh);
  else if (existing) existing.remove();
  else if (fresh) box.insertBefore(fresh, box.firstChild);
}

// renderLaptopTree draws the sidebar as laptop > project > sessions: a row per
// connected laptop, each expanding to its projects, each project expanding to
// its saved sessions. Clicking a laptop or project toggles it; clicking a
// session opens that session's stream on that laptop.
// sidebarSectionHead is a small uppercase divider label for the sidebar tree
// (e.g. "Local Machines", "Agent Builder"). topBorder adds a hairline + gap above
// it to separate it from the section before. Styling is the custom .sidebar-section
// class (app.css) — the uppercase/size utilities aren't in the prebuilt Tailwind.
function sidebarSectionHead(text, topBorder) {
  return el("div", "sidebar-section" + (topBorder ? " is-divided" : ""), text);
}

function renderLaptopTree() {
  const box = $("#sessions"); box.innerHTML = "";
  // Local Machines — the laptops/terminals connected to this relay.
  box.appendChild(sidebarSectionHead("Local Machines"));
  if (!laptops || !laptops.length) {
    box.appendChild(emptyLaptopsNote());
  } else {
    for (const lap of laptops) renderLaptopInto(box, lap);
  }
  // Agent Builder — visual workflow canvas (Phase 1: build + save graphs).
  // Filled by renderAgentBuilderInto (app-workflow.js); guarded in case that
  // script hasn't loaded yet.
  box.appendChild(sidebarSectionHead("Agent Builder", true));
  if (typeof window.renderAgentBuilderInto === "function") {
    window.renderAgentBuilderInto(box);
  } else {
    box.appendChild(el("div", "px-4 py-3 text-[12px] text-faint", "Agent Builder loading…"));
  }
}

// emptyLaptopsNote is shown when the user has no laptops at all — i.e. they've
// never connected a terminal (offline ones would otherwise appear). When the
// relay offers downloads, it nudges them to install; otherwise a plain note.
function emptyLaptopsNote() {
  const box = el("div", "px-4 py-3 text-[12px] text-faint");
  if (!relayDownloads) { box.textContent = "No laptops connected."; return box; }
  box.appendChild(el("div", null, "No terminal connected yet. Install it on your Mac:"));
  const cmd = el("div", "mt-1.5 font-mono text-[11px] text-ink break-all");
  cmd.textContent = "curl -fsSL " + location.origin + "/install.sh | sh";
  box.appendChild(cmd);
  const a = el("a", "mt-1.5 inline-block text-brand", "Open the download page \u2192");
  a.href = "/download"; a.target = "_blank"; a.rel = "noopener noreferrer";
  box.appendChild(a);
  return box;
}

// relTime renders a unix-seconds timestamp as a short "2h ago" style string.
function relTime(unixSecs) {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unixSecs));
  if (s < 60) return "just now";
  const m = Math.floor(s / 60); if (m < 60) return m + "m ago";
  const h = Math.floor(m / 60); if (h < 24) return h + "h ago";
  return Math.floor(h / 24) + "d ago";
}

// renderLaptopInto draws one laptop row and, when expanded, its projects. An
// offline laptop (seen before but not attached now) renders greyed and isn't
// expandable — its sessions live on that machine, reachable only while it's
// connected — with a hint to run the terminal to reconnect.
function renderLaptopInto(box, lap) {
  if (lap.online === false) { renderOfflineLaptopInto(box, lap); return; }
  const open = expandedLaptops.has(lap.host);
  const focused = lap.host === currentLaptop;
  const row = el("div", "host-row px-3 py-2.5 border-b border-edge cursor-pointer hover:bg-raised flex items-center gap-2");
  if (focused) row.style.boxShadow = `inset 3px 0 0 0 ${themeColor("--c-ok")}`; // focused-laptop accent
  const chev = sym(open ? "chevron.down" : "chevron.right", "text-faint shrink-0");
  chev.style.fontSize = "10px";
  row.appendChild(chev);
  row.appendChild(sym("laptopcomputer", "shrink-0 text-ok"));
  row.appendChild(el("div", "host-name truncate flex-1 text-[13px] font-semibold text-ok", lap.name || lap.host));
  row.appendChild(sym("circle.fill", "host-dot shrink-0 text-ok")); // online indicator
  row.onclick = () => toggleLaptop(lap.host);
  box.appendChild(row);
  if (!open) return;

  const projs = laptopProjects[lap.host];
  if (!projs) { box.appendChild(loadingNote(2.25)); return; }
  if (!projs.length) { box.appendChild(indentedNote("No projects yet.", 2.25)); return; }
  for (const p of projs) renderProjectInto(box, lap, p);
}

// renderOfflineLaptopInto draws a greyed, non-expandable row for a laptop that
// has connected before but isn't attached now, with an "offline · 2h ago" tag
// and a hint to run the terminal to bring it back.
function renderOfflineLaptopInto(box, lap) {
  const row = el("div", "host-row px-3 py-2.5 border-b border-edge flex items-center gap-2");
  const spacer = el("span", "shrink-0"); spacer.style.width = "10px"; // align with the chevron column
  row.appendChild(spacer);
  row.appendChild(sym("laptopcomputer", "shrink-0 text-faint"));
  row.appendChild(el("div", "host-name truncate flex-1 text-[13px] font-semibold text-faint", lap.name || lap.host));
  const tag = el("span", "shrink-0 text-[11px] text-faint", "offline" + (lap.lastSeen ? " \u00b7 " + relTime(lap.lastSeen) : ""));
  tag.title = "Run moony on this machine to reconnect.";
  row.appendChild(tag);
  box.appendChild(row);
  box.appendChild(offlineNote("Offline — run moony on this machine to reconnect.", 2.25));
}

// renderProjectInto draws one project row (nested under its laptop) and, when
// expanded, its sessions plus the new/clear actions.
function renderProjectInto(box, lap, p) {
  const key = pkey(lap.host, p.key);
  const open = expandedProjects.has(key);
  const focused = lap.host === currentLaptop && p.key === currentProject;
  const hdr = el("div", "border-b border-edge cursor-pointer hover:bg-raised flex items-center gap-2 py-2.5" + (focused ? " bg-raised" : ""));
  hdr.style.paddingLeft = "1.75rem"; // nested one level under the laptop
  hdr.style.paddingRight = "0.75rem";
  if (focused) hdr.style.boxShadow = `inset 3px 0 0 0 ${themeColor("--c-brand")}`; // current-project accent
  const chev = sym(open ? "chevron.down" : "chevron.right", "text-faint shrink-0");
  chev.style.fontSize = "10px";
  hdr.appendChild(chev);
  hdr.appendChild(sym(open ? "folder.fill" : "folder", "text-brand shrink-0"));
  const name = el("div", "text-[13px] truncate flex-1 text-ink", p.name);
  name.style.fontWeight = "500";
  name.title = p.dir;
  hdr.appendChild(name);
  if (p.sessions) {
    const badge = el("span", "shrink-0 text-[11px] text-faint bg-base border border-edge", String(p.sessions));
    badge.style.cssText = "padding:1px 7px;border-radius:9999px;line-height:1.3";
    hdr.appendChild(badge);
  }
  hdr.onclick = () => toggleProject(lap.host, p.key);
  box.appendChild(hdr);
  if (!open) return;

  const sessions = projectSessionsCache[key];
  // Per-project actions: New session (left) and, when non-empty, Clear all (right).
  const actions = el("div", "tree-actions border-b border-edge text-[12px]");
  actions.style.padding = "0.5rem 0.3rem 0.5rem 3rem"; // left indent matches the session rows below
  const add = el("div", "tree-action tree-action-new");
  add.appendChild(sym("plus", "shrink-0"));
  add.appendChild(el("span", "", "New session"));
  // Opens the folder picker rooted at this project's directory: "Open session
  // here" starts one in the project, or browse elsewhere on the machine.
  add.onclick = (e) => { e.stopPropagation(); openFolderPicker(lap.host, p.dir); };
  actions.appendChild(add);
  if (sessions && sessions.length) {
    const clear = el("div", "tree-action tree-action-clear ml-auto");
    clear.title = "Delete all sessions in this project";
    clear.appendChild(sym("trash", "shrink-0"));
    clear.appendChild(el("span", "", "Clear all"));
    clear.onclick = (e) => { e.stopPropagation(); clearProjectSessions(lap.host, p.key); };
    actions.appendChild(clear);
  }
  box.appendChild(actions);

  if (!sessions) {
    box.appendChild(loadingNote(3));
  } else if (!sessions.length) {
    box.appendChild(indentedNote("No saved sessions yet.", 3));
  } else {
    // Cap the per-project session list so a project with many sessions scrolls
    // instead of pushing the rest of the tree off-screen.
    const list = el("div", "overflow-y-auto");
    list.style.maxHeight = "16rem";
    for (const s of sessions) {
      const active = s.id === current && lap.host === currentLaptop;
      // The selected session gets the brand highlight; every OTHER session with a
      // live agent working (running/awaiting approval) gets an amber "active"
      // accent, so multiple concurrently-active sessions in a project are all
      // visible — not just the one you're viewing.
      let cls = "sess border-b border-edge hover:bg-raised flex items-center gap-1";
      if (active) cls += " bg-raised border-l-[3px] border-l-brand";
      else if (s.running) cls += " sess-active";
      const row = el("div", cls);
      row.style.padding = "0.5rem 0.5rem 0.5rem 3rem";
      const main = el("div", "min-w-0 flex-1 cursor-pointer");
      const titleRow = el("div", "flex items-center gap-1.5 min-w-0");
      if (s.state === "awaiting_approval") { const b = el("span", "sess-attn shrink-0"); b.title = "Waiting for your approval"; titleRow.appendChild(b); }
      else if (s.state === "awaiting_question") { const b = el("span", "sess-attn shrink-0"); b.title = "Waiting for your answer"; titleRow.appendChild(b); }
      else if (s.running) { const dot = el("span", "sess-running shrink-0"); dot.title = "An agent is running here"; titleRow.appendChild(dot); }
      const titleDiv = el("div", "text-[13px] truncate", s.title || "(untitled)");
      titleRow.appendChild(titleDiv);
      main.appendChild(titleRow);
      if (s.detail) main.appendChild(el("div", "text-[11px] text-faint truncate", s.detail));
      main.onclick = () => { focusLaptop(lap.host); currentProject = p.key; selectSession(s.id); };
      row.appendChild(main);

      const pin = sym(s.pinned ? "pin.fill" : "pin", "session-act shrink-0 cursor-pointer " + (s.pinned ? "session-pinned-active" : ""));
      pin.title = s.pinned ? "Unpin session" : "Pin session to top";
      pin.onclick = (e) => { e.stopPropagation(); pinSession(s.id, !s.pinned, lap.host, p.key); };
      row.appendChild(pin);

      const ren = sym("pencil", "session-act shrink-0 cursor-pointer");
      ren.title = "Rename session";
      ren.onclick = (e) => { e.stopPropagation(); startRenameSession(titleDiv, s, lap.host, p.key); };
      row.appendChild(ren);

      const del = el("div", "del shrink-0 text-ink cursor-pointer");
      del.style.padding = "0 0.25rem";
      del.title = "Delete session";
      del.appendChild(sym("trash"));
      del.onclick = (e) => { e.stopPropagation(); deleteSession(s.id, lap.host, p.key); };
      row.appendChild(del);
      list.appendChild(row);
    }
    box.appendChild(list);
  }
}

// indentedNote is a faint placeholder row, indented to padLeftRem so it aligns
// with the tree level it belongs to.
function indentedNote(text, padLeftRem) {
  const n = el("div", "text-[11px] text-faint", text);
  n.style.padding = "0.5rem 0.75rem 0.5rem " + (padLeftRem || 2.25) + "rem";
  return n;
}

// loadingNote is indentedNote with a small jumping-Claude loader before the
// "Loading…" label, for the sidebar tree's lazy-load rows.
function loadingNote(padLeftRem) {
  const n = el("div", "tree-loading text-[11px] text-faint");
  n.style.padding = "0.5rem 0.75rem 0.5rem " + (padLeftRem || 2.25) + "rem";
  const art = el("img", "tree-loading-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "";
  n.append(art, el("span", null, "Loading…"));
  return n;
}

// offlineNote is indentedNote with a small dimmed, grayscaled Claude before the
// text — a faded "waiting" mascot that matches the greyed offline laptop row
// (so it reads as idle/waiting, not celebrating).
function offlineNote(text, padLeftRem) {
  const n = el("div", "tree-offline text-[11px] text-faint");
  n.style.padding = "0.5rem 0.75rem 0.5rem " + (padLeftRem || 2.25) + "rem";
  const art = el("img", "tree-offline-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "";
  n.append(art, el("span", null, text));
  return n;
}

// showConnecting puts a centered jumping-Claude loader in the transcript while a
// newly-selected session connects. The SSE replays history on connect, so the
// first event in clears it (dismissConnecting, called from handle()).
function showConnecting() {
  const d = el("div", "conn-loader"); d.id = "connLoader";
  const art = el("img", "conn-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "";
  d.append(art, el("div", "conn-label", "Connecting…"));
  transcript.appendChild(d);
}

// dismissConnecting removes the connecting loader once content starts arriving.
function dismissConnecting() {
  const d = document.getElementById("connLoader");
  if (d) d.remove();
}

// toggleLaptop expands/collapses a laptop, lazily loading (and caching) its
// projects. forceOpen keeps it open (used at boot) instead of toggling.
async function toggleLaptop(host, forceOpen) {
  if (expandedLaptops.has(host) && !forceOpen) {
    expandedLaptops.delete(host);
    renderLaptopTree();
    return;
  }
  expandedLaptops.add(host);
  renderLaptopTree(); // immediate feedback (chevron + "Loading…")
  if (!laptopProjects[host]) await loadProjects(host);
  renderLaptopTree();
}

// loadProjects fetches and caches one laptop's project list.
async function loadProjects(host) {
  try {
    const r = await fetch(laptopBase(host) + "/api/projects");
    laptopProjects[host] = r.ok ? await r.json() : [];
  } catch (_) {
    laptopProjects[host] = [];
  }
}

// toggleProject expands/collapses a project within a laptop. Expanding lazily
// loads (and caches) its sessions. forceOpen keeps it open instead of toggling.
async function toggleProject(host, key, forceOpen) {
  const pk = pkey(host, key);
  if (expandedProjects.has(pk) && !forceOpen) {
    expandedProjects.delete(pk);
    renderLaptopTree();
    return;
  }
  expandedProjects.add(pk);
  renderLaptopTree();
  if (!projectSessionsCache[pk]) await loadProjectSessions(host, key);
  renderLaptopTree();
}

// loadProjectSessions fetches and caches a project's persisted sessions.
async function loadProjectSessions(host, key) {
  const pk = pkey(host, key);
  try {
    const r = await fetch(laptopBase(host) + `/api/projects/${key}/sessions`);
    projectSessionsCache[pk] = r.ok ? await r.json() : [];
  } catch (_) {
    projectSessionsCache[pk] = [];
  }
}

// openFolderPicker opens the folder browser (reusing the manage overlay) so the
// user can start a session in any directory on a laptop. host scopes the fs
// requests to that machine via laptopBase(); startDir is where to open the
// browser (a project's dir, or "" for the machine's home).
function openFolderPicker(host, startDir) {
  setPanel("New session", "folder.badge.plus");
  renderFolderPicker(host, startDir || "");
}

// renderFolderPicker draws the browser for one directory: a path field, an Up
// control, the subfolders (click to descend), and "Open session here".
async function renderFolderPicker(host, dir) {
  const body = manageBody;
  body.innerHTML = "";
  body.append(el("p", "access-intro", "Pick a folder on this machine to start a new agent session in. Click a folder to open it, or type a path — a new folder is created when you open a session in it (its parent must already exist)."));

  let listing = { dir: dir, parent: "", entries: [] };
  try {
    const r = await fetch(laptopBase(host) + "/api/fs?dir=" + encodeURIComponent(dir));
    if (r.ok) listing = await r.json();
    else body.append(el("div", "access-empty", "This folder doesn't exist yet — “Open session here” will create it (its parent must exist)."));
  } catch (_) { body.append(el("div", "access-empty", "Could not reach the machine.")); }

  // Path field + Go (type/paste an absolute path or ~/...).
  const form = el("div", "flex items-center gap-2 mb-2");
  const input = el("input", "plugins-search"); input.style.margin = "0";
  input.value = listing.dir || ""; input.placeholder = "/path/to/folder";
  const go = el("button", "shrink-0 text-[11px] rounded px-2 py-1 border border-edge text-brand", "Go");
  const navigate = () => renderFolderPicker(host, input.value.trim());
  go.onclick = navigate;
  input.onkeydown = (e) => { if (e.key === "Enter") navigate(); };
  form.append(input, go);
  body.append(form);

  // Current dir + Up.
  const head = el("div", "flex items-center gap-2 mb-1");
  head.append(el("div", "flex-1 truncate font-mono text-[11px] text-faint", listing.dir || ""));
  if (listing.parent) {
    const up = el("button", "shrink-0 text-[11px] text-brand", "\u2191 Up");
    up.onclick = () => renderFolderPicker(host, listing.parent);
    head.append(up);
  }
  body.append(head);

  // Subfolders (scrollable).
  const list = el("div", "plugins-scroll");
  if (!listing.entries || !listing.entries.length) list.append(el("div", "access-empty", "No subfolders here."));
  for (const e of (listing.entries || [])) {
    const r = el("div", "flex items-center gap-2 py-1.5 border-b border-edge cursor-pointer hover:bg-raised");
    r.append(sym("folder", "shrink-0 text-brand"));
    r.append(el("div", "flex-1 truncate text-[13px] text-ink", e.name));
    r.onclick = () => renderFolderPicker(host, e.path);
    list.append(r);
  }
  body.append(list);

  // Open here.
  const footer = el("div", "flex items-center gap-2 mt-2");
  const open = el("button", "text-[12px] rounded px-3 py-1.5 border border-edge text-brand", "Open session here");
  open.onclick = () => openSessionInDir(host, (input.value.trim() || listing.dir || ""));
  footer.append(open);
  body.append(footer);
}

// openSessionInDir cold-starts a session in dir on host, then navigates to it.
async function openSessionInDir(host, dir) {
  if (!dir) return;
  let res;
  try {
    const r = await fetch(laptopBase(host) + "/api/projects/open", { method: "POST", body: JSON.stringify({ dir }) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Could not open a session there."); return; }
    res = await r.json();
  } catch (_) { alertBox("Could not reach the machine."); return; }
  closeManage();
  // Tree mode: focus the machine + project so the sidebar reveals the new session
  // in its project. Mirror/standalone mode has no laptop tree, so just select it —
  // selectSession opens the new session's stream directly.
  if (laptops) {
    focusLaptop(host);
    currentProject = res.key;
    expandedLaptops.add(host);
    expandedProjects.add(pkey(host, res.key));
    await refreshProjects(host);
    await loadProjectSessions(host, res.key);
    renderLaptopTree();
  }
  selectSession(res.id);
}

// refreshProjects re-fetches a laptop's project list (and their session counts).
// A project whose last session was deleted drops off the list server-side.
async function refreshProjects(host) {
  try {
    const r = await fetch(laptopBase(host) + "/api/projects");
    if (r.ok) laptopProjects[host] = await r.json();
  } catch (_) {}
}

// deleteSession removes a saved session after confirmation, then refreshes the
// sidebar. Deleting the terminal's active session is allowed: the server moves
// the terminal to another session and returns {switchedTo}; if we were viewing
// the deleted one, follow along, otherwise just refresh. Deleting any other open
// session clears the conversation view.
async function deleteSession(id, host, key) {
  if (!(await confirmBox("Delete this session? This can't be undone.", { title: "Delete session", okText: "Delete", danger: true }))) return;
  const r = await fetch(laptopBase(host) + `/api/sessions/${id}`, { method: "DELETE" });
  if (!r.ok) {
    // Show the server's actual reason (e.g. a turn is mid-flight) instead of
    // assuming one cause.
    const msg = (await r.text().catch(() => "")).trim();
    alertBox(msg || "Could not delete the session.");
    return;
  }
  const res = await r.json().catch(() => ({})); // {switchedTo} for an active delete; empty (204) otherwise
  await loadProjectSessions(host, key);
  await refreshProjects(host);
  renderLaptopTree();
  if (current === id) {
    if (res && res.switchedTo) { focusLaptop(host); currentProject = key; selectSession(res.switchedTo); }
    else clearView();
  }
}

async function renameSession(id, title, host, key) {
  try {
    const r = await fetch(laptopBase(host) + `/api/sessions/${id}/rename`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    if (r.ok) {
      await loadProjectSessions(host, key);
      renderLaptopTree();
    }
  } catch (e) {
    console.error("rename failed:", e);
  }
}

async function pinSession(id, pinned, host, key) {
  try {
    const r = await fetch(laptopBase(host) + `/api/sessions/${id}/pin`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ pinned }),
    });
    if (r.ok) {
      await loadProjectSessions(host, key);
      renderLaptopTree();
    }
  } catch (e) {
    console.error("pin failed:", e);
  }
}

function startRenameSession(titleEl, s, host, key) {
  const currentTitle = s.title || "";
  const inp = document.createElement("input");
  inp.type = "text";
  inp.className = "text-[13px] bg-backdrop text-ink w-full px-1 py-0.5 rounded outline-none focus:ring-1 focus:ring-brand";
  inp.value = currentTitle;
  titleEl.replaceWith(inp);
  inp.focus();
  inp.select();

  let done = false;
  const finish = async (save) => {
    if (done) return;
    done = true;
    const nextVal = inp.value.trim();
    if (save && nextVal && nextVal !== currentTitle) {
      s.title = nextVal;
      titleEl.textContent = nextVal;
      inp.replaceWith(titleEl);
      await renameSession(s.id, nextVal, host, key);
    } else {
      inp.replaceWith(titleEl);
    }
  };

  inp.onblur = () => finish(true);
  inp.onkeydown = (e) => {
    if (e.key === "Enter") { e.stopPropagation(); finish(true); }
    if (e.key === "Escape") { e.stopPropagation(); finish(false); }
  };
}

// clearProjectSessions deletes every session in a project after confirmation.
// Busy sessions (mid-turn) are skipped server-side and reported.
async function clearProjectSessions(host, key) {
  const pk = pkey(host, key);
  const list = projectSessionsCache[pk] || [];
  if (!list.length) return;
  if (!(await confirmBox(`Delete all ${list.length} session(s) in this project? This can't be undone.`, { title: "Delete sessions", okText: "Delete", danger: true }))) return;
  const r = await fetch(laptopBase(host) + `/api/projects/${key}/sessions`, { method: "DELETE" });
  if (!r.ok) { alertBox("Could not clear the sessions."); return; }
  const res = await r.json().catch(() => ({}));
  await loadProjectSessions(host, key);
  await refreshProjects(host);
  // If the open session was in this project and is now gone, clear the view.
  const remaining = projectSessionsCache[pk] || [];
  if (current && list.some((s) => s.id === current) && !remaining.some((s) => s.id === current)) clearView();
  renderLaptopTree();
  if (res && res.skipped) alertBox(`${res.skipped} busy session(s) were not deleted.`);
}

// renderSessionList draws the sidebar in mirror mode: the terminal publishes
// the project's saved sessions (projectSessions) and clicking one switches the
// terminal to it; the legacy path lists the server's own sessions. (Tree mode
// uses renderLaptopTree instead.)
function renderSessionList() {
  const box = $("#sessions"); box.innerHTML = "";
  const host = buildHostRow(); if (host) box.appendChild(host); // mirror-mode laptop name at the top
  if (projectSessions) {
    if (!projectSessions.length) {
      box.appendChild(el("div", "px-4 py-3 text-[12px] text-faint", "No saved sessions yet."));
      return;
    }
    for (const s of projectSessions) {
      const d = el("div", "sess px-4 py-2.5 border-b border-edge cursor-pointer hover:bg-raised flex items-center justify-between gap-1" + (s.active ? " bg-raised border-l-[3px] border-l-brand" : ""));
      const mainPart = el("div", "min-w-0 flex-1");
      const titleRow = el("div", "flex items-center gap-1.5 min-w-0");
      // The terminal drives one session at a time, so only the active session can
      // be awaiting input or busy — reflect its live state (tracked globally from
      // the mirror's state events) as an attention badge, matching the laptop tree.
      if (s.active && state === "awaiting_approval") { const b = el("span", "sess-attn shrink-0"); b.title = "Waiting for your approval"; titleRow.appendChild(b); }
      else if (s.active && state === "awaiting_question") { const b = el("span", "sess-attn shrink-0"); b.title = "Waiting for your answer"; titleRow.appendChild(b); }
      else if (s.active && state === "busy") { const dot = el("span", "sess-running shrink-0"); dot.title = "An agent is running here"; titleRow.appendChild(dot); }
      const titleDiv = el("div", "text-[13px] truncate", s.title || "(untitled)");
      titleRow.appendChild(titleDiv);
      mainPart.appendChild(titleRow);
      if (s.detail) mainPart.appendChild(el("div", "text-[11px] text-faint truncate", s.detail));
      mainPart.onclick = () => switchProjectSession(s.id);
      d.appendChild(mainPart);

      const pin = sym(s.pinned ? "pin.fill" : "pin", "session-act shrink-0 cursor-pointer " + (s.pinned ? "session-pinned-active" : ""));
      pin.title = s.pinned ? "Unpin session" : "Pin session to top";
      pin.onclick = (e) => { e.stopPropagation(); pinSession(s.id, !s.pinned, currentLaptop, currentProject); };
      d.appendChild(pin);

      const ren = sym("pencil", "session-act shrink-0 cursor-pointer");
      ren.title = "Rename session";
      ren.onclick = (e) => { e.stopPropagation(); startRenameSession(titleDiv, s, currentLaptop, currentProject); };
      d.appendChild(ren);

      const del = el("div", "del shrink-0 text-ink cursor-pointer");
      del.style.padding = "0 0.25rem";
      del.title = "Delete session";
      del.appendChild(sym("trash"));
      del.onclick = (e) => { e.stopPropagation(); deleteSession(s.id, currentLaptop, currentProject); };
      d.appendChild(del);

      box.appendChild(d);
    }
    return;
  }
  for (const s of httpSessions) {
    const active = s.id === current;
    const d = el("div", "px-4 py-2.5 border-b border-edge cursor-pointer hover:bg-raised" + (active ? " bg-raised border-l-[3px] border-l-brand" : ""));
    d.appendChild(el("div", "text-[13px]", s.title));
    d.appendChild(el("div", "text-[11px] text-faint", s.state));
    d.onclick = () => selectSession(s.id);
    box.appendChild(d);
  }
}

// switchProjectSession asks the terminal to switch to a saved session. The live
// stream stays connected; its transcript resets and replays the new session.
function switchProjectSession(id) {
  document.body.classList.remove("nav-open"); // close the mobile drawer on navigate
  sendText("/sessions " + id);
}

async function newSession() {
  const r = await fetch("/api/sessions", { method: "POST", body: "{}" });
  const s = await r.json();
  await loadSessions();
  selectSession(s.id);
}

// clearView tears down the current conversation view: closes the event stream
// and resets the transcript, header, status, todos, and side panels. It does NOT
// open a new stream — selectSession does that, while deleteSession leaves it empty.
function clearView() {
  if (es) { es.close(); es = null; }
  current = null; asstBody = null; asstRaw = ""; asstLabeled = false; turnTodos = null; phaseTimes = {}; toolGroup = null; workerGroup = null; turnActive = false; turnStartMs = 0; stickBottom = true; thinkBody = null; thinkRaw = ""; tools = {}; lastStatus = null; curStep = 0; maxStep = 0; compactingMsg = null;
  cumTurnIn = 0; cumTurnOut = 0; lastTurnCost = 0; lastTurnModel = ""; lastTurnReasoning = ""; lastTurnIn = 0; lastTurnOut = 0; lastTurnCacheRead = 0; // reset session token/cost accumulators; the new session replays its own turn-meta
  upgrading = false; upgradeStartVer = ""; syncUpgradeBtn(); // drop any stale "Updating…" state (button + mascot) when the view is torn down (e.g. the focused laptop dropped)
  usage = { skill: {}, agent: {}, worker: {}, mcp: {}, builtin: {} }; // reset per-session usage history
  boardMsgs = []; renderBoardMsgs(); // reset the agent-messages panel
  transcript.innerHTML = ""; resetPairs(); statsEl.innerHTML = ""; hdrMetaEl.innerHTML = ""; setHeaderVersion(); approval.classList.add("hidden"); hidePalette(); hideMentions(); hideMenu();
  closeHdrMenu(); // close the header approval/reasoning picker if it was open
  if (composerAgentEl) { composerAgentEl.textContent = ""; const c = composerAgentEl.parentElement; if (c) { c.classList.remove("has-agent"); c.style.removeProperty("--agent-base"); c.style.removeProperty("--agent-hue"); } } // clear the composer agent legend + its color
  if (composerMetaEl) { composerMetaEl.innerHTML = ""; composerMetaEl.removeAttribute("title"); } // clear the composer model·reasoning legend
  questionEl.classList.add("hidden");
  if (scrollBtn) scrollBtn.hidden = true;
  if (jobsPill) { jobsPill.hidden = true; if (jobsBtn) jobsBtn.classList.remove("is-running"); if (jobsBtnCount) jobsBtnCount.hidden = true; } // the next session re-counts its own
  syncPagerVisibility(); // the pill must not linger as an empty shell
  todosEl.classList.add("hidden"); todosEl.innerHTML = ""; todosExpanded = false; todosSig = ""; filesEl.classList.add("hidden"); filesEl.innerHTML = ""; mcpEl.classList.add("hidden"); mcpEl.innerHTML = "";
  planEl.classList.add("hidden"); planEl.innerHTML = ""; planExpanded = false; currentPlan = null; draftPlanEl = null; draftPlanExpanded = false; lastTodos = [];
  pendingAttachments = []; renderAttachStrip();
  queuedMsgs = []; queueCapable = false; renderQueueStrip(); // drop the previous session's queue; the new one replays its own
  updateVersionWarning(currentTermVersion()); // re-evaluate (hide stale banner / reset the Upgrade button)
  // Reset the composer to its idle/askable default. The single global `state` is
  // otherwise sticky across a session switch, so leaving a busy session would
  // keep the composer disabled on the next (idle) one — "can't ask while another
  // session is working". The newly-selected session's replay sends its real state.
  state = "idle"; input.disabled = false; setSendMode(false);
  syncInputPlaceholder(); // drop any lingering queue-mode placeholder from the previous session
  if (attachBtn) attachBtn.disabled = false; // re-enable attach on the now-idle composer
}

// selectSession opens a session's event stream. turnsParam (optional) is passed
// through to /events as ?turns= — "all" replays the entire transcript instead of
// the capped tail, which is how the "Load earlier history" banner works.
function selectSession(id, turnsParam) {
  document.body.classList.remove("nav-open"); // close the mobile drawer on navigate
  if (typeof window.closeWorkflowBuilder === "function") window.closeWorkflowBuilder(); // dismiss the Agent Builder overlay so the session renders in #transcript
  maybeRequestNotify(); // a click — a good moment to ask for notification permission
  clearView();
  current = id;
  if (typeof refreshJobsBadge === "function") refreshJobsBadge(); // jobs are per-session
  loadEncryptionStatus(); // connector-scoped: run now that a laptop is focused (apiBase set), not at boot
  loadQuickActions();     // also connector-scoped: the project may ship its own presets
  showConnecting(); // jumping-Claude loader until the session's replay starts streaming in
  if (laptops) {
    renderLaptopTree(); // reflect the new active session immediately
    if (currentLaptop && currentProject) loadProjectSessions(currentLaptop, currentProject).then(renderLaptopTree); // refresh titles/live state
  } else {
    loadSessions();
  }
  hideLoadEarlier();
  const q = turnsParam ? ("?turns=" + encodeURIComponent(turnsParam)) : "";
  es = new EventSource(api(`/api/sessions/${id}/events${q}`));
  historyLoaded = false; // this connection replays history first; the "history_end" marker flips it true
  es.onmessage = (m) => {
    // Guard against non-JSON payloads (proxy/error HTML on a hiccup): a raw
    // JSON.parse throw here would abort event dispatch. Log + skip instead.
    let ev;
    try { ev = JSON.parse(m.data); }
    catch (err) { console.error("SSE: bad event payload, skipping", err, m.data); return; }
    handle(ev);
  };
  // EventSource auto-reconnects on error and hides the HTTP status, so a 401 from
  // an expired session would otherwise loop forever with a stale UI. When auth is
  // on (authUser set), probe /auth/me on error: the fetch wrapper bounces to login
  // on 401; a transient network blip returns 200 and EventSource just reconnects.
  es.onerror = () => { if (authUser) fetch("/auth/me", { headers: { Accept: "application/json" } }); };
}

// renderMDInto reconciles container's children toward the markdown render of
// text, preserving the leading run of already-finalized block nodes that haven't
// changed. Replacing innerHTML wholesale on every animation frame tore down and
// recreated every completed heading/paragraph each tick — the heading's border
// and accent bar made that churn show up as a visible "blinking" header.
//
// Reconciliation compares against the KEYS of the previous render (serialized
// before insertion), NOT the live DOM: <app-icon> is a custom element that
// upgrades and rewrites its own subtree once connected, so a finalized block
// that contains an iconized emoji/checklist no longer equals a fresh parse.
// Comparing live nodes (isEqualNode) therefore diverged at the first icon and
// rebuilt every block after it — including later headings — on every frame.
// Keying off the prior fresh render is immune to that post-insertion mutation,
// so only the trailing, still-streaming block (and anything past the first real
// change) is rebuilt.
function mdNodeKey(n) {
  if (n.nodeType === Node.ELEMENT_NODE) {
    // Performance optimization: keying off node name, class, and text content
    // is vastly faster than n.outerHTML because it avoids recursively serializing
    // syntax-highlighted HTML subtrees on every streamed token.
    return "e:" + n.nodeName + ":" + (n.className || "") + ":" + n.textContent;
  }
  if (n.nodeType === Node.TEXT_NODE) return "t:" + n.nodeValue;
  if (n.nodeType === Node.COMMENT_NODE) return "c:" + n.nodeValue;
  return "o:" + n.nodeName;
}
// DIAGRAM_GLYPHS matches box-drawing (U+2500–257F), block (U+2580–259F), and
// arrow (U+2190–21FF) characters — the marks of an ASCII architecture/flow
// diagram, and rare in ordinary code (which uses plain |, -, +). A <pre> whose
// text contains any of these is treated as a diagram.
const DIAGRAM_GLYPHS = /[←-⇿─-▟]/;

// markDiagramPres tags <pre> blocks that contain diagram glyphs with .md-diagram,
// so the stylesheet can render them with a tight line-height (the vertical bars
// connect into unbroken lines) instead of the loose line-height that's right for
// regular code. Idempotent — safe to call on every incremental re-render.
function markDiagramPres(container) {
  container.querySelectorAll("pre").forEach((pre) => {
    if (DIAGRAM_GLYPHS.test(pre.textContent || "")) pre.classList.add("md-diagram");
  });
}

function renderMDInto(container, text) {
  const tpl = document.createElement("template");
  tpl.innerHTML = renderMD(text);
  const fresh = Array.from(tpl.content.childNodes);
  const freshKeys = fresh.map(mdNodeKey);
  const prevKeys = container.__mdKeys || [];
  let i = 0;
  while (i < prevKeys.length && i < freshKeys.length && prevKeys[i] === freshKeys[i]) i++;
  while (container.childNodes.length > i) container.removeChild(container.lastChild);
  for (let j = i; j < fresh.length; j++) container.appendChild(fresh[j]);
  container.__mdKeys = freshKeys;
  markDiagramPres(container); // tag architecture/flow diagrams for tight rendering
}

// renderPending coalesces a burst of streamed deltas into one render per frame.
let renderPending = false;
function scheduleAsstRender() {
  if (renderPending) return;
  renderPending = true;
  requestAnimationFrame(() => {
    renderPending = false;
    if (asstBody) { renderMDInto(asstBody, asstRaw); scrollBottom(); }
  });
}

// endAssistant flushes a final render of the streaming answer and closes the
// current assistant/thinking blocks so the next event starts fresh.
function endAssistant() {
  if (asstBody) renderMDInto(asstBody, asstRaw);
  asstBody = null; asstRaw = "";
  thinkBody = null; thinkRaw = "";
}

// startMsg builds a message block with an Material Symbols icon + speaker label header.
// cls is an optional modifier (msg-user / msg-assistant) used by the stylesheet
// to tint each turn's accent rail and label.
function startMsg(label, labelCls, iconName, cls) {
  const m = el("div", "msg" + (cls ? " " + cls : ""));
  const head = el("div", "msg-label flex items-center gap-1.5 mb-1 " + labelCls);
  head.appendChild(sym(iconName));
  head.appendChild(el("span", null, label));
  m.appendChild(head);
  return m;
}

function handle(ev) {
  // A capped initial replay: older turns exist but weren't streamed. Offer to
  // reload the session with the full transcript.
  if (ev.type === "history_start") { showLoadEarlierBanner(ev.dropped || 0); return; }
  dismissConnecting(); // first event in → drop the connecting loader
  switch (ev.type) {
    case "user": {
      endAssistant();
      endToolGroup();
      endWorkerGroup();
      asstLabeled = false; // a new turn → the next assistant answer gets a fresh label
      turnTodos = null;    // and a fresh per-question todo card in the transcript
      phaseTimes = {};     // reset per-phase timing for the new turn
      turnStartMs = Date.now(); turnActive = true; lastTurnCost = 0; lastTurnModel = ""; lastTurnReasoning = ""; lastTurnIn = 0; lastTurnOut = 0; lastTurnCacheRead = 0; stickBottom = true; // start timing this turn; jump to it
      lastTodos = [];      // drop the previous turn's todos so a plan event can't resurrect them
      renderTodos([]);     // hide the previous turn's task-list bar until new todos arrive
      const m = startMsg("you", "text-ok", "person.crop.circle", "msg-user");
      if (ev.text) {
        const ub = el("div", "user-body md break-words");
        ub.innerHTML = renderMD(ev.text); // render the prompt as markdown (sanitized)
        highlightMentions(ub);            // then chip the @agent / @worker invocations
        m.appendChild(ub);
      }
      if (ev.attachments && ev.attachments.length) {
        m.appendChild(renderAttachments(ev.attachments));
      }
      m.appendChild(userMetaFooter(ev.text || "", ev.time ? new Date(ev.time) : new Date()));
      startPair(m); // the question becomes this pair's pinned header; its answer follows in the scroll body
      break;
    }
    case "stream_reset": {
      // A transient stream error is being retried; drop the partial assistant /
      // thinking bubbles so the re-streamed response replaces them cleanly.
      if (asstBody && asstBody.parentNode) asstBody.parentNode.remove();
      if (thinkBody && thinkBody.parentNode) thinkBody.parentNode.remove();
      asstBody = null; asstRaw = ""; thinkBody = null; thinkRaw = ""; asstLabeled = false;
      break;
    }
    case "history_end": {
      historyLoaded = true; // past this marker every event is live, not replayed history
      break;
    }
    case "thinking": {
      // The provider redacts the reasoning text but still emits a marker so we know
      // reasoning happened. Don't render an empty "thinking" bubble for it.
      if (!thinkBody && ev.text === "(reasoning hidden by the provider)") break;
      if (!thinkBody) {
        endToolGroup();
        endWorkerGroup();
        const m = el("div", "msg msg-thinking");
        const head = el("button", "msg-label think-toggle flex items-center gap-1.5 mb-1 text-faint");
        head.type = "button";
        const chev = sym("chevron.right"); chev.classList.add("think-chevron");
        head.appendChild(chev);
        head.appendChild(sym("sparkles"));
        head.appendChild(el("span", null, "thinking"));
        m.appendChild(head);
        const bodyEl = el("div", "think-body md text-faint text-[13px] break-words pl-3");
        m.appendChild(bodyEl);
        // Thinking blocks always start expanded (live and replayed alike); the
        // header still toggles collapse on click. Closures capture the local bodyEl
        // (the module-level thinkBody gets nulled between blocks).
        const setCollapsed = (v) => { m.classList.toggle("collapsed", v); bodyEl.hidden = v; head.setAttribute("aria-expanded", String(!v)); };
        setCollapsed(false);
        head.onclick = () => setCollapsed(!bodyEl.hidden);
        appendMsg(m);
        thinkBody = bodyEl;
        thinkRaw = "";
      }
      thinkRaw += ev.text;
      renderMDInto(thinkBody, thinkRaw);
      break;
    }
    case "text": {
      thinkBody = null; thinkRaw = ""; // close any open thinking block
      if (!asstBody) {
        endToolGroup(); // a new prose block ends the prior run of tool calls
        endWorkerGroup();
        // Show the "moony" label once per answer; continuation blocks
        // (a tool call splits the text stream) render without repeating it.
        const m = asstLabeled
          ? el("div", "msg msg-assistant")
          : startMsg("moony", "text-brand", "cpu", "msg-assistant");
        asstLabeled = true;
        asstBody = el("div", "md break-words");
        asstRaw = "";
        m.appendChild(asstBody);
        appendMsg(m);
      }
      asstRaw += ev.text;
      scheduleAsstRender();
      break;
    }
    case "tool_start": {
      endAssistant();
      // Delegated sub-agent tool call: nest it inside the parent `task` card
      // (richer than a flat progress line — full input/output per sub-tool) and
      // don't count it in the top-level tool group or the #mcp usage panel.
      if (ev.parentId && tools[ev.parentId]) {
        const ref = buildNestedToolCard(ev);
        ensureToolKids(tools[ev.parentId]).appendChild(ref.card);
        tools[ev.toolId] = ref;
        break;
      }
      trackUsage(ev); // record skill / subagent / worker / MCP-tool usage for the #mcp panel
      const g = ensureToolGroup();
      const diffText = ev.diff || ev.input || "";
      const stats = headerStats(ev.summary, diffText);
      // bash: show the unwrapped command (header + bash-colored body) instead of
      // the raw {"command":…} JSON. The header label persists once the running
      // body is swapped for the command's output on tool_end. task: show the
      // input dict as an indented key/value block (not a one-line JSON blob), and
      // render its sub-agent result as markdown on completion.
      const cmd = bashCommand(ev);
      let ref;
      if (cmd != null) ref = buildToolCard(ev.tool, clipCmd(cmd), cmd, stats, "bash");
      else if (ev.tool === "task") ref = buildToolCard(ev.tool, ev.summary, ev.input, stats, null, { inMode: "args", outMode: "md", model: ev.model });
      else ref = buildToolCard(ev.tool, ev.summary, ev.diff || "", stats, null, { argsInput: ev.input });
      ref.stats = stats;
      ref.isDiff = cmd == null && ev.tool !== "task" && looksDiff(diffText); // diff tools keep their diff; others swap in the output on end
      g.body.appendChild(ref.card);
      bumpToolGroup(g);
      tools[ev.toolId] = ref;
      break;
    }
    case "tool_end": {
      let ref = tools[ev.toolId];
      if (!ref && ev.parentId && tools[ev.parentId]) {
        // Nested end without a recorded start — build the child into its parent.
        ref = buildNestedToolCard(ev);
        ensureToolKids(tools[ev.parentId]).appendChild(ref.card);
        tools[ev.toolId] = ref;
      }
      if (!ref) {
        const g = ensureToolGroup();
        const dt = ev.diff || ev.input || "";
        const cmd = bashCommand(ev); // see tool_start: unwrap bash command for the header/body
        if (cmd != null) {
          ref = buildToolCard(ev.tool, clipCmd(cmd), ev.output || cmd, headerStats(ev.summary, dt), ev.output ? null : "bash");
        } else if (ev.tool === "task") {
          // task: input dict as indented key/value; result as markdown.
          ref = ev.output
            ? buildToolCard(ev.tool, ev.summary, ev.output, headerStats(ev.summary, dt), null, { inMode: "md", outMode: "md" })
            : buildToolCard(ev.tool, ev.summary, ev.input, headerStats(ev.summary, dt), null, { inMode: "args", outMode: "md", model: ev.model });
        } else {
          ref = buildToolCard(ev.tool, ev.summary, ev.output || ev.diff || "", headerStats(ev.summary, dt), null, { argsInput: ev.input });
        }
        ref.isDiff = cmd == null && ev.tool !== "task" && looksDiff(dt);
        g.body.appendChild(ref.card);
        bumpToolGroup(g);
        tools[ev.toolId] = ref;
      }
      ref.card.classList.add(ev.isError ? "tool-err" : "tool-ok"); // accent the rail by outcome
      if (ev.tool === "delegate") ref.card.classList.add("tool-delegate");
      ref.stateWrap.innerHTML = "";
      ref.stateWrap.appendChild(sym(ev.isError ? "xmark.circle.fill" : "checkmark.circle.fill", ev.isError ? "text-danger" : "text-ok"));
      // Keep the diff for diff tools; for others (read_file/bash/…) show the
      // result — rendered per the card's outMode ("md" for task, else code view).
      if (!ref.isDiff && ev.output) { ref.content = ev.output; ref.renderBody(ev.output, ref.outMode || "code"); }
      // Render any images the tool returned (e.g. a browser screenshot) inline.
      if (ev.images && ev.images.length) appendToolImages(ref, ev.images);
      // Videos a tool returned (e.g. generate_video) render inline too.
      if (ev.videos && ev.videos.length) appendToolVideos(ref, ev.videos);
      break;
    }
    case "task_meta": {
      // A delegated sub-agent finished: append its turn-meta footer inside the
      // parent task card. Emitted live and also replayed from the persisted
      // per-delegation footer when a saved session reopens. parentId is the task
      // call's toolId.
      const parentRef = ev.parentId && tools[ev.parentId];
      if (parentRef) renderTaskMeta(parentRef, ev.meta || null);
      break;
    }
    case "turn_meta": {
      // A persisted per-turn footer replayed when a saved session reopens.
      endAssistant(); endToolGroup(); endWorkerGroup();
      renderReplayedTurnMeta(ev.meta || null);
      // Re-accumulate the session token totals from history so the status bar
      // matches the sum of these footers (the server's in-memory counters aren't
      // restored on reopen). renderStatusBar() runs after replay (final status).
      if (ev.meta) { cumTurnIn += ev.meta.inTokens || 0; cumTurnOut += ev.meta.outTokens || 0; }
      // This turn already has its persisted footer, so clear the live turn
      // state. Otherwise turnActive stays true from the replayed turn_start
      // and the final "idle" status at end of replay fires renderTurnMeta(),
      // appending a SECOND footer stamped with the load time (new Date()).
      turnActive = false; turnStartMs = 0;
      lastTurnCost = 0; lastTurnModel = ""; lastTurnReasoning = ""; lastTurnIn = 0; lastTurnOut = 0; lastTurnCacheRead = 0;
      break;
    }
    // Inert: a wire event whose kind the host didn't map. Rendering it as an info
    // bubble would show the literal "undefined"; dropping it shows nothing.
    case "unknown":
      return;
    case "info": {
      endAssistant(); endToolGroup(); endWorkerGroup();
      // Defensive: an info with no text would throw on .startsWith below and take
      // the whole event handler down with it.
      if (!ev.text) return;
      // Track compaction progress so the status bar can show a chip while the
      // summarise LLM call is in flight.
      if (ev.text.startsWith("Compacting ")) {
        compactingMsg = ev.text; renderStatusBar();
      } else if (ev.text.startsWith("Compacted ")) {
        compactingMsg = null; renderStatusBar();
      }
      // Model/provider switch confirmations get their own look (cpu icon +
      // name pills) instead of the generic info box, so they read as a
      // distinct "backend changed" cue rather than blending into every other
      // note (compaction, auto-continue, etc.).
      const provSwitch = ev.text.match(/^Provider set to (.+?)(?: — model set to (.+))?$/);
      const modelSwitch = !provSwitch && ev.text.match(/^Model set to (.+?)(?: \((.+)\))?$/);
      // Adaptive/cheap-reads downshift routing: name the model, provider, and
      // reasoning level as distinct pills so a cross-provider / cross-reasoning
      // fast tier reads at a glance.
      const autoRoute = !provSwitch && !modelSwitch &&
        ev.text.match(/^auto: using (.+?) on (.+?) \(reasoning: (.+?)\) for this round \((.+?)\)\.?$/);
      // Self-review status: style its model / provider / reasoning as pills too,
      // but keep the plain info icon (it's a review action, not a backend switch).
      const selfReview = !provSwitch && !modelSwitch && !autoRoute &&
        ev.text.match(/^Self-reviewing the diff for introduced bugs… \((.+?) on (.+?), reasoning: (.+?)\)$/);
      // Self-review RESULT lines (clean / flagged) carry the same model · provider
      // · reasoning attribution plus token/cost metrics — pill the first three,
      // leave the metrics plain.
      const selfReviewResult = !provSwitch && !modelSwitch && !autoRoute && !selfReview &&
        ev.text.match(/^(Self-review: no issues found\.|Self-review flagged possible issues — feeding them back to fix\.) \((.+?) on (.+?), reasoning: (.+?)(?: · (.+))?\)$/);
      const provList = !provSwitch && !modelSwitch && !autoRoute && !selfReview && !selfReviewResult && ev.text.match(/^Model providers:\n([\s\S]+)$/);
      const isModel = !!(provSwitch || modelSwitch || autoRoute || provList);
      const d = el("div", "msg msg-info text-sm" + (isModel ? " msg-info-model" : ""));
      d.appendChild(sym(isModel ? "cpu" : "info.circle.fill", "msg-info-icon"));
      const body = el("div", "md");
      if (provSwitch) {
        body.append("Provider set to ", el("span", "msg-info-pill", provSwitch[1]));
        if (provSwitch[2]) body.append(" — model set to ", el("span", "msg-info-pill", provSwitch[2]));
      } else if (modelSwitch) {
        body.append("Model set to ", el("span", "msg-info-pill", modelSwitch[1]));
        if (modelSwitch[2]) body.append(" (", el("span", "msg-info-pill", modelSwitch[2]), ")");
      } else if (autoRoute) {
        body.append(
          "auto: using ",
          el("span", "msg-info-pill msg-info-pill-model", autoRoute[1]),
          " on ",
          el("span", "msg-info-pill msg-info-pill-provider", autoRoute[2]),
          " (reasoning: ",
          el("span", "msg-info-pill msg-info-pill-reasoning", autoRoute[3]),
          ") for this round (" + autoRoute[4] + ").",
        );
      } else if (selfReview) {
        body.append(
          "Self-reviewing the diff for introduced bugs… (",
          el("span", "msg-info-pill msg-info-pill-model", selfReview[1]),
          " on ",
          el("span", "msg-info-pill msg-info-pill-provider", selfReview[2]),
          ", reasoning: ",
          el("span", "msg-info-pill msg-info-pill-reasoning", selfReview[3]),
          ")",
        );
      } else if (selfReviewResult) {
        body.append(
          selfReviewResult[1] + " (",
          el("span", "msg-info-pill msg-info-pill-model", selfReviewResult[2]),
          " on ",
          el("span", "msg-info-pill msg-info-pill-provider", selfReviewResult[3]),
          ", reasoning: ",
          el("span", "msg-info-pill msg-info-pill-reasoning", selfReviewResult[4]),
          (selfReviewResult[5] ? " · " + selfReviewResult[5] : "") + ")",
        );
      } else if (provList) {
        body.appendChild(renderProviderList(provList[1]));
      } else {
        body.innerHTML = renderMD(ev.text);
        highlightCommands(body); // bold-brand the slash commands the note references
      }
      d.appendChild(body);
      appendMsg(d);
      break;
    }
    case "context": {
      endAssistant(); endToolGroup(); endWorkerGroup();
      renderContextBreakdown(ev.context);
      scrollBottom();
      break;
    }
    case "error": {
      endAssistant(); endToolGroup(); endWorkerGroup();
      const d = el("div", "msg msg-error flex items-center gap-1.5 text-danger");
      d.appendChild(sym("exclamationmark.triangle.fill"));
      d.appendChild(el("span", null, ev.text));
      appendMsg(d);
      break;
    }
    case "approval": {
      const diffText = ev.diff || ev.input || "";
      // Header: the tool name as a mono chip, with the summary (path/command)
      // trailing it, muted. The +N −N diff badge lives in its own right-aligned
      // slot rather than tacked onto the question text.
      const q = $(".q", approval);
      q.textContent = "";
      q.appendChild(el("span", "app-approval-tool", ev.tool || "tool"));
      const sum = stripStats(ev.summary);
      if (sum) q.appendChild(el("span", "app-approval-summary", sum));
      const counts = $(".app-approval-counts", approval);
      counts.textContent = "";
      appendDiffCount(counts, headerStats(ev.summary, diffText));
      renderDiff($("pre", approval), diffText);
      approval.classList.remove("hidden");
      break;
    }
    case "question": {
      renderQuestion(ev.question || null);
      break;
    }
    case "state": {
      if (typeof ev.canQueue === "boolean") queueCapable = ev.canQueue;
      setState(ev.state);
      break;
    }
    // A job was started or killed through the panel (here or in another tab):
    // re-count immediately instead of waiting out the poll.
    case "jobs_changed": {
      if (typeof refreshJobsBadge === "function") refreshJobsBadge();
      break;
    }
    case "queue": {
      queuedMsgs = ev.queue || [];
      renderQueueStrip();
      renderStatusBar();
      break;
    }
    case "status": {
      lastStatus = ev.status || null;
      renderHeader();
      renderStatusBar();
      renderLeftPanel(lastStatus);
      renderComposerAgent(lastStatus); // primary-agent name in the composer's top border
      renderComposerMeta(lastStatus);  // model · reasoning in the composer's top-right border
      refreshHostRow(); // keep the sidebar's laptop name + relay accent current
      break;
    }
    case "todos": {
      lastTodos = ev.todos || [];
      renderTodos(lastTodos);
      renderTurnTodos(lastTodos);
      break;
    }
    case "board_message": {
      // A shared message-board post (live or replayed): add it to the Agent
      // Messages panel showing the multi-agent conversation.
      if (ev.board) { boardMsgs.push(ev.board); renderBoardMsgs(); }
      break;
    }
    case "plan": {
      currentPlan = ev.plan || null;
      renderPlan(currentPlan);
      renderTodos(lastTodos); // a draft→approved flip reveals the Tasks bar; a fresh draft hides it again
      break;
    }
    case "worker": {
      // Live progress from a delegated worker / workflow node (summary holds the
      // kind). Collapse the discrete tool/status lines into one "N worker updates"
      // disclosure; the full answer arrives separately as the delegate tool result.
      // A "output" kind (workflow nodes) carries a block's model response — show it
      // as a distinct, markdown response row so you can see what each node produced.
      if (ev.summary === "output" && ev.text) {
        const g = ensureWorkerGroup();
        const row = el("div", "worker-row worker-row-output msg-worker text-xs");
        const md = el("div", "worker-row-body md break-words");
        md.innerHTML = renderMD(ev.text);
        row.appendChild(md);
        g.body.appendChild(row);
        bumpWorkerGroup(g);
        scrollBottom();
      } else if ((ev.summary === "tool" || ev.summary === "info") && ev.text) {
        const g = ensureWorkerGroup();
        const row = el("div", "worker-row msg-worker text-faint text-xs flex items-baseline gap-1.5");
        row.appendChild(el("span", "worker-row-name shrink-0", ev.tool || "worker"));
        row.appendChild(el("span", "break-words", ev.text));
        g.body.appendChild(row);
        bumpWorkerGroup(g);
        scrollBottom();
      }
      break;
    }
    case "step": {
      curStep = ev.step || 0; maxStep = ev.maxSteps || 0;
      renderStatusBar();
      break;
    }
    case "turn_done": {
      // Remember this turn's answering cost so the footer (rendered when the
      // session goes idle) shows the per-turn cost, not the session total.
      if (ev.costUSD > 0) lastTurnCost = ev.costUSD;
      lastTurnModel = ev.model || ""; // the model that actually served this turn (may be an adaptive/cheap-reads downshift)
      lastTurnReasoning = ev.reasoning || ""; // …and the reasoning level it actually used (may be a Fast-tier downshift)
      // …and its total tokens + cached-input subset for the cache-hit display.
      if (ev.turnIn > 0) lastTurnIn = ev.turnIn;
      if (ev.turnOut > 0) lastTurnOut = ev.turnOut;
      if (ev.turnCacheRead > 0) lastTurnCacheRead = ev.turnCacheRead;
      // Add this turn into the running session token totals shown in the status
      // bar, so it stays in sync with the sum of the per-turn meta footers.
      cumTurnIn += ev.turnIn || 0;
      cumTurnOut += ev.turnOut || 0;
      break;
    }
    case "backend_switched": {
      // The quota fallback hopped to another provider mid-turn. Refresh the status
      // (model chip, provider label, quota pill) from the server rather than waiting
      // for the turn to end, so the exhausted provider isn't left on screen.
      if (lastStatus) {
        lastStatus.provider = ev.text || lastStatus.provider;
        lastStatus.model = ev.model || lastStatus.model;
        if (ev.reasoning) lastStatus.reasoning = ev.reasoning;
        renderHeader();
      }
      break;
    }
    case "title": {
      // The agent auto-named this session after its first turn; refresh the
      // sidebar so the new name replaces "(untitled)" / the first-message guess.
      if (laptops) { if (currentLaptop && currentProject) loadProjectSessions(currentLaptop, currentProject).then(renderLaptopTree); }
      else loadSessions();
      break;
    }
    case "commands": {
      commandList = ev.commands || [];
      mentionList = ev.mentions || [];
      // The list can arrive AFTER the user already typed "/" or "@" (always so
      // in mirror mode until the terminal publishes; racy on a fresh connect).
      // Re-render so an open-but-empty palette/dropdown fills in instead of
      // staying hidden until the next keystroke. Both self-hide when N/A.
      renderPalette();
      renderMentions();
      break;
    }
    case "sessions": {
      // Only the terminal mirror publishes the project's session list this way;
      // in tree mode we own the laptop/project sessions, so ignore stray events.
      if (laptops) break;
      projectSessions = ev.sessions || [];
      renderSessionList();
      break;
    }
    case "menu": {
      if (ev.menu) showMenu(ev.menu);
      break;
    }
    case "reset": {
      endAssistant(); endToolGroup(); endWorkerGroup();
      transcript.innerHTML = ""; resetPairs(); tools = {}; turnTodos = null; phaseTimes = {}; turnActive = false; approval.classList.add("hidden"); questionEl.classList.add("hidden"); hideMenu();
      removeDraftPlanInline(); // drop the inline draft-plan ref + reset its expand state (transcript was just cleared)
      usage = { skill: {}, agent: {}, worker: {}, mcp: {}, builtin: {} }; renderUsage(); // clear the usage panel on a session reset
      boardMsgs = []; renderBoardMsgs(); // clear the agent-messages panel on a session reset
      break;
    }
  }
  scrollBottom();
}

function setState(s) {
  const prev = state;
  state = s;
  // Answer finished: chime when leaving a working state for idle (the web
  // equivalent of the TUI's completion sound). prev is "idle" on first load and
  // when already idle, so this only fires on a real turn/await completion.
  if (s === "idle" && prev && prev !== "idle") chime();
  if (s === "idle") renderTurnMeta(); // a completed turn → append its duration · cost · model · calls footer (before curStep is reset below)
  if (s !== "busy") { curStep = 0; maxStep = 0; }
  if (s !== "awaiting_approval") approval.classList.add("hidden");
  if (s !== "awaiting_question") questionEl.classList.add("hidden");
  // The composer stays usable while busy on queue-capable sessions so you can
  // type ahead and Enter to enqueue (the relay terminal-mirror, which can't
  // queue, keeps the composer locked while busy).
  input.disabled = (s !== "idle") && !(queueCapable && s === "busy");
  syncInputPlaceholder(); // reflect busy+queue-capable (or idle/mobile) in the placeholder
  if (attachBtn) attachBtn.disabled = (s !== "idle"); // freeze attach while a turn is active — files are only staged for the next message when idle
  if (s !== "idle") closeAttachMenu(); // and close the menu (un-rotating the icon) if it was open
  setSendMode(s === "busy");                      // the Send button doubles as Stop while answering
  sendBtn.disabled = (s === "awaiting_approval" || s === "awaiting_question"); // respond in the panel, not here
  document.body.classList.toggle("agent-busy", s === "busy"); // tint the status bar while working
  document.body.classList.toggle("agent-awaiting", s === "awaiting_approval" || s === "awaiting_question"); // distinct "your turn" tint
  renderStatusBar();
  syncUpgradeBtn(); // enable/disable the Upgrade button with the turn state
  if (projectSessions) renderSessionList(); // refresh the active session's attention badge (mirror mode)
  loadSessions();
}

// setSendMode flips the composer button between Send (idle) and Stop (busy), so
// one control both sends a prompt and interrupts the running turn.
function setSendMode(stop) {
  sendBtn.title = stop ? "Stop" : "Send";
  sendBtn.setAttribute("aria-label", stop ? "Stop" : "Send");
  sendBtn.classList.toggle("bg-brand", !stop);
  sendBtn.classList.toggle("bg-danger", stop);
  sendBtn.innerHTML = "";
  const icon = sym(stop ? "stop.fill" : "arrow.up");
  icon.style.fontSize = "18px";
  sendBtn.appendChild(icon);
}

const fmtUSD = (n) => "$" + (Number(n) || 0).toFixed(2);

// fmtTokens renders a token count compactly, matching the terminal status bar:
// the raw number below 1,000, "x.xk" in the thousands, "x.xm" at a million+.
const fmtTokens = (n) => {
  n = Number(n) || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "m";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(n);
};

// versionLt reports whether semver a is strictly less than b ("x.y.z"). Returns
// false for anything unparseable so we never show a bogus "update" nudge.
function versionLt(a, b) {
  const parse = (v) => String(v || "").replace(/^v/, "").split(".").map((n) => parseInt(n, 10));
  const av = parse(a), bv = parse(b);
  if (av.length < 3 || bv.length < 3 || av.some(Number.isNaN) || bv.some(Number.isNaN)) return false;
  for (let i = 0; i < 3; i++) if (av[i] !== bv[i]) return av[i] < bv[i];
  return false;
}

// SELF_UPDATE_MIN is the first version whose terminal has the /update command, so
// only those can be upgraded in place (older ones must download + install).
const SELF_UPDATE_MIN = "1.3.0";

// focusedLaptopVersion returns the build version reported by the laptop the user
// is viewing (currentLaptop), from the laptop list — "" if unknown/offline.
function focusedLaptopVersion(list) {
  if (!currentLaptop) return "";
  const l = (list || laptops || []).find((x) => x && x.host === currentLaptop && x.online !== false);
  return (l && l.version) || "";
}

// currentTermVersion is the connected terminal's build version for the version
// banner. In tree mode it trusts the live laptop poll (refreshed every few
// seconds, so the banner clears once a terminal updates + reconnects) and falls
// back to the viewed session's status version — mirror mode, or an older
// connector that doesn't report its version in /api/laptops.
function currentTermVersion() {
  if (laptops) {
    const v = focusedLaptopVersion(laptops);
    if (v) return v;
  }
  return (lastStatus && lastStatus.version) || "";
}

// followLiveSessionAfterUpgrade recovers the view after an in-place Upgrade. The
// terminal self-updates and re-execs into a NEW session, orphaning whatever the
// web was viewing (it freezes on the /update "Checking…" line, with no result
// since the old session is gone). Once the terminal is back (new version), this
// re-points to the focused project's current live session so the view is live
// again instead of stuck.
function followLiveSessionAfterUpgrade() {
  if (!currentLaptop || !currentProject) return;
  const host = currentLaptop, key = currentProject, pk = host + "\n" + key;
  loadProjectSessions(host, key).then(() => {
    const list = projectSessionsCache[pk] || [];
    const live = list.find((s) => s.live);
    if (live && live.id !== current) {
      selectSession(live.id); // jump to the freshly-restarted terminal session
    }
    renderLaptopTree();
  }).catch(() => {});
}

// updateVersionWarning shows the banner when the connected terminal (termVer,
// from its status event) is behind this web UI / relay (relayVersion). Hidden
// when the versions match, the relay version is unknown (mirror mode), or the
// user dismissed it this page load. When the terminal supports self-update it
// offers the in-place Upgrade button; otherwise it offers a Download link.
function updateVersionWarning(termVer) {
  if (!versionWarnEl) return;
  const behind = !versionWarnDismissed && !!relayVersion && !!termVer && versionLt(termVer, relayVersion);
  // Clear a pending Upgrade once the terminal reports back: either it caught up
  // (no longer behind) or it restarted on a different build. Without this the
  // button stays stuck on "Upgrading…" until the blind 2-minute timeout — even
  // though the terminal already updated (and reconnected) or was already current.
  if (upgrading && termVer && (!behind || termVer !== upgradeStartVer)) {
    upgrading = false;
    followLiveSessionAfterUpgrade(); // terminal restarted on the new build → recover the orphaned view
  }
  if (behind) {
    const canGet = relayDownloads;                          // relay serves the binaries
    const selfUpdate = canGet && !versionLt(termVer, SELF_UPDATE_MIN); // terminal has /update
    const action = !canGet ? "Update your local moony to match."
      : selfUpdate ? "Click Upgrade to update it in place."
      : "Download the latest and reinstall to update.";
    versionWarnTextEl.textContent = "";
    versionWarnTextEl.append(
      "Your terminal (", el("b", null, "v" + termVer), ") is behind the web UI (",
      el("b", null, "v" + relayVersion), "). " + action
    );
    if (versionWarnUpgradeEl) versionWarnUpgradeEl.style.display = selfUpdate ? "" : "none";
    if (versionWarnDownloadEl) versionWarnDownloadEl.style.display = (canGet && !selfUpdate) ? "" : "none";
  }
  versionWarnEl.classList.toggle("hidden", !behind);
  // The terminal banner just took or released the strip — let the reload prompt
  // (if one is pending) hide behind it or come forward accordingly.
  refreshReloadBanner();
  syncUpgradeBtn();
}

// syncUpgradeBtn reflects the current state on the Upgrade button: "Upgrading…"
// and disabled while an upgrade is in flight; disabled while a turn is running
// (remote input is ignored unless the terminal is idle); otherwise enabled.
function syncUpgradeBtn() {
  if (!versionWarnUpgradeEl) return;
  // While the remote /update runs, the terminal rebuilds and reconnects on the
  // new build — a "waiting for it to come back" state. Swap the warning triangle
  // for the jumping Claude so the banner reads as actively working, not warning.
  if (versionWarnIconEl) versionWarnIconEl.style.display = upgrading ? "none" : "";
  if (versionWarnArtEl) versionWarnArtEl.hidden = !upgrading;
  if (upgrading) {
    versionWarnUpgradeEl.disabled = true;
    versionWarnUpgradeEl.textContent = "Updating…";
    versionWarnUpgradeEl.title = "";
    return;
  }
  const busy = state !== "idle";
  versionWarnUpgradeEl.disabled = busy;
  versionWarnUpgradeEl.textContent = "Upgrade";
  versionWarnUpgradeEl.title = busy ? "Finish the current turn first" : "";
}

// renderHeader mirrors the terminal's top banner: model · reasoning:level ·
// project-dir:branch status · approval mode, with the app version next to the
// name. (The host/laptop name lives at the root of the sidebar tree, not here.)
// setHeaderVersion shows the web UI's (relay) version in the header. It's
// independent of any session/laptop — the relay reports it at boot via
// GET /api/relay — so a version shows even with no laptops connected. Falls back
// to the connected terminal's version when no relay reported one (standalone/dev).
function setHeaderVersion() {
  const termVer = lastStatus && lastStatus.version;
  hdrVersionEl.textContent = relayVersion ? "v" + relayVersion : (termVer ? "v" + termVer : "");
}

// thinkTierClass maps a reasoning level to a color class for the header. "auto"
// is the adaptive meta-level (off the intensity ramp) → its own green; the fixed
// rungs form a cool→hot ramp (off/minimal faint, low/medium brand, high/x-high
// amber, max red). Unknown levels fall back to the bold ink-bright default.
function thinkTierClass(level) {
  switch ((level || "").toLowerCase()) {
    case "auto":                return " hdr-think-auto";
    case "off": case "minimal": return " hdr-think-off";
    case "low": case "medium":  return " hdr-think-calm";
    case "high": case "x-high": return " hdr-think-deep";
    case "max":                 return " hdr-think-max";
    default:                    return "";
  }
}

function renderHeader() {
  const s = lastStatus;
  updateAvatar(s);
  setHeaderVersion();
  updateVersionWarning(currentTermVersion());
  hdrMetaEl.innerHTML = "";
  if (!s) return;
  const parts = [];
  // Model + reasoning live on the composer (the top-right notched legend), not here.
  // Project + git token: "<project> : <branch> ●N ↑a ↓b". Click opens the git
  // modal (read-only overview). Full working-dir path shown on hover.
  if (s.workingDir || s.gitBranch) {
    const proj = el("span", "hdr-git inline-flex items-center gap-1 min-w-0");
    proj.setAttribute("role", "button");
    proj.tabIndex = 0;
    if (s.workingDir) {
      const wd = s.workingDir, i = wd.lastIndexOf("/");
      proj.appendChild(el("span", "font-semibold text-ink truncate", i >= 0 ? wd.slice(i + 1) : wd));
    }
    if (s.gitBranch) {
      if (s.workingDir) proj.appendChild(el("span", "opacity-50", ":"));
      proj.appendChild(el("span", "font-semibold text-ink", s.gitBranch));
      if (s.gitDirty) {
        const dot = el("span", "hdr-git-dirty inline-flex items-center gap-0.5");
        dot.appendChild(el("span", "hdr-git-dot"));
        dot.appendChild(document.createTextNode(String(s.gitDirty)));
        dot.title = s.gitDirty + " uncommitted change" + (s.gitDirty === 1 ? "" : "s");
        proj.appendChild(dot);
      }
      if (s.gitAhead) proj.appendChild(el("span", "hdr-git-ahead", "↑" + s.gitAhead));
      if (s.gitBehind) proj.appendChild(el("span", "hdr-git-behind", "↓" + s.gitBehind));
    }
    const upPart = s.gitUpstream ? " (upstream " + s.gitUpstream + ")" : "";
    proj.title = (s.workingDir || "") + upPart + " — click for git";
    const openGit = (e) => { e.stopPropagation(); openGitModal(); };
    proj.onclick = openGit;
    proj.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openGit(e); } };
    parts.push(proj);
  }
  // The active agent is shown on the composer (the notched legend), not here.
  // The mode badge below still surfaces plan mode (status overrides approval to
  // "plan"), so dropping the agent badge doesn't lose the plan indicator.
  if (s.approval) {
    const ap = el("span", "hdr-approval cursor-pointer", "approval: ");
    ap.appendChild(el("span", "font-semibold " + (s.approval === "plan" ? "text-warn" : "text-brand"), s.approval));
    ap.title = "Approval policy — click to change";
    ap.setAttribute("role", "button");
    ap.tabIndex = 0;
    ap.onclick = (e) => { e.stopPropagation(); openHdrMenu(ap, "approval", APPROVAL_LEVELS, s.approval, "/approval"); };
    ap.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openHdrMenu(ap, "approval", APPROVAL_LEVELS, s.approval, "/approval"); } };
    parts.push(ap);
  }
  if (s.sandbox) parts.push(el("span", "text-ok", s.sandbox));
  parts.forEach((p, i) => {
    if (i > 0) hdrMetaEl.appendChild(el("span", "opacity-50", "·"));
    hdrMetaEl.appendChild(p);
  });
}

// agentHue maps a custom-agent name to a stable hue (0–359) for its composer
// color. Deterministic, so the agent looks the same across reloads; the actual
// saturation/lightness are applied per-theme in CSS (.composer.has-agent).
function agentHue(name) {
  const k = (name || "").toLowerCase();
  let h = 0;
  for (let i = 0; i < k.length; i++) h = (h * 31 + k.charCodeAt(i)) % 360;
  return h;
}

// --- Shared header/composer picker ------------------------------------------
// A small dropdown opened by clicking a "setting: value" token (approval level,
// reasoning level). Each option routes through the matching slash command (which
// sets + persists). Body-level + position:fixed so it survives the header/composer
// re-rendering on every status event and escapes the topbar's backdrop stacking
// context. One menu element reused for both pickers.
const APPROVAL_LEVELS = [
  { id: "prompt", label: "Prompt", desc: "confirm every mutating action" },
  { id: "safe", label: "Safe", desc: "auto-run read-only; confirm the rest" },
  { id: "auto", label: "Auto", desc: "never prompt (except git commit/push)" },
  { id: "danger-skip-permission", label: "Danger (skip permission)", desc: "auto-approve everything incl. git push; plan-draft still needs approval" },
];
// REASONING_LEVELS is the generic fallback set used only before the first status
// event arrives; once it does, reasoningOptions() uses the model-specific list
// the server sends (status.reasoningLevels — Gemini/Opus/others differ).
const REASONING_LEVELS = [
  { id: "auto", label: "Auto", desc: "pick depth per turn" },
  { id: "off", label: "Off", desc: "no extended thinking" },
  { id: "low", label: "Low", desc: "brief reasoning" },
  { id: "medium", label: "Medium", desc: "balanced" },
  { id: "high", label: "High", desc: "deeper, costlier" },
  { id: "x-high", label: "X-High", desc: "very deep" },
  { id: "max", label: "Max", desc: "maximum (slowest)" },
];
// PROVIDER_OPTIONS is the /provider backend picker (mirrors internal/provider's
// registry). Choosing one sends "/provider <id>".
const PROVIDER_OPTIONS = [
  { id: "ollama", label: "Ollama", desc: "local (or remote) Ollama server — no API key (default)" },
  { id: "openai", label: "OpenAI", desc: "api.openai.com" },
  { id: "anthropic", label: "Anthropic", desc: "api.anthropic.com (Claude)" },
  { id: "sqe-anthropic", label: "SQE Anthropic", desc: "Claude via the SQE LAN gateway (default http://192.168.33.158:8787)" },
  { id: "openrouter", label: "OpenRouter", desc: "openrouter.ai" },
  { id: "gemini", label: "Gemini", desc: "Google's OpenAI-compatible endpoint" },
  { id: "deepseek", label: "DeepSeek", desc: "api.deepseek.com" },
  { id: "groq", label: "Groq", desc: "api.groq.com" },
  { id: "xai", label: "xAI (Grok)", desc: "api.x.ai" },
  { id: "mistral", label: "Mistral", desc: "api.mistral.ai" },
  { id: "together", label: "Together.ai", desc: "api.together.xyz" },
  { id: "fireworks", label: "Fireworks.ai", desc: "api.fireworks.ai" },
  { id: "moonshot", label: "Moonshot", desc: "api.moonshot.ai (Kimi)" },
  { id: "zai", label: "Z.AI", desc: "api.z.ai (Zhipu GLM)" },
  { id: "dashscope", label: "DashScope", desc: "Alibaba DashScope (Qwen)" },
  { id: "nvidia", label: "NVIDIA NIM", desc: "integrate.api.nvidia.com" },
  { id: "huggingface", label: "Hugging Face", desc: "router.huggingface.co" },
  { id: "venice", label: "Venice", desc: "api.venice.ai" },
  { id: "omniroute", label: "OmniRoute", desc: "local OmniRoute gateway (default http://localhost:20128/v1) — needs MN_OMNIROUTE_API_KEY" },
  { id: "cerebras", label: "Cerebras", desc: "api.cerebras.ai — very fast inference for open models" },
  { id: "perplexity", label: "Perplexity", desc: "api.perplexity.ai (Sonar) — web-grounded answers with citations" },
  { id: "cohere", label: "Cohere", desc: "api.cohere.ai (Command, OpenAI-compatible)" },
  { id: "deepinfra", label: "DeepInfra", desc: "api.deepinfra.com — large catalog of hosted open models, cheap" },
  { id: "meta", label: "Meta Model API", desc: "api.meta.ai — Meta's Muse Spark coding models, 1M context" },
  { id: "lmstudio", label: "LM Studio", desc: "local LM Studio server (default http://localhost:1234/v1) — no API key" },
  { id: "llamacpp", label: "llama.cpp", desc: "local llama.cpp / llama-server (default http://localhost:8080/v1) — no API key" },
  { id: "azure", label: "Azure OpenAI", desc: "OpenAI models on your Azure resource — needs MN_AZURE_API_KEY + MN_AZURE_BASE_URL" },
  { id: "vertex", label: "Vertex AI", desc: "Google Vertex AI (OpenAI-compatible) — needs MN_VERTEX_API_KEY + MN_VERTEX_PROJECT/MN_VERTEX_LOCATION" },
  { id: "bedrock", label: "AWS Bedrock", desc: "Claude et al. on AWS, SigV4-signed — needs AWS credentials (MN_AWS_* / standard AWS env)" },
  { id: "copilot", label: "GitHub Copilot", desc: "via the copilot CLI (autonomous, its own tools)" },
];
function hdrMenuOpen() { return hdrMenuEl && !hdrMenuEl.classList.contains("hidden"); }
function closeHdrMenu() { if (hdrMenuEl) { hdrMenuEl.classList.add("hidden"); hdrMenuEl.innerHTML = ""; hdrMenuEl.dataset.kind = ""; } }
// openHdrMenu(anchor, kind, options, current, command): toggles the picker under
// `anchor`; choosing an option (other than the current one) sends `command <id>`.
function openHdrMenu(anchor, kind, options, current, command) {
  if (!hdrMenuEl) return;
  if (hdrMenuOpen() && hdrMenuEl.dataset.kind === kind) { closeHdrMenu(); return; }
  hdrMenuEl.innerHTML = "";
  hdrMenuEl.dataset.kind = kind;
  options.forEach((o) => {
    const item = el("button", "hdr-menu-item" + (o.id === current ? " is-current" : ""));
    item.type = "button";
    item.setAttribute("role", "menuitemradio");
    item.setAttribute("aria-checked", o.id === current ? "true" : "false");
    item.appendChild(el("span", "hdr-menu-label", o.label));
    if (o.desc) item.appendChild(el("span", "hdr-menu-desc", o.desc));
    item.onclick = (e) => {
      e.stopPropagation();
      closeHdrMenu();
      if (o.id !== current) {
        sendText(command + " " + o.id);
      }
    };
    hdrMenuEl.appendChild(item);
  });
  placeHdrMenu(anchor);
}

// openHdrInfo(anchor, kind, rows): toggles the shared picker as a read-only info
// popover (no selectable options / command dispatch) — used by the status bar's
// quota token to surface the quota name + usage. `rows` is an array of
// {label, desc} shown with the same styling as the selectable menu items.
function openHdrInfo(anchor, kind, rows) {
  if (!hdrMenuEl) return;
  if (hdrMenuOpen() && hdrMenuEl.dataset.kind === kind) { closeHdrMenu(); return; }
  hdrMenuEl.innerHTML = "";
  hdrMenuEl.dataset.kind = kind;
  rows.forEach((row) => {
    const item = el("div", "hdr-menu-item is-info");
    item.appendChild(el("span", "hdr-menu-label", row.label));
    if (row.desc) item.appendChild(el("span", "hdr-menu-desc", row.desc));
    hdrMenuEl.appendChild(item);
  });
  placeHdrMenu(anchor);
}

// placeHdrMenu positions the shared picker relative to `anchor`: below it by
// default, flipped ABOVE when it fits better there (e.g. a pill in the bottom
// composer), and — when the menu is taller than the room on either side (e.g.
// the multi-provider picker) — pinned to the roomier side with a capped,
// scrollable height so it never runs off-screen. Also nudged inside the viewport
// horizontally. Assumes the menu is populated.
function placeHdrMenu(anchor) {
  const margin = 8;
  const r = anchor.getBoundingClientRect();
  hdrMenuEl.classList.remove("hidden");
  hdrMenuEl.style.maxHeight = ""; // clear any prior cap before measuring natural height
  const mh = hdrMenuEl.getBoundingClientRect().height;
  const spaceBelow = window.innerHeight - r.bottom - margin;
  const spaceAbove = r.top - margin;
  let top;
  if (mh <= spaceBelow) {
    top = r.bottom + 6; // fits below
  } else if (mh <= spaceAbove) {
    top = r.top - mh - 6; // fits above
  } else if (spaceAbove > spaceBelow) {
    // Too tall for either side: use the roomier one, cap height + scroll.
    hdrMenuEl.style.maxHeight = Math.max(120, spaceAbove) + "px";
    top = margin;
  } else {
    hdrMenuEl.style.maxHeight = Math.max(120, spaceBelow) + "px";
    top = r.bottom + 6;
  }
  hdrMenuEl.style.top = Math.round(top) + "px";
  const mw = hdrMenuEl.getBoundingClientRect().width;
  let left = r.left;
  if (left + mw > window.innerWidth - margin) left = window.innerWidth - margin - mw;
  hdrMenuEl.style.left = Math.round(Math.max(margin, left)) + "px";
}
// An outside click or Escape dismisses it (mirrors the attach menu).
document.addEventListener("click", () => { if (hdrMenuOpen()) closeHdrMenu(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && hdrMenuOpen()) closeHdrMenu(); });

// renderComposerAgent shows the active primary-agent name notched into the
// composer's top border (see .composer-agent) and tints both the legend and the
// textarea border with that agent's color. Built-ins use the semantic tokens
// (build=brand, plan=amber — theme-aware); custom agents use a stable hue whose
// shade shifts per theme in CSS. Inputs are set on .composer (the shared parent),
// which resolves them into --agent-color; cleared (→ neutral defaults) when none.
function renderComposerAgent(s) {
  if (!composerAgentEl) return;
  const name = s && s.agent ? s.agent : "";
  composerAgentEl.textContent = name;
  const composerEl = composerAgentEl.parentElement; // .composer (parent of #input + legend)
  if (composerEl) {
    composerEl.classList.toggle("has-agent", !!name);
    const k = name.toLowerCase();
    if (!name) {
      composerEl.style.removeProperty("--agent-base");
      composerEl.style.removeProperty("--agent-hue");
    } else if (k === "build" || k === "plan") {
      composerEl.style.setProperty("--agent-base", k === "plan" ? "rgb(var(--c-warn))" : "rgb(var(--c-brand))");
      composerEl.style.removeProperty("--agent-hue");
    } else {
      composerEl.style.setProperty("--agent-hue", String(agentHue(name)));
      composerEl.style.removeProperty("--agent-base");
    }
  }
  if (name) composerAgentEl.title = "Active agent: " + name + " — click to switch";
}

// renderComposerMeta shows "model · reasoning" notched into the composer's
// top-right border (mirroring the agent legend on the left). Model is prettified
// + brand-colored; the reasoning level reuses the header's intensity color ramp.
// Empty (hidden via :empty) when there's no status/model.
function renderComposerMeta(s) {
  if (!composerMetaEl) return;
  composerMetaEl.innerHTML = "";
  if (!s || !s.model) { composerMetaEl.removeAttribute("title"); return; }
  // Provider (the /provider backend, e.g. Ollama / OpenAI / Anthropic)
  // before the model name, as its own clickable pill that opens the
  // provider picker. Omitted when unknown. stopPropagation keeps the surrounding
  // legend's model-picker click from also firing.
  const prov = providerLabel(s.llmProvider);
  if (prov) {
    // A quota fallback switches provider on its own, so the pill can differ from
    // what was picked. Mark it and say why, or the change looks like a bug.
    const fbFrom = s.quotaFallbackFrom ? providerLabel(s.quotaFallbackFrom) : "";
    const pp = el("span", "hdr-think-pill cm-provider" + (fbFrom ? " is-fallback" : ""), prov);
    pp.setAttribute("role", "button");
    pp.tabIndex = 0;
    if (fbFrom) {
      let t = "Quota fallback: " + fbFrom + " hit its budget, so this session moved to " + prov + ".";
      if (s.quotaFallbackResetAt) {
        const at = new Date(s.quotaFallbackResetAt);
        if (!isNaN(at)) t += " Reverting to " + fbFrom + " after " + at.toLocaleString() + ".";
      }
      pp.title = t + " Click to change.";
    } else {
      pp.title = "Provider: " + prov + " — click to change";
    }
    const openProvider = (e) => { e.stopPropagation(); openHdrMenu(pp, "provider", PROVIDER_OPTIONS, s.llmProvider || "ollama", "/provider"); };
    pp.onclick = openProvider;
    pp.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openProvider(e); } };
    composerMetaEl.appendChild(pp);
    composerMetaEl.appendChild(el("span", "cm-sep", "·"));
  }
  composerMetaEl.appendChild(el("span", "cm-model", prettyModel(s.model)));
  // Reasoning pill. Show it even when off (status sends reasoning:"" for off) so
  // it stays clickable to turn thinking back on; the level list is the set valid
  // for THIS model (status.reasoningLevels — Gemini/Opus/others differ), falling
  // back to the generic set before the first status arrives.
  const level = s.reasoning || "off";
  composerMetaEl.appendChild(el("span", "cm-sep", "·"));
  // The reasoning value is its own clickable pill → opens the reasoning-level
  // picker. stopPropagation keeps the surrounding legend's model-picker click
  // (the container handler) from also firing.
  const think = el("span", "hdr-think-pill hdr-think-level" + thinkTierClass(level), level);
  think.setAttribute("role", "button");
  think.tabIndex = 0;
  think.title = "Reasoning: " + level + " — click to change";
  const openReasoning = (e) => { e.stopPropagation(); openHdrMenu(think, "reasoning", reasoningOptions(s), level, "/reasoning"); };
  think.onclick = openReasoning;
  think.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openReasoning(e); } };
  composerMetaEl.appendChild(think);
  // Session-memory pill → opens the AGENTS.md rules editor. Shows the rule count.
  composerMetaEl.appendChild(el("span", "cm-sep", "·"));
  const n = s.memoryCount || 0;
  const mem = el("span", "cm-mem");
  mem.appendChild(sym("brain"));
  mem.appendChild(document.createTextNode(n ? String(n) : ""));
  mem.setAttribute("role", "button");
  mem.tabIndex = 0;
  mem.title = "Session memory — " + (n ? n + " project rule" + (n === 1 ? "" : "s") : "no rules yet") + " (click to edit AGENTS.md rules)";
  const openMem = (e) => { e.stopPropagation(); openMemoryEditor(); };
  mem.onclick = openMem;
  mem.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openMem(e); } };
  composerMetaEl.appendChild(mem);

  // Read-only file browser for this session's working directory.
  const files = el("span", "cm-mem");
  files.appendChild(sym("doc.text.magnifyingglass"));
  files.setAttribute("role", "button");
  files.tabIndex = 0;
  files.title = "Browse this project's files (read-only)";
  const openFV = (e) => { e.stopPropagation(); openFileViewer(); };
  files.onclick = openFV;
  files.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openFV(e); } };
  composerMetaEl.appendChild(files);

  composerMetaEl.title = "Model: " + s.model + " — click to change";
}

// reasoningOptions returns the picker options for the current model: the valid
// levels the server sent for it (status.reasoningLevels, which differ between
// Gemini/Opus/other families), or the generic fallback before any status arrives.
function reasoningOptions(s) {
  const lvls = s && Array.isArray(s.reasoningLevels) ? s.reasoningLevels : null;
  if (lvls && lvls.length) {
    return lvls.map((l) => ({ id: l.id, label: titleCase(l.id), desc: l.desc || "" }));
  }
  return REASONING_LEVELS;
}

// titleCase upcases the first letter (and after a hyphen) for a level id label,
// so "x-high" → "X-High", "auto" → "Auto".
function titleCase(s) {
  return String(s || "").split("-").map((w) => w ? w.charAt(0).toUpperCase() + w.slice(1) : w).join("-");
}

// updateAvatar reflects the host machine in the top-bar account avatar — this
// tool has no auth/user identity, so the host (the Mac the agent runs on) is the
// closest thing. Initials come from the host name; the menu shows host + dir.
function initialsFrom(name) {
  const parts = String(name || "").replace(/[^a-zA-Z0-9]+/g, " ").trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return "SQE";
  return (parts.length >= 2 ? parts[0][0] + parts[1][0] : parts[0].slice(0, 2)).toUpperCase();
}
function updateAvatar(s) {
  // Prefer the signed-in user (via the auth provider, when auth is on); otherwise fall back
  // to the host machine the agent runs on. The authed identity is constant, so
  // per-session status updates (whose host changes when switching laptops) must
  // not clobber it.
  const who = (authUser && (authUser.display_name || authUser.accountname)) || (s && s.host) || "";
  const title = (authUser && authUser.accountname) || (s && s.host) || "moony";
  if (userAvatarEl) { userAvatarEl.textContent = initialsFrom(who); userAvatarEl.title = title; }
}

// renderStatusBar mirrors the terminal status bar: a left activity indicator
// (state + active tool) and a right-side stats line (token usage, cost or
// usage quota, and context-window fill). Model/route live in the header.
function renderStatusBar() {
  statusEl.innerHTML = "";
  let icon = null, txt = "ready";
  let working = false; // generic busy (no named tool) → the jumping Claude is the activity indicator
  if (state === "busy") {
    icon = sym("arrow.triangle.2.circlepath", "inline-block animate-spin text-warn");
    if (lastStatus && lastStatus.toolsInFlight > 1) txt = `running ${lastStatus.toolsInFlight} tools…`;
    else if (lastStatus && lastStatus.activeTool) txt = `running ${lastStatus.activeTool}…`;
    else working = true;
  } else if (state === "awaiting_approval") {
    icon = sym("exclamationmark.triangle.fill", "text-warn");
    txt = "awaiting approval";
  } else if (state === "awaiting_question") {
    icon = sym("questionmark.bubble.fill", "text-warn");
    txt = "awaiting your answer";
  } else {
    icon = sym("checkmark.circle", "text-faint"); // idle → a calm "ready / caught up" mark
  }
  if (working) {
    // Generic "working…": the jumping Claude is the activity indicator, replacing
    // the spinning icon + text (so there aren't two competing animations).
    const art = el("img", "status-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "working…"; art.title = "working…";
    statusEl.appendChild(art);
  } else {
    if (icon) statusEl.appendChild(icon);
    statusEl.appendChild(el("span", null, txt));
  }
  if (state === "busy" && curStep > 0) {
    // Step progress: a dotted-circle "in progress" mark instead of a "·" bullet.
    const step = el("span", "inline-flex items-center gap-1 text-faint");
    step.appendChild(sym("circle.dotted"));
    step.appendChild(el("span", null, `step ${curStep}/${maxStep}`));
    statusEl.appendChild(step);
  }
  if (state === "busy" && compactingMsg) {
    // Context compaction in progress — show a brief chip while the summarise
    // LLM call runs so the user knows why there's a quiet moment mid-turn.
    const m = compactingMsg.match(/^Compacting (\d+)/i);
    const label = m ? `compacting ${m[1]} messages…` : "compacting…";
    const chip = el("span", "inline-flex items-center gap-1 text-faint");
    chip.appendChild(sym("arrow.counterclockwise"));
    chip.appendChild(el("span", null, label));
    statusEl.appendChild(chip);
  }
  if (queuedMsgs.length > 0) {
    const q = el("span", "inline-flex items-center gap-1 text-faint");
    q.appendChild(sym("tray.full"));
    q.appendChild(el("span", null, `queued ${queuedMsgs.length}`));
    statusEl.appendChild(q);
  }
  renderStats();
}

function renderStats() {
  statsEl.innerHTML = "";
  const s = lastStatus;
  if (!s) return;

  // Build the parts, then join with faint "·" dividers (like the header meta) so
  // tokens · cost · context read as scannable, distinct fields instead of one run.
  const parts = [];

  // token usage (in / out) — Material Symbols icon arrows + counts, kept tight per pair.
  // Prefer the running sum of the per-turn meta footers (cumTurnIn/Out) so the
  // status bar matches the turn history; fall back to the server's live counters
  // (e.g. before any turn completes this view).
  const tok = el("span", "inline-flex items-center gap-2 text-faint");
  const tokPair = (icon, val) => { const s = el("span", "inline-flex items-center gap-1"); s.appendChild(sym(icon)); s.appendChild(document.createTextNode(fmtTokens(val))); return s; };
  tok.appendChild(tokPair("arrow.up", cumTurnIn || s.tokensIn));
  tok.appendChild(tokPair("arrow.down", cumTurnOut || s.tokensOut));
  parts.push(tok);

  // Spend: the gateway's authoritative quota when we have a reading, otherwise the
  // in-session cost estimate marked with "~". Only providers with a usage endpoint
  // report a quota (see llm.Client.Usage), so on everything else this is the
  // estimate — but it must still show, or the bar looks like it lost a field.
  const quotaLabel = s.quotaKnown
    ? (s.quotaBudget > 0 ? `${fmtUSD(s.quotaSpend)}/${fmtUSD(s.quotaBudget)}`
      : s.quotaBalanceSet ? `${fmtUSD(s.quotaBalance)} left`
      : `${fmtUSD(s.quotaSpend)} 24h`)
    : (s.costUSD > 0 ? `${fmtUSD(s.costUSD)}~` : "");
  if (quotaLabel) {
    const wrap = el("span", "inline-flex items-center gap-1 cursor-pointer stat-quota");
    wrap.setAttribute("role", "button");
    wrap.tabIndex = 0;
    wrap.title = s.quotaKnown
      ? (s.quotaBalanceSet ? "Prepaid credit remaining — click for details" : "Usage quota — click for details")
      : "Estimated session cost — click for details";
    const openQuota = (e) => { e.stopPropagation(); openHdrInfo(wrap, "quota", quotaInfoRows(s)); };
    wrap.onclick = openQuota;
    wrap.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openQuota(e); } };
    // Amber past 90% of the budget, so a near-exhausted quota is visible without
    // opening the popover; the estimate has no budget to compare against.
    const spent = s.quotaKnown && s.quotaBudget > 0 ? (s.quotaSpend / s.quotaBudget) * 100 : 0;
    const lbl = el("span", null, quotaLabel);
    if (spent >= 100) lbl.style.color = themeColor("--c-danger");
    else if (spent >= 90) lbl.style.color = themeColor("--c-warn");
    // Prepaid credit has no percentage to compare against, so warn on the absolute
    // amount left — running out mid-turn is the failure this is meant to pre-empt.
    else if (s.quotaKnown && s.quotaBalanceSet) {
      if (s.quotaBalance <= 0) lbl.style.color = themeColor("--c-danger");
      else if (s.quotaBalance < 1) lbl.style.color = themeColor("--c-warn");
    }
    wrap.appendChild(lbl);
    parts.push(wrap);
  }

  // context-window fill: a small progress bar + percentage (brand → amber → red as it fills)
  if (typeof s.contextPercent === "number" && s.contextPercent >= 0) {
    const pct = Math.max(0, Math.min(100, s.contextPercent));
    const color = pct >= 90 ? themeColor("--c-danger") : pct >= 75 ? themeColor("--c-warn") : themeColor("--c-brand");
    const wrap = el("span", "inline-flex items-center gap-1.5 cursor-pointer stat-context");
    wrap.setAttribute("role", "button");
    wrap.tabIndex = 0;
    const openContext = (e) => { e.stopPropagation(); openHdrInfo(wrap, "context", contextInfoRows(s)); };
    wrap.onclick = openContext;
    wrap.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openContext(e); } };
    wrap.appendChild(el("span", "text-faint", "ctx"));
    const track = el("span");
    track.style.cssText = `display:inline-block;width:46px;height:6px;border-radius:9999px;background:${themeColor("--c-edge")};overflow:hidden;vertical-align:middle`;
    const fill = el("span");
    fill.style.cssText = `display:block;height:100%;width:${pct}%;background:${color};border-radius:9999px`;
    track.appendChild(fill);
    wrap.appendChild(track);
    const pctEl = el("span", null, `${pct}%`); pctEl.style.color = color; // % takes the fill color
    wrap.appendChild(pctEl);
    parts.push(wrap);
  }

  parts.forEach((p, i) => {
    if (i > 0) statsEl.appendChild(el("span", "text-faint opacity-50", "·"));
    statsEl.appendChild(p);
  });
}

// quotaInfoRows builds the hdrMenu detail rows for the status bar's spend pill:
// the gateway quota (name, spend vs. budget, token allowance) when one was
// fetched, otherwise the locally computed cost estimate and why it's an estimate.
function quotaInfoRows(s) {
  const fmt = (n) => (Number(n) || 0).toLocaleString();
  if (!s.quotaKnown) {
    const rows = [{ label: "Session cost", desc: s.model || "current model" }];
    rows.push({ label: `${fmtUSD(s.costUSD)}~`, desc: "estimated from this session's token usage" });
    // Name the provider: "no usage endpoint" is confusing when the same UI shows a real
    // reading on other backends, so say WHICH one can't report.
    //
    // Use llmProvider, NOT provider: the latter is the wire FAMILY (status.go sends
    // ProviderLabel(cfg.Provider)), so an SQE Anthropic session reports itself as
    // "Anthropic" — naming the wrong backend, and claiming it has no usage endpoint
    // when SQE Anthropic is in fact the one provider that does report real quota.
    rows.push({
      label: "No quota reading",
      desc: (s.llmProvider ? providerLabel(s.llmProvider) + " exposes" : "this provider exposes") +
            " no usage endpoint for an inference key",
    });
    return rows;
  }
  const rows = [{ label: "Usage quota", desc: s.quotaName || "current provider" }];
  if (s.quotaBudget > 0) {
    rows.push({ label: `${fmtUSD(s.quotaSpend)} / ${fmtUSD(s.quotaBudget)}`, desc: "spend / budget" });
    rows.push({ label: `${Math.round((s.quotaSpend / s.quotaBudget) * 100)}%`, desc: "of budget used" });
  } else if (s.quotaBalanceSet) {
    // Prepaid providers report credit REMAINING, not spend. Labelling this "spend"
    // would state the exact inverse of the truth.
    rows.push({ label: fmtUSD(s.quotaBalance), desc: "credit remaining" });
    rows.push({ label: `${fmtUSD(s.costUSD)}~`, desc: "this session's estimated cost" });
  } else {
    rows.push({ label: fmtUSD(s.quotaSpend), desc: "spend, last 24h" });
  }
  if (s.quotaTokens > 0 || s.quotaTokensMax > 0) {
    rows.push({
      label: s.quotaTokensMax > 0 ? `${fmt(s.quotaTokens)} / ${fmt(s.quotaTokensMax)}` : fmt(s.quotaTokens),
      desc: "tokens used" + (s.quotaTokensMax > 0 ? " / allowance" : ""),
    });
  }
  if (s.quotaTokenRate > 0) {
    rows.push({ label: `${fmt(s.quotaTokenRate)} tok`, desc: "rate limit" + (s.quotaTokenRatePeriod ? ` per ${s.quotaTokenRatePeriod}` : "") });
  }
  // A fallback means this quota belongs to a provider we were moved to, not the
  // one that was picked — say so, or the numbers look like they're for the wrong backend.
  if (s.quotaFallbackFrom) {
    let desc = providerLabel(s.quotaFallbackFrom) + " hit its budget";
    if (s.quotaFallbackResetAt) {
      const at = new Date(s.quotaFallbackResetAt);
      if (!isNaN(at)) desc += `, reverting ${at.toLocaleString()}`;
    }
    rows.push({ label: "Fallback active", desc });
  }
  return rows;
}

// contextInfoRows builds the hdrMenu detail rows for the context-usage bar:
// tokens used for the last turn vs. the model's context window, plus the model
// name so it's clear which window the percentage is measured against.
function contextInfoRows(s) {
  const fmt = (n) => (Number(n) || 0).toLocaleString();
  const rows = [{ label: "Context window", desc: s.model || "current model" }];
  if (s.contextWindow > 0) {
    rows.push({
      label: `${fmt(s.contextTokens)} / ${fmt(s.contextWindow)}`,
      desc: "last turn input tokens / window",
    });
  }
  rows.push({ label: `${s.contextPercent || 0}%`, desc: "of window used" });
  return rows;
}

// buildTodoRows returns the scrollable list of todo rows, shared by the live
// bottom bar and the per-question history card recorded in the transcript.
function buildTodoRows(todos) {
  const list = el("div", "max-h-32 overflow-y-auto");
  todos.forEach((t) => {
    const active = t.status === "in_progress";
    const row = el("div", "flex items-baseline gap-1.5 py-0.5" + (active ? " todo-active" : ""));
    let icon, cls = "text-ink";
    switch (t.status) {
      case "completed": icon = sym("checkmark.circle.fill", "text-ok"); cls = "line-through text-faint"; break;
      case "in_progress": icon = sym("circle.lefthalf.filled", "text-warn todo-spin"); break;
      case "cancelled": icon = sym("xmark.circle", "text-faint"); cls = "line-through text-faint"; break;
      default: icon = sym("circle", "text-faint");
    }
    row.appendChild(icon);
    row.appendChild(el("span", cls, t.content));
    list.appendChild(row);
  });
  return list;
}

// renderPlan draws the session's plan artifact as a compact bar above the todo
// bar, mirroring the terminal's Plan sidebar section: an icon + "Plan" + a status
// badge (draft / approved / done) + step count + a chevron that expands the
// numbered step list. A null plan (cleared) hides the bar. Expansion is sticky
// across live updates within a session.
// CONTEXT_SEGMENT_COLORS color the /context breakdown's stacked bar + legend in
// the browser, mirroring the TUI's segment palette (blue/green/orange/pink/grey)
// by label so the segments are visually consistent across UIs. Values are theme
// tokens (auto-adapting to light/dark); unknown labels fall back to neutral.
const CONTEXT_SEGMENT_COLORS = {
  "System": "rgb(var(--c-brand))",          // blue
  "Memory files": "rgb(var(--c-ok))",       // green
  "Built-in tools": "rgb(var(--c-warn))",   // orange/amber
  "MCP tools": "#e06ed6",                    // magenta
  "Subagents": "#2bb6a8",                    // teal
  "Skills": "#e0b341",                       // gold
  "Checkpoint": "#bc8cff",                  // pink/purple (no token; fixed)
  "History": "rgb(var(--c-faint))",         // grey
};
const CONTEXT_SEGMENT_FALLBACK = "rgb(var(--c-faint-2))";

// renderContextBreakdown draws the /context output in the browser: a color-coded
// stacked bar showing how much of the model's context window the next turn would
// use, broken down by component, plus a legend with per-segment token estimates
// and percentages. ctx is the wire payload { segments:[{label,tokens}], total,
// window }.
function renderContextBreakdown(ctx) {
  if (!ctx) return;
  const segs = ctx.segments || [];
  const total = ctx.total || 0;
  const window = ctx.window || 0;
  const pct = window > 0 ? Math.min(100, Math.round((total * 100) / window)) : 0;

  endAssistant(); endToolGroup(); endWorkerGroup();
  const card = el("div", "msg msg-info ctx-card text-sm");

  const head = el("div", "ctx-head flex items-baseline gap-1.5");
  head.appendChild(sym("gauge.with.dots.needle.33percent", "text-brand shrink-0"));
  head.appendChild(el("span", "ctx-title", "Context usage"));
  head.appendChild(el("span", "text-faint", `~${fmtTokens(total)} of ${fmtTokens(window)} tokens · ${pct}% of window`));
  card.appendChild(head);

  // Stacked bar: each segment's width is its share of the WINDOW (so unused
  // window shows as empty track). Min 1.5% width so a tiny segment is still
  // visible. The track's own background is the "free" remainder.
  const track = el("div", "ctx-bar");
  segs.forEach((s) => {
    const share = window > 0 ? (s.tokens / window) * 100 : 0;
    const w = share > 0 && share < 1.5 ? 1.5 : share;
    if (w <= 0) return;
    const cell = el("div", "ctx-bar-seg");
    cell.style.width = w + "%";
    cell.style.background = CONTEXT_SEGMENT_COLORS[s.label] || CONTEXT_SEGMENT_FALLBACK;
    cell.title = `${s.label}: ${fmtTokens(s.tokens)} tokens`;
    track.appendChild(cell);
  });
  card.appendChild(track);

  // Legend: a swatch, the label, the token estimate, and % of window per row.
  // Category rows with per-item detail (MCP servers, agents, skills, …) are
  // collapsible — collapsed by default — and expand to an indented sub-list,
  // Claude-Code style.
  const legend = el("div", "ctx-legend");
  segs.forEach((s) => {
    const hasItems = s.items && s.items.length;
    const row = el("div", "ctx-legend-row flex items-center gap-1.5" + (hasItems ? " ctx-legend-row-toggle" : ""));
    if (hasItems) {
      const chev = sym("chevron.right", "ctx-chevron text-faint shrink-0");
      row.appendChild(chev);
    } else {
      row.appendChild(el("span", "ctx-chevron-spacer shrink-0"));
    }
    const sw = el("span", "ctx-swatch shrink-0");
    sw.style.background = CONTEXT_SEGMENT_COLORS[s.label] || CONTEXT_SEGMENT_FALLBACK;
    row.appendChild(sw);
    row.appendChild(el("span", "ctx-legend-label", s.label));
    if (hasItems) row.appendChild(el("span", "ctx-legend-count text-faint", `${s.items.length}`));
    row.appendChild(el("span", "flex-1"));
    row.appendChild(el("span", "ctx-legend-tok text-faint", fmtTokens(s.tokens)));
    const segPct = window > 0 ? Math.round((s.tokens * 100) / window) : 0;
    row.appendChild(el("span", "ctx-legend-pct text-faint", segPct + "%"));
    legend.appendChild(row);
    if (hasItems) {
      const sub = el("div", "ctx-items hidden"); // collapsed by default
      sub.style.borderColor = CONTEXT_SEGMENT_COLORS[s.label] || CONTEXT_SEGMENT_FALLBACK;
      s.items.forEach((it) => {
        const irow = el("div", "ctx-item-row flex items-center gap-1.5");
        irow.appendChild(el("span", "ctx-item-label truncate", it.label));
        irow.appendChild(el("span", "flex-1"));
        irow.appendChild(el("span", "ctx-item-tok text-faint", fmtTokens(it.tokens)));
        sub.appendChild(irow);
      });
      legend.appendChild(sub);
      const chev = $(".ctx-chevron", row);
      row.onclick = () => {
        const open = sub.classList.toggle("hidden") === false;
        if (chev) chev.classList.toggle("rotate-90", open);
      };
    }
  });
  card.appendChild(legend);

  card.appendChild(el("div", "ctx-foot text-faint text-xs", "Estimates (~bytes/4). The status bar's ctx% reflects the last turn's measured input."));

  appendMsg(card);
}

// draftPlanMarkdown renders a draft plan's STEPS as a markdown numbered list
// (title — detail); the "draft / N steps" header lives in the collapsible head
// built by renderDraftPlanInline, so it's not repeated here. Step text is
// flattened to one line so a stray newline can't break the list.
// stripStepNum removes a leading enumerator ("1. ", "2) ") the model sometimes
// bakes into a plan step's title, so our own step numbering isn't doubled —
// otherwise the inline markdown plan renders "N. 1. …" (marked parses the second
// number as a nested ordered list) and the compact bar shows "N. N. …".
function stripStepNum(title) {
  return String(title || "").replace(/^\s*\d{1,2}[.)]\s+/, "");
}

function draftPlanMarkdown(plan) {
  const flat = (s) => String(s || "").replace(/\s*\n+\s*/g, " ").trim();
  const lines = [];
  plan.steps.forEach((s, i) => {
    let line = (i + 1) + ". " + stripStepNum(flat(s.title));
    if (s.detail) line += " — " + flat(s.detail);
    lines.push(line);
  });
  return lines.join("\n");
}

// renderDraftPlanInline shows a DRAFT plan inline in the transcript as a
// COLLAPSIBLE assistant block: a one-line clickable summary (chevron + title +
// "draft" + step count), collapsed by default, over the full steps (md
// break-words). It's created on the first draft and rebuilt in place as the draft
// is refined or the user toggles it, and removed once the plan is approved or
// cleared (then the compact #plan bar takes over — see renderPlan). The expand
// state is sticky across refinements. isConnected-guarded so a transcript reset
// (session switch / replay) transparently re-creates it.
function renderDraftPlanInline(plan) {
  if (!draftPlanEl || !draftPlanEl.isConnected) {
    endAssistant(); // close any open streaming prose so the plan sits cleanly below it
    draftPlanEl = startMsg("moony", "text-brand", "cpu", "msg-assistant");
    appendMsg(draftPlanEl);
  }
  // Rebuild the block, keeping the startMsg speaker label (first child) intact.
  while (draftPlanEl.childNodes.length > 1) draftPlanEl.removeChild(draftPlanEl.lastChild);

  const head = el("div", "draft-plan-head cursor-pointer select-none flex items-center gap-1.5 mt-2");
  head.appendChild(sym(draftPlanExpanded ? "chevron.down" : "chevron.right", "shrink-0 text-faint"));
  head.appendChild(el("span", "font-semibold", plan.title ? "Plan — " + plan.title : "Plan"));
  head.appendChild(el("span", "plan-status shrink-0", "draft"));
  head.appendChild(el("span", "todos-count shrink-0", plan.steps.length + " step" + (plan.steps.length === 1 ? "" : "s")));
  head.title = (draftPlanExpanded ? "Collapse" : "Expand") + " the draft plan";
  head.onclick = () => { draftPlanExpanded = !draftPlanExpanded; renderDraftPlanInline(plan); };

  const body = el("div", "md break-words mt-2" + (draftPlanExpanded ? "" : " hidden"));
  body.innerHTML = renderMD(draftPlanMarkdown(plan));
  markDiagramPres(body); // in case a step detail carries a diagram

  draftPlanEl.appendChild(head);
  draftPlanEl.appendChild(body);
  scrollBottom();
}

// removeDraftPlanInline drops the inline draft-plan block (a no-op when none) and
// resets the expand state so the next draft starts collapsed.
function removeDraftPlanInline() {
  if (draftPlanEl) { draftPlanEl.remove(); draftPlanEl = null; }
  draftPlanExpanded = false;
}

function renderPlan(plan) {
  // Hide for an empty or DONE plan — a done plan is retired server-side too (see
  // planContext, which drops it from the model's context once complete), so don't
  // pin its "done" badge across every later, unrelated answer — and for a draft,
  // which renders inline below (renderDraftPlanInline) until it's approved.
  if (!plan || !plan.steps || !plan.steps.length || plan.status === "done") {
    planEl.classList.add("hidden"); planEl.innerHTML = ""; planExpanded = false;
    removeDraftPlanInline();
    return;
  }
  const status = plan.status || "draft";
  // A DRAFT awaiting approval renders in FULL inline in the transcript (every
  // step, as markdown) so the user can review it before approving; the compact
  // #plan bar stays hidden until the plan is approved, at which point it takes
  // over and the inline block is dropped.
  if (status === "draft") {
    planEl.classList.add("hidden"); planEl.innerHTML = ""; planExpanded = false;
    renderDraftPlanInline(plan);
    return;
  }
  removeDraftPlanInline();
  const icon = status === "done" ? "checkmark.seal.fill"
    : status === "approved" ? "checkmark.circle.fill"
    : "list.bullet.clipboard";
  const tone = status === "done" || status === "approved" ? "text-ok" : "text-faint";

  planEl.innerHTML = "";
  const header = el("div", "todos-head cursor-pointer select-none");
  header.appendChild(sym(icon, tone + " shrink-0"));
  header.appendChild(el("span", "todos-title shrink-0" + (tone === "text-ok" ? " text-ok" : ""), "Plan"));
  header.appendChild(el("span", "plan-status shrink-0", status));
  header.appendChild(el("span", "todos-count shrink-0", `${plan.steps.length} step${plan.steps.length === 1 ? "" : "s"}`));
  if (plan.title) header.appendChild(el("span", "plan-headline truncate text-faint", plan.title));
  header.appendChild(el("span", "flex-1"));
  header.appendChild(sym(planExpanded ? "chevron.down" : "chevron.right", "text-faint shrink-0"));
  header.title = "Click to " + (planExpanded ? "collapse" : "expand") + " the plan";
  header.onclick = () => { planExpanded = !planExpanded; renderPlan(plan); };
  planEl.appendChild(header);

  if (planExpanded) {
    const list = el("div", "plan-steps mt-2");
    plan.steps.forEach((s, i) => {
      const row = el("div", "plan-step");
      row.appendChild(el("span", "plan-step-num text-faint shrink-0", `${i + 1}.`));
      const body = el("div", "plan-step-body");
      body.appendChild(el("div", "plan-step-title", stripStepNum(s.title)));
      if (s.detail) body.appendChild(el("div", "plan-step-detail text-faint", s.detail));
      row.appendChild(body);
      list.appendChild(row);
    });
    planEl.appendChild(list);
  }
  planEl.classList.remove("hidden");
}

// renderTodos draws a COMPACT progress bar above the status bar: a sliver +
// "Tasks N/M" + a chevron. The full per-task list is the canonical inline phase
// timeline in the transcript (renderTurnTodos), so this bar stays collapsed by
// default to avoid duplicating it; click to expand the list here too. Expansion
// persists through live updates within a turn and resets on the next question.
function renderTodos(todos) {
  // Plan-mode gate: while a drafted plan is still awaiting the user's confirmation
  // to execute (status "draft"), keep the Tasks bar hidden — these todos are the
  // plan's execution and shouldn't surface until the user approves it. Once the
  // plan is approved/done, or when there's no plan at all, the bar shows normally.
  if (currentPlan && currentPlan.status === "draft") {
    todosEl.classList.add("hidden"); todosEl.innerHTML = ""; todosExpanded = false; todosSig = ""; return;
  }
  if (!todos.length) { todosEl.classList.add("hidden"); todosEl.innerHTML = ""; todosExpanded = false; todosSig = ""; return; }
  const done = todos.filter((t) => t.status === "completed" || t.status === "cancelled").length;
  const allDone = done === todos.length;
  const pct = todos.length ? Math.round((done / todos.length) * 100) : 0;

  // Skip the rebuild when nothing visible changed: recreating the DOM would restart
  // the in-progress task's spinner animation (plan events re-call this with unchanged
  // todos), making the loader look frozen. Signature covers status/content + expansion.
  const sig = todos.map((t) => t.status + ":" + t.content).join("\u0001") + "|" + (todosExpanded ? "1" : "0");
  if (sig === todosSig) return;
  todosSig = sig;

  todosEl.innerHTML = "";

  const header = el("div", "todos-head cursor-pointer select-none");
  header.appendChild(sym(allDone ? "checkmark.circle.fill" : "checklist", (allDone ? "text-ok" : "text-faint") + " shrink-0"));
  header.appendChild(el("span", "todos-title shrink-0" + (allDone ? " text-ok" : ""), allDone ? "Tasks done" : "Tasks"));
  header.appendChild(el("span", "todos-count shrink-0", `${done}/${todos.length}`));
  const track = el("div", "todos-track");
  const fill = el("div", "todos-fill" + (allDone ? " is-done" : ""));
  fill.style.width = pct + "%";
  track.appendChild(fill);
  header.appendChild(track);
  header.appendChild(sym(todosExpanded ? "chevron.down" : "chevron.right", "text-faint shrink-0"));
  header.title = "Click to " + (todosExpanded ? "collapse" : "expand") + " the task list";
  header.onclick = () => { todosExpanded = !todosExpanded; renderTodos(todos); };
  todosEl.appendChild(header);

  if (todosExpanded) { const rows = buildTodoRows(todos); rows.classList.add("mt-2"); todosEl.appendChild(rows); }
  todosEl.classList.remove("hidden");
}

// renderTurnTodos records the current question's todos as a persistent phase
// timeline in the transcript: a vertical rail with status nodes (✓ done /
// spinning in-progress / ○ pending) + names, created on the turn's first todos
// event and updated live, then frozen once the next question nulls turnTodos.
// Per-phase timing is timed client-side (live turns only; see phaseTimes) and
// shown only when ≥300ms so the replay event-burst doesn't print bogus times.
function renderTurnTodos(todos) {
  if (!todos.length) return;
  // Skip no-op rebuilds: recreating the rows would restart the in-progress task's
  // spinner (a linear rotate that visibly resets to 0°). turnTodos===null means a new
  // turn, so the guard is bypassed then and the card is rebuilt fresh.
  const sig = todos.map((t) => t.status + ":" + t.content).join("\u0001");
  if (turnTodos && sig === turnTodosSig) return;
  turnTodosSig = sig;
  if (!turnTodos) {
    endToolGroup(); // the timeline is its own block → close any open tool-call run
    endWorkerGroup();
    const m = el("div", "msg phase-block");
    turnTodos = el("div", "phase-timeline");
    m.appendChild(turnTodos);
    appendMsg(m);
    scrollBottom();
  }
  const now = Date.now();
  turnTodos.innerHTML = "";
  todos.forEach((t, i) => {
    const done = t.status === "completed", active = t.status === "in_progress", cancelled = t.status === "cancelled";
    const pt = phaseTimes[i] || (phaseTimes[i] = {});
    if (active && !pt.start) pt.start = now;
    if ((done || cancelled) && pt.start && !pt.dur) pt.dur = now - pt.start;

    const row = el("div", "phase" + (done ? " is-done" : active ? " is-active" : cancelled ? " is-cancelled" : ""));
    const marker = el("span", "phase-marker");
    if (done) marker.appendChild(sym("checkmark.circle.fill", "text-ok"));
    else if (active) marker.appendChild(sym("circle.lefthalf.filled", "text-warn todo-spin"));
    else if (cancelled) marker.appendChild(sym("xmark.circle", "text-faint"));
    else marker.appendChild(sym("circle", "text-faint"));
    row.appendChild(marker);
    row.appendChild(el("span", "phase-name", t.content));
    if (pt.dur >= 300) row.appendChild(el("span", "phase-time", fmtDur(pt.dur)));
    turnTodos.appendChild(row);
  });
}

// renderLeftPanel draws the sidebar's Files + MCP sections from the status
// snapshot (mirrors the terminal's left sidebar).
function renderLeftPanel(s) {
  const files = (s && s.files) || [];
  if (!files.length) { filesEl.classList.add("hidden"); filesEl.innerHTML = ""; }
  else {
    filesEl.innerHTML = "";
    filesEl.appendChild(el("div", "text-faint font-medium mb-1.5", `Files (${files.length})`));
    files.forEach((f) => {
      const row = el("div", "flex items-baseline gap-1.5 py-0.5 font-mono");
      row.appendChild(sym(f.created ? "plus.circle" : "pencil.circle", f.created ? "text-ok" : "text-warn"));
      row.appendChild(el("span", "truncate " + (f.created ? "text-ok" : "text-warn"), f.path));
      filesEl.appendChild(row);
    });
    filesEl.classList.remove("hidden");
  }
  // #mcp no longer mirrors MCP servers here — it shows per-session usage history
  // (skills / subagents / workers / MCP tools), driven by trackUsage/renderUsage
  // off the tool-call stream. (MCP servers still live in the Manage panel.)
}

// USAGE_CATS: the four tracked categories, each with how a tool_start maps to it
// and a header icon/tint. mcp.<server>.<tool> tools are detected by name prefix;
// the others by tool name with the specific item read from the call's JSON input.
const USAGE_CATS = [
  { key: "skill",   label: "Skills",    icon: "wand.and.stars",             color: "text-brand" },
  { key: "agent",   label: "Subagents", icon: "person.2.fill",              color: "text-ok" },
  { key: "worker",  label: "Workers",   icon: "hammer.fill",                color: "text-warn" },
  { key: "mcp",     label: "MCP tools", icon: "puzzlepiece.extension",  color: "text-brand" },
  { key: "builtin", label: "Built in",  icon: "wrench.and.screwdriver.fill", color: "text-faint" },
];

// jsonField safely pulls a string field out of a tool call's JSON input.
function jsonField(jsonStr, key) {
  try { const o = JSON.parse(jsonStr || "{}"); return o && o[key] != null ? String(o[key]) : null; }
  catch (_) { return null; }
}

// trackUsage categorizes a tool_start into skill / subagent / worker / MCP tool
// and bumps its per-session count, then re-renders the panel. Everything else is
// a built-in tool (read/edit/bash/grep/…), counted under "Built in". Runs during
// live turns and session replay, so the panel reflects the whole session.
function trackUsage(ev) {
  const tool = ev.tool || "";
  let cat = null, name = null;
  if (tool === "skill") { cat = "skill"; name = jsonField(ev.input, "name"); }
  else if (tool === "task") { cat = "agent"; name = jsonField(ev.input, "agent"); }
  else if (tool === "delegate") { cat = "worker"; name = jsonField(ev.input, "worker"); }
  else if (tool.indexOf("mcp.") === 0) { cat = "mcp"; name = tool.slice(4); } // "server.tool"
  else if (tool) { cat = "builtin"; name = tool; } // read_file/edit_file/bash/grep/…
  if (!cat) return;
  name = name || tool;
  usage[cat][name] = (usage[cat][name] || 0) + 1;
  renderUsage();
}

// renderUsage draws the usage history into #mcp: a section per non-empty
// category (most-used first) with each item and its call count.
function renderUsage() {
  const cats = USAGE_CATS.filter((c) => Object.keys(usage[c.key]).length);
  if (!cats.length) { mcpEl.classList.add("hidden"); mcpEl.innerHTML = ""; return; }
  mcpEl.innerHTML = "";
  // Header: title + a session-total call count (sum of every tracked use).
  const total = USAGE_CATS.reduce((n, c) => n + Object.values(usage[c.key]).reduce((a, b) => a + b, 0), 0);
  const hdr = el("div", "flex items-baseline gap-1.5 mb-1.5 text-faint font-medium");
  hdr.appendChild(el("span", null, "Usage this session"));
  hdr.appendChild(el("span", "ml-auto shrink-0 font-mono", total + " call" + (total === 1 ? "" : "s")));
  mcpEl.appendChild(hdr);
  for (const c of cats) {
    const items = Object.entries(usage[c.key]).sort((a, b) => b[1] - a[1]);
    const head = el("div", "flex items-center gap-1.5 mt-1.5 mb-0.5");
    head.appendChild(sym(c.icon, c.color));
    head.appendChild(el("span", "font-semibold " + c.color, c.label));
    head.appendChild(el("span", "text-faint ml-auto shrink-0", String(items.length)));
    mcpEl.appendChild(head);
    items.forEach(([name, count]) => {
      const row = el("div", "flex items-baseline gap-1.5 py-0.5 pl-3.5");
      row.appendChild(el("span", "truncate", name));
      row.appendChild(el("span", "text-faint ml-auto shrink-0 font-mono", "×" + count));
      mcpEl.appendChild(row);
    });
  }
  mcpEl.classList.remove("hidden");
}

// renderBoardMsgs draws the shared agent message-board conversation into
// #agentmsgs: one row per post (sender → addressee, then the text), so the
// multi-agent back-and-forth is visible live and restored after reopen. Mirrors
// the other sidebar sections; boardMsgs accumulates from "board_message" events
// (live + replay).
function renderBoardMsgs() {
  if (!boardMsgs.length) { agentMsgsEl.classList.add("hidden"); agentMsgsEl.innerHTML = ""; return; }
  agentMsgsEl.innerHTML = "";
  const hdr = el("div", "flex items-baseline gap-1.5 mb-1.5 text-faint font-medium");
  hdr.appendChild(sym("bubble.left.and.bubble.right", "text-brand"));
  hdr.appendChild(el("span", null, "Agent messages"));
  hdr.appendChild(el("span", "ml-auto shrink-0 font-mono", String(boardMsgs.length)));
  agentMsgsEl.appendChild(hdr);
  boardMsgs.forEach((m) => {
    const row = el("div", "py-0.5");
    const head = el("div", "flex items-baseline gap-1");
    head.appendChild(el("span", "font-semibold text-ok truncate", m.from || "?"));
    head.appendChild(sym("arrow.right", "text-faint shrink-0"));
    head.appendChild(el("span", "truncate " + (m.to ? "text-warn" : "text-faint"), m.to || "all"));
    row.appendChild(head);
    row.appendChild(el("div", "pl-2 break-words text-ink", m.text || ""));
    agentMsgsEl.appendChild(row);
  });
  agentMsgsEl.classList.remove("hidden");
}

async function sendText(text) {
  text = (text || "").trim();
  if (!text || !current) return;
  await fetch(api(`/api/sessions/${current}/message`), { method: "POST", body: JSON.stringify({ text }) });
}

// send posts the composer's contents. `override` (optional) carries a quick
// action's one-turn model/provider/reasoning pin, which the server applies for
// that turn only and then reverts.
// pendingOverride holds a model pin restaged by Esc-pop, consumed by the next
// send() so the re-sent message keeps the quick action's model.
let pendingOverride = null;

async function send(override) {
  const text = input.value.trim();
  if ((!text && pendingAttachments.length === 0) || !current) return;
  // An explicit override (a quick action fired directly) wins; otherwise adopt a
  // pin restaged by Esc-pop. Either way the restaged pin is spent by this send.
  if (!override && pendingOverride) override = pendingOverride;
  pendingOverride = null;
  // Only include the pin fields when actually set, so a normal send posts exactly
  // the payload it always did.
  const ov = override || {};
  const pin = {};
  if (ov.model) pin.model = ov.model;
  if (ov.provider) pin.provider = ov.provider;
  if (ov.reasoning) pin.reasoning = ov.reasoning;
  // While busy on a queue-capable session, Enter enqueues the message for after
  // the current turn — WITH any staged attachments, which now ride along in the
  // server-side queue instead of being dropped.
  if (state === "busy" && queueCapable) {
    const attachments = pendingAttachments.map((a) => ({ name: a.name, mediaType: a.mediaType, data: a.data }));
    input.value = "";
    pendingAttachments = [];
    renderAttachStrip();
    hidePalette();
    hideMentions();
    await fetch(api(`/api/sessions/${current}/message`), { method: "POST", body: JSON.stringify({ text, attachments, ...pin }) });
    return;
  }
  const attachments = pendingAttachments.map((a) => ({ name: a.name, mediaType: a.mediaType, data: a.data }));
  // Clear the composer and go responsive IMMEDIATELY.
  input.value = "";
  pendingAttachments = [];
  renderAttachStrip();
  hidePalette();
  hideMentions();
  await fetch(api(`/api/sessions/${current}/message`), {
    method: "POST",
    body: JSON.stringify({ text, attachments, ...pin }),
  });
}

// --- quick actions ----------------------------------------------------------
// A split button that fires a preset prompt at the agent so the user doesn't
// retype it. The presets are user-editable (Manage → Quick actions) and come
// from /api/quickactions, which merges a committable per-project
// .sqe/quickactions.json with a global fallback. The chosen action becomes the
// primary half's default and is remembered across reloads. Sending routes
// through send() rather than sendText() so the preset inherits everything a
// typed message gets: the busy/queue path, composer clearing, attachments.
let quickActionList = [];
let quickAction = null;

function savedQuickActionPrompt() {
  try { return localStorage.getItem("moony.quickAction") || ""; } catch { return ""; }
}

// loadQuickActions refreshes the list from the server and re-renders the menu.
// Called at session select and after the Manage panel edits them.
async function loadQuickActions() {
  try {
    const r = await fetch(api("/api/quickactions"));
    if (!r.ok) throw new Error("http " + r.status);
    const d = await r.json();
    quickActionList = Array.isArray(d.actions) ? d.actions : [];
  } catch {
    quickActionList = []; // offline / not yet connected — menu just stays empty
  }
  renderQuickActionMenu();
}

// renderQuickActionMenu rebuilds the dropdown rows and picks the active action:
// the user's remembered choice when it still exists, else the first (so the
// order set in the Manage panel decides the default).
function renderQuickActionMenu() {
  if (!quickActionMenu) return;
  quickActionMenu.textContent = "";
  const saved = savedQuickActionPrompt();
  quickAction = quickActionList.find((a) => a.prompt === saved) || quickActionList[0] || null;

  for (const a of quickActionList) {
    const item = el("button", "quick-actions-item");
    item.type = "button";
    item.setAttribute("role", "menuitem");
    item.dataset.prompt = a.prompt;
    if (a.icon) item.append(sym(a.icon));
    const text = el("span");
    text.append(el("span", "quick-actions-item-title", a.label || a.prompt));
    if (a.sub) text.append(el("span", "quick-actions-item-sub", a.sub));
    item.append(text);
    item.title = a.prompt; // the full prompt, since the label can be a summary
    item.onclick = (e) => {
      e.stopPropagation();
      closeQuickActionMenu();
      quickAction = a; // picking an action also makes it the new default
      renderQuickActionLabel();
      try { localStorage.setItem("moony.quickAction", a.prompt); } catch { /* ignore */ }
      runQuickAction(a);
    };
    quickActionMenu.append(item);
  }
  if (!quickActionList.length) {
    quickActionMenu.append(el("div", "quick-actions-empty",
      "No quick actions yet — add them in Manage → Quick actions."));
  }
  renderQuickActionLabel();
  // The primary half is meaningless with no presets; keep it disabled unless a
  // busy composer already disabled it (setState owns the busy half of this).
  if (quickActionBtn && !quickAction) quickActionBtn.disabled = true;
  else if (quickActionBtn && state === "idle") quickActionBtn.disabled = false;
  if (quickActions) quickActions.hidden = !quickActionList.length;
  syncFloatingPills(); // the pill may have just appeared/hidden, so re-measure the strip
}

// The pill is icon-only, so the selected action's name is carried entirely by the
// tooltip and the accessible name — there is no visible label to update.
function renderQuickActionLabel() {
  if (!quickActionBtn) return;
  const desc = quickAction ? `Run "${quickAction.prompt}"` : "No quick actions configured";
  quickActionBtn.title = desc;
  quickActionBtn.setAttribute("aria-label", desc);
}

function closeQuickActionMenu() {
  if (!quickActionMenu) return;
  quickActionMenu.classList.remove("open");
  if (quickActionCaret) {
    quickActionCaret.classList.remove("menu-open"); // un-rotate the caret
    quickActionCaret.setAttribute("aria-expanded", "false");
  }
}

// syncFloatingPills lays out the bottom-right pill strip. All of them are absolutely
// positioned against the SAME right edge, so each is shifted left past the ones to its
// right via --pill-shift. Order right to left: turn pager, quick actions, jobs.
//
// Widths are measured, not hardcoded: each pill appears, hides and changes width on its
// own (the pager gains/loses its scroll-to-bottom half, quick actions vanish with no
// presets), and a stale offset would overlap or leave a gap. A hidden pill measures 0
// and contributes nothing, which is what collapses the strip correctly.
//
// The pager consumes no shift — it's rightmost, so its offset is always 0, and it is
// left transform-free deliberately: a transform would become the containing block for
// its "n/total" dropdown and move it off the button.
const PILL_GAP = 6;
function syncFloatingPills() {
  let shift = 0;
  for (const pill of [pager, quickActions, jobsPill]) {
    if (!pill) continue;
    if (pill.hidden) { pill.style.setProperty("--pill-shift", "0px"); continue; }
    pill.style.setProperty("--pill-shift", shift ? shift + "px" : "0px");
    shift += Math.round(pill.getBoundingClientRect().width) + PILL_GAP;
  }
}

// runQuickAction fires an action. Accepts the action object so its configured
// model/provider/reasoning ride along; a bare prompt string still works.
function runQuickAction(action) {
  const a = typeof action === "string" ? { prompt: action } : (action || {});
  if (!current) { flash("Open a session first to run a quick action."); return; }
  if (!a.prompt) return;
  input.value = a.prompt;
  send({ model: a.model, provider: a.provider, reasoning: a.reasoning });
}

if (quickActions && quickActionBtn && quickActionCaret && quickActionMenu) {
  renderQuickActionMenu();
  quickActionBtn.onclick = (e) => {
    e.stopPropagation();
    closeQuickActionMenu();
    if (quickAction) runQuickAction(quickAction);
  };
  quickActionCaret.onclick = (e) => {
    e.stopPropagation();
    closeAttachMenu(); // only one composer menu open at a time
    const open = quickActionMenu.classList.toggle("open");
    quickActionCaret.classList.toggle("menu-open", open);
    quickActionCaret.setAttribute("aria-expanded", open ? "true" : "false");
  };
  document.addEventListener("click", closeQuickActionMenu); // outside click dismisses
}

// --- attachments (images, PDFs, Office files) -------------------------------

// classifyAttachment maps a File to our kind, or null if unsupported.
function classifyAttachment(file) {
  const type = (file.type || "").toLowerCase();
  const name = (file.name || "").toLowerCase();
  // SVG is XML text (Claude's vision API can't ingest it as an image), so send
  // its markup as text — checked before the image branch since the browser
  // reports it as image/svg+xml.
  if (type === "image/svg+xml" || name.endsWith(".svg")) {
    return { kind: "file", max: 5 * 1024 * 1024, maxLabel: "5 MB" };
  }
  if (type.startsWith("image/") || /\.(png|jpe?g|gif|webp)$/.test(name)) {
    return { kind: "image", max: 5 * 1024 * 1024, maxLabel: "5 MB" };
  }
  if (type === "application/pdf" || name.endsWith(".pdf")) {
    return { kind: "document", max: 32 * 1024 * 1024, maxLabel: "32 MB" };
  }
  // Office (docx/xlsx/pptx) + iWork (pages/numbers/key) bundles. The browser
  // often can't infer a media type for iWork files, so classify by extension.
  if (/\.(docx|xlsx|pptx|pages|numbers|key)$/.test(name)) {
    return { kind: "file", max: 30 * 1024 * 1024, maxLabel: "30 MB" };
  }
  return null;
}

// addAttachmentFile reads a File, validates it, and stages it for the next send.
function addAttachmentFile(file) {
  const cls = classifyAttachment(file);
  if (!cls) {
    flash(`Can't attach ${file.name}: only images, PDFs, Office (docx/xlsx/pptx), and iWork (pages/numbers/key) files are supported.`);
    return;
  }
  if (file.size > cls.max) {
    flash(`${file.name} is too large (max ${cls.maxLabel}).`);
    return;
  }
  const reader = new FileReader();
  reader.onload = () => {
    const dataUri = String(reader.result || "");
    const comma = dataUri.indexOf(",");
    const data = comma >= 0 ? dataUri.slice(comma + 1) : dataUri; // strip "data:...;base64,"
    pendingAttachments.push({
      name: file.name || (cls.kind === "image" ? "image.png" : "file"),
      mediaType: file.type || "",
      data,
      kind: cls.kind,
      dataUri: cls.kind === "image" ? dataUri : "",
    });
    renderAttachStrip();
  };
  reader.onerror = () => flash(`Could not read ${file.name}.`);
  reader.readAsDataURL(file);
}

// renderAttachStrip draws the staged-attachment previews above the input: a row
// of icon/thumbnail tiles, each with a corner close button. No filename label —
// the name is the tile's hover tooltip.
function renderAttachStrip() {
  attachStrip.innerHTML = "";
  if (pendingAttachments.length === 0) {
    attachStrip.classList.add("hidden");
    return;
  }
  attachStrip.classList.remove("hidden");
  pendingAttachments.forEach((a, i) => {
    const tile = el("div", "attach-tile");
    tile.title = a.name; // filename on hover (no inline label)
    if (a.kind === "image" && a.dataUri) {
      const img = el("img");
      img.src = a.dataUri;
      img.alt = a.name;
      tile.appendChild(img);
    } else {
      tile.appendChild(sym(a.kind === "document" ? "doc.richtext" : "doc", "attach-doc-icon"));
    }
    const x = el("button", "attach-remove");
    x.type = "button";
    x.setAttribute("aria-label", "Remove " + a.name);
    x.appendChild(sym("xmark"));
    x.onclick = () => { pendingAttachments.splice(i, 1); renderAttachStrip(); };
    tile.appendChild(x);
    attachStrip.appendChild(tile);
  });
}

// renderQueueStrip shows the pending busy-time message queue above the composer:
// a count header plus one row per item with a × to remove it. The server owns
// the queue; this just reflects the latest "queue" event.
function renderQueueStrip() {
  if (!queueStrip) return;
  queueStrip.innerHTML = "";
  if (!queuedMsgs.length) { queueStrip.classList.add("hidden"); return; }
  queueStrip.classList.remove("hidden");
  const head = el("div", "queue-head text-faint text-xs flex items-center gap-1.5");
  head.appendChild(sym("clock"));
  head.appendChild(el("span", "", `queued (${queuedMsgs.length}) — runs when the turn finishes · Esc edits the last · × removes`));
  queueStrip.appendChild(head);
  queuedMsgs.forEach((q, i) => {
    const row = el("div", "queue-row text-faint text-xs flex items-baseline gap-1.5");
    row.appendChild(el("span", "queue-idx shrink-0", `${i + 1}.`));
    row.appendChild(el("span", "queue-text break-words flex-1", q.replace(/\n/g, " ⏎ ")));
    const x = el("button", "queue-remove shrink-0");
    x.type = "button";
    x.title = "Remove";
    x.setAttribute("aria-label", "Remove queued message");
    x.appendChild(sym("xmark"));
    x.onclick = () => removeQueuedMsg(i);
    row.appendChild(x);
    queueStrip.appendChild(row);
  });
}

// popQueued asks the server to pop the most recent queued message back into the
// composer for editing/discard. Any in-progress draft is stashed onto the queue
// front so it isn't lost (mirroring the terminal's Esc-pop).
async function popQueued() {
  if (!current) return;
  const stash = input.value;
  try {
    const r = await fetch(api(`/api/sessions/${current}/queue`), { method: "POST", body: JSON.stringify({ action: "pop", stash }) });
    const j = await r.json();
    if (j && j.ok) {
      input.value = j.text || "";
      // Restage the popped message's per-turn model pin so editing and re-sending a
      // queued quick action still runs where it was configured to; the next send()
      // consumes it (see pendingOverride).
      pendingOverride = (j.model || j.provider || j.reasoning)
        ? { model: j.model, provider: j.provider, reasoning: j.reasoning }
        : null;
      input.focus();
      input.dispatchEvent(new Event("input"));
    }
  } catch (_) { /* the queue event will keep the strip in sync regardless */ }
}

// removeQueuedMsg drops the queued message at index i (the × button).
async function removeQueuedMsg(i) {
  if (!current) return;
  try {
    await fetch(api(`/api/sessions/${current}/queue`), { method: "POST", body: JSON.stringify({ action: "remove", index: i }) });
  } catch (_) { /* the queue event refreshes the strip */ }
}

// renderAttachments builds the read-only attachment row shown on a sent user
// message (image thumbnails inline, other kinds as labeled chips).
function renderAttachments(atts) {
  const row = el("div", "msg-attachments flex flex-wrap gap-2 mt-1.5");
  atts.forEach((a) => {
    if (a.kind === "image" && a.dataUri) {
      const img = el("img", "attach-thumb-lg");
      img.src = a.dataUri;
      img.alt = a.name || "image";
      row.appendChild(img);
    } else {
      const chip = el("div", "attach-chip flex items-center gap-1.5 bg-surface border border-edge rounded-lg px-2 py-1 text-xs");
      chip.appendChild(sym(a.kind === "document" ? "doc.richtext" : "doc", "text-faint"));
      chip.appendChild(el("span", "text-ink truncate max-w-[200px]", a.name || a.kind));
      row.appendChild(chip);
    }
  });
  return row;
}

// appendToolImages renders images a tool returned (e.g. a browser screenshot)
// into the tool card body, below any text output. Each is a data URI.
function appendToolImages(ref, images) {
  const row = el("div", "tool-images flex flex-wrap gap-2 mt-1.5");
  images.forEach((src) => {
    const a = el("a", "tool-image-link");
    a.href = src;
    a.target = "_blank";
    a.rel = "noopener";
    const img = el("img", "tool-image");
    img.src = src;
    img.alt = "screenshot";
    img.loading = "lazy";
    a.appendChild(img);
    row.appendChild(a);
  });
  ref.body.appendChild(row);
}

// appendToolVideos renders videos a tool returned (e.g. generate_video) into the
// tool card body, below any text output. Each src is a data URI or a plain URL.
function appendToolVideos(ref, videos) {
  const row = el("div", "tool-videos flex flex-wrap gap-2 mt-1.5");
  videos.forEach((src) => {
    const v = el("video", "tool-video");
    v.src = src;
    v.controls = true;
    v.preload = "metadata";
    v.playsInline = true;
    row.appendChild(v);
  });
  ref.body.appendChild(row);
}

// flash shows a brief transient notice (reused for attachment errors).
function flash(msg) {
  const note = el("div", "msg msg-info text-warn text-xs");
  note.textContent = msg;
  appendMsg(note);
  const h = answerHost || transcript; h.scrollTop = h.scrollHeight;
}

async function approve(d) {
  if (!current) return;
  await fetch(api(`/api/sessions/${current}/approve`), { method: "POST", body: JSON.stringify({ decision: d }) });
}

// renderQuestion fills the ask_user panel from a {question, multiple, options[]}
// payload. Single-choice options answer immediately on click; multiple-choice
// options toggle, and a Submit button sends the chosen set. A Skip button
// dismisses without choosing. A null/empty payload hides the panel.
function renderQuestion(q) {
  if (!q) { questionEl.classList.add("hidden"); return; }
  // Grouped ask (the questions[] form): render switchable tabs, one per question.
  if (q.questions && q.questions.length) { renderQuestionTabs(q.questions); return; }
  if ((!q.options || !q.options.length) && !q.allowText) { questionEl.classList.add("hidden"); return; }
  $(".q", questionEl).textContent = q.question || "";
  const optsEl = $(".app-question-options", questionEl);
  const actEl = $(".app-question-actions", questionEl);
  optsEl.innerHTML = "";
  actEl.innerHTML = "";
  const chosen = new Set();
  let textInput = null;
  let submitBtn = null;
  // Submit is enabled only when there's something to send (a checked option or
  // typed text); an empty Submit does nothing (Skip dismisses), so the button
  // never looks dead-clickable and can't fire an accidental empty answer.
  const syncSubmit = () => { if (submitBtn) submitBtn.disabled = chosen.size === 0 && !(textInput && textInput.value.trim()); };

  (q.options || []).forEach((o, i) => {
    const btn = el("button", "app-question-option");
    const mark = el("span", "app-question-mark");
    mark.appendChild(sym(q.multiple ? "square" : "circle"));
    btn.appendChild(mark);
    const body = el("div", "app-question-option-body");
    body.appendChild(el("div", "app-question-option-label", o.label || `Option ${i + 1}`));
    if (o.description) body.appendChild(el("div", "app-question-option-desc", o.description));
    btn.appendChild(body);
    btn.onclick = () => {
      if (q.multiple) {
        const on = !chosen.has(o.label);
        if (on) chosen.add(o.label); else chosen.delete(o.label);
        btn.classList.toggle("is-on", on);
        mark.innerHTML = ""; mark.appendChild(sym(on ? "checkmark.square.fill" : "square"));
        syncSubmit();
      } else {
        answer([o.label]); // single choice answers immediately
      }
    };
    optsEl.appendChild(btn);
  });

  // Free-text answer: a text box whose typed value is sent as the answer.
  // Enter (without Shift) submits; blank input is ignored so it doesn't count
  // as a dismiss.
  if (q.allowText) {
    textInput = el("textarea", "app-question-text");
    textInput.rows = 2;
    textInput.placeholder = (q.options && q.options.length) ? "Or type your own answer…" : "Type your answer…";
    textInput.oninput = syncSubmit; // keep Submit's enabled state current while typing
    textInput.onkeydown = (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        const v = textInput.value.trim();
        const ans = q.multiple ? [...chosen] : []; // additive: keep checked options
        if (v) ans.push(v);
        if (ans.length) answer(ans);
      }
    };
    optsEl.appendChild(textInput);
  }

  if (q.multiple) {
    const submit = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 disabled:opacity-50");
    submit.appendChild(sym("checkmark")); submit.appendChild(el("span", null, "Submit"));
    // Additive: include a typed free-text answer alongside the checked options
    // (when allow_text is also on), so "these options plus this note" is one answer.
    submit.onclick = () => {
      const ans = [...chosen];
      if (textInput) { const v = textInput.value.trim(); if (v) ans.push(v); }
      if (ans.length) answer(ans);
    };
    submitBtn = submit;
    actEl.appendChild(submit);
    syncSubmit(); // start disabled — nothing selected/typed yet
  }
  // A standalone Send button only when there are no options to submit alongside;
  // with options, the Submit button above already carries the typed text.
  if (q.allowText && !q.multiple) {
    const send = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5");
    send.appendChild(sym("paperplane")); send.appendChild(el("span", null, "Send"));
    send.onclick = () => { const v = textInput.value.trim(); if (v) answer([v]); };
    actEl.appendChild(send);
  }
  const skip = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5");
  skip.appendChild(sym("arrow.right.to.line")); skip.appendChild(el("span", null, "Skip"));
  skip.onclick = () => answer([]);
  actEl.appendChild(skip);

  questionEl.classList.remove("hidden");
  if (textInput) textInput.focus();
}

// answer fulfils a pending ask_user question with the chosen option labels
// (empty = dismissed). The panel hides on the resulting state change.
async function answer(labels) {
  if (!current) return;
  questionEl.classList.add("hidden");
  await fetch(api(`/api/sessions/${current}/answer`), { method: "POST", body: JSON.stringify({ labels: labels || [] }) });
}

// renderQuestionTabs renders a grouped ask_user (the questions[] form) as a strip
// of switchable tabs — one question each, with its own choices and/or text box —
// answered all at once. Per-question selections persist across tab switches, so
// the user can revisit any tab to change an answer. Submit sends one label-slice
// per question; Skip all dismisses every one.
function renderQuestionTabs(questions) {
  $(".q", questionEl).textContent = questions.length + " questions — answer each, then submit";
  const optsEl = $(".app-question-options", questionEl);
  const actEl = $(".app-question-actions", questionEl);
  optsEl.innerHTML = "";
  actEl.innerHTML = "";

  // Per-question answer state, preserved as the user switches tabs.
  const states = questions.map(() => ({ chosen: new Set(), text: "" }));
  let active = 0;
  let submitBtn = null;

  const tabsEl = el("div", "app-question-tabs");
  const panelEl = el("div", "app-question-panel");
  optsEl.appendChild(tabsEl);
  optsEl.appendChild(panelEl);

  const answered = (i) => states[i].chosen.size > 0 || !!states[i].text.trim();

  // sync refreshes the tab strip (active + answered marks) and the Submit enabled
  // state without rebuilding the active panel — safe to call while typing.
  const sync = () => {
    [...tabsEl.children].forEach((tab, i) => {
      tab.classList.toggle("is-active", i === active);
      tab.classList.toggle("is-answered", answered(i));
      const mk = $(".app-question-tab-mark", tab);
      if (mk) { mk.innerHTML = ""; mk.appendChild(sym(answered(i) ? "checkmark.circle.fill" : "circle", answered(i) ? "text-ok" : "text-faint")); }
    });
    if (submitBtn) submitBtn.disabled = !states.some((_, i) => answered(i));
  };

  // refresh rebuilds the active question's panel so option marks reflect the
  // stored state, then re-syncs the tabs. Called after an option toggle.
  const refresh = () => {
    panelEl.innerHTML = "";
    panelEl.appendChild(buildQuestionPanel(questions[active], states[active], refresh, sync));
    sync();
  };

  questions.forEach((q, i) => {
    const tab = el("button", "app-question-tab");
    const mark = el("span", "app-question-tab-mark");
    mark.appendChild(sym("circle", "text-faint"));
    tab.appendChild(mark);
    tab.appendChild(el("span", "app-question-tab-num", `Q${i + 1}`));
    const label = (q.question || `Question ${i + 1}`).replace(/\s+/g, " ").trim();
    tab.appendChild(el("span", "app-question-tab-label", label));
    tab.title = label;
    tab.onclick = () => { active = i; refresh(); };
    tabsEl.appendChild(tab);
  });

  refresh();

  const submit = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 disabled:opacity-50");
  submit.appendChild(sym("checkmark")); submit.appendChild(el("span", null, "Submit all"));
  submit.onclick = () => {
    const groups = states.map((s) => {
      const arr = [...s.chosen];
      const t = s.text.trim();
      if (t) arr.push(t);
      return arr;
    });
    answerGroups(groups);
  };
  submitBtn = submit;
  actEl.appendChild(submit);

  const skip = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5");
  skip.appendChild(sym("arrow.right.to.line")); skip.appendChild(el("span", null, "Skip all"));
  skip.onclick = () => answerGroups(questions.map(() => []));
  actEl.appendChild(skip);

  sync();
  questionEl.classList.remove("hidden");
}

// buildQuestionPanel renders one question's heading, options, and optional text
// box into a panel, mutating `state` ({chosen:Set, text}) on interaction. Option
// clicks call refresh() (rebuild panel + resync tabs); typing calls sync() only
// (so the caret survives). Single-choice options behave like radios (picking one
// clears the rest; clicking the picked one clears it); multiple-choice toggle.
function buildQuestionPanel(q, state, refresh, sync) {
  const wrap = el("div", "app-question-panel-body");
  wrap.appendChild(el("div", "app-question-qtext", q.question || ""));
  const list = el("div", "app-question-optlist");
  (q.options || []).forEach((o, i) => {
    const on = state.chosen.has(o.label);
    const btn = el("button", "app-question-option" + (on ? " is-on" : ""));
    const mark = el("span", "app-question-mark");
    mark.appendChild(sym(q.multiple ? (on ? "checkmark.square.fill" : "square") : (on ? "checkmark.circle.fill" : "circle")));
    btn.appendChild(mark);
    const body = el("div", "app-question-option-body");
    body.appendChild(el("div", "app-question-option-label", o.label || `Option ${i + 1}`));
    if (o.description) body.appendChild(el("div", "app-question-option-desc", o.description));
    btn.appendChild(body);
    btn.onclick = () => {
      if (q.multiple) {
        if (state.chosen.has(o.label)) state.chosen.delete(o.label); else state.chosen.add(o.label);
      } else {
        const was = state.chosen.has(o.label);
        state.chosen.clear();
        if (!was) state.chosen.add(o.label);
      }
      refresh();
    };
    list.appendChild(btn);
  });
  wrap.appendChild(list);
  if (q.allowText) {
    const ta = el("textarea", "app-question-text");
    ta.rows = 2;
    ta.placeholder = (q.options && q.options.length) ? "Or type your own answer…" : "Type your answer…";
    ta.value = state.text || "";
    ta.oninput = () => { state.text = ta.value; sync(); };
    wrap.appendChild(ta);
  }
  return wrap;
}

// answerGroups fulfils a grouped ask_user (the questions[] tabs) with one
// label-slice per question (empty slice = that question dismissed).
async function answerGroups(groups) {
  if (!current) return;
  questionEl.classList.add("hidden");
  await fetch(api(`/api/sessions/${current}/answer`), { method: "POST", body: JSON.stringify({ groups: groups || [] }) });
}

async function interrupt() {
  if (!current) return;
  await fetch(api(`/api/sessions/${current}/interrupt`), { method: "POST" });
}

// --- clickable menu (model / route / reasoning picker) ----------------------
function showMenu(menu) {
  hidePalette();
  if (menuLoadingTimer) { clearTimeout(menuLoadingTimer); menuLoadingTimer = null; } // data arrived — drop the loader's safety timeout
  menuEl.innerHTML = "";
  const head = el("div", "px-4 py-2 text-faint font-semibold border-b border-edge sticky top-0 bg-surface flex items-center justify-between");
  head.appendChild(el("span", null, menu.title || "Select"));
  const close = el("span", "cursor-pointer text-faint hover:text-ink");
  close.appendChild(sym("xmark"));
  close.onclick = hideMenu;
  head.appendChild(close);
  menuEl.appendChild(head);

  (menu.items || []).forEach((it) => {
    const row = el("div", "px-4 py-2 cursor-pointer flex items-center gap-2");
    row.appendChild(sym(it.current ? "checkmark.circle.fill" : "circle", (it.current ? "text-ok" : "text-faint") + " shrink-0"));
    const label = el("span", "font-mono " + (it.current ? "text-ok" : "text-brand"));
    label.textContent = prettyModel(it.label); // friendly model names (no-op for non-model labels)
    row.appendChild(label);
    if (it.desc) row.appendChild(el("span", "text-faint text-[11px] truncate", it.desc));
    row.onclick = () => { hideMenu(); sendText(menu.command + " " + it.value); };
    menuEl.appendChild(row);
  });
  menuEl.classList.remove("hidden");
}

function hideMenu() { if (menuLoadingTimer) { clearTimeout(menuLoadingTimer); menuLoadingTimer = null; } menuEl.classList.add("hidden"); menuEl.innerHTML = ""; }

// showMenuLoading puts a jumping-Claude loader in #menu while a picker's data is
// fetched (e.g. /models hits the model endpoints). The subsequent "menu" event
// runs showMenu(), which replaces this content; a safety timeout clears it if no
// menu ever arrives (e.g. an error path that doesn't emit a menu).
function showMenuLoading(title) {
  hidePalette();
  menuEl.innerHTML = "";
  const box = el("div", "menu-loading");
  const art = el("img", "menu-loading-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "";
  box.append(art, el("span", "text-faint", title || "Loading…"));
  menuEl.appendChild(box);
  menuEl.classList.remove("hidden");
  if (menuLoadingTimer) clearTimeout(menuLoadingTimer);
  menuLoadingTimer = setTimeout(() => { if (menuEl.querySelector(".menu-loading")) hideMenu(); }, 30000);
}

// --- slash-command palette --------------------------------------------------
// Shown when the input is a bare "/word" (no space yet), mirroring the terminal
// command hints. Tab/click completes; Enter sends the composed command.
// cmdToken renders a slash command as "/name [args]" with the name highlighted
// (brand) and the arguments distinct: the bracket/keyword scaffolding is faint
// and the <placeholders> you fill in are popped in amber.
function cmdToken(c) {
  const cmd = el("span", "shrink-0 font-mono");
  cmd.appendChild(el("span", "text-brand", c.name));
  if (c.args) {
    cmd.appendChild(document.createTextNode(" "));
    const args = el("span", "cmd-args");
    for (const part of c.args.split(/(<[^>]+>)/g)) {
      if (!part) continue;
      if (part[0] === "<" && part.endsWith(">")) args.appendChild(el("span", "cmd-arg-ph", part));
      else args.appendChild(document.createTextNode(part));
    }
    cmd.appendChild(args);
  }
  return cmd;
}

function renderPalette() {
  const m = /^\/(\S*)$/.exec(input.value);
  if (!m || commandList.length === 0) { hidePalette(); return; }
  const q = ("/" + m[1]).toLowerCase();
  paletteItems = commandList.filter((c) => c.name.toLowerCase().startsWith(q));
  if (paletteItems.length === 0) { hidePalette(); return; }
  if (paletteIdx >= paletteItems.length) paletteIdx = 0;

  paletteEl.innerHTML = "";
  paletteItems.forEach((c, i) => {
    const row = el("div", "px-4 py-1.5 cursor-pointer flex items-center gap-2" + (i === paletteIdx ? " is-sel" : ""));
    row.appendChild(sym("slash.circle", "text-faint shrink-0"));
    row.appendChild(cmdToken(c));
    row.appendChild(el("span", "text-faint text-[11px] truncate", c.desc || ""));
    row.onmousedown = (e) => { e.preventDefault(); completeCmd(c.name); };
    paletteEl.appendChild(row);
  });
  paletteEl.classList.remove("hidden");
}

function hidePalette() { paletteEl.classList.add("hidden"); paletteItems = []; paletteIdx = 0; }

function completeCmd(name) {
  input.value = name + " ";
  input.focus();
  renderPalette(); // value now has a trailing space, so the palette hides
}

// --- @-mention dropdown -----------------------------------------------------
// Shown when the text before the caret ends in a "@<partial>" token (the '@'
// starting the input or following whitespace/"(["), mirroring the terminal's
// live mention hints. Arrow keys navigate; Tab/click completes; Escape closes.
// Candidates (enabled agents + workers) arrive on the "commands" event.

// mentionQuery returns the trailing "@<partial>" token before the caret, or null.
function mentionQuery() {
  const pos = input.selectionStart ?? input.value.length;
  const m = /(^|[\s([])@([^\s@/\\]*)$/.exec(input.value.slice(0, pos));
  if (!m) return null;
  return { partial: m[2], at: pos - m[2].length - 1, caret: pos };
}

// MENTION_KIND_ICON: a leading glyph per mention kind for the dropdown.
const MENTION_KIND_ICON = { agent: "person.2.fill", worker: "arrow.turn.down.right", skill: "wand.and.stars", workflow: "flowchart", deep: "brain.head.profile" };

function renderMentions() {
  if (mentionList.length === 0) { hideMentions(); return; }
  const q = mentionQuery();
  if (!q) { hideMentions(); return; }
  const lp = q.partial.toLowerCase();
  mentionItems = mentionList.filter((mn) => mn.name.toLowerCase().startsWith(lp));
  if (mentionItems.length === 0) { hideMentions(); return; }
  if (mentionIdx >= mentionItems.length) mentionIdx = 0;

  mentionsEl.innerHTML = "";
  mentionItems.forEach((mn, i) => {
    const kind = mn.kind === "worker" ? "worker" : mn.kind === "skill" ? "skill" : mn.kind === "workflow" ? "workflow" : mn.kind === "deep" ? "deep" : "agent";
    const row = el("div", "px-4 py-1.5 cursor-pointer flex items-center gap-2" + (i === mentionIdx ? " is-sel" : ""));
    row.appendChild(sym(MENTION_KIND_ICON[kind], "text-faint shrink-0"));
    row.appendChild(el("span", "mention-row-name font-mono shrink-0", "@" + mn.name));
    row.appendChild(el("span", "mention-type mention-type-" + kind, kind));
    // Detail, minus the redundant kind word: workers carry "worker · <backend>",
    // so show just the backend; agents/skills show their description.
    let detail = mn.desc || "";
    if (kind === "worker") { const m = /·\s*(.+)$/.exec(detail); detail = m ? m[1] : ""; }
    if (detail) row.appendChild(el("span", "text-faint text-[11px] truncate", detail));
    row.onmousedown = (e) => { e.preventDefault(); completeMentionPick(mn.name); };
    mentionsEl.appendChild(row);
  });
  mentionsEl.classList.remove("hidden");
}

function hideMentions() { mentionsEl.classList.add("hidden"); mentionItems = []; mentionIdx = 0; }

// completeMentionPick replaces the trailing "@<partial>" token with "@name " and
// restores the caret right after it, preserving any text after the caret.
function completeMentionPick(name) {
  const q = mentionQuery();
  if (!q) return;
  const before = input.value.slice(0, q.at) + "@" + name + " ";
  input.value = before + input.value.slice(q.caret);
  input.focus();
  input.setSelectionRange(before.length, before.length);
  hideMentions();
}

sendBtn.onclick = () => { if (state === "busy") interrupt(); else send(); };

// Composer placeholder: the desktop hint mentions Enter/paste/drop and is long
// enough to wrap (and clip) in the narrow mobile composer, so swap in a short one
// on phones. The full desktop text stays the source of truth (read from the HTML
// attribute); we react to viewport changes so a rotate/resize updates it live.
// While busy on a queue-capable session the composer stays enabled for type-ahead
// (see setState), so the placeholder swaps to say so instead of the idle hint.
const INPUT_PH_FULL = input.getAttribute("placeholder") || "Ask sqe to build, fix, or explain something…";
const INPUT_PH_SHORT = "Message sqe…";
const INPUT_PH_QUEUE = "You can add a message here for queue";
const phoneMQ = window.matchMedia("(max-width: 768px)");
function syncInputPlaceholder() {
  const ph = (state === "busy" && queueCapable) ? INPUT_PH_QUEUE : (phoneMQ.matches ? INPUT_PH_SHORT : INPUT_PH_FULL);
  input.setAttribute("placeholder", ph);
}
syncInputPlaceholder();
if (phoneMQ.addEventListener) phoneMQ.addEventListener("change", syncInputPlaceholder);
else if (phoneMQ.addListener) phoneMQ.addListener(syncInputPlaceholder); // Safari < 14 fallback


// --- Paged transcript: one Q&A pair shown at a time -------------------------
// startPair opens a new pair (pinned question header + scrollable answer body),
// makes its body the append target for the answer, and jumps the view to it.
function startPair(questionMsg) {
  const root = el("div", "pair");
  const q = el("div", "pair-q");
  q.appendChild(questionMsg);
  const answer = el("div", "pair-a");
  answer.addEventListener("scroll", () => onAnswerScroll(answer));
  root.appendChild(q); root.appendChild(answer);
  transcript.appendChild(root);
  pairs.push({ root, answer, q });
  answerHost = answer;   // new answer content appends here, even if the user pages away
  stickBottom = true;
  setActivePair(pairs.length - 1); // show the newest pair
}

// setActivePair shows one pair (by index) and updates the pager. answerHost is
// NOT changed here — new content always streams into the latest pair, so paging
// back to read history never redirects a live answer.
function setActivePair(idx) {
  if (idx < 0 || idx >= pairs.length) return;
  activePairIdx = idx;
  for (let i = 0; i < pairs.length; i++) pairs[i].root.classList.toggle("pair-active", i === idx);
  renderPager();
  updateScrollBtn();
}

// renderPager shows the "< n/total >" nav (#pagerNav) once there are ≥2
// pairs, hidden for a single pair; syncPagerVisibility then decides whether
// the shared #pager container itself needs to show (it also hosts
// #scrollBtn, so a single pair with a long scrolled-up answer still gets a
// visible control). The middle "n/total" opens a dropdown of every question
// in the turn, so paging back doesn't require clicking prev one at a time.
function renderPager() {
  if (!pager || !pagerNav) return;
  const n = pairs.length;
  pagerNav.hidden = n <= 1;
  if (pagerNav.hidden && hdrMenuEl && hdrMenuEl.dataset.kind === "pager") { closeHdrMenu(); pagerPos.setAttribute("aria-expanded", "false"); }
  if (pagerPos) pagerPos.textContent = (activePairIdx + 1) + "/" + n;
  if (pagerPrev) pagerPrev.disabled = activePairIdx <= 0;
  if (pagerNext) pagerNext.disabled = activePairIdx >= n - 1;
  syncPagerVisibility();
}

// syncPagerVisibility shows the shared #pager container whenever either of its
// parts (scroll-to-bottom, the pair nav) needs to be visible, and hides it only
// when NEITHER does — so it never lingers as an empty shell. Jobs is no longer one
// of them: it's its own pill now, and folding it in here kept the pager alive as an
// empty frame around the jobs button alone.
function syncPagerVisibility() {
  if (!pager) return;
  const navVisible = pagerNav && !pagerNav.hidden;
  const scrollVisible = scrollBtn && !scrollBtn.hidden;
  pager.hidden = !navVisible && !scrollVisible;
  if (typeof syncFloatingPills === "function") syncFloatingPills();
}

// ─── Background-jobs button ─────────────────────────────────────────────────
//
// The pager pill doubles as the entry point to the jobs panel, because a job
// outlives the turn that started it: without a permanent control, a dev server
// left running is invisible until someone thinks to open Manage → Jobs.
//
// The badge counts RUNNING jobs only — a finished job is history, not something
// that still wants attention.
let jobsBadgePollTimer = null;

async function refreshJobsBadge() {
  if (!jobsBtn || !jobsPill) return;
  if (!current) { jobsPill.hidden = true; syncFloatingPills(); return; }
  let running = 0, total = 0;
  try {
    const r = await fetch(api(`/api/sessions/${current}/jobs`));
    if (!r.ok) throw new Error("jobs");
    // The list endpoint answers {jobs, policy}, not a bare array.
    const data = await r.json();
    const list = Array.isArray(data) ? data : (data && data.jobs) || [];
    total = list.length;
    running = list.filter((j) => j.running).length;
  } catch (_) {
    // A failed poll must not blank the button mid-session; leave what's on screen.
    return;
  }
  jobsPill.hidden = false;
  jobsBtn.classList.toggle("is-running", running > 0);
  jobsBtn.title = running
    ? `${running} background job${running === 1 ? "" : "s"} running`
    : total
      ? `Background jobs (${total})`
      : "Background jobs";
  if (jobsBtnCount) {
    jobsBtnCount.hidden = running === 0;
    jobsBtnCount.textContent = running > 9 ? "9+" : String(running);
  }
  // The badge changes the pill's width, and the pill may have just appeared, so the
  // strip has to re-measure — the pager's own visibility is untouched by jobs now.
  syncFloatingPills();
}

// Polled rather than pushed: jobs finish on their own, with no event to announce
// it. Paused while the tab is hidden so a backgrounded browser isn't asking every
// few seconds forever.
function startJobsBadgePoll() {
  if (jobsBadgePollTimer) clearInterval(jobsBadgePollTimer);
  jobsBadgePollTimer = setInterval(() => {
    if (!document.hidden) refreshJobsBadge();
  }, 10000);
  refreshJobsBadge();
}

if (jobsBtn) {
  jobsBtn.onclick = () => {
    if (typeof openConfigModal === "function") openConfigModal("jobs");
  };
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refreshJobsBadge();
  });
  startJobsBadgePoll();
}

// openPagerMenu lists every question in the turn (newest last) in the shared
// header dropdown; picking one jumps straight to that pair, like the prev/next
// buttons but without the clicks. Mirrors openHdrMenu's item styling/role, but
// dispatches a local setActivePair instead of a slash command.
function openPagerMenu() {
  if (!hdrMenuEl || !pagerPos) return;
  if (hdrMenuOpen() && hdrMenuEl.dataset.kind === "pager") { closeHdrMenu(); pagerPos.setAttribute("aria-expanded", "false"); return; }
  hdrMenuEl.innerHTML = "";
  hdrMenuEl.dataset.kind = "pager";
  pairs.forEach((p, i) => {
    const isCurrent = i === activePairIdx;
    const item = el("button", "hdr-menu-item" + (isCurrent ? " is-current" : ""));
    item.type = "button";
    item.setAttribute("role", "menuitemradio");
    item.setAttribute("aria-checked", isCurrent ? "true" : "false");
    const text = ((p.q && p.q.textContent) || "").trim().replace(/\s+/g, " ") || "(empty)";
    item.appendChild(el("span", "hdr-menu-label", (i + 1) + ". " + text.slice(0, 80)));
    item.onclick = (e) => { e.stopPropagation(); closeHdrMenu(); pagerPos.setAttribute("aria-expanded", "false"); if (!isCurrent) setActivePair(i); };
    hdrMenuEl.appendChild(item);
  });
  placeHdrMenu(pagerPos);
  pagerPos.setAttribute("aria-expanded", "true");
}

// resetPairs clears pager state when the transcript is wiped (session switch/reset).
function resetPairs() {
  pairs = []; activePairIdx = -1; answerHost = null;
  if (scrollBtn) scrollBtn.hidden = true;
  renderPager(); // also re-syncs #pager's own visibility
}

// The scroll-to-bottom button + auto-stick track the VIEWED answer; auto-stick
// only follows the latest pair (the one new content streams into).
function onAnswerScroll(a) {
  const cur = pairs[activePairIdx];
  if (!cur || cur.answer !== a) return; // ignore scrolls in hidden pairs
  const nearBottom = a.scrollHeight - a.scrollTop - a.clientHeight < 80;
  if (scrollBtn) scrollBtn.hidden = nearBottom;
  if (activePairIdx === pairs.length - 1) stickBottom = nearBottom;
  syncPagerVisibility();
}
function updateScrollBtn() {
  if (!scrollBtn) return;
  const cur = pairs[activePairIdx];
  if (!cur) { scrollBtn.hidden = true; syncPagerVisibility(); return; }
  const a = cur.answer;
  scrollBtn.hidden = a.scrollHeight - a.scrollTop - a.clientHeight < 80;
  syncPagerVisibility();
}

if (scrollBtn) scrollBtn.onclick = () => {
  const cur = pairs[activePairIdx];
  if (cur) cur.answer.scrollTop = cur.answer.scrollHeight;
  if (activePairIdx === pairs.length - 1) stickBottom = true;
  scrollBtn.hidden = true;
  syncPagerVisibility();
};
if (pagerPos) pagerPos.onclick = (e) => { e.stopPropagation(); openPagerMenu(); };
if (pagerPrev) pagerPrev.onclick = () => setActivePair(activePairIdx - 1);
if (pagerNext) pagerNext.onclick = () => setActivePair(activePairIdx + 1);

input.addEventListener("input", () => { syncInputHighlight(); hideMenu(); paletteIdx = 0; renderPalette(); mentionIdx = 0; renderMentions(); });

// --- Backtick code highlighting -----------------------------------------
// A <textarea> can't style parts of its own text, so we mirror it into an
// aria-hidden overlay (#inputHighlight) rendered behind a transparent-background
// textarea. The mirror's text is invisible; only its tinted `...` backgrounds
// show through behind the textarea's own (still visible) text. Active whenever
// the message contains a backtick.
const inputHL = $("#inputHighlight");
function escHTML(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
function buildHL(v) {
  let out = "", i = 0;
  while (i < v.length) {
    if (v[i] === "`") {
      const end = v.indexOf("`", i + 1);
      if (end === -1) { out += `<span class="ihl-code">${escHTML(v.slice(i))}</span>`; return out; }
      out += `<span class="ihl-code">${escHTML(v.slice(i, end + 1))}</span>`;
      i = end + 1;
    } else {
      const nxt = v.indexOf("`", i);
      const seg = nxt === -1 ? v.slice(i) : v.slice(i, nxt);
      out += escHTML(seg); i += seg.length;
    }
  }
  return out;
}
// Box metrics copied from the textarea so the mirror's glyphs line up exactly.
const HL_PROPS = ["fontFamily", "fontSize", "fontWeight", "fontStyle", "letterSpacing",
  "wordSpacing", "lineHeight", "textIndent", "textTransform", "padding",
  "borderWidth", "borderStyle", "borderRadius", "boxSizing", "tabSize"];
function syncInputHighlight() {
  if (!inputHL) return;
  const v = input.value;
  const on = v.includes("`");
  input.classList.toggle("hl-on", on);
  inputHL.classList.toggle("hidden", !on);
  if (!on) { inputHL.innerHTML = ""; return; }
  const cs = getComputedStyle(input);
  for (const p of HL_PROPS) inputHL.style[p] = cs[p];
  inputHL.style.borderColor = "transparent";
  // trailing newline needs a filler char or the mirror drops the last blank line
  inputHL.innerHTML = buildHL(v) + (v.endsWith("\n") ? "\n" : "");
  inputHL.scrollTop = input.scrollTop;
  inputHL.scrollLeft = input.scrollLeft;
}
input.addEventListener("scroll", () => {
  if (inputHL) { inputHL.scrollTop = input.scrollTop; inputHL.scrollLeft = input.scrollLeft; }
});
// Keep the mirror in sync with programmatic `input.value = ...` assignments too.
const _taValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
Object.defineProperty(input, "value", {
  configurable: true,
  get() { return _taValue.get.call(this); },
  set(v) { _taValue.set.call(this, v); syncInputHighlight(); },
});
syncInputHighlight();
input.addEventListener("keydown", (e) => {
  if (!paletteEl.classList.contains("hidden")) {
    if (e.key === "ArrowDown") { e.preventDefault(); paletteIdx = Math.min(paletteIdx + 1, paletteItems.length - 1); renderPalette(); return; }
    if (e.key === "ArrowUp") { e.preventDefault(); paletteIdx = Math.max(paletteIdx - 1, 0); renderPalette(); return; }
    if (e.key === "Tab") { e.preventDefault(); if (paletteItems[paletteIdx]) completeCmd(paletteItems[paletteIdx].name); return; }
    if (e.key === "Escape") { e.preventDefault(); hidePalette(); return; }
  }
  if (!mentionsEl.classList.contains("hidden")) {
    if (e.key === "ArrowDown") { e.preventDefault(); mentionIdx = Math.min(mentionIdx + 1, mentionItems.length - 1); renderMentions(); return; }
    if (e.key === "ArrowUp") { e.preventDefault(); mentionIdx = Math.max(mentionIdx - 1, 0); renderMentions(); return; }
    if (e.key === "Tab") { e.preventDefault(); if (mentionItems[mentionIdx]) completeMentionPick(mentionItems[mentionIdx].name); return; }
    if (e.key === "Escape") { e.preventDefault(); hideMentions(); return; }
  }
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); hidePalette(); hideMentions(); send(); }
  // Esc while busy pops the most recent queued message back into the composer
  // for editing/discard (stashing any in-progress draft onto the queue front so
  // it isn't lost), mirroring the terminal. With an empty queue, Esc interrupts.
  if (e.key === "Escape" && queueCapable && state === "busy") {
    e.preventDefault();
    if (queuedMsgs.length > 0) popQueued();
    else interrupt();
  }
});
approval.querySelectorAll("button").forEach((b) => b.onclick = () => approve(b.dataset.d));

// Attachments: an Image / Document chooser, drag-drop onto the composer, and
// image paste. The paperclip opens a small menu; each item opens a picker
// filtered to that kind (distinct image vs document upload). drag-drop / paste
// still accept any supported file via classifyAttachment.
function closeAttachMenu() {
  if (!attachMenu) return;
  attachMenu.classList.remove("open");
  if (attachBtn) attachBtn.classList.remove("menu-open"); // un-rotate the + icon
}
if (attachBtn && attachMenu) {
  attachBtn.onclick = (e) => {
    e.stopPropagation();
    const open = attachMenu.classList.toggle("open");
    attachBtn.classList.toggle("menu-open", open); // rotate + → × while open
  };
  attachMenu.querySelectorAll(".attach-menu-item").forEach((item) => {
    item.onclick = (e) => {
      e.stopPropagation();
      closeAttachMenu();
      (item.dataset.kind === "image" ? imageInput : docInput).click();
    };
  });
  document.addEventListener("click", closeAttachMenu); // an outside click dismisses it
}
[imageInput, docInput].forEach((inp) => inp && inp.addEventListener("change", () => {
  for (const f of inp.files) addAttachmentFile(f);
  inp.value = ""; // allow re-picking the same file
}));
const inputArea = $("#inputArea");
["dragenter", "dragover"].forEach((ev) => inputArea.addEventListener(ev, (e) => {
  e.preventDefault();
  inputArea.classList.add("drop-active");
}));
["dragleave", "drop"].forEach((ev) => inputArea.addEventListener(ev, (e) => {
  e.preventDefault();
  inputArea.classList.remove("drop-active");
}));
inputArea.addEventListener("drop", (e) => {
  const files = e.dataTransfer && e.dataTransfer.files;
  if (files) for (const f of files) addAttachmentFile(f);
});
input.addEventListener("paste", (e) => {
  const items = e.clipboardData && e.clipboardData.items;
  if (!items) return;
  for (const it of items) {
    if (it.kind === "file" && it.type.startsWith("image/")) {
      const f = it.getAsFile();
      if (f) addAttachmentFile(f);
    }
  }
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && attachMenu && attachMenu.classList.contains("open")) { closeAttachMenu(); return; }
  if (e.key === "Escape" && quickActionMenu && quickActionMenu.classList.contains("open")) { closeQuickActionMenu(); return; }
  if (e.key === "Escape" && userMenuEl && userMenuEl.classList.contains("open")) { userMenuEl.classList.remove("open"); return; }
  if (e.key === "Escape" && document.body.classList.contains("nav-open")) { document.body.classList.remove("nav-open"); return; }
  // No Escape-to-close for manageOverlay: the modal closes only via its X button
  // (#manageClose), so an accidental keypress can't discard a partly-filled panel.
  if (e.key === "Escape" && !menuEl.classList.contains("hidden")) { hideMenu(); return; }
  if (approval.classList.contains("hidden")) return;
  if (e.key === "y") approve("approve"); else if (e.key === "n") approve("deny"); else if (e.key === "a") approve("always");
});

// Sidebar toggle: on phones it slides the drawer in/out (`nav-open`); on desktop
// it collapses the sidebar to the left (`nav-collapsed`). The scrim, a navigation
// tap, or Escape dismiss the mobile drawer (see selectSession / the keydown above).
$("#navBtn").onclick = () => {
  const mobile = window.matchMedia("(max-width: 768px)").matches;
  document.body.classList.toggle(mobile ? "nav-open" : "nav-collapsed");
};
$("#navBackdrop").onclick = () => document.body.classList.remove("nav-open");

// Sidebar resize: drag the right-edge handle to set the sidebar width (desktop
// only). The width is a CSS variable persisted to localStorage and clamped so it
// can't get uselessly narrow or crowd out the conversation; double-click resets.
(function initSidebarResize() {
  const KEY = "sidebarWidth", MIN = 200, MAX = 560;
  const handle = $("#sidebarResize");
  const sidebar = document.querySelector(".app-sidebar");
  if (!handle || !sidebar) return;

  // Scope the variable to the sidebar element (not :root) so a resize only
  // recalcs the sidebar, and restore any saved width.
  const setW = (px) => sidebar.style.setProperty("--sidebar-w", px + "px");
  const saved = parseInt(localStorage.getItem(KEY) || "", 10);
  if (saved >= MIN && saved <= MAX) setW(saved);

  let dragging = false, left = 0, raf = 0, pendingW = 0;
  const flush = () => { raf = 0; setW(pendingW); };
  const onMove = (e) => {
    if (!dragging) return;
    // Use the left edge cached at drag start (it doesn't move) so we never force
    // a synchronous layout during the drag; coalesce to one write per frame.
    pendingW = Math.max(MIN, Math.min(MAX, Math.round(e.clientX - left)));
    if (!raf) raf = requestAnimationFrame(flush);
  };
  const onUp = (e) => {
    if (!dragging) return;
    dragging = false;
    if (raf) { cancelAnimationFrame(raf); raf = 0; setW(pendingW); }
    document.body.classList.remove("sidebar-resizing");
    try { handle.releasePointerCapture(e.pointerId); } catch {}
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    localStorage.setItem(KEY, String(pendingW));
  };
  handle.addEventListener("pointerdown", (e) => {
    if (window.matchMedia("(max-width: 768px)").matches) return; // drawer mode
    e.preventDefault();
    dragging = true;
    left = sidebar.getBoundingClientRect().left; // cache once; avoids per-move reflow
    pendingW = sidebar.getBoundingClientRect().width;
    try { handle.setPointerCapture(e.pointerId); } catch {}
    document.body.classList.add("sidebar-resizing");
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  });
  handle.addEventListener("dblclick", () => {
    sidebar.style.removeProperty("--sidebar-w");
    localStorage.removeItem(KEY);
  });
})();

// --- light / dark theme -----------------------------------------------------
// The <head> bootstrap already applied the saved (or system) choice to <html>
// before first paint; here we keep the toggle's icon in sync, flip + persist on
// click, and re-render the few JS-drawn bits (sidebar accents, context bar) that
// read their colors from the palette tokens so they follow the switch too.
// (CSS-only surfaces re-theme on their own via the var swap.)
const themeOf = () => (document.documentElement.classList.contains("light") ? "light" : "dark");
function setThemeIcon(t) {
  if (!themeToggleEl) return;
  themeToggleEl.innerHTML = "";
  const ic = sym(t === "light" ? "sun.max.fill" : "moon.stars.fill");
  ic.style.fontSize = "16px";
  themeToggleEl.appendChild(ic);
  themeToggleEl.title = t === "light" ? "Switch to dark theme" : "Switch to light theme";
}
function applyTheme(t) {
  const r = document.documentElement;
  r.classList.remove("light", "dark");
  r.classList.add(t);
  r.style.colorScheme = t;
  try { localStorage.setItem("sqe-theme", t); } catch (_) { /* storage blocked — won't persist */ }
  setThemeIcon(t);
  if (laptops) renderLaptopTree(); else renderSessionList();
  renderHeader();
  renderStatusBar();
}
function toggleTheme() { applyTheme(themeOf() === "light" ? "dark" : "light"); }
if (themeToggleEl) {
  setThemeIcon(themeOf());
  themeToggleEl.onclick = () => toggleTheme();
}

// --- Completion chime (web equivalent of the TUI's afplay sound) ---------------
// Synthesized with the Web Audio API so there's no audio asset to embed/serve.
// A short two-tone "ding". The AudioContext is created lazily and resumed on the
// first user gesture (browsers block autoplay until then).
let _audioCtx = null;
function audioCtx() {
  if (_audioCtx) return _audioCtx;
  const AC = window.AudioContext || window.webkitAudioContext;
  if (!AC) return null;
  _audioCtx = new AC();
  return _audioCtx;
}
function chime() {
  if (!soundEnabled) return;
  const ctx = audioCtx();
  if (!ctx) return;
  if (ctx.state === "suspended") ctx.resume().catch(() => {});
  const now = ctx.currentTime;
  // Two soft sine tones (a rising fifth) with a quick decay.
  [[880, 0], [1320, 0.12]].forEach(([freq, at]) => {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = "sine";
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0.0001, now + at);
    gain.gain.exponentialRampToValueAtTime(0.18, now + at + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + at + 0.35);
    osc.connect(gain).connect(ctx.destination);
    osc.start(now + at);
    osc.stop(now + at + 0.4);
  });
}
function setSoundIcon() {
  if (!soundToggleEl) return;
  soundToggleEl.innerHTML = "";
  const ic = sym(soundEnabled ? "speaker.wave.2.fill" : "speaker.slash.fill");
  ic.style.fontSize = "16px";
  soundToggleEl.appendChild(ic);
  soundToggleEl.title = soundEnabled ? "Completion sound on — click to mute" : "Completion sound off — click to enable";
}
function toggleSound() {
  soundEnabled = !soundEnabled;
  try { localStorage.setItem("sqe-sound", soundEnabled ? "on" : "off"); } catch (_) { /* storage blocked */ }
  setSoundIcon();
  if (soundEnabled) chime(); // preview + unlocks the AudioContext via this gesture
}
if (soundToggleEl) {
  setSoundIcon();
  soundToggleEl.onclick = () => toggleSound();
}

// Dismiss the version-skew banner for this page load (it re-evaluates on the
// next status event otherwise).
const versionWarnCloseEl = $("#versionWarnClose");
if (versionWarnCloseEl) {
  versionWarnCloseEl.onclick = () => {
    versionWarnDismissed = true;
    if (versionWarnEl) versionWarnEl.classList.add("hidden");
    refreshReloadBanner(); // strip is free now — surface any deferred reload prompt
  };
}

// Reload banner (front-end asset skew): reload picks up the relay's new assets;
// close hides it for this page's lifetime (the dataset flag keeps later polls
// from re-showing it, so a user who keeps working isn't nagged every minute).
const reloadBannerEl = $("#reloadBanner");
const reloadBannerReloadEl = $("#reloadBannerReload");
if (reloadBannerReloadEl) reloadBannerReloadEl.onclick = () => location.reload();
const reloadBannerCloseEl = $("#reloadBannerClose");
if (reloadBannerCloseEl) {
  reloadBannerCloseEl.onclick = () => {
    if (reloadBannerEl) { reloadBannerEl.dataset.dismissed = "1"; reloadBannerEl.classList.add("hidden"); }
  };
}

// Upgrade: run /update on the connected terminal remotely (it pulls the new
// build from the relay and restarts), so the user needn't download/install by
// hand. Progress shows in the transcript; on success the terminal reconnects on
// the new version and the banner clears itself. Reset after a timeout so a
// failed/ignored update stays retryable.
if (versionWarnUpgradeEl) {
  versionWarnUpgradeEl.onclick = () => {
    if (upgrading || state !== "idle") return; // ignored mid-turn (remoteSubmit drops it)
    upgradeStartVer = (lastStatus && lastStatus.version) || "";
    upgrading = true;
    syncUpgradeBtn();
    sendText("/update");
    // Safety net only — normally cleared when the terminal reports back updated
    // (see updateVersionWarning), well before this fires.
    setTimeout(() => { upgrading = false; syncUpgradeBtn(); }, 120000);
  };
}

// Account avatar menu: the avatar toggles it; an outside click or Escape closes
// it. "Manage" reuses the existing overlay; "Repository" is a plain link.
if (userAvatarEl && userMenuEl) {
  userAvatarEl.onclick = (e) => { e.stopPropagation(); userMenuEl.classList.toggle("open"); };
  document.addEventListener("click", () => userMenuEl.classList.remove("open"));
  const um = $("#umManage");
  if (um) um.onclick = () => { userMenuEl.classList.remove("open"); openManage(); };
  const umc = $("#umConfig");
  if (umc) umc.onclick = () => { userMenuEl.classList.remove("open"); openConfigModal(); };
  const umpr = $("#umPersona");
  if (umpr) umpr.onclick = () => { userMenuEl.classList.remove("open"); openPersona(); };
  const uma = $("#umAdmin");
  if (uma) uma.onclick = () => { userMenuEl.classList.remove("open"); openAdminModal(); };
  const ump = $("#umPlugins");
  if (ump) ump.onclick = () => { userMenuEl.classList.remove("open"); openPlugins(); };
}

// --- manage panel (skills + MCP servers for the current session) ------------
const manageOverlay = $("#manageOverlay"), manageBody = $("#manageBody");
const manageTitleEl = $("#manageTitle"), manageIconEl = $("#manageIcon");

// setPanel opens the shared modal with a title + Material Symbols icon icon and a fresh body.
function setPanel(title, icon) {
  if (manageTitleEl) manageTitleEl.textContent = title;
  if (manageIconEl) manageIconEl.setAttribute("name", icon);
  manageBody.innerHTML = "";
  manageTabsEl.style.display = "none"; manageTabsEl.innerHTML = ""; // only the Config modal (openConfig) shows tabs
  setManageWidth("");
  manageOverlay.style.display = "flex";
}

// setManageWidth overrides the modal card's width (default min(640px,92vw)). The
// git panel uses a wider card for its two-column layout; "" restores the default.
function setManageWidth(width) {
  const card = manageOverlay.querySelector(".app-modal");
  if (card) card.style.width = width || "min(640px,92vw)";
}

async function openManage() {
  if (!current) { alertBox("Open a session first to manage its workers."); return; }
  setPanel("Manage", "slider.horizontal.3");
  await refreshManage();
}
const manageTabsEl = $("#manageTabs");

// Generic tab-strip shell shared by every tabbed panel (Config: Skills/MCP/
// Agents/Tools/Domains/LSP/Schedule; Admin: Announcements/Members) — one modal
// per group instead of one per user-menu item, so switching between related
// panels doesn't mean closing and reopening. Each user-menu item opens its
// group's modal, landing on its own tab; renders reuse the exact same
// per-panel functions (they already target manageBody and self-clear it).
let activeTabs = [];
let activeTab = null;

// renderTabStrip (re)builds the tab strip for the currently-open group,
// highlighting the active tab.
function renderTabStrip() {
  manageTabsEl.innerHTML = "";
  manageTabsEl.style.display = "flex";
  let activeBtn = null;
  for (const t of activeTabs) {
    const isActive = t.key === activeTab;
    const b = el("button", "config-tab" + (isActive ? " config-tab-active" : ""), "");
    b.type = "button";
    b.append(sym(t.icon), el("span", "", " " + t.label));
    b.onclick = () => selectTab(t.key);
    manageTabsEl.append(b);
    if (isActive) activeBtn = b;
  }
  // The strip scrolls horizontally and there are a dozen tabs, so the later ones
  // (Jobs, Doctor) start off-screen. Opening one straight from a shortcut would
  // otherwise render its body with the strip still parked on the first tab —
  // which reads as "that tab doesn't exist".
  if (activeBtn) {
    requestAnimationFrame(() => {
      try { activeBtn.scrollIntoView({ inline: "nearest", block: "nearest" }); } catch (_) {}
    });
  }
}

// stopConfigPolls halts every background poll a Config tab may have started
// (install progress, MCP server status) — called before switching tabs and on
// close, so a backgrounded tab can't keep re-rendering a body it no longer owns.
function stopConfigPolls() { stopLspPoll(); stopMcpPoll(); stopMarketPoll(); stopJobsPolling(); }

// selectTab switches the active tab within the currently-open group: stop the
// outgoing tab's polls, re-render the strip, then render the new tab's content.
function selectTab(key) {
  stopConfigPolls();
  activeTab = activeTabs.find((t) => t.key === key) ? key : activeTabs[0].key;
  renderTabStrip();
  activeTabs.find((t) => t.key === activeTab).render();
}

// openTabbedModal opens `title`/`icon` as the shared modal shell with `tabs` as
// its tab strip, landing on `tabKey` (defaults to the first tab).
function openTabbedModal(title, icon, tabs, tabKey) {
  activeTabs = tabs;
  setPanel(title, icon);
  selectTab(tabKey || tabs[0].key);
}

// Config modal: Skills/MCP/Agents/Tools/Domains/LSP/Schedule.
const CONFIG_TABS = [
  { key: "skills", label: "Skills", icon: "wand.and.stars", render: () => renderSkillsMarketplace() },
  { key: "mcp", label: "MCP", icon: "puzzlepiece.extension", render: () => renderMcpMarketplace() },
  { key: "agents", label: "Agents", icon: "person.2.fill", render: () => renderAgentsMarketplace() },
  { key: "tools", label: "Tools", icon: "wrench.and.screwdriver", render: () => renderAccessPanel(manageBody, "/api/permissions", "rule", {
      intro: "Permanent allow/deny rules for tool calls — deny wins, and they're checked before the approval prompt. A rule is a tool name (e.g. write_file) or a bash prefix (e.g. \"bash:git \").",
      placeholder: "tool name or bash:prefix",
    }) },
  { key: "domains", label: "Domains", icon: "globe", render: () => renderAccessPanel(manageBody, "/api/domains", "domain", {
      intro: "Domains web_fetch may reach. Deny blocks a domain and its subdomains. Add any Allow entry to lock down to an allowlist (everything else is then blocked).",
      placeholder: "example.com",
    }) },
  { key: "lsp", label: "LSP", icon: "curlybraces", render: () => renderLspMarketplace() },
  { key: "schedule", label: "Schedule", icon: "calendar.badge.clock", render: () => renderSchedules() },
  { key: "quickactions", label: "Quick actions", icon: "bolt.fill", render: () => renderQuickActionsPanel() },
  { key: "taskboard", label: "Task Board", icon: "square.grid.2x2", render: () => renderTaskBoard() },
  { key: "codebasemap", label: "Codebase map", icon: "map.fill", render: () => renderCodebaseMapPanel() },
  { key: "jobs", label: "Jobs", icon: "terminal", render: () => renderJobsPanel() },
  { key: "doctor", label: "Doctor", icon: "stethoscope", render: () => renderDoctorPanel() },
];

// openConfigModal opens the Config modal on the given tab (defaults to Skills).
// All 7 panels operate on the connected terminal's machine, so they share one
// connector guard here instead of each repeating it.
function openConfigModal(tabKey) {
  if (!current) { alertBox("Connect to a terminal first — these settings apply to that machine."); return; }
  openTabbedModal("Config", "gearshape", CONFIG_TABS, tabKey);
}

// Admin modal: Announcements (maintainer update feed + admin compose/delete)
// and Members (the allow-list of usernames granted access outside the auth
// group). Both #umAdmin's two former separate menu items — grouped since both
// are admin-only tools, unrelated to the per-connector Config settings above.
let usagePanelRange = "all"; // sticky across re-renders of the Usage tab (not persisted)
const ADMIN_TABS = [
  { key: "announcements", label: "Announcements", icon: "megaphone.fill", render: async () => {
      manageBody.textContent = "Loading…";
      await loadRelayVersion(); // refresh announcements + admin flag
      renderAnnouncementsPanel(manageBody);
    } },
  { key: "members", label: "Members", icon: "person.3.fill", render: () => renderMembersPanel(manageBody) },
  { key: "usage", label: "Usage", icon: "chart.bar.fill", render: () => renderUsagePanel(manageBody) },
];

// openAdminModal opens the Admin modal on the given tab (defaults to Announcements).
function openAdminModal(tabKey) { openTabbedModal("Admin", "shield.fill", ADMIN_TABS, tabKey); }

function closeManage() { manageOverlay.style.display = "none"; manageBody.innerHTML = ""; manageTabsEl.style.display = "none"; manageTabsEl.innerHTML = ""; stopConfigPolls(); stopWorkerPoll(); }


// openMembers (admin only) manages the allow-list of usernames granted access
// even though they aren't in MN_AUTH_GROUP_DSID. It's a single-list variant of
// renderAccessPanel (no deny list), backed by the global /api/members endpoint.
// Unlike openDomains it isn't session-scoped — it edits server-wide config — so
// it doesn't require a connected terminal.
async function renderMembersPanel(body) {
  body.innerHTML = "";
  body.append(el("p", "access-intro",
    "Usernames here are granted access even though they're not in MN_AUTH_GROUP_DSID. " +
    "Changes take effect on the user's next login. Each row shows today's provider spend " +
    "against the daily cap — click the pencil to set a per-user cap (blank reverts to the " +
    "default); group members who've logged in and used quota are listed below."));
  const list = el("div", null);
  body.append(list);

  const form = el("div", "access-form");
  const input = el("input", "access-input");
  input.type = "text";
  input.placeholder = "username";
  const addBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Add");
  form.append(input, addBtn);
  body.append(form);

  let lastData = null;
  const money = (n) => "$" + (Number(n) || 0).toFixed(2);
  const render = (data) => {
    lastData = data;
    list.innerHTML = "";
    const usage = (data && data.usage) || [];
    const defaultCap = (data && data.cap) || 0;
    const byUser = {};
    usage.forEach((u) => { byUser[u.user] = u; });

    // quotaCell renders "$spend / $cap" plus a pencil to edit that user's daily
    // cap inline. cap is the effective cap (the user's override, else the default).
    const quotaCell = (user, u) => {
      const cell = el("span", "access-quota-cell");
      const sp = u ? u.spend : 0;
      const cap = (u && u.cap) || defaultCap;
      const chip = el("span", "access-quota", money(sp) + (cap ? " / " + money(cap) : ""));
      if (cap && sp >= cap) chip.classList.add("access-quota-over");      // at/over cap
      if (cap && defaultCap && cap !== defaultCap) {                       // custom override
        chip.classList.add("access-quota-custom");
        chip.title = "Custom daily cap (default " + money(defaultCap) + ")";
      }
      cell.append(chip);
      if (data && "cap" in data) { // a usage/cap source is present (relay) → allow editing
        const edit = el("button", "access-capedit"); edit.title = "Set daily cap"; edit.append(sym("pencil"));
        edit.onclick = () => editCap(cell, user, cap);
        cell.append(edit);
        if (sp > 0) { // reset today's used quota back to $0 (fresh budget until it re-accrues)
          const rst = el("button", "access-capedit"); rst.title = "Reset today's used quota to $0";
          rst.append(sym("arrow.counterclockwise"));
          rst.onclick = () => resetQuota(user);
          cell.append(rst);
        }
      }
      return cell;
    };
    // editCap swaps the cell for an inline input; Enter/Set saves, Esc cancels.
    const editCap = (cell, user, cap) => {
      cell.innerHTML = "";
      const inp = el("input", "access-capinput"); inp.type = "number"; inp.min = "0"; inp.step = "1";
      inp.value = cap || "";
      inp.title = "Daily cap in USD — blank or 0 reverts to the default " + money(defaultCap);
      const save = el("button", "access-capsave", "Set");
      const commit = () => { const v = parseFloat(inp.value); setCap(user, isNaN(v) || v < 0 ? 0 : v); };
      save.onclick = commit;
      inp.onkeydown = (e) => {
        if (e.key === "Enter") { e.preventDefault(); commit(); }
        else if (e.key === "Escape") { e.preventDefault(); render(lastData); }
      };
      cell.append(inp, save);
      inp.focus(); inp.select();
    };

    // Allowed (granted access) — editable cap + remove.
    const sec = el("div", "access-section");
    sec.append(el("div", "access-head access-allow", defaultCap ? `Allowed — granted access (default ${money(defaultCap)}/day)` : "Allowed — granted access"));
    const items = (data && data.allow) || [];
    if (!items.length) { sec.append(el("div", "access-empty", "— none —")); }
    for (const it of items) {
      const row = el("div", "access-row");
      row.append(el("span", "access-name", it));
      if (byUser[it] && byUser[it].inGroup) row.append(el("span", "access-tag", "in group"));
      if (usage.length || (data && "cap" in data)) row.append(quotaCell(it, byUser[it]));
      const rm = el("button", "access-rm"); rm.title = "Remove"; rm.append(sym("xmark"));
      rm.onclick = () => mutate("DELETE", { user: it });
      row.append(rm);
      sec.append(row);
    }
    list.append(sec);

    // Group members (in MN_AUTH_GROUP_DSID) who've logged in — used quota, editable cap.
    const section = (title, rows) => {
      if (!rows.length) return;
      const s = el("div", "access-section");
      s.append(el("div", "access-head", title));
      for (const u of rows) {
        const row = el("div", "access-row");
        row.append(el("span", "access-name", u.user));
        row.append(quotaCell(u.user, u));
        s.append(row);
      }
      list.append(s);
    };
    section("Group members — used quota today", usage.filter((u) => u.inGroup && !u.allowed));
    // Anyone else who's drawn from the quota (e.g. a connector's machine user) or
    // has a custom cap but isn't allow-listed/in-group — so nothing is hidden.
    section("Other — used quota / custom cap", usage.filter((u) => !u.inGroup && !u.allowed));
  };
  // setCap posts a per-user daily cap override (0/blank reverts to the default).
  const setCap = async (user, cap) => {
    try {
      const r = await fetch("/api/members/cap", { method: "POST", body: JSON.stringify({ user, cap }) });
      if (r.ok) render(await r.json());
      else if (r.status === 403 || r.status === 401) alertBox("Members is admin-only.");
      else if (r.status === 404) alertBox("Per-user caps aren't available on this relay.");
    } catch (_) { render(lastData); }
  };
  // resetQuota zeroes a user's committed daily spend (admin "reset used quota").
  const resetQuota = async (user) => {
    try {
      const r = await fetch("/api/members/reset", { method: "POST", body: JSON.stringify({ user }) });
      if (r.ok) render(await r.json());
      else if (r.status === 403 || r.status === 401) alertBox("Members is admin-only.");
      else if (r.status === 404) alertBox("Quota reset isn't available on this relay.");
    } catch (_) { render(lastData); }
  };
  const mutate = async (method, payload) => {
    try {
      // /api/members is RELAY-GLOBAL (the admin allow-list + quota live on the
      // relay, not per-connector), so always hit the root — never the per-laptop
      // apiBase, which would route to a connector's local (empty) list. This makes
      // the panel work the same whether you're viewing the live terminal session
      // or any store-backed one.
      const r = await fetch("/api/members", { method, body: JSON.stringify(payload) });
      if (r.ok) render(await r.json());
      else if (r.status === 403 || r.status === 401) alertBox("Members is admin-only.");
    } catch (_) { /* leave the list as-is on a transient failure */ }
  };
  const add = () => {
    const v = input.value.trim();
    if (!v) return;
    input.value = "";
    mutate("POST", { user: v });
  };
  addBtn.onclick = add;
  input.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); add(); } };

  try {
    const r = await fetch("/api/members"); // relay-global; root, not apiBase (see mutate)
    if (r.ok) render(await r.json());
  } catch (_) {}
}

// renderUsagePanel is the team-wide usage dashboard: one row per user (total
// cost + tokens across every model they've used), each expandable into its
// per-model breakdown. Backed by /api/usage, which is relay-only (provider
// metering is the only place a request can be attributed to a user) — reports
// {available:false} everywhere else, rendered as a simple explanatory note.
async function renderUsagePanel(body) {
  if (!body) return;
  body.innerHTML = "";
  body.appendChild(el("div", "access-intro",
    "Token usage and cost per user, broken down by model. Tracked by the relay's " +
    "per-user metering (provider access). Pick a window below — \"All time\" never resets; " +
    "the others cover the last N UTC calendar days."));

  const bar = el("div", "access-row", "");
  bar.style.padding = "0 0 .4rem";
  bar.append(el("span", null, "Window:"));
  const sel = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
  for (const [value, label] of [["all", "All time"], ["30d", "Last 30 days"], ["7d", "Last 7 days"], ["today", "Today"]]) {
    const opt = document.createElement("option");
    opt.value = value; opt.textContent = label;
    sel.append(opt);
  }
  sel.value = usagePanelRange || "all";
  sel.onchange = () => { usagePanelRange = sel.value; renderUsagePanel(body); };
  bar.append(sel);
  body.append(bar);

  const list = el("div", null);
  body.append(list);
  list.textContent = "Loading…";

  let data;
  try {
    const r = await fetch("/api/usage?range=" + encodeURIComponent(sel.value)); // relay-global; root, not apiBase
    data = r.ok ? await r.json() : null;
  } catch (_) { data = null; }

  list.innerHTML = "";
  if (!data || !data.available) {
    list.appendChild(el("div", "access-empty", "Usage tracking isn't available here (requires the relay's per-user provider metering)."));
    return;
  }
  const rows = data.rows || [];
  if (!rows.length) {
    list.appendChild(el("div", "access-empty", "No usage recorded in this window."));
    return;
  }
  const fmtTok = (n) => (n >= 1000 ? (n / 1000).toFixed(1) + "k" : String(n));
  const fmtCost = (n) => { n = Number(n) || 0; return "$" + (n < 1 ? n.toFixed(4) : n.toFixed(2)); };
  for (const row of rows) {
    const r = el("div", "access-row", "");
    r.style.flexDirection = "column";
    r.style.alignItems = "stretch";
    const head = el("div", "access-row", "");
    head.style.padding = "0";
    const nameEl = el("span", null, row.user || "(unknown)");
    nameEl.style.fontWeight = "600";
    head.append(nameEl);
    const spacer = el("span", null, "");
    spacer.style.flex = "1";
    head.append(spacer);
    head.append(el("span", "access-quota", fmtTok(row.total_in_tokens) + " in / " + fmtTok(row.total_out_tokens) + " out"));
    head.append(el("span", "access-quota-custom", fmtCost(row.total_cost_usd)));
    r.append(head);
    for (const m of row.models || []) {
      const mr = el("div", "access-row", "");
      mr.style.padding = "0 0 0 1rem";
      mr.append(el("span", "access-quota", m.model + " · " + m.calls + " call" + (m.calls === 1 ? "" : "s")));
      const mspacer = el("span", null, "");
      mspacer.style.flex = "1";
      mr.append(mspacer);
      mr.append(el("span", "access-quota", fmtTok(m.in_tokens) + " in / " + fmtTok(m.out_tokens) + " out"));
      mr.append(el("span", "access-quota", fmtCost(m.cost_usd)));
      r.append(mr);
    }
    list.append(r);
  }
}

// A simple marketplace over the connected terminal's language servers: install
// from a curated catalog (registers in lsp.json and, when the binary is missing,
// runs the install command on that machine), enable/disable, and remove. State
// is fetched from /api/sessions/{id}/lsp/catalog.

let lspPollTimer = null;
const lspPending = new Set(); // names with an install in flight (poll until done)

function openPersona() {
  if (!current) { alertBox("Connect to a terminal first — these settings apply to that machine."); return; }
  setPanel("Persona — environment", "person.text.rectangle");
  renderPersona();
}

// Persona panel: view/edit the connected terminal's runtime env/config. Secrets
// show only a last-4 hint (the full token never leaves the server); leaving a
// secret blank keeps the existing value. Edits apply immediately and reset to the
// deployed env on restart.
async function renderPersona() {
  const body = manageBody;
  body.innerHTML = "";
  body.append(el("p", "access-intro", "Runtime environment for the connected terminal. Changes apply immediately. Provider API keys, base URLs and the credential env vars your MCP servers / skills / agents declare are saved to ~/.config/moony/.env, and settings with a saved slot (like auto-plan) persist; other connection/limit overrides reset to the deployed config on restart. Secrets show only their last 4 characters — type a new value to replace, or leave blank to keep the current one."));
  let fields = [];
  try { const r = await fetch(api(`/api/sessions/${current}/persona`)); if (r.ok) fields = (await r.json()).fields || []; } catch (_) {}
  const form = el("div", "persona-form");
  const readers = {}; // key -> () => current input value
  const status = el("span", "persona-status");
  // persist saves all fields, then re-renders from the server response. Shared by
  // the Save button and the provider selects — changing the Fast/Self-review
  // provider must refetch that provider's model catalogue into the model dropdown.
  // Names the user asked to delete this round. Collected rather than sent immediately so
  // one Save applies edits and removals together, and so a mis-click can be undone by
  // closing the panel instead of having already destroyed a value.
  const pendingRemovals = new Set();

  const persist = async (extra) => {
    const payload = {};
    for (const f of fields) {
      // A field queued for removal must not also submit its (masked) value back.
      if (f.custom && pendingRemovals.has(f.label)) continue;
      const v = readers[f.key] ? readers[f.key]() : undefined;
      if (f.type === "secret") { if (v) payload[f.key] = v; } // blank secret => keep existing
      else payload[f.key] = v;
    }
    if (pendingRemovals.size) payload.envVarRemove = [...pendingRemovals];
    Object.assign(payload, extra || {});
    status.textContent = "Saving…";
    try {
      const r = await fetch(api(`/api/sessions/${current}/persona`), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      // Surface the server's reason: a rejected payload saves NOTHING, so a bare
      // status code leaves the user thinking a provider key was stored when it wasn't.
      if (r.ok) status.textContent = "Saved — applied to this terminal.";
      else {
        let why = "";
        try { why = (await r.text()).trim(); } catch (_) {}
        status.textContent = `Save failed (${r.status})${why ? ": " + why.slice(0, 200) : "."}`;
      }
      if (r.ok) renderPersona();
    } catch (_) { status.textContent = "Save failed."; }
  };
  const makeField = (f) => {
    const field = el("div", "persona-field");
    // "toggle" is an alias for "bool": a connector older/newer than this relay's JS
    // may send either, and rendering a boolean as text posts "true"/"false" as a
    // string, which fails the server's json.Unmarshal and 400s the entire save.
    if (f.type === "bool" || f.type === "toggle") {
      // Checkbox before the label: "[✓] Auto-delegate subtasks".
      const cb = el("input", null); cb.type = "checkbox"; cb.checked = !!f.value;
      const row = el("label", "persona-toggle"); row.append(cb, el("span", "persona-label", f.label));
      field.append(row);
      if (f.desc) field.append(el("div", "persona-desc", f.desc));
      readers[f.key] = () => cb.checked;
    } else if (f.type === "select") {
      const row = el("div", "persona-toggle");
      row.append(el("span", "persona-label", f.label));
      const sel = el("select", "persona-select");
      for (const opt of (f.options || [])) {
        const o = document.createElement("option");
        o.value = opt.value; o.textContent = opt.label;
        if (opt.value === String(f.value ?? "")) o.selected = true;
        sel.append(o);
      }
      row.append(sel);
      field.append(row);
      if (f.desc) field.append(el("div", "persona-desc", f.desc));
      readers[f.key] = () => sel.value;
      // Changing a tier provider refetches its model catalogue into the model
      // dropdown, so persist + re-render on change (the model list is server-side).
      if (f.key === "fastProvider" || f.key === "auxProvider") sel.onchange = () => { persist(); };
    } else {
      if (f.custom) {
        // User-added var: label plus a remove control, since nothing else in the UI can
        // take a hand-typed name back out of the dotenv.
        const head = el("div", "persona-row persona-custom-head");
        head.append(el("div", "persona-label", f.label));
        const rm = el("button", "persona-reveal"); rm.type = "button";
        const paint = () => {
          const queued = pendingRemovals.has(f.label);
          rm.textContent = queued ? "undo" : "remove";
          field.classList.toggle("persona-removing", queued);
        };
        rm.onclick = () => {
          if (pendingRemovals.has(f.label)) pendingRemovals.delete(f.label);
          else pendingRemovals.add(f.label);
          paint();
        };
        paint();
        head.append(rm);
        field.append(head);
      } else {
        field.append(el("div", "persona-label", f.label));
      }
      if (f.desc) field.append(el("div", "persona-desc", f.desc));
      if (f.type === "secret") {
        const inp = el("input", "access-input persona-input"); inp.type = "password";
        inp.placeholder = f.set ? `•••• ${f.hint} (set — type to replace)` : "(not set)";
        const reveal = el("button", "persona-reveal"); reveal.type = "button"; reveal.textContent = "show";
        reveal.onclick = () => { const masked = inp.type === "password"; inp.type = masked ? "text" : "password"; reveal.textContent = masked ? "hide" : "show"; };
        const row = el("div", "persona-row"); row.append(inp, reveal);
        field.append(row);
        readers[f.key] = () => inp.value.trim(); // blank => server keeps existing
      } else {
        const inp = el("input", "access-input persona-input"); inp.type = "text"; inp.value = f.value == null ? "" : String(f.value);
        if (f.placeholder) inp.placeholder = `default: ${f.placeholder}`;
        field.append(inp);
        readers[f.key] = () => inp.value.trim();
      }
    }
    return field;
  };
  // Toggles + selects (auto-delegate / auto-plan / cheap-reads / fast-model) pinned
  // to the top; the rest of the env vars go into a scrollable region so a long
  // token list doesn't grow the panel.
  // Fast model and Self-review model each have 3 fields (provider, model list,
  // reasoning level); group them side by side in one 2-column row instead of
  // interleaving all 6 with the other toggles, so the two tiers read as
  // distinct, comparable blocks.
  const tierKeys = {
    fast: ["fastProvider", "sqeFastModel", "fastReasoning"],
    aux: ["auxProvider", "sqeAuxModel", "auxReasoning"],
  };
  const tierKeySet = new Set([...tierKeys.fast, ...tierKeys.aux]);
  const byKey = Object.fromEntries(fields.map((f) => [f.key, f]));
  const isBool = (f) => f.type === "bool" || f.type === "toggle"; // "toggle": legacy alias for "bool"
  const toggles = fields.filter((f) => (isBool(f) || f.type === "select") && !tierKeySet.has(f.key));
  const rest = fields.filter((f) => !isBool(f) && f.type !== "select");
  for (const f of toggles) form.append(makeField(f));
  // The column heading already names the tier ("Fast model" / "Self-review
  // model"), so inside a column each field only needs its role — "Provider",
  // "Model", "Reasoning" — rather than repeating the tier name and squeezing a
  // long label next to the control. The MN_* env var stays discoverable as a
  // hover title, and each field's Desc below still spells it out in full.
  const tierFieldRole = { fastProvider: "Provider", sqeFastModel: "Model", fastReasoning: "Reasoning", auxProvider: "Provider", sqeAuxModel: "Model", auxReasoning: "Reasoning" };
  const tierCol = (heading, keys) => {
    const present = keys.map((k) => byKey[k]).filter(Boolean);
    if (!present.length) return null;
    const col = el("div", "persona-tier-col");
    col.append(el("div", "persona-tier-heading", heading));
    for (const f of present) {
      const role = tierFieldRole[f.key];
      const node = makeField(role ? Object.assign({}, f, { label: role }) : f);
      if (role) {
        const env = (String(f.label).match(/MN_[A-Z_]+/) || [])[0];
        const lbl = node.querySelector(".persona-label");
        if (lbl && env) lbl.title = env;
      }
      col.append(node);
    }
    return col;
  };
  const fastCol = tierCol("Fast model", tierKeys.fast);
  const auxCol = tierCol("Self-review model", tierKeys.aux);
  if (fastCol || auxCol) {
    const grid = el("div", "persona-tier-grid");
    if (fastCol) grid.append(fastCol);
    if (auxCol) grid.append(auxCol);
    form.append(grid);
  }
  if (rest.length) {
    // Ungrouped fields (session encryption, MCP tokens) stay in the first scroll;
    // provider credentials (grouped) go in a second "Provider" scroll below it.
    const scroll = el("div", "persona-scroll");
    const provScroll = el("div", "persona-scroll");
    const seen = new Set();
    for (const f of rest) {
      if (!f.group) { scroll.append(makeField(f)); continue; }
      if (seen.has(f.group)) continue;
      seen.add(f.group);
      const members = rest.filter((g) => g.group === f.group);
      const keyField = members.find((g) => g.type === "secret") || members[0];
      const isSet = !!(keyField && keyField.set);

      const card = el("div", "persona-provider" + (isSet ? " is-set" : ""));
      const head = el("button", "persona-provider-head"); head.type = "button";
      head.setAttribute("aria-expanded", "false");
      const dot = el("span", "persona-dot" + (isSet ? " on" : ""));
      const name = el("span", "persona-provider-name", f.group);
      const state = el("span", "persona-provider-state", isSet ? (keyField.hint ? "•••• " + keyField.hint : "set") : "not set");
      const chev = sym("chevron.right"); chev.classList.add("persona-provider-chev");
      head.append(dot, name, state, chev);

      const bodyEl = el("div", "persona-provider-body");
      for (const g of members) bodyEl.append(makeField(g));
      // Visibility is class-driven (.open shows the body via CSS). Cards start
      // COLLAPSED — the header dot + set/•••• state show config at a glance;
      // click a header to expand its API-key / base-URL inputs.
      head.onclick = () => {
        const open = card.classList.toggle("open");
        head.setAttribute("aria-expanded", open ? "true" : "false");
      };

      card.append(head, bodyEl);
      provScroll.append(card);
    }
    if (scroll.children.length) form.append(scroll);
    if (provScroll.children.length) {
      form.append(el("div", "persona-section-title", "Provider"));
      form.append(provScroll);
    }
  }
  body.append(form);

  // Add an env var the panel didn't discover — a provider or tool that needs a variable
  // no MCP server, skill or agent declares. Saved to the dotenv like any other, and it
  // comes back as a removable field on the next open (see customEnvFields).
  const addWrap = el("div", "persona-addenv");
  addWrap.append(el("div", "persona-section-title", "Add environment variable"));
  const addName = el("input", "access-input persona-input");
  addName.placeholder = "NAME (e.g. FOO_API_KEY)";
  addName.spellcheck = false;
  addName.autocapitalize = "characters";
  const addVal = el("input", "access-input persona-input"); addVal.type = "password";
  addVal.placeholder = "value";
  const addBtn = el("button", "persona-reveal", "Add");
  addBtn.type = "button";
  const addRow = el("div", "persona-row"); addRow.append(addName, addVal, addBtn);
  addWrap.append(addRow);
  addWrap.append(el("div", "persona-desc",
    "Applied live and saved to ~/.config/moony/.env. Names are UPPER_SNAKE_CASE; a few the agent depends on (PATH, HOME, the loader hooks) are refused."));
  const doAdd = async () => {
    const name = addName.value.trim().toUpperCase();
    const val = addVal.value;
    // Validate here as well as on the server: a bad name would otherwise be dropped
    // silently by the server's gate, which reads as "Saved" with nothing added.
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) { status.textContent = "Not a valid variable name."; addName.focus(); return; }
    if (!val.trim()) { status.textContent = "Give the variable a value."; addVal.focus(); return; }
    addBtn.disabled = true;
    try { await persist({ ["envVar:" + name]: val }); } finally { addBtn.disabled = false; }
  };
  addBtn.onclick = doAdd;
  addVal.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); doAdd(); } };
  body.append(addWrap);

  const save = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Save");
  save.onclick = () => persist();
  const actions = el("div", "persona-actions"); actions.append(save, status);
  body.append(actions);
}

function stopLspPoll() { if (lspPollTimer) { clearInterval(lspPollTimer); lspPollTimer = null; } lspPending.clear(); }

async function renderLspMarketplace() {
  const body = manageBody;
  let items = [];
  try { const r = await fetch(api(`/api/sessions/${current}/lsp/catalog`)); if (r.ok) items = await r.json(); } catch (_) {}
  items = items || [];
  // A pending install is done once it shows installed + on PATH.
  for (const it of items) if (it.installed && it.onPath) lspPending.delete(it.name);
  if (!lspPending.size) stopLspPoll();

  body.innerHTML = "";
  body.append(el("p", "access-intro", "Install language servers for inline diagnostics on this terminal. Installing registers the server and, when its binary isn't found, runs the install command on the machine. Toggle to enable/disable."));

  const installed = items.filter((i) => i.installed);
  const available = items.filter((i) => !i.installed);

  body.append(lspSectionHead("Installed"));
  if (!installed.length) {
    body.append(el("div", "access-empty", "Nothing installed yet — pick one from the marketplace below."));
  } else {
    const group = el("div", "lsp-installed");
    for (const it of installed) group.append(lspInstalledRow(it));
    body.append(group);
  }

  body.append(lspSectionHead("Marketplace"));
  for (const it of available) body.append(lspAvailableRow(it));
}

function lspSectionHead(text) {
  const h = el("div", "msg-label text-faint mt-3 mb-1.5");
  h.textContent = text;
  return h;
}

// lspBadge summarizes a server's live state.
function lspBadge(it) {
  if (!it.installed) return it.onPath ? el("span", "shrink-0 text-[11px] text-ok", "binary found") : null;
  if (it.running) return el("span", "shrink-0 text-[11px] text-ok", "running");
  if (!it.onPath) { const b = el("span", "shrink-0 text-[11px] text-warn", "binary missing"); b.title = "Enabled, but the binary isn't on PATH — install it."; return b; }
  const b = el("span", "shrink-0 text-[11px] text-faint", "idle"); b.title = "enabled but not running — try Reload (toggle off/on)"; return b;
}

function lspInstalledRow(it) {
  const desc = (it.language ? it.language + " · " : "") + (it.extensions || []).join(" ");
  return manageRow(it.name, desc, it.enabled,
    () => lspDo("toggle", { name: it.name, enabled: !it.enabled }),
    () => { confirmBox(`Remove language server "${it.name}" from lsp.json?`, { title: "Remove language server", okText: "Remove", danger: true }).then((ok) => { if (ok) lspDo("remove", { name: it.name }); }); },
    lspBadge(it));
}

function lspAvailableRow(it) {
  const row = el("div", "manage-row");
  const main = el("div", "manage-row-main");
  main.appendChild(el("div", "text-[13px] truncate text-ink", it.name + (it.language ? "  ·  " + it.language : "")));
  const sub = it.desc || (it.extensions || []).join(" ");
  if (sub) main.appendChild(el("div", "text-[11px] text-faint truncate", sub));
  // When the binary is missing, surface the command that will run.
  if (!it.onPath && it.install) { const c = el("div", "text-[11px] text-faint truncate font-mono", "will run: " + it.install); c.title = it.install; main.appendChild(c); }
  row.appendChild(main);
  const badge = lspBadge(it);
  if (badge) row.appendChild(badge);
  const btn = el("button", "manage-install");
  if (lspPending.has(it.name)) { btn.textContent = "Installing…"; btn.disabled = true; }
  else { btn.textContent = it.onPath ? "Add" : "Install"; btn.onclick = () => lspInstall(it); }
  row.appendChild(btn);
  return row;
}

async function lspInstall(it) {
  // If the binary must be fetched, mark pending and poll until it lands.
  if (!it.onPath) { lspPending.add(it.name); if (!lspPollTimer) lspPollTimer = setInterval(renderLspMarketplace, 3000); }
  try {
    const r = await fetch(api(`/api/sessions/${current}/lsp/install`), { method: "POST", body: JSON.stringify({ name: it.name }) });
    if (!r.ok) { lspPending.delete(it.name); alertBox((await r.text().catch(() => "")).trim() || "Install failed."); }
  } catch (_) { lspPending.delete(it.name); }
  renderLspMarketplace();
}

async function lspDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/lsp/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderLspMarketplace();
}

// --- Skills marketplace panel -----------------------------------------------
// Install Agent Skills from a curated catalog (writes a SKILL.md to the global
// ~/.claude/skills), and enable/disable or remove the discovered ones. Mirrors
// the LSP panel; installing a skill is just a file write, so it's instant.

// --- Git modal (read-only, Phase 1) -----------------------------------------
// Opened by clicking the project/git token in the header. Shows an overview
// (branch · upstream · ahead/behind), the working-tree changes grouped by
// staged/unstaged/untracked, and recent commits — each clickable to view its
// diff (reusing renderDiff). All read-only; no staging/commit/push here.
async function openGitModal() {
  if (!current) { alertBox("Open a session first to view git."); return; }
  setPanel("Git", "arrow.triangle.branch");
  setManageWidth("min(980px,96vw)"); // wider card for the two-column git layout
  await renderGitModal();
}

let tfindTimer = null;
let tfindMarks = [];
let tfindIndex = -1;

function initTranscriptSearch() {
  const fbox = document.getElementById("transcript-find");
  const finp = document.getElementById("transcript-find-input");
  const fcnt = document.getElementById("transcript-find-count");
  const fprev = document.getElementById("transcript-find-prev");
  const fnext = document.getElementById("transcript-find-next");
  const fclose = document.getElementById("transcript-find-close");

  if (!fbox || !finp) return;

  const toggleFind = () => {
    if (fbox.classList.contains("hidden")) {
      fbox.classList.remove("hidden");
      finp.focus();
      finp.select();
      performFind(finp.value);
    } else {
      fbox.classList.add("hidden");
      clearFindHighlights();
      input.focus();
    }
  };

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "f") {
      e.preventDefault();
      toggleFind();
    }
    if (e.key === "Escape" && !fbox.classList.contains("hidden")) {
      e.preventDefault();
      toggleFind();
    }
  });

  finp.oninput = () => {
    if (tfindTimer) clearTimeout(tfindTimer);
    tfindTimer = setTimeout(() => performFind(finp.value), 150);
  };

  finp.onkeydown = (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      if (e.shiftKey) jumpToFindMatch(-1);
      else jumpToFindMatch(1);
    }
  };

  fprev.onclick = () => jumpToFindMatch(-1);
  fnext.onclick = () => jumpToFindMatch(1);
  fclose.onclick = () => toggleFind();
}

function clearFindHighlights() {
  const t = document.getElementById("transcript");
  if (!t) return;
  // Replace mark elements with their original text content
  const marks = t.querySelectorAll("mark.tsearch-hit");
  for (const m of marks) {
    m.replaceWith(document.createTextNode(m.textContent));
  }
  t.normalize(); // merge adjacent text nodes
  tfindMarks = [];
  tfindIndex = -1;
  const fcnt = document.getElementById("transcript-find-count");
  if (fcnt) fcnt.textContent = "";
}

function performFind(q) {
  clearFindHighlights();
  q = (q || "").trim();
  if (q.length < 2) return;

  const t = document.getElementById("transcript");
  if (!t) return;

  wrapTextInHighlights(t, new RegExp("(" + q.replace(/[-\/\\^$*+?.()|[\]{}]/g, "\\$&") + ")", "gi"));

  tfindMarks = Array.from(t.querySelectorAll("mark.tsearch-hit"));
  const fcnt = document.getElementById("transcript-find-count");
  if (!tfindMarks.length) {
    if (fcnt) fcnt.textContent = "0/0";
    return;
  }

  tfindIndex = 0;
  jumpToFindMatch(0);
}

function wrapTextInHighlights(node, rx) {
  if (node.nodeType === Node.TEXT_NODE) {
    if (node.parentNode && node.parentNode.tagName === "MARK") return;
    const val = node.nodeValue;
    const parts = val.split(rx);
    if (parts.length > 1) {
      const frag = document.createDocumentFragment();
      for (let i = 0; i < parts.length; i++) {
        if (i % 2 === 1) {
          const m = document.createElement("mark");
          m.className = "tsearch-hit";
          m.textContent = parts[i];
          frag.appendChild(m);
        } else if (parts[i]) {
          frag.appendChild(document.createTextNode(parts[i]));
        }
      }
      node.replaceWith(frag);
    }
  } else if (node.nodeType === Node.ELEMENT_NODE) {
    // Avoid script/style tags or inputs
    if (node.tagName !== "SCRIPT" && node.tagName !== "STYLE" && node.tagName !== "INPUT" && node.tagName !== "TEXTAREA") {
      // Must use a static array since childNodes changes live during replacements
      const children = Array.from(node.childNodes);
      for (const child of children) {
        wrapTextInHighlights(child, rx);
      }
    }
  }
}

function jumpToFindMatch(dir) {
  if (!tfindMarks.length) return;
  
  // Remove current class from old active mark
  if (tfindIndex >= 0 && tfindIndex < tfindMarks.length) {
    tfindMarks[tfindIndex].classList.remove("tsearch-current");
  }

  tfindIndex = (tfindIndex + dir + tfindMarks.length) % tfindMarks.length;
  const active = tfindMarks[tfindIndex];
  active.classList.add("tsearch-current");

  // Scroll active match into view smoothly
  active.scrollIntoView({ behavior: "smooth", block: "center" });

  const fcnt = document.getElementById("transcript-find-count");
  if (fcnt) {
    fcnt.textContent = `${tfindIndex + 1}/${tfindMarks.length}`;
  }
}

initTranscriptSearch();

/* ── Read-only file viewer ─────────────────────────────────────────────────
   Browses and displays files from the session's working directory via the
   confined /api/sessions/{id}/file(s) endpoints. Read-only on purpose: users
   already have the repo checked out in a real editor, and writing from here
   would bypass the agent's approval + snapshot/undo machinery and could clobber
   a concurrent agent edit. */

let fileViewerPath = "";   // repo-relative dir currently browsed
let fileViewerOpen = "";   // repo-relative file currently shown

// openFileViewer shows the viewer modal, optionally focused on one file.
function openFileViewer(path) {
  if (!current) { alertBox("Open a session first — the viewer reads that session's working directory."); return; }
  fileViewerOpen = path || "";
  // Browse the file's own directory so siblings are one click away.
  fileViewerPath = path && path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
  openTabbedModal("Files", "doc.text.magnifyingglass", [
    { key: "files", label: "Files", icon: "folder", render: () => renderFileViewer() },
  ], "files");
  // After openTabbedModal: it calls setPanel(), which resets the card width.
  setManageWidth("min(1400px,96vw)"); // tree + code side by side needs the room
}

// renderFileViewer paints the split browser/content view into the modal body.
function renderFileViewer() {
  const body = manageBody;
  body.textContent = "";

  const wrap = el("div", "fv-wrap");
  const side = el("div", "fv-side");
  const main = el("div", "fv-main");
  wrap.append(side, main);
  body.append(wrap);

  renderFileList(side, main);
  if (fileViewerOpen) loadFileInto(main, fileViewerOpen);
  else main.append(el("div", "fv-empty", "Select a file to view it."));
}

// renderFileList fills the browser pane for the current directory.
async function renderFileList(side, main) {
  side.textContent = "";
  side.append(el("div", "fv-loading", "Loading…"));
  let tree;
  try {
    const r = await fetch(api(`/api/sessions/${current}/files?path=${encodeURIComponent(fileViewerPath)}`));
    if (!r.ok) throw new Error(await r.text());
    tree = await r.json();
  } catch (e) {
    side.textContent = "";
    side.append(el("div", "fv-error", "Could not list files: " + e.message));
    return;
  }

  side.textContent = "";
  // Breadcrumb: the repo root plus each ancestor, all clickable.
  const crumbs = el("div", "fv-crumbs");
  const rootLink = el("button", "fv-crumb", "/");
  rootLink.title = tree.root || "workspace root";
  rootLink.onclick = () => { fileViewerPath = ""; renderFileList(side, main); };
  crumbs.append(rootLink);
  let walked = "";
  for (const part of (tree.path || "").split("/").filter(Boolean)) {
    walked = walked ? walked + "/" + part : part;
    const at = walked;
    const b = el("button", "fv-crumb", part);
    b.onclick = () => { fileViewerPath = at; renderFileList(side, main); };
    crumbs.append(el("span", "fv-crumb-sep", "/"), b);
  }
  side.append(crumbs);

  const list = el("div", "fv-list");
  if (tree.path) {
    const up = el("button", "fv-row fv-dir", "‥");
    up.onclick = () => { fileViewerPath = tree.parent || ""; renderFileList(side, main); };
    list.append(up);
  }
  for (const e of tree.entries || []) {
    const row = el("button", "fv-row " + (e.dir ? "fv-dir" : "fv-file"));
    row.append(el("span", "fv-ico", e.dir ? "📁" : "📄"), el("span", "fv-name", e.name));
    if (!e.dir && e.size != null) row.append(el("span", "fv-size", humanBytes(e.size)));
    if (e.path === fileViewerOpen) row.classList.add("is-active");
    row.onclick = () => {
      if (e.dir) { fileViewerPath = e.path; renderFileList(side, main); return; }
      fileViewerOpen = e.path;
      for (const r of list.querySelectorAll(".fv-row")) r.classList.remove("is-active");
      row.classList.add("is-active");
      loadFileInto(main, e.path);
    };
    list.append(row);
  }
  if (!(tree.entries || []).length && !tree.path) list.append(el("div", "fv-empty", "This directory is empty."));
  side.append(list);
}

// loadFileInto fetches one file and renders it with line numbers.
async function loadFileInto(main, path) {
  main.textContent = "";
  main.append(el("div", "fv-loading", "Loading " + path + "…"));
  let f;
  try {
    const r = await fetch(api(`/api/sessions/${current}/file?path=${encodeURIComponent(path)}`));
    if (!r.ok) throw new Error((await r.text()) || r.status);
    f = await r.json();
  } catch (e) {
    main.textContent = "";
    main.append(el("div", "fv-error", "Could not open " + path + ": " + e.message));
    return;
  }

  main.textContent = "";
  const head = el("div", "fv-head");
  head.append(el("span", "fv-head-path", f.path));
  const meta = el("span", "fv-head-meta");
  meta.textContent = humanBytes(f.size) + (f.lines ? ` · ${f.lines} lines` : "");
  head.append(meta);
  const copy = el("button", "fv-copy", "Copy");
  copy.onclick = async () => {
    try { await navigator.clipboard.writeText(f.content || ""); copy.textContent = "Copied"; setTimeout(() => (copy.textContent = "Copy"), 1200); }
    catch { copy.textContent = "Copy failed"; }
  };
  if (!f.binary) head.append(copy);
  main.append(head);

  if (f.binary) {
    main.append(el("div", "fv-empty", "Binary file — not shown."));
    return;
  }
  if (f.truncated) {
    main.append(el("div", "fv-warn", `Showing the first ${humanBytes(f.content.length)} of ${humanBytes(f.size)} — the file is too large to display in full.`));
  }

  // Reuse buildCodeView (app-render.js): the same line-numbered gutter +
  // dependency-free tokenizer the tool cards and fenced code blocks use, so the
  // viewer picks up identical GitHub-style colors and theming for free.
  const body = f.content.replace(/\n$/, ""); // trailing newline -> no phantom last line
  const view = el("div", "fv-code");
  view.append(buildCodeView(body, langForPath(f.path)));
  main.append(view);
}

// langForPath maps a filename to the hint buildCodeView understands. The shared
// tokenizer is language-agnostic apart from a bash-aware variant, so this only
// needs to separate shell files from the rest.
function langForPath(path) {
  const name = String(path || "").toLowerCase();
  const base = name.slice(name.lastIndexOf("/") + 1);
  // Dotfiles like ".bashrc" have no extension — the leading dot is not a
  // separator — so match the whole basename for the shell-ish ones first.
  if (/^\.?(bashrc|zshrc|profile|bash_profile|zprofile)$/.test(base)) return "bash";
  if (/^(makefile|dockerfile|containerfile)$/.test(base)) return "bash";
  const dot = base.lastIndexOf(".");
  const ext = dot > 0 ? base.slice(dot + 1) : ""; // dot > 0 skips ".bashrc"
  if (/^(sh|bash|zsh|ksh|command)$/.test(ext)) return "bash";
  return ext;
}

function humanBytes(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

// --- cross-session transcript search ---------------------------------------
// The search box (index.html #searchbox) fans out to THIS machine and every
// other connected machine's /api/search (via the /l/<host>/ proxy) and merges
// the results. Clicking a result opens that session on its machine, reusing
// the normal focusLaptop + selectSession path.
let searchSeq = 0; // ignore out-of-order responses from a stale query

function initSearch() {
  const input = $("#search-input");
  if (!input) return;
  let timer = null;
  input.addEventListener("input", () => {
    clearTimeout(timer);
    timer = setTimeout(() => runSearch(input.value.trim()), 220);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { input.value = ""; runSearch(""); }
  });
}

async function runSearch(q) {
  const results = $("#search-results"), tree = $("#sessions");
  if (!results || !tree) return;
  if (q.length < 2) { // clear → restore the tree
    results.classList.add("hidden"); results.innerHTML = "";
    tree.classList.remove("hidden");
    return;
  }
  const seq = ++searchSeq;
  tree.classList.add("hidden");
  results.classList.remove("hidden");
  results.innerHTML = "";
  results.appendChild(el("div", "srch-note", "Searching…"));

  // Fan out to self + every online machine; tag each hit with its host.
  const hosts = (laptops && laptops.length)
    ? laptops.filter((l) => l.online !== false).map((l) => l.host)
    : [null]; // standalone (no relay tree): just this machine
  const settled = await Promise.all(hosts.map(async (host) => {
    try {
      const base = host == null ? "" : laptopBase(host);
      const r = await _fetch(base + "/api/search?q=" + encodeURIComponent(q));
      if (!r.ok) return [];
      const rows = await r.json();
      return (rows || []).map((m) => ({ ...m, host }));
    } catch (_) { return []; }
  }));
  if (seq !== searchSeq) return; // a newer query superseded this one

  const hits = settled.flat().sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
  renderSearchResults(results, q, hits);
}

function renderSearchResults(box, q, hits) {
  box.innerHTML = "";
  const multiHost = (laptops && laptops.filter((l) => l.online !== false).length > 1);
  box.appendChild(sidebarSectionHead(hits.length ? `Results (${hits.length})` : "Results"));
  if (!hits.length) {
    box.appendChild(el("div", "srch-note", `No transcripts match “${q}”.`));
    return;
  }
  for (const h of hits) {
    const row = el("div", "srch-hit");
    const head = el("div", "srch-hit-head");
    head.appendChild(el("span", "srch-hit-title", h.title || "(untitled session)"));
    if (h.role && h.role !== "title") head.appendChild(el("span", "srch-hit-tag", h.role));
    row.appendChild(head);
    const meta = (multiHost && h.host ? h.host + " · " : "") + shortDir(h.dir);
    if (meta) row.appendChild(el("div", "srch-hit-sub", meta));
    if (h.snippet) row.appendChild(el("div", "srch-hit-sub", h.snippet));
    row.onclick = () => openSearchHit(h);
    box.appendChild(row);
  }
}

// shortDir trims a working dir to its last two path segments for compact display.
function shortDir(dir) {
  if (!dir) return "";
  const parts = dir.split("/").filter(Boolean);
  return (parts.length > 2 ? "…/" : "") + parts.slice(-2).join("/");
}

async function openSearchHit(h) {
  // Restore the tree view, then open the session on its machine — mirrors the
  // tree's own session-open path (focusLaptop + currentProject + selectSession).
  const input = $("#search-input"); if (input) input.value = "";
  runSearch("");
  if (h.host != null) {
    await toggleLaptop(h.host, true);
    await toggleProject(h.host, h.projectKey, true);
    focusLaptop(h.host);
  }
  currentProject = h.projectKey;
  selectSession(h.sessionId);
}

// --- capped replay banner ----------------------------------------------------
// Opening a session replays only its most recent turns (defaultReplayTurns on the
// server) so a long transcript doesn't stream and render in full. When turns were
// withheld the server sends a `history_start` event and we show a banner that
// reloads the session with ?turns=all. A full reload (rather than splicing older
// turns into the live DOM) keeps this correct with no second rendering path to
// drift out of sync with the streaming one.
function showLoadEarlierBanner(dropped) {
  if (!loadEarlierEl || !dropped) return;
  loadEarlierEl.hidden = false;
  loadEarlierEl.innerHTML = "";
  const label = el("span", "le-label",
    dropped === 1 ? "1 earlier turn wasn't loaded" : `${dropped} earlier turns weren't loaded`);
  const btn = el("button", "le-btn", "Load full history");
  btn.onclick = () => {
    if (!current) return;
    btn.disabled = true;
    btn.textContent = "Loading…";
    selectSession(current, "all"); // reopens the stream; hideLoadEarlier() clears this banner
  };
  loadEarlierEl.append(label, btn);
}

function hideLoadEarlier() {
  if (!loadEarlierEl) return;
  loadEarlierEl.hidden = true;
  loadEarlierEl.innerHTML = "";
}
