# Shared builder — builds all four binaries (cb-controller, bundler,
# sc-controller, bc-controller) from the same module.
#
# Pinned to $BUILDPLATFORM so the Go compiler runs natively on the host arch
# (e.g. arm64 on Apple Silicon) and cross-compiles for $TARGETPLATFORM via
# GOOS/GOARCH. Without this, `--platform linux/amd64` builds on an arm64 host
# would QEMU-emulate the compiler itself — ~5-10× slower with no benefit
# (CGO_ENABLED=0 so Go's cross-compiler needs no target-arch toolchain).
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS builder
ARG TARGETOS TARGETARCH
ARG BUNDLER_VERSION=v0.0.0-dev

WORKDIR /workspace

ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o manager cmd/controller/main.go

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build \
    -ldflags "-X github.com/armada/configbundle/internal/version.Version=${BUNDLER_VERSION}" \
    -o bundler cmd/bundler/main.go

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o serverconfig cmd/serverconfig/main.go

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o backupconfig cmd/backupconfig/main.go

# ---- controller image ----
FROM gcr.io/distroless/static:nonroot AS controller
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]

# ---- bundler image ----
FROM gcr.io/distroless/static:nonroot AS bundler
WORKDIR /
COPY --from=builder /workspace/bundler .
USER 65532:65532
ENTRYPOINT ["/bundler"]

# ---- serverconfig-controller image ----
FROM gcr.io/distroless/static:nonroot AS serverconfig
WORKDIR /
COPY --from=builder /workspace/serverconfig .
USER 65532:65532
ENTRYPOINT ["/serverconfig"]

# ---- backupconfig-controller image ----
FROM gcr.io/distroless/static:nonroot AS backupconfig
WORKDIR /
COPY --from=builder /workspace/backupconfig .
USER 65532:65532
ENTRYPOINT ["/backupconfig"]
