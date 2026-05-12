# API Server with embedded Web UI

# Stage 1: Build frontend
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --ignore-scripts
COPY web/ .
RUN npx vite build

# Stage 2: Build Go binary
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Copy built frontend into the embed directory
COPY --from=frontend /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=mod -ldflags="-s -w" -o /apiserver ./cmd/apiserver

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /apiserver /apiserver
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/apiserver"]
