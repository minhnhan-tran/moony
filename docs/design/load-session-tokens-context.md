# Load Tokens and Context Usage on Session History Restore

Status: **Approved**

## Context & Motivation

When loading an existing session from history (or refreshing the web UI), the status bar in `.app-statusbar` (`#stats`) should immediately reflect:
1. Accumulated input and output tokens (`in` and `out` counts).
2. Estimated session cost.
3. Context window fill percentage and last turn input tokens (`ctx N%`).

Previously, loading a saved session created an in-memory `live` session with zeroed counters, causing the initial `status` event to emit `0` tokens and `-1` contextPercent. Replaying `turn_meta` on the client side also reset `lastTurnIn` and did not update `lastStatus.contextPercent`.

## Goals

1. **Hydrate backend `live` counters from `sess.TurnMetas`**: On session load, populate `tokensIn`, `tokensOut`, `cachedIn`, and `lastTurnIn` from `sess.TurnMetas`.
2. **Accurate initial status event**: Ensure `s.statusEvent(sess, live)` immediately sends accurate `tokensIn`, `tokensOut`, `costUSD`, `contextTokens`, and `contextPercent`.
3. **Synchronize frontend replay**: During `turn_meta` replay in `app-core.js`, sync `lastTurnIn` and `lastStatus.contextPercent` so client-side history updates match the backend.
4. **Clean session switching**: Ensure all accumulators are cleared when switching sessions.

## Implementation Details

### 1. Backend Hydration (`internal/web/server.go`)
- In `loadSession(id)` (or wherever `live := newSession(...)` is called for a loaded `sess *session.Session`):
  ```go
  for _, tm := range sess.TurnMetas {
      live.tokensIn += tm.InTokens
      live.tokensOut += tm.OutTokens
      live.cachedIn += tm.CacheReadTokens
      live.lastTurnIn = tm.InTokens
  }
  ```
- When `s.statusEvent(sess, live)` is generated at the top of the event stream, `live.usageSnapshot()` supplies the real values.

### 2. Frontend Replay Sync (`internal/web/static/app-core.js`)
- In `case "turn_meta"`:
  - Update `cumTurnIn` and `cumTurnOut`.
  - Set `lastTurnIn = ev.meta.inTokens || 0`.
  - If `lastStatus` exists and `ev.meta.inTokens > 0`:
    - `lastStatus.contextTokens = ev.meta.inTokens`.
    - If `lastStatus.contextWindow > 0`:
      `lastStatus.contextPercent = Math.min(100, Math.round((ev.meta.inTokens / lastStatus.contextWindow) * 100))`.

### 3. Verification Plan
- Unit test in `internal/web/session_test.go` or `internal/web/status_test.go` verifying that loading a session with persisted `TurnMetas` seeds `statusEvent` with non-zero tokens and accurate context percentage.
- Headless browser test verifying that opening a session with history displays the token count and context percentage in `#stats`.
