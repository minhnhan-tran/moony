# Web UI & Relay

[← Back to README](../README.md)

## Web UI (experimental)

`moony` has a browser UI, served through a **relay**. The relay fronts the web app; the terminal **dials out** to it and serves this machine's projects and sessions over that connection — so a browser pointed at the relay browses the whole session tree *and* drives the live session you're typing in (messages, approvals, and interrupts stay in sync). The agent and the on-disk session store stay on your machine; the relay holds no agent and no session state. It has no built-in auth — front it with your own access control if you expose it (see *Authentication*).

```bash
make relay                               # terminal 1 — the web UI on http://127.0.0.1:8770
MN_RELAY=127.0.0.1:8771 moony  # terminal 2 — connect this machine to the relay
# open http://127.0.0.1:8770
```

Save the relay address so every launch auto-connects in the background: run `/relay host:port` in the TUI (persisted to your config); `/relay off` disables it, `/relay` shows the current state. `MN_RELAY` overrides the saved value for a single run. While a relay is configured the status bar shows a **relay pill** — green when connected, amber while connecting/retrying — and the same state is mirrored to the web header.

The browser replays each conversation so a viewer can open the page mid-session and catch up, then follow live. To keep a long transcript from streaming and rendering in full on connect, only the most recent turns are replayed; when older turns exist a **"Load full history"** banner appears above the transcript, and `GET /api/sessions/{id}/history` serves earlier turns as pages. The terminal stays the single owner of the agent, so remote actions are delivered into its normal input loop — one conversation, one history, one approval at a time. See *Relay mode* for the multi-laptop tree, and *Authentication* for login + HTTPS.

### Web-only features

Beyond mirroring the terminal, the browser UI adds a few things that only exist there:

- **Quick actions** — a split button beside the turn pager that fires a preset prompt at the agent without typing it ("run the tests", "review my changes"). Each action can pin a **model, provider, or reasoning level for its own turn only**; the session reverts afterwards, and the transcript says where the turn ran. Presets live in a committable per-project `.sqe/quickactions.json` with a global fallback, and are edited in *Manage → Quick actions* (reorder to pick the button's default).
- **Task Board** (*Manage → Task Board*) — queue prompts that run as **parallel headless sub-processes, each in its own throwaway git worktree**, so they can't clobber each other or your working tree. Tasks may depend on one another, and each finished task is reviewed as a diff you **Apply** or **Discard**. `MN_TASKBOARD_MAX_PARALLEL` bounds concurrency; worktrees are torn down when the session closes.
- **Background jobs** (*Manage → Jobs*) — see the session's long-running background commands (anything started with the `bash` tool's `background` flag), tail their output live, send keystrokes to a job running under a **TTY** (line-buffered output plus a keyboard, for `python`, `django`, `npm` and friends that prompt), and stop or restart them. Log tailing reads by absolute offset and **never consumes the output the model has yet to read**, so having the panel open can't change what the agent sees; a job's buffer is capped and drops its oldest output, so a runaway command can't exhaust memory. You can also start a job from the panel: those go through the same gates as a model-issued `bash` call — destructive commands are refused outright, and the approval policy still applies — since a session on a hosted relay is reachable by other members.
- **File viewer** — a read-only, ignore-aware browser over the session's working tree (the document icon in the composer meta row), sandboxed to the project root.
- **Transcript search** — the sidebar search box queries every session on **every connected machine** and merges the hits, so "where did we discuss X" is one query instead of reopening sessions one by one.
- **Doctor** (*Manage → Doctor*) — the `/doctor` environment/config checks plus live probes only a running session can do: an embeddings smoke test (a real request, not just "is it configured") and the actual connected state of your LSP and MCP servers.
- **Codebase map** (*Manage → Codebase map*) — view `CODEBASE_MAP.md`, regenerate its generated structure section, and edit the hand-authored pinned notes, without spending a turn asking the agent to do it.

### Health and metrics

The connector exposes two operational endpoints:

- `GET /healthz` — liveness JSON: `status`, the build `version`, and `uptimeSeconds`.
- `GET /metrics` — Prometheus text exposition: uptime, in-flight and total requests (by route, method and status), request duration, active/total SSE streams, and counters for capped replays and history pages served. Set `MN_ACCESS_LOG=1` to also log one line per request.

Route labels collapse opaque path segments (a session id becomes `:id`), so ids never become metric labels and the cardinality stays bounded. Neither endpoint exposes transcript content. `/healthz` is reachable without a login so probes work; `/metrics` goes through the normal auth gate like any other path (see *Authentication*).

## Relay mode

A **relay** fronts the web UI while the agent stays on your machine. One or more connectors (your **laptops**) dial **out** to the relay, each announcing its host and serving its local web app over that connection. The relay serves the SPA shell itself, lists the connected laptops at `GET /api/laptops`, and routes `/l/{host}/…` browser requests to that laptop's connector — so the sidebar is a **laptop → project → sessions** tree: pick a laptop to drive its **local** projects, sessions, and files. The relay holds no agent and no session state. This is the building block for hosting the UI elsewhere (e.g. behind an ingress) without moving your code or credentials.

```bash
make relay                               # terminal 1 — web on http://127.0.0.1:8770, connectors dial :8771
MN_RELAY=127.0.0.1:8771 moony  # terminal 2 — connect THIS machine to the relay
# open http://127.0.0.1:8770
```

`make relay ADDR=… TUNNEL=…` overrides the relay's browser/connector addresses. The terminal connects with `MN_RELAY=host:port` (or a saved `/relay`), serving the full multi-project app over the relay while the TUI keeps running; it auto-reconnects.

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

It registers under the local OS user that installed it, so the relay's per-user connector namespacing applies. With no `--relay`, it defaults to the built-in relay tunnel; pass `--relay host:port` (or set the saved `/relay` / `MN_RELAY`) to point it elsewhere. macOS logs go to `~/Library/Logs/moony/daemon.log`; Linux logs to the user journal (`journalctl --user -u moony`). The daemon serves the full multi-project app (each browser-opened session runs its own agent, store-backed) — it's the connector half without the live in-terminal mirror.

**Auto-install on `curl … | install.sh | sh`.** The installer always installs the daemon — with the relay's advertised tunnel (`-tunnel-public` / `MN_RELAY_TUNNEL`) when set, else the built-in default — **and** saves that relay as the interactive default, so a hand-run `moony` connects to the same relay. Since the relay keeps only one connector per machine, the TUI claims it while open (its live in-terminal session is the drivable one) and the **daemon stands down and resumes when you quit** (coordinated via a `connector.lock` with a stale-pid check). To start a session anywhere on a machine, click **New session** under a project in the web sidebar — it opens a folder picker (rooted at that project), so you can start there or browse to any directory.

## Authentication

By default `moony` has **no built-in authentication** — the web UI and relay
serve openly. Bind to loopback for single-user local use, and if you expose the
relay beyond localhost either enable the built-in OIDC login (below) or front it
with your **own** access control (an SSO-terminating reverse proxy, a
firewall/VPN). Exposing it unauthenticated on a routable address hands full
control of the agent (shell + file edits) to anyone who can reach it.

**Built-in OIDC login (opt-in).** Set `MN_AUTH=on` on the `moony relay`
process and give it your OIDC provider's details, and the relay gates the web UI
behind a per-user login and turns on real per-user isolation — each user sees
and can reach only their **own** machines. Required env (the relay fails to start
if any is missing — fail closed):

```
MN_AUTH=on
MN_AUTH_SECRET=<random, ≥16 bytes>          # signs session cookies + connector tokens
MN_OIDC_ISSUER=https://accounts.google.com  # your IdP's issuer URL (discovery)
MN_OIDC_CLIENT_ID=…
MN_OIDC_CLIENT_SECRET=…
MN_OIDC_REDIRECT_URL=https://relay.example.com/auth/callback
# optional: MN_OIDC_ACCOUNT_CLAIM=email  MN_AUTH_TTL=12h  MN_AUTH_ADMINS=you@example.com
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
export MN_RELAY_TOKEN=<token from /connect>
moony --relay relay.example.com:8771      # or set it in the daemon's service env
```

The relay verifies the token and derives the **trusted** owner from it (ignoring
the self-declared OS username), so the browser account and its machines line up
and isolation holds end to end. With `MN_AUTH` unset/off, connectors register
with their OS username and every connector is visible (the trusted/local case).

#### Keeping secrets out of the repo (and out of the agent)

Don't `export` secrets (e.g. a provider API key like `MN_OPENAI_API_KEY` or `MN_OLLAMA_TOKEN`) by hand — they land in shell
history and the process environment. Instead:

- **Local dev — a `.env` file.** Copy `.env.example` to `.env` (git-ignored) and fill it in; it's loaded at startup for any variable that isn't already set. For stronger isolation keep it outside the repo: `export MN_ENV_FILE=~/.config/moony/.env`. Load order is `$MN_ENV_FILE` → `~/.config/moony/.env` → `./.env`.
- **Prod — a secret store.** Inject the same variables via env or a mounted file. Real environment variables always win over `.env`.

The `bash` and background-job tools **strip these secrets** (every provider API key — `MN_OPENAI_API_KEY`, `MN_ANTHROPIC_API_KEY`, `MN_GEMINI_API_KEY`, etc. and their conventional fallbacks — plus `MN_OLLAMA_TOKEN` and `MN_ENV_FILE`) from the environment of commands the agent runs, so the model can't read them via `$VAR`/`env`. (A `.env` in the project root is still readable by the agent's shell by path — prefer `MN_ENV_FILE` outside the tree for anything truly sensitive.)

#### Serving the relay over HTTPS

TLS is optional. Pass a cert/key with `-tls-cert`/`-tls-key` (or
`MN_RELAY_TLS_CERT`/`MN_RELAY_TLS_KEY`) and the relay serves HTTPS on its
`-addr`; otherwise it serves plain HTTP. Privileged ports (`<1024`) need `sudo`.

```bash
# Self-signed cert for local testing (use a real cert in production):
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout key.pem -out cert.pem \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost"

make build-relay                 # -> bin/relay
MN_RELAY_TLS_CERT=cert.pem MN_RELAY_TLS_KEY=key.pem bin/relay -addr localhost:8443
# then connect this machine's terminal:  MN_RELAY=127.0.0.1:8771 moony
```

Only the **browser→relay** hop is TLS; the connector tunnel (`:8771`) stays plain TCP on loopback.






