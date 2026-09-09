#!/usr/bin/env bash
# Explicit, billable catalogue proof against the existing isolated Kind cluster.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
: "${CELLN_CONTROLLER_KUBECONFIG:?explicit isolated kubeconfig required}"
: "${CELLN_COMPOSITION_FIXTURE:?public signed catalogue fixture required}"
: "${CELLN_COMPOSITION_BINARY:?actual Celln binary required}"
if [[ ${CELLN_LIVE_OPERATOR_ADMISSION:-} != 1 ]]; then
  : "${CELLN_ISSUANCE_MATERIALIZER:?explicit public-fixture materializer required}"
fi
: "${CELLN_HARNESS_PACKAGE:?native JSON Harness package required}"
: "${CELLN_LIVE_EVIDENCE_PARENT:?existing absolute evidence parent required}"
[[ ${CELLN_PAUSE_TEST_CONTROLLER:-} == 1 ]] || { echo 'Explicit isolated test-controller pause required' >&2; exit 1; }
[[ -n ${CELLN_LIVE_CREDENTIAL_FILE:-} || -n ${CELLN_LIVE_DEEPSEEK_ZSHRC:-} ]] || { echo 'Explicit host DeepSeek credential source required' >&2; exit 1; }
kc() { kubectl --kubeconfig "$CELLN_CONTROLLER_KUBECONFIG" --context kind-celln-deployed "$@"; }
[[ $(kubectl --kubeconfig "$CELLN_CONTROLLER_KUBECONFIG" config current-context) == kind-celln-deployed ]]
work=$(mktemp -d /tmp/sympozium-catalogue-proof.XXXXXX)
paused=false
paused_uid=''
cleanup() {
  if [[ "$paused" == true ]]; then
    [[ $(kc get deployment harness-proof-controller -n sympozium-system -o jsonpath='{.metadata.uid}') == "$paused_uid" ]] || { echo 'Controller identity changed; refusing restoration' >&2; return 1; }
    kc scale deployment harness-proof-controller -n sympozium-system --current-replicas=0 --replicas=1
    kc rollout status deployment harness-proof-controller -n sympozium-system --timeout=60s
  fi
  case "$work" in /tmp/sympozium-catalogue-proof.*) rm -r -- "$work" ;; esac
}
trap cleanup EXIT
trap 'exit 130' INT TERM
# Build before pausing the existing controller. No ambient cluster deployment.
(cd "$repo" && go build -o "$work/sympozium" ./cmd/sympozium && go build -o "$work/controller" ./cmd/controller)
kc get agentruns -A -o json | jq -e 'all(.items[]; .status.phase=="Succeeded" or .status.phase=="Failed" or .status.phase=="Cancelled")' >/dev/null
state=$(kc get deployment harness-proof-controller -n sympozium-system -o json)
jq -e '.spec.replicas==1 and (.spec.template.spec.containers[0].image|startswith("localhost/sympozium-celln-controller:"))' <<<"$state" >/dev/null
paused_uid=$(jq -r '.metadata.uid' <<<"$state")
paused=true
kc scale deployment harness-proof-controller -n sympozium-system --current-replicas=1 --replicas=0
deadline=$((SECONDS+60))
until kc get deployment harness-proof-controller -n sympozium-system -o json | jq -e '(.status.replicas // 0)==0 and .status.observedGeneration>=.metadata.generation' >/dev/null; do
  (( SECONDS < deadline )) || { echo 'Proof controller did not stop' >&2; exit 1; }
  sleep 1
done
export CELLN_LIVE_CATALOGUE=1
export CELLN_LIVE_SYMPOZIUM_BINARY="$work/sympozium"
export CELLN_LIVE_CONTROLLER_BINARY="$work/controller"
cd "$repo"
test_timeout=10m
if [[ ${CELLN_LIVE_INTERACTIVE:-} == 1 ]]; then test_timeout=9h; fi
go test -race ./test/integration/celln-catalogue-setup -run '^TestLiveCatalogueHarness$' -count=1 -v -timeout="$test_timeout"
