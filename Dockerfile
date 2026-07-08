# syntax=docker/dockerfile:1
# Build + package the moony RELAY (Go). The web UI assets are embedded
# in the binary (go:embed), so the runtime image is just the static binary —
# no venv/pip/source copy like the Python sqeqct images.
#
# Ports: 8770 = browser UI (front behind an L7 HTTPS VIP); 8771 = the connector
# tunnel, which is raw TCP (front behind an L4 TCP VIP reachable from laptops).

# ---- build stage ----------------------------------------------------------
FROM docker.apple.com/base-images/ubi10/go1.26-builder AS build
WORKDIR /src
COPY . .
# Go env for a credential-less, egress-restricted build: fetch all deps through
# Apple's Athens module proxy, which mirrors public github.com modules over
# HTTPS. No "|direct" fallback on purpose: this build network blocks non-Apple
# hosts, so a direct git/github.com fetch would only hang or fail;
# Athens carries every dependency (it auto-mirrors public modules on first
# request), so an Athens miss should fail fast and loudly rather than fall back to
# a blocked host. GONOPROXY=none keeps even the private modules on the proxy.
# GOTOOLCHAIN=local pins the builder's Go (no toolchain download attempt).
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    GOPROXY=https://athens.apple.com \
    GOSUMDB=off \
    GOPRIVATE=*.apple.com \
    GONOPROXY=none
# Pre-warm the module cache with retries BEFORE building. The Athens proxy
# intermittently resets an HTTP/2 stream mid-download ("stream error: ...
# NO_ERROR; received from peer"), which aborts a single module's .zip fetch and
# fails the whole `go build`. Retrying re-pulls just the missing zip (the rest
# stay cached), so a transient proxy hiccup no longer breaks the image build.
# Once the cache is warm here, every build/cross-build step below runs offline.
RUN n=0; until go mod download; do \
      n=$((n+1)); \
      if [ "$n" -ge 5 ]; then echo "go mod download failed after $n attempts" >&2; exit 1; fi; \
      echo "go mod download failed (attempt $n/5); retrying in 3s..." >&2; \
      sleep 3; \
    done
RUN go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

# Cross-compile the CLI (the "terminal": TUI + connector) for the platforms the
# relay offers at /download + /install.sh — macOS for laptops, Linux for running
# the agent inside containers/CI. CGO is already off, so these cross-builds work
# from this Linux builder. SHA256SUMS lets the installer (and humans) verify.
RUN for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
      os="${t%/*}"; arch="${t#*/}"; \
      GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w" \
        -o "/out/dl/moony_${os}_${arch}" . ; \
    done

# Bundle the vendored Apple Fetch runtime (macOS-only) so /install.sh can drop it
# in ~/.sqe/apple-fetch — apple_fetch/apple_search then work without an Optimus
# install. The tarball preserves the framework symlinks; the codesign marker is
# stripped so the installed copy re-signs on first use. SHA256SUMS covers it too.
RUN rm -f assets/apple-fetch/AppleFetchMCP.app/.sqe-codesigned 2>/dev/null || true; \
    tar -C assets/apple-fetch -czf /out/dl/apple-fetch_darwin.tar.gz AppleFetchMCP.app \
 && (cd /out/dl && sha256sum moony_* apple-fetch_darwin.tar.gz > SHA256SUMS)

# ---- runtime stage --------------------------------------------------------
# ubi10/ubi-builder is the generic builder base (no Go toolchain), so it's
# leaner than reusing go1.26-builder, and like all ubi bases it carries the CA
# certs the relay needs for outbound HTTPS to IdMS (idmsac.apple.com). It's still
# a builder image (has build tools); swap in a true ubi-minimal/ubi-micro once
# that tag is confirmed for the smallest, most-hardened runtime.
FROM docker.apple.com/base-images/ubi10/ubi-builder:latest
COPY --from=build /out/relay /usr/local/bin/relay
# The macOS CLI binaries the relay offers at /download + /install.sh (public).
COPY --from=build /out/dl /usr/local/share/moony/downloads
# Kube requires runAsNonRoot; the relay writes nothing to disk (stateless), so a
# read-only root filesystem is fine too (set it in the Deployment securityContext).
USER 1000
EXPOSE 8770 8771
ENTRYPOINT ["/usr/local/bin/relay", "-addr=0.0.0.0:8770", "-tunnel=0.0.0.0:8771", "-downloads=/usr/local/share/moony/downloads"]
