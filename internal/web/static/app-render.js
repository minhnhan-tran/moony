// app-render.js — transcript rendering: markdown/sanitize/highlight, tool cards,
// diffs, and per-turn meta. Shares global scope with app-state.js (loaded first).

function escapeHTML(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function isSafeUrl(v) {
  if (!v) return true;
  const t = String(v).trim().toLowerCase();
  if (t.startsWith("#") || t.startsWith("/") || t.startsWith("./") || t.startsWith("../") || t.startsWith("?")) return true;
  return /^(https?:|mailto:|tel:|sms:)/i.test(t);
}

function sanitizeHTML(html) {
  try {
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    const walk = (node) => {
      for (const child of Array.from(node.childNodes)) {
        if (child.nodeType !== 1) continue; // skip text/comments
        const tag = child.tagName ? child.tagName.toUpperCase() : "";
        if (SAN_DROP_TAGS.has(tag)) { child.remove(); continue; }
        for (const attr of Array.from(child.attributes || [])) {
          const name = attr.name.toLowerCase();
          if (name.startsWith("on")) { child.removeAttribute(attr.name); continue; }
          if (SAN_URL_ATTRS.has(name) && !isSafeUrl(attr.value)) child.removeAttribute(attr.name);
        }
        if (tag === "A") { child.setAttribute("target", "_blank"); child.setAttribute("rel", "noopener noreferrer"); }
        walk(child);
      }
    };
    walk(tpl.content);
    return tpl.innerHTML;
  } catch (e) {
    console.warn("sanitizeHTML failed, escaping:", e);
    return escapeHTML(html);
  }
}

// --- inline icons: emoji + task-list checkboxes → Material Symbols icons ----------------
// Agent (and user) markdown routinely carries emoji (✅ ⚠️ 🔧 …) and GFM task
// lists (- [ ] / - [x]); we swap both for Material Symbols icons so the conversation reads
// as native to the rest of the UI. Conversions happen on the parsed-markdown DOM
// before it's sanitized: the checkbox <input> must be replaced before the
// sanitizer (which drops <input>) removes it, and emoji are swapped inside text
// nodes only (never code/pre). Every node is built with the DOM API — no string
// concatenation — so nothing here widens the XSS surface.
// Map keys omit the U+FE0F variation selector; the matcher consumes a trailing
// one. Unmapped emoji are left as-is. `cls` tints the symbol via a themed text-*.
const EMOJI_ICONS = {
  "✅": { name: "checkmark.circle.fill", cls: "text-ok" },
  "☑": { name: "checkmark.square.fill", cls: "text-ok" },
  "✔": { name: "checkmark", cls: "text-ok" },
  "❎": { name: "checkmark.square.fill", cls: "text-ok" },
  "❌": { name: "xmark.circle.fill", cls: "text-danger" },
  "✖": { name: "xmark", cls: "text-danger" },
  "⚠": { name: "exclamationmark.triangle.fill", cls: "text-warn" },
  "❗": { name: "exclamationmark.circle.fill", cls: "text-danger" },
  "❕": { name: "exclamationmark.circle", cls: "text-warn" },
  "❓": { name: "questionmark.circle.fill", cls: "text-brand" },
  "❔": { name: "questionmark.circle", cls: "text-faint" },
  "ℹ": { name: "info.circle.fill", cls: "text-brand" },
  "💡": { name: "lightbulb.fill", cls: "text-warn" },
  "🔧": { name: "wrench.fill" },
  "🛠": { name: "hammer.fill" },
  "⚙": { name: "gearshape.fill" },
  "📝": { name: "square.and.pencil" },
  "✏": { name: "pencil" },
  "📄": { name: "doc.text" },
  "📃": { name: "doc.text" },
  "📋": { name: "doc.on.clipboard" },
  "📁": { name: "folder.fill", cls: "text-brand" },
  "📂": { name: "folder.fill", cls: "text-brand" },
  "📦": { name: "shippingbox.fill" },
  "🔍": { name: "magnifyingglass" },
  "🔎": { name: "magnifyingglass" },
  "⭐": { name: "star.fill", cls: "text-warn" },
  "🌟": { name: "star.fill", cls: "text-warn" },
  "✨": { name: "sparkles", cls: "text-warn" },
  "🎉": { name: "sparkles", cls: "text-warn" },
  "🎊": { name: "sparkles", cls: "text-warn" },
  "🔥": { name: "flame.fill", cls: "text-danger" },
  "⚡": { name: "bolt.fill", cls: "text-warn" },
  "🚀": { name: "paperplane.fill", cls: "text-brand" },
  "🔒": { name: "lock.fill" },
  "🔓": { name: "lock.open.fill" },
  "🔑": { name: "key.fill", cls: "text-warn" },
  "📌": { name: "pin.fill", cls: "text-danger" },
  "📎": { name: "paperclip" },
  "🔗": { name: "link", cls: "text-brand" },
  "🏷": { name: "tag.fill" },
  "🔔": { name: "bell.fill" },
  "⏰": { name: "alarm.fill" },
  "⏱": { name: "timer" },
  "⏳": { name: "hourglass" },
  "⌛": { name: "hourglass" },
  "📅": { name: "calendar" },
  "📆": { name: "calendar" },
  "🗑": { name: "trash.fill", cls: "text-danger" },
  "👍": { name: "hand.thumbsup.fill", cls: "text-ok" },
  "👎": { name: "hand.thumbsdown.fill", cls: "text-danger" },
  "👀": { name: "eye.fill" },
  "👁": { name: "eye.fill" },
  "💻": { name: "laptopcomputer" },
  "🖥": { name: "desktopcomputer" },
  "📱": { name: "iphone" },
  "📊": { name: "chart.bar.fill", cls: "text-brand" },
  "📈": { name: "chart.line.uptrend.xyaxis", cls: "text-ok" },
  "📉": { name: "chart.line.downtrend.xyaxis", cls: "text-danger" },
  "🎯": { name: "target", cls: "text-danger" },
  "🐛": { name: "ladybug.fill", cls: "text-danger" },
  "❤": { name: "heart.fill", cls: "text-danger" },
  "💬": { name: "bubble.left.fill" },
  "📣": { name: "megaphone.fill" },
  "📢": { name: "megaphone.fill" },
  "🔄": { name: "arrow.triangle.2.circlepath" },
  "➡": { name: "arrow.right" },
  "⬅": { name: "arrow.left" },
  "⬆": { name: "arrow.up" },
  "⬇": { name: "arrow.down" },
  "▶": { name: "play.fill" },
  "⏸": { name: "pause.fill" },
  "⏹": { name: "stop.fill" },
  "🟢": { name: "circle.fill", cls: "text-ok" },
  "🔴": { name: "circle.fill", cls: "text-danger" },
  "🟡": { name: "circle.fill", cls: "text-warn" },
  "🟠": { name: "circle.fill", cls: "text-warn" },
  "🔵": { name: "circle.fill", cls: "text-brand" },
  "⚪": { name: "circle.fill", cls: "text-faint" },
  "⚫": { name: "circle.fill", cls: "text-ink" },
  // plain check / cross / sign marks (the heavy ✔/✖ above don't cover these)
  "✓": { name: "checkmark", cls: "text-ok" },
  "✗": { name: "xmark", cls: "text-danger" },
  "✘": { name: "xmark", cls: "text-danger" },
  "➕": { name: "plus" },
  "➖": { name: "minus" },
  "‼": { name: "exclamationmark", cls: "text-danger" },
  "⁉": { name: "questionmark", cls: "text-warn" },
  "✳": { name: "sparkle", cls: "text-warn" },
  "✴": { name: "sparkle", cls: "text-warn" },
  "❇": { name: "sparkle", cls: "text-warn" },
  "🆕": { name: "sparkles", cls: "text-brand" },
  "🆗": { name: "checkmark.circle", cls: "text-ok" },
  "🚧": { name: "exclamationmark.triangle.fill", cls: "text-warn" },
  "🛑": { name: "stop.fill", cls: "text-danger" },
  "⛔": { name: "nosign", cls: "text-danger" },
  "🛡": { name: "checkmark.shield.fill", cls: "text-ok" },
  "🏆": { name: "trophy.fill", cls: "text-warn" },
  "🎁": { name: "gift.fill", cls: "text-danger" },
  // AI / docs / knowledge
  "🤖": { name: "cpu", cls: "text-brand" },
  "🧠": { name: "brain.head.profile", cls: "text-brand" },
  "📚": { name: "books.vertical.fill", cls: "text-brand" },
  "📖": { name: "book.fill" },
  "📕": { name: "book.closed.fill", cls: "text-danger" },
  "📗": { name: "book.closed.fill", cls: "text-ok" },
  "📘": { name: "book.closed.fill", cls: "text-brand" },
  "📙": { name: "book.closed.fill", cls: "text-warn" },
  "📓": { name: "book.closed.fill" },
  "📔": { name: "book.closed.fill" },
  "📒": { name: "book.closed.fill", cls: "text-warn" },
  "🗒": { name: "note.text" },
  "🗓": { name: "calendar" },
  "🧩": { name: "puzzlepiece.extension", cls: "text-brand" },
  // tools / files
  "🔨": { name: "hammer.fill" },
  "🧰": { name: "wrench.and.screwdriver.fill" },
  "🔩": { name: "gearshape.fill" },
  "🎨": { name: "paintbrush.fill", cls: "text-brand" },
  "🖌": { name: "paintbrush.pointed.fill", cls: "text-brand" },
  "✂": { name: "scissors" },
  "🗃": { name: "folder.fill", cls: "text-brand" },
  "🗂": { name: "folder.fill", cls: "text-brand" },
  "🗄": { name: "folder.fill", cls: "text-brand" },
  "📤": { name: "square.and.arrow.up" },
  "📥": { name: "square.and.arrow.down" },
  "💾": { name: "internaldrive.fill" },
  // colored squares (status legends) + black/white shapes
  "🟩": { name: "square.fill", cls: "text-ok" },
  "🟥": { name: "square.fill", cls: "text-danger" },
  "🟨": { name: "square.fill", cls: "text-warn" },
  "🟧": { name: "square.fill", cls: "text-warn" },
  "🟦": { name: "square.fill", cls: "text-brand" },
  "🟪": { name: "square.fill" },
  "🟫": { name: "square.fill" },
  "⬛": { name: "square.fill", cls: "text-ink" },
  "⬜": { name: "square.fill", cls: "text-faint" },
  "◼": { name: "square.fill", cls: "text-ink" },
  "◻": { name: "square", cls: "text-faint" },
  "▪": { name: "square.fill", cls: "text-ink" },
  "▫": { name: "square", cls: "text-faint" },
  "🔲": { name: "square" },
  "🔳": { name: "square.fill" },
  "🔘": { name: "smallcircle.filled.circle" },
  // nav / arrows
  "↩": { name: "arrow.uturn.left" },
  "↪": { name: "arrow.uturn.right" },
  "🔼": { name: "chevron.up" },
  "🔽": { name: "chevron.down" },
  "⏭": { name: "forward.end.fill" },
  "⏮": { name: "backward.end.fill" },
  "⏩": { name: "forward.fill" },
  "⏪": { name: "backward.fill" },
  "🔝": { name: "arrow.up.to.line" },
  // world / network / weather / power
  "🌐": { name: "globe", cls: "text-brand" },
  "🌍": { name: "globe", cls: "text-brand" },
  "🌎": { name: "globe", cls: "text-brand" },
  "🌏": { name: "globe", cls: "text-brand" },
  "📡": { name: "antenna.radiowaves.left.and.right" },
  "📶": { name: "wifi" },
  "☀": { name: "sun.max.fill", cls: "text-warn" },
  "🌙": { name: "moon.fill" },
  "☁": { name: "cloud.fill" },
  "💧": { name: "drop.fill", cls: "text-brand" },
  "🔋": { name: "battery.100", cls: "text-ok" },
  "🔌": { name: "powerplug.fill" },
  // media / places / comms / commerce / people
  "📷": { name: "camera.fill" },
  "📸": { name: "camera.fill" },
  "🎥": { name: "video.fill" },
  "📹": { name: "video.fill" },
  "🖼": { name: "photo" },
  "🗺": { name: "map.fill" },
  "📍": { name: "mappin", cls: "text-danger" },
  "🏠": { name: "house.fill" },
  "🏡": { name: "house.fill" },
  "🏢": { name: "building.2.fill" },
  "📧": { name: "envelope.fill" },
  "✉": { name: "envelope.fill" },
  "📨": { name: "envelope.fill" },
  "📞": { name: "phone.fill" },
  "☎": { name: "phone.fill" },
  "💰": { name: "dollarsign.circle.fill", cls: "text-ok" },
  "💳": { name: "creditcard.fill" },
  "🛒": { name: "cart.fill" },
  "🎓": { name: "graduationcap.fill" },
  "👤": { name: "person.fill" },
  "🧑": { name: "person.fill" },
  "👥": { name: "person.2.fill" },
};
const EMOJI_RE = new RegExp(
  "(" + Object.keys(EMOJI_ICONS).map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|") + ")\\uFE0F?",
  "gu"
);

// iconizeChecklists swaps GFM task-list <input type=checkbox> for an Material Symbols icon
// (checked → filled box) and tags the row so the list marker is dropped. Runs on
// the parsed DOM before sanitizeHTML strips the <input>.
function iconizeChecklists(root) {
  root.querySelectorAll('input[type="checkbox"]').forEach((box) => {
    const checked = box.checked || box.hasAttribute("checked");
    const s = document.createElement("app-icon");
    s.setAttribute("name", checked ? "checkmark.square.fill" : "square");
    s.className = "md-task" + (checked ? " md-task-done" : "");
    box.replaceWith(s);
    const li = s.closest("li");
    if (li) li.classList.add("md-task-item");
  });
}

// iconizeEmoji replaces mapped emoji in text nodes with Material Symbols icons, skipping code
// spans/blocks (and any symbol we just made). Collects nodes first, then rewrites,
// so the live tree isn't mutated mid-walk.
function iconizeEmoji(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
  const targets = [];
  let n;
  while ((n = walker.nextNode())) {
    const p = n.parentNode;
    if (p && p.closest && p.closest("code, pre, app-icon")) continue;
    EMOJI_RE.lastIndex = 0;
    if (EMOJI_RE.test(n.nodeValue)) targets.push(n);
  }
  for (const node of targets) {
    const text = node.nodeValue, frag = document.createDocumentFragment();
    let last = 0, m;
    EMOJI_RE.lastIndex = 0;
    while ((m = EMOJI_RE.exec(text))) {
      if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
      const spec = EMOJI_ICONS[m[1]];
      const s = document.createElement("app-icon");
      s.setAttribute("name", spec.name);
      s.className = "md-emoji" + (spec.cls ? " " + spec.cls : "");
      s.setAttribute("aria-label", m[0]);
      frag.appendChild(s);
      last = m.index + m[0].length;
    }
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
    node.replaceWith(frag);
  }
}

// highlightCodeBlocks runs the dependency-free tokenizer over fenced code blocks
// (pre > code) so code in replies reads with the same GitHub-style syntax colors
// as the tool-call file viewer. highlight() escapes its input and emits only our
// own .tok-* spans, so assigning innerHTML carries no injection risk.
function highlightCodeBlocks(root) {
  root.querySelectorAll("pre > code").forEach((code) => {
    // marked tags fenced blocks with a language-<lang> class (it survives the
    // sanitizer); route shell languages to the bash-aware tokenizer.
    const lang = (String(code.className).match(/language-([\w-]+)/) || [])[1] || "";
    const hl = /^(bash|sh|shell|zsh|console|shell-session|shellscript)$/.test(lang) ? highlightBash : highlight;
    code.innerHTML = hl(code.textContent || "");
    // Wrap the <pre> in a non-scrolling .code-wrap and add a copy button, so it
    // stays pinned top-right even when the code scrolls horizontally.
    const pre = code.parentElement;
    if (!pre || (pre.parentElement && pre.parentElement.classList.contains("code-wrap"))) return;
    const wrap = el("div", "code-wrap");
    pre.parentNode.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    // A <span> (not <button>): the sanitizer drops <button>, so the copy control
    // is a role="button" span; the delegated click below handles the copy.
    const btn = el("span", "code-copy");
    btn.setAttribute("role", "button");
    btn.setAttribute("tabindex", "0");
    btn.setAttribute("aria-label", "Copy code");
    btn.appendChild(sym("doc.on.doc"));
    wrap.appendChild(btn);
  });
}

// Copy a code block to the clipboard. The buttons live inside sanitized markdown
// (no inline handlers survive), so the click is handled by delegation on the
// transcript; the icon briefly flips to a checkmark as feedback.
transcript.addEventListener("click", (e) => {
  const btn = e.target.closest && e.target.closest(".code-copy");
  if (!btn) return;
  const wrap = btn.closest(".code-wrap");
  const code = wrap && wrap.querySelector("pre code");
  if (!code) return;
  if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(code.textContent || "").catch(() => {});
  btn.classList.add("copied");
  const ic = btn.querySelector("app-icon"); if (ic) ic.setAttribute("name", "checkmark");
  setTimeout(() => { btn.classList.remove("copied"); const i2 = btn.querySelector("app-icon"); if (i2) i2.setAttribute("name", "doc.on.doc"); }, 1200);
});

// renderMD turns markdown into sanitized HTML, with emoji + task-list checkboxes
// rendered as Material Symbols icons and fenced code blocks syntax-highlighted. Falls back to
// escaped plain text if marked didn't load, so the UI degrades gracefully.
// EMOJI_ANY_RE matches a whole emoji grapheme (base + skin-tone / VS16 / keycap /
// ZWJ-joined parts). \p{Emoji_Presentation} = default-colorful emoji (excludes
// text symbols like © ® ™), and \p{Emoji}️ catches text-default chars forced
// to emoji presentation. Needs the /u flag.
const EMOJI_ANY_RE = /(?:\p{Emoji_Presentation}|\p{Emoji}\uFE0F)(?:\u200D(?:\p{Emoji_Presentation}|\p{Emoji})|[\u{1F3FB}-\u{1F3FF}\uFE0F\u20E3])*/gu;

// iconizeUnknownEmoji is the catch-all: after iconizeEmoji has swapped every
// mapped emoji for its Material Symbols icon, any emoji NOT in EMOJI_ICONS still renders as a
// native glyph. Replace each remaining one with a neutral Material Symbols icon so the UI
// never shows a native emoji. Runs after iconizeEmoji (mapped ones are already
// <app-icon>, so the walker skips them); meaning is lost for the long tail, which
// is the accepted trade-off for full visual consistency.
function iconizeUnknownEmoji(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
  const targets = [];
  let n;
  while ((n = walker.nextNode())) {
    const p = n.parentNode;
    if (p && p.closest && p.closest("code, pre, app-icon")) continue;
    EMOJI_ANY_RE.lastIndex = 0;
    if (EMOJI_ANY_RE.test(n.nodeValue)) targets.push(n);
  }
  for (const node of targets) {
    const text = node.nodeValue, frag = document.createDocumentFragment();
    let last = 0, m;
    EMOJI_ANY_RE.lastIndex = 0;
    while ((m = EMOJI_ANY_RE.exec(text))) {
      if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
      const s = document.createElement("app-icon");
      s.setAttribute("name", "sparkle");
      s.className = "md-emoji text-faint";
      s.setAttribute("aria-label", m[0]);
      frag.appendChild(s);
      last = m.index + m[0].length;
    }
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
    node.replaceWith(frag);
  }
}

function renderMD(text) {
  if (window.marked && marked.parse) {
    const tpl = document.createElement("template");
    tpl.innerHTML = marked.parse(text, { breaks: true, gfm: true });
    iconizeChecklists(tpl.content); // before the sanitizer drops <input>
    iconizeEmoji(tpl.content);      // mapped emoji → their Material Symbols icon (skips code/pre)
    iconizeUnknownEmoji(tpl.content); // any remaining emoji → a neutral Material Symbols icon
    highlightCodeBlocks(tpl.content);
    return sanitizeHTML(tpl.innerHTML);
  }
  return escapeHTML(text);
}

// mentionKind classifies a name against the live mention list for color coding —
// "worker" / "agent" / "skill" / "workflow", or "" when it isn't a known
// invocation (still highlighted, with the neutral brand tint).
function mentionKind(name) {
  const lc = name.toLowerCase();
  const mn = (mentionList || []).find((m) => m.name.toLowerCase() === lc);
  if (!mn) return "";
  return mn.kind || "agent";
}

// highlightMentions wraps @name tokens in an already-rendered message with a
// styled chip. It walks text nodes only, skipping code / pre / links / existing
// chips, so it never rewrites markup or touches code spans.
function highlightMentions(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (!node.nodeValue || node.nodeValue.indexOf("@") === -1) return NodeFilter.FILTER_REJECT;
      for (let p = node.parentNode; p && p !== root; p = p.parentNode) {
        const t = p.nodeName;
        if (t === "CODE" || t === "PRE" || t === "A" || (p.classList && p.classList.contains("mention"))) return NodeFilter.FILTER_REJECT;
      }
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  const targets = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n);
  // Known names sorted longest-first, so a multi-word name ("Superpower Workflow")
  // is chipped whole instead of being truncated at the first space.
  const known = (mentionList || []).map((m) => m.name).filter(Boolean).sort((a, b) => b.length - a.length);
  const isBoundary = (ch) => ch === "" || /[\s([]/.test(ch);
  for (const node of targets) {
    const text = node.nodeValue;
    const frag = document.createDocumentFragment();
    let last = 0, any = false;
    for (let i = 0; i < text.length; i++) {
      if (text[i] !== "@") continue;
      if (i > 0 && !isBoundary(text[i - 1])) continue;
      const rest = text.slice(i + 1);
      // Prefer the longest known name that matches here (handles spaces); the next
      // char must be a name boundary so "@reviewX" doesn't match "review".
      let name = null;
      for (const n of known) {
        if (rest.startsWith(n)) {
          const after = rest.charAt(n.length);
          if (after === "" || !/[A-Za-z0-9_]/.test(after)) { name = n; break; }
        }
      }
      // Fall back to the single-token shape for unknown @handles (still chipped,
      // neutral) — matches the original behavior.
      if (name === null) {
        const m = /^[A-Za-z0-9_](?:[A-Za-z0-9._-]*[A-Za-z0-9_])?/.exec(rest);
        if (!m) continue;
        name = m[0];
      }
      if (i > last) frag.appendChild(document.createTextNode(text.slice(last, i)));
      const chip = el("span", "mention", "@" + name);
      const kind = mentionKind(name);
      if (kind) chip.dataset.kind = kind;
      frag.appendChild(chip);
      any = true;
      i += name.length; // skip past the @ + name
      last = i + 1;
      i = last - 1; // for-loop ++ lands on `last`
    }
    if (!any) continue;
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
    node.parentNode.replaceChild(frag, node);
  }
}

// A "/command" token: a leading "/", lowercase-led name, preceded by start /
// whitespace / an opening bracket. Matched against the known command list so
// file paths (/tmp, /api/…) and uppercase paths are never touched.
const CMD_RE = /(^|[\s([])(\/[a-z][\w-]*)/g;

// highlightCommands wraps known slash commands in an already-rendered message
// with a bold brand chip. Walks text nodes only (skips code/pre/links/existing
// chips); only a token that's an actual command in commandList is wrapped.
function highlightCommands(root) {
  if (!commandList.length) return;
  const known = new Set(commandList.map((c) => c.name));
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (!node.nodeValue || node.nodeValue.indexOf("/") === -1) return NodeFilter.FILTER_REJECT;
      for (let p = node.parentNode; p && p !== root; p = p.parentNode) {
        const t = p.nodeName;
        if (t === "CODE" || t === "PRE" || t === "A" || (p.classList && p.classList.contains("cmd-name"))) return NodeFilter.FILTER_REJECT;
      }
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  const targets = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n);
  for (const node of targets) {
    const text = node.nodeValue;
    const frag = document.createDocumentFragment();
    let last = 0, m, any = false;
    CMD_RE.lastIndex = 0;
    while ((m = CMD_RE.exec(text))) {
      if (!known.has(m[2])) continue; // a "/word" that isn't a real command (e.g. a path) — leave it
      any = true;
      const start = m.index + m[1].length; // keep the leading char as plain text
      if (start > last) frag.appendChild(document.createTextNode(text.slice(last, start)));
      frag.appendChild(el("span", "cmd-name", m[2]));
      last = CMD_RE.lastIndex;
    }
    if (!any) continue;
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
    node.parentNode.replaceChild(frag, node);
  }
}

// appendMsg adds one answer node to the CURRENT (latest) pair's scrollable body,
// falling back to the transcript itself before the first question exists. All
// answer content (prose, thinking, tool/worker groups, notices, footers) flows
// through here so it lands in the right pair; the question itself is the pair's
// pinned header, added separately in the "user" event.
function appendMsg(node) { (answerHost || transcript).appendChild(node); }

// scrollBottom keeps the latest answer pinned to its newest output, but only
// while the user is already at the bottom — so scrolling up to read isn't yanked.
function scrollBottom() { const h = answerHost || transcript; if (stickBottom) h.scrollTop = h.scrollHeight; }

// --- tool-call grouping -----------------------------------------------------
// Consecutive tool calls collapse into one "N tool calls" disclosure; any
// assistant text / message between them ends the run (endToolGroup). Delegated-
// worker progress lines group the same way (see ensureWorkerGroup); the two runs
// are mutually exclusive — starting one closes the other.
function endToolGroup() { toolGroup = null; }
function ensureToolGroup() {
  if (toolGroup) return toolGroup;
  endWorkerGroup(); // a tool call interrupts any open run of worker updates
  const wrap = el("div", "tool-group");
  const head = el("div", "tool-group-head cursor-pointer");
  head.appendChild(sym("chevron.right", "tool-group-chevron shrink-0"));
  head.appendChild(sym("wrench.and.screwdriver.fill", "tool-group-dot shrink-0"));
  const label = el("span", "tool-group-label", "0 tool calls");
  head.appendChild(label);
  // Expanded by default (unlike the worker-update group below) — tool calls are
  // usually the interesting part of a turn, so showing them without an extra
  // click is the better default; the user can still collapse via the chevron.
  const body = el("div", "tool-group-body");
  head.classList.add("is-open");
  head.onclick = () => { head.classList.toggle("is-open", !body.classList.toggle("hidden")); };
  wrap.appendChild(head); wrap.appendChild(body);
  appendMsg(wrap);
  toolGroup = { body, label, count: 0 };
  return toolGroup;
}
function bumpToolGroup(g) {
  g.count++;
  g.label.textContent = g.count + (g.count === 1 ? " tool call" : " tool calls");
}

// --- worker-update grouping -------------------------------------------------
// A delegated worker streams discrete progress lines; rather than spraying them
// across the transcript, collapse a run into one "N worker updates" disclosure
// (collapsed by default — unlike the tool-call group above, which starts
// expanded), grouped the same way.
function endWorkerGroup() { workerGroup = null; }
function ensureWorkerGroup() {
  if (workerGroup) return workerGroup;
  endToolGroup(); // worker updates and tool calls are separate sequential runs
  const wrap = el("div", "worker-group");
  const head = el("div", "worker-group-head cursor-pointer");
  head.appendChild(sym("chevron.right", "worker-group-chevron shrink-0"));
  head.appendChild(sym("arrow.turn.down.right", "worker-group-dot shrink-0"));
  const label = el("span", "worker-group-label", "0 worker updates");
  head.appendChild(label);
  const body = el("div", "worker-group-body hidden");
  head.onclick = () => { head.classList.toggle("is-open", !body.classList.toggle("hidden")); };
  wrap.appendChild(head); wrap.appendChild(body);
  appendMsg(wrap);
  workerGroup = { body, label, count: 0 };
  return workerGroup;
}
function bumpWorkerGroup(g) {
  g.count++;
  g.label.textContent = g.count + (g.count === 1 ? " worker update" : " worker updates");
}

// --- tool-call viewer (the read_file "file-card" style, used for every tool) -
// highlight() is a tiny, dependency-free tokenizer (comments/strings/numbers/
// keywords). It escapes all source text and only emits our own <span> tags, so
// it's safe to assign via innerHTML. (Real multi-language highlighting would
// need a vendored library — the network blocks CDNs.)
const CODE_KEYWORDS = new Set("import from export default const let var function fn func return if else for while do class new async await yield true false null nil undefined none package type struct interface enum def public private protected static void switch case break continue this self in of is and or not try catch finally throw raise with as lambda match where impl mod use pub".split(" "));
function highlight(code) {
  const re = /(\/\*[\s\S]*?\*\/|\/\/[^\n]*|#[^\n]*)|(`(?:\\.|[^`\\])*`|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')|(\b\d[\w.]*\b)|([A-Za-z_$][\w$]*)/g;
  let out = "", last = 0, m;
  while ((m = re.exec(code))) {
    out += escapeHTML(code.slice(last, m.index));
    last = re.lastIndex;
    if (m[1]) out += `<span class="tok-c">${escapeHTML(m[1])}</span>`;
    else if (m[2]) out += `<span class="tok-s">${escapeHTML(m[2])}</span>`;
    else if (m[3]) out += `<span class="tok-n">${escapeHTML(m[3])}</span>`;
    else out += CODE_KEYWORDS.has(m[4]) ? `<span class="tok-k">${escapeHTML(m[4])}</span>` : escapeHTML(m[4]);
  }
  return out + escapeHTML(code.slice(last));
}
// BASH_WORDS: shell keywords + common builtins/CLI tools to color as keywords.
// Liberal on purpose — highlightBash runs ONLY on bash, so coloring program names
// (git/grep/make/…) carries no risk of bleeding into other languages.
const BASH_WORDS = new Set(("if then else elif fi for while until do done case esac in function select return " +
  "echo printf cd export local readonly declare typeset unset source eval exec exit trap shift set read alias time test " +
  "sudo env grep sed awk find xargs cat head tail ls cp mv rm mkdir touch chmod chown ln tar curl wget ssh scp rsync " +
  "git make go cargo npm npx node yarn python python3 pip pip3 docker kubectl brew xcrun codesign open defaults launchctl kill ps which").split(/\s+/));
// highlightBash is a bash-flavored variant of highlight(): on top of the shared
// string/number/comment rules it adds $-variable and shell-keyword/builtin
// awareness. Used for the bash tool-card body and ```bash fenced blocks. Like
// highlight() it escapes all input and emits only our own tok-* spans, so the
// result is safe to assign via innerHTML.
function highlightBash(code) {
  const re = /(#[^\n]*)|("(?:\\.|[^"\\])*"|'[^']*')|(\$\{[^}]*\}|\$[A-Za-z_]\w*|\$[-\d?@*#!$])|(\b\d[\w.]*\b)|([A-Za-z_][\w-]*)/g;
  let out = "", last = 0, m;
  while ((m = re.exec(code))) {
    out += escapeHTML(code.slice(last, m.index));
    last = re.lastIndex;
    if (m[1]) out += `<span class="tok-c">${escapeHTML(m[1])}</span>`;        // # comment
    else if (m[2]) out += `<span class="tok-s">${escapeHTML(m[2])}</span>`;   // "string" / 'string'
    else if (m[3]) out += `<span class="tok-n">${escapeHTML(m[3])}</span>`;   // $var / ${…} / $1
    else if (m[4]) out += `<span class="tok-n">${escapeHTML(m[4])}</span>`;   // number
    else out += BASH_WORDS.has(m[5]) ? `<span class="tok-k">${escapeHTML(m[5])}</span>` : escapeHTML(m[5]);
  }
  return out + escapeHTML(code.slice(last));
}
function looksDiff(text) {
  return String(text || "").split("\n").some((l) => l.startsWith("@@") || l[0] === "+" || l[0] === "-");
}
// buildCodeView renders text as a line-numbered code body: a gutter of numbers +
// the source, colored as a diff (when it looks like one) or syntax-highlighted.
// lang === "bash" selects the bash-aware tokenizer; anything else uses highlight().
function buildCodeView(text, lang) {
  const t = String(text || "");
  const lines = t.split("\n");
  const wrap = el("div", "file-code");
  wrap.appendChild(el("pre", "file-gutter", lines.map((_, i) => String(i + 1)).join("\n")));
  const pre = el("pre", "file-pre");
  if (looksDiff(t)) {
    for (const line of lines) {
      const cls = diffLineClass(line);
      pre.appendChild(el("div", "diff-line" + (cls ? " " + cls : ""), line.length ? line : " "));
    }
  } else {
    const code = document.createElement("code");
    code.innerHTML = (lang === "bash" ? highlightBash : highlight)(t);
    pre.appendChild(code);
  }
  wrap.appendChild(pre);
  return wrap;
}

// bashCommand unwraps the shell command from a bash tool event — its input is the
// JSON envelope {"command": "…"} — or returns null for any other tool. The card
// then shows and colors the command itself rather than the raw JSON.
function bashCommand(ev) {
  return ev && ev.tool === "bash" ? jsonField(ev.input, "command") : null;
}
// clipCmd renders a command as a single-line header label (first line, truncated),
// so the bash card still shows what ran once its body is replaced by the output.
function clipCmd(cmd) {
  const one = String(cmd || "").split("\n")[0].trim();
  return one.length > 110 ? one.slice(0, 109) + "…" : one;
}

// prettyArgs renders a tool's JSON input as readable, indented key/value lines
// instead of one clipped line: scalar values inline ("key: value"), and
// multi-line string values (e.g. a task prompt) on their own lines indented
// beneath a "key:" header. Well-known keys are ordered first for stability, the
// rest alphabetically. When model is given (the task tool), it's appended to the
// "agent" line so the card shows which model the delegation runs on. maxVal, when
// > 0, truncates any single value longer than that many chars to a "first N…
// (LEN chars)" preview — so a big value (e.g. write_file's whole content) doesn't
// dump into the parameters block, while the diff/output still shows the change.
// Returns "" when input isn't a non-empty JSON object. Mirrors the TUI's prettyArgs.
function prettyArgs(input, model, maxVal) {
  let args;
  try { args = JSON.parse(String(input || "").trim()); }
  catch (_) { return ""; }
  if (!args || typeof args !== "object" || Array.isArray(args) || !Object.keys(args).length) return "";
  if (model && typeof args.agent === "string") {
    args = Object.assign({}, args);
    args.agent = args.agent + "  ·  " + prettyModel(model);
  }
  const priority = ["description", "agent", "subagent_type", "command", "prompt"];
  const seen = {};
  const order = [];
  for (const k of priority) if (k in args) { order.push(k); seen[k] = true; }
  Object.keys(args).filter((k) => !seen[k]).sort().forEach((k) => order.push(k));
  const lines = [];
  for (const k of order) {
    const v = args[k];
    let val = typeof v === "string" ? v : (v == null ? "null" : JSON.stringify(v));
    if (maxVal > 0 && val.length > maxVal) val = val.slice(0, maxVal) + "… (" + val.length + " chars)";
    if (val.indexOf("\n") >= 0) {
      lines.push(k + ":");
      lines.push(val.replace(/\n+$/, "").split("\n").map((l) => "  " + l).join("\n"));
    } else {
      lines.push(k + ": " + val);
    }
  }
  return lines.join("\n");
}

// buildToolCard renders one tool call in the file-card style: a header (collapse
// chevron + state icon + "tool  summary" + diff badge + copy) over a collapsible
// body. lang is forwarded to buildCodeView (e.g. "bash"). opts.inMode /
// opts.outMode pick how the body renders at start / on completion: "code" (the
// default, line-numbered + highlighted), "args" (a JSON dict as an indented
// key/value block — used for the task tool's input), or "md" (rendered markdown
// — used for a sub-agent's task result). Returns refs so tool_end can flip the
// state icon and swap in the result via ref.renderBody; ref.content is what the
// copy button writes.
function buildToolCard(tool, summary, content, stats, lang, opts) {
  opts = opts || {};
  const ref = { content: String(content || ""), lang: lang, outMode: opts.outMode || "code" };
  // argsText: the model's tool parameters, pinned atop the body for every tool
  // that opts in (opts.argsInput) — i.e. all but bash (command IS the body) and
  // task (args ARE the body). Long values are truncated so a big write_file
  // content doesn't dump (the diff still shows the change). It persists across the
  // start→output body swap because renderBody re-emits it each render.
  ref.argsText = opts.argsInput ? prettyArgs(opts.argsInput, opts.model, 200) : "";
  const card = el("div", "file-card msg");
  const head = el("div", "file-card-head cursor-pointer");
  const chev = sym("chevron.right", "file-chevron inline-block transition-transform shrink-0");
  head.appendChild(chev);
  ref.stateWrap = el("span", "tool-state shrink-0");
  if (tool === "task") {
    // Sub-agent delegation: show the jumping Claude while the task runs; the
    // tool_end handler clears this and swaps in ✓/✗.
    const art = el("img", "tool-run-art"); art.src = "/static/icon/claude-jumping.svg"; art.alt = "running…"; art.title = "running task…";
    ref.stateWrap.appendChild(art);
  } else {
    ref.stateWrap.appendChild(sym("circle.dotted", "text-faint")); // running; → ✓/✗ on end
  }
  head.appendChild(ref.stateWrap);
  head.appendChild(el("span", "file-path", tool + (summary ? "  " + stripStats(summary) : "")));
  if (stats) appendDiffCount(head, stats);
  head.appendChild(el("span", "flex-1"));
  const copyBtn = el("button", "file-copy shrink-0"); copyBtn.title = "Copy"; copyBtn.appendChild(sym("doc.on.doc"));
  copyBtn.onclick = (e) => { e.stopPropagation(); if (navigator.clipboard) navigator.clipboard.writeText(ref.content); };
  head.appendChild(copyBtn);
  ref.body = el("div", "file-card-body hidden");
  // renderBody swaps the body content in the requested mode: "md" (rendered
  // markdown), "args" (JSON input as an indented key/value block), or "code".
  // When the card has parameters (ref.argsText), they're emitted first as a small
  // labeled block, then the content below — so what the model asked for stays
  // visible even after the body swaps from the start state to the output.
  ref.renderBody = (text, mode) => {
    ref.body.innerHTML = "";
    if (ref.argsText) {
      const block = el("div", "file-args-block");
      block.appendChild(el("div", "file-args-label", "parameters"));
      block.appendChild(el("pre", "file-args", ref.argsText));
      ref.body.appendChild(block);
    }
    const hasText = String(text || "") !== "";
    if (mode === "md") {
      const md = el("div", "md break-words file-md");
      md.innerHTML = renderMD(String(text || ""));
      markDiagramPres(md); // tag architecture diagrams in a task/sub-agent result
      ref.body.appendChild(md);
    } else if (mode === "args") {
      ref.body.appendChild(el("pre", "file-args", prettyArgs(text, opts.model) || String(text || "")));
    } else if (hasText || !ref.argsText) {
      // Skip an empty code view while a tool is still running and only its params
      // are known yet (the output arrives on tool_end and re-renders the body).
      ref.body.appendChild(buildCodeView(text, ref.lang));
    }
  };
  ref.renderBody(ref.content, opts.inMode || "code");
  head.onclick = (e) => { if (e.target.closest(".file-copy")) return; const hidden = ref.body.classList.toggle("hidden"); chev.classList.toggle("rotate-90"); if (ref.kids) ref.kids.classList.toggle("hidden", hidden); };
  card.appendChild(head); card.appendChild(ref.body);
  ref.card = card;
  return ref;
}

// ensureToolKids returns (creating once) the container inside a parent tool card
// that holds a delegated sub-agent's nested tool cards. It starts collapsed iff
// the parent's body is collapsed, so expanding the parent reveals the steps.
function ensureToolKids(parentRef) {
  if (!parentRef.kids) {
    parentRef.kids = el("div", "tool-children");
    if (parentRef.body.classList.contains("hidden")) parentRef.kids.classList.add("hidden");
    parentRef.card.appendChild(parentRef.kids);
  }
  return parentRef.kids;
}

// buildNestedToolCard renders one sub-agent tool call as a file-card for nesting
// under its parent `task` card. It mirrors the top-level tool_start rendering
// (bash unwrap, diff/stats) but prefixes the header with the persona ("explore ›
// read_file") and tags the card so it can be styled as a nested child.
function buildNestedToolCard(ev) {
  const cmd = bashCommand(ev);
  const diffText = ev.diff || ev.input || "";
  const stats = headerStats(ev.summary, diffText);
  const label = (ev.agent ? ev.agent + " › " : "") + ev.tool;
  let ref;
  if (cmd != null) ref = buildToolCard(label, clipCmd(cmd), cmd, stats, "bash");
  else if (ev.tool === "task") ref = buildToolCard(label, ev.summary, ev.input, stats, null, { inMode: "args", outMode: "md", model: ev.model });
  else ref = buildToolCard(label, ev.summary, ev.diff || "", stats, null, { argsInput: ev.input });
  ref.stats = stats;
  ref.isDiff = cmd == null && ev.tool !== "task" && looksDiff(diffText);
  ref.card.classList.add("tool-child");
  return ref;
}

// fmtDur renders an elapsed-time like the reference: 416ms / 53.8s / 3m 34s.
function fmtDur(ms) {
  if (ms < 1000) return Math.round(ms) + "ms";
  const s = ms / 1000;
  if (s < 60) return (s < 10 ? s.toFixed(1) : Math.round(s)) + "s";
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}
// tokenMetaParts builds the footer's token + cache-hit segments from a turn's
// total input/output tokens and the cached-input subset. The cache-hit % is the
// key signal for why a long turn was (in)expensive — a high hit rate means the
// re-sent context was served cheaply from the prompt cache. Shown whenever the
// turn reported input tokens (even at 0%, which is itself the warning sign).
// Uses the shared fmtTokens (declared later; resolved at call time).
function tokenMetaParts(inTok, outTok, cacheRead) {
  const parts = [];
  if (inTok > 0) {
    parts.push(`${fmtTokens(inTok)}↑ ${fmtTokens(outTok)}↓`);
    const pct = Math.round((Math.min(cacheRead, inTok) / inTok) * 100);
    parts.push(`cache ${pct}%`);
  }
  return parts;
}
// prettyModel maps a model id to a polite, human name with its version, e.g.
// "anthropic.claude-opus-4-8" -> "Claude Opus 4.8", "claude-3-5-sonnet" ->
// "Claude Sonnet 3.5". Unknown vendors/models are left as-is.
function prettyModel(m) {
  const raw = String(m || "");
  const x = raw.toLowerCase();
  // Gemini: turn "gcp:gemini-3.5-flash" / "gemini-2.5-pro" into "Gemini 3.5 Flash"
  // / "Gemini 2.5 Pro" — strip the gcp: prefix, title-case word tokens, keep
  // version/number tokens as-is.
  if (x.includes("gemini")) {
    return raw.replace(/^gcp:/i, "").split("-").filter(Boolean)
      .map((t) => /\d/.test(t) ? t : t.charAt(0).toUpperCase() + t.slice(1))
      .join(" ");
  }
  let family = "";
  // Lightweight pretty-names for the newer public-provider model families
  // (Moonshot/Z.AI/DashScope/NVIDIA/Hugging Face/Venice); unmatched ids fall
  // through unchanged, same as any other unknown vendor.
  if (x.includes("kimi")) {
    const v = x.match(/kimi-(k\d+)/);
    return v ? `Kimi ${v[1].toUpperCase()}` : "Kimi";
  }
  if (x.includes("glm")) {
    const v = x.match(/glm-([\d.]+)/);
    return v ? `GLM ${v[1]}` : "GLM";
  }
  if (x.startsWith("qwen") || x.includes("/qwen")) {
    return raw.split("/").pop().split("-").filter(Boolean)
      .map((t) => {
        const size = t.match(/^(\d+(?:\.\d+)?)([bm])$/i); // "72b" -> "72B", not left lowercase
        if (size) return size[1] + size[2].toUpperCase();
        return t.charAt(0).toUpperCase() + t.slice(1); // "qwen2.5" -> "Qwen2.5", not skipped
      }).join(" ");
  }
  if (x.includes("nemotron")) {
    const v = x.match(/(\d+(?:\.\d+)?b)/i);
    return v ? `Nemotron ${v[1].toUpperCase()}` : "Nemotron";
  }
  if (x.includes("llama")) {
    const v = x.match(/llama-?([\d.]+)-?(\d+b)?/i);
    return v ? `Llama ${v[1]}${v[2] ? " " + v[2].toUpperCase() : ""}` : "Llama";
  }
  if (x.includes("opus")) family = "Opus";
  else if (x.includes("sonnet")) family = "Sonnet";
  else if (x.includes("haiku")) family = "Haiku";
  else if (x.includes("fable")) return "Fable";
  if (!family) return raw; // unknown model: don't mangle it

  // Pull the version sitting next to the family word — after it in newer ids
  // (claude-opus-4-8) or before it in older ones (claude-3-5-sonnet) — and join
  // the parts with dots. Drop date-like stamps (e.g. -20241022) so they don't
  // masquerade as a version, and fall back to the other position when the
  // adjacent token is only a date.
  const fam = family.toLowerCase();
  const pick = (re) => {
    const mm = x.match(re);
    return mm ? mm[1].split(/[-_.]/).filter((t) => t && t.length < 5).join(".") : "";
  };
  // Separator before/after the version may be "-", "_", ".", or a space, so the
  // helper is idempotent: re-running it on an already-pretty "Claude Opus 4.8"
  // (space before the number) still keeps the version instead of dropping it.
  const ver = pick(new RegExp(fam + "[-_. ]?(\\d+(?:[-_.]\\d+)*)")) ||
    pick(new RegExp("(\\d+(?:[-_.]\\d+)*)[-_. ]?" + fam));
  return ver ? `Claude ${family} ${ver}` : `Claude ${family}`;
}
// providerLabel maps a /provider backend id to the friendly name shown before
// the model in the composer meta. Unknown/empty ids return "" so the caller can
// omit the provider segment entirely.
function providerLabel(id) {
  switch (String(id || "").toLowerCase()) {
    case "ollama": return "Ollama";
    case "openai": return "OpenAI";
    case "anthropic": return "Anthropic";
    case "sqe-anthropic": return "SQE Anthropic";
    case "openrouter": return "OpenRouter";
    case "gemini": return "Gemini";
    case "deepseek": return "DeepSeek";
    case "groq": return "Groq";
    case "xai": return "xAI";
    case "mistral": return "Mistral";
    case "together": return "Together.ai";
    case "fireworks": return "Fireworks.ai";
    case "moonshot": return "Moonshot";
    case "zai": return "Z.AI";
    case "dashscope": return "DashScope";
    case "nvidia": return "NVIDIA NIM";
    case "huggingface": return "Hugging Face";
    case "venice": return "Venice";
    case "omniroute": return "OmniRoute";
    default: return "";
  }
}

// renderProviderList turns the "/provider" command's raw "Model providers:"
// text (fixed-width columns meant for a monospace terminal, which HTML
// collapses into a run-on paragraph) into a scannable row list: an id pill,
// the friendly label, and the description with any MOONY_* env var set as
// inline code. `rest` is everything after the "Model providers:\n" header
// line, including the trailing "(* = active. ...)" footer note.
function renderProviderList(rest) {
  const wrap = el("div", "msg-provider-list");
  for (const line of rest.split("\n")) {
    if (!line) continue;
    if (line.startsWith("(")) {
      wrap.appendChild(el("div", "text-faint text-xs mt-2", line));
      continue;
    }
    const m = line.match(/^(.)\s(\S+)\s+(.*)$/);
    if (!m) continue;
    const [, mark, id, desc] = m;
    const active = mark === "*";
    const row = el("div", "msg-provider-row" + (active ? " is-active" : ""));
    row.appendChild(el("span", "msg-info-pill", id));
    const label = providerLabel(id);
    if (label) row.appendChild(el("span", "msg-provider-label", label));
    if (active) row.appendChild(sym("checkmark.circle.fill", "msg-provider-check"));
    const descEl = el("span", "msg-provider-desc");
    appendWithCode(descEl, desc, /MOONY_[A-Z0-9_]+/g);
    row.appendChild(descEl);
    wrap.appendChild(row);
  }
  return wrap;
}

// appendWithCode appends `text` to `node` as child nodes, wrapping every
// match of `re` (a global regex) in a <code> element instead of plain text.
function appendWithCode(node, text, re) {
  let last = 0, m;
  re.lastIndex = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) node.append(text.slice(last, m.index));
    node.appendChild(el("code", "", m[0]));
    last = m.index + m[0].length;
  }
  if (last < text.length) node.append(text.slice(last));
}

// fmtDateTime renders a Date as a readable local "YYYY-MM-DD HH:MM" stamp for the
// per-turn / per-message footer (e.g. "2026-06-19 08:52").
function fmtDateTime(d) {
  try {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  } catch (_) {
    return d.toISOString();
  }
}

// turnMetaLine builds the "datetime · duration · cost · model · reasoning:level"
// footer element shared by the live render and session replay.
function turnMetaLine(parts) {
  return el("div", "turn-meta text-faint text-[11px] font-mono", parts.join("  ·  "));
}

// userMetaFooter builds the footer under a user message: a datetime stamp plus a
// copy button that copies the prompt text. text is the raw prompt to copy.
function userMetaFooter(text, when) {
  const row = el("div", "user-meta flex items-center gap-1.5 mt-1 text-faint text-[11px] font-mono");
  row.appendChild(el("span", null, fmtDateTime(when || new Date())));
  if (text) {
    const copyBtn = el("button", "user-copy shrink-0");
    copyBtn.title = "Copy";
    copyBtn.appendChild(sym("doc.on.doc"));
    copyBtn.onclick = (e) => { e.stopPropagation(); if (navigator.clipboard) navigator.clipboard.writeText(text); };
    row.appendChild(copyBtn);

    const askBtn = el("button", "user-copy shrink-0");
    askBtn.title = "Ask again";
    askBtn.appendChild(sym("arrow.clockwise"));
    // Re-ask: drop the question into the composer and send it through the normal
    // pipeline (so busy/queue handling and any staged attachments still apply).
    askBtn.onclick = (e) => {
      e.stopPropagation();
      if (typeof input === "undefined" || !input) return;
      input.value = text;
      if (typeof send === "function") send();
      else input.focus();
    };
    row.appendChild(askBtn);
  }
  return row;
}

// renderTurnMeta appends a "datetime · duration · cost · model · reasoning:<level>"
// footer after a live turn. It fires for every completed live turn, matching the
// server, which persists a turn_meta record for every turn with no duration gate
// (see recordTurnMeta) — so the footer is consistent between the live view and a
// reopened session. (Session replay renders persisted "turn_meta" events via
// renderReplayedTurnMeta, not this path, so there's no risk of footer spam from
// the burst of replay events.) Cost is this turn's answering cost (from the
// turn_done event), not the cumulative session total.
function renderTurnMeta() {
  if (!turnActive) return;
  turnActive = false;
  const ms = turnStartMs ? Date.now() - turnStartMs : 0;
  const s = lastStatus || {};
  const parts = [fmtDateTime(new Date()), fmtDur(ms)];
  if (lastTurnCost > 0) parts.push(fmtUSD(lastTurnCost));
  // Prefer the model that actually served this turn (adaptive/cheap-reads can
  // downshift so the session model never ran); fall back to the status model.
  const turnModel = lastTurnModel || s.model;
  if (turnModel) parts.push(prettyModel(turnModel)); // polite model name with version, e.g. "Claude Opus 4.8"
  // Prefer the reasoning that actually served this turn (a Fast-tier downshift
  // can make it differ from the composer's level); fall back to the status level.
  const turnReasoning = lastTurnReasoning || s.reasoning;
  if (turnReasoning) parts.push("reasoning: " + turnReasoning); // thinking/reasoning level
  if (curStep > 0) parts.push(curStep + (curStep === 1 ? " call" : " calls")); // model calls made this turn
  parts.push(...tokenMetaParts(lastTurnIn, lastTurnOut, lastTurnCacheRead)); // total tokens ↑↓ + prompt-cache hit rate
  appendMsg(turnMetaLine(parts));
  scrollBottom();
}

// renderReplayedTurnMeta appends a persisted per-turn footer when a saved
// session is reopened, so each past turn shows its datetime · duration · cost ·
// model · reasoning:level.
function renderReplayedTurnMeta(meta) {
  if (!meta) return;
  const parts = [];
  if (meta.time) { const d = new Date(meta.time); if (!isNaN(d)) parts.push(fmtDateTime(d)); }
  if (meta.durationMs) parts.push(fmtDur(meta.durationMs));
  if (meta.costUSD > 0) parts.push(fmtUSD(meta.costUSD));
  if (meta.model) parts.push(prettyModel(meta.model));
  if (meta.reasoning) parts.push("reasoning: " + meta.reasoning);
  if (meta.calls > 0) parts.push(meta.calls + (meta.calls === 1 ? " call" : " calls"));
  parts.push(...tokenMetaParts(meta.inTokens, meta.outTokens, meta.cacheReadTokens)); // total tokens ↑↓ + prompt-cache hit rate
  if (!parts.length) return;
  appendMsg(turnMetaLine(parts));
}

// renderTaskMeta appends a per-delegation footer (duration · cost · model ·
// tool calls · tokens) inside a `task` card when its sub-agent finishes,
// mirroring the parent's per-turn footer but scoped to the delegation. parentRef
// is the task card's tool ref; meta is the task_meta event's payload.
function renderTaskMeta(parentRef, meta) {
  if (!parentRef || !meta || parentRef.taskMeta) return; // once per delegation
  const parts = [];
  if (meta.durationMs) parts.push(fmtDur(meta.durationMs));
  if (meta.costUSD > 0) parts.push(fmtUSD(meta.costUSD));
  if (meta.model) parts.push(prettyModel(meta.model));
  if (meta.calls > 0) parts.push(meta.calls + (meta.calls === 1 ? " tool call" : " tool calls"));
  if (meta.inTokens > 0) parts.push(`${fmtTokens(meta.inTokens)}↑ ${fmtTokens(meta.outTokens)}↓`);
  if (!parts.length) return;
  const line = turnMetaLine(parts);
  line.classList.add("task-meta");
  parentRef.card.appendChild(line); // after head/body/kids → sits at the card's bottom
  parentRef.taskMeta = line;
}

// renderDiff paints a unified-diff string into `pre` with per-line coloring —
// added (green) / removed (red) / hunk (@@) / file headers — so the approval
// prompt and tool-call previews read like a real diff. Handles both the
// internal diff format ("+ " / "- " / "  ") and raw `git diff`. Anything that
// doesn't look like a diff falls back to a plain block, so it never mis-colors a
// shell command, JSON input, or file body.
function diffLineClass(line) {
  if (line.startsWith("@@")) return "diff-hunk";
  if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff --git") || line.startsWith("index ")) return "diff-meta";
  const c = line[0];
  if (c === "+") return "diff-add";
  if (c === "-") return "diff-del";
  return ""; // context / elision / blank
}
// diffStats counts added / removed lines (file headers + hunks excluded) for the
// +N −N badge shown in the approval header.
function diffStats(text) {
  let add = 0, del = 0;
  for (const line of String(text || "").split("\n")) {
    const c = diffLineClass(line);
    if (c === "diff-add") add++;
    else if (c === "diff-del") del++;
  }
  return { add, del };
}
// headerStats resolves the +N −N counts: prefer the authoritative "(+N -M)" the
// Go side embeds in the summary (correct even for large/elided diffs), else fall
// back to counting the rendered diff lines.
function headerStats(summary, diffText) {
  const m = String(summary || "").match(/\(\+(\d+) -(\d+)\)\s*$/);
  return m ? { add: +m[1], del: +m[2] } : diffStats(diffText);
}
// stripStats drops that trailing "(+N -M)" so the count isn't shown twice beside
// the colored badge.
const stripStats = (s) => String(s || "").replace(/\s*\(\+\d+ -\d+\)\s*$/, "");
// appendDiffCount adds the colored +N −N badge to a header node, when nonzero.
function appendDiffCount(node, st) {
  if (!st || (!st.add && !st.del)) return;
  node.appendChild(el("span", "diff-count diff-count-add", "+" + st.add));
  node.appendChild(el("span", "diff-count diff-count-del", "−" + st.del));
}
function renderDiff(pre, text) {
  const t = text || "";
  pre.textContent = "";
  pre.classList.remove("diff");
  const lines = t.split("\n");
  const looksDiff = lines.some((l) => l.startsWith("@@") || l[0] === "+" || l[0] === "-");
  if (!looksDiff) { pre.textContent = t; return; }
  pre.classList.add("diff");
  for (const line of lines) {
    const cls = diffLineClass(line);
    pre.appendChild(el("div", "diff-line" + (cls ? " " + cls : ""), line.length ? line : " "));
  }
}

// api prefixes a path with the focused laptop's base ("" for the standalone
// server, "/l/{host}" via the relay), so the same calls reach the right machine.
