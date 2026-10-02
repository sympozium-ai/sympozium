# The base supplies only the reviewed curl/sh/native runtime dependencies.
# Every executable below is rebuilt from the recorded local source checkout.
ARG BASE
FROM ${BASE}
COPY celln controller model-gateway review-provider /usr/local/bin/
