# Skill sidecar: echotool
# A minimal sample sidecar used to exercise native sidecar tools end to end.
# Ships a tiny `svc-tool` CLI plus the argv-capable tool-executor.
# Runs as non-root (UID 1000) with a read-only rootfs.
FROM alpine:3.20

RUN apk add --no-cache bash jq \
    && adduser -D -u 1000 agent

COPY images/skill-echotool/svc-tool.sh /usr/local/bin/svc-tool
COPY images/skill-echotool/tool-executor.sh /usr/local/bin/tool-executor.sh
RUN chmod +x /usr/local/bin/svc-tool /usr/local/bin/tool-executor.sh

USER 1000
WORKDIR /workspace

CMD ["/usr/local/bin/tool-executor.sh"]
