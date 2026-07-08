---
name: pr-review
description: Use when asked to review a GitHub pull request (read the diff, assess correctness, run tests, and post a review). Covers the end-to-end review workflow on GitHub (including self-hosted GitHub Enterprise), including how to post APPROVE / REQUEST_CHANGES reviews and inline comments via the API.
---

# Reviewing a GitHub pull request

This skill describes how to perform a substantive PR review on GitHub (including a self-hosted GitHub Enterprise host). For the underlying `gh api` / host / token mechanics, follow the **`gh`** skill — every API call below assumes those rules.

## 1. Gather context

- Fetch PR metadata: `gh api repos/ORG/REPO/pulls/NUMBER` (title, body, base/head, mergeable state, requested reviewers).
- Fetch the diff: `gh api repos/ORG/REPO/pulls/NUMBER -H "Accept: application/vnd.github.v3.diff"`.
- Read the changed files **in the working tree**, not just the diff, so you see surrounding context the diff omits.
- Check linked issues referenced in the title or body for intent.

## 2. Assess the change

Evaluate, in roughly this order:

1. **Correctness** — does it do what it claims? Trace the actual code paths, including the ones the diff doesn't show. Cross-classloader/type, null, and concurrency edge cases are common misses.
2. **Tests** — is the fixed behavior actually covered by an automated test? A manual-only checkbox for the exact bug being fixed is a gap worth calling out. When practical, **check out the branch and run the tests yourself** to confirm green rather than trusting the description.
3. **Scope & blast radius** — is the change minimal and focused? Flag unrelated changes bundled in.
4. **Clarity** — naming, log levels, comments, and whether observability matches intent (e.g. a "warn" helper that logs at `info` won't surface in CI).

## 3. Write the review

Structure feedback as: a short verdict, then a correctness section, then concrete requested changes (numbered), then non-blocking nits clearly marked as such. Be specific and reference file paths and line context.

## 4. Post the review

Submit a single review with an explicit event, not loose comments. Use `gh api --method POST` with a body file (see the `gh` skill on multiline bodies):

```bash
GH_HOST=github.example.com GITHUB_TOKEN=$(gh auth token --hostname github.example.com) \
  gh api repos/ORG/REPO/pulls/NUMBER/reviews --method POST \
    -f event=REQUEST_CHANGES \
    -f body="$(cat review.md)"
```

- `event` is one of `APPROVE`, `REQUEST_CHANGES`, or `COMMENT`.
- For **inline** comments, add a `comments` array where each entry uses `path` + `position` (the 1-indexed offset from the first `@@` hunk header, continuous across hunks) — **not** `line`+`side`, which some GitHub Enterprise versions reject.

## 5. Etiquette

- Don't approve a PR that contains your own commits — either keep authorship boundaries clean (split your changes into a separate PR) or flag the bundling explicitly and let the author decide.
- Prefer `REQUEST_CHANGES` only for real blockers; use `COMMENT` for advisory feedback so you don't gate on nits.
- After requesting changes, when the author says they've addressed them, re-fetch the diff and re-verify (and re-run tests) before approving.
