# API Server with embedded Web UI

# Stage 1: Build frontend
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --ignore-scripts
COPY web/ .
RUN npx vite build

# Stage 2: Build Go binary
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
# Copy built frontend into the embed directory
COPY --from=frontend /web/dist ./web/dist
# Declared late so a new tag does not invalidate the module download layer.
ARG IMAGE_TAG=latest
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w -X main.version=${IMAGE_TAG}" -o /apiserver ./cmd/apiserver

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /apiserver /apiserver
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/apiserver"]
