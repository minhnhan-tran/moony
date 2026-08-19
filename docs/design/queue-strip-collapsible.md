# Collapsible/Expandable Queue Strip

Status: **Approved**

## Context & Motivation

When the agent is busy and the user enters messages into the composer, messages are placed into `queuedMsgs` and displayed in `#queueStrip` docked above the input area.

Currently, `#queueStrip` renders all rows expanded in full, taking up vertical space above the composer. To provide a cleaner experience and align with the collapsible `#jobsStrip` design, `#queueStrip` should be collapsible/expandable and collapsed by default.

## Goals

1. **Collapsed by default**: When messages are queued, `#queueStrip` defaults to a compact single-line disclosure header.
2. **Interactive disclosure header (`.queue-head`)**:
   - Indicator chevron (`chevron.right` when collapsed, `chevron.down` when expanded).
   - Clock icon + queue count and description (`Queued (N) — runs when turn finishes · Esc edits last`).
   - Accessible button semantics (`role="button"`, `tabindex="0"`, `aria-expanded`, `aria-controls="queueStripList"`, keyboard toggle on `Enter` and `Space`).
3. **Structured list container (`.queue-list`)**:
   - Container with `id="queueStripList"`.
   - Hidden when `#queueStrip` is collapsed.
   - Scrollable when expanded (`max-height: min(10rem, 25vh)`) with `flex-shrink: 0` on rows.
4. **Item removal and auto-hide**:
   - Individual items can be removed via `×` button.
   - Strip hides automatically when queue becomes empty.

## Implementation Details

### 1. State (`internal/web/static/app-state.js`)
- Add `let queueCollapsed = true;` tracking the collapsed/expanded state.

### 2. Markup & Styling (`internal/web/static/app.css` & `index.html`)
- In `internal/web/static/app.css`:
  - `#queueStrip.is-collapsed`: compact padding (`.25rem .75rem .25rem`).
  - `#queueStrip.is-collapsed .queue-list`: `display: none;`.
  - `.queue-head`: interactive disclosure bar with hover/focus-visible styles.
  - `.queue-head-chev`: icon wrapper for rotation/indicator.
  - `.queue-list`: flex container with scroll containment.
  - `.queue-row`: flex row with `flex-shrink: 0` and rounded border.

### 3. Logic (`internal/web/static/app-core.js`)
- In `renderQueueStrip()`:
  - If `!queuedMsgs.length`: hide `#queueStrip` and exit.
  - Toggle `#queueStrip.classList.toggle("is-collapsed", queueCollapsed)`.
  - Render `.queue-head` with chevron, icon, text, and click/key handlers toggling `queueCollapsed`.
  - If not collapsed, populate `.queue-list` with `.queue-row` elements.

### 4. Verification Plan
- Automated Chrome CDP E2E test (`scripts/verify_queue_strip.mjs`) testing:
  - Default collapsed state on queueing messages.
  - Toggle to expanded state on header click.
  - Verification of `.queue-list` visibility and row count.
  - Removal of an item from the queue.
  - Auto-hiding when all items are cleared.
