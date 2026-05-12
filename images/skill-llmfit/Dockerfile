# Skill sidecar: llmfit
# Provides llmfit + kubectl utilities for node-level model-fit analysis.
# Runs as non-root (UID 1000) with a read-only rootfs.
FROM alpine:3.20

ARG KUBECTL_VERSION=v1.31.4
ARG LLMFIT_VERSION=v0.9.24
ARG TARGETARCH

RUN set -eux; \
    apk add --no-cache bash curl jq ca-certificates; \
    adduser -D -u 1000 agent; \
    case "${TARGETARCH}" in \
      amd64) LLMFIT_ARCH="x86_64" ;; \
      arm64) LLMFIT_ARCH="aarch64" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl; \
    chmod +x /usr/local/bin/kubectl; \
    curl -fsSL "https://github.com/AlexsJones/llmfit/releases/download/${LLMFIT_VERSION}/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl.tar.gz" -o /tmp/llmfit.tar.gz; \
    tar -xzf /tmp/llmfit.tar.gz -C /tmp; \
    mv /tmp/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl/llmfit /usr/local/bin/llmfit; \
    chmod +x /usr/local/bin/llmfit; \
    rm -rf /tmp/llmfit.tar.gz /tmp/llmfit-${LLMFIT_VERSION}-${LLMFIT_ARCH}-unknown-linux-musl

COPY images/skill-llmfit/tool-executor.sh /usr/local/bin/tool-executor.sh
COPY images/skill-llmfit/llmfit-probe-json.sh /usr/local/bin/llmfit-probe-json.sh
COPY images/skill-llmfit/llmfit-cluster-fit.sh /usr/local/bin/llmfit-cluster-fit.sh
RUN chmod +x /usr/local/bin/tool-executor.sh /usr/local/bin/llmfit-probe-json.sh /usr/local/bin/llmfit-cluster-fit.sh

USER 1000
WORKDIR /workspace

# The tool executor watches /ipc/tools/ for exec-request JSON files,
# executes them, and writes exec-result JSON files back.
CMD ["/usr/local/bin/tool-executor.sh"]
