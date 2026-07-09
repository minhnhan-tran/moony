### Rules

- Always verify code changes (do review if any issues, leak of logic, security then fix them. Then if there is no issue left, make build + make test / go vet on touched packages) before writing the CHANGELOG.md entry and bumping config.Version — don't document unverified behavior.
- Do the CHANGELOG.md entry + config.Version bump with `make release` (BUMP=patch for fixes, minor for features, major for breaking changes; default minor — or VERSION=x.y.z to pin), never by hand: it bumps the const, regenerates the top CHANGELOG.md section from commit subjects since the last `vX.Y.Z` tag, and commits + tags — keeping the two in sync. Preview with `make release-dry`, then review/re-bucket the generated entry (commit subjects use an `area:` prefix, so most land under Changed).
