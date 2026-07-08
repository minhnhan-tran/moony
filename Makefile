# moony Makefile
#
# GOROOT is unset on every invocation so the `go` tool uses its own matching
# standard library, in case the environment exports a GOROOT from a different
# Go SDK. Modules resolve through the default public proxy.

BINARY      := moony
BIN_DIR     := bin
PKG         := .
GO          := env -u GOROOT GIT_TERMINAL_PROMPT=0 go

.PHONY: all build build-relay run test smoke vet fmt tidy vendor clean install release release-dry relay tailwind dist publish eval

BUMP ?= minor

# TailwindCSS CLI used to (re)build the vendored web stylesheet. Override if the
# binary lives elsewhere, e.g. `make tailwind TAILWIND=/path/to/tailwindcss`.
TAILWIND ?= tailwindcss

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
## with MOONY_RELAY=host:port to connect). Override the addresses with ADDR=
## (browser) and TUNNEL= (connector); pass extra flags via FLAGS= (e.g. TLS).
relay:
	$(GO) run ./cmd/relay $(if $(ADDR),-addr=$(ADDR),) $(if $(TUNNEL),-tunnel=$(TUNNEL),) $(FLAGS)

## tailwind: regenerate the vendored web stylesheet from index.html. Needed only
## after changing Tailwind classes in internal/web/static/index.html. Requires a
## Tailwind v3 CLI on PATH (or pass TAILWIND=...); the built CSS is committed so
## normal builds don't need it.
tailwind:
	$(TAILWIND) -c internal/web/tailwind/tailwind.config.js -i internal/web/tailwind/input.css -o internal/web/static/tailwind.css --minify

## test: run unit tests (skips the live smoke test)
test:
	$(GO) test ./...

## smoke: run the live end-to-end smoke test (needs auth + network)
smoke:
	MOONY_SMOKE=1 $(GO) test ./internal/agent -run TestAgentSmoke -v

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

## ship: one-command [rio: build] release — stage your files first, then run this.
## Signs (ac-sign biometry), commits, pushes to main, and publishes in one shot,
## instead of ~20 agent round-trips. MESSAGE="..." required; EMPTY=1 for a
## trigger-only commit; VERSION=x.y.z to pin the published version.
ship:
	./scripts/ship.sh -m "$(MESSAGE)" $(if $(EMPTY),--empty,) $(if $(VERSION),--version=$(VERSION),)
