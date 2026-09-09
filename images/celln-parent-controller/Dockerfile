# Operator-only co-located parent provisioner. CELLN_IMAGE must be a qualified
# digest-pinned Celln image containing /usr/local/bin/celln and its libraries.
# It is never used as a guest/agent image and requires no KVM device.
ARG CELLN_IMAGE
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY pkg/ pkg/
COPY cmd/controller/ cmd/controller/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -trimpath -o /controller ./cmd/controller

FROM ${CELLN_IMAGE}
COPY --from=builder /controller /controller
USER 10001:10001
ENTRYPOINT ["/controller"]
