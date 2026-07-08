# Changelog

All notable changes to **moony** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The top entry always
matches `config.Version` (enforced by `TestChangelogMatchesVersion`).

## [1.0.0] — 2026-07-08

Initial public release.

### Added

- **Terminal coding agent** with a streaming Bubble Tea TUI: live markdown
  rendering, tool activity, token/cost counters, a todo/files sidebar, mouse
  scroll, and slash commands.
- **Eleven model providers**, selectable at runtime with `/provider` (or
  `SQE_PROVIDER`):
  - **Ollama** (default) — a local or remote server, no API key.
  - **OpenAI**, **Anthropic** (native Messages API), **OpenRouter** (one key,
    hundreds of models), **Google Gemini**, **DeepSeek**, **Groq**, **xAI**
    (Grok), **Mistral**, **Together.ai**, and **Fireworks.ai**.
- **Extended thinking** (`/reasoning`), gated per backend + model so it's only
  requested where the provider actually supports it.
- **Tools**: read/write/edit files, run shell commands (with an approval
  policy and optional sandbox), search the tree, fetch the web, and more.
- **Sessions** with history, `--continue`/`--session`, `/compact`, `/undo`,
  goals, and auto-refreshed checkpoints.
- **Headless mode** (`-p`/`--print`, `--json`) for scripts, pipelines, and CI.
- **Extensibility**: `AGENTS.md` project instructions, custom slash commands,
  sub-agent personas (`task`), out-of-process workers (`delegate`), Agent
  Skills, MCP servers, and LSP integration.
- **Browser UI + relay**: an optional web front end served through a relay the
  agent dials out to, with an always-on background connector daemon.
