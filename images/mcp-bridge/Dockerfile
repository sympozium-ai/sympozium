# MCP Bridge Sidecar
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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /mcp-bridge ./cmd/mcp-bridge

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /mcp-bridge /usr/local/bin/mcp-bridge
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mcp-bridge"]
