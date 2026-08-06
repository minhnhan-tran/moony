<p align="center">
  <img src=".github/banner.svg" alt="moony" width="560">
</p>

<p align="center">
  <strong>A terminal coding agent that edits your files, runs your commands, and keeps going until the task is done.</strong>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#highlights">Highlights</a> ·
  <a href="docs/getting-started.md">Docs</a> ·
  <a href="docs/web-and-relay.md">Web UI</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="Platforms" src="https://img.shields.io/badge/macOS%20·%20Linux%20·%20Windows-informational">
  <img alt="Providers" src="https://img.shields.io/badge/providers-28-00afff">
  <img alt="Local first" src="https://img.shields.io/badge/local%20models-Ollama%20·%20LM%20Studio%20·%20llama.cpp-ff6ec7">
</p>

Written in Go, in the spirit of [OpenCode](https://github.com/sst/opencode) and Claude Code. Point it at a local **[Ollama](https://ollama.com)** server with no API key, or at any of **28 supported providers**. It streams the response, calls tools (read/write/edit files, run shell commands, search the tree, drive a browser), and loops until your task is finished — all inside a Bubble Tea TUI, with an optional web UI.

<p align="center">
  <img src=".github/media/tui.png" alt="moony running in the terminal" width="860">
</p>

## Quick start

```sh
git clone https://github.com/minhnhan-tran/moony && cd moony
make build         # -> ./bin/moony
```

Then pick a backend. Local, no key, nothing to sign up for:

```sh
ollama serve &
ollama pull llama3.1:8b     # moony's default model
./bin/moony
```

…or use a hosted provider:

```sh
export MN_OPENAI_API_KEY=sk-...     # or MN_ANTHROPIC_API_KEY, MN_GEMINI_API_KEY, …
./bin/moony -model gpt-5
```

Useful from the first run:

```sh
./bin/moony -p "explain what this repo does"   # headless: one prompt, then exit
./bin/moony --continue                         # resume your last session
```

Inside the TUI, `/help` lists every command, `/model` switches models, `/relay` connects you to the browser UI, and `/init` writes an `AGENTS.md` so the agent learns your project's conventions. Full walkthrough: **[docs/getting-started.md](docs/getting-started.md)**.

> **Requirements:** Go 1.26+ to build. Everything else is optional — `git` for worktree features, `ripgrep` for faster search, Node only if you touch the web UI's CSS.

## Highlights

### The agent loop

| | |
|---|---|
| **Real tool use** | Reads, writes and edits files, runs shell commands, searches the tree, fetches URLs, drives a real browser. |
| **Plans before it acts** | Drafts an approvable plan for multi-step work, then executes it step by step with a live task list. |
| **Sub-agents** | Delegates focused work (explore, review, plan) to parallel agents, each in its own context — and optionally its own throwaway git worktree. |
| **Knows your codebase** | A local symbol/caller map plus semantic search, so it finds the right file instead of grepping blindly. |
| **Self-verifying** | Runs your build/test command after edits and fixes what it broke; a judge-verified `/goal` sets an explicit stop condition. |

### Safety and control

| | |
|---|---|
| **Approval policies** | `prompt`, `safe` or `auto`, with per-tool rules that persist. Diffs are previewed before they're applied. |
| **Sandboxing** | Confine shell writes to the working directory, with network on or off. Sub-agents inherit it. |
| **Blocking hooks** | A `pre_tool` hook exiting `77` refuses a tool call outright, so project policy is enforced rather than merely logged. |
| **Undo and review** | `/undo` reverts a turn's file changes; `/changes` shows one combined diff of everything the turn touched. |
| **Never loses context** | Auto-compaction plus `/checkpoint`; if a provider *rejects* a turn as too long, the history is trimmed and the round re-run instead of failing. |

### Models

**28 providers**, all through their OpenAI- or Anthropic-compatible APIs — so a new backend is usually just a base URL and a key.

<details>
<summary>Full provider list</summary>

**Local:** Ollama · LM Studio · llama.cpp

**Hosted:** OpenAI · Anthropic (Claude) · Google Gemini · OpenRouter · DeepSeek · Groq · xAI (Grok) · Mistral · Together.ai · Fireworks.ai · Moonshot (Kimi) · Z.AI (GLM) · DashScope (Qwen) · NVIDIA NIM · Hugging Face · Venice · OmniRoute · Cerebras · Perplexity · Cohere · DeepInfra · GitHub Copilot

**Cloud:** Azure OpenAI · Google Vertex AI · AWS Bedrock (SigV4-signed via the AWS SDK)

</details>

Mix them freely: run cheap models for file reads and a strong one for reasoning, route embeddings or image generation to a different backend entirely, and — with quota fallback on — hop to the next provider in a chain when one hits its spend budget, then return once the quota resets.

### Beyond the terminal

<p align="center">
  <img src=".github/media/web-ui.png" alt="the moony web UI, served through the relay" width="900">
</p>

| | |
|---|---|
| **Web UI + relay** | The same agent in a browser, served through a relay your terminal dials out to. Adds a Task Board that runs prompts in parallel with Apply/Discard diff review, a background-jobs panel, quick actions, cross-machine transcript search, and a file viewer. |
| **Extensible** | MCP servers, Agent Skills, custom sub-agents, lifecycle hooks, and Bubble Tea plugins. |
| **Automation** | Headless single-prompt runs with JSON output, scheduled prompts, multi-step workflows, and an always-on daemon that can keep itself updated. |
| **LSP + debugging** | Real diagnostics from your language server, and DAP breakpoints for stepping through a failure. |

## Documentation

| | |
|---|---|
| **[Getting started](docs/getting-started.md)** | How the loop works, install, first run |
| **[Commands & configuration](docs/commands-and-configuration.md)** | Every slash command, key binding and environment variable |
| **[Web UI & relay](docs/web-and-relay.md)** | Browser front end, relay, task board, background jobs |
| **[Extensibility](docs/extensibility.md)** | MCP, skills, sub-agents, hooks, plugins |
| **[Development](docs/development.md)** | Repo layout, build and test |

## License

No license file is included yet. Until a `LICENSE` is added, all rights are
reserved — open an issue if you need clarification on usage.
