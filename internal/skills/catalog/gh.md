---
name: gh
description: Use when calling the GitHub API or `gh` CLI against Apple's internal GitHub Enterprise (github.pie.apple.com or github.geo.apple.com) — covers token/host handling, avoiding silent routing to api.github.com, and the TLS/quoting gotchas that break naive `gh` and `curl` commands.
---

# GitHub on Apple GitHub Enterprise (GHE)

These rules apply to both `github.pie.apple.com` and `github.geo.apple.com`. They exist because the default `gh`/`curl` behavior silently targets the wrong host or mangles auth, producing confusing failures.

## Authenticating `gh api` calls

Always inline `GH_HOST` and `GITHUB_TOKEN` on the **same line** as `gh api`. Assigning the token on a separate line loses it from the subprocess environment:

```bash
# Correct — token is captured in the same command environment
GH_HOST=github.pie.apple.com GITHUB_TOKEN=$(gh auth token --hostname github.pie.apple.com) \
  gh api repos/ORG/REPO/pulls

# Wrong — TOKEN is not exported into the gh subprocess
TOKEN=$(gh auth token --hostname github.pie.apple.com)
GH_HOST=github.pie.apple.com GITHUB_TOKEN=$TOKEN gh api ...
```

Swap in `github.geo.apple.com` wherever the repo lives on the geo host.

## Always set `GH_HOST`

Without `GH_HOST`, `gh api` silently routes to `api.github.com` and either fails auth or returns data for the wrong host. Always include it.

## Don't use `curl` for the GitHub API

TLS certificate handling for `curl` against GHE is unreliable. Use `gh api`, or `web_fetch` with an `Authorization: token <token>` header, instead.

## Avoid `gh pr create` with a multiline body

The shell mangles newlines in `--body`. To create or comment on PRs/issues with multi-paragraph bodies, use `gh api --method POST` with `-f` fields (or `--input` with a JSON file) instead of `gh pr create`.

```bash
GH_HOST=github.pie.apple.com GITHUB_TOKEN=$(gh auth token --hostname github.pie.apple.com) \
  gh api --method POST repos/ORG/REPO/issues/NUMBER/comments -f body="$(cat body.md)"
```

## Inline review comments use `position`, not `line`+`side`

For PR review inline comments, use the `position` field — the 1-indexed offset from the first `@@` hunk header, counted continuously across hunks. The `line`+`side` form only works on github.com and is rejected by this GHE version.
