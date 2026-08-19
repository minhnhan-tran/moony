# Collapsible / Expandable Jobs Strip (Default Collapsed)

Status: **Approved**

## Context & Motivation

The `jobsStrip` is docked above the composer in `#composerWrap`. It renders when background jobs exist in the current session.
Previously, when jobs were present, the full job list was always open by default, taking up vertical space above the composer.

This change makes the entire `jobsStrip` collapsible and expandable, defaulting to collapsed so that it stays out of the user's way while still providing an immediate summary of active jobs.

## Goals

1. **Default collapsed**: When background jobs are present or a session is loaded/reset, `jobsStrip` defaults to collapsed state.
2. **Clear summary**: When collapsed, only a compact header bar (~26px) is shown with an indicator chevron (`chevron.right`), terminal icon, and job counts (e.g. `Background jobs: 2 (1 running)`).
3. **Toggle on click & keyboard**: Clicking the header bar or pressing `Enter`/`Space` when focused toggles between collapsed and expanded states.
4. **Smooth interaction & styling**: When expanded, chevron points down (`chevron.down`) and the `.jobstrip-list` is shown. When collapsed, border-bottom on header is cleanly removed.
5. **Accessibility**: Header acts as a button with `role="button"`, `tabindex="0"`, `aria-expanded="false|true"`, and `aria-controls="jobsStripList"`.
6. **Efficiency**: Pauses log tailing requests for open job items when the entire strip is collapsed.

## Implementation Details

### 1. State in `internal/web/static/app-core.js`
- Introduce state variable `let stripCollapsed = true;`.
- In `resetJobsStrip()`, reset `stripCollapsed = true`.
- In `renderJobsStrip()`:
  - Build `head` element `.jobstrip-head` with `role="button"`, `tabindex="0"`, `aria-expanded=(!stripCollapsed)`, `aria-controls="jobsStripList"`.
  - Add leading chevron icon: `sym(stripCollapsed ? "chevron.right" : "chevron.down")`.
  - Attach `onclick` handler to toggle `stripCollapsed = !stripCollapsed; renderJobsStrip();`.
  - Attach `onkeydown` handler for `Enter` and `Space` keys to toggle.
  - Apply `.is-collapsed` class to `#jobsStrip` when `stripCollapsed` is true.
  - When `stripCollapsed` is true, omit `.jobstrip-list` from display / hide it and skip tail syncing.

### 2. Styling in `internal/web/static/app.css`
- Update `.jobstrip-head`:
  - `cursor: pointer;`
  - `user-select: none;`
  - Hover background highlight: `background: rgb(var(--c-bg-subtle-hover, var(--c-surface-hover, var(--c-bg))))` / subtle tint.
  - `.jobstrip-head .jobstrip-chev` / icon alignment.
- Update `.jobs-strip.is-collapsed`:
  - `.jobstrip-head` has `border-bottom: none`.
  - `.jobstrip-list` has `display: none`.

### 3. Verification Plan
- Build both binaries (`bin/moony.exe` and `bin/relay.exe`).
- Run unit/integration tests to ensure no regressions.
- Verify in browser / headless Chrome driver:
  - Jobs strip starts collapsed when jobs exist.
  - Clicking header expands the strip and displays job rows.
  - Clicking again collapses the strip.
  - Keyboard navigation (Tab + Enter/Space) toggles state.
