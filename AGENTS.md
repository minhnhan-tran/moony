### Rules

- Always verify code changes (do review if any issues, leak of logic, security then fix them. Then if there is no issue left, make build + make test / go vet on touched packages) before writing the CHANGELOG.md entry and bumping config.Version — don't document unverified behavior.
