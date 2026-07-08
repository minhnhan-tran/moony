// app-workflow.js — Agent Builder: the visual workflow canvas (Phase 1, builder
// only). Renders a workflow graph as draggable DOM nodes over an SVG bezier-edge
// layer; supports add / move / connect / select / delete and debounced autosave
// to the connector (PUT /l/{host}/api/workflows/{id}). Graph *execution* — node
// executors, branching, loops, human-in-the-loop — is a later phase.
//
// This is a classic script sharing the global scope defined by app-state.js /
// app-core.js (el, sym, $, laptopBase, currentLaptop, laptops, renderLaptopTree).
// It's wrapped in an IIFE so its mutable state doesn't leak into that shared
// namespace; the two entry points are exposed on window.
(function () {
  "use strict";

  var SVGNS = "http://www.w3.org/2000/svg";

  // Palette: the node types you can drop on the canvas → label + SF Symbol.
  // (Start is seeded by the server; Note is added from the palette too.)
  var NODE_TYPES = [
    { type: "agent",         label: "Agent",     icon: "sparkles" },
    { type: "skill",         label: "Skill",     icon: "wand.and.stars" },
    { type: "worker",        label: "Worker",    icon: "person.2.badge.gearshape.fill" },
    { type: "tool",          label: "Tool",      icon: "wrench.and.screwdriver" },
    { type: "mcp",           label: "MCP",       icon: "puzzlepiece.extension.fill" },
    { type: "if-else",       label: "If / Else", icon: "arrow.triangle.branch" },
    { type: "while",         label: "Loop",      icon: "arrow.triangle.2.circlepath" },
    { type: "parallel",      label: "Parallel",  icon: "arrow.trianglehead.branch" },
    { type: "join",          label: "Join",      icon: "arrow.trianglehead.merge" },
    { type: "user-approval", label: "Approval",  icon: "hand.raised.fill" },
    { type: "user-input",    label: "Ask user",  icon: "text.cursor" },
    { type: "guardrails",    label: "Guardrails",icon: "shield.lefthalf.filled" },
    { type: "transform",     label: "Transform", icon: "function" },
    { type: "note",          label: "Note",      icon: "note.text" },
    { type: "end",           label: "End",       icon: "stop.circle.fill" },
  ];
  var TYPE_ICON = { start: "play.circle.fill" };
  NODE_TYPES.forEach(function (t) { TYPE_ICON[t.type] = t.icon; });
  function typeLabel(t) {
    if (t === "start") return "Start";
    var m = NODE_TYPES.find(function (n) { return n.type === t; });
    return m ? m.label : t;
  }

  // --- module state (reset on each open) ---
  var host = "";   // laptop host the workflow lives on
  var wf = null;   // the loaded workflow {id,name,nodes[],edges[]}
  var dom = null;  // cached overlay elements
  var sel = null;  // { kind:"node"|"edge", id }
  var saveTimer = null, saving = false, dirtyWhileSaving = false;
  var pan = { x: 0, y: 0 };
  var zoom = 1;            // canvas scale (0.3–2); world transform is translate then scale
  var idSeq = 0;
  var undoStack = [], redoStack = []; // graph snapshots (JSON) for undo/redo
  var suppressSnapshot = false;       // guard so applying an undo doesn't push a new snapshot
  var running = false, runAbort = null; // a Preview run is in flight + its fetch AbortController
  var curRunId = "";                     // current run id (from the run_id event), for HITL /resume
  var lastRunInput = "";                  // remembers the previous Run input (prefilled in the run modal)
  var caps = null;                        // {agents,tools,mcp} capability lists from the connector (for node dropdowns)
  var models = null;                      // [{id,name}] servable models from the connector (for the model picker)
  var runtime = null;                     // {provider,model,reasoning,providers,reasoningLevels} — the run settings switcher
  var runtimeAway = null;                 // document pointerdown handler that closes the runtime popover on an outside click
  var evalsDatasetCache = "";            // remembers the evals dataset text within a page session

  function base() { return laptopBase(host) + "/api/workflows"; }
  function uid(prefix) { idSeq += 1; return prefix + "_" + Date.now().toString(36) + idSeq.toString(36); }
  function truncate(s, n) { s = String(s); return s.length > n ? s.slice(0, n - 1) + "…" : s; }
  function cssEsc(s) { return String(s).replace(/["\\]/g, "\\$&"); }

  // ------------------------------------------------------------------ DOM build
  function ensureDOM() {
    if (dom) return dom;
    var root = el("div", "wf-builder hidden"); root.id = "wfBuilder";

    var bar = el("div", "wf-toolbar");
    var name = el("input", "wf-name"); name.type = "text"; name.placeholder = "Workflow name";
    name.oninput = function () { if (wf) { wf.name = name.value; scheduleSave(); } };

    var palette = el("div", "wf-palette");
    NODE_TYPES.forEach(function (t) {
      var b = el("button", "wf-pal-btn"); b.type = "button"; b.title = "Add " + t.label;
      b.appendChild(sym(t.icon)); b.appendChild(el("span", null, t.label));
      b.onclick = function () { addNode(t.type); };
      palette.appendChild(b);
    });

    var saveState = el("span", "wf-save");
    var newBtn = el("button", "wf-btn"); newBtn.type = "button"; newBtn.title = "Create a new agent or skill on this machine";
    newBtn.appendChild(sym("plus")); newBtn.appendChild(el("span", null, "New"));
    newBtn.onclick = function () { showCreateForm("agent"); };
    var runBtn = el("button", "wf-btn wf-run-btn"); runBtn.type = "button"; runBtn.title = "Run this workflow (Preview)";
    runBtn.appendChild(sym("play.fill")); runBtn.appendChild(el("span", null, "Run"));
    runBtn.onclick = toggleRun;
    var evalBtn = el("button", "wf-btn"); evalBtn.type = "button"; evalBtn.title = "Evaluate over a dataset";
    evalBtn.appendChild(sym("checklist")); evalBtn.appendChild(el("span", null, "Evals"));
    evalBtn.onclick = toggleEvals;
    var tidyBtn = el("button", "wf-btn"); tidyBtn.type = "button"; tidyBtn.title = "Auto-arrange nodes left→right";
    tidyBtn.appendChild(sym("rectangle.3.group")); tidyBtn.appendChild(el("span", null, "Tidy"));
    tidyBtn.onclick = autoLayout;
    var histBtn = el("button", "wf-btn"); histBtn.type = "button"; histBtn.title = "Run history";
    histBtn.appendChild(sym("clock.arrow.circlepath")); histBtn.appendChild(el("span", null, "History"));
    histBtn.onclick = toggleHistory;
    // Runtime switcher: provider / model / reasoning that runs use (drives s.cfg).
    var runtimeBtn = el("button", "wf-btn wf-runtime-btn"); runtimeBtn.type = "button"; runtimeBtn.title = "Provider · model · reasoning for runs";
    runtimeBtn.appendChild(sym("cpu"));
    var runtimeLabel = el("span", "wf-runtime-label", "Model"); runtimeBtn.appendChild(runtimeLabel);
    runtimeBtn.appendChild(sym("chevron.down", "wf-runtime-caret"));
    runtimeBtn.onclick = toggleRuntimeMenu;
    // Row 1: name, then the action cluster (provider, New, Tidy, History, Evals, Run).
    var row1 = el("div", "wf-toolbar-row");
    row1.appendChild(name);
    row1.appendChild(el("span", "wf-spacer")); row1.appendChild(saveState);
    row1.appendChild(runtimeBtn); row1.appendChild(newBtn); row1.appendChild(tidyBtn); row1.appendChild(histBtn); row1.appendChild(evalBtn); row1.appendChild(runBtn);
    // Row 2: the node palette.
    var row2 = el("div", "wf-toolbar-row wf-toolbar-palette");
    row2.appendChild(el("span", "wf-palette-label", "Add:")); row2.appendChild(palette);
    bar.appendChild(row1); bar.appendChild(row2);

    var stage = el("div", "wf-stage");
    var world = el("div", "wf-world");
    var svg = document.createElementNS(SVGNS, "svg"); svg.setAttribute("class", "wf-edges");
    world.appendChild(svg); stage.appendChild(world);

    // Zoom controls (bottom-left) + minimap (bottom-right), overlaid on the stage.
    var zoomCtl = el("div", "wf-zoom");
    var zOut = el("button", "wf-zoom-btn"); zOut.type = "button"; zOut.textContent = "−"; zOut.title = "Zoom out"; zOut.onclick = function () { zoomBy(1 / 1.2); };
    var zFit = el("button", "wf-zoom-btn"); zFit.type = "button"; zFit.appendChild(sym("arrow.up.left.and.arrow.down.right")); zFit.title = "Reset zoom"; zFit.onclick = resetView;
    var zIn = el("button", "wf-zoom-btn"); zIn.type = "button"; zIn.textContent = "+"; zIn.title = "Zoom in"; zIn.onclick = function () { zoomBy(1.2); };
    zoomCtl.appendChild(zOut); zoomCtl.appendChild(zFit); zoomCtl.appendChild(zIn);
    stage.appendChild(zoomCtl);
    var mini = document.createElementNS(SVGNS, "svg"); mini.setAttribute("class", "wf-minimap");
    stage.appendChild(mini);

    var config = el("div", "wf-config hidden");
    // Resize handle: a full-width bar (its own grid row) just above the trace
    // panel. It lives outside the scroll area so it's always visible and easy to
    // grab; CSS shows it only when the trace is open.
    var traceHandle = el("div", "wf-trace-handle"); traceHandle.title = "Drag to resize the trace panel";
    traceHandle.addEventListener("pointerdown", onTraceResize);
    var trace = el("div", "wf-trace hidden");
    var evals = el("div", "wf-evals hidden");

    root.appendChild(bar); root.appendChild(stage); root.appendChild(config); root.appendChild(traceHandle); root.appendChild(trace); root.appendChild(evals);
    // Mount inside the main panel (.transcript-wrap) so the builder fills the
    // conversation area while the header + sidebar stay usable, rather than as a
    // full-screen overlay over everything.
    var parent = document.querySelector(".transcript-wrap") || document.body;
    parent.appendChild(root);

    stage.addEventListener("pointerdown", onStagePointerDown);
    stage.addEventListener("wheel", onWheel, { passive: false });
    document.addEventListener("keydown", onKeyDown);

    dom = { root: root, toolbar: bar, name: name, saveState: saveState, runBtn: runBtn, runtimeLabel: runtimeLabel, stage: stage, world: world, svg: svg, mini: mini, config: config, trace: trace, evals: evals };
    return dom;
  }

  // ------------------------------------------------------------------ open/close
  async function open(h, wid) {
    host = h || currentLaptop || "";
    ensureDOM();
    if (runAbort) runAbort.abort(); // stop any in-flight run from a previous workflow
    setRunning(false);
    curRunId = "";
    closeRuntimeMenu(); // a popover left open on the previous workflow would dangle its listener
    caps = null; models = null; runtime = null; // refetch per machine (providers/models can differ)
    dom.trace.classList.add("hidden"); dom.trace.innerHTML = "";
    dom.evals.classList.add("hidden"); dom.evals.innerHTML = "";
    wf = null; // drop any previously-open graph until this one loads
    undoStack = []; redoStack = [];
    pan = { x: 0, y: 0 }; zoom = 1;
    dom.world.style.transform = "translate(0px,0px) scale(1)";
    sel = null; dom.config.classList.add("hidden");
    setStageMsg("");
    Array.prototype.slice.call(dom.world.querySelectorAll(".wf-node")).forEach(function (n) { n.remove(); });
    Array.prototype.slice.call(dom.svg.querySelectorAll(".wf-edge")).forEach(function (p) { p.remove(); });
    dom.saveState.textContent = "Loading…";
    dom.root.classList.remove("hidden");
    document.body.classList.add("wf-active"); // hides the chat status/input bars while the builder is open
    var loaded;
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wid), { headers: { Accept: "application/json" } });
      if (!r.ok) throw new Error("HTTP " + r.status);
      loaded = await r.json();
    } catch (e) {
      dom.saveState.textContent = "Couldn't load";
      setStageMsg("Couldn't load this workflow. The machine may be running an older version of the connector.");
      return;
    }
    wf = loaded;
    if (!wf.nodes) wf.nodes = [];
    if (!wf.edges) wf.edges = [];
    dom.name.value = wf.name || "";
    dom.saveState.textContent = "";
    render();
    loadCapabilities(); // populate Agent/Tool/MCP node dropdowns from the connector
    // Prefetch the model list so the agent Model field is a picker; refresh the
    // open inspector once it lands.
    loadModels().then(function () { if (wf && sel && sel.kind === "node") openConfig(); });
    loadRuntime(); // populate the provider/model/reasoning switcher label
  }

  // loadCapabilities fetches this connector's real subagents, built-in tools, and
  // connected MCP tools so the node config selectors offer actual choices. Best
  // effort: a failure just leaves the fields as free text.
  async function loadCapabilities() {
    try {
      var r = await fetch(base() + "/capabilities", { headers: { Accept: "application/json" } });
      if (r.ok) caps = await r.json();
    } catch (_) { /* free-text fallback */ }
  }

  // loadModels fetches the connector's servable models (for the New-agent picker
  // and the agent-node Model field), cached on `models` for the page session.
  // Best effort: a failure leaves `models` null and the UI falls back to free text.
  async function loadModels() {
    if (models) return models;
    try {
      var r = await fetch(base() + "/models", { headers: { Accept: "application/json" } });
      if (r.ok) models = await r.json();
    } catch (_) { /* free-text fallback */ }
    return models;
  }

  // ----------------------------------------------------------- runtime switcher
  // loadRuntime fetches the connector's current provider/model/reasoning (what a
  // run uses) plus the available providers + reasoning levels, and updates the
  // toolbar button label. Best effort.
  async function loadRuntime() {
    try {
      var r = await fetch(base() + "/runtime", { headers: { Accept: "application/json" } });
      if (r.ok) { runtime = await r.json(); updateRuntimeLabel(); }
    } catch (_) { /* leave the default label */ }
    return runtime;
  }

  function updateRuntimeLabel() {
    if (!dom || !dom.runtimeLabel) return;
    if (!runtime) { dom.runtimeLabel.textContent = "Model"; return; }
    var name = (typeof prettyModel === "function") ? prettyModel(runtime.model) : runtime.model;
    var r = runtime.reasoning && runtime.reasoning !== "off" ? " · " + runtime.reasoning : "";
    dom.runtimeLabel.textContent = name + r;
  }

  // toggleRuntimeMenu opens (or closes) the provider/model/reasoning popover.
  function toggleRuntimeMenu() {
    if (document.getElementById("wfRuntimeMenu")) { closeRuntimeMenu(); return; }
    if (!runtime) { loadRuntime().then(function () { if (runtime) toggleRuntimeMenu(); }); return; }
    openRuntimeMenu();
    if (!models) loadModels().then(function () { if (document.getElementById("wfRuntimeMenu")) openRuntimeMenu(); });
    runtimeAway = function (e) {
      var m = document.getElementById("wfRuntimeMenu");
      if (!m || m.contains(e.target) || (e.target.closest && e.target.closest(".wf-runtime-btn"))) return;
      closeRuntimeMenu();
    };
    document.addEventListener("pointerdown", runtimeAway, true);
  }
  function closeRuntimeMenu() {
    var m = document.getElementById("wfRuntimeMenu");
    if (m) m.remove();
    if (runtimeAway) { document.removeEventListener("pointerdown", runtimeAway, true); runtimeAway = null; }
  }

  // openRuntimeMenu (re)builds the popover from the current runtime + models state.
  function openRuntimeMenu() {
    var old = document.getElementById("wfRuntimeMenu");
    if (old) old.remove();
    if (!runtime) return;
    var menu = el("div", "wf-runtime-menu"); menu.id = "wfRuntimeMenu";

    // Provider.
    menu.appendChild(runtimeSelect("Provider", (runtime.providers || []).map(function (p) { return [p.id, p.label]; }), runtime.provider, function (v) { setRuntime({ provider: v }); }));
    // Model — from the live model list; fall back to just the current one.
    var modelOpts = (models && models.length) ? models.map(function (m) { return [m.id, (typeof prettyModel === "function" ? prettyModel(m.id) : (m.name || m.id))]; }) : [[runtime.model, (typeof prettyModel === "function" ? prettyModel(runtime.model) : runtime.model)]];
    menu.appendChild(runtimeSelect("Model", modelOpts, runtime.model, function (v) { setRuntime({ model: v }); }));
    if (!models) menu.appendChild(el("div", "wf-hint", "Loading models…"));
    // Reasoning — model-specific levels.
    menu.appendChild(runtimeSelect("Reasoning", (runtime.reasoningLevels || []).map(function (l) { return [l, l]; }), runtime.reasoning, function (v) { setRuntime({ reasoning: v }); }));

    var msg = el("div", "wf-runtime-msg hidden"); msg.id = "wfRuntimeMsg"; menu.appendChild(msg);
    // Sit just below the toolbar (its height varies — two rows, and wraps on
    // narrow widths), so the popover never overlaps the palette row.
    if (dom.toolbar) menu.style.top = (dom.toolbar.offsetHeight + 4) + "px";
    dom.root.appendChild(menu);
  }

  function runtimeSelect(label, options, current, onChange) {
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var sel = el("select", "wf-input");
    options.forEach(function (o) {
      var opt = el("option", null, o[1]); opt.value = o[0]; if (o[0] === current) opt.selected = true;
      sel.appendChild(opt);
    });
    // A current value not in the option list (e.g. a stale model) is preserved.
    if (current && !options.some(function (o) { return o[0] === current; })) {
      var cur = el("option", null, current + " (current)"); cur.value = current; cur.selected = true; sel.appendChild(cur);
    }
    sel.onchange = function () { onChange(sel.value); };
    wrap.appendChild(sel);
    return wrap;
  }

  function runtimeMsg(text, ok) {
    var m = document.getElementById("wfRuntimeMsg");
    if (!m) return;
    m.textContent = text || ""; m.classList.toggle("hidden", !text); m.classList.toggle("is-ok", !!ok);
  }

  // setRuntime POSTs a provider/model/reasoning change to the connector and
  // refreshes the switcher from the response (reasoning levels can change with the
  // model). On an SQE-Proxy denial it refreshes membership and retries once.
  async function setRuntime(patch, retried) {
    runtimeMsg("Applying…");
    try {
      var r = await fetch(base() + "/runtime", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      if (!r.ok) {
        var txt = (await r.text()).trim();
        if (!retried && isProxyDenied(txt)) {
          runtimeMsg("Refreshing SQE Proxy access…");
          try {
            var rr = await fetch("/api/refresh-membership", { method: "POST" });
            var d = rr.ok ? await rr.json().catch(function () { return {}; }) : {};
            if (typeof authUser !== "undefined" && authUser) authUser.in_group = !!d.in_group;
            if (d.in_group) { return setRuntime(patch, true); }
          } catch (_) { /* fall through */ }
        }
        runtimeMsg(txt || ("HTTP " + r.status));
        return;
      }
      runtime = await r.json();
      updateRuntimeLabel();
      // A provider/model change can change the servable model list → refetch, then
      // rebuild the menu so the model select reflects it.
      if (patch.provider || patch.model) { models = null; loadModels().then(function () { if (document.getElementById("wfRuntimeMenu")) openRuntimeMenu(); }); }
      else openRuntimeMenu();
    } catch (e) {
      runtimeMsg("Couldn't reach the machine: " + (e && e.message));
    }
  }

  // setStageMsg shows (or clears) a centered note over the canvas — used for the
  // load-failure state so a null-workflow canvas isn't a silent mystery.
  function setStageMsg(text) {
    var prev = dom.stage.querySelector(".wf-stage-msg");
    if (prev) prev.remove();
    if (text) dom.stage.appendChild(el("div", "wf-stage-msg", text));
  }

  function close() {
    if (runAbort) runAbort.abort(); // stop any in-flight Preview run
    flushSave();
    closeRuntimeMenu();
    if (dom) dom.root.classList.add("hidden");
    document.body.classList.remove("wf-active");
    wf = null;
  }

  // ------------------------------------------------------------------ rendering
  function render() {
    if (!wf) return;
    Array.prototype.slice.call(dom.world.querySelectorAll(".wf-node")).forEach(function (n) { n.remove(); });
    wf.nodes.forEach(function (node) { dom.world.appendChild(nodeEl(node)); });
    redrawEdges();
    drawMinimap();
  }

  function nodeEl(node) {
    var n = el("div", "wf-node wf-node-" + node.type);
    n.dataset.id = node.id;
    n.style.left = (node.position.x || 0) + "px";
    n.style.top = (node.position.y || 0) + "px";
    if (sel && sel.kind === "node" && sel.id === node.id) n.classList.add("selected");

    var head = el("div", "wf-node-head");
    head.appendChild(sym(TYPE_ICON[node.type] || "circle", "wf-node-icon"));
    head.appendChild(el("span", "wf-node-label", (node.data && node.data.label) ? node.data.label : typeLabel(node.type)));
    n.appendChild(head);

    var sub = nodeSubtitle(node);
    if (sub) n.appendChild(el("div", "wf-node-sub", sub));

    // Input handles: left (default) + top, so edges can enter horizontally or
    // vertically. Both carry data-side so an edge remembers which it landed on.
    if (node.type !== "start") {
      var hin = el("div", "wf-handle wf-handle-in side-left"); hin.dataset.node = node.id; hin.dataset.side = "left"; n.appendChild(hin);
      var hinT = el("div", "wf-handle wf-handle-in side-top"); hinT.dataset.node = node.id; hinT.dataset.side = "top"; n.appendChild(hinT);
    }
    // Output handles: branching nodes (if-else / while) expose two labeled ports
    // whose handle name becomes the edge's sourceHandle (which the engine routes
    // on); every other non-terminal node has a single right output plus a bottom
    // output (same handle name "") for vertical / top-down layouts.
    outHandles(node.type).forEach(function (h) {
      var ho = el("div", "wf-handle wf-handle-out side-right" + (h.cls ? " " + h.cls : ""));
      ho.dataset.node = node.id; ho.dataset.handle = h.handle; ho.dataset.side = "right";
      if (h.label) { ho.title = h.label; ho.appendChild(el("span", "wf-handle-label", h.label)); }
      ho.addEventListener("pointerdown", function (ev) { startConnect(ev, node.id, h.handle, "right"); });
      n.appendChild(ho);
    });
    if (node.type !== "end" && node.type !== "note") {
      // A single bottom output for the primary route (branch nodes keep their
      // labeled right ports; the bottom port maps to the first/only handle name).
      var hb = el("div", "wf-handle wf-handle-out side-bottom");
      var primary = (outHandles(node.type)[0] || { handle: "" }).handle;
      hb.dataset.node = node.id; hb.dataset.handle = primary; hb.dataset.side = "bottom";
      hb.addEventListener("pointerdown", function (ev) { startConnect(ev, node.id, primary, "bottom"); });
      n.appendChild(hb);
    }
    n.addEventListener("pointerdown", function (ev) { onNodePointerDown(ev, node); });
    return n;
  }

  // outHandles returns the output ports for a node type: two labeled branches for
  // if-else/while, a single unlabeled port for other non-terminal nodes, none for
  // end/note.
  function outHandles(type) {
    if (type === "end" || type === "note") return [];
    if (type === "if-else") return [{ handle: "if", label: "if", cls: "branch-1" }, { handle: "else", label: "else", cls: "branch-2" }];
    if (type === "while") return [{ handle: "continue", label: "loop", cls: "branch-1" }, { handle: "break", label: "done", cls: "branch-2" }];
    return [{ handle: "", label: "", cls: "" }];
  }

  function nodeSubtitle(node) {
    var d = node.data || {};
    switch (node.type) {
      case "agent": {
        if (d.agent) return "↳ " + d.agent;
        var tags = [];
        if (d.skills && d.skills.length) tags.push("✦ " + d.skills.length + " skill" + (d.skills.length === 1 ? "" : "s"));
        if (d.outputSchema) tags.push("⊟ structured");
        if (d.requireApproval) tags.push("⏸ approval");
        if (tags.length) return tags.join(" · ");
        return d.instructions ? truncate(d.instructions, 60) : "";
      }
      case "worker": return d.worker ? "⇲ " + d.worker : "";
      case "skill": return d.skill ? "✦ " + d.skill : "";
      case "tool":
      case "mcp": return d.tool || "";
      case "guardrails": {
        var on = [];
        if (d.guardPii) on.push("PII");
        if (d.guardJailbreak) on.push("jailbreak");
        if (d.guardModeration) on.push("moderation");
        return on.length ? on.join(" · ") + (d.guardLlmJudge ? " +judge" : "") : "no checks";
      }
      case "if-else":
      case "while": return d.condition ? truncate(d.condition, 50) : "";
      case "parallel": return d.noFailFast ? "fan out · collect" : "fan out · fail-fast";
      case "join": return "merge · " + (d.joinMode || "concat");
      case "user-approval": return d.message ? truncate(d.message, 50) : "";
      case "user-input": return d.message ? truncate(d.message, 50) : "free-text input";
      case "transform": return d.transform ? truncate(d.transform, 50) : "";
      case "note": return d.text ? truncate(d.text, 80) : "";
      default: return "";
    }
  }

  // Edge endpoints come from the live handle DOM rects (world-local coords), so
  // they track node size and stay correct under pan without a redraw. `which` is
  // "in"/"out"; `handle` selects a branch output port; `side` ("left/right/top/
  // bottom") selects which physical handle to use and is returned so the curve can
  // bow toward that edge. Falls back to any matching handle if the exact side is
  // absent (e.g. older saved edges with no side recorded).
  function handleCenter(nodeId, which, handle, side) {
    var nEl = dom.world.querySelector('.wf-node[data-id="' + cssEsc(nodeId) + '"]');
    if (!nEl) return null;
    var h = null;
    if (which === "in") {
      if (side) h = nEl.querySelector('.wf-handle-in[data-side="' + cssEsc(side) + '"]');
      if (!h) h = nEl.querySelector(".wf-handle-in");
    } else {
      var hsel = handle ? '.wf-handle-out[data-handle="' + cssEsc(handle) + '"]' : ".wf-handle-out";
      if (side) h = nEl.querySelector('.wf-handle-out[data-side="' + cssEsc(side) + '"]' + (handle ? '[data-handle="' + cssEsc(handle) + '"]' : ""));
      if (!h) h = nEl.querySelector(hsel);
    }
    if (!h) return null;
    var wr = dom.world.getBoundingClientRect(), hr = h.getBoundingClientRect();
    // world.getBoundingClientRect().left is where world-x=0 renders; screen
    // offsets are scaled by zoom, so divide to recover world (svg) coordinates.
    return { x: (hr.left + hr.width / 2 - wr.left) / zoom, y: (hr.top + hr.height / 2 - wr.top) / zoom, side: h.dataset.side || side || "" };
  }

  // bezier draws a smooth cubic between two endpoints, bowing the control points
  // out along each endpoint's side (right/left → horizontal, bottom/top → vertical)
  // so vertical handles route top-down and horizontal ones left-right.
  function bezier(p0, p1) {
    var d = Math.max(40, Math.hypot(p1.x - p0.x, p1.y - p0.y) / 2);
    var c0 = ctrl(p0, p0.side, d), c1 = ctrl(p1, p1.side, d);
    return "M " + p0.x + " " + p0.y + " C " + c0.x + " " + c0.y + " " + c1.x + " " + c1.y + " " + p1.x + " " + p1.y;
  }
  // ctrl offsets a control point away from the endpoint along its handle side.
  function ctrl(p, side, d) {
    switch (side) {
      case "bottom": return { x: p.x, y: p.y + d };
      case "top": return { x: p.x, y: p.y - d };
      case "left": return { x: p.x - d, y: p.y };
      default: return { x: p.x + d, y: p.y }; // right (or unknown) → outward right
    }
  }

  function redrawEdges() {
    if (!wf) return;
    Array.prototype.slice.call(dom.svg.querySelectorAll(".wf-edge, .wf-edge-hit, .wf-edge-label, .wf-edge-del")).forEach(function (p) { p.remove(); });
    wf.edges.forEach(function (edge) {
      var a = handleCenter(edge.source, "out", edge.sourceHandle, edge.sourceSide), b = handleCenter(edge.target, "in", null, edge.targetSide);
      if (!a || !b) return;
      var d = bezier(a, b);
      var selected = sel && sel.kind === "edge" && sel.id === edge.id;

      // Invisible wide hit-path BEHIND the visible edge: a thin 2px stroke is
      // almost impossible to click, so this ~20px transparent stroke makes the
      // whole edge easy to select (it carries the pointer events; the visible
      // path stays non-interactive).
      var hit = document.createElementNS(SVGNS, "path");
      hit.setAttribute("class", "wf-edge-hit");
      hit.setAttribute("d", d);
      hit.dataset.id = edge.id;
      hit.addEventListener("pointerdown", function (ev) { ev.stopPropagation(); selectEdge(edge.id); });
      dom.svg.appendChild(hit);

      var path = document.createElementNS(SVGNS, "path");
      path.setAttribute("class", "wf-edge" + (selected ? " selected" : ""));
      path.setAttribute("d", d);
      path.dataset.id = edge.id;
      dom.svg.appendChild(path);

      // Edge label: explicit edge.label, else the branch handle (if/else/loop/done).
      var labelText = edge.label || edge.sourceHandle || "";
      var mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2;
      if (labelText) {
        var t = document.createElementNS(SVGNS, "text");
        t.setAttribute("class", "wf-edge-label");
        t.setAttribute("x", mx); t.setAttribute("y", my - 4);
        t.setAttribute("text-anchor", "middle");
        t.textContent = labelText;
        dom.svg.appendChild(t);
      }

      // When selected, show a clickable ✕ at the edge midpoint so it can be
      // deleted without the keyboard.
      if (selected) {
        var g = document.createElementNS(SVGNS, "g");
        g.setAttribute("class", "wf-edge-del");
        g.setAttribute("transform", "translate(" + mx + " " + my + ")");
        var circ = document.createElementNS(SVGNS, "circle");
        circ.setAttribute("r", "11");
        g.appendChild(circ);
        var x1 = document.createElementNS(SVGNS, "path");
        x1.setAttribute("d", "M -4 -4 L 4 4 M 4 -4 L -4 4");
        g.appendChild(x1);
        g.addEventListener("pointerdown", function (ev) { ev.stopPropagation(); sel = { kind: "edge", id: edge.id }; deleteSelected(); });
        dom.svg.appendChild(g);
      }
    });
  }

  // ------------------------------------------------------------------ interactions
  function clientToWorld(ev) {
    var wr = dom.world.getBoundingClientRect();
    return { x: (ev.clientX - wr.left) / zoom, y: (ev.clientY - wr.top) / zoom };
  }

  // applyTransform writes the world's translate+scale and refreshes the minimap.
  function applyTransform() {
    dom.world.style.transform = "translate(" + pan.x + "px," + pan.y + "px) scale(" + zoom + ")";
    drawMinimap();
  }

  // zoomBy scales around the stage center, keeping that point fixed on screen.
  function zoomBy(factor) {
    var sr = dom.stage.getBoundingClientRect();
    var cx = sr.width / 2, cy = sr.height / 2;
    var nz = Math.max(0.3, Math.min(2, zoom * factor));
    pan.x = cx - (cx - pan.x) * (nz / zoom);
    pan.y = cy - (cy - pan.y) * (nz / zoom);
    zoom = nz;
    applyTransform();
  }

  // onWheel: ctrl/⌘+wheel (or plain wheel) zooms around the cursor.
  function onWheel(ev) {
    ev.preventDefault();
    var sr = dom.stage.getBoundingClientRect();
    var mx = ev.clientX - sr.left, my = ev.clientY - sr.top;
    var nz = Math.max(0.3, Math.min(2, zoom * (ev.deltaY < 0 ? 1.1 : 1 / 1.1)));
    pan.x = mx - (mx - pan.x) * (nz / zoom);
    pan.y = my - (my - pan.y) * (nz / zoom);
    zoom = nz;
    applyTransform();
  }

  function resetView() { zoom = 1; pan = { x: 0, y: 0 }; applyTransform(); }

  // drawMinimap renders a scaled overview of all nodes plus the current viewport
  // rectangle, into the bottom-right SVG. Cheap; redrawn on transform/edit.
  function drawMinimap() {
    var mini = dom.mini;
    if (!mini || !wf || !wf.nodes.length) { if (mini) mini.innerHTML = ""; return; }
    var W = 160, H = 110, NW = 180, NH = 56; // mini box + assumed node size (world units)
    // Content bounds in world coords.
    var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    wf.nodes.forEach(function (n) {
      var x = n.position.x || 0, y = n.position.y || 0;
      minX = Math.min(minX, x); minY = Math.min(minY, y);
      maxX = Math.max(maxX, x + NW); maxY = Math.max(maxY, y + NH);
    });
    var sr = dom.stage.getBoundingClientRect();
    // Include the viewport in the bounds so the indicator is always visible.
    var vx0 = -pan.x / zoom, vy0 = -pan.y / zoom, vx1 = vx0 + sr.width / zoom, vy1 = vy0 + sr.height / zoom;
    minX = Math.min(minX, vx0); minY = Math.min(minY, vy0); maxX = Math.max(maxX, vx1); maxY = Math.max(maxY, vy1);
    var spanX = Math.max(1, maxX - minX), spanY = Math.max(1, maxY - minY);
    var sc = Math.min(W / spanX, H / spanY);
    mini.setAttribute("viewBox", "0 0 " + W + " " + H);
    var parts = [];
    var mapX = function (x) { return (x - minX) * sc; }, mapY = function (y) { return (y - minY) * sc; };
    wf.nodes.forEach(function (n) {
      parts.push('<rect x="' + mapX(n.position.x || 0).toFixed(1) + '" y="' + mapY(n.position.y || 0).toFixed(1) +
        '" width="' + (NW * sc).toFixed(1) + '" height="' + (NH * sc).toFixed(1) + '" rx="1.5" class="wf-mini-node"/>');
    });
    parts.push('<rect x="' + mapX(vx0).toFixed(1) + '" y="' + mapY(vy0).toFixed(1) +
      '" width="' + ((vx1 - vx0) * sc).toFixed(1) + '" height="' + ((vy1 - vy0) * sc).toFixed(1) + '" class="wf-mini-view"/>');
    mini.innerHTML = parts.join("");
  }

  // ----- undo / redo (graph snapshots) -----
  function snapshot() {
    if (suppressSnapshot || !wf) return;
    undoStack.push(JSON.stringify({ nodes: wf.nodes, edges: wf.edges }));
    if (undoStack.length > 50) undoStack.shift();
    redoStack = [];
  }
  function restoreGraph(s) {
    var g = JSON.parse(s);
    wf.nodes = g.nodes; wf.edges = g.edges;
    suppressSnapshot = true; render(); suppressSnapshot = false;
    scheduleSave();
  }
  function undo() {
    if (!wf || !undoStack.length) return;
    redoStack.push(JSON.stringify({ nodes: wf.nodes, edges: wf.edges }));
    restoreGraph(undoStack.pop());
    dom.config.classList.add("hidden"); sel = null;
  }
  function redo() {
    if (!wf || !redoStack.length) return;
    undoStack.push(JSON.stringify({ nodes: wf.nodes, edges: wf.edges }));
    restoreGraph(redoStack.pop());
    dom.config.classList.add("hidden"); sel = null;
  }

  function onNodePointerDown(ev, node) {
    if (ev.target.classList && ev.target.classList.contains("wf-handle")) return; // handle → connect
    ev.stopPropagation();
    selectNode(node.id);
    var nEl = ev.currentTarget;
    var startMouse = { x: ev.clientX, y: ev.clientY };
    var startPos = { x: node.position.x || 0, y: node.position.y || 0 };
    var moved = false;
    function move(e) {
      if (!moved) { snapshot(); moved = true; } // record one undo step at drag start
      // Screen delta → world delta is /zoom.
      node.position.x = startPos.x + (e.clientX - startMouse.x) / zoom;
      node.position.y = startPos.y + (e.clientY - startMouse.y) / zoom;
      nEl.style.left = node.position.x + "px";
      nEl.style.top = node.position.y + "px";
      redrawEdges();
    }
    function up() {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", up);
      if (moved) { scheduleSave(); drawMinimap(); }
    }
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerup", up);
  }

  function startConnect(ev, nodeId, handle, sourceSide) {
    ev.stopPropagation(); ev.preventDefault();
    var temp = document.createElementNS(SVGNS, "path");
    temp.setAttribute("class", "wf-edge wf-edge-temp");
    dom.svg.appendChild(temp);
    var from = handleCenter(nodeId, "out", handle, sourceSide);
    function move(e) { if (from) temp.setAttribute("d", bezier(from, sidePoint(clientToWorld(e), ""))); }
    function up(e) {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", up);
      temp.remove();
      var tgt = document.elementFromPoint(e.clientX, e.clientY);
      // Prefer the exact input handle dropped on (so we record its side); else the node.
      var inHandle = tgt && tgt.closest ? tgt.closest(".wf-handle-in") : null;
      var nodeBox = tgt && tgt.closest ? tgt.closest(".wf-node") : null;
      var target = inHandle ? inHandle.dataset.node : (nodeBox ? nodeBox.dataset.id : null);
      var targetSide = inHandle ? (inHandle.dataset.side || "left") : "left";
      if (target && target !== nodeId) addEdge(nodeId, target, handle || "", sourceSide || "right", targetSide);
    }
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerup", up);
  }
  // sidePoint tags a bare {x,y} (the live cursor) with a side for bezier().
  function sidePoint(p, side) { p.side = side; return p; }

  function onStagePointerDown(ev) {
    if (ev.target.closest && (ev.target.closest(".wf-node") || ev.target.closest(".wf-edge") || ev.target.closest(".wf-edge-hit") || ev.target.closest(".wf-edge-del"))) return;
    deselect();
    var start = { x: ev.clientX, y: ev.clientY }, base0 = { x: pan.x, y: pan.y };
    function move(e) {
      pan.x = base0.x + (e.clientX - start.x);
      pan.y = base0.y + (e.clientY - start.y);
      applyTransform();
    }
    function up() {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", up);
    }
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerup", up);
  }

  function onKeyDown(e) {
    if (!dom || dom.root.classList.contains("hidden")) return;
    var a = document.activeElement;
    var typing = a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA");
    // Undo / redo (⌘/Ctrl+Z, ⌘/Ctrl+Shift+Z or Ctrl+Y) — but not while typing.
    if (!typing && (e.metaKey || e.ctrlKey) && (e.key === "z" || e.key === "Z")) {
      e.preventDefault();
      if (e.shiftKey) redo(); else undo();
      return;
    }
    if (!typing && (e.metaKey || e.ctrlKey) && (e.key === "y" || e.key === "Y")) { e.preventDefault(); redo(); return; }
    if (e.key === "Escape") { close(); return; }
    if ((e.key === "Delete" || e.key === "Backspace") && sel) {
      if (typing) return;
      e.preventDefault();
      deleteSelected();
    }
  }

  // ------------------------------------------------------------------ mutations
  function addNode(type) {
    if (!wf) return;
    snapshot();
    var wr = dom.world.getBoundingClientRect(), sr = dom.stage.getBoundingClientRect();
    var cx = (sr.left + sr.width / 2 - wr.left) / zoom, cy = (sr.top + sr.height / 2 - wr.top) / zoom;
    var node = { id: uid("n"), type: type, position: { x: cx - 90, y: cy - 24 }, data: { label: typeLabel(type) } };
    wf.nodes.push(node);
    dom.world.appendChild(nodeEl(node));
    selectNode(node.id);
    drawMinimap();
    scheduleSave();
  }

  function addEdge(source, target, handle, sourceSide, targetSide) {
    if (!wf) return;
    snapshot();
    // One edge per (source, handle): re-dragging a branch re-points it.
    wf.edges = wf.edges.filter(function (e) { return !(e.source === source && (e.sourceHandle || "") === (handle || "")); });
    wf.edges.push({ id: uid("e"), source: source, target: target, sourceHandle: handle || "", sourceSide: sourceSide || "right", targetSide: targetSide || "left" });
    redrawEdges();
    scheduleSave();
  }

  // autoLayout arranges nodes left→right by BFS depth from the Start node: each
  // graph distance becomes a column, nodes at the same depth stack into rows.
  // Unreached nodes (disconnected) are parked in a trailing column.
  function autoLayout() {
    if (!wf || !wf.nodes.length) return;
    snapshot();
    var COL = 240, ROW = 110, X0 = 60, Y0 = 60;
    var adj = {};
    wf.edges.forEach(function (e) { (adj[e.source] = adj[e.source] || []).push(e.target); });
    var start = wf.nodes.filter(function (n) { return n.type === "start"; })[0] || wf.nodes[0];
    var depth = {}; depth[start.id] = 0;
    var queue = [start.id];
    while (queue.length) {
      var id = queue.shift();
      (adj[id] || []).forEach(function (t) {
        if (depth[t] === undefined) { depth[t] = depth[id] + 1; queue.push(t); }
      });
    }
    var maxDepth = 0;
    Object.keys(depth).forEach(function (k) { if (depth[k] > maxDepth) maxDepth = depth[k]; });
    var rowIn = {}; // per-column row counter
    wf.nodes.forEach(function (n) {
      var d = depth[n.id]; if (d === undefined) d = maxDepth + 1; // disconnected → trailing column
      var row = rowIn[d] || 0; rowIn[d] = row + 1;
      n.position = { x: X0 + d * COL, y: Y0 + row * ROW };
    });
    resetView();
    render();
    scheduleSave();
  }

  function deleteSelected() {
    if (!wf || !sel) return;
    snapshot();
    if (sel.kind === "node") {
      var id = sel.id;
      if (id === "start") return; // keep the entry point
      wf.nodes = wf.nodes.filter(function (n) { return n.id !== id; });
      wf.edges = wf.edges.filter(function (e) { return e.source !== id && e.target !== id; });
    } else {
      var eid = sel.id;
      wf.edges = wf.edges.filter(function (e) { return e.id !== eid; });
    }
    deselect();
    render();
    scheduleSave();
  }

  // ------------------------------------------------------------------ selection + config
  function selectNode(id) { sel = { kind: "node", id: id }; refreshSelection(); openConfig(); }
  function selectEdge(id) { sel = { kind: "edge", id: id }; refreshSelection(); dom.config.classList.add("hidden"); }
  function deselect() { sel = null; refreshSelection(); dom.config.classList.add("hidden"); }

  function refreshSelection() {
    Array.prototype.slice.call(dom.world.querySelectorAll(".wf-node")).forEach(function (n) {
      n.classList.toggle("selected", !!(sel && sel.kind === "node" && sel.id === n.dataset.id));
    });
    redrawEdges();
  }

  function curNode() { return (sel && sel.kind === "node") ? wf.nodes.find(function (n) { return n.id === sel.id; }) : null; }

  function openConfig() {
    var node = curNode();
    if (!node) { dom.config.classList.add("hidden"); return; }
    var c = dom.config; c.innerHTML = ""; c.classList.remove("hidden");

    var head = el("div", "wf-config-head");
    head.appendChild(sym(TYPE_ICON[node.type] || "circle"));
    head.appendChild(el("span", null, typeLabel(node.type)));
    if (node.type !== "start") {
      var del = el("button", "wf-config-del"); del.type = "button"; del.title = "Delete node";
      del.appendChild(sym("trash"));
      del.onclick = function () { sel = { kind: "node", id: node.id }; deleteSelected(); };
      head.appendChild(del);
    }
    c.appendChild(head);

    if (node.type !== "start" && node.type !== "end") {
      c.appendChild(field("Label", node.data.label || "", function (v) { node.data.label = v; relabel(node); }));
    }
    // Retry-on-failure: applies to any executable node (not start/end/note).
    if (node.type !== "start" && node.type !== "end" && node.type !== "note") {
      c.appendChild(field("Retries on failure", node.data.retries || "", function (v) { node.data.retries = parseInt(v, 10) || 0; }, "0 = no retry (max 5)"));
    }
    switch (node.type) {
      case "agent":
        c.appendChild(combo("Subagent (optional)", node.data.agent || "", (caps && caps.agents) || [], function (v) { node.data.agent = v; relabel(node); }, "delegate to a saved persona (e.g. explore)"));
        c.appendChild(newCapLink("agent", "+ New agent…", function (name) { node.data.agent = name; relabel(node); scheduleSave(); }));
        c.appendChild(varArea("Instructions", node.data.instructions || "", node, function (v) { node.data.instructions = v; relabel(node); }));
        c.appendChild(modelField("Model (optional)", node.data.model || "", function (v) { node.data.model = v; }));
        c.appendChild(toolPicker("Tools (allowlist)", node.data.tools, function (v) { node.data.tools = v; }));
        c.appendChild(multiPicker("Skills (loadable)", node.data.skills, (caps && caps.skills) || [], function (v) { node.data.skills = v; }, "No skills found on this machine."));
        c.appendChild(area("Output JSON schema (optional)", node.data.outputSchema || "", function (v) { node.data.outputSchema = v; }, '{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}'));
        c.appendChild(checkbox("Require approval before running", !!node.data.requireApproval, function (v) { node.data.requireApproval = v; }));
        c.appendChild(el("div", "wf-hint", "Pauses the run for your OK before this step. The agent also pauses to ask you about mutating tools (e.g. bash, write) and any questions it raises."));
        break;
      case "worker":
        c.appendChild(combo("Worker", node.data.worker || "", (caps && caps.workers) || [], function (v) { node.data.worker = v; relabel(node); }, "out-of-process engine (e.g. claude, optimus)"));
        if (!caps || !caps.workers || !caps.workers.length) c.appendChild(el("div", "wf-hint", "No workers configured/detected on this machine — see Manage → Workers."));
        c.appendChild(varArea("Task (optional)", node.data.instructions || "", node, function (v) { node.data.instructions = v; relabel(node); }, "{{input}} — empty uses the upstream output as the task"));
        break;
      case "skill":
        c.appendChild(combo("Skill", node.data.skill || "", (caps && caps.skills) || [], function (v) { node.data.skill = v; relabel(node); }, "an Agent Skill to apply"));
        c.appendChild(newCapLink("skill", "+ New skill…", function (name) { node.data.skill = name; relabel(node); scheduleSave(); }));
        if (!caps || !caps.skills || !caps.skills.length) c.appendChild(el("div", "wf-hint", "No skills found on this machine — install one in Manage → Skills."));
        c.appendChild(varArea("Instructions (optional)", node.data.instructions || "", node, function (v) { node.data.instructions = v; relabel(node); }, "extra guidance; the upstream output is the input"));
        break;
      case "tool":
        c.appendChild(combo("Tool", node.data.tool || "", (caps && caps.tools) || [], function (v) { node.data.tool = v; relabel(node); }, "read_file / grep / glob / bash …"));
        c.appendChild(varArea("Arguments JSON (optional)", node.data.toolArgs || "", node, function (v) { node.data.toolArgs = v; }, '{"path":"{{input}}"}  — {{input}} / {{lastOutput}} / {{nodeId.field}}'));
        c.appendChild(el("div", "wf-hint", "Empty → uses the upstream input if it's JSON, else {}."));
        break;
      case "mcp":
        c.appendChild(combo("MCP tool", node.data.tool || "", (caps && caps.mcp) || [], function (v) { node.data.tool = v; relabel(node); }, "mcp.<server>.<tool> e.g. mcp.firecrawl.scrape"));
        if (!caps || !caps.mcp || !caps.mcp.length) c.appendChild(el("div", "wf-hint", "No MCP servers connected on this machine — enable one in Manage → MCP."));
        c.appendChild(varArea("Arguments JSON (optional)", node.data.toolArgs || "", node, function (v) { node.data.toolArgs = v; }, '{"url":"{{input}}"}'));
        break;
      case "guardrails":
        c.appendChild(checkbox("Detect PII", !!node.data.guardPii, function (v) { node.data.guardPii = v; relabel(node); }));
        c.appendChild(checkbox("Detect jailbreak / prompt-injection", !!node.data.guardJailbreak, function (v) { node.data.guardJailbreak = v; relabel(node); }));
        c.appendChild(checkbox("Moderation (LLM judge only)", !!node.data.guardModeration, function (v) { node.data.guardModeration = v; relabel(node); }));
        c.appendChild(checkbox("Add LLM-judge pass", !!node.data.guardLlmJudge, function (v) { node.data.guardLlmJudge = v; }));
        c.appendChild(selectField("On violation", node.data.actionOnViolation || "block", [["block", "Block (fail the run)"], ["warn", "Warn (continue)"]], function (v) { node.data.actionOnViolation = v; }));
        break;
      case "if-else":
        c.appendChild(varArea("Condition", node.data.condition || "", node, function (v) { node.data.condition = v; relabel(node); }, "{{lastOutput}} contains yes   ·   {{n1.score}} > 0.5"));
        c.appendChild(el("div", "wf-hint", "true → if branch, false → else. Ops: == != < <= > >= contains."));
        break;
      case "while":
        c.appendChild(varArea("Condition", node.data.condition || "", node, function (v) { node.data.condition = v; relabel(node); }, "{{count}} < 5"));
        c.appendChild(field("Max iterations", node.data.maxIterations || "", function (v) { node.data.maxIterations = parseInt(v, 10) || 0; }, "default 10"));
        c.appendChild(el("div", "wf-hint", "Wire the “loop” port back into the body, “done” to the exit."));
        break;
      case "parallel":
        c.appendChild(checkbox("Continue if a branch fails (collect mode)", !!node.data.noFailFast, function (v) { node.data.noFailFast = v; }));
        c.appendChild(el("div", "wf-hint", "Wire 2+ branches out of this node; they run concurrently and must reconverge on a single Join. Off (default) = fail-fast: one branch error cancels the siblings and fails the run. On = let the Join run on whichever branches succeed."));
        break;
      case "join":
        c.appendChild(selectField("Combine branch outputs", node.data.joinMode || "concat", [["concat", "Concatenate (separator)"], ["array", "JSON array"], ["first", "First branch only"]], function (v) { node.data.joinMode = v; relabel(node); openConfig(); }));
        if ((node.data.joinMode || "concat") === "concat") {
          c.appendChild(field("Separator", node.data.joinSeparator || "", function (v) { node.data.joinSeparator = v; }, "default: a --- divider"));
        }
        c.appendChild(el("div", "wf-hint", "Waits for every branch of its Parallel, then merges their outputs. Feed the result into an agent to synthesize."));
        break;
      case "user-approval":
        c.appendChild(varArea("Message", node.data.message || "", node, function (v) { node.data.message = v; relabel(node); }));
        break;
      case "user-input":
        c.appendChild(varArea("Prompt", node.data.message || "", node, function (v) { node.data.message = v; relabel(node); }, "What to ask the user for. Their typed answer becomes this node's output."));
        c.appendChild(el("div", "wf-hint", "Pauses the run for free-text input; the answer flows downstream as {{" + node.id + "}} / the next node's input."));
        break;
      case "transform":
        c.appendChild(varArea("Transform template", node.data.transform || "", node, function (v) { node.data.transform = v; relabel(node); }, "Summary: {{lastOutput}}\nScore: {{n1.score}}"));
        c.appendChild(el("div", "wf-hint", "Text with {{input}} / {{lastOutput}} / {{nodeId.field}} placeholders."));
        break;
      case "note":
        c.appendChild(area("Text", node.data.text || "", function (v) { node.data.text = v; relabel(node); }));
        break;
    }
  }

  function relabel(node) {
    var nEl = dom.world.querySelector('.wf-node[data-id="' + cssEsc(node.id) + '"]');
    if (nEl) {
      var lab = nEl.querySelector(".wf-node-label");
      if (lab) lab.textContent = node.data.label || typeLabel(node.type);
      var sub = nEl.querySelector(".wf-node-sub"), subTxt = nodeSubtitle(node);
      if (sub) { if (subTxt) sub.textContent = subTxt; else sub.remove(); }
      else if (subTxt) nEl.appendChild(el("div", "wf-node-sub", subTxt));
    }
  }

  function field(label, val, onInput, placeholder) {
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var i = el("input", "wf-input"); i.type = "text"; i.value = val; if (placeholder) i.placeholder = placeholder;
    i.onfocus = snapshot; // one undo step per edited field
    i.oninput = function () { onInput(i.value); scheduleSave(); };
    wrap.appendChild(i);
    return wrap;
  }
  var comboSeq = 0;
  // combo is field() backed by a <datalist> of {name,description} options from the
  // connector — real choices the user can pick, while staying free-typeable.
  function combo(label, val, options, onInput, placeholder) {
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var i = el("input", "wf-input"); i.type = "text"; i.value = val; if (placeholder) i.placeholder = placeholder;
    comboSeq += 1;
    var listId = "wfdl_" + comboSeq;
    i.setAttribute("list", listId);
    var dl = el("datalist"); dl.id = listId;
    (options || []).forEach(function (o) {
      var opt = el("option"); opt.value = o.name; if (o.description) opt.label = o.description;
      dl.appendChild(opt);
    });
    i.onfocus = snapshot;
    i.oninput = function () { onInput(i.value); scheduleSave(); };
    wrap.appendChild(i); wrap.appendChild(dl);
    return wrap;
  }
  // newCapLink is a small inline button under an agent/skill combo that opens the
  // create form on the right tab; on success the new capability name is passed to
  // onCreated so the node selects it. Returns a wrapper element.
  function newCapLink(kind, label, onCreated) {
    var wrap = el("div", "wf-newcap");
    var b = el("button", "wf-newcap-btn"); b.type = "button";
    b.appendChild(sym("plus.circle")); b.appendChild(el("span", null, label));
    b.onclick = function () { showCreateForm(kind, onCreated); };
    wrap.appendChild(b);
    return wrap;
  }
  // toolPicker is a checkbox list of built-in tools (the agent node's allowlist).
  // Empty selection = all tools (least surprise; matches the executor default).
  function toolPicker(label, selected, onChange) {
    selected = selected || [];
    var wrap = el("div", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var opts = (caps && caps.tools) || [];
    if (!opts.length) {
      // No capability list (older connector / fetch failed) → free-text fallback.
      return field(label + " (comma-separated)", selected.join(", "), function (v) {
        onChange(v.split(",").map(function (s) { return s.trim(); }).filter(Boolean));
      });
    }
    var box = el("div", "wf-toolpick");
    var sel = {}; selected.forEach(function (n) { sel[n] = true; });
    opts.forEach(function (o) {
      var row = el("label", "wf-check"); row.title = o.description || "";
      var cb = el("input"); cb.type = "checkbox"; cb.checked = !!sel[o.name];
      cb.onchange = function () {
        snapshot();
        sel[o.name] = cb.checked;
        onChange(Object.keys(sel).filter(function (k) { return sel[k]; }));
        scheduleSave();
      };
      row.appendChild(cb); row.appendChild(el("span", null, o.name));
      box.appendChild(row);
    });
    wrap.appendChild(box);
    wrap.appendChild(el("div", "wf-hint", "None checked = all tools available."));
    return wrap;
  }
  // multiPicker is a generic checkbox list over a capability list (name+desc),
  // storing the checked names as an array. emptyMsg shows when the list is empty.
  function multiPicker(label, selected, options, onChange, emptyMsg) {
    selected = selected || [];
    var wrap = el("div", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    if (!options || !options.length) {
      wrap.appendChild(el("div", "wf-hint", emptyMsg || "Nothing available."));
      return wrap;
    }
    var box = el("div", "wf-toolpick");
    var sel = {}; selected.forEach(function (n) { sel[n] = true; });
    options.forEach(function (o) {
      var row = el("label", "wf-check"); row.title = o.description || "";
      var cb = el("input"); cb.type = "checkbox"; cb.checked = !!sel[o.name];
      cb.onchange = function () {
        snapshot();
        sel[o.name] = cb.checked;
        onChange(Object.keys(sel).filter(function (k) { return sel[k]; }));
        scheduleSave();
      };
      row.appendChild(cb); row.appendChild(el("span", null, o.name));
      box.appendChild(row);
    });
    wrap.appendChild(box);
    return wrap;
  }
  function area(label, val, onInput, placeholder) {
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var t = el("textarea", "wf-area"); t.value = val; if (placeholder) t.placeholder = placeholder;
    t.onfocus = snapshot;
    t.oninput = function () { onInput(t.value); scheduleSave(); };
    wrap.appendChild(t);
    return wrap;
  }

  // availableVars returns the variable references usable in `node`'s templates:
  // the always-present input/lastOutput, plus every node that can reach `node`
  // (an upstream ancestor via the edges) as {{nodeId}} — and, for upstream agent
  // nodes with an Output JSON schema, each top-level schema field as {{id.field}}.
  function availableVars(node) {
    var vars = [
      { ref: "input", desc: "the workflow's initial input" },
      { ref: "lastOutput", desc: "the previous node's output" },
    ];
    if (!wf || !node) return vars;
    // Reverse-reachability: walk edges backward from node to find ancestors.
    var incoming = {};
    wf.edges.forEach(function (e) { (incoming[e.target] = incoming[e.target] || []).push(e.source); });
    var seen = {}, stack = (incoming[node.id] || []).slice();
    while (stack.length) {
      var id = stack.pop();
      if (seen[id]) continue;
      seen[id] = true;
      (incoming[id] || []).forEach(function (s) { stack.push(s); });
    }
    wf.nodes.forEach(function (n) {
      if (!seen[n.id] || n.type === "note") return;
      var label = (n.data && n.data.label) || typeLabel(n.type);
      vars.push({ ref: n.id, desc: label + " output" });
      // Schema-aware fields for structured agent outputs.
      if (n.type === "agent" && n.data && n.data.outputSchema) {
        try {
          var props = (JSON.parse(n.data.outputSchema) || {}).properties || {};
          Object.keys(props).forEach(function (k) { vars.push({ ref: n.id + "." + k, desc: label + " · " + k }); });
        } catch (_) { /* invalid schema → no field suggestions */ }
      }
    });
    return vars;
  }

  // varArea is area() plus an "Insert variable" menu that inserts a {{ref}} token
  // at the textarea's cursor — the wiring picker. `node` scopes which variables
  // are offered (only those reachable upstream).
  function varArea(label, val, node, onInput, placeholder) {
    var wrap = el("label", "wf-field");
    var head = el("div", "wf-field-head");
    head.appendChild(el("span", "wf-field-label", label));
    var pick = el("button", "wf-varbtn"); pick.type = "button";
    pick.appendChild(sym("curlybraces")); pick.appendChild(el("span", null, "Insert variable"));
    head.appendChild(pick);
    wrap.appendChild(head);

    var t = el("textarea", "wf-area"); t.value = val; if (placeholder) t.placeholder = placeholder;
    t.onfocus = snapshot;
    t.oninput = function () { onInput(t.value); scheduleSave(); };
    wrap.appendChild(t);

    var menu = el("div", "wf-varmenu hidden");
    availableVars(node).forEach(function (v) {
      var row = el("button", "wf-varrow"); row.type = "button";
      row.appendChild(el("code", "wf-varref", "{{" + v.ref + "}}"));
      if (v.desc) row.appendChild(el("span", "wf-vardesc", v.desc));
      row.onclick = function () {
        insertAtCursor(t, "{{" + v.ref + "}}");
        onInput(t.value); scheduleSave();
        menu.classList.add("hidden");
        t.focus();
      };
      menu.appendChild(row);
    });
    pick.onclick = function (e) { e.preventDefault(); menu.classList.toggle("hidden"); };
    wrap.appendChild(menu);
    return wrap;
  }

  // insertAtCursor inserts text at the textarea's caret (or appends), preserving
  // selection semantics, and leaves the caret after the inserted token.
  function insertAtCursor(t, text) {
    var s = t.selectionStart, e = t.selectionEnd;
    if (typeof s !== "number") { t.value += text; return; }
    t.value = t.value.slice(0, s) + text + t.value.slice(e);
    var pos = s + text.length;
    t.selectionStart = t.selectionEnd = pos;
  }
  function checkbox(label, checked, onChange) {
    var wrap = el("label", "wf-check");
    var c = el("input"); c.type = "checkbox"; c.checked = !!checked;
    c.onchange = function () { onChange(c.checked); scheduleSave(); };
    wrap.appendChild(c);
    wrap.appendChild(el("span", null, label));
    return wrap;
  }
  function selectField(label, val, options, onChange) {
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var sel = el("select", "wf-input");
    options.forEach(function (o) {
      var opt = el("option", null, o[1]); opt.value = o[0]; if (o[0] === val) opt.selected = true;
      sel.appendChild(opt);
    });
    sel.onchange = function () { onChange(sel.value); scheduleSave(); };
    wrap.appendChild(sel);
    return wrap;
  }
  // modelField is a model picker backed by the connector's servable models
  // (loadModels). It shows "Inherit …" (empty id) plus each model; a current
  // value not in the list (a stale or cross-provider pin) is kept as its own
  // option so it isn't silently dropped. When the list is empty (older connector
  // or a fetch failure) it degrades to a free-text field.
  function modelField(label, val, onChange, inheritLabel) {
    val = val || "";
    var list = models || [];
    if (!list.length) {
      return field(label, val, onChange, "blank = inherit the parent's model");
    }
    var wrap = el("label", "wf-field");
    wrap.appendChild(el("span", "wf-field-label", label));
    var sel = el("select", "wf-input");
    var inherit = el("option", null, inheritLabel || "Inherit (parent's model)"); inherit.value = "";
    if (val === "") inherit.selected = true;
    sel.appendChild(inherit);
    var found = false;
    list.forEach(function (m) {
      var opt = el("option", null, m.name || m.id); opt.value = m.id;
      if (m.id === val) { opt.selected = true; found = true; }
      sel.appendChild(opt);
    });
    if (val && !found) {
      var cur = el("option", null, val + " (current)"); cur.value = val; cur.selected = true;
      sel.appendChild(cur);
    }
    sel.onchange = function () { onChange(sel.value); scheduleSave(); };
    wrap.appendChild(sel);
    return wrap;
  }
  // ------------------------------------------------------------------ save
  function scheduleSave() {
    if (!wf) return;
    if (dom) dom.saveState.textContent = "Editing…";
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(doSave, 800);
  }
  function flushSave() { if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; doSave(); } }

  async function doSave() {
    saveTimer = null;
    if (!wf) return;
    if (saving) { dirtyWhileSaving = true; return; }
    saving = true;
    if (dom) dom.saveState.textContent = "Saving…";
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wf.id), {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(wf),
      });
      if (dom) dom.saveState.textContent = r.ok ? "Saved" : "Save failed";
    } catch (e) {
      if (dom) dom.saveState.textContent = "Save failed";
    } finally {
      saving = false;
      if (dirtyWhileSaving) { dirtyWhileSaving = false; scheduleSave(); }
    }
  }

  // ------------------------------------------------------------------ run / preview
  // toggleRun starts a Preview run (or cancels one in flight). It flushes any
  // pending save, prompts for the workflow's input, POSTs to /run, and consumes
  // the SSE trace — highlighting nodes live and filling the trace panel.
  function toggleRun() {
    if (running) { if (runAbort) runAbort.abort(); return; }
    if (!wf) return;
    showRunInput(function (input) {
      lastRunInput = input;
      flushSave();
      startRun(input);
    });
  }

  // showRunInput opens a styled modal asking for the workflow's initial input
  // (replacing window.prompt). onSubmit(input) runs the workflow; ⌘/Ctrl-Enter
  // submits, Escape or the backdrop cancels. The previous input is prefilled.
  function showRunInput(onSubmit) {
    ensureDOM();
    var prev = document.getElementById("wfRunInput");
    if (prev) prev.remove();
    var ov = el("div", "wf-tpl-overlay"); ov.id = "wfRunInput";
    var card = el("div", "wf-tpl-card wf-run-card");
    var head = el("div", "wf-tpl-head");
    head.appendChild(el("span", null, "Run workflow"));
    var x = el("button", "wf-tpl-close"); x.type = "button"; x.appendChild(sym("xmark"));
    function done() { document.removeEventListener("keydown", onKey, true); ov.remove(); }
    x.onclick = done;
    head.appendChild(x);
    card.appendChild(head);

    var body = el("div", "wf-create-body");
    var lbl = el("label", "wf-field");
    lbl.appendChild(el("span", "wf-field-label", "Input"));
    var ta = el("textarea", "wf-area"); ta.value = lastRunInput || "";
    ta.placeholder = "The workflow's initial input — available downstream as {{input}}. Leave blank for none.";
    lbl.appendChild(ta);
    body.appendChild(lbl);
    body.appendChild(el("div", "wf-hint", "Sent to the Start node; reference it anywhere as {{input}}. ⌘/Ctrl-Enter to run."));
    card.appendChild(body);

    var actions = el("div", "wf-create-actions");
    var cancel = el("button", "wf-btn"); cancel.type = "button"; cancel.textContent = "Cancel"; cancel.onclick = done;
    var run = el("button", "wf-btn wf-run-btn"); run.type = "button";
    run.appendChild(sym("play.fill")); run.appendChild(el("span", null, "Run"));
    function submit() { var v = ta.value; done(); onSubmit(v); }
    run.onclick = submit;
    actions.appendChild(cancel); actions.appendChild(run);
    card.appendChild(actions);

    function onKey(e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); done(); }
      else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); e.stopPropagation(); submit(); }
    }
    document.addEventListener("keydown", onKey, true);
    ov.addEventListener("pointerdown", function (e) { if (e.target === ov) done(); });

    ov.appendChild(card);
    (document.querySelector(".transcript-wrap") || document.body).appendChild(ov);
    ta.focus(); ta.select();
  }

  function setRunning(on) {
    running = on;
    if (!dom) return;
    dom.runBtn.classList.toggle("is-running", on);
    dom.runBtn.querySelector("span").textContent = on ? "Stop" : "Run";
    var ic = dom.runBtn.querySelector("sf-symbol");
    if (ic) ic.setAttribute("name", on ? "stop.fill" : "play.fill");
  }

  // clearRunStatus strips run-state classes from all node elements.
  function clearRunStatus() {
    Array.prototype.slice.call(dom.world.querySelectorAll(".wf-node")).forEach(function (n) {
      n.classList.remove("is-running", "is-done", "is-failed");
    });
  }

  function nodeElById(id) { return dom.world.querySelector('.wf-node[data-id="' + cssEsc(id) + '"]'); }

  // onTraceResize resizes the trace panel as the user drags the handle bar above
  // it. Dragging up grows the panel, down shrinks it. The handle is its own
  // full-width element (not inside the scrolling panel), so the whole bar is the
  // grab target. Tracks on document so the drag keeps working past the bar.
  function onTraceResize(e) {
    if (!dom) return;
    e.preventDefault();
    var t = dom.trace;
    var startY = e.clientY, startH = t.offsetHeight;
    t.style.maxHeight = "none"; // let an explicit height exceed the default 30vh cap
    document.body.style.cursor = "row-resize";
    document.body.style.userSelect = "none";
    function move(ev) {
      var h = startH + (startY - ev.clientY); // drag up → taller
      h = Math.max(80, Math.min(h, Math.round(window.innerHeight * 0.8)));
      t.style.height = h + "px";
    }
    function up() {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerup", up);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    }
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerup", up);
  }

  // mdBody builds a .wf-trace-body that renders `text` as markdown via the shared
  // renderMD (which sanitizes its output), reusing the global .md styles. Falls
  // back to plain text if renderMD isn't loaded.
  function mdBody(text) {
    var d = el("div", "wf-trace-body md wf-trace-md");
    text = String(text == null ? "" : text);
    if (typeof renderMD === "function") d.innerHTML = renderMD(text);
    else d.textContent = text;
    return d;
  }

  // traceDetail returns a short identifier for the node behind a trace event —
  // the skill / persona / worker / tool it ran — shown as a chip in the trace
  // head (so e.g. a Skill node's row names the skill it applied).
  function traceDetail(ev) {
    if (!wf || !ev.nodeId) return "";
    var node = wf.nodes.find(function (n) { return n.id === ev.nodeId; });
    if (!node) return "";
    var d = node.data || {};
    switch (node.type) {
      case "skill": return d.skill ? "✦ " + d.skill : "";
      case "agent":
        if (d.agent) return "↳ " + d.agent;
        if (d.skills && d.skills.length) return "✦ " + d.skills.join(", ");
        return "";
      case "worker": return d.worker ? "⇲ " + d.worker : "";
      case "tool":
      case "mcp": return d.tool || "";
      default: return "";
    }
  }

  function traceRow(ev) {
    var failed = ev.type === "node_failed";
    var row = el("div", "wf-trace-row wf-trace-" + (failed ? "fail" : "ok"));
    var head = el("div", "wf-trace-head");
    head.appendChild(el("span", "wf-trace-name", ev.label || ev.nodeId || ev.nodeType || ""));
    var detail = traceDetail(ev);
    if (detail) head.appendChild(el("span", "wf-trace-detail", detail));
    var meta = [];
    if (ev.model) meta.push(typeof prettyModel === "function" ? prettyModel(ev.model) : ev.model);
    if (ev.usage && (ev.usage.inputTokens || ev.usage.outputTokens)) meta.push((ev.usage.inputTokens || 0) + "→" + (ev.usage.outputTokens || 0) + " tok");
    if (ev.usage && ev.usage.costUSD) meta.push("$" + ev.usage.costUSD.toFixed(4));
    if (ev.durationMs) meta.push(ev.durationMs + "ms");
    if (meta.length) head.appendChild(el("span", "wf-trace-meta", meta.join(" · ")));

    // The body: a failure's error string (plain), or the node's full output
    // (markdown — no truncation, so expanding shows the complete response).
    var body = failed ? el("div", "wf-trace-body", "⚠ " + (ev.error || "failed")) : (ev.output ? mdBody(ev.output) : null);
    if (!body) { row.appendChild(head); return row; }

    // Collapsible: the header toggles the body. Default collapsed for successful
    // outputs (compact trace); failures stay expanded so the error is visible.
    row.classList.add("wf-trace-collapsible");
    if (!failed) row.classList.add("is-collapsed");
    head.insertBefore(sym("chevron.right", "wf-trace-caret"), head.firstChild);
    head.addEventListener("click", function () { row.classList.toggle("is-collapsed"); });
    row.appendChild(head);
    row.appendChild(body);
    return row;
  }

  // runningRow is the live placeholder shown the moment a node starts: a spinner,
  // the node name + detail, a "running…" tag, and a body that streams the node's
  // output (deltas) as it arrives. It's tagged with data-node so node_completed /
  // node_failed can swap it for the final row.
  function runningRow(ev) {
    var row = el("div", "wf-trace-row wf-trace-running"); row.dataset.node = ev.nodeId;
    var head = el("div", "wf-trace-head");
    head.appendChild(sym("circle.dotted", "wf-trace-spin"));
    head.appendChild(el("span", "wf-trace-name", ev.label || ev.nodeId || ev.nodeType || ""));
    var detail = traceDetail(ev);
    if (detail) head.appendChild(el("span", "wf-trace-detail", detail));
    head.appendChild(el("span", "wf-trace-meta", "running…"));
    row.appendChild(head);
    var body = el("div", "wf-trace-body wf-trace-stream hidden");
    row.appendChild(body);
    return row;
  }

  // streamInto updates the running row's body with the accumulated streamed text.
  function streamInto(trace, nodeId, text) {
    var body = trace.querySelector('.wf-trace-running[data-node="' + cssEsc(nodeId) + '"] .wf-trace-stream');
    if (!body) return;
    body.textContent = text;
    body.classList.toggle("hidden", !text);
  }

  // finalizeRow swaps a node's running placeholder for its final (collapsible) row
  // on completion/failure; if there was no placeholder, it just appends. ev.groupId
  // (a parallel region) routes the row into that group's branch column so concurrent
  // branches render side-by-side instead of interleaving in the flat trace.
  function finalizeRow(ev, trace) {
    var dest = containerFor(ev, trace);
    var running = dest.querySelector('.wf-trace-running[data-node="' + cssEsc(ev.nodeId) + '"]');
    var final = traceRow(ev);
    if (running) running.replaceWith(final);
    else dest.appendChild(final);
  }

  // groupBox returns (creating if needed) the container for a parallel region's
  // trace, keyed by the parallel node's id. It holds a titled header ("∥ Parallel —
  // N branches") and one column per branch; branch rows append into their column so
  // the concurrent branches read top-to-bottom within their own lane.
  function groupBox(trace, groupId, branchCount) {
    var box = trace.querySelector('.wf-trace-group[data-group="' + cssEsc(groupId) + '"]');
    if (box) return box;
    box = el("div", "wf-trace-group"); box.dataset.group = groupId;
    var node = wf && wf.nodes.find(function (n) { return n.id === groupId; });
    var title = (node && node.data && node.data.label) || "Parallel";
    var head = el("div", "wf-trace-group-head");
    head.appendChild(sym("arrow.trianglehead.branch"));
    head.appendChild(el("span", "wf-trace-group-title", "∥ " + title + (branchCount ? " — " + branchCount + " branches" : "")));
    box.appendChild(head);
    var cols = el("div", "wf-trace-group-cols"); cols.dataset.cols = "1";
    box.appendChild(cols);
    trace.appendChild(box);
    return box;
  }

  // branchCol returns (creating if needed) the column element for one branch index
  // inside a region's group box.
  function branchCol(trace, groupId, branch) {
    var box = groupBox(trace, groupId, 0);
    var cols = box.querySelector(".wf-trace-group-cols");
    var col = cols.querySelector('.wf-trace-col[data-branch="' + branch + '"]');
    if (!col) {
      col = el("div", "wf-trace-col"); col.dataset.branch = branch;
      cols.appendChild(col);
      cols.dataset.cols = cols.children.length; // CSS grids the columns by this count
    }
    return col;
  }

  // containerFor picks where a trace row lives: a branch column when the event
  // carries a groupId (a parallel branch), else the flat trace.
  function containerFor(ev, trace) {
    if (ev.groupId) return branchCol(trace, ev.groupId, ev.branch || 0);
    return trace;
  }

  async function startRun(input, retried) {
    setRunning(true);
    curRunId = ""; // drop any prior run's id so a pause can't resume the wrong run
    clearRunStatus();
    var trace = dom.trace;
    trace.dataset.mode = "trace";
    trace.innerHTML = "";
    trace.classList.remove("hidden");
    trace.appendChild(el("div", "wf-trace-title", "Run trace"));
    var deltas = {}; // nodeId -> accumulated streamed text (live, before node_completed)
    var proxyDenied = false; // a node hit the SQE-Proxy group gate (403) → refresh-first

    runAbort = new AbortController();
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wf.id) + "/run", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ input: input }),
        signal: runAbort.signal,
      });
      if (r.status === 403) { proxyDenied = true; }
      else if (!r.ok || !r.body) { trace.appendChild(el("div", "wf-trace-row wf-trace-fail", "Run failed (" + r.status + ")")); return; }
      if (r.body) {
        var reader = r.body.getReader(), dec = new TextDecoder(), buf = "";
        for (;;) {
          var chunk = await reader.read();
          if (chunk.done) break;
          buf += dec.decode(chunk.value, { stream: true });
          var frames = buf.split("\n\n");
          buf = frames.pop(); // keep the trailing partial frame
          for (var i = 0; i < frames.length; i++) {
            var line = frames[i].trim();
            if (line.indexOf("data:") !== 0) continue;
            var ev;
            try { ev = JSON.parse(line.slice(5).trim()); } catch (_) { continue; }
            // SQE-Proxy group gate: suppress the raw 403 rows; handle it once the
            // stream drains by refreshing membership and retrying (refresh-first).
            if (isProxyDeniedEvent(ev)) { proxyDenied = true; continue; }
            handleRunEvent(ev, deltas, trace);
          }
        }
      }
    } catch (e) {
      if (!(e && e.name === "AbortError")) trace.appendChild(el("div", "wf-trace-row wf-trace-fail", "Run error: " + (e && e.message)));
    } finally {
      setRunning(false);
      runAbort = null;
    }
    if (proxyDenied) handleRunProxyDenied(input, retried, trace);
  }

  // isProxyDenied reports whether an error string is the SQE-Proxy group gate (a
  // floodgate 403), in any of its phrasings.
  function isProxyDenied(text) {
    if (typeof text !== "string") return false;
    return text.indexOf("sqe-proxy: access denied") !== -1
      || text.indexOf("SQE Proxy unavailable") !== -1
      || text.indexOf("SQE Proxy is restricted") !== -1
      || text.indexOf("not a member of the configured group") !== -1;
  }
  function isProxyDeniedEvent(ev) {
    return !!ev && (ev.type === "node_failed" || ev.type === "workflow_failed") && isProxyDenied(ev.error);
  }

  // handleRunProxyDenied implements the refresh-first rule for a run that hit the
  // SQE-Proxy group gate: silently re-check membership (POST /api/refresh-membership,
  // sharing the global dedup/cooldown) and, on success, retry the run ONCE; only if
  // the user genuinely isn't in the group does a concise note + Refresh button show
  // — the raw 403 is never surfaced. `retried` guards against a refresh→retry loop.
  function handleRunProxyDenied(input, retried, trace) {
    if (retried) { showRunProxyDeniedNote(trace, input); return; }
    // Cooldown: just refreshed and STILL denied → don't loop; show the note.
    var sinceLast = Date.now() - (typeof sqeProxyRefreshAt !== "undefined" ? sqeProxyRefreshAt : 0);
    if (typeof sqeProxyRefreshAt !== "undefined" && sqeProxyRefreshAt && sinceLast < 8000) {
      showRunProxyDeniedNote(trace, input); return;
    }
    if (typeof sqeProxyRefreshing !== "undefined" && sqeProxyRefreshing) {
      // Another surface is already refreshing — wait it out, then retry once.
      var note = el("div", "wf-trace-row", "↻ Refreshing SQE Proxy access…");
      trace.appendChild(note); trace.scrollTop = trace.scrollHeight;
      setTimeout(function () { note.remove(); startRun(input, true); }, 2000);
      return;
    }
    var row = el("div", "wf-trace-row", "↻ SQE Proxy access denied — refreshing membership…");
    trace.appendChild(row); trace.scrollTop = trace.scrollHeight;
    (async function () {
      var ok = false;
      try {
        if (typeof sqeProxyRefreshing !== "undefined") sqeProxyRefreshing = true;
        if (typeof sqeProxyRefreshAt !== "undefined") sqeProxyRefreshAt = Date.now();
        var rr = await fetch("/api/refresh-membership", { method: "POST" });
        var data = rr.ok ? await rr.json().catch(function () { return {}; }) : {};
        if (typeof authUser !== "undefined" && authUser) authUser.in_group = !!data.in_group; // mirror re-issued cookie
        ok = !!data.in_group;
      } catch (_) { /* treat as denied */ }
      finally { if (typeof sqeProxyRefreshing !== "undefined") sqeProxyRefreshing = false; }
      if (ok) { row.textContent = "✓ Access refreshed — retrying run…"; startRun(input, true); }
      else { row.remove(); showRunProxyDeniedNote(trace, input); }
    })();
  }

  // showRunProxyDeniedNote shows the concise "not in the group" note in the run
  // trace (never the raw 403), with a Refresh Access button that retries the run.
  function showRunProxyDeniedNote(trace, input) {
    var box = el("div", "wf-trace-done wf-trace-fail");
    box.appendChild(el("span", null, "✕ You're not in the SQE Proxy group — agent nodes can't reach the model. If you were just added, retry. "));
    var btn = el("button", "wf-btn"); btn.type = "button"; btn.style.marginLeft = ".4rem";
    btn.appendChild(sym("arrow.clockwise")); btn.appendChild(el("span", null, "Refresh Access"));
    btn.onclick = function () { startRun(input, false); };
    box.appendChild(btn);
    trace.appendChild(box);
    trace.scrollTop = trace.scrollHeight;
  }

  function handleRunEvent(ev, deltas, trace) {
    switch (ev.type) {
      case "run_id": {
        curRunId = ev.output || "";
        break;
      }
      case "node_started": {
        clearRunStatus();
        var n = nodeElById(ev.nodeId); if (n) n.classList.add("is-running");
        // A parallel node's own start opens its branch group (carrying branchCount)
        // but renders no row of its own — the branches do.
        if (ev.nodeType === "parallel") { groupBox(trace, ev.nodeId, ev.branchCount || 0); trace.scrollTop = trace.scrollHeight; break; }
        deltas[ev.nodeId] = "";
        containerFor(ev, trace).appendChild(runningRow(ev)); // active block, in its branch column if grouped
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "node_delta": {
        deltas[ev.nodeId] = (deltas[ev.nodeId] || "") + (ev.delta || "");
        streamInto(trace, ev.nodeId, deltas[ev.nodeId]);
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "node_retry": {
        var rdest = containerFor(ev, trace);
        rdest.appendChild(el("div", "wf-trace-row wf-trace-fail", "↻ retry " + (ev.attempt || 0) + "/" + ((ev.maxAttempts || 1) - 1) + " — " + (ev.error || "")));
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "node_completed": {
        var n2 = nodeElById(ev.nodeId); if (n2) { n2.classList.remove("is-running"); n2.classList.add("is-done"); }
        // The parallel node's completion just closes its group (its branches +
        // join already rendered their own rows); don't render a row for it.
        if (ev.nodeType === "parallel") { var gb = trace.querySelector('.wf-trace-group[data-group="' + cssEsc(ev.nodeId) + '"]'); if (gb) gb.classList.add("is-done"); break; }
        finalizeRow(ev, trace);
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "node_failed": {
        var n3 = nodeElById(ev.nodeId); if (n3) { n3.classList.remove("is-running"); n3.classList.add("is-failed"); }
        finalizeRow(ev, trace);
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "workflow_paused": {
        var np = nodeElById(ev.nodeId); if (np) np.classList.add("is-running");
        trace.appendChild(pauseRow(ev));
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "workflow_completed": {
        trace.appendChild(el("div", "wf-trace-done", "✓ Completed"));
        trace.scrollTop = trace.scrollHeight;
        break;
      }
      case "workflow_failed": {
        trace.appendChild(el("div", "wf-trace-done wf-trace-fail", "✕ " + (ev.error || "failed")));
        trace.scrollTop = trace.scrollHeight;
        break;
      }
    }
  }

  // pauseRow renders a human-in-the-loop pause in the trace. ev.pauseMode selects
  // the controls: "approval" → Approve/Reject (with an optional tool diff),
  // "question" → one button per option, "input" → a textarea + Submit. Each
  // resolves the pause via POST /resume with the run id, the pause id, and (for
  // input/question) the chosen text.
  function pauseRow(ev) {
    var mode = ev.pauseMode || "approval";
    var row = el("div", "wf-trace-row wf-approval");
    var icon = mode === "input" ? "⌨ " : (mode === "question" ? "❓ " : "⏸ ");
    var title = ev.label || (mode === "input" ? "Ask user" : (mode === "question" ? "Question" : "Approval"));
    if (ev.pauseTool) title += " · " + ev.pauseTool;
    row.appendChild(el("div", "wf-trace-name", icon + title));
    if (ev.prompt) row.appendChild(mdBody(ev.prompt));
    if (ev.pauseDiff) { var pre = el("pre", "wf-approval-diff"); pre.textContent = truncate(ev.pauseDiff, 1200); row.appendChild(pre); }

    var bar = el("div", "wf-approval-bar");
    var rid = curRunId, pid = ev.pauseId || "approval";
    var resumeURL = base() + "/" + encodeURIComponent(wf.id) + "/resume?run=" + encodeURIComponent(rid) + "&pause=" + encodeURIComponent(pid);
    function resume(decision, text, label, controls) {
      controls.forEach(function (b) { b.disabled = true; });
      var url = resumeURL + "&decision=" + decision + (text != null ? "&text=" + encodeURIComponent(text) : "");
      fetch(url, { method: "POST" })
        .then(function (r) { bar.textContent = r.ok ? "→ " + label : "resume failed (" + r.status + ")"; })
        .catch(function () { bar.textContent = "resume failed (network)"; });
    }

    if (mode === "input") {
      var ta = el("textarea", "wf-area wf-approval-input"); ta.placeholder = "Type your answer…";
      row.appendChild(ta);
      var submit = el("button", "wf-btn wf-run-btn"); submit.type = "button";
      submit.appendChild(sym("paperplane.fill")); submit.appendChild(el("span", null, "Submit"));
      var cancelIn = el("button", "wf-btn"); cancelIn.type = "button"; cancelIn.textContent = "Cancel";
      submit.onclick = function () { resume("approve", ta.value, "submitted", [submit, cancelIn]); };
      cancelIn.onclick = function () { resume("reject", "", "cancelled", [submit, cancelIn]); };
      ta.addEventListener("keydown", function (e) { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submit.onclick(); } });
      bar.appendChild(submit); bar.appendChild(cancelIn);
      setTimeout(function () { ta.focus(); }, 0);
    } else if (mode === "question") {
      var optBtns = [];
      (ev.pauseOptions || []).forEach(function (o) {
        var b = el("button", "wf-btn wf-approval-opt"); b.type = "button";
        b.appendChild(el("span", "wf-opt-label", o.label));
        if (o.description) b.appendChild(el("span", "wf-opt-desc", o.description));
        optBtns.push(b);
      });
      optBtns.forEach(function (b, i) {
        b.onclick = function () { resume("approve", (ev.pauseOptions[i] || {}).label || "", "chose " + ((ev.pauseOptions[i] || {}).label || ""), optBtns.concat([dismiss])); };
        bar.appendChild(b);
      });
      var dismiss = el("button", "wf-btn"); dismiss.type = "button"; dismiss.textContent = "Dismiss";
      dismiss.onclick = function () { resume("reject", "", "dismissed", optBtns.concat([dismiss])); };
      bar.appendChild(dismiss);
    } else {
      var approve = el("button", "wf-btn wf-run-btn"); approve.type = "button"; approve.textContent = "Approve";
      var reject = el("button", "wf-btn"); reject.type = "button"; reject.textContent = "Reject";
      approve.onclick = function () { resume("approve", null, "approved", [approve, reject]); };
      reject.onclick = function () { resume("reject", null, "rejected", [approve, reject]); };
      bar.appendChild(approve); bar.appendChild(reject);
    }
    row.appendChild(bar);
    return row;
  }
  // approvalRow kept as an alias (older callers / resume-history view).
  function approvalRow(ev) { return pauseRow(ev); }

  // ------------------------------------------------------------------ evals
  // toggleEvals opens/closes the evals panel: a dataset editor (one JSON case per
  // line: {"input":"...","expect":"..."}) + a Run-evals button that streams
  // per-case pass/fail and an aggregate summary.
  function toggleEvals() {
    if (!wf) return;
    var p = dom.evals;
    if (!p.classList.contains("hidden")) { p.classList.add("hidden"); return; }
    p.innerHTML = "";
    p.classList.remove("hidden");
    p.appendChild(el("div", "wf-trace-title", "Evals — one JSON case per line: {\"input\":\"…\",\"expect\":\"…\"}"));
    var ta = el("textarea", "wf-evals-input");
    ta.placeholder = '{"input":"hello","expect":"a friendly greeting"}\n{"input":"2+2","expect":"4"}';
    // Prefer the dataset saved with the workflow; else the in-session cache.
    ta.value = datasetToText(wf.evalDataset) || evalsDatasetCache || "";
    p.appendChild(ta);
    var bar = el("div", "wf-evals-bar");
    var runEv = el("button", "wf-btn wf-run-btn"); runEv.type = "button";
    runEv.appendChild(sym("play.fill")); runEv.appendChild(el("span", null, "Run evals"));
    var saveEv = el("button", "wf-btn"); saveEv.type = "button"; saveEv.title = "Save this dataset with the workflow (travels + shares with it)";
    saveEv.appendChild(sym("square.and.arrow.down")); saveEv.appendChild(el("span", null, "Save dataset"));
    var summary = el("span", "wf-evals-summary");
    runEv.onclick = function () { startEvals(ta.value, runEv, summary, p, false); };
    saveEv.onclick = function () { startEvals(ta.value, runEv, summary, p, true); };
    bar.appendChild(runEv); bar.appendChild(saveEv); bar.appendChild(summary);
    p.appendChild(bar);
    var results = el("div", "wf-evals-results"); results.id = "wfEvalsResults";
    p.appendChild(results);
  }

  // datasetToText renders saved EvalCase[] back to one-JSON-per-line text.
  function datasetToText(ds) {
    if (!ds || !ds.length) return "";
    return ds.map(function (c) { return JSON.stringify({ input: c.input, expect: c.expect || "" }); }).join("\n");
  }

  function parseEvalCases(text) {
    var cases = [];
    (text || "").split("\n").forEach(function (line) {
      line = line.trim();
      if (!line) return;
      try { var c = JSON.parse(line); if (c && typeof c.input === "string") cases.push({ input: c.input, expect: c.expect || "" }); } catch (_) {}
    });
    return cases;
  }

  async function startEvals(datasetText, btn, summaryEl, panel, save) {
    evalsDatasetCache = datasetText;
    var cases = parseEvalCases(datasetText);
    var results = panel.querySelector("#wfEvalsResults");
    results.innerHTML = "";
    if (!cases.length) { summaryEl.textContent = "No valid cases (one JSON object per line)."; return; }
    if (save) { wf.evalDataset = cases; } // reflect locally so a later plain save keeps it too
    btn.disabled = true; summaryEl.textContent = save ? "Saving + running " + cases.length + "…" : "Running " + cases.length + " case(s)…";
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wf.id) + "/eval", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ cases: cases, save: !!save }),
      });
      if (!r.ok || !r.body) { summaryEl.textContent = "Eval failed (" + r.status + ")"; return; }
      var reader = r.body.getReader(), dec = new TextDecoder(), buf = "";
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) break;
        buf += dec.decode(chunk.value, { stream: true });
        var frames = buf.split("\n\n"); buf = frames.pop();
        frames.forEach(function (line) {
          line = line.trim();
          if (line.indexOf("data:") !== 0) return;
          var msg; try { msg = JSON.parse(line.slice(5).trim()); } catch (_) { return; }
          if (msg.type === "eval_case") results.appendChild(evalCaseRow(msg.payload));
          else if (msg.type === "eval_summary") {
            var s = msg.payload;
            summaryEl.textContent = s.passed + "/" + s.total + " passed · avg " + (s.avgScore || 0).toFixed(2) + (s.costUSD ? " · $" + s.costUSD.toFixed(4) : "");
          }
          results.scrollTop = results.scrollHeight;
        });
      }
    } catch (e) {
      summaryEl.textContent = "Eval error: " + (e && e.message);
    } finally {
      btn.disabled = false;
    }
  }

  function evalCaseRow(cr) {
    var row = el("div", "wf-eval-row " + (cr.passed ? "wf-eval-pass" : "wf-eval-fail"));
    var head = el("div", "wf-trace-head");
    head.appendChild(sym(cr.passed ? "checkmark.circle.fill" : "xmark.circle.fill"));
    head.appendChild(el("span", "wf-trace-name", truncate(cr.input, 50)));
    head.appendChild(el("span", "wf-trace-meta", "score " + (cr.score || 0).toFixed(2)));
    row.appendChild(head);
    if (cr.error) row.appendChild(el("div", "wf-trace-body", "⚠ " + cr.error));
    else {
      if (cr.output) row.appendChild(el("div", "wf-trace-body", "→ " + truncate(cr.output, 200)));
      if (cr.reason) row.appendChild(el("div", "wf-eval-reason", cr.reason));
    }
    return row;
  }

  // ------------------------------------------------------------------ run history
  // toggleHistory opens/closes a history panel (reusing the trace dock) listing
  // past runs of this workflow; each row expands to its persisted trace, and
  // paused/failed runs offer a durable Resume.
  async function toggleHistory() {
    if (!wf) return;
    var t = dom.trace;
    if (!t.classList.contains("hidden") && t.dataset.mode === "history") { t.classList.add("hidden"); return; }
    t.dataset.mode = "history";
    t.classList.remove("hidden");
    t.innerHTML = "";
    t.appendChild(el("div", "wf-trace-title", "Run history"));
    var listBox = el("div"); t.appendChild(listBox);
    listBox.appendChild(el("div", "wf-trace-body", "Loading…"));
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wf.id) + "/runs", { headers: { Accept: "application/json" } });
      var runs = r.ok ? await r.json() : [];
      listBox.innerHTML = "";
      if (!runs.length) { listBox.appendChild(el("div", "wf-trace-body", "No runs yet.")); return; }
      runs.forEach(function (run) { listBox.appendChild(historyRow(run)); });
    } catch (e) {
      listBox.innerHTML = ""; listBox.appendChild(el("div", "wf-trace-body", "Couldn't load history."));
    }
  }

  function statusGlyph(status) {
    if (status === "completed") return "checkmark.circle.fill";
    if (status === "failed") return "xmark.circle.fill";
    if (status === "paused") return "pause.circle.fill";
    return "circle";
  }

  function historyRow(run) {
    var row = el("div", "wf-hist-row wf-hist-" + run.status);
    var head = el("div", "wf-hist-head");
    head.appendChild(sym(statusGlyph(run.status), "wf-hist-icon"));
    head.appendChild(el("span", "wf-trace-name", truncate(run.input || "(no input)", 40)));
    var meta = [run.status];
    if (run.costUSD) meta.push("$" + run.costUSD.toFixed(4));
    if (run.startedAt) meta.push(run.startedAt.replace("T", " ").replace("Z", ""));
    head.appendChild(el("span", "wf-trace-meta", meta.join(" · ")));
    row.appendChild(head);

    var body = el("div", "wf-hist-body hidden");
    (run.trace || []).forEach(function (te) {
      var tr = el("div", "wf-trace-row " + (te.status === "failed" ? "wf-trace-fail" : "wf-trace-ok"));
      var h = el("div", "wf-trace-head");
      h.appendChild(el("span", "wf-trace-name", (te.label || te.nodeId) + " · " + te.status));
      if (te.usage && te.usage.costUSD) h.appendChild(el("span", "wf-trace-meta", "$" + te.usage.costUSD.toFixed(4)));
      tr.appendChild(h);
      var txt = te.error ? ("⚠ " + te.error) : (te.output || "");
      if (txt) tr.appendChild(el("div", "wf-trace-body", truncate(txt, 300)));
      body.append(tr);
    });
    if (run.status === "paused" || run.status === "failed") {
      var resume = el("button", "wf-btn wf-run-btn"); resume.type = "button";
      resume.appendChild(sym("play.fill")); resume.appendChild(el("span", null, "Resume from " + (run.currentNode || "start")));
      resume.onclick = function (e) { e.stopPropagation(); resumeRun(run.id); };
      body.append(resume);
    }
    row.appendChild(body);
    head.onclick = function () { body.classList.toggle("hidden"); };
    return row;
  }

  // resumeRun durably resumes a saved paused/failed run: it streams a fresh trace
  // (into the run dock, in trace mode) continuing from the recorded node.
  async function resumeRun(runId) {
    var t = dom.trace; t.dataset.mode = "trace"; t.innerHTML = "";
    t.classList.remove("hidden");
    t.appendChild(el("div", "wf-trace-title", "Resuming run…"));
    clearRunStatus();
    runAbort = new AbortController();
    var deltas = {};
    try {
      var r = await fetch(base() + "/" + encodeURIComponent(wf.id) + "/runs/" + encodeURIComponent(runId) + "/resume", { method: "POST", signal: runAbort.signal });
      if (!r.ok || !r.body) { t.appendChild(el("div", "wf-trace-row wf-trace-fail", "Resume failed (" + r.status + ")")); return; }
      var reader = r.body.getReader(), dec = new TextDecoder(), buf = "";
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) break;
        buf += dec.decode(chunk.value, { stream: true });
        var frames = buf.split("\n\n"); buf = frames.pop();
        for (var i = 0; i < frames.length; i++) {
          var line = frames[i].trim();
          if (line.indexOf("data:") !== 0) continue;
          var ev; try { ev = JSON.parse(line.slice(5).trim()); } catch (_) { continue; }
          handleRunEvent(ev, deltas, t);
        }
      }
    } catch (e) {
      if (!(e && e.name === "AbortError")) t.appendChild(el("div", "wf-trace-row wf-trace-fail", "Resume error: " + (e && e.message)));
    } finally { runAbort = null; }
  }

  // ------------------------------------------------------------------ sidebar
  function firstOnlineHost() {
    if (typeof laptops !== "undefined" && laptops && laptops.length) {
      var on = laptops.find(function (l) { return l.online !== false; });
      if (on) return on.host;
    }
    return "";
  }

  // renderAgentBuilderInto fills the "Agent Builder" sidebar section: the workflow
  // list for the focused (or first online) laptop, plus a "New workflow" button.
  async function renderAgentBuilderInto(box) {
    var h = currentLaptop || firstOnlineHost();
    if (!h) {
      box.appendChild(el("div", "px-4 py-3 text-[12px] text-faint", "Connect a machine to build agents."));
      return;
    }
    var listBox = el("div", "wf-list");
    box.appendChild(listBox);
    var newRow = el("button", "wf-new-row"); newRow.type = "button";
    newRow.appendChild(sym("plus.circle.fill")); newRow.appendChild(el("span", null, "New workflow"));
    newRow.onclick = function () { createAndOpen(h); };
    box.appendChild(newRow);

    listBox.appendChild(el("div", "px-4 py-2 text-[11px] text-faint", "Loading…"));
    try {
      var r = await fetch(laptopBase(h) + "/api/workflows", { headers: { Accept: "application/json" } });
      var items = r.ok ? await r.json() : [];
      listBox.innerHTML = "";
      if (!items.length) {
        listBox.appendChild(el("div", "px-4 py-2 text-[11px] text-faint", "No workflows yet."));
      } else {
        items.forEach(function (it) {
          var row = el("button", "wf-row"); row.type = "button";
          row.appendChild(sym("flowchart", "shrink-0 text-faint"));
          row.appendChild(el("span", "wf-row-name truncate", it.name || it.id));
          row.appendChild(el("span", "wf-row-meta", String(it.nodes || 0)));
          row.onclick = function () { open(h, it.id); };
          listBox.appendChild(row);
        });
      }
    } catch (e) {
      listBox.innerHTML = "";
      listBox.appendChild(el("div", "px-4 py-2 text-[11px] text-faint", "Couldn't load workflows."));
    }
  }

  // createAndOpen offers a template picker (built-ins + blank), then creates a
  // workflow seeded from the chosen template and opens it.
  async function createAndOpen(h) {
    var templates = [];
    try {
      var tr = await fetch(laptopBase(h) + "/api/workflows/templates", { headers: { Accept: "application/json" } });
      if (tr.ok) templates = await tr.json();
    } catch (_) { /* templates optional */ }
    showTemplatePicker(h, templates);
  }

  // ----------------------------------------------------------- create agent/skill
  // showCreateForm opens a modal to author a brand-new subagent persona or Agent
  // Skill on the connector (POST /api/workflows/agents | /skills). `kind` is the
  // initial tab ("agent" | "skill"); onCreated(name) — when given (from a node
  // combo's "+ New") — selects the freshly created capability in that field.
  function showCreateForm(kind, onCreated) {
    ensureDOM();
    var prev = document.getElementById("wfCreateForm");
    if (prev) prev.remove();
    var ov = el("div", "wf-tpl-overlay"); ov.id = "wfCreateForm";
    var card = el("div", "wf-tpl-card wf-create-card");
    var head = el("div", "wf-tpl-head");
    var title = el("span", null, "New agent");
    var x = el("button", "wf-tpl-close"); x.type = "button"; x.appendChild(sym("xmark"));
    x.onclick = function () { ov.remove(); };
    head.appendChild(title); head.appendChild(x);
    card.appendChild(head);

    // Segmented Agent | Skill switcher.
    var tabs = el("div", "wf-create-tabs");
    var body = el("div", "wf-create-body");
    var msg = el("div", "wf-create-msg hidden");
    var actions = el("div", "wf-create-actions");
    var cancel = el("button", "wf-btn"); cancel.type = "button"; cancel.textContent = "Cancel";
    cancel.onclick = function () { ov.remove(); };
    var save = el("button", "wf-btn wf-run-btn"); save.type = "button";
    save.appendChild(sym("checkmark")); save.appendChild(el("span", null, "Create"));
    actions.appendChild(cancel); actions.appendChild(save);

    var cur = kind === "skill" ? "skill" : "agent";
    function setMsg(t, ok) { msg.textContent = t || ""; msg.classList.toggle("hidden", !t); msg.classList.toggle("is-ok", !!ok); }

    function renderBody() {
      body.innerHTML = ""; setMsg("");
      title.textContent = cur === "skill" ? "New skill" : "New agent";
      Array.prototype.forEach.call(tabs.children, function (b) { b.classList.toggle("active", b.dataset.kind === cur); });

      // Generate-with-AI: describe it, the connector's model drafts the fields.
      var gen = el("div", "wf-gen");
      gen.appendChild(el("span", "wf-field-label", "Generate with AI"));
      var genTa = el("textarea", "wf-area wf-gen-input"); genTa.value = body._genPrompt || "";
      genTa.placeholder = cur === "skill"
        ? "Describe the skill — e.g. “review Terraform plans for security issues”"
        : "Describe the agent — e.g. “a careful reviewer that finds security bugs and returns a prioritized list”";
      genTa.oninput = function () { body._genPrompt = genTa.value; };
      gen.appendChild(genTa);
      var genBar = el("div", "wf-gen-bar");
      var genBtn = el("button", "wf-btn wf-gen-btn"); genBtn.type = "button";
      genBtn.appendChild(sym("sparkles")); genBtn.appendChild(el("span", null, "Generate"));
      var genMsg = el("span", "wf-gen-msg");
      genBtn.onclick = function () { generateInto(genTa.value, genBtn, genMsg); };
      genBar.appendChild(genBtn); genBar.appendChild(genMsg);
      gen.appendChild(genBar);
      body.appendChild(gen);
      body.appendChild(el("div", "wf-gen-or", "or fill in manually"));

      if (cur === "agent") {
        body.appendChild(field("Name", body._name || "", function (v) { body._name = v; }, "e.g. researcher (letters, digits, dashes)"));
        body.appendChild(field("Description", body._desc || "", function (v) { body._desc = v; }, "one line: what it does + when to delegate to it"));
        body.appendChild(modelField("Model (optional)", body._model || "", function (v) { body._model = v; }));
        body.appendChild(toolPicker("Tools (allowlist)", body._tools || [], function (v) { body._tools = v; }));
        body.appendChild(area("System prompt", body._sys || "", function (v) { body._sys = v; }, "You are the … subagent. Describe its role, constraints, and the output it returns."));
      } else {
        body.appendChild(field("Name", body._name || "", function (v) { body._name = v; }, "e.g. pdf-tools (letters, digits, dashes)"));
        body.appendChild(field("Description", body._desc || "", function (v) { body._desc = v; }, "one line: what it does + when to use it"));
        body.appendChild(area("Instructions", body._sys || "", function (v) { body._sys = v; }, "Markdown the model reads when it loads this skill: the workflow, steps, and any commands."));
      }
      body.appendChild(msg);
    }

    // generateInto asks the connector's model to draft this kind's fields from the
    // description, then re-renders the form pre-filled (the user reviews + edits
    // before Create). Uses the connector's active model (set via the toolbar).
    async function generateInto(prompt, btn, msgEl) {
      prompt = (prompt || "").trim();
      if (!prompt) { msgEl.textContent = "Describe what you want first."; return; }
      btn.disabled = true;
      // Jumping-Claude loader while the model drafts (matches the app's busy art).
      msgEl.innerHTML = "";
      var art = el("img", "wf-gen-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "";
      msgEl.appendChild(art); msgEl.appendChild(el("span", null, "Generating…"));
      try {
        var r = await fetch(base() + "/generate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ kind: cur, prompt: prompt }) });
        if (!r.ok) { msgEl.textContent = (await r.text()).trim() || ("HTTP " + r.status); btn.disabled = false; return; }
        var g = await r.json();
        if (g.name) body._name = g.name;
        if (g.description) body._desc = g.description;
        if (cur === "skill") { if (g.body) body._sys = g.body; }
        else {
          if (g.systemPrompt) body._sys = g.systemPrompt;
          if (Array.isArray(g.tools)) body._tools = g.tools;
          if (typeof g.model === "string" && g.model) body._model = g.model;
        }
        renderBody(); // re-render pre-filled (preserves the description in _genPrompt)
      } catch (e) {
        msgEl.textContent = "Couldn't reach the machine."; btn.disabled = false;
      }
    }
    ["agent", "skill"].forEach(function (k) {
      var b = el("button", "wf-create-tab"); b.type = "button"; b.dataset.kind = k;
      b.appendChild(sym(k === "agent" ? "sparkles" : "wand.and.stars"));
      b.appendChild(el("span", null, k === "agent" ? "Agent" : "Skill"));
      b.onclick = function () { if (cur !== k) { cur = k; renderBody(); } };
      tabs.appendChild(b);
    });

    save.onclick = async function () {
      var name = (body._name || "").trim();
      var desc = (body._desc || "").trim();
      var sys = (body._sys || "").trim();
      if (!name) { setMsg("A name is required."); return; }
      if (!desc) { setMsg("A description is required."); return; }
      if (!sys) { setMsg(cur === "skill" ? "Instructions are required." : "A system prompt is required."); return; }
      save.disabled = true; setMsg("Creating…");
      var url = base() + (cur === "skill" ? "/skills" : "/agents");
      var payload = cur === "skill"
        ? { name: name, description: desc, body: sys }
        : { name: name, description: desc, model: (body._model || "").trim(), tools: body._tools || [], systemPrompt: sys };
      try {
        var r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
        if (!r.ok) { setMsg((await r.text()).trim() || ("HTTP " + r.status)); save.disabled = false; return; }
        var created = await r.json();
        await loadCapabilities();         // refresh node-combo option lists
        if (onCreated) onCreated(created.name);
        if (sel && sel.kind === "node") openConfig(); // reflect new option in the open inspector
        ov.remove();
      } catch (e) {
        setMsg("Couldn't reach the machine: " + (e && e.message)); save.disabled = false;
      }
    };

    card.appendChild(tabs); card.appendChild(body); card.appendChild(actions);
    renderBody();
    ov.appendChild(card);
    (document.querySelector(".transcript-wrap") || document.body).appendChild(ov);
    var first = body.querySelector("input"); if (first) first.focus();
    // Models may not be cached yet — fetch then re-render so the agent tab shows a
    // real picker. Skip the re-render if the user is already typing in the form
    // (don't steal focus); the plain-text Model field stays editable until then.
    if (!models) {
      loadModels().then(function () {
        if (document.getElementById("wfCreateForm") !== ov) return;
        if (body.contains(document.activeElement)) return;
        renderBody();
      });
    }
  }

  // showTemplatePicker renders a modal-ish overlay (inside the builder root or
  // body) listing "Blank" + each template; choosing one creates + opens it.
  function showTemplatePicker(h, templates) {
    ensureDOM();
    var prev = document.getElementById("wfTplPicker");
    if (prev) prev.remove();
    var ov = el("div", "wf-tpl-overlay"); ov.id = "wfTplPicker";
    var card = el("div", "wf-tpl-card");
    var head = el("div", "wf-tpl-head");
    head.appendChild(el("span", null, "New workflow"));
    var x = el("button", "wf-tpl-close"); x.type = "button"; x.appendChild(sym("xmark")); x.onclick = function () { ov.remove(); };
    head.appendChild(x);
    card.appendChild(head);
    var list = el("div", "wf-tpl-list");
    var blank = el("button", "wf-tpl-item"); blank.type = "button";
    blank.appendChild(el("div", "wf-tpl-name", "Blank"));
    blank.appendChild(el("div", "wf-tpl-desc", "An empty canvas with just a Start node."));
    blank.onclick = function () { ov.remove(); createFrom(h, "Untitled workflow", null); };
    list.appendChild(blank);
    (templates || []).forEach(function (t) {
      var it = el("button", "wf-tpl-item"); it.type = "button";
      it.appendChild(el("div", "wf-tpl-name", t.name));
      it.appendChild(el("div", "wf-tpl-desc", t.description || ""));
      it.onclick = function () { ov.remove(); createFrom(h, t.name, t); };
      list.appendChild(it);
    });
    card.appendChild(list);
    ov.appendChild(card);
    (document.querySelector(".transcript-wrap") || document.body).appendChild(ov);
  }

  // createFrom creates a workflow (optionally seeded from a template's nodes/edges)
  // and opens it. The server seeds a Start node for a blank one; for a template we
  // PUT its full graph right after create so the saved workflow matches it.
  async function createFrom(h, name, template) {
    try {
      var r = await fetch(laptopBase(h) + "/api/workflows", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: name }),
      });
      if (!r.ok) return;
      var created = await r.json();
      if (template && template.nodes) {
        created.nodes = template.nodes;
        created.edges = template.edges || [];
        await fetch(laptopBase(h) + "/api/workflows/" + encodeURIComponent(created.id), {
          method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(created),
        });
      }
      open(h, created.id);
      if (typeof renderLaptopTree === "function") renderLaptopTree();
    } catch (e) { /* ignore */ }
  }

  window.openWorkflowBuilder = open;
  window.closeWorkflowBuilder = close;
  window.renderAgentBuilderInto = renderAgentBuilderInto;
})();
