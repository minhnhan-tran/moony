# Extensibility

[← Back to README](../README.md)

## Project instructions: `AGENTS.md`

When the agent starts, it assembles instruction files into the system prompt, least- to most-specific (so the closest file's guidance wins):

1. a **global** file at `~/.config/moony/AGENTS.md` (honoring `$XDG_CONFIG_HOME`; this same `~/.config/moony` location is used on macOS and Linux) for personal, cross-project rules;
2. every project `AGENTS.md` from the repo root down to the working directory — the walk stops at the repo root (a `.git` dir) or `$HOME`, whichever comes first.

`CLAUDE.md` is accepted as a fallback wherever `AGENTS.md` is looked for, so existing repos work without renaming (`AGENTS.md` takes precedence if both are present in a directory). Use these for project conventions, build commands, "always do X / never touch Y" rules, and so on. Each file is capped at 16 KB.

## Memory

Save durable notes that persist across sessions, à la Claude Code's `/memory`:

- **`/memory`** — show the notes currently saved in the project and global files.
- **`/memory <note>`** — append a note; **`/memory --global <note>`** targets the user-level file.
- **`# <note>`** — a leading `#` on the input line is a quick-save shortcut (it saves instead of messaging the model).
- The model can also save facts itself via a non-interactive **`remember`** tool.

Notes are appended under a `## Memory` section of the relevant `AGENTS.md` (so they're just part of the instruction files above), and a freshly saved note is reloaded into the system prompt immediately — no restart needed.

## Custom slash commands

Drop `.md` files into either:

- `~/.config/moony/commands/<name>.md` (global)
- `./.moony/commands/<name>.md` (per-project; overrides global)

Each file's body becomes a templated user message; `$ARGUMENTS` is replaced with whatever you typed after the command name. Optional frontmatter sets a one-line description shown in the menu:

```markdown
---
description: review my staged diff and suggest improvements
---

Please review the following staged diff and suggest improvements:

$ARGUMENTS
```

Run it as `/review` (or `/review with focus on tests`).

## Sub-agents (`task` tool)

Define specialized personas in `.md` files at `~/.config/moony/agents/<name>.md` or `./.moony/agents/<name>.md`. The model can delegate self-contained subtasks to them via a built-in `task` tool, getting back just the persona's final answer (so the parent's history stays focused).

```markdown
---
description: read-only explorer that surveys a codebase
tools: read_file, list_dir, glob, grep
---

You are a code explorer. Survey the repo to answer the user's question briefly.
Never write or run anything; you have read-only tools.
```

The optional `tools:` allowlist restricts which built-in tools the persona can use (omit for full access).

When the model dispatches several `task` calls in one turn, they run concurrently (you approve each up front, then they execute in parallel). Set `MN_PARALLEL_TOOLS=0` to run them one at a time.

**Invoke a subagent directly with `@<name>`.** Typing `@explore find the auth flow` in a message — where `explore` is an enabled persona — appends a directive telling the model to carry out the request via the `task` tool with that agent. The same works for [worker agents](#worker-agents-delegate-to-claude-code--optimus) (`@claude review this`), which route to the `delegate` tool instead. You still approve the dispatch like any other tool call, and the model summarizes what the agent returned. A `@token` that resolves to a real file stays a file mention, and `@tokens` that don't match any agent (or look like paths) are left untouched.

## Hooks

Run user scripts at lifecycle points by adding either `~/.config/moony/hooks.json` or `./.moony/hooks.json`:

```json
{
  "pre_turn":  ["echo started >> /tmp/sqe.log"],
  "post_turn": ["my-notifier 'sqe finished'"],
  "pre_tool":  ["./scripts/audit-tool.sh"],
  "post_tool": ["./scripts/log-tool.sh"]
}
```

Hooks run via `sh -c` with these env vars: `MN_HOOK`, `MN_WORKING_DIR`, plus `MN_TOOL_NAME` / `MN_TOOL_INPUT` (pre_tool) / `MN_TOOL_OUTPUT` / `MN_TOOL_ERROR` (post_tool). 10-second timeout; a hook that fails is logged to stderr and does **not** block the agent.

### Blocking a tool call from a `pre_tool` hook

A `pre_tool` hook that exits with status **77** *blocks* the call: the tool doesn't run, and the hook's own output is handed to the model as the reason, so your policy can explain itself. This makes hooks an enforcement point rather than just an audit log — e.g. refuse writes outside the repo:

```sh
#!/bin/sh
case "$MN_TOOL_NAME" in
  write_file|edit_file)
    echo "$MN_TOOL_INPUT" | grep -q '"path": *"/' && {
      echo "absolute paths are not allowed by project policy"
      exit 77
    } ;;
esac
```

Only `pre_tool` can block, and only with exit code 77 — every other non-zero status is treated as a broken hook and ignored. That distinction is deliberate: `sh` already exits 2 on a syntax error and 127 on "command not found", so treating any failure as a deny would let a typo silently wedge every tool call. A hook that times out or hangs never blocks either.

Sub-agents (`task`) inherit the parent's hooks and sandbox setting, so a `pre_tool` policy covers them too. This matters because a sub-agent auto-runs its tools with no approval prompt — hooks are the only per-call gate it has.

## Agent Skills

**Agent Skills** are Claude-style `SKILL.md` files — a folder with a `SKILL.md` whose YAML frontmatter carries a `name` and `description`, followed by markdown instructions (and optionally bundled `scripts/` and `references/`). They're discovered automatically, and project skills override global ones with the same name:

- `~/.claude/skills/<name>/SKILL.md` — Claude global (source `claude`)
- `~/.agents/skills/<name>/SKILL.md` — shared agents dir (source `agents`)
- `./.claude/skills/<name>/SKILL.md` — this project (source `project`)

```markdown
---
name: pdf-fill
description: fill PDF form fields from a JSON map; use when asked to fill a PDF
---

# PDF fill

Read `references/fields.md` for the field map, then run `scripts/fill.py …`.
```

Discovery uses **progressive disclosure**: only the catalog of names + descriptions is added to the system prompt, and the model pulls in a skill's full body on demand via the built-in `skill` tool — so a large library of skills costs almost no context until one is used. A skill with no `description` is skipped (it can't be advertised usefully). Set `MN_SKILLS=0` to turn discovery off entirely.

## MCP servers

Add `~/.config/moony/mcp.json` or `./.moony/mcp.json` to connect to any [Model Context Protocol](https://modelcontextprotocol.io) server. Their tools appear to the model as `mcp.<server>.<tool>`. Resources are exposed as `mcp.<server>.read_resource`, and any prompt the server advertises auto-registers as a `/mcp.<server>.<prompt>` slash command.

```json
{
  "servers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/dir"]
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "ghp_..." }
    },
    "remote-mcp": {
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer ..." }
    }
  }
}
```

Both the **stdio** transport (`command`+`args`) and **HTTP+SSE** transport (`url`+`headers`) are supported; servers that don't implement resources or prompts are detected automatically and skipped without errors.

Claude's own MCP config is **auto-discovered** too (the `mcpServers` key of `claude_desktop_config.json` and `~/.claude.json`, including its per-project scope), so servers you already configured for Claude work here without copying them. Set `MN_MCP_CLAUDE=0` to skip that, or `MN_MCP=0` to skip connecting to MCP servers altogether (discovery and the listings below still work).

## Managing skills, MCP servers & subagents

Skills, MCP servers, subagent personas, and worker agents can be listed and changed at runtime — from the TUI with `/skills`, `/mcps`, `/agents`, and `/workers`, or from the web UI's **Manage** panel (the gear in the header) — without editing config files or restarting. The Manage panel's **Workers** section toggles each worker and edits its model inline.

- **List** — `/skills`, `/mcps`, or `/agents` with no arguments shows every discovered item with its on/off state. In the terminal it's a selectable picker (arrow keys + enter toggles); in the web UI it's the same clickable picker, and the Manage panel has a section for each.
- **Enable / disable** — `/skills enable|disable <name>`, `/mcps enable|disable <name>`, and `/agents enable|disable <name>` flip an item on or off. Toggling a skill or subagent re-attaches the enabled set to the model immediately; toggling an MCP server takes effect after `/mcps reload`. A disabled subagent disappears from the `task` tool's persona list.
- **Add / scaffold** — `/skills new <name>` scaffolds a `SKILL.md` template (in `./.claude/skills`, or `~/.claude/skills` with `--global`); `/agents new <name>` scaffolds a persona `.md` (in `./.moony/agents`, or `~/.config/moony/agents` with `--global`); `/mcps add <name> -- <cmd> [args]` (stdio) or `/mcps add <name> <url>` (HTTP) writes a server into `mcp.json`.
- **Remove** — `/skills remove <name>` deletes the skill's directory (refused for `~/.agents` skills — disable those instead); `/agents remove <name>` deletes the persona's `.md`; `/mcps remove <name>` drops the server from `mcp.json`.
- **Reload** — `/mcps reload` reconnects the enabled MCP servers in place (detaching the old tools first), so adds/removes/toggles apply live.

**Scope & precedence.** Changes default to **this project** (`./.moony/manage.json`); add `--global` (or `-g`) to target the user-level file (`~/.config/moony/manage.json`). Enable/disable state is tracked separately from the skill/MCP/agent source files (so nothing is edited to turn an item off), and resolves most- to least-specific:

```
project explicitly enabled   -> on   (re-enables a globally-disabled item)
project explicitly disabled  -> off
global disabled              -> off
otherwise                    -> on
```

In the web UI the same actions are available as buttons: each row has an on/off toggle and a trash (remove) control, with "+ New skill", "+ Add" (MCP), and "Reload" at the bottom of each section. The panel operates on the **current session's project**, and MCP changes there reconnect that session's servers.

## LSP servers (diagnostics)

Add `~/.config/moony/lsp.json` or `./.moony/lsp.json` to wire up language servers. After every successful `write_file`/`edit_file`/`multi_edit`/`apply_patch`, diagnostics for the changed file(s) are appended to the tool's result so the model sees errors right away.

```json
{
  "servers": {
    "go":     { "command": "gopls",   "extensions": [".go"] },
    "python": { "command": "pyright-langserver", "args": ["--stdio"], "extensions": [".py"] },
    "ts":     { "command": "typescript-language-server", "args": ["--stdio"], "extensions": [".ts", ".tsx", ".js", ".jsx"] }
  }
}
```

This is a minimal, diagnostics-only LSP client — `textDocument/didOpen`/`didChange` plus `publishDiagnostics`. Hover, go-to-definition, completions, and other LSP features are out of scope.

## Debug Adapter Protocol (interactive debugger)

Point moony at a running [DAP](https://microsoft.github.io/debug-adapter-protocol/) adapter and the agent gains 14 debugger tools: connect, launch/attach, set breakpoints, step (over/in/out), pause, inspect the call stack, list variable scopes, expand variables, and evaluate expressions.

**Quick start (Go with `dlv`):**

```bash
# In one terminal — start the Delve DAP server
dlv dap --listen=:54321

# In another terminal — run moony with the adapter address
MN_DAP_ADDR=localhost:54321 moony
```

| Env var | Default | Purpose |
|---|---|---|
| `MN_DAP_ADDR` | _(unset)_ | Auto-connect to a DAP adapter at startup (`host:port`). If unset, call `dap_connect` from within a session instead. |

**Tools (always registered; error gracefully if not connected):**

| Tool | Description |
|---|---|
| `dap_connect` | Connect to a DAP adapter at `addr`; all other tools become available. |
| `dap_disconnect` | Send `disconnect` and close the adapter connection. |
| `dap_launch` | Launch a debug target with adapter-specific `args` (e.g. `{"program":"./main","stopOnEntry":true}`), wait for the first stop. |
| `dap_attach` | Attach to a running process (e.g. `{"processId":12345}`), same lifecycle as launch. |
| `dap_set_breakpoints` | Set or replace all breakpoints in a file by line number, with optional conditions. |
| `dap_continue` | Resume a stopped thread; blocks until the next breakpoint or timeout. |
| `dap_next` | Step over one statement; returns the new file:line + any output. |
| `dap_step_in` | Step into a function call. |
| `dap_step_out` | Step out of the current function. |
| `dap_pause` | Pause a running thread. |
| `dap_stack_trace` | Return the call stack with frame IDs, function names, and file:line. |
| `dap_scopes` | List variable scopes for a frame (Locals, Globals, …) and their `variablesReference` handles. |
| `dap_variables` | Expand a scope or nested variable reference into a name/type/value table. |
| `dap_evaluate` | Evaluate an expression (`watch`, `repl`, or `hover` context) and return its value. |

Works with any DAP-compliant adapter: `dlv dap` (Go), `debugpy` (Python), `node --inspect` (Node.js), VS Code's extension hosts, and more.

## Code formatters (format-on-write)

Add `~/.config/moony/format.json` or `./.moony/format.json` to run formatters on changed files. After every successful `write_file`/`edit_file`/`multi_edit`/`apply_patch`, each changed file is formatted **in place** — right before LSP diagnostics run, so diagnostics see the formatted code. The file path is appended as the final argument, so each formatter is invoked as `<command> <args...> <file>` and is expected to edit the file in place.

```json
{
  "formatters": {
    "go":     { "command": "gofmt",    "args": ["-w"],      "extensions": [".go"] },
    "web":    { "command": "prettier", "args": ["--write"], "extensions": [".ts", ".tsx", ".js", ".css", ".json"] },
    "python": { "command": "ruff",     "args": ["format"],  "extensions": [".py"] }
  }
}
```

Project entries override global ones with the same name. A formatter that fails (e.g. on a syntax error) appends a short note to the tool result but never blocks the agent. Add `"disabled": true` to keep an entry in the file but skip it.

## Multimodal input (images, PDFs, Office files)

Attach files to a turn so the model can see them. How they're delivered depends on the type:

| Type | Delivery |
|------|----------|
| Images — `.png`, `.jpg/.jpeg`, `.gif`, `.webp` | sent as vision blocks (both routes) |
| PDFs — `.pdf` | sent as document blocks, read natively by Claude on the Anthropic provider; on OpenAI-compatible providers the model is told a PDF was attached but can't view its contents |
| Office — `.docx`, `.xlsx`, `.pptx` | text-extracted locally (no model API ingests these) and sent as text (both routes) |

**Terminal:**
- `@photo.png` — an `@`-mention of an image/PDF/Office file auto-attaches it (text/code files still inline as before).
- `/attach <path>` — explicitly stage a file for the next message.
- `/paste` — stage an image from the macOS clipboard.

Staged attachments show above the input as `Attached: …` and are sent (and cleared) with your next message.

**Browser:**
- the paperclip button (file picker), **drag-and-drop** onto the composer, or **paste** an image from the clipboard.
- staged items appear as thumbnails/chips above the input; sent images render inline in the transcript.

Caps: images ≤ 5 MB, PDFs ≤ 32 MB, Office files ≤ 30 MB (extracted text ≤ 200 KB), up to 20 attachments per message. Attachments are saved with the session, so reopening it re-shows them.

## Worker agents (delegate to Claude Code / Optimus)

`moony` can dispatch a focused task to **another coding agent running out-of-process** and get its answer back — orchestrating other agents as workers. Unlike `task` sub-agents (in-process, same binary), a worker is a separate agent with its own context, model, and tools.

**Auto-detection:** if the `claude` (Claude Code) or `optimus` CLI is on your `PATH`, a default read-only worker for it is added automatically — `delegate` works with no config. `/workers` probes each engine by running `<cli> --version` and shows its state (`ready` / `not ready` / `missing`).

To customize (a named worker, a pinned model, a second worker on the same engine), add entries to `~/.config/moony/workers.json` or `./.moony/workers.json`:

```json
{
  "workers": [
    { "name": "explorer", "backend": "optimus", "description": "deep codebase exploration" },
    { "name": "reviewer", "backend": "claude", "model": "claude-sonnet-4-5", "description": "second-opinion code review" }
  ]
}
```

The model then calls the **`delegate`** tool: `delegate(worker, task)`. Dispatching to the same worker again **continues its session** (multi-turn), and `worker_reset(worker)` starts it fresh. Workers are **read-only by default** (exploration/review/research/second opinions), so there's no working-tree contention.

| Backend | How it's driven |
|---|---|
| `claude` (Claude Code) | one-shot `claude -p … --output-format json`, made stateful via `--resume`; read-only enforced with `--permission-mode plan` |
| `optimus` (Optimus / opencode) | a long-lived `optimus acp` process over the **Agent Client Protocol** (JSON-RPC on stdio); read-only enforced by rejecting edit/execute permission requests |

Per-worker fields: `backend` (`claude`\|`optimus`), `model`, `agent`, `cwd` (default: the project dir), `readOnly` (default `true`), `timeoutSec`, `command` (override the binary), `disabled`. Each CLI authenticates itself with its own credentials — `moony` doesn't pass tokens. A `delegate` is gated by your approval policy like `task`/`bash`. Held-open Optimus worker processes are reaped when `moony` exits.

While a worker runs, its **live progress** (the tools it calls, status notes) streams into the transcript as dim, indented lines under the `delegate` call — in both the terminal and the browser — so you can watch a long dispatch unfold; the worker's full answer then lands as the tool result.

Manage workers at runtime with **`/workers`** (terminal or web), which applies live and persists to `workers.json`:

- `/workers` — opens a clickable **toggle picker** (in the terminal: arrow keys + enter; in the web UI: click a row) listing each worker with its backend, model, and engine state (`ready`/`not ready`/`missing`, probed via `<cli> --version`); selecting a row enables/disables it. Auto-detects any newly-installed engine.
- `/workers enable|disable <name>` — toggle a worker from the command line.
- `/workers model <name> <provider/model>` — **set a worker's model** (omit the model to clear the override), e.g. `/workers model reviewer claude-sonnet-4-6`. Resets the worker's session so the next dispatch uses the new model.
- `/workers reset <name>` — drop a worker's session so it starts fresh.

```bash
./bin/moony --model claude-sonnet-4-5 -C ~/code/myrepo
./bin/moony --continue           # pick up where you left off
./bin/moony --no-approval        # trust mode: no prompts
```

