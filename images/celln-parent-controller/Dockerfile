# Operator-only co-located parent provisioner. CELLN_IMAGE must be a qualified
# digest-pinned Celln image containing /usr/local/bin/celln and its libraries.
# It is never used as a guest/agent image and requires no KVM device.
# The Celln version/digest pin lives in config/celln/release.json.
ARG CELLN_IMAGE
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG IMAGE_TAG=latest
WORKDIR /workspace
COPY go.mod go.sum ./
# Retry: proxy.golang.org intermittently resets streams in CI.
RUN for i in 1 2 3 4 5; do go mod download && break; [ "$i" = 5 ] && exit 1; \
    echo "go mod download failed (attempt $i/5); retrying in $((i*5))s" >&2; sleep $((i*5)); done
COPY api/ api/
COPY internal/ internal/
COPY pkg/ pkg/
COPY cmd/controller/ cmd/controller/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -trimpath -ldflags="-X main.imageTag=${IMAGE_TAG}" -o /controller ./cmd/controller

FROM ${CELLN_IMAGE}
COPY --from=builder /controller /controller
USER 10001:10001
ENTRYPOINT ["/controller"]
