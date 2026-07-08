# syntax=docker/dockerfile:1
# Build + package the moony RELAY (Go). The web UI assets are embedded
# in the binary (go:embed), so the runtime image is just the static binary —
# no venv/pip/source copy is needed.
#
# Ports: 8770 = browser UI (front behind an L7 HTTPS VIP); 8771 = the connector
# tunnel, which is raw TCP (front behind an L4 TCP VIP reachable from laptops).

# ---- build stage ----------------------------------------------------------
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
# Static, CGO-free build. Modules resolve through the default public proxy
# (proxy.golang.org) with checksum-DB verification on. GOTOOLCHAIN=local pins
# the builder image's Go so the build never tries to download a toolchain.
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local
# Pre-warm the module cache with retries BEFORE building, so a transient proxy
# hiccup mid-download (an aborted .zip fetch) doesn't fail the whole build —
# retrying re-pulls just the missing module. Once the cache is warm here, the
# build/cross-build steps below run offline.
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

# SHA256SUMS lets the installer (and humans) verify the downloaded binaries.
RUN (cd /out/dl && sha256sum moony_* > SHA256SUMS)

# ---- runtime stage --------------------------------------------------------
# distroless "static" is the smallest, most-hardened base for a static Go
# binary: no shell or package manager, ships the CA certs the relay needs for
# outbound HTTPS, and the :nonroot tag runs as an unprivileged user (uid 65532),
# satisfying Kubernetes runAsNonRoot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/relay /usr/local/bin/relay
# The macOS CLI binaries the relay offers at /download + /install.sh (public).
COPY --from=build /out/dl /usr/local/share/moony/downloads
# Kube requires runAsNonRoot; the relay writes nothing to disk (stateless), so a
# read-only root filesystem is fine too (set it in the Deployment securityContext).
USER 65532
EXPOSE 8770 8771
ENTRYPOINT ["/usr/local/bin/relay", "-addr=0.0.0.0:8770", "-tunnel=0.0.0.0:8771", "-downloads=/usr/local/share/moony/downloads"]
