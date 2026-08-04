# moony Makefile
#
# GOROOT is unset on every invocation so the `go` tool uses its own matching
# standard library, in case the environment exports a GOROOT from a different
# Go SDK. Modules resolve through the default public proxy.

BINARY      := moony
BIN_DIR     := bin
PKG         := .
GO          := env -u GOROOT GIT_TERMINAL_PROMPT=0 go

.PHONY: all build build-relay run test smoke vet fmt tidy vendor clean install release release-dry relay tailwind tailwind-check dist publish eval

BUMP ?= minor

# TailwindCSS CLI used to (re)build the vendored web stylesheet. Override if the
# binary lives elsewhere, e.g. `make tailwind TAILWIND=/path/to/tailwindcss`.
# This repo's tailwind.config.js is v3-style and the v4 CLI ignores it (emitting an
# almost-empty stylesheet) instead of failing loudly, so v3 is required. Prefer a
# tailwindcss already on PATH — assumed to be v3, and it keeps offline/air-gapped
# builds working — and fall back to a pinned npx download only when there is none.
TAILWIND ?= $(shell command -v tailwindcss 2>/dev/null || echo npx --yes tailwindcss@3.4.17)

all: build

## build: compile the binary into ./bin
build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) $(PKG)

## build-relay: compile the relay into ./bin/relay (e.g. to run on a privileged
## port with sudo, where `go run` is awkward)
build-relay:
	$(GO) build -o $(BIN_DIR)/relay ./cmd/relay

## sim-decide: build the browser-bot persona brain into ./bin/sim-decide (the
## test/browser Playwright harness invokes it for each simulated human decision)
sim-decide:
	$(GO) build -o $(BIN_DIR)/sim-decide ./cmd/sim-decide

## run: build and launch the TUI
run: build
	./$(BIN_DIR)/$(BINARY)

## relay: run the relay (web on 127.0.0.1:8770; connectors dial 127.0.0.1:8771).
## Fronts the web UI; the agent stays on the connector machine (run the terminal
## with MN_RELAY=host:port to connect). Override the addresses with ADDR=
## (browser) and TUNNEL= (connector); pass extra flags via FLAGS= (e.g. TLS).
relay:
	$(GO) run ./cmd/relay $(if $(ADDR),-addr=$(ADDR),) $(if $(TUNNEL),-tunnel=$(TUNNEL),) $(FLAGS)

## tailwind: regenerate the vendored web stylesheet by scanning the SPA sources.
## Needed after using a NEW Tailwind class anywhere in internal/web/static (the
## config globs *.html and *.js, so all five app-*.js modules count). Requires a
## Tailwind v3 CLI — v4 silently ignores this v3-style config and emits a nearly
## empty stylesheet, so the default pins v3 via npx. The built CSS is committed so
## normal builds don't need this.
##
## Run `make tailwind-check` in CI/after edits to catch classes used in markup that
## have no generated rule — those render as no-ops and are invisible in review.
tailwind:
	$(TAILWIND) -c internal/web/tailwind/tailwind.config.js -i internal/web/tailwind/input.css -o internal/web/static/tailwind.css --minify

## tailwind-check: verify the committed stylesheet is up to date with the sources.
## Regenerates into a temp file and diffs, so a class added to the markup without a
## rebuild fails here instead of silently doing nothing in the browser.
tailwind-check:
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	$(TAILWIND) -c internal/web/tailwind/tailwind.config.js -i internal/web/tailwind/input.css -o $$tmp/tailwind.css --minify >/dev/null 2>&1; \
	if ! diff -q $$tmp/tailwind.css internal/web/static/tailwind.css >/dev/null; then \
		echo "internal/web/static/tailwind.css is stale — run 'make tailwind'"; \
		exit 1; \
	fi; \
	echo "tailwind.css is up to date"

## test: run unit tests (skips the live smoke test)
test:
	$(GO) test ./...

## smoke: run the live end-to-end smoke test (needs auth + network)
smoke:
	MN_SMOKE=1 $(GO) test ./internal/agent -run TestAgentSmoke -v

## eval: run the live agent regression harness (needs auth + network);
## prints a scorecard and exits non-zero if any task fails.
eval:
	$(GO) run ./cmd/eval

## vet: static analysis
vet:
	$(GO) vet ./...

## fmt: format all Go sources
fmt:
	$(GO) fmt ./...

## tidy: resync go.mod/go.sum
tidy:
	$(GO) mod tidy

## install: install the binary into $GOPATH/bin
install:
	$(GO) install $(PKG)

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## release: run tests, then bump config.Version, update CHANGELOG.md from commits
## since the last tag, commit the two files, and create a vX.Y.Z tag (no push).
## Override the bump (BUMP=patch|minor|major) or pin a version (VERSION=1.2.3).
release: test
	$(GO) run ./cmd/release -bump=$(BUMP) $(if $(VERSION),-version=$(VERSION),)

## release-dry: preview the version bump and CHANGELOG entry without changing anything
release-dry:
	$(GO) run ./cmd/release -dry-run -bump=$(BUMP) $(if $(VERSION),-version=$(VERSION),)

## dist: cross-compile release tarballs (+ SHA256SUMS) into ./dist, without uploading
dist:
	./scripts/publish-release.sh --dry-run $(if $(VERSION),--version=$(VERSION),)

## publish: cross-compile and upload a GitHub release (needs gh auth + a pushed vX.Y.Z tag)
publish:
	./scripts/publish-release.sh $(if $(VERSION),--version=$(VERSION),)
