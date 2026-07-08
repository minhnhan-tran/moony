# Global Memory

## Versioning

- If a project doesn't already have a `CHANGELOG.md`, create one. It records the changes made to the codebase over time, and the project's version should be auto-incremented (bumped) alongside each entry added to it.

### Version format

- Use **Semantic Versioning**: `MAJOR.MINOR.PATCH` (e.g. `4.2.40`).
  - **MAJOR** — breaking/incompatible change.
  - **MINOR** — new backward-compatible feature.
  - **PATCH** — bug fix, small tweak, doc-only change.
- Default to a **patch** bump per logical change/commit; reserve minor/major for real feature additions or breaking changes.
- Keep exactly one canonical version constant in code (e.g. `const Version`), and mirror it as the top entry of `CHANGELOG.md` using Keep a Changelog format: `## [x.y.z] — YYYY-MM-DD` with Added/Changed/Fixed sections.
- Where practical, add a test that fails the build if the code constant and the CHANGELOG's top entry disagree (e.g. `TestChangelogMatchesVersion`).
