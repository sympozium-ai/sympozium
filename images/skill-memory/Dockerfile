# Skill sidecar: memory
# Provides SQLite-backed persistent memory with FTS5 full-text search.
# Communicates with agent-runner via file-based IPC in /ipc/tools/.
# The SQLite database is stored on a PersistentVolume at /data/memory.db.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /memory-server ./cmd/memory-server

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /memory-server /memory-server
USER 65532:65532
ENTRYPOINT ["/memory-server"]
