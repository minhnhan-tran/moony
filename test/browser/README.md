# Browser simulation bot

A Playwright bot that **simulates a real human** driving the moony web UI
in a real browser — typing messages, answering `ask_user` prompts (single **and**
the `questions[]` tabs), and approving/denying tool actions. Two "brains" ship: a
**scripted** persona (deterministic, no user-side tokens, with concrete
assertions) and an **LLM** persona (realistic, exploratory).

## How it works

```
*.spec.ts ──▶ runConversation(sqe, decider) ──drives──▶ real browser ──▶ web UI (your stack)
                     │
   Decider ─────────┤  scripted  → canned decisions (deterministic, no model)
                     └  LLM       → ../../bin/sim-decide (Go) → internal/llm → Claude (Floodgate/SQE Proxy)
```

- **Hands** — `src/sqe.ts` (page object) drives the actual DOM the app ships:
  `#input`/`#send`, the `#approval` panel (`[data-d="approve|deny"]`), the
  `#question` panel (single question **or** the `.app-question-tab` tabs), and
  `#transcript` messages (incl. `.file-card` tool cards and `.msg-error` banners
  for assertions).
- **Loop** — `src/driver.ts` `runConversation()` is shared by both modes: open a
  session, send the opening message, then wait for the agent to pause
  (question/approval) or settle and let the `Decider` choose each response.
- **Brains** (`src/decider.ts`) —
  - **scripted** (`scriptedDecider`): a fixed list of messages + policies for
    questions/approvals. No model, deterministic → good for assertable regression.
  - **LLM** (`llmDecider`): role-plays the user via `../../bin/sim-decide` (Go). It
    lives in Go on purpose — it reuses `internal/llm`, so Claude auth is already
    solved; Node never authenticates to the model.

## Two modes

| | Scripted (`journey.spec.ts`) | LLM persona (`persona.spec.ts`) |
|---|---|---|
| User decisions | canned, deterministic | a Claude model role-playing a user |
| Reproducible? | yes (user side) | no |
| User-side tokens | none | yes (persona model) |
| Best for | assertable regression / CI | realistic exploration / soak |
| Agent | real (real tokens either way) | real |

> Both drive the **real** agent, so the agent's work costs tokens and its exact
> tool choices vary. Scripted mode fixes the *user* side and adds outcome-based
> assertions that tolerate that variation.


## Setup

```bash
# 1. (LLM mode only) build the persona brain — from the repo root
make sim-decide            # → ./bin/sim-decide  (scripted mode doesn't need this)

# 2. Install the harness (from this dir)
cd test/browser
npm install
npx playwright install chromium
```

## Run

Point it at your running stack and go:

```bash
# Scripted journey with assertions — deterministic user, no persona model.
# Set SQE_WORKDIR to the session's working dir to also assert the file on disk.
SQE_BASE_URL="https://localhost.apple.com:448" SQE_WORKDIR="/path/to/your/repo" npm run test:journey

# LLM persona (exploratory), headed so you can watch it.
SQE_BASE_URL="https://localhost.apple.com:448" HEADED=1 npm run test:persona

npm test                   # run everything (both specs)
npm run report             # open the HTML report (trace/video on failure)
```

### Configuration

| Env | Applies to | Default | Meaning |
|-----|-----------|---------|---------|
| `SQE_BASE_URL` | both | `https://localhost.apple.com:448` | Where the SPA is served |
| `SQE_STORAGE_STATE` | both | _(unset)_ | Playwright storage state for auth (see below) |
| `HEADED` | both | _(unset)_ | Set to run with a visible browser |
| `SQE_WORKDIR` | journey | _(unset)_ | Session working dir; enables the on-disk file assertion |
| `SIM_PERSONA` | persona | pragmatic backend engineer | The user's character (system prompt) |
| `SIM_GOAL` | persona | add a `GET /health` endpoint + test | What the user is trying to achieve |
| `SIM_MAX_STEPS` | persona | `10` | Safety cap on interactions (don't chat forever) |
| `SIM_DECIDE_BIN` | persona | `../../bin/sim-decide` | Path to the Go decider |

The persona model defaults to `config.Model`; point the decider at a cheaper one
with `./bin/sim-decide -model claude-haiku-4-5` (wire via a wrapper if you want it
in `SIM_DECIDE_BIN`).

**Add your own journeys:** copy `tests/journey.spec.ts`, give `scriptedDecider` a
new `Script` (`messages`, optional `answer`/`approve` policies), and assert on
`sqe.toolCards()` / `sqe.errors()` / the returned transcript (and files via
`SQE_WORKDIR`).

## Auth

`SQE_AUTH` is **off by default** in the web server, so a **loopback / single-user
instance needs no login** — just point `SQE_BASE_URL` at it.

For an auth-gated target (`SQE_AUTH=1`, e.g. a shared relay), capture a login once
and replay it:

```bash
npm run login                       # opens a browser; log in via AppleConnect, then close
SQE_STORAGE_STATE=.auth/state.json SQE_BASE_URL="https://…" npm test
```

`.auth/` and `*.storageState.json` are git-ignored — never commit them.

## Notes & limits

- **Real agent, real cost, non-deterministic work.** Both modes drive the actual
  agent (tokens $) and its exact tool use varies. The scripted journey's
  assertions are outcome-based (a write tool touched the marker; no error) to stay
  robust to that; strengthen them with `SQE_WORKDIR` (on-disk check) or your own.
- **`journey.spec.ts` ships real assertions** (no error banner, a file-write on the
  marker, the agent replied, and — with `SQE_WORKDIR` — the file on disk). The LLM
  `persona.spec.ts` only asserts the conversation runs; add goal-specific checks.
- `openSession()` calls the SPA's top-level `newSession()`; if your build renames
  it, adjust `src/sqe.ts` (or click your "new session" control there instead).
- This lives outside the Go module (its own `package.json`) — `make test` / `go`
  don't touch it.
