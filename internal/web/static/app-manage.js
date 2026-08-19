// app-manage.js — the manage panel and its sub-UIs (git, memory, skills, mcp, agents,
// plugins, schedules, workers, access). Shares global scope; loads last.

async function renderGitModal(notice) {
  if (!current) return;
  const body = manageBody;
  body.innerHTML = "";
  let ov = null;
  try { const r = await fetch(api(`/api/sessions/${current}/git`)); if (r.ok) ov = await r.json(); } catch (_) {}
  if (!ov || !ov.isRepo) {
    body.append(el("div", "access-empty", ov ? "This folder isn't a git repository." : "Couldn't read git status."));
    return;
  }

  // Two columns: the left holds the working state (head, change groups, commit
  // box, diff); the right is the recent-commits history. Collapses to one column
  // on narrow screens (see .git-grid in the CSS).
  const grid = el("div", "git-grid");
  const main = el("div", "git-col git-col-main");
  const side = el("div", "git-col git-col-side");
  grid.append(main, side);
  body.append(grid);

  // Overview line.
  const head = el("div", "git-head");
  head.appendChild(sym("arrow.triangle.branch", "text-faint"));
  head.appendChild(el("span", "git-branch", ov.branch || "(detached)"));
  if (ov.upstream) head.appendChild(el("span", "git-up", "→ " + ov.upstream));
  if (ov.ahead) head.appendChild(el("span", "git-ahead", "↑" + ov.ahead));
  if (ov.behind) head.appendChild(el("span", "git-behind", "↓" + ov.behind));
  const nch = (ov.changes || []).length;
  head.appendChild(el("span", "git-clean " + (nch ? "text-warn" : "text-ok"), nch ? nch + " changed" : "clean"));
  // Push (Phase 3): publish commits to the remote, behind a confirm. Only when a
  // remote is configured.
  if (ov.hasRemote) {
    const push = el("button", "git-push", ov.ahead ? "Push ↑" + ov.ahead : "Push");
    push.title = ov.behind ? ov.behind + " behind upstream — pull first if the push is rejected" : "Push to " + (ov.upstream || "the remote");
    push.onclick = () => gitPushDo(ov, push);
    head.appendChild(push);
  }
  main.append(head);

  // A reusable diff viewer pane the file/commit rows write into. After a
  // commit/push it shows that action's result line until the next selection.
  const diffPane = el("pre", "git-diff");
  diffPane.textContent = notice || "Select a file or commit to view its diff.";
  const showDiff = async (url, emptyMsg) => {
    diffPane.textContent = "Loading…";
    try {
      const r = await fetch(url);
      if (!r.ok) { diffPane.textContent = (await r.text().catch(() => "")).trim() || "Failed to load diff."; return; }
      const d = (await r.json()).diff || "";
      if (!d.trim()) { diffPane.textContent = emptyMsg || "(no diff)"; return; }
      renderDiff(diffPane, d);
    } catch (_) { diffPane.textContent = "Failed to load diff."; }
  };

  // Working-tree changes, grouped (left column).
  const changes = ov.changes || [];
  const anyStaged = changes.some((c) => c.staged && c.status !== "??");
  if (changes.length) {
    const staged = changes.filter((c) => c.staged && c.status !== "??");
    const unstaged = changes.filter((c) => c.unstaged && c.status !== "??");
    const untracked = changes.filter((c) => c.status === "??");
    const group = (title, list, kind) => {
      if (!list.length) return;
      main.append(lspSectionHead(title));
      for (const c of list) main.append(gitFileRow(c, kind, showDiff));
    };
    group("Staged", staged, "staged");
    group("Unstaged", unstaged, "unstaged");
    group("Untracked", untracked, "untracked");
  } else {
    main.append(lspSectionHead("Changes"));
    main.append(el("div", "access-empty", "Working tree clean."));
  }

  // Commit composer (Phase 3): a message box + Commit, behind a confirm. Enabled
  // when there's something to commit; "Stage all & commit" handles the common case
  // of committing tracked edits without staging each one first.
  if (changes.length) main.append(gitCommitBox(ov, anyStaged));

  // The diff viewer sits at the bottom of the left column.
  main.append(diffPane);

  // Recent commits (right column).
  side.append(lspSectionHead("Recent commits"));
  const commits = ov.commits || [];
  if (!commits.length) {
    side.append(el("div", "access-empty", "No commits yet."));
  } else {
    for (const c of commits) side.append(gitCommitRow(c, diffPane, showDiff));
  }
}

// gitFileRow renders one changed file: status badge + path (click → its diff) +
// stage/unstage/discard actions (Phase 2). `group` is "staged" | "unstaged" |
// "untracked" and decides which actions apply.
function gitFileRow(c, group, showDiff) {
  const row = el("div", "git-row");
  const badge = el("span", "git-badge " + gitBadgeClass(c), gitBadgeText(c));
  row.appendChild(badge);
  const name = el("span", "git-path truncate", c.path);
  name.title = c.label + " — " + c.path + " (click to view diff)";
  name.onclick = () => {
    const q = new URLSearchParams({ path: c.path });
    if (group === "staged") q.set("staged", "1");
    showDiff(api(`/api/sessions/${current}/git/diff?` + q.toString()), "(no textual diff — binary or mode change)");
  };
  row.appendChild(name);

  const actions = el("div", "git-actions");
  const act = (label, cls, action, confirmMsg) => {
    const b = el("button", "git-act " + cls, label);
    b.onclick = async (e) => {
      e.stopPropagation();
      if (confirmMsg && !(await confirmBox(confirmMsg, { title: "Confirm", okText: "Yes" }))) return;
      gitWrite(action, c.path);
    };
    actions.appendChild(b);
  };
  if (group === "staged") {
    act("Unstage", "", "unstage");
  } else {
    act("Stage", "", "stage");
    act("Discard", "is-danger", "discard",
      c.status === "??"
        ? `Delete untracked file "${c.path}"? This cannot be undone.`
        : `Discard all changes to "${c.path}"? This cannot be undone.`);
  }
  row.appendChild(actions);
  return row;
}

// gitWrite performs a stage/unstage/discard, then re-renders the modal. The
// server re-emits status so the header dirty count + sidebar refresh too.
async function gitWrite(action, path) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/git/${action}`), { method: "POST", body: JSON.stringify({ path }) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderGitModal();
}

// gitCommitBox renders the Phase 3 commit composer: a message textarea plus a
// Commit button. With staged changes it commits the index; otherwise it offers
// "Stage all & commit" (git commit -a) so tracked edits can be committed in one
// step. anyStaged decides the wording and whether to send all=true.
function gitCommitBox(ov, anyStaged) {
  const box = el("div", "git-commit-box");
  const ta = el("textarea", "git-commit-msg");
  ta.rows = 2; ta.spellcheck = true;
  ta.placeholder = anyStaged ? "Commit message for staged changes…" : "Commit message (stages all tracked changes)…";
  box.appendChild(ta);

  const row = el("div", "git-commit-actions");
  const hint = el("span", "git-commit-hint text-faint",
    anyStaged ? "Commits staged changes only." : "Nothing staged — will stage all tracked edits.");
  row.appendChild(hint);

  const btn = el("button", "manage-install", anyStaged ? "Commit" : "Stage all & commit");
  const submit = async () => {
    const msg = ta.value.trim();
    if (!msg) { ta.focus(); return; }
    const what = anyStaged ? "Commit the staged changes?" : "Stage all tracked changes and commit them?";
    if (!(await confirmBox(what + "\n\n" + msg, { title: "Commit", okText: "Commit" }))) return;
    gitCommitDo(msg, !anyStaged, btn);
  };
  btn.onclick = submit;
  // Cmd/Ctrl+Enter commits from the textarea.
  ta.onkeydown = (e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") { e.preventDefault(); submit(); } };
  row.appendChild(btn);
  box.appendChild(row);
  return box;
}

// gitCommitDo POSTs a commit, then re-renders with the result shown in the diff
// pane. all=true does `git commit -a` (stage tracked edits first).
async function gitCommitDo(message, all, btn) {
  if (btn) { btn.disabled = true; btn.textContent = "Committing…"; }
  let notice = "";
  try {
    const r = await fetch(api(`/api/sessions/${current}/git/commit`), { method: "POST", body: JSON.stringify({ message, all }) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Commit failed."); }
    else { notice = "✓ " + ((await r.json().catch(() => ({}))).result || "Committed."); }
  } catch (_) { alertBox("Commit failed."); }
  renderGitModal(notice);
}

// gitPushDo pushes the current branch to its upstream, behind a confirm. Shows the
// push output in the diff pane on success.
async function gitPushDo(ov, btn) {
  const target = ov.upstream || "the remote";
  const warn = ov.behind ? `\n\nThis branch is ${ov.behind} behind ${target} — the push may be rejected; pull first if so.` : "";
  if (!(await confirmBox(`Push ${ov.ahead ? ov.ahead + " commit(s) " : ""}to ${target}?` + warn, { title: "Push", okText: "Push" }))) return;
  if (btn) { btn.disabled = true; btn.textContent = "Pushing…"; }
  let notice = "";
  try {
    const r = await fetch(api(`/api/sessions/${current}/git/push`), { method: "POST", body: JSON.stringify({}) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Push failed."); }
    else { notice = "✓ " + ((await r.json().catch(() => ({}))).result || "Pushed."); }
  } catch (_) { alertBox("Push failed."); }
  renderGitModal(notice);
}

// gitCommitRow renders one commit: short hash · subject · relative time; click →
// the commit's diff.
function gitCommitRow(c, diffPane, showDiff) {
  const row = el("div", "git-row git-commit");
  row.appendChild(el("span", "git-hash", c.hash));
  row.appendChild(el("span", "git-subject truncate", c.subject || "(no message)"));
  row.appendChild(el("span", "git-when text-faint", c.when || ""));
  row.title = (c.subject || "") + (c.author ? "  ·  " + c.author : "");
  row.onclick = () => showDiff(api(`/api/sessions/${current}/git/commit?hash=` + encodeURIComponent(c.hash)), "(empty commit)");
  return row;
}

function gitBadgeText(c) { return c.status === "??" ? "?" : (c.label ? c.label[0] : "•"); }
function gitBadgeClass(c) {
  switch (c.label) {
    case "Added": case "Untracked": return "is-add";
    case "Deleted": return "is-del";
    case "Renamed": case "Copied": return "is-ren";
    case "Conflicted": return "is-conflict";
    default: return "is-mod";
  }
}

// --- Project memory editor ---------------------------------------------------
// The composer's memory pill opens this: a structured editor over EVERY section
// of the project AGENTS.md (Memory, Versioning, …). Each section shows its whole
// content as ONE block — rendered markdown by default, swapping to a raw-markdown
// textarea on Edit (memorySectionBlock). Saving rewrites the whole section
// (empty content removes it); changes hit the agent immediately (it rewrites
// AGENTS.md and rebuilds the system prompt), so they apply to the next turn.

// The session-memory modal groups everything the agent carries INTO a turn as
// project knowledge: the AGENTS.md memory bullets, the project notes backlog that
// @notes hands over, and the codebase map it consults before exploring. They were
// scattered across two modals before, which made the map hard to find even though
// it is the piece that most affects how much the agent has to re-read.
const MEMORY_TABS = [
  { key: "memory", label: "Memory", icon: "brain", render: () => renderMemoryEditor() },
  { key: "notes", label: "Notes", icon: "note.text", render: () => renderMemoryNotesTab() },
  { key: "map", label: "Codebase map", icon: "map.fill", render: () => renderCodebaseMapPanel() },
];

async function openMemoryEditor(tabKey) {
  if (!current) { alertBox("Open a session first to edit its memory."); return; }
  openTabbedModal("Project memory", "brain", MEMORY_TABS, tabKey || "memory");
}

// renderMemoryNotesTab renders the active project's notes checklist inside the
// memory modal, or explains why it can't.
function renderMemoryNotesTab() {
  manageBody.innerHTML = "";
  const p = (typeof getActiveProject === "function") ? getActiveProject() : null;
  if (!p) {
    manageBody.append(el("p", "access-intro",
      "No active project for this session, so there are no notes to show."));
    return;
  }
  renderProjectNotesInto(manageBody, currentLaptop, p);
}

async function renderMemoryEditor() {
  if (!current) return;
  const body = manageBody;
  let data = { project: [], global: [] };
  try { const r = await fetch(api(`/api/sessions/${current}/memory`)); if (r.ok) data = await r.json(); } catch (_) {}

  body.innerHTML = "";
  body.append(el("p", "access-intro", "The agent reads these every turn. Notes live in two places: this project's AGENTS.md, shared by every session in the project, and a global one shared by every project on this machine. Edit, delete, or add a bullet to any section — changes apply to the next message."));

  // Two clearly-separated groups so it's obvious which scope a note lands in.
  memoryGroup(body, false, "This project", "brain", "Every session in this project (its AGENTS.md).", data.project || []);
  memoryGroup(body, true, "Global", "globe", "Every project on this machine (~/.config/moony/AGENTS.md).", data.global || []);
}

// memoryGroupHead is a strong divider introducing one scope (session/global).
function memoryGroupHead(title, iconName, subtitle) {
  const h = el("div", "mem-group-head mt-4 mb-1 pb-1 border-b border-edge");
  const row = el("div", "flex items-center gap-1.5");
  const ic = sym(iconName); if (ic) row.append(ic);
  row.append(el("span", "font-semibold", title));
  h.append(row);
  if (subtitle) h.append(el("div", "text-faint text-[11px] mt-0.5", subtitle));
  return h;
}

// memoryGroup renders one scope's header and its sections. Each section shows
// its whole content (all bullets) as ONE block: rendered markdown by default,
// swapping to a raw-markdown textarea when the user hits Edit (memorySectionBlock).
function memoryGroup(body, global, title, iconName, subtitle, sections) {
  body.append(memoryGroupHead(title, iconName, subtitle));
  if (!sections.length) {
    body.append(el("div", "access-empty", "No rules here yet — add one below."));
    body.append(lspSectionHead("Memory"));
    body.append(memorySectionBlock("Memory", [], global, true));
    return;
  }
  for (const sec of sections) {
    body.append(lspSectionHead(sec.heading || "Memory"));
    body.append(memorySectionBlock(sec.heading || "Memory", sec.bullets || [], global, false));
  }
}

// memGrow sizes a textarea to its content so the full text is visible.
function memGrow(ta) { ta.style.height = "auto"; ta.style.height = ta.scrollHeight + "px"; }

// memorySectionBlock renders one whole section: the combined content of all its
// bullets. By default it shows the content as RENDERED markdown (view mode);
// Edit swaps in a textarea holding the RAW markdown (one "- bullet" per line) so
// the user edits the section's full text at once. Save writes the whole section
// back via replace-section (empty text removes the section). startEditing opens
// directly in edit mode (used for a brand-new, empty section).
function memorySectionBlock(section, bullets, global, startEditing) {
  const raw = (bullets || []).map((b) => "- " + b).join("\n");
  const wrap = el("div", "manage-row mem-row mem-section");

  const view = el("div", "mem-view md");
  view.innerHTML = raw ? renderMD(raw) : "<p class='text-faint'>(empty — click Edit to add content)</p>";

  const ta = el("textarea", "mem-input");
  ta.value = raw; ta.spellcheck = false; ta.rows = Math.max(3, (bullets || []).length + 1);
  ta.placeholder = "One note per line (a leading “- ” is optional)…";
  ta.style.display = "none";
  ta.oninput = () => memGrow(ta);

  const actions = el("div", "mem-actions");
  const editBtn = el("button", "manage-install", "Edit");
  const saveBtn = el("button", "manage-install", "Save");
  const cancelBtn = el("button", "manage-remove", "Cancel");

  const setEditing = (on) => {
    view.style.display = on ? "none" : "";
    ta.style.display = on ? "block" : "none";
    editBtn.style.display = on ? "none" : "";
    saveBtn.style.display = on ? "" : "none";
    cancelBtn.style.display = on ? "" : "none";
    if (on) { memGrow(ta); ta.focus(); }
  };
  editBtn.onclick = () => setEditing(true);
  cancelBtn.onclick = () => { ta.value = raw; setEditing(false); };
  // Save even when cleared — empty content deletes the section on the server.
  saveBtn.onclick = () => { if (ta.value !== raw) memoryDo("replace-section", { section, text: ta.value, global }); else setEditing(false); };

  actions.append(editBtn, saveBtn, cancelBtn);
  wrap.append(view, ta, actions);
  setEditing(!!startEditing);
  requestAnimationFrame(() => { if (ta.style.display !== "none") memGrow(ta); });
  return wrap;
}

async function memoryDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/memory/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderMemoryEditor(); // re-list; the server's status event refreshes the pill count
}

async function renderSkillsMarketplace() {
  const body = manageBody;
  let items = [];
  try { const r = await fetch(api(`/api/sessions/${current}/skills/catalog`)); if (r.ok) items = await r.json(); } catch (_) {}
  items = items || [];

  body.innerHTML = "";
  body.append(el("p", "access-intro", "Agent Skills give the model focused, on-demand expertise (loaded via the skill tool). Install from the catalog, or enable/disable and remove the ones you have. Installs write to your global ~/.claude/skills."));

  const installed = items.filter((i) => i.installed);
  const available = items.filter((i) => !i.installed);

  body.append(lspSectionHead("Installed"));
  if (!installed.length) {
    body.append(el("div", "access-empty", "No skills yet — install one from the marketplace below."));
  } else {
    const group = el("div", "skills-installed");
    for (const it of installed) group.append(skillInstalledRow(it));
    body.append(group);
  }

  body.append(lspSectionHead("Marketplace"));
  if (!available.length) body.append(el("div", "access-empty", "Everything in the catalog is installed."));
  for (const it of available) body.append(skillAvailableRow(it));
}

function skillInstalledRow(it) {
  const desc = (it.desc || "") + (it.source ? "  ·  " + it.source : "");
  const remove = it.removable ? () => { confirmBox(`Remove skill "${it.name}"?`, { title: "Remove skill", okText: "Remove", danger: true }).then((ok) => { if (ok) skillDo("remove", { name: it.name }); }); } : null;
  return manageRow(it.name, desc, it.enabled,
    () => skillDo("toggle", { name: it.name, enabled: !it.enabled }), remove);
}

function skillAvailableRow(it) {
  const row = el("div", "manage-row");
  const main = el("div", "manage-row-main");
  main.appendChild(el("div", "text-[13px] truncate text-ink", it.title || it.name));
  if (it.desc) main.appendChild(el("div", "text-[11px] text-faint truncate", it.desc));
  row.appendChild(main);
  const btn = el("button", "manage-install", "Install");
  btn.onclick = () => skillDo("install", { name: it.name });
  row.appendChild(btn);
  return row;
}

async function skillDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/skills/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderSkillsMarketplace();
}

// --- MCP marketplace panel --------------------------------------------------
// Install MCP servers from a curated catalog (registers in the global mcp.json
// and connects), and enable/disable or remove the configured ones. Mirrors the
// LSP/Skills panels. Connecting can be slow (npx/uvx fetch on first run), so the
// result is reported to the conversation; the panel polls to reflect it.

let mcpPollTimer = null;


function stopMcpPoll() { if (mcpPollTimer) { clearInterval(mcpPollTimer); mcpPollTimer = null; } }

async function renderMcpMarketplace() {
  const body = manageBody;
  let items = [];
  let catalogOK = false;
  try { const r = await fetch(api(`/api/sessions/${current}/mcps/catalog`)); if (r.ok) { items = await r.json(); catalogOK = true; } } catch (_) {}
  items = items || [];

  // A live terminal mirror doesn't serve the catalog/management endpoints, so the
  // fetch 404s. Fall back to the connected MCP servers carried on the status event
  // (read-only — they're installed/managed from the terminal via /mcps).
  if (!catalogOK) { stopMcpPoll(); renderMcpFromStatus(body); return; }

  body.innerHTML = "";
  body.append(el("p", "access-intro", "MCP servers add tools (filesystem, git, fetch, …) to the model. Install from the catalog — it's registered globally and connected via npx/uvx. Some servers need an env var (e.g. a token); add it to mcp.json if connection fails."));

  const installed = items.filter((i) => i.installed);
  const available = items.filter((i) => !i.installed);

  body.append(lspSectionHead("Installed"));
  if (!installed.length) {
    body.append(el("div", "access-empty", "No MCP servers yet — install one from the marketplace below."));
  } else {
    const group = el("div", "mcp-installed");
    for (const it of installed) group.append(mcpInstalledRow(it));
    body.append(group);
  }

  body.append(lspSectionHead("Marketplace"));
  if (!available.length) body.append(el("div", "access-empty", "Everything in the catalog is installed."));
  for (const it of available) body.append(mcpAvailableRow(it));

  // Keep refreshing while anything is connecting (enabled + not yet connected/failed).
  const connecting = installed.some((i) => i.enabled && !i.connected && !i.failed);
  if (connecting && !mcpPollTimer) mcpPollTimer = setInterval(renderMcpMarketplace, 3000);
  if (!connecting) stopMcpPoll();
}

// renderMcpFromStatus shows the connected MCP servers carried on the status event
// — the read-only fallback for a live terminal mirror, which can't serve the
// management catalog. Servers are installed/enabled/removed from the terminal
// session (/mcps); here we just list what's connected.
function renderMcpFromStatus(body) {
  body.innerHTML = "";
  body.append(el("p", "access-intro", "Connected MCP servers on the terminal (live mirror — read-only). Install, enable, or remove servers from the terminal session with /mcps."));
  const servers = (lastStatus && lastStatus.mcp) || [];
  body.append(lspSectionHead("Connected"));
  if (!servers.length) {
    body.append(el("div", "access-empty", "No MCP servers connected on the terminal."));
    return;
  }
  for (const mc of servers) {
    const row = el("div", "manage-row");
    const main = el("div", "manage-row-main");
    main.appendChild(el("div", "text-[13px] truncate text-ink", mc.name));
    const parts = [];
    if (mc.tools) parts.push(mc.tools + (mc.tools === 1 ? " tool" : " tools"));
    if (mc.resources) parts.push(mc.resources + (mc.resources === 1 ? " resource" : " resources"));
    if (mc.prompts) parts.push(mc.prompts + (mc.prompts === 1 ? " prompt" : " prompts"));
    if (parts.length) main.appendChild(el("div", "text-[11px] text-faint truncate", parts.join(" · ")));
    row.appendChild(main);
    row.appendChild(el("span", "shrink-0 text-[11px] text-ok", "connected"));
    body.append(row);
  }
}

function mcpBadge(it) {
  if (!it.installed) return null;
  if (it.connected) return el("span", "shrink-0 text-[11px] text-ok", "connected");
  if (it.failed) { const b = el("span", "shrink-0 text-[11px] text-danger", "failed"); b.title = it.error || ""; return b; }
  if (it.enabled) { const b = el("span", "shrink-0 text-[11px] text-faint", "connecting…"); return b; }
  return null;
}

function mcpInstalledRow(it) {
  const row = manageRow(it.name, it.target || it.desc || "", it.enabled,
    () => mcpDo("toggle", { name: it.name, enabled: !it.enabled }),
    () => { confirmBox(`Remove MCP server "${it.name}"?`, { title: "Remove MCP server", okText: "Remove", danger: true }).then((ok) => { if (ok) mcpDo("remove", { name: it.name }); }); },
    mcpBadge(it));
  // Surface the connection error inline (not just as the badge's tooltip) so a
  // server that won't connect is debuggable at a glance.
  if (it.failed && it.error) {
    const frag = document.createDocumentFragment();
    frag.appendChild(row);
    const detail = el("div", "px-3 pb-2 -mt-1 text-[11px] text-danger break-words");
    detail.textContent = "✗ " + it.error;
    frag.appendChild(detail);
    return frag;
  }
  return row;
}

function mcpAvailableRow(it) {
  const row = el("div", "manage-row");
  const main = el("div", "manage-row-main");
  main.appendChild(el("div", "text-[13px] truncate text-ink", it.title || it.name));
  if (it.desc) main.appendChild(el("div", "text-[11px] text-faint truncate", it.desc));
  if (it.needs && it.needs.length) { const n = el("div", "text-[11px] text-warn truncate", "needs env: " + it.needs.join(", ")); main.appendChild(n); }
  row.appendChild(main);
  const btn = el("button", "manage-install", "Install");
  btn.onclick = () => mcpDo("install", { name: it.name });
  row.appendChild(btn);
  return row;
}

async function mcpDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/mcps/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderMcpMarketplace();
}

// --- Agents marketplace panel -----------------------------------------------
// Install subagent personas from a curated catalog (writes a .md to the global
// ~/.config/moony/agents), and enable/disable or remove the discovered
// ones. Mirrors the Skills panel; installing is just a file write.


async function renderAgentsMarketplace() {
  const body = manageBody;
  let items = [];
  let models = [];
  let options = { providers: [], reasoningLevels: [] };
  try { const r = await fetch(api(`/api/sessions/${current}/agents/catalog`)); if (r.ok) items = await r.json(); } catch (_) {}
  try { const r = await fetch(api(`/api/sessions/${current}/agents/models`)); if (r.ok) models = await r.json(); } catch (_) {}
  try { const r = await fetch(api(`/api/sessions/${current}/agents/options`)); if (r.ok) options = await r.json(); } catch (_) {}
  items = items || [];
  models = models || [];
  const providers = options.providers || [];
  const reasoningLevels = options.reasoningLevels || [];

  body.innerHTML = "";
  body.append(el("p", "access-intro", "Subagents are focused personas the model delegates subtasks to (via the task tool or @mention). Install from the catalog, or enable/disable and remove the ones you have. Pick a model, provider, and reasoning level per agent (or leave them on “inherit” to use the main model's). Installs write to your global ~/.config/moony/agents."));

  const installed = items.filter((i) => i.installed);
  const available = items.filter((i) => !i.installed);

  body.append(lspSectionHead("Installed"));
  if (!installed.length) {
    body.append(el("div", "access-empty", "No subagents yet — install one from the marketplace below."));
  } else {
    const group = el("div", "agents-installed");
    for (const it of installed) group.append(agentInstalledRow(it, models, providers, reasoningLevels));
    body.append(group);
  }

  body.append(lspSectionHead("Marketplace"));
  if (!available.length) body.append(el("div", "access-empty", "Everything in the catalog is installed."));
  for (const it of available) body.append(agentAvailableRow(it));
}

// agentModelSelect builds the per-agent model picker: an "inherit" option (clears
// the pin) plus every available model, with the persona's current pin selected.
// Changing it writes the model: frontmatter via agentDo("model", …). Only shown
// for editable (project/global) personas — imported externals inherit the parent.
function agentModelSelect(it, models) {
  const sel = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
  sel.style.fontSize = "11px"; sel.style.padding = "0.1rem 0.2rem"; sel.style.maxWidth = "150px";
  sel.title = "Model for this subagent";
  const inherit = el("option", null, "inherit"); inherit.value = ""; sel.appendChild(inherit);
  let known = false;
  for (const m of models) {
    const o = el("option", null, m.name || m.id); o.value = m.id;
    if (m.id === it.model) { o.selected = true; known = true; }
    sel.appendChild(o);
  }
  // A pinned model that isn't in the fetched list still shows as the selection.
  if (it.model && !known) {
    const o = el("option", null, it.model); o.value = it.model; o.selected = true;
    sel.appendChild(o);
  }
  if (!it.model) inherit.selected = true;
  sel.onchange = () => agentDo("model", { name: it.name, model: sel.value });
  return sel;
}

// agentProviderSelect / agentReasoningSelect build the per-agent provider and
// reasoning-level pickers from /agents/options (the same inherit-first lists
// the Persona modal's Fast/Self-review tiers use). Changing either writes the
// provider:/reasoning: frontmatter via agentDo(...). Only shown for editable
// (project/global) personas — imported externals inherit the parent's.
function agentProviderSelect(it, providers) {
  const sel = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
  sel.style.fontSize = "11px"; sel.style.padding = "0.1rem 0.2rem"; sel.style.maxWidth = "110px";
  sel.title = "Provider for this subagent";
  let known = false;
  for (const p of providers) {
    const o = el("option", null, p.label); o.value = p.value;
    if (p.value === (it.provider || "")) { o.selected = true; known = true; }
    sel.appendChild(o);
  }
  // A pinned provider that isn't in the fetched list still shows as the selection.
  if (it.provider && !known) {
    const o = el("option", null, it.provider); o.value = it.provider; o.selected = true;
    sel.appendChild(o);
  }
  sel.onchange = () => agentDo("provider", { name: it.name, provider: sel.value });
  return sel;
}

function agentReasoningSelect(it, levels) {
  const sel = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
  sel.style.fontSize = "11px"; sel.style.padding = "0.1rem 0.2rem"; sel.style.maxWidth = "110px";
  sel.title = "Reasoning level for this subagent";
  let known = false;
  for (const l of levels) {
    const o = el("option", null, l.label); o.value = l.value;
    if (l.value === (it.reasoning || "")) { o.selected = true; known = true; }
    sel.appendChild(o);
  }
  if (it.reasoning && !known) {
    const o = el("option", null, it.reasoning); o.value = it.reasoning; o.selected = true;
    sel.appendChild(o);
  }
  sel.onchange = () => agentDo("reasoning", { name: it.name, reasoning: sel.value });
  return sel;
}

function agentInstalledRow(it, models, providers, reasoningLevels) {
  let desc = it.desc || "";
  if (it.source && it.source !== "global" && it.source !== "project") desc += "  ·  from " + it.source;
  const remove = it.removable ? () => { confirmBox(`Remove agent "${it.name}"?`, { title: "Remove agent", okText: "Remove", danger: true }).then((ok) => { if (ok) agentDo("remove", { name: it.name }); }); } : null;
  // Editable personas get model/provider/reasoning pickers grouped into one
  // status slot; imported externals are read-only and always inherit the
  // parent's.
  let status = null;
  if (it.removable) {
    status = el("div", "flex items-center gap-1 shrink-0");
    status.append(agentModelSelect(it, models || []), agentProviderSelect(it, providers || []), agentReasoningSelect(it, reasoningLevels || []));
  }
  return manageRow(it.name, desc, it.enabled,
    () => agentDo("toggle", { name: it.name, enabled: !it.enabled }), remove, status);
}

function agentAvailableRow(it) {
  const row = el("div", "manage-row");
  const main = el("div", "manage-row-main");
  main.appendChild(el("div", "text-[13px] truncate text-ink", it.title || it.name));
  if (it.desc) main.appendChild(el("div", "text-[11px] text-faint truncate", it.desc));
  if (it.tools && it.tools.length) main.appendChild(el("div", "text-[11px] text-faint truncate font-mono", "tools: " + it.tools.join(", ")));
  row.appendChild(main);
  const btn = el("button", "manage-install", "Install");
  btn.onclick = () => agentDo("install", { name: it.name });
  row.appendChild(btn);
  return row;
}

async function agentDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/agents/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderAgentsMarketplace();
}

// --- Plugins marketplace: one-click bundles of skills/agents/commands/MCP -----
// A plugin install fans out into the skills/agents/mcp installers + command
// files on the connected terminal; removing reverses it. Install/remove only
// (the individual parts are toggled in their own Skills/Agents/MCP panels).
function openPlugins() {
  if (!current) { alertBox("Connect to a terminal first — plugins install skills/agents/commands/MCP on that machine."); return; }
  setPanel("Plugins", "shippingbox");
  renderPluginsMarketplace();
}

// Marketplace clones run in the BACKGROUND on the server (the repos are large —
// tens to hundreds of MB). marketLoading tracks suggested marketplaces whose
// clone is in flight (name -> start ms); the panel shows them as "Cloning…" and a
// poll re-renders when one lands in the added list. The authoritative
// success/failure is the info/error event the server publishes to the activity
// log, so the user always learns the outcome even with this panel closed.
const marketLoading = new Map();
let marketPollTimer = null;
function stopMarketPoll() { if (marketPollTimer) { clearInterval(marketPollTimer); marketPollTimer = null; } }
function startMarketPoll() {
  if (marketPollTimer || marketLoading.size === 0) return;
  marketPollTimer = setInterval(async () => {
    if (marketLoading.size === 0) { stopMarketPoll(); return; }
    let added = [];
    try { const r = await fetch(api(`/api/sessions/${current}/plugins/marketplaces`)); if (r.ok) { const m = await r.json(); added = (m && m.marketplaces) || []; } } catch (_) {}
    let changed = false;
    for (const [name, started] of Array.from(marketLoading)) {
      // Done (now in the added list) or gave up (past the server's ~10-min clone
      // budget); either way stop showing "Cloning…". A failure's reason is in the
      // activity log as an error event.
      if (added.some((mm) => mm.name === name) || (Date.now() - started) > 11 * 60 * 1000) { marketLoading.delete(name); changed = true; }
    }
    if (marketLoading.size === 0) stopMarketPoll();
    if (changed) renderPluginsMarketplace();
  }, 4000);
}

async function renderPluginsMarketplace() {
  const body = manageBody;
  let builtins = [], mkts = { marketplaces: [], plugins: [] };
  try { const r = await fetch(api(`/api/sessions/${current}/plugins/catalog`)); if (r.ok) builtins = await r.json(); } catch (_) {}
  try { const r = await fetch(api(`/api/sessions/${current}/plugins/marketplaces`)); if (r.ok) mkts = await r.json(); } catch (_) {}
  builtins = builtins || [];
  const marketplaces = (mkts && mkts.marketplaces) || [];
  const marketPlugins = (mkts && mkts.plugins) || [];

  // Reconcile in-flight clones against what's now added (one may have landed while
  // this panel was closed); keep the poll running while any remain.
  for (const [name, started] of Array.from(marketLoading)) {
    if (marketplaces.some((mm) => mm.name === name) || (Date.now() - started) > 11 * 60 * 1000) marketLoading.delete(name);
  }
  if (marketLoading.size > 0) startMarketPoll(); else stopMarketPoll();

  body.innerHTML = "";
  body.append(el("p", "access-intro", "Plugins are one-click bundles of skills, subagents, slash-commands, and MCP servers. Install built-in bundles below, or add a git marketplace (Claude-Code style) to install the plugins it ships."));

  // Search box filters the grouped plugin list below — by plugin name/description
  // or by the skills/agents/commands/MCP each one bundles.
  const search = el("input", "plugins-search");
  search.type = "search";
  search.placeholder = "Search plugins, skills, agents…";
  search.setAttribute("aria-label", "Search plugins");
  body.append(search);

  // Marketplaces management: add a repo + the added repos (kept above the list).
  body.append(lspSectionHead("Marketplaces"));
  body.append(marketplaceAddRow());
  for (const m of marketplaces) body.append(marketplaceRow(m));
  // Curated default marketplaces not yet added — one-click Load clones + registers
  // the git repo, after which its plugins (skills/agents/MCP/commands) show up below.
  const suggested = (mkts && mkts.suggested) || [];
  for (const m of suggested) body.append(marketplaceSuggestedRow(m));

  // Marketplace plugins go in a capped, scrollable box; the built-in bundles are
  // detached into their own block below it (always in view, not buried in the
  // scroll). section(label, into) targets whichever container it's given.
  const listWrap = el("div", "plugins-scroll");
  const bundlesWrap = el("div", "plugins-bundles");
  const section = (label, into) => { const h = lspSectionHead(label); h.dataset.section = "1"; into.appendChild(h); };

  if (marketPlugins.length) {
    section("Marketplace plugins", listWrap);
    for (const p of marketPlugins) listWrap.appendChild(pluginRow(p, p.installed, p.marketplace));
  } else if (marketplaces.length) {
    listWrap.appendChild(el("div", "access-empty", "No plugins found in the added marketplace(s)."));
  }
  section("Built-in bundles", bundlesWrap);
  const installed = builtins.filter((i) => i.installed);
  const available = builtins.filter((i) => !i.installed);
  for (const it of installed) bundlesWrap.appendChild(pluginRow(it, true, ""));
  for (const it of available) bundlesWrap.appendChild(pluginRow(it, false, ""));

  const noMatch = el("div", "access-empty", "No plugins match your search.");
  noMatch.hidden = true;
  body.append(listWrap);    // marketplace plugins (capped scroll)
  body.append(bundlesWrap); // built-in bundles, detached below the scroll
  body.append(noMatch);

  search.oninput = () => filterPluginList([listWrap, bundlesWrap], search.value, noMatch);
}

// filterPluginList shows only the rows whose searchable text contains the query,
// hiding a section header when all of its rows are filtered out. Scans across the
// given containers (marketplace scroll box + the detached built-in bundles).
function filterPluginList(wraps, query, noMatch) {
  const q = query.trim().toLowerCase();
  let header = null, headerHit = false, anyHit = false;
  const flush = () => { if (header) header.hidden = !headerHit; };
  for (const wrap of wraps) {
    for (const node of Array.from(wrap.children)) {
      if (node === noMatch) continue;
      if (node.dataset.section === "1") { flush(); header = node; headerHit = false; continue; }
      const hit = !q || (node.dataset.search || "").includes(q);
      node.hidden = !hit;
      if (hit) { headerHit = true; anyHit = true; }
    }
  }
  flush();
  noMatch.hidden = !q || anyHit;
}

// marketplaceAddRow is the "add a git marketplace" input + button.
function marketplaceAddRow() {
  const row = el("div", "flex items-center gap-2 py-1.5 border-b border-edge");
  const url = manageInput("git URL (https://…/repo.git)"); url.className += " flex-1";
  const add = el("button", "shrink-0 text-[11px] rounded px-2 py-1 border border-edge text-brand", "Add marketplace");
  const go = () => { const u = url.value.trim(); if (!u) return; url.value = ""; addMarketplace(u); };
  add.onclick = go;
  url.onkeydown = (e) => { if (e.key === "Enter") go(); };
  row.appendChild(url); row.appendChild(add);
  return row;
}

// marketplaceRow shows an added marketplace (name + URL) with a Remove control.
function marketplaceRow(m) {
  const row = el("div", "flex items-center gap-2 py-1 border-b border-edge");
  const main = el("div", "min-w-0 flex-1");
  main.appendChild(el("div", "text-[13px] truncate text-ink", m.name));
  main.appendChild(el("div", "text-[11px] text-faint truncate font-mono", m.url));
  row.appendChild(main);
  const rm = el("button", "shrink-0 text-[11px] text-danger", "\u2715");
  rm.title = "Remove marketplace (installed plugins stay)";
  rm.onclick = () => { confirmBox(`Remove marketplace "${m.name}"? Installed plugins stay.`, { title: "Remove marketplace", okText: "Remove", danger: true }).then((ok) => { if (ok) marketplaceRemove(m.name); }); };
  row.appendChild(rm);
  return row;
}

// marketplaceSuggestedRow shows a curated default marketplace that isn't added
// yet (name + URL + a "Suggested" tag), with a Load button that clones the repo
// on demand so its skills/agents/MCP/commands become installable.
function marketplaceSuggestedRow(m) {
  const row = el("div", "flex items-center gap-2 py-1 border-b border-edge");
  const main = el("div", "min-w-0 flex-1");
  const top = el("div", "flex items-center gap-1.5 min-w-0");
  top.appendChild(el("span", "text-[13px] truncate text-ink", m.name));
  top.appendChild(el("span", "shrink-0 text-[9px] uppercase tracking-wide text-faint border border-edge rounded px-1", "suggested"));
  main.appendChild(top);
  main.appendChild(el("div", "text-[11px] text-faint truncate font-mono", m.url));
  row.appendChild(main);
  const loading = marketLoading.has(m.name);
  const load = el("button", "shrink-0 text-[11px] rounded px-2 py-1 border border-edge " + (loading ? "text-faint" : "text-brand"), loading ? "Cloning\u2026" : "Load");
  if (loading) {
    load.disabled = true;
    load.title = "Cloning this marketplace \u2014 watch the activity log for the result; this row updates when it's ready.";
  } else {
    load.title = "Clone this marketplace and register it (large repos can take a minute; progress + result show in the activity log)";
    load.onclick = () => addMarketplace(m.url, m.name);
  }
  row.appendChild(load);
  return row;
}

// pluginContents renders the bundle's parts as one color-coded line per type
// (skills / agents / mcp / commands) with a bold label, or null if it lists
// nothing. Each type gets its own color via a .t-<type> hook (see app.css).
function pluginContents(it) {
  const types = [
    ["skills", "skills", it.skills],
    ["agents", "agents", it.agents],
    ["mcp", "mcp", it.mcp],
    ["cmds", "cmds", it.commands],
  ];
  const box = el("div", "plugin-parts");
  let any = false;
  for (const [type, label, vals] of types) {
    if (!vals || !vals.length) continue;
    any = true;
    const line = el("div", "plugin-part truncate t-" + type);
    line.appendChild(el("span", "plugin-part-label", label + ":"));
    line.appendChild(document.createTextNode(" " + vals.join(", ")));
    box.appendChild(line);
  }
  return any ? box : null;
}

// pluginRow renders a built-in bundle (marketplace="") or a marketplace plugin.
// Marketplace rows get a violet accent + badge (.is-market) to set them apart.
function pluginRow(it, installed, marketplace) {
  const row = el("div", "plugin-row" + (marketplace ? " is-market" : ""));
  row.dataset.search = [it.name, it.title, it.desc, marketplace,
    (it.skills || []).join(" "), (it.agents || []).join(" "),
    (it.commands || []).join(" "), (it.mcp || []).join(" ")].join(" ").toLowerCase();
  const top = el("div", "flex items-center gap-2");
  const main = el("div", "min-w-0 flex-1");
  const titleRow = el("div", "flex items-center gap-2 min-w-0");
  titleRow.appendChild(el("div", "text-[13px] truncate text-ink flex-1 min-w-0", it.title || it.name));
  if (marketplace) {
    const badge = el("span", "plugin-badge shrink-0", marketplace);
    badge.title = "From marketplace: " + marketplace;
    titleRow.appendChild(badge);
  }
  main.appendChild(titleRow);
  if (it.desc) main.appendChild(el("div", "text-[11px] text-faint truncate", it.desc));
  const contents = pluginContents(it);
  if (contents) main.appendChild(contents);
  top.appendChild(main);
  if (installed) {
    const rm = el("button", "shrink-0 text-[11px] rounded px-2 py-1 border border-edge text-danger", "Remove");
    rm.onclick = () => { confirmBox(`Remove plugin "${it.name}" and its parts?`, { title: "Remove plugin", okText: "Remove", danger: true }).then((ok) => { if (ok) pluginDo("remove", it.name, marketplace); }); };
    top.appendChild(rm);
  } else {
    const label = (!marketplace && it.present > 0) ? `Install (${it.present}/${it.components} present)` : "Install";
    const btn = el("button", "shrink-0 text-[11px] rounded px-2 py-1 border border-edge text-brand", label);
    btn.onclick = () => pluginDo("install", it.name, marketplace);
    top.appendChild(btn);
  }
  row.appendChild(top);
  return row;
}

async function pluginDo(action, name, marketplace) {
  try {
    const payload = marketplace ? { name, marketplace } : { name };
    const r = await fetch(api(`/api/sessions/${current}/plugins/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || (action + " failed.")); }
  } catch (_) {}
  renderPluginsMarketplace();
}

async function addMarketplace(url, name) {
  let ok = false;
  try {
    const body = name ? { url, name } : { url };
    const r = await fetch(api(`/api/sessions/${current}/plugins/marketplace/add`), { method: "POST", body: JSON.stringify(body) });
    ok = r.ok; // 202 = clone started in the background
    if (!ok) { alertBox((await r.text().catch(() => "")).trim() || "Could not add marketplace."); }
  } catch (_) { alertBox("Could not add marketplace."); }
  // A suggested Load (has a name) shows "Cloning…" + polls until it lands; the
  // result also appears in the activity log as an info/error event. A manual add
  // (no name) just relies on those events.
  if (ok && name) { marketLoading.set(name, Date.now()); startMarketPoll(); }
  renderPluginsMarketplace();
}

async function marketplaceRemove(name) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/plugins/marketplace/remove`), { method: "POST", body: JSON.stringify({ name }) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Could not remove marketplace."); }
  } catch (_) {}
  renderPluginsMarketplace();
}

// --- Schedule panel: manage headless scheduled prompts ----------------------
// The same machine-global jobs the daemon fires and the `/schedule` command
// lists. Add a job (spec + prompt), toggle/remove it, or run one now. New jobs
// run in the current session's working directory. State comes from
// /api/sessions/{id}/schedules.

async function renderSchedules() {
  const body = manageBody;
  let items = [];
  try { const r = await fetch(api(`/api/sessions/${current}/schedules`)); if (r.ok) items = await r.json(); } catch (_) {}
  items = items || [];

  body.innerHTML = "";
  body.append(el("p", "access-intro", "Headless prompts that run automatically while the terminal or daemon is running. Each run is auto-approved (deny rules still apply) and appears as a normal, browsable session. New jobs run in this session's working directory."));

  // Add form: optional name, a trigger spec, and the prompt.
  const form = el("div", "schedule-form");
  const nameI = manageInput("name (optional)");
  nameI.style.flex = "0 0 34%";
  const specI = manageInput("every 30m · daily 09:00 · once 2026-07-01T09:00");
  specI.style.flex = "1 1 auto";
  const row = el("div", "schedule-form-row");
  row.append(nameI, specI);
  const promptI = el("textarea", "schedule-prompt bg-base border border-edge rounded px-2 py-1 text-ink");
  promptI.placeholder = "prompt to run on this schedule…";
  promptI.rows = 2;
  promptI.style.fontSize = "12px";
  const addBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Add schedule");
  addBtn.prepend(sym("calendar.badge.plus"));
  form.append(row, promptI, addBtn);
  body.append(form);

  const doAdd = async () => {
    const spec = specI.value.trim(), prompt = promptI.value.trim();
    if (!spec || !prompt) { alertBox("Enter a schedule (e.g. \"every 30m\") and a prompt."); return; }
    addBtn.disabled = true;
    const ok = await scheduleDo("add", { spec, prompt, name: nameI.value.trim() });
    addBtn.disabled = false;
    if (ok) { nameI.value = specI.value = promptI.value = ""; }
  };
  addBtn.onclick = doAdd;

  body.append(el("div", "msg-label text-faint mt-3 mb-1.5", "Scheduled jobs"));
  if (!items.length) { body.append(el("div", "access-empty", "No schedules yet — add one above.")); return; }
  for (const it of items) body.append(scheduleRow(it));
}

function scheduleRow(it) {
  const parts = [it.trigger];
  if (it.next) parts.push("next " + it.next);
  if (it.last) parts.push("last " + it.last);
  if (it.prompt) parts.push(it.prompt);
  const desc = parts.filter(Boolean).join("  ·  ");
  // "Run now" sits in the status slot; the toggle enables/disables; trash removes.
  const run = el("button", "manage-install", "Run");
  run.title = "Run now";
  run.onclick = async () => { run.disabled = true; run.textContent = "Running…"; await scheduleDo("run", { id: it.id }); };
  return manageRow(it.name, desc, it.enabled,
    () => scheduleDo("toggle", { id: it.id, enabled: !it.enabled }),
    () => { confirmBox(`Remove schedule "${it.name}"?`, { title: "Remove schedule", okText: "Remove", danger: true }).then((ok) => { if (ok) scheduleDo("remove", { id: it.id }); }); },
    run);
}

async function scheduleDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/schedules/${action}`), { method: "POST", body: JSON.stringify(payload) });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Action failed."); return false; }
  } catch (_) { return false; }
  renderSchedules();
  return true;
}

// renderAccessPanel renders an allow/deny editor backed by endpoint: GET returns
// {allow,deny}; POST {key,deny} adds; DELETE {key} removes. Shared by Tools and
// Domains since both are simple allow/deny lists.
async function renderAccessPanel(body, endpoint, key, opts) {
  body.innerHTML = "";
  body.append(el("p", "access-intro", opts.intro));
  const lists = el("div", null);
  body.append(lists);

  const form = el("div", "access-form");
  const input = el("input", "access-input");
  input.type = "text";
  input.placeholder = opts.placeholder;
  const allowBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Allow");
  const denyBtn = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Deny");
  form.append(input, allowBtn, denyBtn);
  body.append(form);

  const mutate = async (method, payload) => {
    try {
      const r = await fetch(api(endpoint), { method, body: JSON.stringify(payload) });
      if (r.ok) render(await r.json());
    } catch (_) { /* leave the list as-is on a transient failure */ }
  };
  const addItem = (deny) => {
    const v = input.value.trim();
    if (!v) return;
    input.value = "";
    mutate("POST", { [key]: v, deny });
  };
  const sectionEl = (title, items, kindCls) => {
    const sec = el("div", "access-section");
    sec.append(el("div", "access-head " + kindCls, title));
    if (!items.length) { sec.append(el("div", "access-empty", "— none —")); return sec; }
    for (const it of items) {
      const row = el("div", "access-row");
      row.append(el("span", "access-name", it));
      const rm = el("button", "access-rm"); rm.title = "Remove"; rm.append(sym("xmark"));
      rm.onclick = () => mutate("DELETE", { [key]: it });
      row.append(rm);
      sec.append(row);
    }
    return sec;
  };
  function render(data) {
    lists.innerHTML = "";
    lists.append(sectionEl("Allowed", data.allow || [], "access-allow"));
    lists.append(sectionEl("Denied", data.deny || [], "access-deny"));
  }

  allowBtn.onclick = () => addItem(false);
  denyBtn.onclick = () => addItem(true);
  input.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); addItem(false); } };

  try {
    const r = await fetch(api(endpoint));
    if (r.ok) render(await r.json());
  } catch (_) {}
}

// manageDo runs a slash command for the current session, then refreshes the
// panel. (The command also echoes its result into the transcript.)
async function manageDo(cmd) { await sendText(cmd); await refreshManage(); }

async function refreshManage() {
  if (!current) return;
  const workers = await fetch(api(`/api/sessions/${current}/workers`)).then((r) => (r.ok ? r.json() : [])).catch(() => []);
  manageBody.innerHTML = "";
  manageBody.appendChild(manageWorkersSection(workers || []));
}

function manageRow(label, desc, enabled, onToggle, onRemove, status) {
  const row = el("div", "manage-row");
  const main = el("div", "manage-row-main");
  main.appendChild(el("div", "text-[13px] truncate " + (enabled ? "text-ink" : "text-faint"), label));
  if (desc) main.appendChild(el("div", "text-[11px] text-faint truncate", desc));
  row.appendChild(main);
  if (status) row.appendChild(status);
  const t = el("button", "manage-toggle" + (enabled ? " is-on" : ""), enabled ? "on" : "off");
  t.onclick = onToggle;
  row.appendChild(t);
  if (onRemove) {
    const rm = el("button", "manage-remove");
    rm.title = "Remove";
    rm.appendChild(sym("trash"));
    rm.onclick = onRemove;
    row.appendChild(rm);
  }
  return row;
}

function manageInput(placeholder, width) {
  const i = el("input", "bg-base border border-edge rounded px-2 py-1 text-ink");
  i.style.fontSize = "12px";
  i.placeholder = placeholder;
  i.style.minWidth = "0";
  if (width) i.style.width = width;
  return i;
}

// noneRow is the muted "nothing here" placeholder used by both sections.
// (12px font is set inline because the prebuilt tailwind.css has no text-[12px].)
function noneRow(text) {
  const d = el("div", "text-faint py-1", text);
  d.style.fontSize = "12px";
  return d;
}

// Worker engine installs run on the connected terminal (like the LSP panel):
// POST /workers/install kicks off the backend's vetted one-liner there, streams
// progress to the transcript, and we poll the panel until readiness flips. A
// generous cap clears the in-flight state if an install fails (the failure is
// reported in the transcript), so the button never sticks on "installing…".
let workerPollTimer = null;
const workerInstalling = new Map(); // worker name -> start epoch ms
const WORKER_INSTALL_MAX_MS = 12 * 60 * 1000;

function stopWorkerPoll() { if (workerPollTimer) { clearInterval(workerPollTimer); workerPollTimer = null; } }

function installWorker(name) {
  if (!current || workerInstalling.has(name)) return;
  workerInstalling.set(name, Date.now());
  if (!workerPollTimer) workerPollTimer = setInterval(refreshManage, 2500);
  refreshManage(); // reflect "installing…" immediately
  fetch(api(`/api/sessions/${current}/workers/install`), { method: "POST", body: JSON.stringify({ name }) })
    .then((r) => {
      if (r.ok) return; // 202: install runs in the background; the poll tracks it
      workerInstalling.delete(name);
      r.text().then((t) => alertBox((t || "").trim() || "Install failed.")).catch(() => {});
      refreshManage();
    })
    .catch(() => { workerInstalling.delete(name); });
}

// workerInstallRow offers to install a missing engine on this Mac, showing the
// exact command that will run on the connected terminal.
function workerInstallRow(wk) {
  const box = el("div", "mt-1");
  const head = el("div", "flex items-center gap-2");
  head.appendChild(el("div", "flex-1 text-[11px] text-warn truncate", wk.backend + " CLI not found on this Mac"));
  const pending = workerInstalling.has(wk.name);
  const btn = el("button", "shrink-0 text-[11px] rounded px-2 py-0.5 border border-edge text-brand", pending ? "installing…" : "Install on this Mac");
  btn.disabled = pending;
  if (!pending) btn.onclick = () => installWorker(wk.name);
  head.appendChild(btn);
  box.appendChild(head);
  const cmd = el("div", "mt-1 font-mono text-[11px] text-faint break-all");
  cmd.textContent = wk.install;
  box.appendChild(cmd);
  return box;
}

// manageWorkersSection lists worker agents with a toggle, engine readiness, and
// an inline model editor. Toggles and model changes run /workers and persist.
function manageWorkersSection(items) {
  const sec = el("div");
  sec.style.marginBottom = "1rem";
  sec.appendChild(el("div", "msg-label text-faint mb-1", "Workers"));
  // An install is done once its engine is no longer "missing"; also drop any
  // that ran past the cap (failed/aborted) so the poll can stop.
  for (const wk of items) if (wk.ready !== "missing") workerInstalling.delete(wk.name);
  for (const [n, started] of workerInstalling) if (Date.now() - started > WORKER_INSTALL_MAX_MS) workerInstalling.delete(n);
  if (!workerInstalling.size) stopWorkerPoll();
  const list = el("div", "overflow-y-auto");
  list.style.maxHeight = "500px";
  if (!items.length) list.appendChild(noneRow("No workers. Install claude, optimus, or gemini, or add one below."));
  for (const wk of items) {
    const row = el("div", "py-1.5 border-b border-edge");
    const top = el("div", "flex items-center gap-2");
    const main = el("div", "min-w-0 flex-1");
    main.appendChild(el("div", "text-[13px] truncate " + (wk.enabled ? "text-ink" : "text-faint"), wk.name));
    main.appendChild(el("div", "text-[11px] text-faint truncate", wk.backend + " · " + (wk.ready || "") + (wk.custom ? "" : " · auto-detected")));
    top.appendChild(main);
    const t = el("button", "shrink-0 text-[11px] rounded px-2 py-0.5 border border-edge " + (wk.enabled ? "text-ok" : "text-faint"), wk.enabled ? "on" : "off");
    t.onclick = () => manageDo("/workers " + (wk.enabled ? "disable " : "enable ") + wk.name);
    top.appendChild(t);
    // Only workers defined in workers.json are removable; auto-detected ones can
    // only be disabled (removing them in memory, they'd reappear next launch).
    if (wk.custom) {
      const rm = el("button", "shrink-0 text-[11px] text-danger", "\u2715");
      rm.title = "Remove this worker";
      rm.onclick = () => { confirmBox(`Remove worker "${wk.name}"?`, { title: "Remove worker", okText: "Remove", danger: true }).then((ok) => { if (ok) manageDo("/workers remove " + wk.name); }); };
      top.appendChild(rm);
    }
    row.appendChild(top);
    // Inline model editor: type a provider/model and "set" (blank clears it).
    const mrow = el("div", "flex items-center gap-2 mt-1");
    const minp = manageInput("provider/model"); minp.className += " flex-1"; minp.value = wk.model || "";
    const set = el("button", "shrink-0 text-brand"); set.style.fontSize = "12px"; set.textContent = "set model";
    set.onclick = () => manageDo("/workers model " + wk.name + " " + minp.value.trim());
    mrow.appendChild(minp); mrow.appendChild(set);
    row.appendChild(mrow);
    // Engine not on PATH: offer to install it on the connected terminal.
    if (wk.ready === "missing" && wk.install) row.appendChild(workerInstallRow(wk));
    list.appendChild(row);
  }
  sec.appendChild(list);

  // "+ Add worker": a name, a backend (claude/optimus/gemini), and an
  // optional model → /workers add, written to the global workers.json.
  const form = el("div", "flex items-center gap-2 mt-2");
  const name = manageInput("name", "84px");
  const backend = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
  backend.style.fontSize = "12px"; backend.style.padding = "0.15rem 0.25rem";
  for (const b of ["claude", "optimus", "gemini"]) backend.appendChild(el("option", null, b));
  const model = manageInput("provider/model (optional)"); model.className += " flex-1";
  const add = el("button", "shrink-0 text-brand"); add.style.fontSize = "12px"; add.textContent = "+ Add";
  add.onclick = () => {
    const n = name.value.trim();
    if (!n || /\s/.test(n)) { alertBox("Enter a worker name (no spaces)."); return; }
    const mdl = model.value.trim();
    name.value = ""; model.value = "";
    manageDo("/workers add " + n + " " + backend.value + (mdl ? " " + mdl : ""));
  };
  form.appendChild(name); form.appendChild(backend); form.appendChild(model); form.appendChild(add);
  sec.appendChild(form);
  return sec;
}

// green "connected" (with the tool count as a tooltip), red "failed" (full
// error on hover), or a faint "off" when an enabled server hasn't connected.
$("#manageClose").onclick = closeManage;
// The X button is deliberately the ONLY dismissal: no backdrop-click close. These
// panels hold half-finished input (a typed provider key in Persona, an edited
// skill), and a stray click just outside the card would silently discard it.
// Escape is likewise not wired — see the keydown chain in app-core.js.

boot();
loadAuthUser();

// --- Quick actions panel -----------------------------------------------------
// CRUD for the composer's preset prompts (/api/quickactions): two scopes
// (committable per-project .moony/quickactions.json, and a global fallback used
// only when the project defines none), reorderable because the first action is
// the split button's default, each optionally pinning a model/provider/reasoning
// for its own turn.
async function renderQuickActionsPanel() {
  const body = manageBody;
  body.innerHTML = "";

  // Reuse the Agents panel catalogs so the per-action pin offers real ids rather
  // than free text the user has to spell correctly.
  let qaModels = [], qaOptions = { providers: [], reasoningLevels: [] };
  try { const r = await fetch(api(`/api/sessions/${current}/agents/models`)); if (r.ok) qaModels = await r.json(); } catch (_) {}
  try { const r = await fetch(api(`/api/sessions/${current}/agents/options`)); if (r.ok) qaOptions = await r.json(); } catch (_) {}
  qaModels = qaModels || [];
  const qaProviders = qaOptions.providers || [], qaLevels = qaOptions.reasoningLevels || [];

  body.append(el("p", "access-intro",
    "Preset prompts for the quick-actions button beside the turn pager. Picking one sends it to the agent as if you'd typed it. An action can optionally pin a model, provider or reasoning level for its own turn only — the session reverts afterwards. Project actions live in .moony/quickactions.json (commit it to share them); global actions are a fallback used only when a project defines none. The first action is the button's default."));

// QA_ICONS is the curated icon catalog offered by the quick-action icon picker,
// grouped by what an action tends to DO so the list is scannable.
//
// Every name here must exist in SF_TO_MATERIAL (app-state.js): <app-icon> falls back
// to a generic "help" glyph for anything unmapped, so an unmapped suggestion would
// silently render as a broken icon in both the picker and the composer button.
// TestQuickActionIconsAreMapped guards this.
const QA_ICONS = [
  { group: "Build & run",     names: ["hammer", "hammer.fill", "play.fill", "stop.fill", "bolt.fill", "gearshape.fill", "wrench.and.screwdriver"] },
  { group: "Test & verify",   names: ["checkmark", "checkmark.circle", "checkmark.circle.fill", "checklist", "checkmark.shield.fill"] },
  { group: "Source control",  names: ["arrow.triangle.branch", "arrow.triangle.2.circlepath", "arrow.clockwise", "arrow.counterclockwise", "arrow.up", "square.and.arrow.down"] },
  { group: "Ship & deploy",   names: ["shippingbox.fill", "cloud.fill", "paperplane.fill", "arrow.up.to.line", "tag.fill"] },
  { group: "Code & terminal", names: ["curlybraces", "chevron.left.forwardslash.chevron.right", "doc.text", "doc.on.doc", "text.cursor"] },
  { group: "Files & search",  names: ["folder.fill", "folder", "magnifyingglass", "doc.text.magnifyingglass", "tray.full"] },
  { group: "Problems",        names: ["exclamationmark.triangle.fill", "exclamationmark.circle.fill", "ladybug.fill", "wrench.fill", "shield.fill"] },
  { group: "AI & automation", names: ["sparkles", "wand.and.stars", "brain", "brain.head.profile", "cpu"] },
  { group: "Time",            names: ["clock", "timer", "calendar"] },
  { group: "Notes & comms",   names: ["note.text", "bubble.left.fill", "envelope.fill", "bell.fill", "book.fill"] },
];

// qaIconPicker builds the icon field: a button showing the current glyph plus a
// grouped dropdown of previewed choices, so the icon can be picked by sight rather
// than typed from memory (the field used to be a free-text box, which meant guessing
// an exact symbol name and getting a silent fallback glyph when it was wrong).
//
// The list is portaled to <body> with fixed positioning because the Config modal's
// body is `overflow-y-auto` — an in-flow dropdown would be clipped by it or would
// scroll away from its button.
function qaIconPicker() {
  const wrap = el("div", "qa-icon-picker");
  const btn = el("button", "qa-icon-btn");
  btn.type = "button";
  btn.setAttribute("aria-haspopup", "listbox");
  btn.setAttribute("aria-expanded", "false");
  const btnIcon = el("span", "qa-icon-btn-glyph");
  const btnText = el("span", "qa-icon-btn-text");
  btn.append(btnIcon, btnText, sym("chevron.down", "qa-icon-caret"));
  const list = el("ul", "qa-icon-list");
  list.setAttribute("role", "listbox");
  let value = "";

  const paint = () => {
    btnIcon.replaceChildren(value ? sym(value, "qa-icon-glyph") : el("span", "qa-icon-none"));
    btnText.textContent = value || "No icon";
    btnText.classList.toggle("is-empty", !value);
  };

  const choose = (name) => { value = name; paint(); close(); };

  const buildList = () => {
    list.replaceChildren();
    const mk = (name) => {
      const li = el("li", "qa-icon-item" + (name === value ? " is-active" : ""));
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", String(name === value));
      li.title = name || "None";
      li.append(name ? sym(name, "qa-icon-glyph") : el("span", "qa-icon-none"),
                el("span", "qa-icon-name", name || "None"));
      li.onclick = () => choose(name);
      return li;
    };
    list.append(mk("")); // explicit "no icon" choice
    for (const g of QA_ICONS) {
      list.append(el("li", "qa-icon-group", g.group));
      for (const n of g.names) list.append(mk(n));
    }
  };

  function close() {
    list.classList.remove("open");
    btn.setAttribute("aria-expanded", "false");
    if (list.parentNode) list.remove();
    document.removeEventListener("mousedown", onDocDown, true);
    document.removeEventListener("keydown", onKey, true);
  }
  const onDocDown = (e) => { if (!list.contains(e.target) && !btn.contains(e.target)) close(); };
  const onKey = (e) => { if (e.key === "Escape") { e.stopPropagation(); close(); btn.focus(); } };

  const open = () => {
    buildList();
    document.body.appendChild(list);
    const r = btn.getBoundingClientRect();
    list.style.left = r.left + "px";
    list.style.minWidth = Math.max(200, r.width) + "px";
    // Flip above the button when there isn't room below, so the list is never
    // stranded off-screen at the bottom of a short viewport.
    const room = window.innerHeight - r.bottom;
    if (room < 240 && r.top > room) {
      list.style.top = "";
      list.style.bottom = (window.innerHeight - r.top + 4) + "px";
      list.style.maxHeight = Math.min(320, r.top - 12) + "px";
    } else {
      list.style.bottom = "";
      list.style.top = (r.bottom + 4) + "px";
      list.style.maxHeight = Math.min(320, room - 12) + "px";
    }
    list.classList.add("open");
    btn.setAttribute("aria-expanded", "true");
    document.addEventListener("mousedown", onDocDown, true);
    document.addEventListener("keydown", onKey, true);
  };

  btn.onclick = () => (list.classList.contains("open") ? close() : open());
  wrap.append(btn);
  // Expose .value so the picker drops into the form like the <input> it replaces.
  Object.defineProperty(wrap, "value", {
    get: () => value,
    set: (v) => { value = String(v ?? ""); paint(); },
    configurable: true,
  });
  paint();
  return wrap;
}

  let scope = "project";
  let data = { project: [], global: [], scope: "global" };

  const scopeBar = el("div", "qa-scopebar");
  const lists = el("div", null);
  const form = el("div", "qa-form");
  body.append(scopeBar, lists, form);

  // --- editor form: doubles as add + edit. Editing remembers the original
  // prompt (the row's identity) so a rename still finds the row to replace.
  let editingPrompt = null;
  const mkInput = (ph) => { const i = el("input", "access-input"); i.type = "text"; i.placeholder = ph; return i; };
  const fPrompt = mkInput("Prompt sent to the agent (required), e.g. run the tests");
  const fLabel = mkInput("Button label (defaults to the prompt)");
  const fSub = mkInput("Subtitle (optional)");
  const fIcon = qaIconPicker();
  // Pin selects are plain <select>s built from the same catalogs the Agents panel
  // uses; "" means "leave the session's setting alone".
  const mkSel = (title, width, opts, blank) => {
    const sel = el("select", "shrink-0 rounded border border-edge bg-surface text-ink");
    sel.style.fontSize = "11px"; sel.style.padding = "0.1rem 0.2rem"; sel.style.maxWidth = width;
    sel.title = title;
    if (blank) { const o = el("option", null, blank); o.value = ""; sel.appendChild(o); }
    for (const o of opts) { const x = el("option", null, o.label); x.value = o.value; sel.appendChild(x); }
    return sel;
  };
  const modelOpts = qaModels.map((m) => ({ value: m.id, label: m.name || m.id }));
  // The catalogs already lead with an inherit/default entry; only the model list
  // needs one synthesized.
  const provOpts = qaProviders.length ? qaProviders : [{ value: "", label: "session default" }];
  const lvlOpts = qaLevels.length ? qaLevels : [{ value: "", label: "session default" }];

  const pinField = (label, sel) => {
    const w = el("label", "agent-pin");
    w.append(el("span", "agent-pin-label", label), sel);
    return w;
  };
  const fProvider = mkSel("Provider for this action's turn", "110px", provOpts, provOpts[0].value === "" ? null : "session default");
  const fModel = mkSel("Model for this action's turn", "150px", modelOpts, "session default");
  const fReasoning = mkSel("Reasoning level for this action's turn", "110px", lvlOpts, lvlOpts[0].value === "" ? null : "session default");
  const pinRow = el("div", "agent-pins qa-pins");
  pinRow.append(pinField("Provider", fProvider), pinField("Model", fModel), pinField("Reasoning", fReasoning));

  const saveBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Add");
  const cancelBtn = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Cancel");
  cancelBtn.hidden = true;
  const err = el("div", "qa-error"); err.hidden = true;
  const formActions = el("div", "qa-form-actions");
  formActions.append(saveBtn, cancelBtn);
  // Each control gets a persistent label + hint. Placeholders alone vanish as soon as
  // there's text, so while EDITING an existing action every field was an unlabelled box
  // and "which one is the subtitle?" needed guessing.
  const field = (label, hint, ctl) => {
    const w = el("label", "qa-field");
    const head = el("span", "qa-field-label", label);
    if (hint) head.append(el("span", "qa-field-hint", hint));
    w.append(head, ctl);
    return w;
  };
  // Mode heading: the only previous cue that you were editing rather than adding was
  // the submit button's text flipping to "Save", which is easy to miss — and nothing at
  // all said WHICH action was loaded into the form.
  const formHead = el("div", "qa-form-head");
  form.append(
    formHead,
    field("Prompt", "required — sent to the agent", fPrompt),
    field("Label", "button text; defaults to the prompt", fLabel),
    field("Subtitle", "optional second line", fSub),
    field("Icon", "optional", fIcon),
    pinRow, formActions, err,
  );

  const setFormHead = (editing) => {
    formHead.replaceChildren();
    if (editing) {
      formHead.append(sym("square.and.pencil", "qa-form-head-icon"),
                      el("span", "qa-form-head-text", "Editing"),
                      el("code", "qa-form-head-target", editing));
    } else {
      formHead.append(sym("plus", "qa-form-head-icon"),
                      el("span", "qa-form-head-text", "New quick action"));
    }
  };

  const resetForm = () => {
    editingPrompt = null;
    fPrompt.value = fLabel.value = fSub.value = fIcon.value = "";
    fProvider.value = fModel.value = fReasoning.value = "";
    saveBtn.textContent = "Add";
    cancelBtn.hidden = true;
    err.hidden = true;
    setFormHead(null);
  };

  const mutate = async (method, payload, path) => {
    err.hidden = true;
    try {
      const r = await fetch(api("/api/quickactions" + (path || "")), {
        method, headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scope, ...payload }),
      });
      if (!r.ok) { err.textContent = (await r.text()).trim() || "Request failed."; err.hidden = false; return false; }
      data = await r.json();
      render();
      // The composer menu reads the same endpoint — refresh it so an edit shows
      // up immediately instead of after a reload.
      if (typeof loadQuickActions === "function") loadQuickActions();
      return true;
    } catch (_) {
      err.textContent = "Could not reach the server."; err.hidden = false; return false;
    }
  };

  saveBtn.onclick = async () => {
    const action = {
      prompt: fPrompt.value.trim(), label: fLabel.value.trim(),
      sub: fSub.value.trim(), icon: fIcon.value.trim(),
      model: fModel.value, provider: fProvider.value, reasoning: fReasoning.value,
    };
    if (!action.prompt) { err.textContent = "Prompt is required."; err.hidden = false; return; }
    const ok = editingPrompt
      ? await mutate("PUT", { oldPrompt: editingPrompt, action })
      : await mutate("POST", { action });
    if (ok) resetForm();
  };
  cancelBtn.onclick = resetForm;
  fPrompt.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); saveBtn.click(); } };

  const startEdit = (a) => {
    editingPrompt = a.prompt;
    fPrompt.value = a.prompt; fLabel.value = a.label || ""; fSub.value = a.sub || ""; fIcon.value = a.icon || "";
    fModel.value = a.model || ""; fProvider.value = a.provider || ""; fReasoning.value = a.reasoning || "";
    saveBtn.textContent = "Save";
    cancelBtn.hidden = false;
    err.hidden = true;
    setFormHead(a.label || a.prompt);
    fPrompt.focus();
  };

  function render() {
    scopeBar.innerHTML = "";
    for (const s of ["project", "global"]) {
      const b = el("button", "qa-scope" + (scope === s ? " is-active" : ""), s === "project" ? "Project" : "Global");
      b.onclick = () => { scope = s; resetForm(); render(); };
      scopeBar.append(b);
    }
    const list = (scope === "project" ? data.project : data.global) || [];
    if (scope === "project" && !list.length) {
      scopeBar.append(el("span", "qa-scope-note", "No project actions — the global list is in use."));
    } else if (scope === "global" && (data.project || []).length) {
      scopeBar.append(el("span", "qa-scope-note", "Project actions override these."));
    }

    lists.innerHTML = "";
    if (!list.length) lists.append(el("div", "access-empty", "— none —"));

    list.forEach((a, i) => {
      const row = el("div", "manage-row qa-card");
      if (a.icon) row.append(sym(a.icon, "qa-row-icon"));
      const main = el("div", "manage-row-main");
      const title = el("div", "text-[13px] truncate text-ink", a.label || a.prompt);
      if (i === 0) title.append(el("span", "qa-default-badge", "default"));
      main.append(title);
      main.append(el("div", "text-[11px] text-faint truncate font-mono", a.prompt));
      if (a.sub) main.append(el("div", "text-[11px] text-faint truncate", a.sub));
      // Summarize the pin so an action that runs somewhere else is visibly
      // different without opening the editor.
      const ovBits = [a.provider, a.model, a.reasoning && "reasoning: " + a.reasoning].filter(Boolean);
      if (ovBits.length) main.append(el("div", "text-[11px] truncate qa-pin-summary", "↯ " + ovBits.join(" · ")));
      row.append(main);

      const btn = (icon, title, fn, disabled) => {
        const b = el("button", "access-rm"); b.title = title; b.append(sym(icon));
        b.disabled = !!disabled; b.onclick = fn; return b;
      };
      row.append(
        btn("chevron.up", "Move up", () => mutate("POST", { prompt: a.prompt, delta: -1 }, "/move"), i === 0),
        btn("chevron.down", "Move down", () => mutate("POST", { prompt: a.prompt, delta: 1 }, "/move"), i === list.length - 1),
        btn("pencil", "Edit", () => startEdit(a)),
        btn("xmark", "Remove", async () => {
          if (editingPrompt === a.prompt) resetForm(); // don't leave the form editing a deleted row
          await mutate("DELETE", { prompt: a.prompt });
        }),
      );
      lists.append(row);
    });

    // Reset-to-defaults sits under the list, away from the destructive row buttons.
    const foot = el("div", "qa-foot");
    const reset = el("button", "qa-reset", "Restore built-in defaults");
    reset.onclick = async () => {
      if (!confirm(`Replace the ${scope} quick actions with the built-in defaults?`)) return;
      resetForm();
      await mutate("POST", {}, "/reset");
    };
    foot.append(reset);
    lists.append(foot);
  }

  try {
    const r = await fetch(api("/api/quickactions"));
    if (r.ok) {
      data = await r.json();
      // Land on whichever scope is actually in effect, so the panel opens on the
      // list the button is really using.
      scope = data.scope === "global" && !(data.project || []).length ? "global" : "project";
    }
  } catch (_) { /* offline — render empty */ }
  setFormHead(null); // paint the "New quick action" heading on first open
  render();
}

// ── Task Board ──────────────────────────────────────────────────────────────
// Queue prompts that run in parallel, each in its own throwaway git worktree;
// review each finished task's diff and Apply it onto the real tree or Discard.
let taskBoardTimer = null;

async function renderTaskBoard() {
  const body = manageBody;
  body.innerHTML = "";
  body.append(el("p", "access-intro",
    "Queue prompts that run in parallel — each in its own throwaway git worktree off the current HEAD, so nothing touches your real working tree until you Apply. Runs are headless and auto-approved (deny rules still apply). “Run after” only delays a task's start until its prerequisites finish; it doesn't carry over their edits, so Apply a prerequisite first if this task needs it."));

  const form = el("div", "schedule-form");
  const titleI = manageInput("title (optional)");
  const promptI = el("textarea", "schedule-prompt bg-base border border-edge rounded px-2 py-1 text-ink");
  promptI.placeholder = "what should this task do? e.g. “add a --json flag to the export command, with a test”";
  promptI.rows = 2;
  promptI.style.fontSize = "12px";
  const addBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Queue task");
  addBtn.prepend(sym("plus.rectangle.on.rectangle"));
  form.append(titleI, promptI, addBtn);
  body.append(form);

  addBtn.onclick = async () => {
    const prompt = promptI.value.trim();
    if (!prompt) { alertBox("Enter a prompt for the task."); return; }
    addBtn.disabled = true;
    const ok = await taskBoardDo("add", { title: titleI.value.trim(), prompt });
    addBtn.disabled = false;
    if (ok) { titleI.value = promptI.value = ""; }
  };

  const list = el("div", "taskboard-list");
  body.append(list);
  refreshTaskBoard(list);
}

async function refreshTaskBoard(list) {
  let tasks = [];
  try { const r = await fetch(api(`/api/sessions/${current}/taskboard`)); if (r.ok) tasks = (await r.json()).tasks || []; } catch (_) {}
  if (!document.contains(list)) { // tab switched or modal closed — stop polling
    if (taskBoardTimer) { clearTimeout(taskBoardTimer); taskBoardTimer = null; }
    return;
  }
  list.innerHTML = "";
  if (!tasks.length) list.append(el("div", "access-empty", "No tasks yet — queue one above."));
  else for (const t of tasks) list.append(taskCard(t));

  // Poll faster while something is in flight, slower when idle.
  const active = tasks.some((t) => t.status === "queued" || t.status === "running" || t.status === "blocked");
  if (taskBoardTimer) clearTimeout(taskBoardTimer);
  taskBoardTimer = setTimeout(() => refreshTaskBoard(list), active ? 1500 : 5000);
}

const TB_STATUS = {
  queued:     "Queued",
  blocked:    "Waiting",
  running:    "Running",
  done:       "Ready to apply",
  failed:     "Failed",
  applying:   "Applying…",
  applied:    "Applied",
  discarding: "Discarding…",
  discarded:  "Discarded",
};

function taskCard(t) {
  const card = el("div", "taskboard-card is-" + t.status);
  const head = el("div", "taskboard-head");
  const badge = el("span", "taskboard-badge is-" + t.status, TB_STATUS[t.status] || t.status);
  if (t.status === "running") badge.prepend(sym("arrow.clockwise", "taskboard-spin"));
  head.append(badge, el("span", "taskboard-title", t.title || t.id));
  card.append(head);

  if (t.status === "failed" && t.error) card.append(el("div", "taskboard-error", t.error));
  else if (t.response) card.append(el("div", "taskboard-response", t.response));

  const actions = el("div", "taskboard-actions");
  let diffBox = null;
  if (t.status === "done") {
    if (t.diff && t.diff.trim()) {
      diffBox = el("div", "taskboard-diff-wrap");
      diffBox.hidden = true;
      diffBox.append(renderDiffEl(t.diff));
      const view = el("button", "manage-install", "View diff");
      view.onclick = () => { diffBox.hidden = !diffBox.hidden; view.textContent = diffBox.hidden ? "View diff" : "Hide diff"; };
      actions.append(view);
    } else {
      actions.append(el("span", "taskboard-nochange", "no file changes"));
    }
    const apply = el("button", "b-yes inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs", "Apply");
    apply.onclick = async () => { apply.disabled = true; if (!(await taskBoardDo("apply", { id: t.id }))) apply.disabled = false; };
    const discard = el("button", "b-no inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs", "Discard");
    discard.onclick = () => taskBoardDo("discard", { id: t.id });
    actions.append(apply, discard);
  } else if (t.status === "queued" || t.status === "blocked" || t.status === "failed") {
    const drop = el("button", "b-no inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs", t.status === "failed" ? "Dismiss" : "Cancel");
    drop.onclick = () => taskBoardDo("discard", { id: t.id });
    actions.append(drop);
  }
  if (actions.childNodes.length) card.append(actions);
  if (diffBox) card.append(diffBox);
  return card;
}

// renderDiffEl builds a standalone <pre> diff element (taskboard view). Distinct
// from the shared renderDiff(pre, text) in app-render.js, which fills an existing
// element — same name previously shadowed that one (app-manage.js loads last),
// breaking the git-modal and approval diffs with "Failed to load diff.".
function renderDiffEl(diff) {
  const pre = el("pre", "taskboard-diff");
  for (const line of diff.split("\n")) {
    let cls = "";
    if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff ") || line.startsWith("index ")) cls = "d-meta";
    else if (line.startsWith("@@")) cls = "d-hunk";
    else if (line.startsWith("+")) cls = "d-add";
    else if (line.startsWith("-")) cls = "d-del";
    pre.append(el("span", "d-line " + cls, line + "\n"));
  }
  return pre;
}

async function taskBoardDo(action, payload) {
  try {
    const r = await fetch(api(`/api/sessions/${current}/taskboard/${action}`), { method: "POST", body: JSON.stringify(payload) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || (j && j.ok === false)) { alertBox((j && j.error) || "Action failed."); return false; }
  } catch (_) { return false; }
  const list = manageBody && manageBody.querySelector(".taskboard-list");
  if (list) refreshTaskBoard(list);
  return true;
}

// --- Codebase map panel ------------------------------------------------------
// Read + maintain CODEBASE_MAP.md without messaging the agent: view the whole
// map, regenerate the auto structural section, and edit the pinned notes (the
// one section a human owns outright — never overwritten by the generator or by a
// narrative rewrite). The feature→files narrative is still written by the agent
// via the codebase_map tool.
async function renderCodebaseMapPanel() {
  const body = manageBody;
  body.innerHTML = "";
  if (!current) { body.append(el("p", "access-intro", "Open a session first to view its codebase map.")); return; }

  body.append(el("p", "access-intro",
    "The maintained map of this project. The structure section is generated from the code and auto-refreshed on change; the narrative (features → files) is written on demand by the agent's codebase_map tool. The agent reads this before exploring, so a good map means fewer wasted look-ups."));

  const actions = el("div", "cm-actions");
  const genBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Generate map");
  actions.append(genBtn, el("span", "text-[11px] text-faint",
    "Tip: ask the agent to update the codebase map to (re)write the feature → files narrative."));

  // Pinned notes: hand-authored, sent to the model first, never regenerated.
  const pinWrap = el("div", "cm-pin");
  const pinHead = el("div", "cm-pin-head");
  pinHead.append(el("span", "cm-pin-title", "Pinned notes"),
    el("span", "text-[11px] text-faint", "hand-authored · never overwritten · sent to the model first"));
  const pinTa = el("textarea", "cm-pin-ta");
  pinTa.rows = 6;
  pinTa.placeholder = "Architecture decisions, gotchas, invariants, conventions…";
  const pinBar = el("div", "cm-pin-bar");
  const pinSave = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5", "Save pinned notes");
  const pinMsg = el("span", "text-[11px] text-faint");
  pinBar.append(pinSave, pinMsg);
  pinWrap.append(pinHead, pinTa, pinBar);

  const status = el("div", "text-[12px] text-faint");
  const view = el("div", "cm-view");
  body.append(actions, pinWrap, status, view);

  async function load() {
    view.innerHTML = "";
    status.textContent = "Loading…";
    try {
      const r = await fetch(api(`/api/sessions/${current}/codebasemap`));
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const j = await r.json();
      // Don't clobber what the user is typing if a refresh lands mid-edit.
      if (document.activeElement !== pinTa) pinTa.value = j.pinned || "";
      if (!j.exists || !(j.content || "").trim()) {
        status.textContent = "No codebase map yet.";
        genBtn.textContent = "Generate map";
        view.append(el("p", "text-faint", "This project has no CODEBASE_MAP.md. Generate it to create the structural map."));
      } else {
        status.textContent = j.path || "CODEBASE_MAP.md";
        genBtn.textContent = "Refresh structure";
        view.innerHTML = renderMD(j.content);
      }
    } catch (err) {
      status.textContent = "Failed to load: " + err.message;
    }
  }

  genBtn.onclick = async () => {
    genBtn.disabled = true;
    const prev = genBtn.textContent;
    genBtn.textContent = "Generating…";
    try {
      const r = await fetch(api(`/api/sessions/${current}/codebasemap`), { method: "POST" });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      await load();
    } catch (err) {
      status.textContent = "Failed to generate: " + err.message;
      genBtn.textContent = prev;
    } finally {
      genBtn.disabled = false;
    }
  };

  pinSave.onclick = async () => {
    pinSave.disabled = true;
    pinMsg.textContent = "Saving…";
    try {
      const r = await fetch(api(`/api/sessions/${current}/codebasemap`), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pinned: pinTa.value }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      pinMsg.textContent = "Saved.";
      await load();
    } catch (err) {
      pinMsg.textContent = "Failed: " + err.message;
    } finally {
      pinSave.disabled = false;
    }
  };

  await load();
}

// renderDoctorPanel — the Config modal's "Doctor" tab. Fetches the per-session
// diagnostics endpoint and renders one status row per check (ok/warn/fail badge
// + name + detail), with a "Re-run checks" button. Read-only: it surfaces the
// same checks as the /doctor command plus live embeddings/LSP/MCP probes.
async function renderDoctorPanel() {
  const body = manageBody;
  body.innerHTML = "";
  body.append(el("p", "access-intro",
    "Health checks for the connected terminal — runtime, providers/endpoints, config files, and live embeddings, LSP and MCP status. Run this when something isn't behaving to see what's actually wired up and reachable."));

  const bar = el("div", "doctor-bar");
  const rerun = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5");
  rerun.type = "button";
  rerun.append(sym("arrow.clockwise", ""), document.createTextNode("Re-run checks"));
  const summary = el("span", "doctor-summary");
  bar.append(rerun, summary);
  body.append(bar);

  const list = el("div", "doctor-list");
  body.append(list);

  const ICON = { ok: "checkmark.circle.fill", warn: "exclamationmark.triangle.fill", fail: "xmark.octagon.fill" };
  const run = async () => {
    rerun.disabled = true;
    list.innerHTML = "";
    summary.textContent = "";
    list.append(el("div", "doctor-loading", "Running checks…"));
    let checks = [];
    try {
      const r = await fetch(api(`/api/sessions/${current}/doctor`));
      if (!r.ok) throw new Error("http " + r.status);
      checks = (await r.json()).checks || [];
    } catch (_) {
      list.innerHTML = "";
      list.append(el("div", "doctor-loading", "Couldn't run the diagnostics."));
      rerun.disabled = false;
      return;
    }
    list.innerHTML = "";
    let ok = 0, warn = 0, fail = 0;
    for (const c of checks) {
      const st = c.status === "ok" || c.status === "warn" || c.status === "fail" ? c.status : "fail";
      if (st === "ok") ok++; else if (st === "warn") warn++; else fail++;
      const row = el("div", "doctor-row doctor-" + st);
      row.append(sym(ICON[st] || ICON.fail, "doctor-row-icon"));
      const txt = el("div", "doctor-row-text");
      txt.append(el("div", "doctor-row-name", c.name || ""));
      if (c.detail) txt.append(el("div", "doctor-row-detail", c.detail));
      row.append(txt);
      list.append(row);
    }
    summary.textContent = `${ok} ok · ${warn} warn · ${fail} fail`;
    summary.className = "doctor-summary" + (fail ? " has-fail" : warn ? " has-warn" : " all-ok");
    rerun.disabled = false;
  };
  rerun.onclick = run;
  run();
}

// ─── Background jobs ────────────────────────────────────────────────────────
//
// Lists this session's background jobs and tails their output.
//
// Tailing uses an ABSOLUTE cursor (`?cursor=N`) that is independent of the cursor
// `bash_output` consumes, so watching a job here never swallows output the model
// hasn't read yet — an open panel must not change what the agent sees.

let jobsPollTimer = null;   // status poll for the list
let jobsOpenID = null;      // which job's log is expanded
let jobsCursor = 0;         // absolute offset of the expanded job's log
// jobsLogText holds the log accumulated so far. The status poll rebuilds the rows every
// couple of seconds, which discards the <pre>; without keeping the text here the pane
// would reset to "Loading…" and never refill, because the cursor has already advanced
// past everything we'd fetched.
let jobsLogText = "";
let jobsLogScroll = 0;         // preserved scroll offset across re-renders
let jobsLogAtBottom = true;    // follow the tail until the user scrolls up
let jobsInputText = "";        // unsent keystrokes for the expanded TTY job
let jobsInputFocused = false;  // whether to hand focus back after a poll rebuild
let jobsInputCaret = 0;        // caret offset to restore with it
let jobsPolicy = "prompt";  // approval policy, for the pre-run warning
let jobsRestartingID = null; // job mid-restart; lives outside the row because the
                             // 2s poll rebuilds every row and would re-enable its button

// stopJobsPolling clears the timers. Called when the panel closes, so a hidden tab
// isn't polling the session forever.
function stopJobsPolling() {
  if (jobsPollTimer) { clearInterval(jobsPollTimer); jobsPollTimer = null; }
}

async function renderJobsPanel() {
  const body = manageBody;
  stopJobsPolling();
  body.innerHTML = "";
  body.append(el("p", "access-intro", "Long-running commands started with the bash tool's background flag, plus any you start here. Output is buffered — expanding a job shows its log without consuming what the agent still has to read. TTY jobs run under a pseudo-terminal: their output isn't block-buffered, and you can type into them."));

  // Start form. The button warns when the policy means it will prompt, so the
  // approval dialog isn't a surprise.
  const form = el("div", "schedule-form");
  const cmdI = manageInput("command to run in the background…");
  cmdI.style.flex = "1 1 auto";
  // TTY switch. Off by default: pipes are the cheaper, quieter option, and a job
  // only needs a terminal when it buffers its output or wants to be typed at.
  const ttyBox = el("input", null); ttyBox.type = "checkbox";
  const ttyLabel = el("label", "persona-toggle");
  ttyLabel.title = "Run under a pseudo-terminal: line-buffered output (python, django, npm) and a keyboard for prompts.";
  ttyLabel.append(ttyBox, el("span", "persona-label", "TTY"));
  const addBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Start job");
  addBtn.prepend(sym("play.fill"));
  const row = el("div", "schedule-form-row");
  row.append(cmdI, ttyLabel, addBtn);
  form.append(row);
  body.append(form);

  // Tickets for job starts awaiting approval: ticket -> command. The outcome arrives on
  // the session's SSE stream, which every tab sees, so the ticket is how this tab
  // recognises its own request instead of reacting to another's.
  const setJobHint = (text) => {
    let h = document.getElementById("jobStartHint");
    if (!h) {
      h = el("div", "jobs-hint text-faint");
      h.id = "jobStartHint";
      form.appendChild(h);
    }
    h.textContent = text || "";
    h.style.display = text ? "" : "none";
  };
  // Exposed so the SSE handler can report the outcome even though it lives in app-core.js.
  window.__jobStartHint = setJobHint;
  window.__jobStartInput = cmdI;

  const doStart = async () => {
    const command = cmdI.value.trim();
    if (!command) { alertBox("Enter a command to run."); return; }
    addBtn.disabled = true;
    try {
      const r = await fetch(api(`/api/sessions/${current}/jobs`), {
        method: "POST", body: JSON.stringify({ command, tty: ttyBox.checked }),
      });
      if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Could not start the job."); return; }
      const out = await r.json().catch(() => ({}));
      // A denial is a normal outcome (the user said no), not an error dialog.
      if (out && out.denied) { alertBox("The job was not approved."); return; }
      // 202 + pending: the start needs approval, and the answer arrives later over SSE as
      // a job_start event. The request deliberately does NOT wait for it — it used to,
      // which deadlocked: the prompt is rendered by this very UI, so a pending fetch meant
      // the user could never answer and the click hung forever.
      if (out && out.pending) {
        pendingJobTickets.set(out.ticket, command);
        // Keep the command in the box until it actually starts, so a denied or timed-out
        // approval doesn't silently lose what was typed.
        setJobHint(`Waiting for approval to run “${command}” — confirm it in the approval bar below.`);
        return;
      }
      cmdI.value = "";
      renderJobsPanel();
    } catch (_) {
      alertBox("Could not start the job.");
    } finally {
      addBtn.disabled = false;
    }
  };
  addBtn.onclick = doStart;
  cmdI.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); doStart(); } };

  const list = el("div", "jobs-list");
  body.append(el("div", "msg-label text-faint mt-3 mb-1.5", "Jobs"));
  body.append(list);

  await refreshJobsList(list);
  // Poll while the panel is open: jobs finish on their own, and a stale "running"
  // badge is worse than a slightly delayed one.
  jobsPollTimer = setInterval(() => {
    if (!document.body.contains(list)) { stopJobsPolling(); return; }
    refreshJobsList(list);
  }, 2000);
}

async function refreshJobsList(list) {
  let data = null;
  try {
    const r = await fetch(api(`/api/sessions/${current}/jobs`));
    if (r.ok) data = await r.json();
  } catch (_) { return; } // transient: keep the last render rather than blanking it
  if (!data) return;
  jobsPolicy = data.policy || "prompt";

  const jobs = data.jobs || [];
  list.innerHTML = "";
  if (!jobs.length) {
    list.append(el("div", "access-empty", "No background jobs yet."));
    return;
  }
  for (const j of jobs) list.append(jobRow(j, list));
}

// waitForJobExit polls until a killed job reports not-running, so a restart doesn't
// race the process it just stopped. JobStore.KillJob only *signals* cancellation
// (internal/tools/jobsview.go) — it doesn't wait for the process to die — so starting
// the replacement immediately would fight the old one for a port or a lock file.
// Returns false if it's still running when we give up.
async function waitForJobExit(id, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((res) => setTimeout(res, 150));
    let data = null;
    try {
      const r = await fetch(api(`/api/sessions/${current}/jobs`));
      if (!r.ok) return false;
      data = await r.json();
    } catch (_) {
      return false;
    }
    const j = ((data && data.jobs) || []).find((x) => x.id === id);
    if (!j || !j.running) return true;
  }
  return false;
}

// restartJob stops a job (when it's still running) and runs its command again as a
// NEW job, keeping the original's TTY mode.
//
// Deliberately re-POSTs to /jobs instead of adding a server-side restart endpoint:
// that routes the re-run through StartBackgroundJobApproved, so it faces exactly the
// same approval prompt and destructive-command refusal as the first start. A restart
// must not become a way to re-run a command the policy would now reject.
async function restartJob(j, list) {
  if (jobsRestartingID) return; // one at a time; the poll rebuilds rows underneath us
  jobsRestartingID = j.id;
  refreshJobsList(list); // reflect the disabled/"Restarting…" state immediately
  try {
    if (j.running) {
      try {
        await fetch(api(`/api/sessions/${current}/jobs/${encodeURIComponent(j.id)}/kill`), { method: "POST" });
      } catch (_) {
        alertBox("Could not stop the job, so it was not restarted.");
        return;
      }
      if (!(await waitForJobExit(j.id))) {
        // Start anyway rather than stranding the user, but don't pretend it was clean:
        // the replacement may fail if the old process still holds a port or lock.
        alertBox("The old job is still shutting down — starting the new one anyway, so it may fail to bind a port it holds.");
      }
    }
    const r = await fetch(api(`/api/sessions/${current}/jobs`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command: j.command, tty: !!j.tty }),
    });
    if (!r.ok) { alertBox((await r.text().catch(() => "")).trim() || "Could not restart the job."); return; }
    const out = await r.json().catch(() => ({}));
    if (out && out.denied) { alertBox("The restart was not approved."); return; }
    // Follow the replacement if the old job's log was open, otherwise the pane stays
    // pinned to a dead buffer and the restart looks like it produced nothing.
    if (jobsOpenID === j.id && out && out.id) {
      jobsOpenID = out.id;
      jobsCursor = 0;
      jobsLogText = "";
      jobsLogScroll = 0;
      jobsLogAtBottom = true;
      jobsInputText = "";
      jobsInputFocused = false;
      jobsInputCaret = 0;
    }
  } catch (_) {
    alertBox("Could not restart the job.");
  } finally {
    jobsRestartingID = null;
    refreshJobsList(list);
  }
}

function jobRow(j, list) {
  const wrap = el("div", "job-row");

  const head = el("div", "job-head");
  head.append(el("span", "job-state job-state-" + (j.state || "unknown"), j.state || "?"));
  head.append(el("code", "job-cmd", j.command || ""));

  const meta = [];
  if (j.tty) meta.push("tty");
  if (j.outputLen) meta.push(fmtBytes(j.outputLen));
  if (!j.running && j.exit !== 0) meta.push("exit " + j.exit);
  if (j.err) meta.push(j.err);
  if (meta.length) head.append(el("span", "job-meta", meta.join("  ·  ")));

  const actions = el("div", "job-actions");
  const logBtn = el("button", "manage-install", jobsOpenID === j.id ? "Hide log" : "Log");
  logBtn.onclick = () => {
    // Toggle: collapsing resets the cursor so re-opening replays the whole buffer.
    jobsOpenID = jobsOpenID === j.id ? null : j.id;
    jobsCursor = 0;
    jobsLogText = "";
    jobsLogScroll = 0;
    jobsLogAtBottom = true;
    jobsInputText = "";        // don't carry one job's half-typed line into another
    jobsInputFocused = false;
    jobsInputCaret = 0;
    refreshJobsList(list);
  };
  actions.append(logBtn);
  const busy = jobsRestartingID === j.id;
  if (j.running) {
    const killBtn = el("button", "manage-install", "Stop");
    killBtn.disabled = busy; // a restart already kills it; a second kill would hit the new job
    killBtn.onclick = async () => {
      killBtn.disabled = true;
      try { await fetch(api(`/api/sessions/${current}/jobs/${encodeURIComponent(j.id)}/kill`), { method: "POST" }); } catch (_) {}
      refreshJobsList(list);
    };
    actions.append(killBtn);
  }
  // Restart is offered for finished jobs too — re-running a command that failed or
  // exited is the common case, and it's the only way back without retyping it.
  const restartBtn = el("button", "manage-install", busy ? "Restarting…" : "Restart");
  restartBtn.disabled = busy;
  restartBtn.title = j.running
    ? "Stop this job and run the same command again as a new job."
    : "Run this command again as a new job.";
  restartBtn.onclick = () => restartJob(j, list);
  actions.append(restartBtn);

  const deleteBtn = el("button", "manage-install", "Delete");
  deleteBtn.title = "Delete this job and its output.";
  deleteBtn.onclick = async () => {
    deleteBtn.disabled = true;
    try {
      await fetch(api(`/api/sessions/${current}/jobs/${encodeURIComponent(j.id)}`), { method: "DELETE" });
      if (jobsOpenID === j.id) {
        jobsOpenID = null;
        jobsLogText = "";
      }
      const idx = list.findIndex((x) => x.id === j.id);
      if (idx !== -1) list.splice(idx, 1);
      refreshJobsList(list);
    } catch (_) {
      deleteBtn.disabled = false;
    }
  };
  actions.append(deleteBtn);
  head.append(actions);
  wrap.append(head);

  if (jobsOpenID === j.id) {
    const pre = el("pre", "job-log");
    // Restore what we already have, so a re-render doesn't blank the pane.
    pre.textContent = jobsLogText || "Loading…";
    wrap.append(pre);
    // Re-apply the scroll position after the rebuild; without this the pane jumps to
    // the top every poll and the log is unreadable while a job is producing output.
    const prevTop = jobsLogScroll, wasBottom = jobsLogAtBottom;
    requestAnimationFrame(() => {
      pre.scrollTop = wasBottom ? pre.scrollHeight : prevTop;
    });
    pre.addEventListener("scroll", () => {
      jobsLogScroll = pre.scrollTop;
      // "At the bottom" within a couple of pixels, so follow-the-tail survives
      // fractional scroll heights.
      jobsLogAtBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 4;
    });
    tailJob(j.id, pre);

    // Keyboard — only for a running TTY job, because that's the only kind with
    // anything on the other end of stdin.
    if (j.running && j.tty) wrap.append(jobKeyboard(j, list));
  }
  return wrap;
}

// jobKeyboard is the input line under an expanded TTY job's log: answer a prompt,
// drive a shell, or interrupt what's running.
//
// The panel rebuilds every poll, so the typed-but-unsent text and the focus live in
// module state — otherwise the field would empty itself under the user's fingers
// every two seconds.
function jobKeyboard(j, list) {
  const kb = el("div", "job-input");
  const inp = el("input", "job-input-field");
  inp.type = "text";
  inp.spellcheck = false;
  inp.autocomplete = "off";
  inp.placeholder = "type into the terminal — Enter sends…";
  inp.value = jobsInputText;

  const post = async (data, enter) => {
    let ok = false;
    try {
      const r = await fetch(api(`/api/sessions/${current}/jobs/${encodeURIComponent(j.id)}/input`), {
        method: "POST", body: JSON.stringify({ data, enter }),
      });
      ok = r.ok;
      if (!r.ok) alertBox((await r.text().catch(() => "")).trim() || "Could not send input.");
    } catch (_) {
      alertBox("Could not send input.");
    }
    // Pull the echo in straight away rather than waiting out the poll.
    refreshJobsList(list);
    return ok;
  };

  const sendLine = () => {
    const data = inp.value;
    // An empty line is legitimate: it's how you accept a prompt's default.
    inp.value = "";
    jobsInputText = "";
    jobsInputCaret = 0;
    // Put the line back if the server refused it, so a rejected command can be
    // edited instead of retyped.
    post(data, true).then((ok) => {
      if (!ok) { inp.value = data; jobsInputText = data; jobsInputCaret = data.length; }
    });
  };

  const rememberCaret = () => { jobsInputCaret = inp.selectionStart || 0; };
  inp.oninput = () => { jobsInputText = inp.value; rememberCaret(); };
  inp.addEventListener("keyup", rememberCaret);
  inp.addEventListener("click", rememberCaret);
  inp.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); sendLine(); return; }
    // Ctrl-C with nothing selected means "interrupt", the way it would in a terminal;
    // with a selection it stays a copy.
    if (e.key === "c" && e.ctrlKey && inp.selectionStart === inp.selectionEnd) {
      e.preventDefault();
      post("\u0003", false);
    }
  };
  inp.addEventListener("focus", () => { jobsInputFocused = true; });
  inp.addEventListener("blur", () => {
    // A blur caused by the poll ripping the row out doesn't count as the user
    // leaving the field — only a blur while the node is still on screen does.
    if (document.body.contains(inp)) jobsInputFocused = false;
  });
  if (jobsInputFocused) {
    requestAnimationFrame(() => {
      inp.focus();
      // Restore the caret where it was, not at the end: a rebuild lands mid-edit.
      const at = Math.min(jobsInputCaret, inp.value.length);
      try { inp.setSelectionRange(at, at); } catch (_) {}
    });
  }

  const sendBtn = el("button", "manage-install", "Send");
  sendBtn.onclick = sendLine;
  const intrBtn = el("button", "manage-install", "^C");
  intrBtn.title = "Send an interrupt (Ctrl-C)";
  intrBtn.onclick = () => post("\u0003", false);
  // Keep the caret in the field: without this, mousedown blurs the input and the
  // next poll rebuild drops focus for good.
  for (const b of [sendBtn, intrBtn]) b.onmousedown = (e) => e.preventDefault();

  kb.append(inp, sendBtn, intrBtn);
  return kb;
}

// tailJob fetches from the current cursor and appends. Called on each poll for the
// expanded job, so a running command's log grows in place.
// Guarded against overlap: the 2s poll and a just-sent keystroke can both ask for
// the tail, and two in-flight requests would each append their slice to
// jobsLogText — the same output twice, with the cursor left wherever the loser
// finished.
let jobsTailBusy = false;

async function tailJob(id, pre) {
  if (jobsTailBusy) return;
  jobsTailBusy = true;
  try {
    const r = await fetch(api(`/api/sessions/${current}/jobs/${encodeURIComponent(id)}/tail?cursor=${jobsCursor}`));
    if (!r.ok) { pre.textContent = "(log unavailable)"; return; }
    const out = await r.json();
    if (jobsCursor === 0) jobsLogText = "";
    if (out.output) jobsLogText += out.output;
    pre.textContent = jobsLogText || "(no output yet)";
    // Trust the server's cursor rather than adding lengths locally: after a restart
    // truncates the buffer the server pulls us back into sync.
    jobsCursor = out.cursor || 0;
    // Only follow the tail when the user hasn't scrolled up to read history.
    if (jobsLogAtBottom) pre.scrollTop = pre.scrollHeight;
  } catch (_) {
    pre.textContent = "(log unavailable)";
  } finally {
    jobsTailBusy = false;
  }
}

function fmtBytes(n) {
  if (!n) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1).replace(/\.0$/, "") + " KB";
  return (n / (1024 * 1024)).toFixed(1).replace(/\.0$/, "") + " MB";
}

// ─── Deploy panel (AWS) ──────────────────────────────────────────────────────
//
// Puts this connector's relay on a single EC2 instance without leaving the browser.
// The work runs detached on the connector (a deploy takes minutes and must survive a
// page navigation), so this polls /api/deploy/aws/log by offset rather than holding a
// stream open — the same shape the jobs panel uses.

let deployPollTimer = null;
let deployLogOffset = 0;

function stopDeployPolling() {
  if (deployPollTimer) { clearInterval(deployPollTimer); deployPollTimer = null; }
}

async function renderDeployPanel() {
  const body = manageBody;
  stopDeployPolling();
  body.innerHTML = "";
  body.append(el("p", "access-intro",
    "Deploy the relay to one EC2 instance: Caddy terminates TLS in front of the browser UI and the connector tunnel is published on an Elastic IP — no load balancers, about $15/month. Uses the AWS CLI on this machine, so whatever `aws login` gave you is what deploys. Re-running is also the update path."));

  const statusBox = el("div", "access-intro");
  statusBox.style.whiteSpace = "pre-wrap";
  statusBox.textContent = "checking…";
  body.append(statusBox);

  // Form. Defaults match the CLI's, and the two CIDRs default to the world because a
  // relay nobody can reach is useless — the warning below says what that means.
  const fields = {
    domain: manageInput("relay.example.com (enables HTTPS)"),
    email: manageInput("you@example.com (Let's Encrypt contact)"),
    envParameter: manageInput("/moony/relay-env (SSM SecureString with MN_AUTH…)"),
    webCidr: manageInput("0.0.0.0/0"),
    tunnelCidr: manageInput("0.0.0.0/0"),
    instanceType: manageInput("t4g.small"),
  };
  const grid = el("div", "schedule-form");
  for (const [key, input] of Object.entries(fields)) {
    const label = el("label", "persona-toggle");
    label.style.display = "flex";
    label.append(el("span", "persona-label", key.replace(/([A-Z])/g, " $1").toLowerCase()), input);
    input.style.flex = "1 1 auto";
    const row = el("div", "schedule-form-row");
    row.append(label);
    grid.append(row);
  }
  body.append(grid);

  const runBtn = el("button", "b-yes inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Deploy");
  runBtn.prepend(sym("arrow.up.circle"));
  const delBtn = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Delete stack");
  const cancelBtn = el("button", "b-no inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 self-start", "Stop watching");
  cancelBtn.hidden = true;
  const btnRow = el("div", "schedule-form-row");
  btnRow.append(runBtn, delBtn, cancelBtn);
  body.append(btnRow);

  const logBox = el("pre", "job-log");
  logBox.style.maxHeight = "300px";
  logBox.style.overflow = "auto";
  logBox.hidden = true;
  body.append(logBox);

  const appendLog = (text) => {
    if (!text) return;
    logBox.hidden = false;
    const atBottom = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 8;
    logBox.textContent += text;
    if (atBottom) logBox.scrollTop = logBox.scrollHeight;
  };

  const setRunning = (running) => {
    runBtn.disabled = running;
    delBtn.disabled = running;
    cancelBtn.hidden = !running;
  };

  const poll = async () => {
    try {
      const r = await fetch(api(`/api/deploy/aws/log?offset=${deployLogOffset}`));
      if (!r.ok) return;
      const j = await r.json();
      deployLogOffset = j.offset || 0;
      appendLog(j.data);
      setRunning(!!j.running);
      if (!j.running) {
        stopDeployPolling();
        statusBox.textContent = j.failure ? "failed: " + j.failure : "idle — reload the panel for the stack's new state";
        if (!j.failure) refreshStatus();
      }
    } catch (_) { /* transient: the connector may be busy */ }
  };

  const startPolling = () => {
    stopDeployPolling();
    setRunning(true);
    deployPollTimer = setInterval(poll, 2000);
  };

  const refreshStatus = async () => {
    try {
      const r = await fetch(api("/api/deploy/aws"));
      const j = await r.json();
      if (!j.cli) {
        statusBox.textContent = j.hint || "the AWS CLI is not installed on this machine";
        runBtn.disabled = true; delBtn.disabled = true;
        return;
      }
      const st = j.status || {};
      const out = st.outputs || {};
      const lines = [];
      lines.push(`region ${st.region || "?"}${st.account ? "  account " + st.account : ""}`);
      lines.push(st.stackStatus ? `stack ${st.stack}: ${st.stackStatus}` : `stack ${st.stack || "moony-relay"}: not deployed`);
      if (out.WebUrl) lines.push(`web    ${out.WebUrl}`);
      if (out.TunnelAddress) lines.push(`tunnel ${out.TunnelAddress}`);
      if (out.ConnectorCommand) lines.push(`run    ${out.ConnectorCommand}`);
      if (j.error) lines.push(`error  ${j.error}`);
      statusBox.textContent = lines.join("\n");
      if (j.running) { deployLogOffset = 0; logBox.textContent = ""; startPolling(); }
      else setRunning(false);
    } catch (e) {
      statusBox.textContent = "could not read the deployment status: " + e;
    }
  };

  const body_ = () => ({
    domain: fields.domain.value.trim(),
    email: fields.email.value.trim(),
    envParameter: fields.envParameter.value.trim(),
    webCidr: fields.webCidr.value.trim(),
    tunnelCidr: fields.tunnelCidr.value.trim(),
    instanceType: fields.instanceType.value.trim(),
  });

  const run = async (action) => {
    const req = { action, ...body_() };
    if (action === "deploy" && !req.envParameter) {
      const ok = await confirmBox(
        "No SSM parameter given, so the relay will run with NO authentication: anyone who can reach it gets a shell on this machine. Deploy anyway?",
        { yes: "Deploy without auth", no: "Cancel" });
      if (!ok) return;
    }
    if (action === "delete") {
      const ok = await confirmBox("Delete the CloudFormation stack? The instance and its Elastic IP go away; the source bucket is left behind.",
        { yes: "Delete", no: "Keep it" });
      if (!ok) return;
    }
    deployLogOffset = 0;
    logBox.textContent = "";
    try {
      const r = await fetch(api("/api/deploy/aws"), { method: "POST", body: JSON.stringify(req) });
      if (!r.ok) { alertBox("Could not start: " + (await r.text())); return; }
      startPolling();
    } catch (e) { alertBox("Could not start: " + e); }
  };

  runBtn.onclick = () => run("deploy");
  delBtn.onclick = () => run("delete");
  cancelBtn.onclick = async () => {
    await fetch(api("/api/deploy/aws/cancel"), { method: "POST" }).catch(() => {});
    stopDeployPolling();
    setRunning(false);
    appendLog("\n(stopped watching; the stack keeps converging on its own)\n");
  };

  await refreshStatus();
}
