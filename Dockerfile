# Shared builder — builds both the controller and bundler binaries from the same module.
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG BUNDLER_VERSION=v0.0.0-dev

WORKDIR /workspace

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o manager cmd/main.go

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags "-X github.com/armada/configbundle/internal/version.Version=${BUNDLER_VERSION}" \
    -o bundler cmd/bundler/main.go

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
