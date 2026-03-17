# Telegram Channel
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /channel-telegram ./channels/telegram

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /channel-telegram /channel-telegram
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/channel-telegram"]
