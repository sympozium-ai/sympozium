# Swaps a harness run token for its model key; the key never enters the run pod.
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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -ldflags="-s -w" -o /harness-gateway ./cmd/harness-gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /harness-gateway /harness-gateway
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/harness-gateway"]
