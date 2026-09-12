#!/usr/bin/env bash
# Component-tier tenancy proof. Never represents installed Celln/KVM acceptance.
set -euo pipefail
umask 077

if [[ "${1:-}" != "--components" || $# != 1 ]]; then
  echo 'Only --components is implemented; installed/release qualification is unavailable.' >&2
  exit 64
fi
: "${CELLN_TENANCY_KUBECONFIG:?explicit isolated kubeconfig required}"
: "${CELLN_TENANCY_CONTEXT:?explicit kube context required}"
: "${CELLN_MODEL_BUDGET_DATABASE_URL:?disposable PostgreSQL database required}"
: "${CELLN_TENANCY_CELLN_SOURCE:?explicit Celln source checkout required}"
: "${CELLN_TENANCY_CELLN_SHA:?exact tested Celln commit required}"
[[ "$CELLN_TENANCY_KUBECONFIG" = /* && -f "$CELLN_TENANCY_KUBECONFIG" ]] || { echo 'kubeconfig must be one absolute existing file' >&2; exit 64; }
for tool in git kubectl go cargo jq; do command -v "$tool" >/dev/null || { echo "missing prerequisite: $tool" >&2; exit 64; }; done
export KUBECONFIG="$CELLN_TENANCY_KUBECONFIG"
[[ "$(kubectl config current-context)" = "$CELLN_TENANCY_CONTEXT" ]] || { echo 'kube context mismatch' >&2; exit 64; }
[[ "$(git -C "$CELLN_TENANCY_CELLN_SOURCE" rev-parse HEAD)" = "$CELLN_TENANCY_CELLN_SHA" ]] || { echo 'Celln source pin mismatch' >&2; exit 64; }
[[ -z "$(git -C "$CELLN_TENANCY_CELLN_SOURCE" status --porcelain)" ]] || { echo 'Celln source tree must be clean' >&2; exit 64; }
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
[[ -z "$(git status --porcelain)" ]] || { echo 'Sympozium source tree must be clean' >&2; exit 64; }
cmp test/fixtures/celln-authorisation/v1/bundle/BUNDLE.sha256 "$CELLN_TENANCY_CELLN_SOURCE/tests/fixtures/celln-authorisation/v1/bundle/BUNDLE.sha256" || { echo 'cross-repository fixture pin mismatch' >&2; exit 64; }
kubectl get crd modelconnections.sympozium.ai >/dev/null
out="$(mktemp -d "${TMPDIR:-/tmp}/celln-tenancy-components.XXXXXXXX")"
: > "$out/cases.jsonl"
finish() {
  code=$?
  trap - EXIT
  jq -n --arg tier 'components' --argjson exitCode "$code" \
    --arg source "$(git rev-parse HEAD)" --arg cellnSource "$CELLN_TENANCY_CELLN_SHA" \
    --arg context "$CELLN_TENANCY_CONTEXT" --slurpfile cases "$out/cases.jsonl" \
    '{tier:$tier,exitCode:$exitCode,sympoziumSource:$source,cellnSource:$cellnSource,kubeContext:$context,cases:$cases,installedAcceptance:false,missing:["runtime-admission","native-parent-brokering","enforcing-CNI","KVM-guest-attempts","browser-journeys","real-provider-smoke"]}' > "$out/summary.json"
  echo "Component evidence: $out/summary.json"
  exit "$code"
}
trap finish EXIT
run_case() {
  name="$1"; shift
  code=0
  "$@" > "$out/$name.log" 2>&1 || code=$?
  jq -n --arg name "$name" --argjson exitCode "$code" --arg log "$name.log" \
    '{name:$name,exitCode:$exitCode,log:$log}' >> "$out/cases.jsonl"
  [[ "$code" = 0 ]] || { echo "Failed component case: $name (see evidence log)" >&2; return "$code"; }
}
run_case shared-go go test -race ./cmd/celln-authorisation-fixture ./internal/cellncapability -count=1
run_case shared-rust cargo test --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" -p celln-cli --lib --locked
run_case durable-accounting go test -race ./internal/modelbudget -run Postgres -count=1 -v
export CELLN_GATEWAY_LIVE_KUBERNETES=1
run_case gateway-live-api go test -race ./internal/modelgateway -run 'TestLiveKubernetesGateway|TestPostgres|TestBudgetWatcher|TestAuthorityReadiness' -count=1 -v
