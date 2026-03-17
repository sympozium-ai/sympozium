# IPC Bridge Sidecar
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /ipc-bridge ./cmd/ipc-bridge

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /ipc-bridge /ipc-bridge
USER 65532:65532
ENTRYPOINT ["/ipc-bridge"]
