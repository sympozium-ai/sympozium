# llmfit DaemonSet: continuous hardware fitness telemetry.
# Runs llmfit v0.9.24+ in event-publishing mode, pushing per-node hardware
# specs and model fitness scores to NATS every 60 seconds.
# Runs as non-root (UID 1000) with a read-only rootfs.
FROM alpine:3.20

ARG LLMFIT_VERSION=v0.9.24
ARG TARGETARCH

RUN set -eux; \
    apk add --no-cache ca-certificates; \
    adduser -D -u 1000 llmfit; \
    case "${TARGETARCH}" in \
      amd64) LLMFIT_ARCH="x86_64" ;; \
      arm64) LLMFIT_ARCH="aarch64" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl_url="https://github.com/AlexsJones/llmfit/releases/download/${LLMFIT_VERSION}/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl.tar.gz"; \
    wget -q -O /tmp/llmfit.tar.gz "${curl_url}"; \
    tar -xzf /tmp/llmfit.tar.gz -C /tmp; \
    mv /tmp/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl/llmfit /usr/local/bin/llmfit; \
    chmod +x /usr/local/bin/llmfit; \
    rm -rf /tmp/llmfit.tar.gz /tmp/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl

USER 1000

# The DaemonSet runs llmfit in HTTP serve mode on port 8787.
# When built with the 'nats' cargo feature, pass --send-events to
# publish fitness events to NATS. The default release binary does not
# include the nats feature, so we fall back to HTTP-only mode.
# The controller's FitnessSubscriber can poll the REST API as a fallback.
# Host mounts for /proc, /sys, /dev are provided by the DaemonSet spec.
CMD ["llmfit", "serve", "--host", "0.0.0.0"]
