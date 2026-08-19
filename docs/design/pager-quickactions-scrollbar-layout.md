# Floating Pills (`pager`, `quickActions`, `jobsPill`) Scrollbar Clearance

Status: **Approved**

## Context & Motivation

In the web UI transcript view, three floating controls sit in the lower-right corner above the composer:
1. `#pager` (`.pair-pager`): turn pager (`< N/total >`) and scroll-to-bottom button.
2. `#quickActions` (`.quick-actions`): quick prompt action split button.
3. `#jobsPill` (`.jobs-pill`): background jobs indicator.

Previously, these controls were anchored at `right: calc(.85rem + 10px)` (`~23.6px`). On Windows and Chromium environments (especially at 125%–150% DPI display scaling), the vertical scrollbar track is 17px–22px wide, leaving little to no margin and causing `#pager` to directly overlap the vertical scrollbar track and thumb.

## Goals

1. **Clear vertical scrollbar track**: Ensure that floating pills maintain a clean, unobstructed gutter (`>= 14px`) from the transcript scrollbar on all platforms and DPI settings.
2. **Consistent, shared positioning**: Use a dedicated CSS custom property (`--floating-pills-right`) across `.pair-pager`, `.quick-actions`, and `.jobs-pill`.
3. **Preserve dynamic left-shifting**: Ensure `syncFloatingPills()` continues measuring widths accurately and shifting adjacent pills leftward using `--pill-shift`.

## Implementation Details

### 1. CSS Custom Property & Pill Anchors (`internal/web/static/app.css`)
- Define `--floating-pills-right: calc(1.25rem + 16px);` (approx. `36px`) in the root / pill geometry variables block.
- Update `.pair-pager`, `.jobs-pill`, and `.quick-actions` to reference `right: var(--floating-pills-right, calc(1.25rem + 16px));`.

### 2. Layout Sync (`internal/web/static/app-core.js`)
- `syncFloatingPills()` measures the rendered widths of `#pager`, `#quickActions`, and `#jobsPill` and applies `--pill-shift` to shift pills leftward relative to their common right anchor.

### 3. Verification Plan
- Automated Chrome CDP test (`scripts/verify_floating_pills_clearance.mjs`) verifying:
  - All three pills are visible when active.
  - The rightmost pill is positioned with `>= 32px` offset from the right boundary of the transcript wrap and `>= 14px` clearance from the scrollbar track.
  - No overlap between pills and the scrollbar.
