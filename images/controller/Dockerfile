# Controller Manager
#
# This single controller also provisions native Celln parents locally, so it
# embeds the Celln CLI at /usr/local/bin/celln (the path the parent registration
# expects). There is no separate parent-only controller image.
# See docs/design/celln-single-execution-plane.md.
#
# CELLN_IMAGE is the pinned Celln image from config/celln/release.json.
ARG CELLN_IMAGE=ghcr.io/sympozium-ai/celln@sha256:d5d1d9890d21fb758bb402bced24eccec817722b3cacddc133b2103f0a9d2210
FROM ${CELLN_IMAGE} AS celln

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
COPY --from=celln /usr/local/bin/celln /usr/local/bin/celln
USER 65532:65532
ENTRYPOINT ["/controller"]
