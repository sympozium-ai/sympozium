# Agent Runner
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
# Retry: proxy.golang.org intermittently resets streams in CI.
RUN for i in 1 2 3 4 5; do go mod download && break; [ "$i" = 5 ] && exit 1; \
    echo "go mod download failed (attempt $i/5); retrying in $((i*5))s" >&2; sleep $((i*5)); done
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /agent-runner ./cmd/agent-runner

# /ipc and /workspace are volumes in a pod. In a Substrate actor there are no
# pod volumes, so they must already exist and be writable by the runner.
RUN mkdir -p /rootfs/ipc /rootfs/workspace

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /agent-runner /agent-runner
COPY --from=builder --chown=65532:65532 /rootfs/ /
USER 65532:65532
ENTRYPOINT ["/agent-runner"]
