### Rules

- Always verify code changes (do review if any issues, leak of logic, security then fix them. Then if there is no issue left, make build + make test / go vet on touched packages) before writing the CHANGELOG.md entry and bumping config.Version — don't document unverified behavior.
- Do the CHANGELOG.md entry + config.Version bump with `make release` (BUMP=patch for fixes, minor for features, major for breaking changes; default minor — or VERSION=x.y.z to pin), never by hand: it bumps the const, regenerates the top CHANGELOG.md section from commit subjects since the last `vX.Y.Z` tag, and commits + tags — keeping the two in sync. Preview with `make release-dry`, then review/re-bucket the generated entry (commit subjects use an `area:` prefix, so most land under Changed).
- Web UI changes need the RELAY rebuilt, not just the connector. The relay (`cmd/relay` → `relay.exe`) serves the SPA shell and every static asset (`/` and `/static/` — `app.css`, `app-core.js`, `index.html`, …) from its own `//go:embed static` copy (`web.SPAHandler`); the `moony` connector (`moony.exe`) serves only the per-session API, proxied through the relay. So after editing anything under `internal/web/static/`, rebuild **both** `relay.exe` and `moony.exe`, **restart the running relay**, and hard-refresh the browser — a rebuilt connector alone won't change the CSS/JS the browser loads (this is why web fixes can appear "not applied"). Backend/agent changes (Go handlers, config, llm) only need `moony.exe` rebuilt + the connector restarted.

## Memory

### Rules

- After completing any code change, always add a CHANGELOG.md entry and bump config.Version via `make release` (BUMP=patch for fixes, minor for features, major for breaking) — do this automatically as part of finishing the work, don't wait to be asked.
- After every git commit (including release commits), always run `git push origin main --tags` immediately — don't leave pushes pending.
- CI (.github/workflows/ci.yml) only runs its test/windows-build/gofmt jobs when the triggering commit message contains the phrase "github build" (case-insensitive), gated by a `gate` job. Include "github build" in a commit message to actually trigger CI on push/PR.
- Web UI: the manage modal (#manageOverlay / .app-modal) must be closable ONLY via the #manageClose X button — no backdrop-click and no Escape-to-close, so panels can't discard half-filled input.

### Notes

- PlanStore.Set fires the plan_write notify (applyDraftPlan → forces Status=draft); host-side mirror writes of plan status (retire/complete/resume-restore in internal/agent/plan.go & agent.go) must use the silent PlanStore.Sync instead, or an approved/done plan reverts to draft and blocks mutating tools on later turns.

### Conventions

- moony CI (.github/workflows/ci.yml) is gated: test/windows-build/gofmt jobs only run when the triggering commit message contains "github build" (case-insensitive). Include "github build" in a commit message to trigger CI.
