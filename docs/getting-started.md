# Getting Started & Architecture

[← Back to README](../README.md)

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
| `internal/rag`    | Local per-workspace embedding vector index (single gob file, brute-force cosine) behind `semantic_search` |
| `internal/quickactions` · `internal/taskboard` | Web preset prompts (project + global scope) and the parallel isolated task runner (one throwaway git worktree per task) |
| `internal/format` · `internal/permissions` | Format-on-write formatter runner and persisted allow/deny permission rules (global + per-project) |
| `internal/attach` | Resolves images/PDFs/Office files into model content blocks (vision, document, or locally-extracted text) for both UIs |
| `internal/worker` | Drives external worker agents (Claude Code via `-p`/JSON; Optimus via the ACP protocol) behind the `delegate` tool |
| `internal/web` · `internal/bridge` | Browser UI — the multi-project app and the live terminal mirror — and the hub that syncs the live session to/from web viewers |
| `internal/relay` · `cmd/relay` | Relay that fronts the web UI; the terminal dials out to it (`MN_RELAY`) to reach the agent remotely without opening a local port |
| `internal/app` · `cmd/release` | Shared server builder and the release tool |

### Built-in tools
**Files & shell** — `read_file`, `write_file`, `edit_file`, `multi_edit`, `apply_patch`, `list_dir`, `glob`, `grep`, `bash`, `bash_output`, `bash_kill`.

**Code discovery** — `search_code`, `semantic_search`, `find_symbol`, `find_references`, `read_symbol`, `find_files`, `codebase_map`, `code_nav`, plus the diagnostics-only `lsp_definition`, `lsp_references`, `lsp_hover`, `lsp_diagnostics`.

**Web** — `web_fetch`, `batch_web_fetch`, `web_search`, `browser`.

**Project & VCS** — `test_run`, `lint`, `diff`, `review_changes`, git: `git_status`, `git_diff`, `git_log` (read-only) and `git_commit`, `git_branch`, `git_worktree` (mutating); GitHub/GitLab: `pr_list`, `pr_checks` (read-only), `pr_create`, `pr_comments` (mutating when posting), plus the `gh`-CLI-based `pull_request` and `ci_status`.

**Memory, planning & delegation** — `remember`, `remember_user`, `recall`, `usage`, `todo_write`, `plan_write`, `exit_plan`, `ask_user`, `task`, `delegate`.

**Media & devices** — `generate_image`, `generate_video` (only registered when a media model is configured), `mobile`.

**Debugging** — `dap_connect`, `dap_launch`, `dap_attach`, `dap_set_breakpoints`, `dap_continue`, `dap_next`, `dap_step_in`, `dap_step_out`, `dap_pause`, `dap_stack_trace`, `dap_scopes`, `dap_variables`, `dap_evaluate`, `dap_disconnect`.

The mutating ones (`write_file`, `edit_file`, `multi_edit`, `apply_patch`, `bash`, `bash_kill`, `git_commit`, `git_branch`, `git_worktree`, `pr_create`, `pr_comments`, `pull_request`, `mobile`, `generate_image`, `generate_video`) are gated behind a configurable approval policy (see below); `git_commit`/`pr_create`/`pr_comments` show the pending diff/PR/comment in their approval prompt.

`multi_edit` applies several exact-string replacements to **one** file in a single atomic step — the edits run in order, each against the result of the previous, and either all apply or none do (the file is left untouched on any miss). Prefer it over repeated `edit_file` calls when changing several spots in the same file.

`bash` accepts `background: true` to launch a long-running command (e.g. a dev server) detached, returning a job id immediately instead of blocking. Poll new output and status with `bash_output(job_id)` and stop it with `bash_kill(job_id)`. Background jobs honour the same OS-level sandbox as foreground `bash` and are all terminated when the session exits.

Add `tty: true` to run a background job under a pseudo-terminal. Use it for the two cases pipes handle badly: programs that block-buffer their output when stdout isn't a terminal (a Django/Flask dev server, most Python scripts), and programs that ask a question. The output is line-buffered and stripped of escape codes before it reaches the log, and the job can be typed into from the web UI's jobs panel (Enter sends a line, `^C` interrupts).

In the web UI the jobs panel is reachable from the button on the left of the pager pill, bottom-right of the transcript; its badge counts jobs still running.

`pr_create`, `pr_list`, `pr_comments`, and `pr_checks` work against **GitHub or GitLab** (including self-hosted), resolved automatically from `git remote get-url origin`. `pr_create` opens a PR/MR from the current branch (title/body/base/draft; `head` defaults to the current branch, `base` to the repo's default branch). `pr_list` lists open PRs/MRs, or views one in detail when given a `number`. `pr_comments` lists review comment threads on a PR/MR, or posts a reply (`thread_id`) or a new general comment (no `thread_id`) when given a `body`. `pr_checks` reports CI check-run/pipeline status for a PR/MR number or a raw ref. Auth resolves in order: `MN_GITHUB_TOKEN`/`MN_GITLAB_TOKEN` (or the bare `GITHUB_TOKEN`/`GITLAB_TOKEN`), then `gh auth token`/`glab auth token` if that CLI is installed and logged in, then a `~/.netrc` entry for the host. Self-hosted instances: set `MN_GITHUB_BASE_URL`/`MN_GITLAB_BASE_URL` to the instance's API base (e.g. `https://git.example.com/api/v3` for GitHub Enterprise, `https://gitlab.example.com/api/v4` for self-hosted GitLab).

`git_worktree` gives the agent **isolated git worktrees** so a risky or large task runs on a throwaway branch + directory instead of your live tree. `add` creates a worktree on a new branch (default `sqe/<name>`) under `.sqe/worktrees/<name>` — inside the working dir, so the path sandbox and ignore rules cover it — and the agent does its edits there; `list` shows the worktrees; `merge` merges the worktree's branch into the current branch (with `--no-ff`, refusing a dirty worktree unless `force`) and removes the directory; `remove` discards a worktree (optionally deleting its branch). `.sqe/` is ignored by `glob`/`grep`/`list_dir` so searches don't descend into worktrees. The `merge` approval prompt shows a diffstat of the incoming changes.

`apply_patch` applies one textual patch envelope (`*** Begin Patch` … `*** End Patch`) that can **add, update, and delete several files atomically** — useful for cross-file refactors in a single approved step. Update hunks match by exact context (not line numbers): each hunk's context + removed lines must appear exactly once in the file. The whole patch is validated before anything is written, its combined multi-file diff is shown in the approval prompt, and every touched file is snapshotted so `/undo` reverts the entire patch.

`web_fetch` retrieves a URL over HTTP(S) GET and routes the response by content type: JSON is pretty-printed, HTML is reduced to readable **markdown** (or plain `text`/raw `html` via the `format` option), and plain text / XML / JavaScript are returned as-is; **binary** responses are streamed to a temp file and reported by path instead of flooding the context. Pass `headers` for custom request headers and `max_bytes` to cap the body. `batch_web_fetch` fetches many URLs **concurrently** (bounded parallelism, results in request order, per-item errors inline), each item taking the same options. Both honour the **Domains** allow/deny rules and read `~/.netrc` (or `$NETRC`): when the target host has a `machine` entry, the `password` is sent as `Authorization: Bearer <token>` (so internal endpoints like Artifactory work without the model handling secrets) — a caller-supplied `Authorization` header always wins, the token is only sent to hosts you listed, Go drops it on cross-host redirects, and you can disable the behaviour with `MN_NETRC=0`. Neither tool executes JavaScript — use the browser tool for JS-heavy pages.

`web_search` queries a web search engine and returns ranked results (title, URL, snippet) so the agent can **discover** pages when it doesn't already have a URL, then hand a promising link to `web_fetch`. It uses the keyless DuckDuckGo HTML endpoint by default, or the **Brave Search API** when `MN_SEARCH_API_KEY` is set, and honours the same Domains allow/deny rules.

`test_run` runs the project's tests and returns a **structured** summary — total/passed/failed/skipped, coverage, and the names + output of failing tests — instead of raw scrollback. It detects the ecosystem by marker file (`go.mod` → `go test -json`, parsed from the test2json stream; `package.json` → `npm test`; `Cargo.toml` → `cargo test`; `pyproject.toml` → `pytest`) and treats a failing suite as a normal result, not a tool error.

`semantic_search` finds code by **meaning** rather than by matching text: it embeds the workspace into a local vector index and returns the chunks closest to a natural-language query, so "where do we handle API rate limiting?" finds a throttle/backoff implementation that never uses the word "rate". The index is a single gob file inside the project (`.sqe/rag/index.gob`, or `MN_RAG_INDEX_DIR`), built on first use and refreshed incrementally as files change — brute-force cosine over the stored vectors, so there's no external service or cgo dependency. Needs `MN_EMBEDDINGS_MODEL`; without it the tool isn't registered. Use `search_code`/`grep` for exact strings and this for concepts.

`codebase_map` maintains a **`CODEBASE_MAP.md`** at the project root and injects it into the system prompt, so the model reads a map of the project instead of re-exploring the tree every turn. It has three sections: an auto-generated **structure** digest (directories, key definitions, and how many other files reference each one — built from a symbol/caller graph and refreshed whenever the code changes), a **narrative** the agent writes on demand mapping features → files, and **pinned notes** you own outright (architecture decisions, gotchas, invariants) that nothing regenerates. Enabled when a `CODEBASE_MAP.md` exists or `MN_CODEBASE_MAP=1`; `MN_CODEBASE_MAP_BYTES` bounds how much is injected. The web UI's *Manage → Codebase map* panel views it, regenerates the structure, and edits the pinned notes.

`find_files` answers "which files are involved in X" from the same symbol/caller graph: query terms are matched against symbol names, file paths, and the cross-file reference graph, and files are ranked by symbol importance, term hits, and how widely their symbols are used. Cheaper than a grep-then-read loop when you want a shortlist to read before editing.

`code_nav` resolves definitions, references, and hover **through a running language server**, so unlike the regex-based `find_symbol`/`find_references` it disambiguates overloads, methods, and shadowed names via the actual compiler. Give it a file, a symbol name, and optionally a line to pick the occurrence. Degrades with a clear message when no server handles that file type.

`review_changes` returns **one combined unified diff** of every file the current turn changed — a cohesive multi-file view for self-review before finishing, rather than re-reading each file. Read-only, and backed by the same turn snapshots that power `/undo`.

`pull_request` and `ci_status` drive the **`gh` CLI**: `pull_request` creates, views, or comments on a PR for the current branch, and `ci_status` reports GitHub Actions check status for a branch or PR **including the logs of failed checks**, so a red build can be diagnosed without leaving the session. These complement the token/API-based `pr_*` tools (which also support GitLab); use whichever matches your auth.

`mobile` drives an **iOS simulator or Android emulator/device**: list devices, boot, install and launch a built app, tap/swipe/type/press keys, read logs, and take a screenshot (returned as an image the model can see). It only handles device control — build the app first with `bash` (`xcodebuild`/`xcrun`, or `./gradlew`). iOS input injection needs Meta's `idb` on `PATH`; without it screenshots still work.

`generate_image` and `generate_video` produce media with an image- or video-output model — placeholder assets, icons, UI mockups, diagrams, or a short clip. `generate_image` runs on any OpenAI-compatible route and can **edit** existing images by passing `reference_images`; `generate_video` uses the native Gemini route (Veo models). Both are registered only when `MN_IMAGE_MODEL` / `MN_VIDEO_MODEL` is set. Generated images are fed back to the model so it can see the result; videos are surfaced to the UI only (models can't watch video), with the saved path returned as text.

`remember` and `remember_user` split durable memory by **what the fact is about**. `remember` writes project knowledge — conventions, build/test commands, architecture notes, repo rules — to this project's `AGENTS.md` (or the global one), so it travels with the code. `remember_user` writes facts about **you** — coding style, preferred output format, tone, workflow habits, standing requests — to `~/.config/moony/USER.md`, which is injected into every session in **every** project and never committed to a repo. Both are non-mutating (no approval prompt): frictionless memory is the point.

`recall` searches your **persisted long-term memory** (the notes `remember`/`remember_user` write to project + global `AGENTS.md` and `USER.md`) and returns the entries most **relevant** to a query, ranked with BM25 over identifier-aware tokens (plus an optional hybrid semantic rerank, same as `search_code` — see `MN_EMBEDDINGS_MODEL`) — the read-side companion to `remember`.

`usage` reports the session's **token and cost** usage aggregated from per-turn metadata: total input/output/cached tokens, estimated USD cost, call counts, and a per-model (or, with `group_by: "turn"`, per-turn) breakdown.

### Feature details

- **Approval policy** — controls when a mutating tool is gated behind a confirmation prompt. Three levels (`/approval <level>`, persisted; or `--approval`/`MN_APPROVAL`):
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

- One of: a running **[Ollama](https://ollama.com)** server (default; local `http://localhost:11434` or remote via `MN_OLLAMA_HOST`) with a model pulled, **or** an API key for a hosted provider (OpenAI, Anthropic, OpenRouter, Gemini, DeepSeek, Groq, xAI, Mistral, Together.ai, Fireworks.ai, Moonshot, Z.AI, DashScope, NVIDIA NIM, Hugging Face, or Venice).
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

