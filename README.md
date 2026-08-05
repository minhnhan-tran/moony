<p align="center">
  <img src=".github/banner.svg" alt="moony" width="560">
</p>

A terminal coding agent in the spirit of [OpenCode](https://github.com/sst/opencode) / Claude Code, written in Go. It works with a local **[Ollama](https://ollama.com)** server (default, no API key) or a hosted provider — **OpenAI**, **Anthropic (Claude)**, **OpenRouter**, **Google Gemini**, **DeepSeek**, **Groq**, **xAI (Grok)**, **Mistral**, **Together.ai**, **Fireworks.ai**, **Moonshot (Kimi)**, **Z.AI (GLM)**, **DashScope (Qwen)**, **NVIDIA NIM**, **Hugging Face**, **Venice**, **OmniRoute**, **Cerebras**, **Perplexity**, **Cohere**, **DeepInfra**, **LM Studio**, **llama.cpp**, **Azure OpenAI**, **Google Vertex AI**, **AWS Bedrock**, or **GitHub Copilot** — via their OpenAI/Anthropic-compatible APIs (Bedrock is SigV4-signed via the AWS SDK; Copilot via its own SDK/CLI). It streams responses, calls tools (read/write/edit files, run shell commands, search the tree), and loops until your task is done — all inside a Bubble Tea TUI.

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

## Highlights

- **Streaming TUI** with live markdown rendering (token-by-token), tool activity, token & **cost** counters, mouse-wheel scroll, todo panel.
- **29 model providers** (`/provider`) — **Ollama** (local, default, no key), plus hosted **OpenAI**, **Anthropic (Claude)**, **OpenRouter**, **Google Gemini**, **DeepSeek**, **Groq**, **xAI (Grok)**, **Mistral**, **Together.ai**, **Fireworks.ai**, **Moonshot (Kimi)**, **Z.AI (GLM)**, **DashScope (Qwen)**, **NVIDIA NIM**, **Hugging Face**, **Venice**, **OmniRoute**, **Cerebras**, **Perplexity (Sonar)**, **Cohere**, **DeepInfra**, **Azure OpenAI**, **Google Vertex AI**, **AWS Bedrock**, and **GitHub Copilot** — plus the local, keyless **LM Studio** and **llama.cpp** servers. Switch at runtime; the choice is saved.
- **Model picker** (`/models` — live from your provider) and **extended thinking** (`/reasoning`), with your choices saved across launches.
- **Built-in tools**: `read_file`, `write_file`, `edit_file`, `multi_edit`, `apply_patch`, `list_dir`, `glob`, `grep`, `bash` (with `background` jobs — optionally on a pseudo-terminal — via `bash_output`/`bash_kill`), `web_fetch`/`batch_web_fetch`, `web_search`, `test_run`, `recall`, `usage`, `todo_write`, git (`git_status`, `git_diff`, `git_log`, `git_commit`, `git_branch`), and GitHub/GitLab (`pr_create`, `pr_list`, `pr_comments`, `pr_checks`, plus `gh`-based `pull_request`/`ci_status`).
- **Code intelligence**: `semantic_search` (meaning-based search over a local embedding index — no external service), a maintained **`CODEBASE_MAP.md`** injected into the prompt so the model stops re-exploring the tree, `find_files` and `code_nav` (compiler-grade go-to-definition/references via a language server), and `review_changes` for one combined diff of the turn.
- **Media & devices**: `generate_image` / `generate_video` (image editing and Veo clips) and `mobile` — drive an iOS simulator or Android emulator: install, launch, tap/swipe/type, logs, and screenshots the model can see.
- **Plan vs Build modes** — `/plan` disables mutating tools for safe exploration; a first-class, **approvable** plan artifact (`/plan <description>`, `/plan approve`) is saved with the session.
- **Session tools**: `/init` (bootstrap `AGENTS.md`), `/undo`, `/compact`, `/goal` (judge-verified stop-condition), `/checkpoint` (auto-distilled context that survives compaction/resume).
- **Configurable approval policy** (`prompt`/`safe`/`auto`), **persisted permission rules**, **diff previews**, **session persistence** (`--continue`/`--session`), and **auto-compaction**.
- **Headless mode** — `-p`/`--print "prompt"` runs one prompt non-interactively (`--json` for machine-readable output). Great for scripts, pipelines, and CI.
- **Multimodal input** — images, PDFs, and Office files (docx/xlsx/pptx).
- **Path sandboxing** for file tools, plus an optional **OS-level `bash` sandbox** (`sandbox-exec` on macOS, `bwrap` on Linux).
- **Extensible**: `AGENTS.md` project instructions, custom slash commands, sub-agents (`task`), out-of-process worker agents (`delegate`), Agent Skills, MCP servers (tools/resources/prompts), LSP diagnostics, format-on-write.
- **Performance & cost knobs**: parallel tool execution, repo-map priming, verbosity steering, smart tool-output truncation, code-aware `read_file` outlines, dynamic effort routing, an automatic verification loop, and optional self-review.
- **Web UI + relay** — an optional browser front end served through a relay the terminal dials out to, with an always-on background connector daemon. Adds **quick actions** (preset prompts with per-turn model pins), a **Task Board** that runs prompts in parallel, each in its own throwaway git worktree, with Apply/Discard diff review, a read-only **file viewer**, **cross-machine transcript search**, a **Doctor** panel, and `/healthz` + `/metrics`.

## Documentation

| Guide | Covers |
|---|---|
| [Getting Started & Architecture](docs/getting-started.md) | How it works, built-in tools, feature details, requirements, install, build from source, releasing |
| [Web UI & Relay](docs/web-and-relay.md) | Browser UI, relay mode, the always-on connector, authentication |
| [Commands & Configuration](docs/commands-and-configuration.md) | In-app commands & keys, env vars/flags, headless mode, upstream routes |
| [Extensibility](docs/extensibility.md) | `AGENTS.md`, memory, custom commands, sub-agents, hooks, Agent Skills, MCP, LSP, formatters, multimodal input, worker agents |
| [Development](docs/development.md) | Building, testing, releasing, license |

## License

No license file is included yet. Until a `LICENSE` is added, all rights are
reserved — open an issue if you need clarification on usage.
