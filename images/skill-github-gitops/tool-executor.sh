#!/bin/bash
# tool-executor.sh — entrypoint of the github-gitops skill sidecar.
#
# Prepares the environment the sidecar's commands need, then runs the shared
# tool-executor (pkg/sidecartools/tool-executor.sh, installed in this image as
# /usr/local/bin/sympozium-tool-executor.sh):
#   - Loads GH_TOKEN from the mounted Secret so `gh` is authenticated.
#   - Exports the sympozium_fingerprint() helper for deterministic deduplication hashes.
#   - Raises the executor's timeouts: git and gh operations run longer than the
#     defaults (60s when a request sets none, up to 300s).

set -euo pipefail

SECRET_FILE="/secrets/github-token/GH_TOKEN"

# ---------------------------------------------------------------------------
# Auth — load the GitHub token from the mounted Secret (if present).
# The Secret is managed by the Sympozium API server's auth flow.
# ---------------------------------------------------------------------------
if [[ -f "$SECRET_FILE" ]]; then
    GH_TOKEN="$(cat "$SECRET_FILE")"
    export GH_TOKEN
    echo "[tool-executor] GitHub token loaded from $SECRET_FILE"
else
    echo "[tool-executor] WARNING: $SECRET_FILE not found — gh commands will run unauthenticated"
fi

# ---------------------------------------------------------------------------
# sympozium_fingerprint <string>
#
# Produces a stable 12-character base-36 lowercase identifier for <string>.
# Used to tag GitHub issues/PRs so the agent can detect duplicates.
#
# Usage (inside execute_command calls):
#   FP=$(sympozium_fingerprint "Deployment/my-app/crashloop")
#   gh issue list --label "sympozium-fp:$FP" ...
# ---------------------------------------------------------------------------
sympozium_fingerprint() {
    local input="$1"
    # sha256sum outputs hex; take first 16 hex chars → convert to decimal → format
    # as 12-char base-36.  Pure bash + standard coreutils, no Python needed.
    local hex
    hex=$(printf '%s' "$input" | sha256sum | awk '{print $1}' | head -c 16)
    local dec
    dec=$(printf '%d' "0x${hex}" 2>/dev/null || python3 -c "print(int('${hex}',16))")
    # Convert decimal to base-36 (0-9, a-z).
    local result="" n=$dec
    local charset="0123456789abcdefghijklmnopqrstuvwxyz"
    if [[ $n -eq 0 ]]; then
        result="0"
    else
        while [[ $n -gt 0 ]]; do
            result="${charset:$((n % 36)):1}${result}"
            n=$((n / 36))
        done
    fi
    # Pad / truncate to exactly 12 characters.
    result="${result:0:12}"
    while [[ ${#result} -lt 12 ]]; do result="0${result}"; done
    printf '%s' "$result"
}
# Exported functions reach the executor and every `bash -c` it runs.
export -f sympozium_fingerprint

export TOOL_EXECUTOR_DEFAULT_TIMEOUT="${TOOL_EXECUTOR_DEFAULT_TIMEOUT:-60}"
export TOOL_EXECUTOR_MAX_TIMEOUT="${TOOL_EXECUTOR_MAX_TIMEOUT:-300}"

exec "${SYMPOZIUM_TOOL_EXECUTOR:-/usr/local/bin/sympozium-tool-executor.sh}"
