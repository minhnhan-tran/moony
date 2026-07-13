<p align="center">
  <img src=".github/banner.svg" alt="moony" width="560">
</p>

A terminal coding agent in the spirit of [OpenCode](https://github.com/sst/opencode) / Claude Code, written in Go. It works with a local **[Ollama](https://ollama.com)** server (default, no API key) or a hosted provider — **OpenAI**, **Anthropic (Claude)**, **OpenRouter**, **Google Gemini**, **DeepSeek**, **Groq**, **xAI (Grok)**, **Mistral**, **Together.ai**, **Fireworks.ai**, **Moonshot (Kimi)**, **Z.AI (GLM)**, **DashScope (Qwen)**, **NVIDIA NIM**, **Hugging Face**, or **Venice** — via their OpenAI/Anthropic-compatible APIs. It streams responses, calls tools (read/write/edit files, run shell commands, search the tree), and loops until your task is done — all inside a Bubble Tea TUI.

Highlights:
- **Streaming TUI** with live markdown rendering (token-by-token), tool activity, token & **cost** counters, mouse-wheel scroll, todo panel.
- **Multiple providers** (`/provider`) — **Ollama** (local, default, no key), plus hosted **OpenAI**, **Anthropic (Claude)**, **OpenRouter**, **Google Gemini**, **DeepSeek**, **Groq**, **xAI (Grok)**, **Mistral**, **Together.ai**, **Fireworks.ai**, **Moonshot (Kimi)**, **Z.AI (GLM)**, **DashScope (Qwen)**, **NVIDIA NIM**, **Hugging Face**, and **Venice**. Switch at runtime; the choice is saved.
- **Model picker** (`/models` — live from your Ollama server) and **extended thinking** (`/reasoning`), with your choices saved across launches.
- **Built-in tools**: `read_file`, `write_file`, `edit_file`, `multi_edit`, `apply_patch`, `list_dir`, `glob`, `grep`, `bash` (with `background` jobs via `bash_output`/`bash_kill`), `web_fetch`/`batch_web_fetch`, `web_search`, `test_run`, `recall`, `usage`, `todo_write`, git (`git_status`, `git_diff`, `git_log`, `git_commit`, `git_branch`), and GitHub/GitLab (`pr_create`, `pr_list`, `pr_comments`, `pr_checks`).
- **Plan vs Build modes** — `/plan` disables `write_file`/`edit_file`/`multi_edit`/`apply_patch`/`bash`/`task` for safe exploration; `/build` re-enables.
- **First-class plan artifact** — `/plan <description>` drafts an ordered, persisted, **approvable** plan (via the `plan_write` tool) that you can amend (`/plan add`), approve to execute (`/plan approve`), or discard (`/plan clear`). It's saved with the session and shown in the sidebar.
- **`/init`** — bootstrap an `AGENTS.md` by having the model survey the repo.
- **`/undo`** — revert files the most recent turn wrote or edited (snapshots are recorded automatically).
- **`/compact`** — summarize older history on demand.
- **`/goal`** — set a session stop-condition; the agent keeps working until an independent judge model confirms the goal is truly met (no premature "optimistic stops").
- **`/checkpoint`** — a model-distilled snapshot of the session's state (goal, decisions, files touched, findings, progress, next steps) is kept automatically and re-injected as context, so the agent retains its understanding across compaction and resume without relearning it.
- **Configurable approval policy** (`prompt`/`safe`/`auto` via `/approval`), **persisted permission rules** (`/permissions`, allow/deny), **diff previews**, **session persistence** (`--continue`/`--session`), and **auto-compaction**.
- **Headless mode** — `-p`/`--print "prompt"` (and/or piped stdin) runs a single prompt non-interactively and exits; add `--json` for a machine-readable result. Great for scripts, pipelines, and CI.
- **Multimodal input** — attach **images** (vision), **PDFs** (read natively by Claude), and **Office files** (docx/xlsx/pptx, text-extracted locally). Terminal: `@photo.png`, `/attach <path>`, or `/paste` (macOS clipboard). Browser: the paperclip button, drag-drop, or paste. Works on both the Anthropic and OpenAI routes.
- **Path sandboxing** for file tools, plus an optional **OS-level `bash` sandbox** that confines shell writes to the working directory (`MOONY_SANDBOX`; `sandbox-exec` on macOS, `bwrap` on Linux).
- **`AGENTS.md`** ingestion (global + per-project, with `CLAUDE.md` fallback) + **`@file` mentions**.
- **`.sqeignore`** + `.gitignore` honored by `glob`/`grep`/`list_dir`.
- **Custom slash commands** via `.md` files; **sub-agents** via `task` tool + `.md` personas, which can run **in parallel**.
- **Worker agents** — `delegate` to other coding agents running out-of-process (**Claude Code** and **Optimus**) as workers via `workers.json`. Each worker keeps its own context across dispatches (multi-turn) and is read-only by default — good for exploration, review, and a second opinion.
- **Parallel tool execution** — independent read-only calls (and sub-agents) in a turn run concurrently; mutating file/shell tools stay sequential (`MOONY_PARALLEL_TOOLS`).
- **Repository map in system prompt** (`MOONY_REPO_MAP`) — a compact ignore-aware structural overview is prepended to the prompt so the model immediately understands the repository structure and layout.
- **Verbosity steering** (`MOONY_TERSE`) — an "OUTPUT BUDGET" directive steers the model away from spending pricey output tokens on ceremony (preambles, sign-offs, restated context, re-printed unchanged code), trimming cost/latency without skipping actual work.
- **Smart tool-output truncation** — oversized tool output is clipped on line boundaries (never mid-line or mid-UTF-8-rune), and oversized JSON is shrunk first — losslessly (whitespace) then by dropping empty/null fields — so structured payloads often fit in full instead of being hard-truncated mid-token.
- **Code-aware `read_file` outline** (`MOONY_CODE_OUTLINE`) — when a `read_file` result on a source file is over budget, it's compressed into a signatures/declarations outline (kept with line numbers; bodies collapsed into `… (N lines elided)`) instead of a blunt byte clip, so the model keeps the file's shape and can re-read a specific range on demand.
- **Dynamic effort routing** (`MOONY_DYNAMIC_EFFORT`) — in `auto` reasoning mode, steps that just digest a routine tool batch (edits, a listing, a todo update) run at a lower thinking budget, while new requests and tool errors still get full effort — spending reasoning tokens where they matter.
- **Automatic verification loop** (`/verify`) — configures a build or test command run automatically after file edits; if the command fails, the output is fed back so the agent self-corrects.
- **Optional self-review** (`MOONY_SELF_REVIEW=1`) — runs a cheap, fast-model review of the turn's own diff to find and fix bugs (logic errors, off-by-one, missed errors) before ending a turn.
- **Agent-to-agent messaging** — sub-agents concurrently spawned via `task` can communicate over a shared concurrency-safe message board (`post_message`/`read_messages`) and coordinate during a turn.
- **Hooks** (`pre/post_turn`, `pre/post_tool`).
- **MCP client** (stdio + HTTP/SSE) with **tools**, **resources** (read), and **prompts** (auto-registered as `/mcp.<server>.<name>` slash commands).
- **Agent Skills** — Claude-style `SKILL.md` skills are auto-discovered (`~/.claude/skills`, `~/.agents/skills`, `./.claude/skills`) and offered to the model via progressive disclosure (it loads one on demand with the `skill` tool).
- **Manage skills & MCP servers live** — `/skills` and `/mcps` (and a **Manage** panel in the web UI) list everything with on/off state and let you enable/disable, scaffold/add, remove, and hot-reload MCP servers — per-project or `--global` — without a restart.
- **LSP integration** — diagnostics from configured language servers are appended to `write_file`/`edit_file` results so the model sees errors immediately.
- **Format-on-write** — configured formatters (gofmt, prettier, ruff, …) run on changed files after each successful write, before diagnostics, so changes land already formatted.

```
┌ moony ───────────────── claude-sonnet-4-6 · /path/to/repo ┐
│ You                                                                │
│ add a --json flag to the export command                            │
│ sqe                                                                │
│ I'll update the command and show you the change first.             │
│ ✓ read_file  {"path":"cmd/export.go"}                              │
│ ⚙ edit_file  edit cmd/export.go — 1 match (+6 -1)                  │
│   - func export(w io.Writer) {                                     │
│   + func export(w io.Writer, asJSON bool) {                        │
│ ┌ ⚠ approve edit_file? ─────────────────────────────────────────┐ │
│ │ edit cmd/export.go — 1 match (+6 -1)                           │ │
│ │ y approve   n deny   a always allow this tool                  │ │
│ └────────────────────────────────────────────────────────────────┘ │
└ awaiting approval — y/n/a ─────────────────────────────────── ↑1834 ↓212 ┘
```

## Contents

- [How it works](#how-it-works) · [Built-in tools](#built-in-tools) · [Feature details](#feature-details)
- [Requirements](#requirements) · [Install](#install) · [Build & run](#build--run-from-source) · [Releasing](#releasing-maintainers)
- [Web UI](#web-ui-experimental) · [Relay mode](#relay-mode) · [In-app commands & keys](#in-app-commands--keys)
- [Configuration](#configuration) (env vars + flags) · [Headless mode](#headless-mode) · [Upstream routes (Anthropic vs OpenAI)](#upstream-routes-anthropic-vs-openai)
- [Project instructions: `AGENTS.md`](#project-instructions-agentsmd) · [Memory](#memory) · [Custom slash commands](#custom-slash-commands)
- [Sub-agents](#sub-agents-task-tool) · [Hooks](#hooks) · [Agent Skills](#agent-skills) · [MCP servers](#mcp-servers)
- [Managing skills, MCP servers & subagents](#managing-skills-mcp-servers--subagents) · [LSP servers](#lsp-servers-diagnostics) · [Code formatters](#code-formatters-format-on-write) · [Multimodal input](#multimodal-input-images-pdfs-office-files) · [Worker agents](#worker-agents-delegate-to-claude-code--optimus)
- [Development](#development) · [Tests](#tests) · [Releasing](#releasing)

## How it works

The core is the classic agent loop (`internal/agent`):

```
user message ─► model (stream) ─► tool_use? ─► run tools ─► tool_result ─┐
                   ▲                                                      │
                   └──────────────────── loop until end_turn ◄───────────┘
```

| Package | Responsibility |
|---------|----------------|
| `internal/llm`    | Multi-provider LLM client: OpenAI-compatible + native Anthropic wire formats, SSE streaming, tool-call assembly |
| `internal/tools`  | Tool interface + registry and the built-in tools, with path sandboxing, `Mutates()` classification, and diff `Preview` for writes/edits |
| `internal/agent`  | The agent loop: streams a turn, gates mutating tools behind approval, dispatches tool calls, compacts long history, persists sessions |
| `internal/diff`   | Compact LCS unified diffs (with context collapsing) for change previews |
| `internal/session`| JSON session persistence (save/load/latest/list) |
| `internal/tui`    | Bubble Tea UI: streaming transcript, markdown (Glamour), colored diffs, approval modal, input, spinner, status bar |
| `internal/config` | Runtime configuration, defaults, and saved preferences |
| `internal/skills` · `internal/subagents` · `internal/manage` | Agent Skills + sub-agent personas (discover/scaffold/remove) and their per-project/global enable–disable state |
| `internal/mcp`    | MCP client (stdio + HTTP/SSE): connect servers, register their tools/resources/prompts, hot-reload |
| `internal/lsp` · `internal/hooks` · `internal/doctor` | Diagnostics-only LSP client, lifecycle hook runner, and the `/doctor` environment/config checker |
| `internal/format` · `internal/permissions` | Format-on-write formatter runner and persisted allow/deny permission rules (global + per-project) |
| `internal/attach` | Resolves images/PDFs/Office files into model content blocks (vision, document, or locally-extracted text) for both UIs |
| `internal/worker` | Drives external worker agents (Claude Code via `-p`/JSON; Optimus via the ACP protocol) behind the `delegate` tool |
| `internal/web` · `internal/bridge` | Browser UI — the multi-project app and the live terminal mirror — and the hub that syncs the live session to/from web viewers |
| `internal/relay` · `cmd/relay` | Relay that fronts the web UI; the terminal dials out to it (`MOONY_RELAY`) to reach the agent remotely without opening a local port |
| `internal/app` · `cmd/release` | Shared server builder and the release tool |

### Built-in tools
`read_file`, `write_file`, `edit_file`, `multi_edit`, `apply_patch`, `list_dir`, `glob`, `grep`, `bash`, `bash_output`, `bash_kill`, `web_fetch`, `batch_web_fetch`, `web_search`, `test_run`, `recall`, `usage`, `todo_write`, plus git: `git_status`, `git_diff`, `git_log` (read-only) and `git_commit`, `git_branch`, `git_worktree` (mutating), plus GitHub/GitLab: `pr_list`, `pr_checks` (read-only) and `pr_create`, `pr_comments` (mutating when posting).
The mutating ones (`write_file`, `edit_file`, `multi_edit`, `apply_patch`, `bash`, `bash_kill`, `git_commit`, `git_branch`, `git_worktree`, `pr_create`, `pr_comments`) are gated behind a configurable approval policy (see below); `git_commit`/`pr_create`/`pr_comments` show the pending diff/PR/comment in their approval prompt.

`multi_edit` applies several exact-string replacements to **one** file in a single atomic step — the edits run in order, each against the result of the previous, and either all apply or none do (the file is left untouched on any miss). Prefer it over repeated `edit_file` calls when changing several spots in the same file.

`bash` accepts `background: true` to launch a long-running command (e.g. a dev server) detached, returning a job id immediately instead of blocking. Poll new output and status with `bash_output(job_id)` and stop it with `bash_kill(job_id)`. Background jobs honour the same OS-level sandbox as foreground `bash` and are all terminated when the session exits.

`pr_create`, `pr_list`, `pr_comments`, and `pr_checks` work against **GitHub or GitLab** (including self-hosted), resolved automatically from `git remote get-url origin`. `pr_create` opens a PR/MR from the current branch (title/body/base/draft; `head` defaults to the current branch, `base` to the repo's default branch). `pr_list` lists open PRs/MRs, or views one in detail when given a `number`. `pr_comments` lists review comment threads on a PR/MR, or posts a reply (`thread_id`) or a new general comment (no `thread_id`) when given a `body`. `pr_checks` reports CI check-run/pipeline status for a PR/MR number or a raw ref. Auth resolves in order: `MOONY_GITHUB_TOKEN`/`MOONY_GITLAB_TOKEN` (or the bare `GITHUB_TOKEN`/`GITLAB_TOKEN`), then `gh auth token`/`glab auth token` if that CLI is installed and logged in, then a `~/.netrc` entry for the host. Self-hosted instances: set `MOONY_GITHUB_BASE_URL`/`MOONY_GITLAB_BASE_URL` to the instance's API base (e.g. `https://git.example.com/api/v3` for GitHub Enterprise, `https://gitlab.example.com/api/v4` for self-hosted GitLab).

`git_worktree` gives the agent **isolated git worktrees** so a risky or large task runs on a throwaway branch + directory instead of your live tree. `add` creates a worktree on a new branch (default `sqe/<name>`) under `.sqe/worktrees/<name>` — inside the working dir, so the path sandbox and ignore rules cover it — and the agent does its edits there; `list` shows the worktrees; `merge` merges the worktree's branch into the current branch (with `--no-ff`, refusing a dirty worktree unless `force`) and removes the directory; `remove` discards a worktree (optionally deleting its branch). `.sqe/` is ignored by `glob`/`grep`/`list_dir` so searches don't descend into worktrees. The `merge` approval prompt shows a diffstat of the incoming changes.

`apply_patch` applies one textual patch envelope (`*** Begin Patch` … `*** End Patch`) that can **add, update, and delete several files atomically** — useful for cross-file refactors in a single approved step. Update hunks match by exact context (not line numbers): each hunk's context + removed lines must appear exactly once in the file. The whole patch is validated before anything is written, its combined multi-file diff is shown in the approval prompt, and every touched file is snapshotted so `/undo` reverts the entire patch.

`web_fetch` retrieves a URL over HTTP(S) GET and routes the response by content type: JSON is pretty-printed, HTML is reduced to readable **markdown** (or plain `text`/raw `html` via the `format` option), and plain text / XML / JavaScript are returned as-is; **binary** responses are streamed to a temp file and reported by path instead of flooding the context. Pass `headers` for custom request headers and `max_bytes` to cap the body. `batch_web_fetch` fetches many URLs **concurrently** (bounded parallelism, results in request order, per-item errors inline), each item taking the same options. Both honour the **Domains** allow/deny rules and read `~/.netrc` (or `$NETRC`): when the target host has a `machine` entry, the `password` is sent as `Authorization: Bearer <token>` (so internal endpoints like Artifactory work without the model handling secrets) — a caller-supplied `Authorization` header always wins, the token is only sent to hosts you listed, Go drops it on cross-host redirects, and you can disable the behaviour with `MOONY_NETRC=0`. Neither tool executes JavaScript — use the browser tool for JS-heavy pages.

`web_search` queries a web search engine and returns ranked results (title, URL, snippet) so the agent can **discover** pages when it doesn't already have a URL, then hand a promising link to `web_fetch`. It uses the keyless DuckDuckGo HTML endpoint by default, or the **Brave Search API** when `MOONY_SEARCH_API_KEY` is set, and honours the same Domains allow/deny rules.

`test_run` runs the project's tests and returns a **structured** summary — total/passed/failed/skipped, coverage, and the names + output of failing tests — instead of raw scrollback. It detects the ecosystem by marker file (`go.mod` → `go test -json`, parsed from the test2json stream; `package.json` → `npm test`; `Cargo.toml` → `cargo test`; `pyproject.toml` → `pytest`) and treats a failing suite as a normal result, not a tool error.

`recall` searches your **persisted long-term memory** (the notes `remember` writes to project + global `AGENTS.md`) and returns the entries most **relevant** to a query, ranked with BM25 over identifier-aware tokens (plus an optional hybrid semantic rerank, same as `search_code` — see `MOONY_EMBEDDINGS_MODEL`) — the read-side companion to `remember`.

`usage` reports the session's **token and cost** usage aggregated from per-turn metadata: total input/output/cached tokens, estimated USD cost, call counts, and a per-model (or, with `group_by: "turn"`, per-turn) breakdown.

### Feature details

- **Approval policy** — controls when a mutating tool is gated behind a confirmation prompt. Three levels (`/approval <level>`, persisted; or `--approval`/`MOONY_APPROVAL`):
  - **`prompt`** (default) — confirm every `write_file`/`edit_file`/`bash`/`task`.
  - **`safe`** — additionally auto-run *read-only* bash (e.g. `ls`, `cat`, `grep`, `git status`/`diff`/`log`, `go test`/`build`/`vet`); still prompt for writes, edits, `task`, and any bash with redirection, command substitution, backgrounding, or an unrecognized program.
  - **`auto`** — never prompt (equivalent to `--no-approval`).

  At a prompt you press `y` (approve once), `n` (deny — the model is told and asked to adjust), or `a` (always allow that tool for the session). Read-only tools never prompt. Sub-agents launched via `task` auto-run their own tools (you already approved the delegation, and their prompts can't be surfaced).
- **Permission rules** — persisted allow/deny rules that pre-decide a tool call *before* the approval policy runs. Managed with `/permissions` and stored in `permissions.json` (global `~/.config/moony/permissions.json` + project `./.sqe/permissions.json`). A rule is either a tool name (`write_file`) or a bash command-prefix of the form `bash:<prefix>` (`bash:git `, `bash:rm -rf`); a bash rule matches when the trimmed command starts with `<prefix>`. **Deny always wins over allow.** A deny match blocks the call outright (even under `auto`/`--no-approval`); an allow match runs it without a prompt; no match falls through to the approval policy above.
  - `/permissions` — list active rules.
  - `/permissions allow|deny <rule> [--global]` — add a rule (project scope unless `--global`).
  - `/permissions remove <rule> [--global]` — delete a rule.
- **Diff previews** — `write_file` and `edit_file` implement a `Preview` capability; the agent computes a unified diff of the pending change and shows it both inline in the transcript and inside the approval box (green additions, red deletions).
- **Session persistence** — after every turn the conversation is written to `~/.config/moony/sessions/<id>.json`. Resume the latest with `--continue`, a specific one with `--session <id>`, list them with `/sessions`, and start fresh with `/reset`.
- **Context compaction** — before each turn the agent estimates history size; once it crosses `CompactThresholdTokens` (~140k), it summarizes the oldest messages via the model and replaces them with a compact summary, always cutting at a safe user-message boundary so `tool_use`/`tool_result` pairs stay intact.
- **Resilient requests** — transient model-request failures (network errors, `429`, `5xx`/`529`) are retried with exponential backoff + jitter, honoring `Retry-After`, so a blip doesn't waste a whole turn. Retries cover only the request/initial response; once a stream starts it isn't re-issued (no duplicated output). Client errors (`4xx`) and cancellations are not retried.

## Requirements

- One of: a running **[Ollama](https://ollama.com)** server (default; local `http://localhost:11434` or remote via `MOONY_OLLAMA_HOST`) with a model pulled, **or** an API key for a hosted provider (OpenAI, Anthropic, OpenRouter, Gemini, DeepSeek, Groq, xAI, Mistral, Together.ai, Fireworks.ai, Moonshot, Z.AI, DashScope, NVIDIA NIM, Hugging Face, or Venice).
- **Go 1.26+** (to build from source).
- Network access to your chosen provider.

## Install

The quickest way is to download a prebuilt release onto your `PATH`:

```bash
./install.sh                  # latest release, → /usr/local/bin (or ~/.local/bin)
./install.sh --version 0.16.0 # a specific release
./install.sh --bindir ~/bin   # choose where the binary goes
./install.sh --uninstall      # remove it
```

`install.sh` detects your OS/arch, downloads the matching binary from the public GitHub release, verifies its SHA-256 checksum, and installs it (prompting for `sudo` only if the target dir needs it). Only `curl` is required — no authentication, since releases are public.

## Build & run (from source)

```bash
make build     # compiles ./bin/moony
make run       # build + launch the TUI
```

Then type a request and press **enter**. The agent streams its reply from your configured provider (with Ollama, make sure `ollama serve` is running and you've pulled a model; with a hosted provider, set its API key first — see *Configuration*).

### Releasing (maintainers)

Cut a version, then publish prebuilt binaries that `install.sh` downloads:

```bash
make release VERSION=0.16.0   # bump config.Version + CHANGELOG, commit, create the vX.Y.Z tag
git push origin main --tags   # publish the commit + tag
make publish                  # cross-compile darwin arm64+amd64, upload the GitHub release
```

`make dist` (or `./scripts/publish-release.sh --dry-run`) builds the release tarballs and `SHA256SUMS` into `./dist` without touching GitHub, for a local preview. `make publish` wraps `scripts/publish-release.sh`, which creates/updates the `vX.Y.Z` GitHub release (notes come from the matching `CHANGELOG.md` section) and uploads the `moony_<os>_<arch>.tar.gz` assets plus `SHA256SUMS`. Publishing needs `gh` authenticated to `github.com`.

### Web UI (experimental)

`moony` has a browser UI, served through a **relay**. The relay fronts the web app; the terminal **dials out** to it and serves this machine's projects and sessions over that connection — so a browser pointed at the relay browses the whole session tree *and* drives the live session you're typing in (messages, approvals, and interrupts stay in sync). The agent and the on-disk session store stay on your machine; the relay holds no agent and no session state. It has no built-in auth — front it with your own access control if you expose it (see *Authentication*).

```bash
make relay                               # terminal 1 — the web UI on http://127.0.0.1:8770
MOONY_RELAY=127.0.0.1:8771 moony  # terminal 2 — connect this machine to the relay
# open http://127.0.0.1:8770
```

Save the relay address so every launch auto-connects in the background: run `/relay host:port` in the TUI (persisted to your config); `/relay off` disables it, `/relay` shows the current state. `MOONY_RELAY` overrides the saved value for a single run. While a relay is configured the status bar shows a **relay pill** — green when connected, amber while connecting/retrying — and the same state is mirrored to the web header.

The browser replays each conversation from the start (history is kept), so a viewer can open the page mid-session and catch up, then follow live. The terminal stays the single owner of the agent, so remote actions are delivered into its normal input loop — one conversation, one history, one approval at a time. See *Relay mode* for the multi-laptop tree, and *Authentication* for login + HTTPS.

### Relay mode

A **relay** fronts the web UI while the agent stays on your machine. One or more connectors (your **laptops**) dial **out** to the relay, each announcing its host and serving its local web app over that connection. The relay serves the SPA shell itself, lists the connected laptops at `GET /api/laptops`, and routes `/l/{host}/…` browser requests to that laptop's connector — so the sidebar is a **laptop → project → sessions** tree: pick a laptop to drive its **local** projects, sessions, and files. The relay holds no agent and no session state. This is the building block for hosting the UI elsewhere (e.g. behind an ingress) without moving your code or credentials.

```bash
make relay                               # terminal 1 — web on http://127.0.0.1:8770, connectors dial :8771
MOONY_RELAY=127.0.0.1:8771 moony  # terminal 2 — connect THIS machine to the relay
# open http://127.0.0.1:8770
```

`make relay ADDR=… TUNNEL=…` overrides the relay's browser/connector addresses. The terminal connects with `MOONY_RELAY=host:port` (or a saved `/relay`), serving the full multi-project app over the relay while the TUI keeps running; it auto-reconnects.

**The live terminal session is part of it — and the whole project is drivable.** Over the relay, this terminal's active session appears in its project (flagged *live*); opening it drives the **exact in-terminal conversation** bidirectionally — the hub-backed live mirror — so a remote browser and the terminal stay in sync (messages, approvals, interrupts). Opening **any other session of the terminal's project** switches the terminal to it (the terminal follows the browser, just as the browser follows a terminal-side `/sessions` switch) and then drives it the same way — so the whole project is controllable from the browser, one active session at a time (mirroring the single-session terminal). Switching only happens while the terminal is idle. Sessions in **other projects** open normally (store-backed and independent), since the single-project terminal can't host them. Multiple browser viewers can share the live session.

This is a **local-first prototype**: a plain-TCP connector tunnel. Multiple connectors (laptops) can attach at once and appear in the laptop tree; auth is optional (see *Authentication*) and the browser-facing side can serve HTTPS (see below).

#### Always-on connector (background daemon)

To keep a machine available in the web UI **without hand-launching the TUI**, install the connector as a background OS service. It runs headlessly (no TUI), auto-starts on login, and auto-reconnects — so the machine stays in the laptop tree and its projects/sessions are browsable and drivable from the relay.

```sh
moony daemon install                                      # launchd (macOS) / systemd --user (Linux)
moony daemon status                                       # is it installed + running?
moony daemon stop | start                                 # without removing the unit
moony daemon uninstall                                    # stop + remove the unit
```

It registers under the local OS user that installed it, so the relay's per-user connector namespacing applies. With no `--relay`, it defaults to the built-in relay tunnel; pass `--relay host:port` (or set the saved `/relay` / `MOONY_RELAY`) to point it elsewhere. macOS logs go to `~/Library/Logs/moony/daemon.log`; Linux logs to the user journal (`journalctl --user -u moony`). The daemon serves the full multi-project app (each browser-opened session runs its own agent, store-backed) — it's the connector half without the live in-terminal mirror.

**Auto-install on `curl … | install.sh | sh`.** The installer always installs the daemon — with the relay's advertised tunnel (`-tunnel-public` / `MOONY_RELAY_TUNNEL`) when set, else the built-in default — **and** saves that relay as the interactive default, so a hand-run `moony` connects to the same relay. Since the relay keeps only one connector per machine, the TUI claims it while open (its live in-terminal session is the drivable one) and the **daemon stands down and resumes when you quit** (coordinated via a `connector.lock` with a stale-pid check). To start a session anywhere on a machine, click **New session** under a project in the web sidebar — it opens a folder picker (rooted at that project), so you can start there or browse to any directory.

### Authentication

By default `moony` has **no built-in authentication** — the web UI and relay
serve openly. Bind to loopback for single-user local use, and if you expose the
relay beyond localhost either enable the built-in OIDC login (below) or front it
with your **own** access control (an SSO-terminating reverse proxy, a
firewall/VPN). Exposing it unauthenticated on a routable address hands full
control of the agent (shell + file edits) to anyone who can reach it.

**Built-in OIDC login (opt-in).** Set `MOONY_AUTH=on` on the `moony relay`
process and give it your OIDC provider's details, and the relay gates the web UI
behind a per-user login and turns on real per-user isolation — each user sees
and can reach only their **own** machines. Required env (the relay fails to start
if any is missing — fail closed):

```
MOONY_AUTH=on
MOONY_AUTH_SECRET=<random, ≥16 bytes>          # signs session cookies + connector tokens
MOONY_OIDC_ISSUER=https://accounts.google.com  # your IdP's issuer URL (discovery)
MOONY_OIDC_CLIENT_ID=…
MOONY_OIDC_CLIENT_SECRET=…
MOONY_OIDC_REDIRECT_URL=https://relay.example.com/auth/callback
# optional: MOONY_OIDC_ACCOUNT_CLAIM=email  MOONY_AUTH_TTL=12h  MOONY_AUTH_ADMINS=you@example.com
```

Browsers hitting the relay are redirected to your IdP (Authorization Code +
PKCE); on return a signed, HTTP-only session cookie identifies them. `/healthz`,
`/install.sh`, `/download`, static assets, `/api/relay`, and `/auth/*` stay
public so the installer and version checks keep working.

**Matching the TUI/daemon (connectors).** A connector can't do an interactive
browser login, so after signing in a user visits **`/connect`** on the relay to
get a per-user connector token (HMAC-signed, stateless, expiring). Run the linked
machine with it:

```
export MOONY_RELAY_TOKEN=<token from /connect>
moony --relay relay.example.com:8771      # or set it in the daemon's service env
```

The relay verifies the token and derives the **trusted** owner from it (ignoring
the self-declared OS username), so the browser account and its machines line up
and isolation holds end to end. With `MOONY_AUTH` unset/off, connectors register
with their OS username and every connector is visible (the trusted/local case).

#### Keeping secrets out of the repo (and out of the agent)

Don't `export` secrets (e.g. a provider API key like `MOONY_OPENAI_API_KEY` or `MOONY_OLLAMA_TOKEN`) by hand — they land in shell
history and the process environment. Instead:

- **Local dev — a `.env` file.** Copy `.env.example` to `.env` (git-ignored) and fill it in; it's loaded at startup for any variable that isn't already set. For stronger isolation keep it outside the repo: `export MOONY_ENV_FILE=~/.config/moony/.env`. Load order is `$MOONY_ENV_FILE` → `~/.config/moony/.env` → `./.env`.
- **Prod — a secret store.** Inject the same variables via env or a mounted file. Real environment variables always win over `.env`.

The `bash` and background-job tools **strip these secrets** (every provider API key — `MOONY_OPENAI_API_KEY`, `MOONY_ANTHROPIC_API_KEY`, `MOONY_GEMINI_API_KEY`, etc. and their conventional fallbacks — plus `MOONY_OLLAMA_TOKEN` and `MOONY_ENV_FILE`) from the environment of commands the agent runs, so the model can't read them via `$VAR`/`env`. (A `.env` in the project root is still readable by the agent's shell by path — prefer `MOONY_ENV_FILE` outside the tree for anything truly sensitive.)

#### Serving the relay over HTTPS

TLS is optional. Pass a cert/key with `-tls-cert`/`-tls-key` (or
`MOONY_RELAY_TLS_CERT`/`MOONY_RELAY_TLS_KEY`) and the relay serves HTTPS on its
`-addr`; otherwise it serves plain HTTP. Privileged ports (`<1024`) need `sudo`.

```bash
# Self-signed cert for local testing (use a real cert in production):
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout key.pem -out cert.pem \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost"

make build-relay                 # -> bin/relay
MOONY_RELAY_TLS_CERT=cert.pem MOONY_RELAY_TLS_KEY=key.pem bin/relay -addr localhost:8443
# then connect this machine's terminal:  MOONY_RELAY=127.0.0.1:8771 moony
```

Only the **browser→relay** hop is TLS; the connector tunnel (`:8771`) stays plain TCP on loopback.






### In-app commands & keys
- `/help` — list commands and keys
- `/init` — bootstrap an `AGENTS.md` by having the model survey the repo
- `/attach <path>` — stage an image, PDF, or Office file (docx/xlsx/pptx) for your next message
- `/paste` — stage an image from the macOS clipboard for your next message
- `/plan` — draft a first-class, approvable **plan artifact** (and switch to read-only **plan mode** while doing so). `/plan <description>` has the agent explore read-only and propose ordered steps via the `plan_write` tool; `/plan add <step>` amends the draft, `/plan approve` accepts it and starts executing (mutating tools re-enabled), `/plan clear` discards it, and bare `/plan` shows the current plan + status. The plan is persisted with the session and rendered in the sidebar.
- `/build` — leave plan mode; mutating tools are re-enabled
- `/memory [note]` — show project + global memory, or append a note (`--global` for the user-level file; or start a line with `#`)
- `/doctor` — run an environment & config check (Ollama reachability, MCP/LSP servers, Go env)
- `/model [id]` — show the current model, or set it (saved to config for next launch)
- `/models` — fetch the models installed on the configured Ollama server and pick one (↑/↓ to move, enter to select)
- `/provider [name]` — show or set the **model provider**: `ollama` (default, local, no key), or a hosted one: `openai`, `anthropic`, `openrouter`, `gemini`, `deepseek`, `groq`, `xai`, `mistral`, `together`, `fireworks`, `moonshot`, `zai`, `dashscope`, `nvidia`, `huggingface`, `venice`. With no argument it opens a picker. Switching moves you to that provider's default model and saves the choice; hosted providers need their API key set (see *Configuration*). Pick a specific model with `/models`/`/model`.
- `/reasoning [level]` — set the extended-thinking level; available levels depend on the model (Opus: `low`/`medium`/`high`/`x-high`/`max`; Gemini: `minimal`/`low`/`medium`/`high`; others: `low`/`medium`/`high`). With no argument it opens a picker showing what the current model supports; saved per model and clamped when you switch models
- `/compact` — summarize older history into a single synthetic exchange to free up context
- `/goal [description|clear]` — set a stop-condition the agent must satisfy before it stops. With no argument it shows the current goal. When a goal is set, the agent doesn't end a turn just because the model emitted no tool calls — an independent **judge model** evaluates the conversation against the goal and, if it isn't fully met, the judge's feedback is injected and the agent keeps working (bounded per turn). Persisted with the session, so it survives `--continue`. `/goal clear` removes it
- `/checkpoint [now|clear]` — show, refresh, or clear the session **checkpoint**: a model-distilled snapshot of the session's state (goal, key decisions, files created/modified, findings, what's done vs. in progress, next steps). It is refreshed automatically as the session grows (every ~12 messages; tune with `MOONY_CHECKPOINT_EVERY`, `0` disables) and re-injected into the model's context every turn, so the distilled state survives `/compact` and `--continue` without the agent relearning it. Unlike `/compact`, the checkpoint never discards history — it augments it
- `/clear` — clear the screen
- `/reset` — clear screen and start a **new** session (forgets history)
- `/undo` — revert the files written or edited in the most recent turn (snapshots are recorded automatically)
- `/tokens` — show token usage this session
- `/usage` — show token usage for the session. Ollama is local and has no account, quota, or billing, so there's no remote spend to report.
- `/sessions [id]` — pick a recent session for this directory to switch to (opens a picker; or switch directly by id)
- `/approval [level]` — set the approval policy: `prompt`/`safe`/`auto` (saved)
- `/agent [profile]` — switch the active primary agent profile (e.g. `build`/`plan`; also clickable in the web UI header, at parity with `/agent` and `Tab`)
- `/verify [command\|off]` — configure a build or test command run automatically at the end of a turn that edited files; if the command fails, the output is fed back so the agent self-corrects before stopping (e.g. `/verify go test ./...` or `/verify off`)
- `/permissions [allow\|deny\|remove <rule>] [--global]` — list or edit persisted allow/deny rules (deny wins; rules are tool names like `write_file` or bash prefixes like `bash:git `; `--global` targets the user-level file, default is this project)
- `/sandbox [on\|off]` — macOS: confine `bash` writes to the working directory + build caches (saved)
- `/relay [addr\|off]` — auto-connect to a relay (`host:port`) in the background on every launch so a remote browser can reach this machine's full multi-project app (with this terminal's live session wired in); shows a status pill (saved; `MOONY_RELAY` overrides; applies on next start)
- `/skills [enable\|disable\|new\|remove <name>]` — list discovered skills with `[on]`/`[off]` state, toggle one (re-attached to the model live), scaffold a new `SKILL.md`, or delete one (`--global` targets the user-level setting; defaults to this project)
- `/mcps [enable\|disable\|add\|remove\|reload]` — list MCP servers with `[on]`/`[off]` state, toggle one, add (`/mcps add <name> -- <cmd> [args]` or `<name> <url>`), remove, or `reload` to reconnect the enabled servers without a restart (`--global` for the user-level setting; defaults to this project)
- `/agents [enable\|disable\|new\|remove <name>]` — list subagent personas (used by the `task` tool) with `[on]`/`[off]` state, toggle one (re-attached live), scaffold a new `.md`, or delete one (`--global` targets the user-level setting; defaults to this project)
- `/copy` — copy the last assistant response to the clipboard
- `/exit` — quit
- **enter** send · **esc** interrupt the current turn · **pgup/pgdn** scroll · **ctrl+c** quit
- **↑/↓** browse command history (when the cursor is at the input edge) · **tab** autocomplete a slash command
- **ctrl+b** toggle the left sidebar (current todos + files modified this session + connected MCP servers with their tool counts)
- **ctrl+o** expand/collapse all tool outputs at once; or **click** a tool call's line to toggle just that one. Large outputs/diffs (>8 lines) auto-collapse to a one-line summary so the transcript stays readable.
- In a menu: **↑/↓** (or `j`/`k`) move · **1–9** jump · **enter** select · **esc** cancel
- The input box grows to fit multi-line messages (up to 6 lines)
- Type `/` to pop up a command menu; matching commands are listed above the input
- Type `@path/to/file` in your message to attach the file's contents to the prompt (deduplicated; capped at 32 KB per file)
- Type `@<agent>` or `@<worker>` (a configured subagent persona or worker name) to dispatch the message to it — the model hands it to the `task`/`delegate` tool for you. A token that resolves to a real file stays a file mention; unknown `@tokens` are left as-is
- During an approval prompt: **y** approve · **n** deny · **a** always allow that tool

## Configuration

| Env var | Default | Purpose |
|---------|---------|---------|
| `MOONY_PROVIDER` | `ollama` | backend provider: `ollama` (default), `openai`, `anthropic`, `openrouter`, `gemini`, `deepseek`, `groq`, `xai`, `mistral`, `together`, `fireworks`, `moonshot`, `zai`, `dashscope`, `nvidia`, `huggingface`, or `venice`. Overrides the saved `/provider` choice for the run |
| `MOONY_MODEL` | provider default | model id (e.g. `llama3.1:8b`, `gpt-4o`, `claude-sonnet-4-5`, or a `vendor/model` OpenRouter slug) |
| `MOONY_OLLAMA_HOST` | `http://localhost:11434` | Ollama server base URL (a bare host:port is accepted; `/v1` is appended automatically) |
| `MOONY_OLLAMA_TOKEN` | _(unset)_ | optional bearer token for a remote/protected Ollama server |
| `MOONY_OPENAI_API_KEY` | _(unset)_ | OpenAI API key for the `openai` provider (falls back to `OPENAI_API_KEY`) |
| `MOONY_ANTHROPIC_API_KEY` | _(unset)_ | Anthropic API key for the `anthropic` provider (falls back to `ANTHROPIC_API_KEY`) |
| `MOONY_OPENROUTER_API_KEY` | _(unset)_ | OpenRouter API key for the `openrouter` provider (falls back to `OPENROUTER_API_KEY`) |
| `MOONY_GEMINI_API_KEY` | _(unset)_ | Google Gemini API key for the `gemini` provider (falls back to `GEMINI_API_KEY`) |
| `MOONY_DEEPSEEK_API_KEY` | _(unset)_ | DeepSeek API key for the `deepseek` provider (falls back to `DEEPSEEK_API_KEY`) |
| `MOONY_GROQ_API_KEY` | _(unset)_ | Groq API key for the `groq` provider (falls back to `GROQ_API_KEY`) |
| `MOONY_XAI_API_KEY` | _(unset)_ | xAI API key for the `xai` provider (falls back to `XAI_API_KEY`) |
| `MOONY_MISTRAL_API_KEY` | _(unset)_ | Mistral API key for the `mistral` provider (falls back to `MISTRAL_API_KEY`) |
| `MOONY_TOGETHER_API_KEY` | _(unset)_ | Together.ai API key for the `together` provider (falls back to `TOGETHER_API_KEY`) |
| `MOONY_FIREWORKS_API_KEY` | _(unset)_ | Fireworks.ai API key for the `fireworks` provider (falls back to `FIREWORKS_API_KEY`) |
| `MOONY_MOONSHOT_API_KEY` | _(unset)_ | Moonshot API key for the `moonshot` provider (falls back to `MOONSHOT_API_KEY`) |
| `MOONY_ZAI_API_KEY` | _(unset)_ | Z.AI API key for the `zai` provider (falls back to `ZAI_API_KEY`) |
| `MOONY_DASHSCOPE_API_KEY` | _(unset)_ | DashScope API key for the `dashscope` provider (falls back to `DASHSCOPE_API_KEY`) |
| `MOONY_NVIDIA_API_KEY` | _(unset)_ | NVIDIA NIM API key for the `nvidia` provider (falls back to `NVIDIA_API_KEY`) |
| `MOONY_HUGGINGFACE_API_KEY` | _(unset)_ | Hugging Face API key for the `huggingface` provider (falls back to `HF_TOKEN`) |
| `MOONY_VENICE_API_KEY` | _(unset)_ | Venice API key for the `venice` provider (falls back to `VENICE_API_KEY`) |
| `MOONY_<PROVIDER>_BASE_URL` | provider default | override any provider's API base URL (e.g. `MOONY_OPENAI_BASE_URL`, `MOONY_GEMINI_BASE_URL`) — useful for proxies or self-hosted gateways |
| `MOONY_REASONING` | `off` | extended-thinking level (model-dependent: `off`/`minimal`/`low`/`medium`/`high`/`x-high`/`max`; clamped to what the model supports) |
| `MOONY_MAX_STEPS` | `150` | max model↔tool rounds per turn before the agent stops (raise for long, todo-driven tasks; a runaway-loop guard, not a budget) |
| `MOONY_AUTO_CONTINUE` | `3` | how many times a turn auto-extends past `MOONY_MAX_STEPS` when the model is still working (so long tasks finish without a manual "continue"); `0` disables. Hard ceiling = `MOONY_MAX_STEPS × (1 + MOONY_AUTO_CONTINUE)` |
| `MOONY_APPROVAL` | `prompt` | approval policy: `prompt`/`safe`/`auto` |
| `MOONY_MOUSE` | _(on)_ | mouse-wheel scrolling is on by default; set to `0` to disable it (enables native text selection; scroll with the keyboard) |
| `MOONY_WHEEL_DELTA` | `2` | lines scrolled per mouse-wheel tick; non-numeric or out-of-range values fall back to 2 |
| `MOONY_CTX_BAR` | _(on)_ | show the graphical context-window usage gauge in the status bar; set to `0`/`off` for a text-only readout |
| `MOONY_STREAM_MARKDOWN` | _(on)_ | render the assistant's reply as markdown live while it streams; set to `0` to stream raw text and render markdown only on completion |
| `MOONY_SOUND` | _(on)_ | play a short chime when the agent finishes a turn (macOS); set to `0` to silence it |
| `MOONY_AUTO_TITLE` | _(on)_ | after a new session's first turn, ask the model for a short session title (replacing the first-message heuristic); set to `0`/`off` to keep the heuristic and skip the extra call |
| `MOONY_PROMPT_CACHE` | _(on)_ | Anthropic prompt caching: cache the system prompt, tools, and conversation prefix so repeated agentic steps bill at the much cheaper cache-read rate; set to `0` to disable |
| `MOONY_REPO_MAP` | _(on)_ | when true, prepends a compact, ignore-aware structural overview of the repository (detected stack, key files, a depth-2 directory tree with file counts) to the system prompt; `0`/`off` disables |
| `MOONY_TERSE` | _(on)_ | verbosity steering: add an "OUTPUT BUDGET" directive to the system prompt so the model spends fewer (pricier) output tokens on ceremony — preambles, sign-offs, restated context, re-printed unchanged code — without skipping actual work; `0`/`off` disables |
| `MOONY_CODE_OUTLINE` | _(on)_ | compress oversized `read_file` output on source files into a signatures/declarations outline (line numbers kept, bodies collapsed into `… (N lines elided)`) instead of a blunt byte clip, so the model keeps the file's shape and can re-read a range; `0`/`off` disables |
| `MOONY_DYNAMIC_EFFORT` | _(on)_ | dynamic effort routing (`auto` reasoning only): run steps that just digest a routine/benign tool batch (edits, a listing, a todo update — no errors) at a lower reasoning floor, while new requests and tool errors still get full effort; `0`/`off` disables |
| `MOONY_ADAPTIVE_MODEL` | _(on)_ | run the main turn on the cheaper fast model until it shows sustained friction (tool errors / rejected stops), then escalate to the session model for the rest of the turn — right-sizing the model to the task; `0`/`off` disables. No-op when the fast model equals the session model, or on ultrathink turns |
| `MOONY_CHEAP_READS` | _(on)_ | route pure read-only rounds (no edits/writes yet) to the fast model, restoring the full session model once the turn hits friction; `0`/`off` disables. No-op when the fast model equals the session model |
| `MOONY_FAST_MODEL` | provider default | pin the cheap/fast model used by `MOONY_ADAPTIVE_MODEL`/`MOONY_CHEAP_READS` (and as a fallback for `MOONY_AUX_MODEL`) instead of the provider's own default fast model |
| `MOONY_AUX_MODEL` | _(falls back to `MOONY_FAST_MODEL`)_ | pin the model used for small mechanical sub-tasks (e.g. summarization); checked before `MOONY_FAST_MODEL` |
| `MOONY_EMBEDDINGS_MODEL` | _(unset)_ | enable hybrid semantic reranking in `search_code` and `recall`: the top BM25 candidates are re-scored by embedding cosine similarity to the query and blended with their lexical score. Unset = pure BM25 |
| `MOONY_EMBEDDINGS_PROVIDER` | _(falls back to Ollama)_ | which backend serves `MOONY_EMBEDDINGS_MODEL`, independent of the session's main provider — so a session on e.g. Anthropic can still point embeddings at OpenAI (or any other OpenAI-compatible provider: OpenRouter, Gemini, DeepSeek, Groq, xAI, Mistral, Together, Fireworks, NVIDIA NIM, Hugging Face, Venice, DashScope, Moonshot, Z.AI) without switching its main model. Unset or `ollama` uses Ollama's native embeddings endpoint. Anthropic has no embeddings API and is rejected with a clear error |
| `MOONY_GITHUB_TOKEN` / `GITHUB_TOKEN` | _(falls back to `gh auth token`, then `~/.netrc`)_ | auth for the `pr_*` tools on GitHub repos |
| `MOONY_GITHUB_BASE_URL` | _(unset — github.com)_ | GitHub Enterprise API base, e.g. `https://git.example.com/api/v3`; also identifies which self-hosted host is GitHub for repo detection |
| `MOONY_GITLAB_TOKEN` / `GITLAB_TOKEN` | _(falls back to `glab auth token`, then `~/.netrc`)_ | auth for the `pr_*` tools on GitLab repos |
| `MOONY_GITLAB_BASE_URL` | _(unset — gitlab.com)_ | self-hosted GitLab API base, e.g. `https://gitlab.example.com/api/v4`; also identifies which self-hosted host is GitLab for repo detection |
| `MOONY_AUTO_PLAN` | `0` (off) | auto-enter read-only plan mode for a build-mode turn that looks like a large/multi-step change, drafting a plan for approval before touching files; toggle at runtime with `/plan auto on|off`. `/build` always overrides it for a turn |
| `MOONY_AUTO_DELEGATE` | `0` (off) | auto-delegate a clearly read-heavy investigation ("how/where/why does X work", "investigate …") to the read-only `explore` sub-agent, so bulky reads stay in its (discarded) context instead of re-billing this turn every round. Requires the task tool; skipped in plan mode |
| `MOONY_PARALLEL_TOOLS` | _(on)_ | run independent tool calls in a turn concurrently (read-only tools and sub-agents); set to `0` to force sequential execution, or a number to cap concurrency (default 8) |
| `MOONY_AGENT_MESSAGING` | _(on)_ | enable agent-to-agent message board for concurrent sub-agents; `0`/`off` disables |
| `MOONY_RG` | _(on)_ | use `ripgrep` (`rg`) for the `grep` tool when it's installed (faster on large trees); set to `0`/`off` to always use the built-in Go scanner. Output is identical either way |
| `MOONY_SKILLS` | _(on)_ | discover Agent Skills (`SKILL.md`); set to `0`/`off` to turn skill discovery off entirely |
| `MOONY_MCP` | _(on)_ | connect to configured MCP servers; set to `0`/`off` to skip connecting (config discovery and the `/mcps` listing still work) |
| `MOONY_MCP_CLAUDE` | _(on)_ | auto-discover MCP servers from Claude's config (`claude_desktop_config.json`, `~/.claude.json`); set to `0`/`off` to skip |
| `MOONY_RELAY` | _(unset)_ | `host:port` of a relay (see `cmd/relay`) to dial out to; the terminal serves the full multi-project app over it (so a remote browser can browse and run all this machine's sessions), with this terminal's **live session wired in through the hub** — opening it drives the exact in-terminal conversation. Best-effort + auto-reconnecting, and the TUI runs normally whether or not it connects. Overrides the saved `/relay` address for the run |
| `MOONY_SANDBOX` | _(off)_ | confine `bash` writes to the working directory (plus temp and common build caches) via `sandbox-exec` (macOS) or `bwrap` (Linux). Set `on`/`1` to enable. Reads, process execution, and network are unaffected. No-op on other platforms or if the confinement tool isn't installed. |
| `MOONY_SANDBOX_NET` | _(on)_ | when the sandbox is on, set `0`/`off` to also deny network access to sandboxed commands |
| `MOONY_NETRC` | _(on)_ | `web_fetch`/`batch_web_fetch` read `~/.netrc` (or `$NETRC`) and send the matching host's `password` as `Authorization: Bearer <token>`; set `0`/`off` to disable. A caller-supplied `Authorization` header always wins |
| `MOONY_VERIFY_CMD` | _(unset)_ | shell command (e.g. `go test ./...`) run automatically on file edits to feed failures back for self-correction |
| `MOONY_VERIFY_MAX` | `2` | max verify retries per turn before stopping for review |
| `MOONY_VERIFY_TIMEOUT` | `120` | timeout in seconds for a single verify command run |
| `MOONY_SELF_REVIEW` | `0` (off) | set to `1`/`on`/`true` to run a cheap fast-model self-review on changed diffs to detect and fix bugs before ending a turn |
| `MOONY_ENV_FILE` | _(unset)_ | path to a `.env` file loaded at startup for any **unset** variables (real env always wins). Defaults to `~/.config/moony/.env` then `./.env` when unset. Local-dev convenience — never use in prod |
| `MOONY_RELAY_TLS_CERT` | _(unset)_ | PEM certificate file so the relay serves **HTTPS** (also `-tls-cert`). Pair with the key below |
| `MOONY_RELAY_TLS_KEY` | _(unset)_ | PEM private-key file for the relay's HTTPS cert (also `-tls-key`) |
| `MOONY_DEBUG_FILE` | _(unset)_ | debugging: on the OpenAI route, append the request body and every raw SSE chunk to this file (useful for inspecting how the proxy streams) |

Flags:

| Flag | Purpose |
|------|---------|
| `--model <id>` | override the model |
| `--reasoning <level>` | extended-thinking level (model-dependent; clamped to what the model supports) |
| `--approval <level>` | approval policy: `prompt`/`safe`/`auto` |
| `--sandbox <on\|off>` | macOS/Linux: confine `bash` writes to the working dir (also `/sandbox`, `MOONY_SANDBOX`) |
| `-C <dir>` | working directory for the agent |
| `--continue` | resume the most recent session for this directory |
| `--session <id>` | resume a specific session by id |
| `--no-approval` | auto-run mutating tools without confirmation prompts (alias for `--approval auto`) |
| `-p`, `--print <prompt>` | headless mode: run a single prompt non-interactively and exit (also reads piped stdin) |
| `--json` | headless mode: emit one JSON result object (`{ok, response, sessionId, error?}`) instead of streamed text |
| `--version` | print version and exit |

### Headless mode

Run a single prompt without the TUI — useful for scripts, pipelines, and CI:

```bash
# Prompt from a flag
moony -p "summarize what this repo does"

# Prompt from stdin (piped or redirected)
git diff | moony -p "review this diff and flag risky changes"
moony -p < prompt.txt

# Machine-readable result
moony --json -p "list the top-level packages" | jq -r .response
```

Behavior:

- The prompt is assembled from the `-p`/`--print` value, any trailing positional args, and piped stdin (joined with blank lines).
- Assistant text streams to **stdout**; tool/diagnostic activity goes to **stderr**, so `stdout` stays clean for piping.
- With `--json`, stdout is a single object: `{ "ok": bool, "response": string, "sessionId": string, "error"?: string }`.
- Tool calls are **auto-approved** (there's no interactive user), but **deny** permission rules still block — they're enforced before the approval policy. Use `/permissions deny …` (or a project `permissions.json`) to harden a headless/CI run.
- Exit code: `0` on success, `1` on an agent error, `2` for an empty prompt.



The agent supports seventeen backends, selected with `/provider` (or `MOONY_PROVIDER`):

- **Ollama** (default) — a local or remote **[Ollama](https://ollama.com)** server, no API key. Set `MOONY_OLLAMA_HOST` (`http://localhost:11434` by default; a bare `host:port` gets `/v1` appended) and optionally `MOONY_OLLAMA_TOKEN`. Runs locally, so there's no account, quota, or billing — the in-app cost counter reads `$0`.
- **OpenAI** — `api.openai.com`, `MOONY_OPENAI_API_KEY` (or `OPENAI_API_KEY`). Reasoning-model families (o1/o3/o4/gpt-5) get `reasoning_effort` from `/reasoning`.
- **Anthropic** — the native Claude Messages API at `api.anthropic.com`, `MOONY_ANTHROPIC_API_KEY` (or `ANTHROPIC_API_KEY`). `/reasoning` maps to Anthropic's extended-thinking token budget, and prompt caching is on by default (`MOONY_PROMPT_CACHE=0` to disable).
- **OpenRouter** — `openrouter.ai`, `MOONY_OPENROUTER_API_KEY` (or `OPENROUTER_API_KEY`); one key reaches hundreds of models via `vendor/model` slugs (e.g. `anthropic/claude-sonnet-4.5`).
- **Google Gemini** — Google's OpenAI-compatible endpoint, `MOONY_GEMINI_API_KEY` (or `GEMINI_API_KEY`).
- **DeepSeek** — `api.deepseek.com`, `MOONY_DEEPSEEK_API_KEY`; `/reasoning` engages on `deepseek-reasoner`.
- **Groq** — `api.groq.com`, `MOONY_GROQ_API_KEY`; very fast inference for open models.
- **xAI (Grok)** — `api.x.ai`, `MOONY_XAI_API_KEY`.
- **Mistral** — `api.mistral.ai`, `MOONY_MISTRAL_API_KEY`.
- **Together.ai** — `api.together.xyz`, `MOONY_TOGETHER_API_KEY`; hosted open models.
- **Fireworks.ai** — `api.fireworks.ai`, `MOONY_FIREWORKS_API_KEY`; hosted open models.
- **Moonshot (Kimi)** — `api.moonshot.ai`, `MOONY_MOONSHOT_API_KEY` (or `MOONSHOT_API_KEY`); Kimi K2 models.
- **Z.AI (GLM)** — `api.z.ai`, `MOONY_ZAI_API_KEY` (or `ZAI_API_KEY`); Zhipu GLM models (e.g. `glm-4.6`).
- **DashScope (Qwen)** — `dashscope-intl.aliyuncs.com`, `MOONY_DASHSCOPE_API_KEY` (or `DASHSCOPE_API_KEY`); Alibaba Qwen via OpenAI-compatible mode.
- **NVIDIA NIM** — `integrate.api.nvidia.com`, `MOONY_NVIDIA_API_KEY` (or `NVIDIA_API_KEY`); hosted open models.
- **Hugging Face** — `router.huggingface.co`, `MOONY_HUGGINGFACE_API_KEY` (or `HF_TOKEN`); inference router across many open models.
- **Venice** — `api.venice.ai`, `MOONY_VENICE_API_KEY` (or `VENICE_API_KEY`); privacy-focused open models.

Every provider except Anthropic speaks the **OpenAI-compatible Chat Completions** wire format; Anthropic uses its **native Messages API**. Either way the agent translates its conversation and tools to/from the provider's format automatically. Each hosted provider's base URL can be overridden with `MOONY_<PROVIDER>_BASE_URL` (e.g. for a proxy or self-hosted gateway). Switching provider moves you to that provider's default model; `/models` lists what's available and `/model <id>` (or `MOONY_MODEL`) selects one.

Sessions are stored under `~/.config/moony/sessions/`. Your model and reasoning-level choices (set via `/model`, `/models`, or `/reasoning`) persist to `~/.config/moony/config.json` and are restored on the next launch. Precedence is **flags > environment variables > saved config > built-in defaults**. Approval and compaction thresholds (`ApprovalEnabled`, `CompactThresholdTokens`, `CompactKeepTokens`) live in `internal/config`.

### Project instructions: `AGENTS.md`

When the agent starts, it assembles instruction files into the system prompt, least- to most-specific (so the closest file's guidance wins):

1. a **global** file at `~/.config/moony/AGENTS.md` (honoring `$XDG_CONFIG_HOME`; this same `~/.config/moony` location is used on macOS and Linux) for personal, cross-project rules;
2. every project `AGENTS.md` from the repo root down to the working directory — the walk stops at the repo root (a `.git` dir) or `$HOME`, whichever comes first.

`CLAUDE.md` is accepted as a fallback wherever `AGENTS.md` is looked for, so existing repos work without renaming (`AGENTS.md` takes precedence if both are present in a directory). Use these for project conventions, build commands, "always do X / never touch Y" rules, and so on. Each file is capped at 16 KB.

### Memory

Save durable notes that persist across sessions, à la Claude Code's `/memory`:

- **`/memory`** — show the notes currently saved in the project and global files.
- **`/memory <note>`** — append a note; **`/memory --global <note>`** targets the user-level file.
- **`# <note>`** — a leading `#` on the input line is a quick-save shortcut (it saves instead of messaging the model).
- The model can also save facts itself via a non-interactive **`remember`** tool.

Notes are appended under a `## Memory` section of the relevant `AGENTS.md` (so they're just part of the instruction files above), and a freshly saved note is reloaded into the system prompt immediately — no restart needed.

### Custom slash commands

Drop `.md` files into either:

- `~/.config/moony/commands/<name>.md` (global)
- `./.sqe/commands/<name>.md` (per-project; overrides global)

Each file's body becomes a templated user message; `$ARGUMENTS` is replaced with whatever you typed after the command name. Optional frontmatter sets a one-line description shown in the menu:

```markdown
---
description: review my staged diff and suggest improvements
---

Please review the following staged diff and suggest improvements:

$ARGUMENTS
```

Run it as `/review` (or `/review with focus on tests`).

### Sub-agents (`task` tool)

Define specialized personas in `.md` files at `~/.config/moony/agents/<name>.md` or `./.sqe/agents/<name>.md`. The model can delegate self-contained subtasks to them via a built-in `task` tool, getting back just the persona's final answer (so the parent's history stays focused).

```markdown
---
description: read-only explorer that surveys a codebase
tools: read_file, list_dir, glob, grep
---

You are a code explorer. Survey the repo to answer the user's question briefly.
Never write or run anything; you have read-only tools.
```

The optional `tools:` allowlist restricts which built-in tools the persona can use (omit for full access).

When the model dispatches several `task` calls in one turn, they run concurrently (you approve each up front, then they execute in parallel). Set `MOONY_PARALLEL_TOOLS=0` to run them one at a time.

**Invoke a subagent directly with `@<name>`.** Typing `@explore find the auth flow` in a message — where `explore` is an enabled persona — appends a directive telling the model to carry out the request via the `task` tool with that agent. The same works for [worker agents](#worker-agents-delegate-to-claude-code--optimus) (`@claude review this`), which route to the `delegate` tool instead. You still approve the dispatch like any other tool call, and the model summarizes what the agent returned. A `@token` that resolves to a real file stays a file mention, and `@tokens` that don't match any agent (or look like paths) are left untouched.

### Hooks

Run user scripts at lifecycle points by adding either `~/.config/moony/hooks.json` or `./.sqe/hooks.json`:

```json
{
  "pre_turn":  ["echo started >> /tmp/sqe.log"],
  "post_turn": ["my-notifier 'sqe finished'"],
  "pre_tool":  ["./scripts/audit-tool.sh"],
  "post_tool": ["./scripts/log-tool.sh"]
}
```

Hooks run via `sh -c` with these env vars: `MOONY_HOOK`, `MOONY_WORKING_DIR`, plus `MOONY_TOOL_NAME` / `MOONY_TOOL_INPUT` (pre_tool) / `MOONY_TOOL_OUTPUT` / `MOONY_TOOL_ERROR` (post_tool). 10-second timeout; failures are logged to stderr but never block the agent.

### Agent Skills

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

Discovery uses **progressive disclosure**: only the catalog of names + descriptions is added to the system prompt, and the model pulls in a skill's full body on demand via the built-in `skill` tool — so a large library of skills costs almost no context until one is used. A skill with no `description` is skipped (it can't be advertised usefully). Set `MOONY_SKILLS=0` to turn discovery off entirely.

### MCP servers

Add `~/.config/moony/mcp.json` or `./.sqe/mcp.json` to connect to any [Model Context Protocol](https://modelcontextprotocol.io) server. Their tools appear to the model as `mcp.<server>.<tool>`. Resources are exposed as `mcp.<server>.read_resource`, and any prompt the server advertises auto-registers as a `/mcp.<server>.<prompt>` slash command.

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

Claude's own MCP config is **auto-discovered** too (the `mcpServers` key of `claude_desktop_config.json` and `~/.claude.json`, including its per-project scope), so servers you already configured for Claude work here without copying them. Set `MOONY_MCP_CLAUDE=0` to skip that, or `MOONY_MCP=0` to skip connecting to MCP servers altogether (discovery and the listings below still work).

### Managing skills, MCP servers & subagents

Skills, MCP servers, subagent personas, and worker agents can be listed and changed at runtime — from the TUI with `/skills`, `/mcps`, `/agents`, and `/workers`, or from the web UI's **Manage** panel (the gear in the header) — without editing config files or restarting. The Manage panel's **Workers** section toggles each worker and edits its model inline.

- **List** — `/skills`, `/mcps`, or `/agents` with no arguments shows every discovered item with its on/off state. In the terminal it's a selectable picker (arrow keys + enter toggles); in the web UI it's the same clickable picker, and the Manage panel has a section for each.
- **Enable / disable** — `/skills enable|disable <name>`, `/mcps enable|disable <name>`, and `/agents enable|disable <name>` flip an item on or off. Toggling a skill or subagent re-attaches the enabled set to the model immediately; toggling an MCP server takes effect after `/mcps reload`. A disabled subagent disappears from the `task` tool's persona list.
- **Add / scaffold** — `/skills new <name>` scaffolds a `SKILL.md` template (in `./.claude/skills`, or `~/.claude/skills` with `--global`); `/agents new <name>` scaffolds a persona `.md` (in `./.sqe/agents`, or `~/.config/moony/agents` with `--global`); `/mcps add <name> -- <cmd> [args]` (stdio) or `/mcps add <name> <url>` (HTTP) writes a server into `mcp.json`.
- **Remove** — `/skills remove <name>` deletes the skill's directory (refused for `~/.agents` skills — disable those instead); `/agents remove <name>` deletes the persona's `.md`; `/mcps remove <name>` drops the server from `mcp.json`.
- **Reload** — `/mcps reload` reconnects the enabled MCP servers in place (detaching the old tools first), so adds/removes/toggles apply live.

**Scope & precedence.** Changes default to **this project** (`./.sqe/manage.json`); add `--global` (or `-g`) to target the user-level file (`~/.config/moony/manage.json`). Enable/disable state is tracked separately from the skill/MCP/agent source files (so nothing is edited to turn an item off), and resolves most- to least-specific:

```
project explicitly enabled   -> on   (re-enables a globally-disabled item)
project explicitly disabled  -> off
global disabled              -> off
otherwise                    -> on
```

In the web UI the same actions are available as buttons: each row has an on/off toggle and a trash (remove) control, with "+ New skill", "+ Add" (MCP), and "Reload" at the bottom of each section. The panel operates on the **current session's project**, and MCP changes there reconnect that session's servers.

### LSP servers (diagnostics)

Add `~/.config/moony/lsp.json` or `./.sqe/lsp.json` to wire up language servers. After every successful `write_file`/`edit_file`/`multi_edit`/`apply_patch`, diagnostics for the changed file(s) are appended to the tool's result so the model sees errors right away.

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

### Code formatters (format-on-write)

Add `~/.config/moony/format.json` or `./.sqe/format.json` to run formatters on changed files. After every successful `write_file`/`edit_file`/`multi_edit`/`apply_patch`, each changed file is formatted **in place** — right before LSP diagnostics run, so diagnostics see the formatted code. The file path is appended as the final argument, so each formatter is invoked as `<command> <args...> <file>` and is expected to edit the file in place.

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

### Multimodal input (images, PDFs, Office files)

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

### Worker agents (delegate to Claude Code / Optimus)

`moony` can dispatch a focused task to **another coding agent running out-of-process** and get its answer back — orchestrating other agents as workers. Unlike `task` sub-agents (in-process, same binary), a worker is a separate agent with its own context, model, and tools.

**Auto-detection:** if the `claude` (Claude Code) or `optimus` CLI is on your `PATH`, a default read-only worker for it is added automatically — `delegate` works with no config. `/workers` probes each engine by running `<cli> --version` and shows its state (`ready` / `not ready` / `missing`).

To customize (a named worker, a pinned model, a second worker on the same engine), add entries to `~/.config/moony/workers.json` or `./.sqe/workers.json`:

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

## Development

Standard Go tooling works — from a clean checkout:

```bash
go build ./...     # build everything
go test ./...      # run the unit tests
go vet ./...       # static checks
go run . -C .      # run the TUI against the current directory
```

The repo also ships a `Makefile` with convenience targets (`make build`,
`make build-relay`, `make test`, `make vet`, `make fmt`, `make tidy`, …). Note
that the committed Makefile pins a specific module proxy and unsets `GOROOT` for
the sandboxed environment moony is developed in; if you're on a normal machine
with regular internet access, plain `go build`/`go test` (above) is simpler and
works out of the box. There are no non-public module dependencies.

## Tests

```bash
make test    # unit tests: diff, session round-trip, compaction boundary logic
make smoke   # live end-to-end tests against your Ollama server (needs `ollama serve` + a pulled model)
make eval    # run the live agent regression/eval harness on task fixtures
```

Unit tests cover the pure logic (diff generation, session persistence, safe compaction boundaries). The live tests in `internal/agent` are skipped unless `MOONY_SMOKE=1`:
- `TestAgentSmoke` — drives a real turn that calls `list_dir` and verifies a response.
- `TestApprovalSmoke` — asks the model to create a file, asserts an approval request with a non-empty diff is raised, approves it, and verifies the file is written.

The regression and evaluation harness is run with `make eval`. It drives the live agent through seeded fixture tasks defined in `internal/eval` (each specifying a repository context, prompt, and expected check command), and outputs a scorecard analyzing success-rate, API costs, and turn/model steps.

## Releasing

`make release` cuts a new version in one step: it runs the tests, bumps `config.Version`, prepends a `CHANGELOG.md` section built from the git commit subjects since the last tag (grouped into Added/Changed/Fixed by leading verb), then commits those two files and creates a `vX.Y.Z` tag. It never pushes — do that yourself once you've reviewed.

```bash
make release-dry            # preview the bump + generated CHANGELOG entry
make release                # bump the minor version (default), commit + tag
make release BUMP=patch     # bump the patch version instead
make release VERSION=1.0.0  # set an explicit version
git push origin main --tags # publish when ready
```

The generated CHANGELOG entry is a starting point — edit it before pushing if you want to refine the wording. The top entry always matches `config.Version`, which the `TestChangelogMatchesVersion` check enforces.

## License

No license file is included yet. Until a `LICENSE` is added, all rights are
reserved — open an issue if you need clarification on usage.
