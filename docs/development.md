# Development

[← Back to README](../README.md)

## Development

Standard Go tooling works — from a clean checkout:

```bash
go build ./...     # build everything
go test ./...      # run the unit tests
go vet ./...       # static checks
go run . -C .      # run the TUI against the current directory
```

The repo also ships a `Makefile` with convenience targets (`make build`,
`make build-relay`, `make test`, `make vet`, `make fmt`, `make tidy`, …). Note
that the committed Makefile pins a specific module proxy and unsets `GOROOT` for
the sandboxed environment moony is developed in; if you're on a normal machine
with regular internet access, plain `go build`/`go test` (above) is simpler and
works out of the box. There are no non-public module dependencies.

## Tests

```bash
make test    # unit tests: diff, session round-trip, compaction boundary logic
make smoke   # live end-to-end tests against your Ollama server (needs `ollama serve` + a pulled model)
make eval    # run the live agent regression/eval harness on task fixtures
```

Unit tests cover the pure logic (diff generation, session persistence, safe compaction boundaries). The live tests in `internal/agent` are skipped unless `MN_SMOKE=1`:
- `TestAgentSmoke` — drives a real turn that calls `list_dir` and verifies a response.
- `TestApprovalSmoke` — asks the model to create a file, asserts an approval request with a non-empty diff is raised, approves it, and verifies the file is written.

The regression and evaluation harness is run with `make eval`. It drives the live agent through seeded fixture tasks defined in `internal/eval` (each specifying a repository context, prompt, and expected check command), and outputs a scorecard analyzing success-rate, API costs, and turn/model steps.

## Releasing

`make release` cuts a new version in one step: it runs the tests, bumps `config.Version`, prepends a `CHANGELOG.md` section built from the git commit subjects since the last tag (grouped into Added/Changed/Fixed by leading verb), then commits those two files and creates a `vX.Y.Z` tag. It never pushes — do that yourself once you've reviewed.

```bash
make release-dry            # preview the bump + generated CHANGELOG entry
make release                # bump the minor version (default), commit + tag
make release BUMP=patch     # bump the patch version instead
make release VERSION=1.0.0  # set an explicit version
git push origin main --tags # publish when ready
```

The generated CHANGELOG entry is a starting point — edit it before pushing if you want to refine the wording. The top entry always matches `config.Version`, which the `TestChangelogMatchesVersion` check enforces.
