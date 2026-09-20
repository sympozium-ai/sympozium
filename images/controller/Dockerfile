# Controller Manager
#
# This single controller also provisions native Celln parents locally, so it
# embeds the Celln CLI at /usr/local/bin/celln (the path the parent registration
# expects). There is no separate parent-only controller image.
# See docs/design/celln-single-execution-plane.md.
#
# CELLN_IMAGE is the pinned Celln image from config/celln/release.json.
ARG CELLN_IMAGE=ghcr.io/sympozium-ai/celln@sha256:471d5831151f349d4133c309bb0d8cddaff83268bcaa36728556d597b0cc6681

# The Celln CLI is amd64-only: its KVM backend has no ARM64 ABI, so Celln
# publishes no arm64 bundle. Native parent provisioning runs only on the amd64
# KVM host, so stage the CLI behind an arch gate and carry it in the multi-arch
# controller image only where it can run. Pin the source stage to amd64 so the
# digest resolves on any build host.
FROM --platform=linux/amd64 ${CELLN_IMAGE} AS celln
FROM --platform=$BUILDPLATFORM alpine:3.20 AS celln-cli
ARG TARGETARCH
COPY --from=celln /usr/local/bin/celln /celln
RUN mkdir -p /rootfs/usr/local/bin && \
    if [ "${TARGETARCH}" = "amd64" ]; then cp /celln /rootfs/usr/local/bin/celln; fi

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG IMAGE_TAG=latest
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
# Retry: proxy.golang.org intermittently resets streams in CI.
RUN for i in 1 2 3 4 5; do go mod download && break; [ "$i" = 5 ] && exit 1; \
    echo "go mod download failed (attempt $i/5); retrying in $((i*5))s" >&2; sleep $((i*5)); done
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w -X main.imageTag=${IMAGE_TAG}" -o /controller ./cmd/controller
# TLS edge for a fleet node's scoped receiver (celln.mediation): the chart runs
# it from this image as an unprivileged sidecar beside the plaintext dispatcher.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /celln-parent-proxy ./cmd/celln-parent-proxy

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /controller /controller
COPY --from=builder /celln-parent-proxy /celln-parent-proxy
COPY --from=celln-cli /rootfs/ /
USER 65532:65532
ENTRYPOINT ["/controller"]
