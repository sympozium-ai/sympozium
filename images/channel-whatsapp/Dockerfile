# WhatsApp Channel — uses whatsmeow (WhatsApp Web multi-device)
# Credentials are stored in /data/whatsapp.db — mount a PVC here.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /channel-whatsapp ./channels/whatsapp
RUN mkdir -p /data-seed && chown 65532:65532 /data-seed

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /channel-whatsapp /channel-whatsapp
COPY --from=builder --chown=65532:65532 /data-seed /data
USER 65532:65532
VOLUME /data
EXPOSE 3000
ENTRYPOINT ["/channel-whatsapp"]
