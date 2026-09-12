# Dedicated Celln model credential boundary; never deployed as the web proxy.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -ldflags="-s -w" -o /model-gateway ./cmd/model-gateway

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /model-gateway /model-gateway
USER 65532:65532
EXPOSE 8443
ENTRYPOINT ["/model-gateway"]
