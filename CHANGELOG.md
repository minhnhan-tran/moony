# Changelog

All notable changes to **moony** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The top entry always
matches `config.Version` (enforced by `TestChangelogMatchesVersion`).

## [1.2.4] — 2026-07-09

### Fixed

- **Web Persona provider cards collapsed to a 2px line, hiding the inputs.** The
  scroll region is a height-constrained flex column and the cards use
  `overflow: hidden` (rounded corners), which per spec gives each flex item
  `min-height: 0` — so with many cards the cards shrank to their borders instead
  of the region scrolling. Added `flex: 0 0 auto` to the cards so they keep their
  content height. (v1.2.3 fixed the collapse/expand logic; this fixes the
  shrink-to-a-line that made them look empty regardless.)

## [1.2.3] — 2026-07-09

### Fixed

- **Web Persona provider cards showed only their header line** — the API-key and
  base-URL inputs were hidden. Body visibility mixed two mechanisms (the DOM
  `hidden` attribute plus a `display: flex` rule that overrode it), so the state
  was inconsistent and the `.open` class only rotated the chevron. Visibility is
  now class-driven: the card body is hidden by default and shown under
  `.persona-provider.open`, which the header toggle flips. Cards still start
  expanded.

## [1.2.2] — 2026-07-09

### Changed

- web: show Persona provider cards expanded by default so key/base-URL inputs are always visible

## [1.2.1] — 2026-07-09

### Changed

- llm: use the configured provider (not hardcoded anthropic) when deciding if thinking is active

## [1.2.0] — 2026-07-09

### Added

- **Web Persona provider credentials shown as per-provider cards.** Each
  provider's API key and base-URL override are grouped into one collapsible card
  with a set / not-set status dot, and the default endpoint is shown as the
  base-URL input placeholder — replacing the flat list of ~22 stacked fields.
- **Provider usage/quota fetch restored for the SQE Anthropic gateway.**
  `Usage()` again queries `/api/usage/v1/personal` (same `x-api-key` +
  `anthropic-version` auth as the Messages API), so the TUI `/usage` command and
  the web status-bar quota chip work again. All other providers report "not
  available" and degrade gracefully.

### Changed

- **Assistant speaker label renamed to "moony"** in the web transcript and the
  TUI, replacing the leftover "sqe code agent" text from before the rebrand.
- **The status-bar scan-line and the running-session pulse now animate even
  under `prefers-reduced-motion`**, since they're primary "agent is working"
  indicators.

### Fixed

- **The in-progress todo/plan spinner and the running-session dot no longer
  freeze.** Their DOM nodes were recreated on every re-render (todos/plan/state
  events), which restarted the CSS animation; `renderTodos`/`renderTurnTodos` now
  skip no-op rebuilds and the `sess-pulse` keyframe is restart-tolerant.

## [1.1.1] — 2026-07-09

### Fixed

- **SQE Anthropic now appears in the web UI provider picker.** The browser header
  picker read a hardcoded provider list (and label map) that hadn't been updated
  for the new backend, so `sqe-anthropic` was selectable only in the TUI. The
  slash `/provider` menu and workflow picker were already dynamic.

## [1.1.0] — 2026-07-09

### Added

- **SQE Anthropic provider** (`/provider sqe-anthropic`): Claude over the same
  native Messages API and model set as public Anthropic, but defaulting to the
  SQE LAN gateway (`MOONY_SQE_ANTHROPIC_BASE_URL`, default
  `http://192.168.33.158:8787`). Its key (`MOONY_SQE_ANTHROPIC_API_KEY`) falls
  back to `MOONY_ANTHROPIC_API_KEY` when unset.
- **Provider credentials in the web Persona panel**: each provider's API key
  (masked to a last-4 hint) and base-URL override are editable in the browser
  and saved to `~/.config/moony/.env`, so they persist across restarts.
- **Optional OIDC per-user relay auth** with signed per-user connector tokens,
  off by default (`MOONY_AUTH`). When off, the relay lists all connected
  terminals as before.

### Changed

- The relay now **defaults on at `127.0.0.1:8771`** instead of off. Disable with
  `/relay off` (persisted) or `MOONY_RELAY=off`.
- The Ollama backend now uses the native `/api/chat` endpoint so `num_ctx` is
  honored (`MOONY_OLLAMA_NUM_CTX`, default 32768) — fixing empty or truncated
  replies when the prompt overflowed the server's default context.
- Vendored the Material Symbols icon font locally (dropped the Google Fonts CDN
  dependency).

### Fixed

- **Extended thinking on Claude 4.6+**: switch to adaptive thinking
  (`thinking: {type: "adaptive"}` + `output_config.effort`) from the removed
  fixed-budget shape, fixing a 400 on Opus 4.8 / Sonnet 5 / Fable 5.
- **"Invalid signature in thinking block" 400 on replay**: preserve the thinking
  block verbatim (including its signature and empty-text blocks), and auto-heal
  the error some gateways return by stripping replayed thinking blocks and
  retrying (`MOONY_ANTHROPIC_STRIP_THINKING=1` forces it from the first request).
- `/auth/me` returned 404 when relay auth was off — it now reports an anonymous
  user, clearing the browser console error.
- The header/composer provider picker could render off-screen for tall menus.
- The jumping-Claude mascot animation didn't run (SVG `transform-box`).

## [1.0.0] — 2026-07-08

Initial public release.

### Added

- **Terminal coding agent** with a streaming Bubble Tea TUI: live markdown
  rendering, tool activity, token/cost counters, a todo/files sidebar, mouse
  scroll, and slash commands.
- **Eleven model providers**, selectable at runtime with `/provider` (or
  `MOONY_PROVIDER`):
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
