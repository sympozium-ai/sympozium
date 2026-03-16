# MCP Bridge Sidecar
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -ldflags="-s -w" -o /mcp-bridge ./cmd/mcp-bridge

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /mcp-bridge /usr/local/bin/mcp-bridge
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mcp-bridge"]
