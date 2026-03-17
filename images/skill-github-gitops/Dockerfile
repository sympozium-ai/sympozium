FROM alpine:3.21

ARG GH_VERSION=2.65.0
ARG TARGETOS=linux
ARG TARGETARCH

RUN apk add --no-cache \
    bash \
    curl \
    git \
    jq \
    openssh-client \
    ca-certificates \
    tar \
    && true

# Install the GitHub CLI (gh).
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) GH_ARCH="amd64" ;; \
        arm64) GH_ARCH="arm64" ;; \
        arm)   GH_ARCH="armv6" ;; \
        *)     echo "Unsupported arch: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl -fsSL \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_${TARGETOS}_${GH_ARCH}.tar.gz" \
        -o /tmp/gh.tar.gz; \
    tar -xzf /tmp/gh.tar.gz -C /tmp; \
    install -m 0755 /tmp/gh_${GH_VERSION}_${TARGETOS}_${GH_ARCH}/bin/gh /usr/local/bin/gh; \
    rm -rf /tmp/gh.tar.gz /tmp/gh_${GH_VERSION}_${TARGETOS}_${GH_ARCH}

# Create a non-root user for the sidecar.
RUN addgroup -g 1000 sympozium && \
    adduser -u 1000 -G sympozium -s /bin/bash -D sympozium && \
    mkdir -p /workspace /ipc/tools && \
    chown -R sympozium:sympozium /workspace /ipc

COPY images/skill-github-gitops/tool-executor.sh /usr/local/bin/tool-executor.sh
RUN chmod +x /usr/local/bin/tool-executor.sh

USER 1000

WORKDIR /workspace

CMD ["/usr/local/bin/tool-executor.sh"]
