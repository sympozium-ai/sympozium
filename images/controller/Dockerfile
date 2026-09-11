# Controller Manager
#
# This single controller also provisions native Celln parents locally, so it
# embeds the Celln CLI at /usr/local/bin/celln (the path the parent registration
# expects). There is no separate parent-only controller image.
# See docs/design/celln-single-execution-plane.md.
#
# CELLN_IMAGE is the pinned Celln image from config/celln/release.json.
ARG CELLN_IMAGE=ghcr.io/sympozium-ai/celln@sha256:a52d80fe8f492b456f6f47c49190935bff3939465c7189778eb3f3d18e767ef4

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
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w -X main.imageTag=${IMAGE_TAG}" -o /controller ./cmd/controller

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /controller /controller
COPY --from=celln-cli /rootfs/ /
USER 65532:65532
ENTRYPOINT ["/controller"]
