#!/usr/bin/env bash
# Component-tier tenancy proof. Never represents installed Celln/KVM acceptance.
set -euo pipefail
umask 077

if [[ $# != 1 || ( "${1:-}" != "--components" && "${1:-}" != "--local-kvm" ) ]]; then
  echo 'Use --components or --local-kvm; installed/release qualification is unavailable.' >&2
  exit 64
fi
tier="${1#--}"
: "${CELLN_TENANCY_KUBECONFIG:?explicit isolated kubeconfig required}"
: "${CELLN_TENANCY_CONTEXT:?explicit kube context required}"
: "${CELLN_MODEL_BUDGET_DATABASE_URL:?disposable PostgreSQL database required}"
: "${CELLN_TENANCY_CELLN_SOURCE:?explicit Celln source checkout required}"
: "${CELLN_TENANCY_CELLN_SHA:?exact tested Celln commit required}"
[[ "$CELLN_TENANCY_KUBECONFIG" = /* && -f "$CELLN_TENANCY_KUBECONFIG" ]] || { echo 'kubeconfig must be one absolute existing file' >&2; exit 64; }
for tool in git kubectl go cargo jq; do command -v "$tool" >/dev/null || { echo "missing prerequisite: $tool" >&2; exit 64; }; done
[[ "$(uname -s)" = Linux && -x /usr/bin/curl ]] || { echo 'Linux and /usr/bin/curl required for the actual host relay' >&2; exit 64; }
export KUBECONFIG="$CELLN_TENANCY_KUBECONFIG"
[[ "$(kubectl config current-context)" = "$CELLN_TENANCY_CONTEXT" ]] || { echo 'kube context mismatch' >&2; exit 64; }
[[ "$(git -C "$CELLN_TENANCY_CELLN_SOURCE" rev-parse HEAD)" = "$CELLN_TENANCY_CELLN_SHA" ]] || { echo 'Celln source pin mismatch' >&2; exit 64; }
[[ -z "$(git -C "$CELLN_TENANCY_CELLN_SOURCE" status --porcelain)" ]] || { echo 'Celln source tree must be clean' >&2; exit 64; }
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
[[ -z "$(git status --porcelain)" ]] || { echo 'Sympozium source tree must be clean' >&2; exit 64; }
cmp test/fixtures/celln-authorisation/v1/bundle/BUNDLE.sha256 "$CELLN_TENANCY_CELLN_SOURCE/tests/fixtures/celln-authorisation/v1/bundle/BUNDLE.sha256" || { echo 'cross-repository fixture pin mismatch' >&2; exit 64; }
cmp test/fixtures/celln-model-requests/v1.json "$CELLN_TENANCY_CELLN_SOURCE/tests/fixtures/celln-model-requests/v1.json" || { echo 'model-request fixture mismatch' >&2; exit 64; }
kubectl get crd modelconnections.sympozium.ai >/dev/null
out="$(mktemp -d "${TMPDIR:-/tmp}/celln-tenancy-components.XXXXXXXX")"
: > "$out/cases.jsonl"
finish() {
  code=$?
  trap - EXIT
  jq -n --arg tier "$tier" --argjson exitCode "$code" \
    --arg source "$(git rev-parse HEAD)" --arg cellnSource "$CELLN_TENANCY_CELLN_SHA" \
    --arg context "$CELLN_TENANCY_CONTEXT" --slurpfile cases "$out/cases.jsonl" \
    '{tier:$tier,exitCode:$exitCode,sympoziumSource:$source,cellnSource:$cellnSource,kubeContext:$context,cases:$cases,installedAcceptance:false,missing:["runtime-admission","native-parent-brokering","enforcing-CNI","mediated-KVM-credential-attempts","browser-journeys","real-provider-smoke"]}' > "$out/summary.json"
  echo "Component evidence: $out/summary.json"
  exit "$code"
}
trap finish EXIT
run_case() {
  name="$1"; shift
  code=0
  "$@" > "$out/$name.log" 2>&1 || code=$?
  if [[ "$1" = go && "${2:-}" = test && "$code" = 0 ]]; then
    jq -s -e 'any(.[]; .Action == "pass" and .Test != null) and all(.[]; .Action != "skip")' "$out/$name.log" >/dev/null || code=65
  fi
  jq -n --arg name "$name" --argjson exitCode "$code" --arg log "$name.log" \
    '{name:$name,exitCode:$exitCode,log:$log}' >> "$out/cases.jsonl"
  [[ "$code" = 0 ]] || { echo "Failed component case: $name (see evidence log)" >&2; return "$code"; }
}
run_case shared-go go test -json -race ./cmd/celln-authorisation-fixture ./internal/cellncapability -count=1
run_case model-request-go go test -json -race ./internal/modelgateway -run '^Test(SharedModelRequestCanonicalVectors|ModelRequestCanonicalBounds)$' -count=1
run_case shared-rust cargo test --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" -p celln-cli --lib --locked
run_case host-control cargo test --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" -p celln-control --lib --locked
run_case host-broker cargo test --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" -p celln-warden --lib egress --locked
run_case host-relay-build cargo build --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" -p celln-cli --example tenancy-gateway-probe --locked
relay_target="$(cargo metadata --manifest-path "$CELLN_TENANCY_CELLN_SOURCE/Cargo.toml" --no-deps --format-version 1 --locked | jq -er .target_directory)"
export CELLN_GATEWAY_RELAY_PROBE="$relay_target/debug/examples/tenancy-gateway-probe"
[[ -x "$CELLN_GATEWAY_RELAY_PROBE" ]] || { echo 'built Rust relay probe unavailable' >&2; exit 64; }
run_case durable-accounting go test -json -race ./internal/modelbudget -run Postgres -count=1 -v
export CELLN_GATEWAY_LIVE_KUBERNETES=1
run_case gateway-live-api go test -json -race ./internal/modelgateway -run 'TestLiveKubernetesGatewayTenantCredentialIsolation|TestPostgres|TestBudgetWatcher|TestAuthorityReadiness' -count=1 -v
run_case required-live-case jq -s -e 'any(.[]; .Action == "pass" and .Test == "TestLiveKubernetesGatewayTenantCredentialIsolation")' "$out/gateway-live-api.log"
run_case gateway-build go build -o "$out/model-gateway" ./cmd/model-gateway
export CELLN_GATEWAY_TEST_BINARY="$out/model-gateway" CELLN_GATEWAY_PROCESS=1
run_case gateway-process go test -json -race ./internal/modelgateway -run '^TestLiveKubernetesGatewayProcessTenantCredentialIsolation$' -count=1 -v
run_case required-process-case jq -s -e 'any(.[]; .Action == "pass" and .Test == "TestLiveKubernetesGatewayProcessTenantCredentialIsolation")' "$out/gateway-process.log"
run_case required-relay-cases jq -s -e '. as $events | all(["TestPostgresGatewayTenantCredentialIsolation", "TestLiveKubernetesGatewayTenantCredentialIsolation", "TestLiveKubernetesGatewayProcessTenantCredentialIsolation"][]; . as $root | all(["a", "b"][]; . as $tenant | all(["", "/untrusted-ca", "/redirect", "/credential-echo", "/cancellation"][]; . as $case | any($events[]; .Action == "pass" and .Test == ($root + "/" + $tenant + "/RustHostRelay" + $case)))))' "$out/gateway-live-api.log" "$out/gateway-process.log"
if [[ "$tier" = local-kvm ]]; then
  [[ -x "$CELLN_TENANCY_CELLN_SOURCE/scripts/conformance-kvm.sh" ]] || { echo 'strict Celln KVM runner required' >&2; exit 64; }
  run_case local-kvm make -C "$CELLN_TENANCY_CELLN_SOURCE" conformance-kvm
fi
