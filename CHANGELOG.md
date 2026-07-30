# Changelog

All notable changes to **moony** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The top entry always
matches `config.Version` (enforced by `TestChangelogMatchesVersion`).

## [2.3.0] — 2026-07-30

### Added

- web: session rename + pin (backend `Store.Rename`/`SetPinned`/`Persist`, `POST /api/sessions/{id}/rename` and `/pin`, sidebar hover controls with inline rename)
- web: transcript find panel (⌘F) with match highlighting, next/prev jumping and a match counter
- web: speech-to-text dictation button on the composer via `webkitSpeechRecognition`
- web: reload banner when the relay's build moves past the assets the page loaded (deferred behind the "terminal is behind" banner)

## [2.2.4] — 2026-07-24

### Changed

- web: add backend support for human-in-the-loop denial feedback

## [2.2.3] — 2026-07-21

_No changes recorded._

## [2.2.2] — 2026-07-21

### Changed

- web: optimize the relay web UI for mobile viewports

## [2.2.1] — 2026-07-21

### Changed

- relay/web: harden relay against transient errors, slow clients, unbounded bodies

## [2.2.0] — 2026-07-20

### Changed

- web: add per-agent provider/reasoning overrides to Config modal's Agents panel

## [2.1.4] — 2026-07-20

### Changed

- web: improve Fast/Self-review model layout in Persona modal (stacked full-width selects, concise labels)

## [2.1.3] — 2026-07-20

### Changed

- web: make .msg-user span .pair-q and .pair-q span .pair.pair-active explicitly
- docs: record CI commit-message gating rule in AGENTS.md
- ci: only run CI when the commit message contains 'github build'

## [2.1.2] — 2026-07-20

### Changed

- web: remap 8 icon names to glyphs actually present in the vendored Material Symbols subset font

## [2.1.1] — 2026-07-20

### Changed

- web: map 11 missing SF-symbol icon names to Material Symbols, fixing '?' glyphs

## [2.1.0] — 2026-07-20

### Changed

- web: a11y keyboard support, focus rings, streaming/diff perf, and SSE parse robustness

## [2.0.4] — 2026-07-20

### Changed

- web: render thinking/reasoning blocks as Markdown instead of raw text

## [2.0.3] — 2026-07-20

### Changed

- web: map slash.circle to code instead of block to avoid prohibition icons in command palette

## [2.0.2] — 2026-07-20

### Changed

- web: trigger slash/mention palette when list arrives after typing / or @

## [2.0.1] — 2026-07-19

### Fixed

- The web composer's provider picker (and provider label) now lists all 29 providers. Two hardcoded lists in the web UI were stuck at the old 20-provider set, so the providers added since (Cerebras, Perplexity, Cohere, DeepInfra, LM Studio, llama.cpp, Azure, Vertex, Bedrock, Copilot) never showed up in the menu.

## [2.0.0] — 2026-07-19

### Changed

- **BREAKING: the environment variable prefix changed from `MOONY_` to `MN_`.** Every env var moony reads has been renamed (e.g. `MOONY_PROVIDER` → `MN_PROVIDER`, `MOONY_ANTHROPIC_API_KEY` → `MN_ANTHROPIC_API_KEY`, `MOONY_MODEL`, `MOONY_APPROVAL`, per-provider keys/base URLs, …). This is a clean break — `MOONY_*` is no longer read, with **no deprecated fallback**. Update your shell profile, `.env` files, and CI secrets from `MOONY_*` to `MN_*`. Vendor-standard fallback env vars (`OPENAI_API_KEY`, `AWS_*`, `GH_TOKEN`, etc.) are unaffected.

## [1.20.0] — 2026-07-19

### Added

- **Three new model providers (26 → 29).** Added **DeepInfra** and the local, keyless **LM Studio** and **llama.cpp** servers, selectable with `/provider` (or `MOONY_PROVIDER`).
  - **DeepInfra** (`deepinfra`) — OpenAI-compatible bearer-key backend for `api.deepinfra.com`; a large, cheap catalog of hosted open models. Key via `MOONY_DEEPINFRA_API_KEY` (falls back to `DEEPINFRA_API_KEY` / `DEEPINFRA_TOKEN`); default model `meta-llama/Llama-3.3-70B-Instruct`.
  - **LM Studio** (`lmstudio`) and **llama.cpp** (`llamacpp`) — local, keyless OpenAI-compatible servers (like Ollama), defaulting to `http://localhost:1234/v1` and `http://localhost:8080/v1` (override with `MOONY_LMSTUDIO_BASE_URL` / `MOONY_LLAMACPP_BASE_URL`). They serve whichever model you've loaded, so use `/models` + `/model`.

### Changed

- Remote OpenAI-compatible backends gained a `noKeyOK` path so keyless local servers (LM Studio / llama.cpp) are no longer rejected for a missing API key, and no empty `Authorization` header is sent.

## [1.19.0] — 2026-07-19

### Added

- **Six new model providers (20 → 26).** Added **Cerebras**, **Perplexity (Sonar)**, **Cohere**, **Azure OpenAI**, **Google Vertex AI**, and **AWS Bedrock**, selectable with `/provider` (or `MOONY_PROVIDER`).
  - Cerebras / Perplexity / Cohere are plain OpenAI-compatible backends (`MOONY_CEREBRAS_API_KEY` / `MOONY_PERPLEXITY_API_KEY` / `MOONY_COHERE_API_KEY`).
  - **Azure OpenAI** uses your resource endpoint (`MOONY_AZURE_BASE_URL`), the `api-key` header, deployment-scoped paths, and an `api-version` query param (`MOONY_AZURE_API_VERSION`); `/model` is your deployment name.
  - **Vertex AI** uses a GCP access token (`MOONY_VERTEX_API_KEY`) plus `MOONY_VERTEX_PROJECT` / `MOONY_VERTEX_LOCATION`.
  - **AWS Bedrock** is SigV4-signed and streamed over the AWS event-stream protocol via the AWS SDK (reusing the Anthropic wire format); credentials come from `MOONY_AWS_*` / a profile / the standard AWS chain, with `MOONY_AWS_REGION`. The standard `AWS_*` env vars are left unscrubbed from tool shells so your own `aws` CLI keeps working.

### Changed

- `ReasoningActive` gating extended for the new providers (Azure follows the OpenAI reasoning rules; Cerebras/Perplexity detect thinking models; Vertex is always-on; Bedrock/Cohere gate on the model).
- Refactored the Anthropic request build into a shared `buildAnthropicBody` helper now reused by the AWS Bedrock backend.

## [1.18.2] — 2026-07-18

### Changed

- **Cheaper live markdown while streaming.** The in-progress assistant reply is no longer re-parsed through Glamour on a fixed 60ms timer regardless of content. Two changes cut the CPU cost of long/fast replies: (1) an append-only content guard skips the Glamour pass entirely when the streamed text hasn't grown since the last render (so refreshes from non-text events mid-turn are free), and (2) the render throttle is now adaptive — short replies stay snappy at 60ms while longer ones scale up toward 300ms, keeping total render work roughly bounded instead of O(n²) over the turn. No visible change to output; the completed reply is still rendered in full. Live markdown can still be disabled with `MOONY_STREAM_MARKDOWN=0`.

## [1.18.1] — 2026-07-18

### Changed

- **Split the README into focused docs.** The top-level `README.md` is now a concise intro + feature overview; the detailed material moved into `docs/`: [getting-started.md](docs/getting-started.md) (how it works, tools, requirements, install, build, releasing), [web-and-relay.md](docs/web-and-relay.md), [commands-and-configuration.md](docs/commands-and-configuration.md), [extensibility.md](docs/extensibility.md), and [development.md](docs/development.md). No behavior changes.

## [1.18.0] — 2026-07-13

### Added

- **GitHub Copilot provider.** A 20th model provider (`/provider copilot`), driven through the official [github/copilot-sdk](https://github.com/github/copilot-sdk) Go SDK rather than a plain HTTP API — the SDK spawns and talks to the local `copilot` CLI over JSON-RPC. It runs in **autonomous mode**: the Copilot CLI plans and calls its *own* built-in tools (file read/write, shell, etc.) with its permission prompts auto-approved, and moony streams the assistant's text, reasoning, and tool activity into the TUI as a normal turn. Requires the `copilot` CLI installed and on `PATH` (or `MOONY_COPILOT_CLI_PATH`) and a GitHub token via `MOONY_COPILOT_TOKEN` (falls back to `COPILOT_GITHUB_TOKEN` / `GH_TOKEN` / `GITHUB_TOKEN`, else the CLI's stored credentials). `/reasoning` maps to Copilot's reasoning-effort levels; `/models` lists the account's available models. New backend: `internal/llm/copilot.go`. Note: because Copilot's CLI executes its own tools, moony's approval policy / sandbox / tool logging do **not** cover what it does on disk.

## [1.17.1] — 2026-07-13

### Changed

- Status bar shows a **"↺ compacting N messages…"** chip while context compaction is in progress (the summarise LLM call), so a quiet mid-turn pause is no longer unexplained. The chip clears automatically once compaction completes.

## [1.17.0] — 2026-07-13

### Added

- **Debug Adapter Protocol integration.** 14 new tools (`dap_connect`, `dap_disconnect`, `dap_launch`, `dap_attach`, `dap_set_breakpoints`, `dap_continue`, `dap_next`, `dap_step_in`, `dap_step_out`, `dap_pause`, `dap_stack_trace`, `dap_scopes`, `dap_variables`, `dap_evaluate`) backed by a new `internal/dap` package — a pure-stdlib TCP DAP client with Content-Length framing, request/response correlation, and async event dispatch. Stepping tools block until the next stopped event and return the new file:line + accumulated output. `dap_launch`/`dap_attach` handle the full handshake (launch → initialized event → optional inline breakpoints → configurationDone → first stop). Set `MOONY_DAP_ADDR=host:port` to auto-connect at startup, or call `dap_connect` interactively. Works with `dlv dap` (Go), `debugpy` (Python), `node --inspect` (Node.js), and any DAP-compliant adapter.

## [1.16.0] — 2026-07-13

### Added

- **Cross-session cost budgets and completion notifications.** `MOONY_DAILY_BUDGET`/`MOONY_MONTHLY_BUDGET` cap spend across *all* sessions (unlike the existing per-turn-only `MOONY_TURN_WARN_COST`/`MOONY_TURN_MAX_COST`), tracked in a small ledger (`usage_ledger.json`) and warn-only — crossing a cap surfaces an Info message, it never blocks a turn. `MOONY_NOTIFY_WEBHOOK_URL` fires a fire-and-forget outbound webhook on turn completion / approval-needed, with a body compatible with both Slack/Mattermost and Discord incoming webhooks (filtered by `MOONY_NOTIFY_EVENTS`, gated for turn-done by `MOONY_NOTIFY_MIN_SECONDS` so quick turns don't ping you). The TUI also rings the terminal bell on turn-done/approval-needed (`MOONY_TUI_BELL`, default on).

## [1.15.0] — 2026-07-13

### Added

- **GitHub + GitLab PR tooling: `pr_create`, `pr_list`, `pr_comments`, `pr_checks`.** A new `internal/githost` package normalizes GitHub's REST v3 and GitLab's REST v4 behind one `Client` interface (GitHub "pull request" vs GitLab "merge request" terminology absorbed there), so the tools don't need to know which host a repo lives on — resolved automatically from `git remote get-url origin`, including self-hosted instances via `MOONY_GITHUB_BASE_URL`/`MOONY_GITLAB_BASE_URL`. `pr_create` opens a PR/MR from the current branch (mutates, has a Preview). `pr_list` lists open PRs/MRs or views one in detail. `pr_comments` lists review comment threads, or posts a reply/new comment when given a body (mutates only then). `pr_checks` reports CI check-run/pipeline status. Auth resolves in order: `MOONY_GITHUB_TOKEN`/`MOONY_GITLAB_TOKEN` (or the bare `GITHUB_TOKEN`/`GITLAB_TOKEN`), `gh auth token`/`glab auth token`, then `~/.netrc`.

## [1.14.0] — 2026-07-13

### Added

- **Provider-agnostic embeddings, and `recall` gets the same hybrid semantic rerank `search_code` already had.** `Client.Embed` previously only ever hit Ollama's native `/embeddings` endpoint, regardless of the session's main provider. It now routes through the same `remoteBackendFor` plumbing chat completions use, gated by a new `EmbeddingsProvider` field (`MOONY_EMBEDDINGS_PROVIDER`, independent of the main `LLMProvider`) — so a session running on Anthropic can still point embeddings at OpenAI (or any other OpenAI-compatible provider) without switching its main model. Anthropic itself has no embeddings API and is rejected with a clear error rather than silently misrouted. `recall` was pure BM25 until now; it reuses the same `Registry.embedder`/`cosineSim`/blend-scoring `search_code` uses, so a paraphrased query can surface a memory note without shared keywords — a no-op when no embedder is configured.

## [1.13.3] — 2026-07-13

### Changed

- web: move the pager back to bottom-right, clear of pair-a's scrollbar (not the opposite corner)

## [1.13.2] — 2026-07-13

### Changed

- web: move the pager to bottom-left (was bottom-right, sitting near pair-a's scrollbar)

## [1.13.1] — 2026-07-13

### Changed

- web: shrink the pager and shift it clear of pair-a's scrollbar

## [1.13.0] — 2026-07-13

### Changed

- **Merged `scrollBtn` into the pager, and the pager no longer overlaps answer text.** The scroll-to-bottom button and the "< n/total >" pair pager were two separate floating controls (bottom-center and bottom-right); they're now one shared bottom-right control — each half shows/hides independently (scroll-to-bottom per scroll position, the pair nav per pair count), and the container itself only hides when neither is needed. `.pair-a` also gained bottom clearance so its content never renders underneath the floating control, even scrolled all the way down.

## [1.12.3] — 2026-07-13

### Fixed

- **`pagerPrev`'s left chevron was still invisible after the previous "fix."** The real cause wasn't a missing codepoint mapping — `fontTools` confirms the bundled `material-symbols.woff2` subset (144 glyphs) genuinely has no `chevron_left` glyph (only `chevron_right`, since nothing used the former before the pager). Pointing `MATERIAL_CP["chevron_left"]` at the textbook codepoint (as v1.12.2 did) rendered a real but *absent* glyph, i.e. nothing. `AppIcon._render()` now detects `chevron_left` and renders `chevron_right`'s (present) glyph mirrored via `transform: scaleX(-1)` instead — no font file changes needed.

## [1.12.2] — 2026-07-13

### Fixed

- **`pagerPrev` (and any `chevron.left` icon) rendered a "help" glyph instead of a left chevron.** `SF_TO_MATERIAL` mapped `chevron.left` → `chevron_left`, but the `MATERIAL_CP` codepoint table had no `chevron_left` entry (only `chevron_right`), so the lookup's `||` fallback silently rendered the generic "help" icon. Added the missing codepoint.

### Changed

- web: make the pager a rectangle instead of a pill (`--r-lg`/`--r-sm` instead of `--r-full`/`--r-pill`).

## [1.12.1] — 2026-07-13

### Changed

- web: unify the pager into one pill (< n/total >, shared bg/border) and move it to the transcript's bottom-right

## [1.12.0] — 2026-07-13

### Added

- **Pager merged into one `< n/total >` control, and `n/total` now opens a question-jump dropdown.** `pagerPrev`/`pagerPos`/`pagerNext` were three separate controls in a vertical stack; they're now a single horizontal `< n/total >` row. Clicking `n/total` opens a dropdown listing every question asked this turn — pick one to jump straight to that Q&A pair instead of stepping through prev/next one at a time.

## [1.11.9] — 2026-07-13

### Fixed

- **Escalating from a non-thinking Fast/Aux round back to a thinking-enabled call could 400 forever on Anthropic.** AdaptiveModel/CheapReads (and Self-review) can serve a round on the Fast/Aux tier without extended thinking, possibly on a different provider; when the turn then escalates back to a thinking-enabled call, the replayed history's last assistant turn (tool_use, no thinking block) violates Anthropic's "an assistant turn must start with a thinking block" rule. The existing self-heal (strip all historical thinking, retry) already fixes this class of error for the replayed-signature case; `isThinkingSignatureError` now also matches this structural rejection so the same retry kicks in here too — a one-time strip-and-retry, since the fresh response gets its own leading thinking block.

## [1.11.8] — 2026-07-12

### Changed

- web: order Fast/Self-review tier fields as provider > model > reasoning

## [1.11.7] — 2026-07-12

### Changed

- web: widen the manageOverlay modal further (880px -> 1200px cap, taller too)

## [1.11.6] — 2026-07-12

### Changed

- web: widen the manageOverlay modal (640px -> 880px cap)

## [1.11.5] — 2026-07-12

### Changed

- web: lay out Fast model and Self-review model (list, provider, reasoning) as a single 2-column row in settings

## [1.11.4] — 2026-07-12

### Changed

- web: expand the tool-call group by default instead of starting collapsed

## [1.11.3] — 2026-07-12

### Changed

- **Self-review result line now names the provider, model, and reasoning level.** The `Self-review: no issues found. …` and `Self-review flagged possible issues …` notices now carry the reviewer's `model on provider, reasoning: level` alongside the token/cost metrics — matching the `Self-reviewing the diff…` starting notice — and the web transcript renders the three as the same colored pills.

## [1.11.2] — 2026-07-12

### Fixed

- **Turn-meta footer now shows the reasoning level that actually served the turn.** When AdaptiveModel/CheapReads downshifted a round to the Fast tier (with `MOONY_FAST_REASONING` set to a different level), the per-turn footer kept showing the *session/main model's* configured reasoning instead of the Fast tier's — even though the footer already correctly showed the downshifted *model*. Both paths are fixed: the persisted `TurnMeta.Reasoning` (used on session reopen/replay) now records the effective per-turn level, and the live `turn_done` event carries it to the browser, which uses it instead of the composer's current level.

### Changed

- docs: note the PlanStore.Set-vs-Sync distinction for future plan-status mirror writes.

## [1.11.1] — 2026-07-12

### Fixed

- **Plan gate no longer haunts follow-up turns.** An approved or completed plan could silently revert to *draft* status and keep blocking the mutating tools (`bash`, `edit_file`, …) on later, unrelated turns — forcing a redundant `exit_plan`/`/plan approve` each time. The agent's internal "mark plan done / retire / restore-on-resume" writes went through `PlanStore.Set`, which fires the same notify the `plan_write` tool uses (`applyDraftPlan`, which forces `Status=draft`), so each completion immediately undid itself. Those host-side mirror writes now use a new silent `PlanStore.Sync`; only genuine model `plan_write` calls fire the notify.

### Changed

- web: place the transcript pager position indicator directly under the prev button.

## [1.11.0] — 2026-07-12

### Added

- **`lsp_diagnostics` tool** — surfaces the language server's live compiler/linter diagnostics (severity, line, message) for a file from its current on-disk contents, so the agent can check an edit compiles cleanly without a full build.
- **`web_search` tool** — queries a web search engine and returns ranked title/URL/snippet results so the agent can *discover* pages (not just fetch known URLs); keyless DuckDuckGo HTML by default, or the Brave Search API when `MOONY_SEARCH_API_KEY` is set, honouring the Domains allow/deny rules.
- **`test_run` tool** — runs the project's tests and returns a structured summary (passed/failed/skipped, coverage, failing-test names + output), parsing `go test -json` for Go and running the conventional command for npm/Cargo/pytest projects; a failing suite is a normal result, not a tool error.
- **`recall` tool** — retrieves persisted long-term memory notes (from project + global `AGENTS.md`) ranked by relevance to a query using BM25 over identifier-aware tokens — the read-side companion to `remember`.
- **`usage` tool** — reports the session's token and cost usage (input/output/cached tokens, estimated USD, call counts) with a per-model or per-turn breakdown.

## [1.10.5] — 2026-07-12

### Fixed

- **The transcript no longer overflows over the question / approval panel.** The
  1.10.0 paged view put `min-h-0` and `overflow-hidden` utility classes on
  `#transcript`, but the prebuilt (purged) `tailwind.css` doesn't include those
  two — so `#transcript` stayed `display:block` / `overflow:visible` and grew to
  full content height, spilling the answer down over the translucent `#question`
  (and `#approval`) panels below it. `#transcript`'s flex-column clip is now
  defined in `app.css` (loaded verbatim, not purged), so the answer scrolls within
  its slot.

## [1.10.4] — 2026-07-12

### Fixed

- **The Persona panel's Fast / Self-review model dropdown now repopulates when
  you change that tier's provider.** Two problems: `ModelFallback` only had a real
  catalogue for Anthropic/SQE/Gemini, so a cross-provider tier's model dropdown
  showed just that provider's single default; and it only re-rendered on Save. Now
  the panel **live-fetches the chosen tier provider's own catalogue** (via a
  config clone that already carries every provider's key/base-URL — falling back
  to the static list if unauthenticated), and changing the Fast/Self-review
  provider select **re-renders immediately** so its model list updates on the spot.

## [1.10.3] — 2026-07-12

### Changed

- **Thinking (reasoning) blocks now always start expanded**, including reasoning
  replayed from a reopened session — previously only live reasoning streamed
  expanded while replayed reasoning started collapsed (1.3.0). The header still
  toggles collapse on click.

## [1.10.2] — 2026-07-12

### Changed

- The transcript pager's previous/next buttons now use **up / down chevrons**
  (previous = up, next = down) to match their vertical top-right / bottom-right
  placement, instead of left / right.

## [1.10.1] — 2026-07-12

### Changed

- **The transcript pager moved to the right edge** (from a centered row at the
  bottom): the **previous** button is pinned to the top-right, **next** to the
  bottom-right, and the `n / total` position sits centered on the right edge
  between them — a vertical pager down the transcript's edge. The
  scroll-to-bottom button returns to bottom-center now that the pager has vacated
  it.

## [1.10.0] — 2026-07-12

### Added

- **Paged single-pair transcript.** The web transcript now shows one Q&A pair at
  a time instead of a continuous scroll: the user's question is pinned as a fixed
  header over the model's answer in its own scrollable region, with a centered
  **prev / next pager** (and an `n / total` counter) to walk earlier pairs. A new
  question always jumps to the latest pair, and answer content keeps streaming
  into the latest pair even while you're reading an older one. The
  scroll-to-bottom button is kept for the answer region (moved to the bottom-right
  so it doesn't collide with the pager).

## [1.9.3] — 2026-07-12

### Changed

- **The self-review notice now names the review's provider and reasoning level,
  styled as pills.** `Self-reviewing the diff for introduced bugs… (<model> on
  <provider>, reasoning: <level>)` renders the model / provider / reasoning as the
  same colored pills as the downshift notice, and shows the full model id instead
  of a shortened one. The level reflects what self-review actually runs at — off
  by default, or the `MOONY_AUX_REASONING` level when set.

## [1.9.2] — 2026-07-12

### Changed

- **The adaptive / cheap-reads downshift notice is now styled and persisted.** In
  the web transcript the three parts of `auto: using <model> on <provider>
  (reasoning: <level>) …` render as distinct colored pills (model = brand,
  provider = violet, reasoning = green), matching the provider/model-switch
  confirmations. The notice is also saved to session history, so it replays in
  place (with the same styling) when a session is reopened instead of vanishing.

## [1.9.1] — 2026-07-12

### Fixed

- **The adaptive / cheap-reads downshift notice now names the provider, the full
  model id, and the reasoning level** — e.g. `auto: using gemma4:8b on Ollama
  (reasoning: low) for this round (adaptive model).` It previously ran the model
  through a shortener that split on the first `.`/`:`, mangling version-dotted
  ids (a fast model could show up as a bare `2`), and it named neither the
  provider nor the reasoning level — so a cross-provider / cross-reasoning Fast
  tier (new in 1.9.0) was invisible in the notice.

## [1.9.0] — 2026-07-12

### Added

- **Per-tier provider + reasoning level for the Fast and Self-review models.**
  The Persona panel's **Fast model** (`MOONY_FAST_MODEL`) and **Self-review
  model** (`MOONY_AUX_MODEL`) settings each gain a **provider** select and a
  **reasoning-level** select, so either tier can run on a *different provider*
  than the session and at a chosen reasoning level — e.g. a session on Opus via
  the SQE gateway with cheap-reads/adaptive downshifts routed to a local Ollama
  model, or self-review sent to a cheaper provider. Picking a cross-provider
  tier repopulates its model dropdown with that provider's known models so the
  exact model is selectable ("Full model picker"). New env vars
  `MOONY_FAST_PROVIDER` / `MOONY_FAST_REASONING` / `MOONY_AUX_PROVIDER` /
  `MOONY_AUX_REASONING` (aux provider falls back to the fast provider; aux
  reasoning defaults to off and does not inherit the fast level). All four
  persist across restarts. **Inherit-when-unset:** with nothing chosen, routing
  is byte-identical to before — the fast provider/reasoning apply only on a
  downshifted round, and self-review still forces reasoning off unless a level
  is set.

## [1.8.1] — 2026-07-12

### Fixed

- A turn no longer ends **silently** when the provider returns an empty response
  (HTTP 200 with no text, no tool call, and zero tokens — seen with unconfigured
  routes behind gateways like OmniRoute). moony now emits a notice naming the
  model and pointing at `/model`, instead of looking like it froze.

## [1.8.0] — 2026-07-12

### Added

- **OmniRoute provider** (`/provider omniroute`): a self-hosted, OpenAI-compatible
  gateway ([diegosouzapw/OmniRoute](https://github.com/diegosouzapw/OmniRoute))
  defaulting to `http://localhost:20128/v1` (override `MOONY_OMNIROUTE_BASE_URL`),
  authed with `MOONY_OMNIROUTE_API_KEY`, and starting on its zero-config `auto`
  router model. Routes through the existing OpenAI-compatible backend; selectable
  in the TUI `/provider` list and the web provider picker, with its own Persona
  key/base-URL card.

## [1.7.0] — 2026-07-10

### Changed

- tools,lsp: add lint and LSP go-to-definition/references/hover tools

## [1.6.5] — 2026-07-10

### Changed

- web: drop the 'needs MOONY_*_API_KEY' clause from the /provider hdrMenu

## [1.6.4] — 2026-07-09

### Changed

- docs: fill README gaps left by the new providers and cost-routing toggles

## [1.6.3] — 2026-07-09

### Changed

- web: render the /provider list as scannable rows instead of raw fixed-width text

## [1.6.2] — 2026-07-09

### Changed

- web: give provider/model switch info notices a distinct style

## [1.6.1] — 2026-07-09

### Changed

- config,web,tui: scope the /models fetch-failure fallback to the active provider

## [1.6.0] — 2026-07-09

### Changed

- config: default MOONY_ADAPTIVE_MODEL and MOONY_CHEAP_READS to on

## [1.5.3] — 2026-07-09

### Changed

- web: fix prettyModel qwen formatting dropping capitalization on any digit-bearing token

## [1.5.2] — 2026-07-09

### Changed

- web: add the 6 new providers to the relay UI's provider/model pickers

## [1.5.1] — 2026-07-09

### Changed

- web: always animate the in-progress todo/phase spinner (drop reduced-motion override)

## [1.5.0] — 2026-07-09

### Changed

- provider: add 6 public OpenAI-compatible providers (Moonshot, Z.AI, DashScope, NVIDIA NIM, Hugging Face, Venice)
- config,web: persist MOONY_ADAPTIVE_MODEL persona toggle across restarts

## [1.4.0] — 2026-07-09

### Changed

- agent: also run maybeCompact mid-turn so long single turns auto-compact

## [1.3.0] — 2026-07-09

### Changed

- web: make thinking blocks collapsible; collapse reasoning replayed from history

## [1.2.6] — 2026-07-09

### Changed

- Web Persona panel now groups the provider credential cards under their own
  **"Provider"** section (a second scroll region), separate from the session /
  MCP-token fields above.

## [1.2.5] — 2026-07-09

### Changed

- Web Persona provider cards now start **collapsed** by default. The header's
  set/not-set dot and last-4 hint show each provider's config at a glance; click
  a header to expand its API-key and base-URL inputs.

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
