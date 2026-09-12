#!/usr/bin/env bash
# Read-only namespace inventory. No proposal, migration, backup or cleanup authority.
set -euo pipefail
umask 077
: "${CELLN_MIGRATION_KUBECONFIG:?explicit absolute kubeconfig required}"
: "${CELLN_MIGRATION_CONTEXT:?explicit context required}"
: "${CELLN_MIGRATION_NAMESPACE:?explicit namespace required}"
[[ "$CELLN_MIGRATION_KUBECONFIG" = /* && -f "$CELLN_MIGRATION_KUBECONFIG" ]] || { echo 'invalid kubeconfig file' >&2; exit 64; }
[[ "$CELLN_MIGRATION_NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#CELLN_MIGRATION_NAMESPACE} -le 63 ]] || { echo 'invalid namespace' >&2; exit 64; }
k() { kubectl --kubeconfig "$CELLN_MIGRATION_KUBECONFIG" --context "$CELLN_MIGRATION_CONTEXT" "$@"; }
root="$(cd "$(dirname "$0")/.." && pwd)"
# --context selects the named context without altering the operator's current context.
# Only GET requests; never fetch Secret objects or write raw responses to disk.
k get agentruntimes.sympozium.ai,cellntools.sympozium.ai,agentruns.sympozium.ai,pods,configmaps,persistentvolumeclaims \
  --namespace "$CELLN_MIGRATION_NAMESPACE" -o json |
  jq -s -f "$root/scripts/lib/celln-migration-inventory.jq"
