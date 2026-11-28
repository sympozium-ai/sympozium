#!/usr/bin/env bash
# Run one lane step, tee its output to <evidence>/logs/<name>.log and append
# "<name>\t<exit>\t<seconds>" to <evidence>/steps.tsv. Exits with the step's
# own status, so the workflow step fails when the journey does.
#
#   hack/celln-lane/run-step.sh <evidence-dir> <name> -- command [args...]
set -uo pipefail
evidence="${1:?evidence dir}" name="${2:?step name}"
shift 2
[ "${1:-}" = -- ] && shift
[ $# -gt 0 ] || { echo "usage: $0 <evidence-dir> <name> -- command..." >&2; exit 2; }
mkdir -p "$evidence/logs"
start="$(date +%s)"
"$@" 2>&1 | tee "$evidence/logs/$name.log"
status="${PIPESTATUS[0]}"
printf '%s\t%s\t%s\n' "$name" "$status" "$(($(date +%s) - start))" >>"$evidence/steps.tsv"
exit "$status"
