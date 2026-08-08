# Changelog

All notable changes to **moony** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The top entry always
matches `config.Version` (enforced by `TestChangelogMatchesVersion`).

## [2.18.4] — 2026-08-08

### Fixed

- `deploy/aws/ec2`: the security group applied `--web-cidr` to port 80, which would have
  made Let's Encrypt issuance fail, since validation arrives over port 80 from addresses
  that cannot be predicted. Port 80 is now world-open when a domain is configured (it
  serves only the challenge and a redirect) while `--web-cidr` still restricts the UI on
  443; without a domain, port 80 is the UI and stays restricted.

## [2.18.3] — 2026-08-08

### Fixed

- `deploy/aws/ec2`: the AL2023 AMI is published under
  `/aws/service/ami-amazon-linux-latest`, not `ami-al2023-latest`, so CloudFormation
  could not resolve the image and the stack never started.
- Caddy rejects `email` with an empty argument, so deploying without `--email` left the
  proxy crash-looping in front of a healthy relay. The address now comes from a file the
  instance always writes and the global options block imports.
- Under git-bash, arguments that look like absolute Unix paths are rewritten before the
  native `aws.exe` sees them, which turned an SSM parameter name into a path inside the
  Git install. That one override is now excluded from conversion.

Verified end to end on a live stack: relay healthy, Caddy serving the SPA, `/api/relay`
advertising the Elastic IP, and the tunnel port accepting TCP.

## [2.18.2] — 2026-08-08

### Changed

- `deploy/aws/ec2/deploy.sh` preflights before touching CloudFormation: it reports not
  being signed in, warns on account root credentials, and recognises an account whose
  sign-up was never finished (STS and IAM answer while every regional service returns
  `OptInRequired`, which reads as a permissions problem and is not one).
- The VPC and subnet are now discovered from the account's default VPC and its first
  public subnet, so deploying is one command plus a domain.

## [2.18.1] — 2026-08-08

### Added

- `deploy/vercel/`: rewrite config and notes for putting Vercel in front of a relay that
  runs on a VM. The relay cannot be hosted on Vercel at all (it needs a raw TCP listener
  for connectors and a long-lived process holding the connector registry and open SSE
  streams), so the docs spell out what the proxy does and does not buy.

## [2.18.0] — 2026-08-07

### Added

- `deploy/aws/ec2/`: a single-instance AWS recipe for the relay. Caddy terminates TLS
  in front of the browser port and the connector tunnel is published on an Elastic IP,
  so no ALB and no NLB are needed (~$15/month instead of ~$45). `deploy.sh` ships the
  working tree through a private S3 bucket, so a private repo needs no deploy key: the
  instance reads the tarball with its own IAM role. Auth settings come from an SSM
  SecureString read at boot, and shell access is SSM Session Manager rather than SSH.
- `docs/deployment.md` gains an AWS section covering when to prefer this over the
  Kubernetes manifests.

## [2.17.2] — 2026-08-07

### Fixed

- The jumping-Claude loader no longer freezes mid-jump while a reopened session's
  history streams into the transcript. It was an `<img>` of an animated SVG, which
  Blink re-rasterizes on the main thread every frame; it is now an inline `<template>`
  clone whose transforms animate on the compositor (and honour `prefers-reduced-motion`).
- History replay no longer thrashes layout. Each replayed turn toggled `pair-active` on
  every pair (a `display:none/flex` switch, so two box trees rebuilt per turn) and then
  forced ~3 synchronous layouts re-measuring the pager, scroll button and scroll
  position, on a transcript that grows every turn. Pair switching is now O(1) and the
  measuring happens once at the history/live boundary, so a burst of N replayed events
  costs one layout instead of 3N — most noticeable on "load earlier history".

## [2.17.1] — 2026-08-07

### Fixed

- Reopening a session while `MN_PROVIDER` or `MN_MODEL` was pinned could mix halves from
  two different backends — `MN_PROVIDER=ollama` over an Anthropic session kept ollama but
  adopted the session's `anthropic.claude-opus-5`, so the UI showed the session's model
  while requests went to `localhost:11434` asking for a Claude id (and the mirror case
  stranded a pinned local model on a hosted provider). Provider and model now restore as
  a pair, each half only when it fits the other, and the restored reasoning level clamps
  against the model that actually ends up active.
- Switching provider without also setting a model left the wire route naming the previous
  backend's API family, which the status bar and quota panel read; `config.SetLLMProvider`
  now re-derives the two together.

## [2.17.0] — 2026-08-07

### Added

- `MN_ANTHROPIC_CUSTOM_HEADERS` (falling back to `ANTHROPIC_CUSTOM_HEADERS`) adds extra
  headers to Anthropic-wire requests, so `MN_ANTHROPIC_BASE_URL` can point at a gateway
  that has auth of its own — HTTP Basic, a proxy token, ngrok's skip-warning header.
  Previously any such gateway answered 401 with no way to add a header. Pairs parse from
  newlines, semicolons or a single run-together line, and reach both the Messages POST
  and the `/v1/models` GET behind the auth check. The headers are scoped to the host of
  `MN_ANTHROPIC_BASE_URL` and never sent to `api.anthropic.com`, so gateway credentials
  cannot leak to another operator if the base URL is cleared or the provider switched.

## [2.16.0] — 2026-08-07

### Added

- **Meta Model API** as a provider (`/provider meta`), serving Meta's Muse Spark
  coding models over its OpenAI-compatible wire — the models behind Muse Code.
  Default model `muse-spark-1.2`, with its real prices and 1M-token context window,
  and `/effort` enabled since every tier reasons before answering. Authenticate with
  `MN_META_API_KEY`, or Meta's own `MODEL_API_KEY` / `META_API_KEY`; override the
  endpoint with `MN_META_BASE_URL`. The cheaper `muse-spark-1.2-contributor` tier is
  selectable with `/model` but never the default — it is rate-limited by a rolling
  token window and opts prompts into training. Muse Spark has no open weights, so
  local runtimes (Ollama, LM Studio) correctly refuse to claim it.

## [2.15.1] — 2026-08-07

### Fixed

- SQE Anthropic sessions no longer report themselves as plain **Anthropic**. The quota
  popover read the wire-family label from the status payload instead of the actual
  provider, so an SQE session whose quota fetch failed claimed "Anthropic exposes no
  usage endpoint" — the wrong backend, and untrue, since SQE Anthropic is the only
  provider that does report real quota. Three error messages in the shared
  Anthropic-wire client likewise hardcoded `MN_ANTHROPIC_API_KEY` (a variable that
  ranks *behind* `MN_SQE_ANTHROPIC_API_KEY`, so the advice appeared not to work) and
  named "the Anthropic API" when the endpoint is a LAN gateway.
- The documented fallback from `MN_SQE_ANTHROPIC_API_KEY` to `MN_ANTHROPIC_API_KEY`
  now applies per request, not only at startup — a key entered in the Persona panel
  sets the Anthropic field alone, so the fallback the config field and the panel card
  both promise had silently stopped holding after launch. The reverse never applies:
  an SQE credential is never sent to `api.anthropic.com`.

## [2.15.0] — 2026-08-07

### Added

- The Persona panel can add and remove **arbitrary environment variables**. Previously it
  only edited vars discovered from MCP servers, skills and sub-agents, and those names had
  to look like credentials — so a provider needing something like `FOO_BASE_URL` had no
  route in. Hand-typed names are validated for shape instead, and refused if they would
  hijack or break the connector (`PATH`, `HOME`, the `LD_*`/`DYLD_*` loader hooks,
  `MN_ENV_FILE`, …), since the panel is reachable by anyone who can reach the session.
  Added vars are saved to `~/.config/moony/.env` and come back as removable fields.
  Removals stage until Save, and names owned by a provider card can't be removed there.

## [2.14.2] — 2026-08-07

### Changed

- The background-jobs button (and the Jobs tab in the manage panel) uses a terminal
  glyph instead of a lightning bolt. Adding it also required extending the vendored
  Material Symbols subset with `terminal` (U+EB8E) — a codepoint that is mapped but
  absent from the subset font renders a blank box just as silently as an unmapped
  name. The regeneration command is now recorded beside the `@font-face`.

## [2.14.1] — 2026-08-07

### Fixed

- Credential env vars the Persona panel discovers from MCP servers, skills and
  sub-agents are now saved to `~/.config/moony/.env` like provider keys, instead of
  being applied with `os.Setenv` only — the panel used to accept a token, report
  "Saved", and lose it on the next restart. A typed provider field still wins if the
  same name appears on both a provider card and a discovered field, and a blank value
  keeps the stored one rather than deleting it.

## [2.14.0] — 2026-08-06

### Added

- **Real quota readings from 7 more providers.** The usage/quota display previously worked
  for exactly one provider (`sqe-anthropic`) and every other backend fell back to a local
  token-count estimate — so on OpenRouter, DeepSeek and friends there was simply no "used
  quota" to see. `Usage()` now dispatches to a per-provider adapter:
  - **OpenRouter** (`GET /api/v1/auth/key`) — spend **and** limit. The only provider that
    reports both, so it's also the only one where a percentage, and therefore
    `MN_QUOTA_BUDGET_PCT`, is meaningful.
  - **DeepSeek**, **Moonshot**, **Z.AI**, **DeepInfra**, **Venice** — prepaid credit
    remaining. Venice prefers USD over its VCU/DIEM units so the figure stays comparable
    with other providers.
  - **xAI** (`GET /v1/api-key`) — no money figure exists, but a **blocked or disabled key**
    is now reported, which is precisely what someone staring at a missing quota needs told.
  - Endpoint existence was verified by probing each provider directly; response field names
    come from documentation, so each parser accepts several spellings and reports "no
    reading" rather than guessing when the shape is unfamiliar.
  - Providers with no inference-key-readable endpoint are unchanged and now say so by name.
    OpenAI and Anthropic do have usage APIs, but they require an **admin** key rather than
    an inference key; Groq, Mistral, Together, Fireworks, NVIDIA, Hugging Face, Cerebras,
    Perplexity and Gemini expose nothing; local backends have no billing concept.

### Changed

- **Remaining credit and amount spent are now displayed as the different things they are.**
  A provider reporting spend against a cap shows `$12.50/$100.00`; a prepaid provider shows
  `$4.66 left`. Putting a remaining balance in the "spent" field would have told users the
  exact inverse of the truth, so it travels in its own field all the way through the LLM
  client, the bridge, the web wire and both UIs. A prepaid balance never fabricates a
  percentage, because there is no cap to divide by — and `$0.00 left` is reported as a real
  (urgent) reading rather than being mistaken for "nothing reported".
- The "no quota reading" tooltip now names the provider that can't report, instead of
  saying "this provider" while other backends visibly show a real figure.
- docs: lead the README with Highlights, then Quick start
- deploy: add k8s manifests, compose file, .dockerignore, relay healthcheck probe, and deployment docs
- docs: add real TUI and web UI screenshots to the README
- docs: rework the README — fix the misaligned hero demo, add a quick start, and make highlights skimmable
- docs: document /changes, jobs panel, blocking pre_tool hooks, quota fallback, image provider, and auto-update
- web: regenerate tailwind.css after the jobs-pill and restart-button markup

## [2.13.0] — 2026-08-06

### Added

- Each row in the background-jobs panel has a **Restart** button: it stops the job when
  it is still running, waits for the process to actually exit, then re-runs the same
  command and TTY mode as a new job. The re-run goes back through `POST /jobs`, so it
  faces the same approval prompt and destructive-command refusal as the original start.

### Changed

- Background jobs moved out of the turn pager into their own floating pill, left of the
  quick actions. The pager no longer lingers as an empty frame around the jobs button
  alone, and the running count stays visible when the pager hides itself. The one-off
  pager-width offset became `syncFloatingPills`, which lays the strip out right-to-left
  from measured widths so each pill can appear and resize independently.
- The quick-actions pill is icon-only; the selected preset's name now lives in its
  tooltip and accessible name, so the pill's width no longer varies with the preset.
- Scroll-to-bottom sits to the right of the pair navigator in the pager, with the
  divider flipped to match.
- The manage modal closes only via its X button — backdrop-click and Escape are gone, so
  a stray click can no longer discard a half-filled panel (a typed provider key, an
  edited skill).

### Fixed

- The quick-actions pill rendered 6.4px shorter than the turn pager beside it: it sized
  its halves to `.pager-btn` alone, but the pager also pads around that button, and under
  `box-sizing: border-box` that padding sits inside the halves' `min-height`. Both pills
  now derive from shared `--pager-*` tokens.

## [2.12.3] — 2026-08-06

### Fixed

- The background-jobs button and the Jobs tab in Config showed a question mark, and the
  pair pager's "previous" arrow did the same: `<app-icon>` falls back to a generic "help"
  glyph when a name is missing from its map or from the subset font, silently and with no
  console error. Mapped `bolt.horizontal.circle`, and added `chevron_left` (U+E5CB) to both
  the codepoint map and the vendored font. Unmapped icon names now fail the tests.
- Persona saves no longer fail with a 400 on every field.

## [2.12.2] — 2026-08-05

### Fixed

- Config's tab strip now scrolls the active tab into view. With twelve tabs the later
  ones (Jobs, Doctor) start off-screen behind a scrollbar most trackpads never show,
  so opening Jobs from the pager button rendered its body while the strip stayed
  parked on Skills — making the tab look like it didn't exist.

## [2.12.1] — 2026-08-05

### Fixed

- TTY job input: an embedded CR/LF submitted a line to the shell without vetting it,
  so a single `{"data": "rm -rf /\r"}` reached the terminal with the
  destructive-command check seeing only the empty remainder. Every line the shell
  will see is now checked.
- A write to a terminal the program had stopped draining outlived its deadline: the
  writer stayed parked and could deliver its bytes after the caller was told it
  failed, possibly behind a newer write. One write is now in flight per job at most,
  and the next call is refused rather than reordered.

## [2.12.0] — 2026-08-05

### Added

- Background jobs can run on a pseudo-terminal: `bash(background: true, tty: true)`,
  a **TTY** switch in the jobs panel, and `tty` on `POST /api/sessions/{id}/jobs`.
  Output stops being block-buffered (python, django, npm) and is stripped of escape
  codes before it reaches the log the model reads.
- A keyboard for TTY jobs in the jobs panel — Enter sends a line, `^C` interrupts —
  backed by `POST /api/sessions/{id}/jobs/{job}/input`. The destructive-command
  refusal that gates a job start also gates its input, applied to the assembled line
  so splitting a command across calls can't slip past it.
- A jobs button on the left of the pager pill, with a badge counting running jobs, so
  a dev server left running after its turn stays visible.

### Changed

- Killing a TTY job now takes the whole process group with it, instead of leaving a
  reloading dev server holding its port.
- web: restore the spend/quota pill in the status bar

### Fixed

- Windows build break in `internal/procutil` (missing `time` import).

## [2.11.0] — 2026-08-05

Closes the feature gaps against the sibling `sqeqct-coding-agent` codebase.

### Added

- **Background-jobs panel** (Config → Jobs). Lists the session's background jobs with a
  live status pill, tails their output, and stops them. The tools existed already; there
  was no way to see a job without asking the model to call `bash_output`.
  - The log tail reads by **absolute offset and never advances the cursor `bash_output`
    consumes**, so watching a job cannot starve the model of output it hasn't read.
    Having the panel open must not change what the agent knows.
  - Jobs can also be **started from the panel**, routed through the same gates as a
    model-issued `bash` call: destructive commands are refused outright (a command typed
    into a box carries none of the conversational context that could justify one), the
    approval policy is honoured, and `safe`/`auto` still confirm — a backgrounded process
    outlives the turn, and `hardApproval` commands must not slip through. Without this the
    endpoint would be an unauthenticated remote shell on a hosted relay.
  - Only one job approval at a time, and never during a turn: a turn owns the approval
    channel, so a second concurrent prompt could cross answers.
- **Per-provider image routing** (`MN_IMAGE_PROVIDER`, or the Persona panel). Few backends
  serve an image-capable chat route, so a session on a local or text-only model previously
  had no way to generate images at all. Empty inherits the session provider.
- **`pre_tool` hooks can now block a tool call** by exiting with code **77**. Previously
  hooks could observe a dangerous call but not stop it, which made them a logging
  side-channel rather than a security control. A distinct code — not "any non-zero" —
  because the shell already returns 2 for a syntax error and 127 for "command not found",
  so a hook with a typo would otherwise wedge every tool call.
- **`/changes`** (alias `/diff`) shows a combined diff of the current turn's file changes,
  in both the TUI and the web UI. The agent had computed this all along for self-review;
  nothing exposed it.
- **Unattended auto-update for the daemon** (`MN_AUTO_UPDATE`, default on). An always-on
  connector has nobody watching to run `/update`. Deferred while any session is mid-turn,
  and re-checked *after* the download — the relaunch drops in-flight work, and a download
  takes minutes during which a turn can start. A live terminal or browser session is never
  auto-updated; it still only shows a notice.

### Changed

- **Sessions now persist through a `session.Backend` interface.** The web server, agent and
  TUI depend on the interface rather than the concrete file store, so storage can change
  without touching any call site. The file store remains the only (and default) backend —
  no new dependencies, and the binary is unchanged in size.

### Fixed

- **A turn that overflows the model's context window now recovers instead of failing.** On
  a provider's "prompt is too long" rejection the agent adopts the provider's authoritative
  token count, trims stale tool output (cheap, and it preserves the prompt cache), then
  summarizes if needed, and re-runs the round. Previously the turn just failed with a raw
  provider 400 and the user had to run `/compact` by hand. Bounded to two attempts, and a
  conversation that genuinely cannot fit still fails — now with guidance naming roomier
  models instead of the provider's blob.
- **Sub-agents ignored a sandbox enabled at runtime.** `/sandbox` and the web UI set the
  sandbox on the parent's tool registry only; a sub-agent got a fresh registry that fell
  back to the environment, so enabling the sandbox left every sub-agent unconfined while
  the user believed otherwise. Sub-agents auto-run their tools with no approval prompt, so
  that setting is their only protection.
- **Sub-agents ran outside the parent's hooks**, so a `pre_tool` policy installed to audit
  or block dangerous calls saw nothing a sub-agent did — the widest hole in a hook-based
  policy, for the same reason.
- **`/compact` left the persisted transcript anchors stale.** They are message-count
  offsets and compaction shortens the history, so footers and notes pointed at the wrong
  messages (or past the end) on replay. Both compaction paths now share one implementation.
- **A background job's output buffer is now bounded** (8 MB, oldest-first, both cursors
  rebased). It lived in memory for the process's whole life, so one `yes` or
  `cat /dev/urandom` backgrounded once would grow it until the host OOM-killed the agent.
- **A UI-approved background job no longer wedges the session.** Approving set the session
  busy with no turn loop to hand it back to idle, blocking every later turn and job until
  a restart.

## [2.10.0] — 2026-08-05

### Added

- **Quota-based automatic provider fallback.** When the current backend's spend crosses a
  budget — or the provider refuses with a hard quota error — the agent now switches to the
  next backend in a configured chain instead of failing for the rest of the period, and
  switches back once the quota resets. Off by default: silently moving traffic to another
  backend (different models, different billing) must be opt-in. Configure it in the
  Persona panel or via `MN_QUOTA_FALLBACK`, `MN_PROVIDER_FALLBACKS`,
  `MN_QUOTA_BUDGET_USD`, `MN_QUOTA_BUDGET_PCT` and `MN_QUOTA_RESET_AT`.
  - The chain is an ordered spec like `anthropic,openai:gpt-4o,ollama:qwen3-coder:30b:high`.
    A hop may pin its own model and reasoning level; a bare provider uses that backend's
    default, because **model ids are not portable across backends** — so every switch
    changes the model too, or the new backend would reject the old id.
  - Hops **skip backends that aren't usable** on this machine (no API key, no base URL),
    since hopping to a keyless provider would turn "near your quota" into "every turn
    fails". The check reuses the streaming path's own credential resolution rather than
    re-listing which provider needs which key.
  - Typo'd provider ids are dropped rather than becoming live switch targets.
  - The walk **never wraps around**: cycling back to an exhausted provider would thrash.
  - The state is persisted on the session, so a hop — and its scheduled return to the
    primary — survives a restart instead of looking like a deliberate user choice.
  - The restore only ever reverts the fallback's **own** change: pick a provider by hand
    afterwards and that choice wins. It also replays the primary's reasoning level, so a
    hop's pinned level doesn't stick after returning home.
  - Both the reason for the switch and the return are written into the transcript, shown
    as an amber provider pill in the composer (with the reset time in its tooltip), and
    reported in the status snapshot for terminal-mirrored clients too.

### Fixed

- **Hard provider quota errors are no longer retried as if transient.** A spent-out
  account and an ordinary rate limit both arrive as HTTP 429; the client now tells them
  apart and fails fast with the provider's own message instead of burning the retry budget
  and reporting a confusing "after N attempts". Detection is deliberately conservative —
  per-minute/"try again in" throttles stay retryable — because a false positive would
  silently reroute a user's traffic for the rest of the period.
- **Provider error bodies are redacted before being shown or persisted.** Some providers
  echo the API key back in an error message, and that text is surfaced to the user *and*
  written to the searchable transcript, so key-shaped tokens are now masked.

## [2.9.0] — 2026-08-04

### Added

- **Quick-action editor: pick icons by sight instead of typing symbol names.** The icon
  field was a free-text box, so setting one meant knowing an exact symbol name from
  memory — and a wrong guess was *silent*, because `<app-icon>` falls back to a generic
  "help" glyph with no error. It's now a dropdown of 51 previewed icons in 10 task-shaped
  groups (Build & run, Test & verify, Source control, …), with an explicit "No icon"
  choice, keyboard/outside-click dismissal, and flip-up placement when the viewport is
  short. The catalog is curated for moony specifically: this app renders **Material
  Symbols** through a name-mapping table, not SF Symbols, so every entry is checked to
  exist in that table — `TestQuickActionIconsAreMapped` fails the build on an unmapped or
  duplicated name, and a companion test fails if any picker class lacks a CSS rule.
  (Adapting rather than copying mattered: 7 of the icons a direct port would have offered
  are unmapped here and would have rendered as broken glyphs.)

### Fixed

- **The quick-action form was unlabelled while editing.** Fields carried only
  placeholders, which disappear as soon as there's text — so editing an existing action
  presented four anonymous boxes and "which one is the subtitle?" needed guessing. Each
  control now has a persistent label plus a hint (`required — sent to the agent`,
  `defaults to the prompt`). The form also gained a mode heading naming the action being
  edited; previously the only cue that you were about to *overwrite* rather than *add*
  was the submit button's text changing to "Save".

## [2.8.1] — 2026-08-04

### Fixed

- **The codebase map described JS modules by their private locals instead of their
  public API.** `codebase_map` already shipped, but with v2.8.0's JS indexing in place
  the map's per-file listings were dominated by closures declared *inside* other
  functions — `app-core.js` was advertised via `flush`, `commit`, `mutate` and `tick`
  (40 nested helpers against only 7 module-level bindings), none of which are reachable
  from outside their enclosing function. Definitions nested below a file's base
  declaration level are now pruned, so each file is summarized by the entry points a
  caller can actually use (`api`, `handle`, `sendText`, `boot`, …).

  Two deliberate constraints: the base level is **detected per file** rather than assumed
  to be column 0, because an IIFE-wrapped module (`app-workflow.js`) holds its entire
  surface at one indent and a hardcoded column-0 rule would erase it from the map
  outright — it keeps all 98 of its symbols. And pruning is limited to JS/TS: Go and
  Rust nest functions too, but their top-level declarations dominate, so applying it
  there would strip legitimate symbols. Both constraints are covered by tests, each
  verified to fail when the corresponding guard is removed.

## [2.8.0] — 2026-08-04

Ports the outstanding fixes from the upstream sibling project. All four were latent
in moony and are verified by regression tests (each confirmed to fail without its fix).

### Fixed

- **The symbol map barely saw the web UI, and erased whole files.** Three compounding
  defects: `defKeywordRe` listed Go's `func`, which does *not* match JavaScript's
  `function`, so top-level JS functions were never indexed — `app-core.js` has 161 and
  the map found 5. What it listed instead were the value bindings `assignFuncRe` swept
  up (`const now = Date.now()`), so the largest frontend file was described by its
  throwaway locals; that regex is now restricted to function-valued bindings, with a
  separate `exportRe` for named exports. CSS was excluded entirely (0 symbols from a
  2000-line stylesheet), so `.css` now gets a dedicated pass indexing top-level rules
  and custom properties, and JS/HTML that names a class registers as a *reference* to
  it — `#` is an id selector in CSS, not a comment, and was silently dropping every id
  rule. Finally the global `entries[:1500]` cut was score-ordered, so flat files whose
  names individually score low were deleted wholesale; a per-file floor now guarantees
  each contributing file keeps its best few, and the cap is 6000 (only `CODEBASE_MAP.md`
  and `find_files` read the full set — the prompt takes only its digest, so this costs
  disk, not context). Net effect on this repo: 281 → 561 files represented,
  `app-core.js` 5 → 231 symbols, `app.css` 0 → 522.
- **Nested `.gitignore` files were ignored.** Only the repo-root ignore file was read,
  so a vendored or generated subtree excluded by its own nested `.gitignore` was still
  walked and indexed as project source, crowding real files out of the symbol map's
  budget. Nested files are now honoured and, as git does, *scoped to their own
  subtree*, so `editors/vscode/.gitignore`'s `out/` can't hide `internal/web/out/`.
  The scope dir is deliberately never matched against its own rules — a catch-all `*`
  would otherwise hide the very subtree the file lives in and prune it entirely.
  Separately, a directory-only rule (`build/`) now also hides the files *underneath*
  it: skipping non-dirs only works for callers that walk top-down and prune, so a tool
  asking about one path directly was told a file inside an ignored directory was visible.
- **Unmapped events rendered as the literal string `undefined` in the transcript.**
  Both `toWire` and `mirrorWire` returned a text-less `info` event for any kind they
  didn't recognize, and the browser builds an info bubble from `ev.text` — so the
  bubble read `undefined`, and in moony's client it *threw* on `ev.text.startsWith`,
  taking the whole event handler down with it. Both now return an inert `unknown` type
  that the client drops, with a defensive text guard behind it. This was live, not
  theoretical: `agent.Compaction` is emitted by the compactor and had no case, so it
  hit the default branch every time context was compacted; it now surfaces as a proper
  note explaining why earlier turns vanished.
- **~18 Tailwind utility classes did nothing.** The generator's `content` globs listed
  `static/app.js` — a file that no longer exists, having been split into the five
  `app-*.js` modules — so it matched nothing and every class used only in JS was absent
  from the vendored stylesheet. `w-full`, `font-medium`, `mt-3`, `break-all`,
  `focus:ring-1` and others were present in the markup with no rule behind them (the
  session-rename input, for one, was meant to be full-width with a focus ring and got
  neither). The config now globs `*.html`/`*.js`, the missing `--c-backdrop` token is
  defined for both themes, and the regenerated CSS drops 29 genuinely unused classes.
  Two guards so this can't rot again: `TAILWIND` prefers a local v3 binary and falls
  back to a *pinned* `npx tailwindcss@3` (the v4 CLI silently ignores this v3-style
  config and emits an almost-empty stylesheet), and a new `make tailwind-check` fails
  when the committed CSS is stale.

## [2.7.1] — 2026-07-31

### Fixed

- **`bash` ignored its timeout and interrupts.** The foreground `bash` tool ran `sh -c` without a process group, so cancelling killed only the shell — the actual work kept running, and because a surviving grandchild inherits the output pipe, the tool blocked until the command finished on its own. A `timeout_sec: 1` call that spawned a background child took 30 seconds to return, and interrupting a turn didn't stop the command. The whole process tree is now killed (shared `procutil.KillTree`: process group + group signal + a `WaitDelay` backstop), which also fixes the same latent bug in the verify runner and lifecycle hooks. `kill(-pid)` is now guarded against a zero/absurd pid, which would have signalled the agent's *own* process group.
- **Data race on the registry root.** `/cd` replaces the path-sandbox root and ignore set in place, while the symbol-map and repo-map builders read them from background goroutines that outlive the tool call that started them. Both are now behind a read/write lock, and the whole-tree walks read them once per walk instead of once per file — cheaper, and every path in a walk now resolves against one consistent root. The reset-able `sync.Once` guarding the repo-map memo was also replaced: resetting a `sync.Once` is itself a race against a concurrent `Do`.
- **Data race on the cached system prompt**, plus `/cd` mid-turn. The prompt is rebuilt at runtime (MCP connect, a `remember`, a background map refresh) while a turn reads it to build a request; it is now lock-guarded, and concurrent rebuilds are serialized so a slower older build can't clobber a newer prompt. `/cd` is now refused while a turn is running — re-rooting swapped the sandbox, ignore set, skills and permissions out from under in-flight tool calls — and the web path re-roots before persisting, so a refusal no longer leaves the saved session pointing somewhere the live agent isn't.

## [2.7.0] — 2026-07-31

### Added

- `remember_user`: durable facts about **you** — coding style, preferred output format, tone, workflow habits, standing requests — saved to `~/.config/moony/USER.md` and injected into every session in every project. Complements `remember`, which stays project-scoped (`AGENTS.md`): the split is *who you are* vs *what this repo does*. `/memory --user` writes the same file, `recall` searches all three scopes, and the context breakdown reports the profile as its own segment.
- Docs for everything shipped in 2.5.0 and 2.6.0: the 10 new tools (`semantic_search`, `codebase_map`, `find_files`, `code_nav`, `review_changes`, `pull_request`, `ci_status`, `mobile`, `generate_image`, `generate_video`), the web-only features (quick actions, Task Board, file viewer, transcript search, Doctor, codebase-map panel, `/healthz` + `/metrics`), and the new environment variables.

### Fixed

- **Data race in the tool registry.** The tool map and ordering slice are mutated at runtime — an MCP reload, a skills toggle, or a sub-agent change re-registers tools — while a turn concurrently reads them for the next request's schemas or to dispatch the current call. A map write racing a map read is a hard crash, not a stale read. All accesses now go through an `RWMutex`, with dispatch resolving the tool under a read lock and releasing it before running the (potentially long) call.
- Tests no longer spawn real MCP subprocesses. `MN_MCP=0` was set per-test, so a test that built a server directly inherited the ambient environment and launched real servers that wrote into its temp `HOME` — and kept writing after the test returned, making `t.TempDir` cleanup fail in whichever test happened to be running next. It is now pinned for the whole test binary.
- `remember`'s `category` accepts `style` and `format` (filed under their own headings) instead of silently falling back to keyword heuristics.

## [2.6.0] — 2026-07-30

### Added

- Read-only file browser/viewer (`GET /api/sessions/{id}/files`, `/file`) reachable from the composer, sandboxed to the project root and ignore-aware.
- Cross-session transcript search (`GET /api/search`) in the sidebar: fans out to every connected machine and merges the hits, so "where did we discuss X" is one query instead of reopening sessions.
- Doctor panel (`GET /api/sessions/{id}/doctor`): the `/doctor` checks plus live probes that only a running session can do — an embeddings smoke test and the actual connected state of LSP + MCP servers. New `doctor.CheckEmbeddings` verifies `MN_EMBEDDINGS_MODEL` with a real request rather than just its presence.
- Codebase map panel (`GET/POST/PUT /api/sessions/{id}/codebasemap`): view `CODEBASE_MAP.md`, regenerate its structural section, and edit the hand-authored pinned notes without messaging the agent.
- `GET /healthz` (liveness JSON) and `GET /metrics` (Prometheus text) on the connector, with request/session/SSE counters and an opt-in access log (`MN_ACCESS_LOG`). Route labels collapse ids, so session ids never reach a metric label.
- Stateless fill-in-the-middle completion endpoint (`POST /api/complete`) for editor clients, served by the fast model.
- Verify-command auto-detection (`MN_VERIFY_AUTO=1`): infers a project's check command (Makefile target, `go.mod`, npm test script, Cargo, pytest, …). Opt-in, and an explicit `MN_VERIFY_CMD` always wins.
- Paged transcript history (`GET /api/sessions/{id}/history`).

### Changed

- Opening a session now replays only its most recent turns instead of streaming the entire saved transcript; a banner offers the full history on demand. `?turns=` is bounded so one request can't ask the server to serialize an arbitrary slice.
- A session now remembers the model, provider and reasoning level it last used and reopens on them, instead of silently reverting to the global default. An explicit `-model`/`-provider`/`-reasoning` flag or env var still wins.
- Editing a queued message (Esc-pop) keeps the per-turn model pin it was queued with, and a quick action's pin is validated before the turn is accepted — an unknown provider or reasoning level is now a 400 rather than a silent misroute.

### Fixed

- Compaction now remaps the persisted per-turn anchors (turn footers, board messages, info notes) it invalidates. Previously these went stale on the next save, so a reopened session anchored footers and notes to the wrong messages — visibly out-of-order timestamps in the transcript.
- A plan draft the user moved past is no longer re-proposed every turn from the model's own stale history, and an auto-entered plan mode whose draft was ignored reverts to build mode instead of stranding the session read-only.
- An empty model response (no text, no tool call) is retried with a nudge instead of silently ending the turn with no explanation.
- Leaked task-isolation worktrees from runs killed before cleanup are now swept; live worktrees are protected by a git lock so a long sub-agent run can't have its tree removed underneath it.
- Per-session goroutines (MCP connect, turn loop) are now evicted when the server is torn down or a session is deleted, so nothing keeps running against a stale working directory.

## [2.5.0] — 2026-07-30

### Added

- `semantic_search`: embedding-based code search over a local per-workspace vector index (`internal/rag`, stored at `.sqe/rag/index.gob`; `MN_RAG_INDEX_DIR` overrides). Finds code by meaning where grep needs the exact string.
- `codebase_map`: a maintained `CODEBASE_MAP.md` (pinned notes + auto-refreshed structural section + feature→files narrative) plus a symbol/caller graph, injected into the system prompt so the model consults the map instead of re-exploring the tree each turn. `MN_CODEBASE_MAP`, `MN_CODEBASE_MAP_BYTES`.
- `find_files`: ranked relevant-file shortlist built from the symbol map and cross-file caller graph.
- `code_nav`: compiler-grade definition/references/hover resolved through a running language server, disambiguating overloads and shadowed names that the regex-based `find_symbol` cannot.
- `review_changes`: read-only tool returning one combined unified diff of everything the current turn changed.
- `pull_request` and `ci_status`: open/view/comment on PRs and report check status with failure logs, via the `gh` CLI.
- `mobile`: drive an iOS simulator or Android emulator — boot, install, launch, tap/swipe/type, logs and screenshots.
- `generate_image` and `generate_video`: image generation/editing on any OpenAI-compatible route (`MN_IMAGE_MODEL`) and Veo video generation on the native Gemini route (`MN_VIDEO_MODEL`). Tool-produced videos render inline in the web UI.
- Web quick actions: user-editable preset prompts behind a split button beside the turn pager, each able to pin a model, provider or reasoning level for its own turn only. Project-scoped `.sqe/quickactions.json` (committable) with a global fallback; edit them in Manage → Quick actions.
- Web Task Board: queue prompts that run as parallel headless sub-processes, each in its own throwaway git worktree, with per-task diff review and Apply/Discard. `MN_TASKBOARD_MAX_PARALLEL`.

### Changed

- An answer cut off by the output-length cap is now continued instead of abandoned; a response that burned its whole budget on reasoning without acting is retried with a terser nudge (bounded, and counted as friction so effort ramp-up still applies).

## [2.4.0] — 2026-07-30

### Added

- agent: contextual destructive-command guard (`git reset --hard`, `rm -r`, `terraform destroy`, DB drops, …) — forced through human approval unless the *current* user turn plainly asked for it, with a parent-session approval bridge for sub-agents
- agent: isolated-worktree shell escape guard — an `isolate:true` sub-agent's bash can no longer reach the real checkout (absolute paths, `cd ..`, `git -C`, substitutions)
- agent: indirect-prompt-injection hardening — sub-agent system prompt guard plus `<untrusted-content>` framing (boundary-token escaping) of file/web/shell tool output
- agent: background sub-agents — `task(background:true)` plus `task_output` / `task_status` / `task_cancel` tools over a new concurrency-safe task store, reaped on shutdown
- agent: cross-turn model routing — a repeated ask (lexical overlap) or a correction/retry ("still broken", "try again") escalates the turn to the strong model instead of silently repeating a cheap pass
- agent: smart self-review gating — skips the review model call when a turn only changed docs or the `const Version` bump, still reviewing when real source is mixed in
- agent: stale-todo notice at end of a file-changing turn, and opt-in auto-test suggestions (`MN_AUTO_TESTGEN`, suggest-only)
- config: `SubagentMaxDepth` (default 2, `MN_MAX_SUBAGENT_DEPTH`, clamped to `SubagentDepthCeiling`), `AutoTestGen`/`AutoTestGenMax`, and `FastModelResolved`/`AuxModelResolved` cross-provider model resolution

### Changed

- llm: `ModelServableBy` now rejects only clearly cross-family model pins (claude/gpt/gemini/open-weight) instead of accepting any non-empty id, so stale persona pins degrade to "inherit" rather than a guaranteed API error

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
