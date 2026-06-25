#!/bin/bash
# svc-tool — a tiny sample CLI exercised by native sidecar tools in e2e tests.
#
# Usage:
#   svc-tool greet <name>                 # args mode: prints a greeting
#   svc-tool evaluate <id>   (JSON stdin) # stdin mode: echoes id + the stdin payload
#
# It deliberately prints its arguments and stdin verbatim so tests can assert
# that shell metacharacters in argument values are NOT interpreted.
set -euo pipefail

subcommand="${1:-}"
shift || true

# Drop the "--" end-of-options marker the runtime inserts before positional
# values (so a value starting with "-" is treated as an operand).
[[ "${1:-}" == "--" ]] && shift

case "$subcommand" in
    greet)
        name="${1:-world}"
        printf 'hello %s\n' "$name"
        ;;
    evaluate)
        id="${1:-}"
        payload="$(cat || true)"
        # Emit a small JSON result combining the positional id and stdin payload.
        jq -cn --arg id "$id" --argjson payload "${payload:-null}" \
            '{evaluated: $id, received: $payload}'
        ;;
    *)
        echo "svc-tool: unknown subcommand '${subcommand}'" >&2
        exit 2
        ;;
esac
