# syntax=docker/dockerfile:1.7
#
# Rendezvous server image: `remohop signal`, nothing else. Static binary on `scratch`, runs as
# a non-root user, works with a read-only root filesystem, and carries a health probe.
#
# Build (context is the repository root):
#   docker build -f packaging/docker/Dockerfile -t remohop-signal --build-arg VERSION=v1.2.3 .
# Multi-arch (the Go toolchain cross-compiles, so no QEMU is needed):
#   docker buildx build -f packaging/docker/Dockerfile --platform linux/amd64,linux/arm64 \
#       --build-arg VERSION=v1.2.3 -t ghcr.io/OWNER/remohop-signal:v1.2.3 --push .
# Run: see packaging/docker/README.md and packaging/docker/compose.yaml.

# The toolchain version comes from go.mod's `go` line at build time (GOTOOLCHAIN=auto
# downloads the exact one if the image is older). For fully pinned builds, pass a digest:
#   --build-arg GO_IMAGE=golang:1.26-bookworm@sha256:<digest>
ARG GO_IMAGE=golang:1.26-bookworm

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Same flags as the Makefile's release build (see LDFLAGS there).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    export CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH"; \
    go build -trimpath -ldflags "-s -w -X github.com/readori/RemoHop/internal/cli.Version=$VERSION" -o /out/remohop ./cmd/remohop; \
    go build -trimpath -ldflags "-s -w" -o /out/healthcheck ./packaging/docker/healthcheck; \
    mkdir -p /out/data

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="remohop-signal" \
      org.opencontainers.image.description="RemoHop rendezvous server (remohop signal)" \
      org.opencontainers.image.source="https://github.com/readori/RemoHop" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="$VERSION"
# CA roots: the optional account mail (SMTP over TLS) needs them.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/remohop /remohop
COPY --from=build /out/healthcheck /healthcheck
# The server key and the ID table live here. A named volume copies this ownership.
COPY --from=build --chown=65532:65532 /out/data /data
COPY LICENSE /LICENSE
ENV REMOHOP_CONFIG_DIR=/data
VOLUME /data
USER 65532:65532
# 21116/udp rendezvous, 21117/udp NAT-type detection, 8080/tcp the optional websocket
# bridge (--ws-listen 0.0.0.0:8080), and the relay range if --relay-ports is set.
EXPOSE 21116/udp 21117/udp 8080/tcp
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 CMD ["/healthcheck"]
ENTRYPOINT ["/remohop", "signal", "--data", "/data"]
