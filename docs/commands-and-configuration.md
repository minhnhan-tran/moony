# Commands & Configuration

[← Back to README](../README.md)

## In-app commands & keys
- `/help` — list commands and keys
- `/init` — bootstrap an `AGENTS.md` by having the model survey the repo
- `/attach <path>` — stage an image, PDF, or Office file (docx/xlsx/pptx) for your next message
- `/paste` — stage an image from the macOS clipboard for your next message
- `/plan` — draft a first-class, approvable **plan artifact** (and switch to read-only **plan mode** while doing so). `/plan <description>` has the agent explore read-only and propose ordered steps via the `plan_write` tool; `/plan add <step>` amends the draft, `/plan approve` accepts it and starts executing (mutating tools re-enabled), `/plan clear` discards it, and bare `/plan` shows the current plan + status. The plan is persisted with the session and rendered in the sidebar.
- `/build` — leave plan mode; mutating tools are re-enabled
- `/memory [note]` — show project + global memory, or append a note (or start a line with `#`). Scope: default is this project's `AGENTS.md`, `--global` the user-level `AGENTS.md`, `--user` the `USER.md` profile of facts about *you* (style, formats, habits) that apply in every project
- `/doctor` — run an environment & config check (Ollama reachability, MCP/LSP servers, Go env)
- `/model [id]` — show the current model, or set it (saved to config for next launch)
- `/models` — fetch the models installed on the configured Ollama server and pick one (↑/↓ to move, enter to select)
- `/provider [name]` — show or set the **model provider**: `ollama` (default, local, no key), or a hosted one: `openai`, `anthropic`, `openrouter`, `gemini`, `deepseek`, `groq`, `xai`, `mistral`, `together`, `fireworks`, `moonshot`, `zai`, `dashscope`, `nvidia`, `huggingface`, `venice`. With no argument it opens a picker. Switching moves you to that provider's default model and saves the choice; hosted providers need their API key set (see *Configuration*). Pick a specific model with `/models`/`/model`.
- `/reasoning [level]` — set the extended-thinking level; available levels depend on the model (Opus: `low`/`medium`/`high`/`x-high`/`max`; Gemini: `minimal`/`low`/`medium`/`high`; others: `low`/`medium`/`high`). With no argument it opens a picker showing what the current model supports; saved per model and clamped when you switch models
- `/compact` — summarize older history into a single synthetic exchange to free up context
- `/goal [description|clear]` — set a stop-condition the agent must satisfy before it stops. With no argument it shows the current goal. When a goal is set, the agent doesn't end a turn just because the model emitted no tool calls — an independent **judge model** evaluates the conversation against the goal and, if it isn't fully met, the judge's feedback is injected and the agent keeps working (bounded per turn). Persisted with the session, so it survives `--continue`. `/goal clear` removes it
- `/checkpoint [now|clear]` — show, refresh, or clear the session **checkpoint**: a model-distilled snapshot of the session's state (goal, key decisions, files created/modified, findings, what's done vs. in progress, next steps). It is refreshed automatically as the session grows (every ~12 messages; tune with `MN_CHECKPOINT_EVERY`, `0` disables) and re-injected into the model's context every turn, so the distilled state survives `/compact` and `--continue` without the agent relearning it. Unlike `/compact`, the checkpoint never discards history — it augments it
- `/clear` — clear the screen
- `/reset` — clear screen and start a **new** session (forgets history)
- `/undo` — revert the files written or edited in the most recent turn (snapshots are recorded automatically)
- `/changes` (alias `/diff`) — show one combined diff of every file the current turn changed, so you can review the whole change set before keeping it (`/undo` reverts it all)
- `/tokens` — show token usage this session
- `/usage` — show token usage for the session. Ollama is local and has no account, quota, or billing, so there's no remote spend to report.
- `/sessions [id]` — pick a recent session for this directory to switch to (opens a picker; or switch directly by id)
- `/approval [level]` — set the approval policy: `prompt`/`safe`/`auto` (saved)
- `/agent [profile]` — switch the active primary agent profile (e.g. `build`/`plan`; also clickable in the web UI header, at parity with `/agent` and `Tab`)
- `/verify [command\|off]` — configure a build or test command run automatically at the end of a turn that edited files; if the command fails, the output is fed back so the agent self-corrects before stopping (e.g. `/verify go test ./...` or `/verify off`)
- `/permissions [allow\|deny\|remove <rule>] [--global]` — list or edit persisted allow/deny rules (deny wins; rules are tool names like `write_file` or bash prefixes like `bash:git `; `--global` targets the user-level file, default is this project)
- `/sandbox [on\|off]` — macOS: confine `bash` writes to the working directory + build caches (saved)
- `/relay [addr\|off]` — auto-connect to a relay (`host:port`) in the background on every launch so a remote browser can reach this machine's full multi-project app (with this terminal's live session wired in); shows a status pill (saved; `MN_RELAY` overrides; applies on next start)
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

Most of these are also editable at runtime from the web UI's **Persona** panel (*Persona* in the header), which persists your choice so it survives a restart and wins over the environment.

| Env var | Default | Purpose |
|---------|---------|---------|
| `MN_PROVIDER` | `ollama` | backend provider: `ollama` (default), `openai`, `anthropic`, `openrouter`, `gemini`, `deepseek`, `groq`, `xai`, `mistral`, `together`, `fireworks`, `moonshot`, `zai`, `dashscope`, `nvidia`, `huggingface`, `venice`, `omniroute`, `cerebras`, `perplexity`, `cohere`, `deepinfra`, `meta`, `lmstudio`, `llamacpp`, `azure`, `vertex`, `bedrock`, or `copilot`. Overrides the saved `/provider` choice for the run |
| `MN_MODEL` | provider default | model id (e.g. `llama3.1:8b`, `gpt-4o`, `claude-sonnet-4-5`, or a `vendor/model` OpenRouter slug) |
| `MN_OLLAMA_HOST` | `http://localhost:11434` | Ollama server base URL (a bare host:port is accepted; `/v1` is appended automatically) |
| `MN_OLLAMA_TOKEN` | _(unset)_ | optional bearer token for a remote/protected Ollama server |
| `MN_OPENAI_API_KEY` | _(unset)_ | OpenAI API key for the `openai` provider (falls back to `OPENAI_API_KEY`) |
| `MN_ANTHROPIC_API_KEY` | _(unset)_ | Anthropic API key for the `anthropic` provider (falls back to `ANTHROPIC_API_KEY`) |
| `MN_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Messages API base URL — point it at a self-hosted gateway, proxy or tunnel |
| `MN_ANTHROPIC_CUSTOM_HEADERS` | _(unset)_ | extra headers for Anthropic-wire requests, e.g. `Authorization: Basic …` (falls back to `ANTHROPIC_CUSTOM_HEADERS`); only sent when `MN_ANTHROPIC_BASE_URL` is set |
| `MN_OPENROUTER_API_KEY` | _(unset)_ | OpenRouter API key for the `openrouter` provider (falls back to `OPENROUTER_API_KEY`) |
| `MN_GEMINI_API_KEY` | _(unset)_ | Google Gemini API key for the `gemini` provider (falls back to `GEMINI_API_KEY`) |
| `MN_DEEPSEEK_API_KEY` | _(unset)_ | DeepSeek API key for the `deepseek` provider (falls back to `DEEPSEEK_API_KEY`) |
| `MN_GROQ_API_KEY` | _(unset)_ | Groq API key for the `groq` provider (falls back to `GROQ_API_KEY`) |
| `MN_XAI_API_KEY` | _(unset)_ | xAI API key for the `xai` provider (falls back to `XAI_API_KEY`) |
| `MN_MISTRAL_API_KEY` | _(unset)_ | Mistral API key for the `mistral` provider (falls back to `MISTRAL_API_KEY`) |
| `MN_TOGETHER_API_KEY` | _(unset)_ | Together.ai API key for the `together` provider (falls back to `TOGETHER_API_KEY`) |
| `MN_FIREWORKS_API_KEY` | _(unset)_ | Fireworks.ai API key for the `fireworks` provider (falls back to `FIREWORKS_API_KEY`) |
| `MN_MOONSHOT_API_KEY` | _(unset)_ | Moonshot API key for the `moonshot` provider (falls back to `MOONSHOT_API_KEY`) |
| `MN_ZAI_API_KEY` | _(unset)_ | Z.AI API key for the `zai` provider (falls back to `ZAI_API_KEY`) |
| `MN_DASHSCOPE_API_KEY` | _(unset)_ | DashScope API key for the `dashscope` provider (falls back to `DASHSCOPE_API_KEY`) |
| `MN_NVIDIA_API_KEY` | _(unset)_ | NVIDIA NIM API key for the `nvidia` provider (falls back to `NVIDIA_API_KEY`) |
| `MN_HUGGINGFACE_API_KEY` | _(unset)_ | Hugging Face API key for the `huggingface` provider (falls back to `HF_TOKEN`) |
| `MN_VENICE_API_KEY` | _(unset)_ | Venice API key for the `venice` provider (falls back to `VENICE_API_KEY`) |
| `MN_CEREBRAS_API_KEY` | _(unset)_ | Cerebras API key for the `cerebras` provider (falls back to `CEREBRAS_API_KEY`) |
| `MN_PERPLEXITY_API_KEY` | _(unset)_ | Perplexity API key for the `perplexity` provider (falls back to `PERPLEXITY_API_KEY` / `PPLX_API_KEY`) |
| `MN_COHERE_API_KEY` | _(unset)_ | Cohere API key for the `cohere` provider (falls back to `CO_API_KEY` / `COHERE_API_KEY`) |
| `MN_DEEPINFRA_API_KEY` | _(unset)_ | DeepInfra API key for the `deepinfra` provider (falls back to `DEEPINFRA_API_KEY` / `DEEPINFRA_TOKEN`) |
| `MN_META_API_KEY` | _(unset)_ | Meta Model API key for the `meta` provider (falls back to Meta's own `MODEL_API_KEY` / `META_API_KEY`) |
| `MN_META_BASE_URL` | `https://api.meta.ai/v1` | Meta Model API base URL |
| `MN_LMSTUDIO_BASE_URL` | `http://localhost:1234/v1` | LM Studio server URL for the `lmstudio` provider (falls back to `LM_STUDIO_BASE_URL`); keyless — optional `MN_LMSTUDIO_API_KEY` if you've enabled auth |
| `MN_LLAMACPP_BASE_URL` | `http://localhost:8080/v1` | llama.cpp (`llama-server`) URL for the `llamacpp` provider (falls back to `LLAMA_CPP_BASE_URL`); keyless |
| `MN_AZURE_API_KEY` | _(unset)_ | Azure OpenAI key for the `azure` provider (falls back to `AZURE_OPENAI_API_KEY` / `AZURE_API_KEY`) |
| `MN_AZURE_BASE_URL` | _(unset, required)_ | Azure OpenAI resource endpoint, e.g. `https://my-res.openai.azure.com` (falls back to `AZURE_OPENAI_ENDPOINT`) |
| `MN_AZURE_API_VERSION` | `2024-10-21` | Azure OpenAI `api-version` query param |
| `MN_VERTEX_API_KEY` | _(unset)_ | GCP access token for the `vertex` provider, e.g. `gcloud auth print-access-token` (falls back to `GOOGLE_VERTEX_API_KEY`) |
| `MN_VERTEX_PROJECT` | _(unset)_ | GCP project id for `vertex` (falls back to `GOOGLE_CLOUD_PROJECT`) |
| `MN_VERTEX_LOCATION` | `us-central1` | Vertex AI region for `vertex` (`global` also valid; falls back to `GOOGLE_CLOUD_LOCATION` / `CLOUD_ML_REGION`) |
| `MN_AWS_ACCESS_KEY_ID` / `MN_AWS_SECRET_ACCESS_KEY` / `MN_AWS_SESSION_TOKEN` | _(unset)_ | AWS credentials for the `bedrock` provider; if unset, the standard `AWS_*` env vars / profile / default chain are used |
| `MN_AWS_REGION` | `us-east-1` | AWS region for `bedrock` (falls back to `AWS_REGION` / `AWS_DEFAULT_REGION`) |
| `MN_AWS_PROFILE` | _(unset)_ | named AWS profile for `bedrock` when explicit keys aren't set (falls back to `AWS_PROFILE`) |
| `MN_COPILOT_TOKEN` | _(unset)_ | GitHub token for the `copilot` provider (falls back to `COPILOT_GITHUB_TOKEN` / `GH_TOKEN` / `GITHUB_TOKEN`; else the CLI's stored credentials) |
| `MN_COPILOT_CLI_PATH` | _(unset)_ | path to the `copilot` CLI executable for the `copilot` provider (falls back to `COPILOT_CLI_PATH`; else `copilot` is looked up on `PATH`) |
| `MN_<PROVIDER>_BASE_URL` | provider default | override any provider's API base URL (e.g. `MN_OPENAI_BASE_URL`, `MN_GEMINI_BASE_URL`) — useful for proxies or self-hosted gateways |
| `MN_REASONING` | `off` | extended-thinking level (model-dependent: `off`/`minimal`/`low`/`medium`/`high`/`x-high`/`max`; clamped to what the model supports) |
| `MN_MAX_STEPS` | `150` | max model↔tool rounds per turn before the agent stops (raise for long, todo-driven tasks; a runaway-loop guard, not a budget) |
| `MN_AUTO_CONTINUE` | `3` | how many times a turn auto-extends past `MN_MAX_STEPS` when the model is still working (so long tasks finish without a manual "continue"); `0` disables. Hard ceiling = `MN_MAX_STEPS × (1 + MN_AUTO_CONTINUE)` |
| `MN_APPROVAL` | `prompt` | approval policy: `prompt`/`safe`/`auto` |
| `MN_MOUSE` | _(on)_ | mouse-wheel scrolling is on by default; set to `0` to disable it (enables native text selection; scroll with the keyboard) |
| `MN_WHEEL_DELTA` | `2` | lines scrolled per mouse-wheel tick; non-numeric or out-of-range values fall back to 2 |
| `MN_CTX_BAR` | _(on)_ | show the graphical context-window usage gauge in the status bar; set to `0`/`off` for a text-only readout |
| `MN_STREAM_MARKDOWN` | _(on)_ | render the assistant's reply as markdown live while it streams; set to `0` to stream raw text and render markdown only on completion |
| `MN_SOUND` | _(on)_ | play a short chime when the agent finishes a turn (macOS); set to `0` to silence it |
| `MN_AUTO_TITLE` | _(on)_ | after a new session's first turn, ask the model for a short session title (replacing the first-message heuristic); set to `0`/`off` to keep the heuristic and skip the extra call |
| `MN_PROMPT_CACHE` | _(on)_ | Anthropic prompt caching: cache the system prompt, tools, and conversation prefix so repeated agentic steps bill at the much cheaper cache-read rate; set to `0` to disable |
| `MN_REPO_MAP` | _(on)_ | when true, prepends a compact, ignore-aware structural overview of the repository (detected stack, key files, a depth-2 directory tree with file counts) to the system prompt; `0`/`off` disables |
| `MN_TERSE` | _(on)_ | verbosity steering: add an "OUTPUT BUDGET" directive to the system prompt so the model spends fewer (pricier) output tokens on ceremony — preambles, sign-offs, restated context, re-printed unchanged code — without skipping actual work; `0`/`off` disables |
| `MN_CODE_OUTLINE` | _(on)_ | compress oversized `read_file` output on source files into a signatures/declarations outline (line numbers kept, bodies collapsed into `… (N lines elided)`) instead of a blunt byte clip, so the model keeps the file's shape and can re-read a range; `0`/`off` disables |
| `MN_DYNAMIC_EFFORT` | _(on)_ | dynamic effort routing (`auto` reasoning only): run steps that just digest a routine/benign tool batch (edits, a listing, a todo update — no errors) at a lower reasoning floor, while new requests and tool errors still get full effort; `0`/`off` disables |
| `MN_ADAPTIVE_MODEL` | _(on)_ | run the main turn on the cheaper fast model until it shows sustained friction (tool errors / rejected stops), then escalate to the session model for the rest of the turn — right-sizing the model to the task; `0`/`off` disables. No-op when the fast model equals the session model, or on ultrathink turns |
| `MN_CHEAP_READS` | _(on)_ | route pure read-only rounds (no edits/writes yet) to the fast model, restoring the full session model once the turn hits friction; `0`/`off` disables. No-op when the fast model equals the session model |
| `MN_FAST_MODEL` | provider default | pin the cheap/fast model used by `MN_ADAPTIVE_MODEL`/`MN_CHEAP_READS` (and as a fallback for `MN_AUX_MODEL`) instead of the provider's own default fast model |
| `MN_AUX_MODEL` | _(falls back to `MN_FAST_MODEL`)_ | pin the model used for small mechanical sub-tasks (e.g. summarization); checked before `MN_FAST_MODEL` |
| `MN_EMBEDDINGS_MODEL` | _(unset)_ | enable hybrid semantic reranking in `search_code` and `recall`: the top BM25 candidates are re-scored by embedding cosine similarity to the query and blended with their lexical score. Unset = pure BM25 |
| `MN_EMBEDDINGS_PROVIDER` | _(falls back to Ollama)_ | which backend serves `MN_EMBEDDINGS_MODEL`, independent of the session's main provider — so a session on e.g. Anthropic can still point embeddings at OpenAI (or any other OpenAI-compatible provider: OpenRouter, Gemini, DeepSeek, Groq, xAI, Mistral, Together, Fireworks, NVIDIA NIM, Hugging Face, Venice, DashScope, Moonshot, Z.AI) without switching its main model. Unset or `ollama` uses Ollama's native embeddings endpoint. Anthropic has no embeddings API and is rejected with a clear error |
| `MN_RAG_INDEX_DIR` | `<project>/.sqe/rag/index.gob` | where `semantic_search` keeps its local vector index. Point several git worktrees of one repo at the same path to share (and avoid re-embedding) an index |
| `MN_CODEBASE_MAP` | _(auto)_ | `1`/`on` keeps `CODEBASE_MAP.md`'s generated structure section fresh and injects the map into the system prompt; `0`/`off` forces it off. Enabled implicitly when a `CODEBASE_MAP.md` already exists at the project root |
| `MN_CODEBASE_MAP_BYTES` | built-in default | caps how much of the codebase map is injected into the system prompt, so a large map can't crowd out the conversation |
| `MN_IMAGE_MODEL` | _(unset — tool off)_ | image-output model enabling `generate_image` (any OpenAI-compatible route, e.g. a Gemini/GPT image model) |
| `MN_IMAGE_PROVIDER` | _(falls back to the session provider)_ | which backend serves `generate_image`, independent of the session's main provider. Few backends offer an image-capable chat route, so this lets a session on a local or text-only model still generate images by pointing it at one that does |
| `MN_VIDEO_MODEL` | _(unset — tool off)_ | video-output model enabling `generate_video` (a Veo model on the native Gemini route; needs `MN_GEMINI_API_KEY`) |
| `MN_TASKBOARD_MAX_PARALLEL` | `2` | how many web **Task Board** tasks run at once. Each task is a headless sub-process in its own throwaway git worktree, so this bounds concurrent CPU/API load |
| `MN_ACCESS_LOG` | `0` (off) | set `1`/`on` to log one line per HTTP request (method, route, status, duration) alongside the `/metrics` counters |
| `MN_GITHUB_TOKEN` / `GITHUB_TOKEN` | _(falls back to `gh auth token`, then `~/.netrc`)_ | auth for the `pr_*` tools on GitHub repos |
| `MN_GITHUB_BASE_URL` | _(unset — github.com)_ | GitHub Enterprise API base, e.g. `https://git.example.com/api/v3`; also identifies which self-hosted host is GitHub for repo detection |
| `MN_GITLAB_TOKEN` / `GITLAB_TOKEN` | _(falls back to `glab auth token`, then `~/.netrc`)_ | auth for the `pr_*` tools on GitLab repos |
| `MN_GITLAB_BASE_URL` | _(unset — gitlab.com)_ | self-hosted GitLab API base, e.g. `https://gitlab.example.com/api/v4`; also identifies which self-hosted host is GitLab for repo detection |
| `MN_DAILY_BUDGET` / `MN_MONTHLY_BUDGET` | _(unset — disabled)_ | USD caps on **cross-session** spend (unlike `MN_TURN_WARN_COST`/`MN_TURN_MAX_COST`, which only ever see one turn). Tracked in a small ledger at `usage_ledger.json` next to your sessions. Warn-only: crossing a cap surfaces an Info message, it never blocks a turn |
| `MN_NOTIFY_WEBHOOK_URL` | _(unset — disabled)_ | outbound webhook fired on turn completion / approval-needed. Posts a small JSON body with both `text` (Slack/Mattermost incoming-webhook shape) and `content` (Discord) keys, so one URL works with either |
| `MN_NOTIFY_EVENTS` | `turn_done,approval` (both) | comma-separated subset to actually notify for |
| `MN_NOTIFY_MIN_SECONDS` | `20` | only fire a `turn_done` webhook for turns that ran at least this long (avoids pinging you for trivial quick turns); `approval` always fires regardless — it's blocking |
| `MN_TUI_BELL` | `1` (on) | ring the terminal bell (`\a`) on turn-done / approval-needed in the TUI; `0` to disable |
| `MN_DAP_ADDR` | _(unset)_ | auto-connect to a Debug Adapter Protocol server at `host:port` on startup; if unset, use `dap_connect` interactively |
| `MN_AUTO_PLAN` | `0` (off) | auto-enter read-only plan mode for a build-mode turn that looks like a large/multi-step change, drafting a plan for approval before touching files; toggle at runtime with `/plan auto on|off`. `/build` always overrides it for a turn |
| `MN_AUTO_DELEGATE` | `0` (off) | auto-delegate a clearly read-heavy investigation ("how/where/why does X work", "investigate …") to the read-only `explore` sub-agent, so bulky reads stay in its (discarded) context instead of re-billing this turn every round. Requires the task tool; skipped in plan mode |
| `MN_PARALLEL_TOOLS` | _(on)_ | run independent tool calls in a turn concurrently (read-only tools and sub-agents); set to `0` to force sequential execution, or a number to cap concurrency (default 8) |
| `MN_AGENT_MESSAGING` | _(on)_ | enable agent-to-agent message board for concurrent sub-agents; `0`/`off` disables |
| `MN_RG` | _(on)_ | use `ripgrep` (`rg`) for the `grep` tool when it's installed (faster on large trees); set to `0`/`off` to always use the built-in Go scanner. Output is identical either way |
| `MN_SKILLS` | _(on)_ | discover Agent Skills (`SKILL.md`); set to `0`/`off` to turn skill discovery off entirely |
| `MN_MCP` | _(on)_ | connect to configured MCP servers; set to `0`/`off` to skip connecting (config discovery and the `/mcps` listing still work) |
| `MN_MCP_CLAUDE` | _(on)_ | auto-discover MCP servers from Claude's config (`claude_desktop_config.json`, `~/.claude.json`); set to `0`/`off` to skip |
| `MN_RELAY` | _(unset)_ | `host:port` of a relay (see `cmd/relay`) to dial out to; the terminal serves the full multi-project app over it (so a remote browser can browse and run all this machine's sessions), with this terminal's **live session wired in through the hub** — opening it drives the exact in-terminal conversation. Best-effort + auto-reconnecting, and the TUI runs normally whether or not it connects. Overrides the saved `/relay` address for the run |
| `MN_AUTO_UPDATE` | `1` (on) | let a **background connector daemon** poll for a newer published build and relaunch onto it. Deferred while any session is mid-turn (the relaunch would drop in-flight work), and re-checked after the download. A live terminal or browser session is never auto-updated — it only shows a notice and waits for `/update`, because swapping the process under someone who is watching would lose their turn. Set `0`/`off` to pin a machine to its current build |
| `MN_QUOTA_FALLBACK` | `0` (off) | opt in to **automatic provider fallback**: when the current backend crosses a spend budget, or hard-refuses with a quota error, switch to the next reachable provider in `MN_PROVIDER_FALLBACKS` and switch back once the quota period resets. Off by default because silently moving traffic to another backend means different models and different billing |
| `MN_PROVIDER_FALLBACKS` | _(unset)_ | ordered fallback chain, e.g. `anthropic,openai:gpt-4o,ollama:qwen3-coder:30b:high`. Each hop is `provider[:model[:reasoning]]`; a bare provider uses that backend's default model (model ids aren't portable across providers, so every switch changes the model too). Hops with no configured credentials are skipped, unknown provider ids are dropped, and the chain never wraps around onto an exhausted provider |
| `MN_QUOTA_BUDGET_USD` | _(unset)_ | absolute spend on the current provider that triggers a fallback |
| `MN_QUOTA_BUDGET_PCT` | _(unset)_ | percentage of the provider's reported quota that triggers a fallback. Skipped when the provider reports no cap — see [Quota readings](#quota-readings) for which providers report one. Either threshold alone can fire |
| `MN_QUOTA_RESET_AT` | `00:00` | local daily rollover used to schedule the return to the primary provider when the provider doesn't report a reset time (a hard 429 carries none). Set empty to disable the timed restore and stay on the fallback until you switch back by hand |
| `MN_SANDBOX` | _(off)_ | confine `bash` writes to the working directory (plus temp and common build caches) via `sandbox-exec` (macOS) or `bwrap` (Linux). Set `on`/`1` to enable. Reads, process execution, and network are unaffected. No-op on other platforms or if the confinement tool isn't installed. |
| `MN_SANDBOX_NET` | _(on)_ | when the sandbox is on, set `0`/`off` to also deny network access to sandboxed commands |
| `MN_NETRC` | _(on)_ | `web_fetch`/`batch_web_fetch` read `~/.netrc` (or `$NETRC`) and send the matching host's `password` as `Authorization: Bearer <token>`; set `0`/`off` to disable. A caller-supplied `Authorization` header always wins |
| `MN_VERIFY_CMD` | _(unset)_ | shell command (e.g. `go test ./...`) run automatically on file edits to feed failures back for self-correction |
| `MN_VERIFY_AUTO` | `0` (off) | set `1`/`on` to **auto-detect** a verify command when `MN_VERIFY_CMD` is unset, from the project's own markers (a Makefile `test`/`check` target, `go.mod`, a `package.json` test script, `Cargo.toml`, pytest/tox, Maven/Gradle). Opt-in on purpose — it runs a project command you didn't type; an explicit `MN_VERIFY_CMD` always wins |
| `MN_VERIFY_MAX` | `2` | max verify retries per turn before stopping for review |
| `MN_VERIFY_TIMEOUT` | `120` | timeout in seconds for a single verify command run |
| `MN_SELF_REVIEW` | `0` (off) | set to `1`/`on`/`true` to run a cheap fast-model self-review on changed diffs to detect and fix bugs before ending a turn |
| `MN_ENV_FILE` | _(unset)_ | path to a `.env` file loaded at startup for any **unset** variables (real env always wins). Defaults to `~/.config/moony/.env` then `./.env` when unset. Local-dev convenience — never use in prod |
| `MN_RELAY_TLS_CERT` | _(unset)_ | PEM certificate file so the relay serves **HTTPS** (also `-tls-cert`). Pair with the key below |
| `MN_RELAY_TLS_KEY` | _(unset)_ | PEM private-key file for the relay's HTTPS cert (also `-tls-key`) |
| `MN_DEBUG_FILE` | _(unset)_ | debugging: on the OpenAI route, append the request body and every raw SSE chunk to this file (useful for inspecting how the proxy streams) |

Flags:

| Flag | Purpose |
|------|---------|
| `--model <id>` | override the model |
| `--reasoning <level>` | extended-thinking level (model-dependent; clamped to what the model supports) |
| `--approval <level>` | approval policy: `prompt`/`safe`/`auto` |
| `--sandbox <on\|off>` | macOS/Linux: confine `bash` writes to the working dir (also `/sandbox`, `MN_SANDBOX`) |
| `-C <dir>` | working directory for the agent |
| `--continue` | resume the most recent session for this directory |
| `--session <id>` | resume a specific session by id |
| `--no-approval` | auto-run mutating tools without confirmation prompts (alias for `--approval auto`) |
| `-p`, `--print <prompt>` | headless mode: run a single prompt non-interactively and exit (also reads piped stdin) |
| `--json` | headless mode: emit one JSON result object (`{ok, response, sessionId, error?}`) instead of streamed text |
| `--version` | print version and exit |

## Headless mode

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



The agent supports twenty-nine backends, selected with `/provider` (or `MN_PROVIDER`):

- **Ollama** (default) — a local or remote **[Ollama](https://ollama.com)** server, no API key. Set `MN_OLLAMA_HOST` (`http://localhost:11434` by default; a bare `host:port` gets `/v1` appended) and optionally `MN_OLLAMA_TOKEN`. Runs locally, so there's no account, quota, or billing — the in-app cost counter reads `$0`.
- **OpenAI** — `api.openai.com`, `MN_OPENAI_API_KEY` (or `OPENAI_API_KEY`). Reasoning-model families (o1/o3/o4/gpt-5) get `reasoning_effort` from `/reasoning`.
- **Anthropic** — the native Claude Messages API at `api.anthropic.com`, `MN_ANTHROPIC_API_KEY` (or `ANTHROPIC_API_KEY`). `/reasoning` maps to Anthropic's extended-thinking token budget, and prompt caching is on by default (`MN_PROMPT_CACHE=0` to disable).
  Point `MN_ANTHROPIC_BASE_URL` at any Messages-API-compatible gateway (LiteLLM, a Vertex/Bedrock shim, an ngrok tunnel) to use it instead. When that gateway wants its own auth in front of the API, put it in `MN_ANTHROPIC_CUSTOM_HEADERS` as `Name: value` pairs — separated by newlines, semicolons, or nothing at all, so a pasted `Authorization: Basic dXNlcjpwdw== ngrok-skip-browser-warning: 1` parses as two headers. They are applied after the built-ins (so an operator can override one on purpose) and are **never** sent to `api.anthropic.com`, so gateway credentials can't leak upstream if the base URL is cleared. Gateways that authenticate you themselves still need `MN_ANTHROPIC_API_KEY` set to some non-empty value.
- **OpenRouter** — `openrouter.ai`, `MN_OPENROUTER_API_KEY` (or `OPENROUTER_API_KEY`); one key reaches hundreds of models via `vendor/model` slugs (e.g. `anthropic/claude-sonnet-4.5`).
- **Google Gemini** — Google's OpenAI-compatible endpoint, `MN_GEMINI_API_KEY` (or `GEMINI_API_KEY`).
- **DeepSeek** — `api.deepseek.com`, `MN_DEEPSEEK_API_KEY`; `/reasoning` engages on `deepseek-reasoner`.
- **Groq** — `api.groq.com`, `MN_GROQ_API_KEY`; very fast inference for open models.
- **xAI (Grok)** — `api.x.ai`, `MN_XAI_API_KEY`.
- **Mistral** — `api.mistral.ai`, `MN_MISTRAL_API_KEY`.
- **Together.ai** — `api.together.xyz`, `MN_TOGETHER_API_KEY`; hosted open models.
- **Fireworks.ai** — `api.fireworks.ai`, `MN_FIREWORKS_API_KEY`; hosted open models.
- **Moonshot (Kimi)** — `api.moonshot.ai`, `MN_MOONSHOT_API_KEY` (or `MOONSHOT_API_KEY`); Kimi K2 models.
- **Z.AI (GLM)** — `api.z.ai`, `MN_ZAI_API_KEY` (or `ZAI_API_KEY`); Zhipu GLM models (e.g. `glm-4.6`).
- **DashScope (Qwen)** — `dashscope-intl.aliyuncs.com`, `MN_DASHSCOPE_API_KEY` (or `DASHSCOPE_API_KEY`); Alibaba Qwen via OpenAI-compatible mode.
- **NVIDIA NIM** — `integrate.api.nvidia.com`, `MN_NVIDIA_API_KEY` (or `NVIDIA_API_KEY`); hosted open models.
- **Hugging Face** — `router.huggingface.co`, `MN_HUGGINGFACE_API_KEY` (or `HF_TOKEN`); inference router across many open models.
- **Venice** — `api.venice.ai`, `MN_VENICE_API_KEY` (or `VENICE_API_KEY`); privacy-focused open models.
- **OmniRoute** — a local OmniRoute gateway (default `http://localhost:20128/v1`), `MN_OMNIROUTE_API_KEY` (or `OMNIROUTE_API_KEY`); use `prefix/model` ids or `auto` to let it route.
- **Cerebras** — `api.cerebras.ai`, `MN_CEREBRAS_API_KEY` (or `CEREBRAS_API_KEY`); extremely fast inference for open models. `/reasoning` engages on thinking models (QwQ/R1/etc.).
- **Perplexity (Sonar)** — `api.perplexity.ai`, `MN_PERPLEXITY_API_KEY` (or `PERPLEXITY_API_KEY` / `PPLX_API_KEY`); web-grounded answers with citations. `/reasoning` engages on the `sonar-reasoning` models.
- **Cohere** — `api.cohere.ai` (OpenAI-compatible `/compatibility/v1`), `MN_COHERE_API_KEY` (or `CO_API_KEY` / `COHERE_API_KEY`); Command models with strong RAG/tool use.
- **DeepInfra** — `api.deepinfra.com` (OpenAI-compatible), `MN_DEEPINFRA_API_KEY` (or `DEEPINFRA_API_KEY` / `DEEPINFRA_TOKEN`); a large, cheap catalog of hosted open models (Llama, DeepSeek, Qwen, Mixtral, …). Default model `meta-llama/Llama-3.3-70B-Instruct`.
- **Meta Model API** — `api.meta.ai/v1` (OpenAI-compatible), `MN_META_API_KEY` (or Meta's own `MODEL_API_KEY` / `META_API_KEY`; keys look like `LLM|<id>|<secret>`, from [dev.meta.ai](https://dev.meta.ai/)); Meta's Muse Spark coding models — reasoning-first, 1M-token context, prompt caching and tool calling. Default model `muse-spark-1.2`; `muse-spark-1.2-contributor` is much cheaper but rate-limited by a rolling token window and opts prompts into training.
- **LM Studio** — a **local, keyless** OpenAI-compatible server (default `http://localhost:1234/v1`, override with `MN_LMSTUDIO_BASE_URL`). Like Ollama, it needs no API key and serves whichever model you've loaded in the LM Studio app, so there's no fixed default — use `/models` to list and `/model <id>` to pick. Set `MN_LMSTUDIO_API_KEY` only if you've enabled auth on the server.
- **llama.cpp** — a **local, keyless** OpenAI-compatible server (`llama-server`, default `http://localhost:8080/v1`, override with `MN_LLAMACPP_BASE_URL`); serves the single model it was launched with (`/model` / `/models` reflect that).
- **Azure OpenAI** — OpenAI models on your Azure resource. Needs `MN_AZURE_API_KEY` (or `AZURE_OPENAI_API_KEY`) **and** `MN_AZURE_BASE_URL` (your resource endpoint, e.g. `https://my-res.openai.azure.com`; or `AZURE_OPENAI_ENDPOINT`). Uses the `api-key` header, deployment-scoped paths, and an `api-version` query param (`MN_AZURE_API_VERSION`, default `2024-10-21`). `/model` is your **deployment name** (there's no universal default).
- **Google Vertex AI** — Vertex's OpenAI-compatible endpoint. Auth is a short-lived GCP access token in `MN_VERTEX_API_KEY` (e.g. `gcloud auth print-access-token`), plus `MN_VERTEX_PROJECT` (or `GOOGLE_CLOUD_PROJECT`) and `MN_VERTEX_LOCATION` (default `us-central1`; `global` also works). The base URL is derived from project+location, or overridden with `MN_VERTEX_BASE_URL`.
- **AWS Bedrock** — Claude (and other) models on AWS. Unlike the rest, requests are **SigV4-signed and streamed over the AWS event-stream protocol via the AWS SDK** (reusing the Anthropic wire format). Credentials come from `MN_AWS_ACCESS_KEY_ID`/`MN_AWS_SECRET_ACCESS_KEY`(/`MN_AWS_SESSION_TOKEN`), or a named profile (`MN_AWS_PROFILE`), or the standard AWS credential chain (shared config, SSO, env, instance role). Set `MN_AWS_REGION` (default `us-east-1`). `/model` is a Bedrock model or cross-region **inference-profile** id (default `us.anthropic.claude-sonnet-4-5-…`). `/reasoning` maps to Claude extended thinking. The standard `AWS_*` env vars are honored and, unlike the moony-prefixed keys, are **not** scrubbed from tool shells so your own `aws` CLI keeps working.
- **GitHub Copilot** — driven through the [github/copilot-sdk](https://github.com/github/copilot-sdk) Go SDK (not a plain HTTP API): it spawns and talks to the local **`copilot` CLI** over JSON-RPC. Runs **autonomously** — the Copilot CLI plans and calls its *own* built-in tools (file read/write, shell, …) with permission prompts auto-approved, while moony streams its text/reasoning/tool-activity into the TUI. Requires the `copilot` CLI on `PATH` (or `MN_COPILOT_CLI_PATH`) and a GitHub token via `MN_COPILOT_TOKEN` (falls back to `COPILOT_GITHUB_TOKEN` / `GH_TOKEN` / `GITHUB_TOKEN`, else the CLI's stored credentials). Note: because Copilot's CLI runs its own tools, moony's approval policy / sandbox / tool logging do **not** cover what it does on disk.

Every provider except Anthropic, AWS Bedrock, and GitHub Copilot speaks the **OpenAI-compatible Chat Completions** wire format; Anthropic uses its **native Messages API**, AWS Bedrock is SigV4-signed over the AWS event-stream protocol (reusing the Anthropic wire), and GitHub Copilot is driven through its SDK/CLI (autonomous). Either way the agent translates its conversation and tools to/from the provider's format automatically. Each hosted HTTP provider's base URL can be overridden with `MN_<PROVIDER>_BASE_URL` (e.g. for a proxy or self-hosted gateway). Switching provider moves you to that provider's default model; `/models` lists what's available and `/model <id>` (or `MN_MODEL`) selects one.

Sessions are stored under `~/.config/moony/sessions/`. Your model and reasoning-level choices (set via `/model`, `/models`, or `/reasoning`) persist to `~/.config/moony/config.json` and are restored on the next launch. Precedence is **flags > environment variables > saved config > built-in defaults**. Approval and compaction thresholds (`ApprovalEnabled`, `CompactThresholdTokens`, `CompactKeepTokens`) live in `internal/config`.


## Quota readings

The status bar shows a real usage figure when the provider exposes one to the **same API
key** used for inference. Most don't: quota lives in a dashboard, or behind an admin/org
credential a normal key doesn't carry. When there's no reading, moony shows an *estimate*
from local token counts instead, suffixed with `~`, and says which provider couldn't
report.

| Provider | What it reports | Endpoint |
|---|---|---|
| **OpenRouter** | spend **and** limit — the only one with both, so percentage thresholds work | `GET /api/v1/auth/key` |
| **DeepSeek** | credit remaining | `GET /user/balance` |
| **Moonshot** | credit remaining | `GET /v1/users/me/balance` |
| **Z.AI** | credit remaining | `GET /api/paas/v4/account/balance` |
| **DeepInfra** | balance, or spend + limit | `GET /v1/me` |
| **Venice** | credit remaining (USD preferred over VCU/DIEM) | `GET /api/v1/api_keys/rate_limits` |
| **xAI** | no money figure; reports a **blocked or disabled key** | `GET /v1/api-key` |
| **sqe-anthropic** | full spend + budget + per-model rates | gateway `/api/usage/v1/personal` |

Everything else shows the local estimate:

- **No such endpoint:** Groq, Mistral, Together.ai, Fireworks.ai, NVIDIA NIM, Hugging Face, Cerebras, Perplexity, Gemini.
- **Needs a different credential:** OpenAI and Anthropic both have usage APIs, but they require an *admin* key, not an inference key. Azure, Vertex and Bedrock bill through their cloud provider's own APIs.
- **No billing concept:** Ollama, LM Studio, llama.cpp.

### Spend vs remaining credit

These are opposite quantities, and moony labels them differently rather than blurring the
two. A provider reporting **spend** with a cap shows `$12.50/$100.00`; a prepaid provider
reporting **credit left** shows `$4.66 left`. Only the first can produce a percentage, so
`MN_QUOTA_BUDGET_PCT` has no effect on prepaid providers — use `MN_QUOTA_BUDGET_USD` there.

> Endpoint *existence* was verified by probing each provider directly. The exact response
> field names come from provider documentation and have not all been confirmed against a
> live paid account, so each parser accepts several spellings and reports "no reading"
> rather than guessing if the shape is unfamiliar.
