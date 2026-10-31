#!/usr/bin/env bash
# Integration test: a harness reaches its model through the harness gateway,
# and the real key never enters the run pod (docs/modes/harness-adapters.md).
#
# Requires: a cluster with Sympozium deployed with harnessGateway.enabled, and
# the upstream's origin in harnessGateway.privateOrigins if it is private.
#
#   MEDIATION_HARNESS_IMAGE  digest-pinned image with sh and curl (required)
#   MEDIATION_BASE_URL       OpenAI-compatible upstream, ending in /v1 (required)
#   MEDIATION_PROVIDER       a provider with a profile (default: openai)
#   MEDIATION_MODEL          model name (default: test-model)
#
# In-place resume and successor retry are covered by the controller tests.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
TIMEOUT="${TEST_TIMEOUT:-240}"
NAME="inttest-mediation-$(date +%s)"
PROVIDER="${MEDIATION_PROVIDER:-openai}"
MODEL="${MEDIATION_MODEL:-test-model}"
: "${MEDIATION_HARNESS_IMAGE:?set MEDIATION_HARNESS_IMAGE to a digest-pinned image with sh and curl}"
: "${MEDIATION_BASE_URL:?set MEDIATION_BASE_URL to an OpenAI-compatible upstream}"
REAL_KEY="sentinel-real-key-${NAME}"

pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; FAILED=1; }
FAILED=0

cleanup() {
  kubectl delete agentrun "$NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agent "$NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziumpolicy "$NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# The stand-in fails if the sentinel is anywhere in its env, then calls the model
# through the gateway using only what it was given.
SCRIPT='set -e
if env | grep -q "sentinel-real-key"; then echo "the real key is in the env"; exit 1; fi
curl -sS --fail-with-body --cacert "$NODE_EXTRA_CA_CERTS" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL_NAME\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":1}" \
  "$OPENAI_BASE_URL/chat/completions" >/dev/null
mkdir -p /ipc/output
printf "%s" "{\"status\":\"success\",\"response\":\"mediated\"}" > /ipc/output/result.json'
ARGS="$(jq -cn --arg s "$SCRIPT" '["/bin/sh","-c",$s]')"

kubectl create secret generic "$NAME" -n "$NAMESPACE" --from-literal=OPENAI_API_KEY="$REAL_KEY" >/dev/null
cat <<YAML | kubectl apply -n "$NAMESPACE" -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: ${NAME}
spec:
  harnessPolicy:
    enabled: true
    allowUnmetered: true
    modelMediation: required
---
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${NAME}
spec:
  policyRef: ${NAME}
  authRefs:
    - provider: ${PROVIDER}
      secret: ${NAME}
  agents:
    default:
      model: ${MODEL}
---
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${NAME}
spec:
  agentRef: ${NAME}
  agentId: primary
  sessionKey: mediation-test
  task:
    mode: harness
    parameters:
      prompt: "mediated"
      image: ${MEDIATION_HARNESS_IMAGE}
      args: '${ARGS}'
  model:
    provider: ${PROVIDER}
    model: ${MODEL}
    baseURL: ${MEDIATION_BASE_URL}
    authSecretRef: ${NAME}
  timeout: "3m"
YAML

phase=""
for _ in $(seq 1 "$TIMEOUT"); do
  phase="$(kubectl get agentrun "$NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
  case "$phase" in Succeeded|Failed) break ;; esac
  sleep 1
done
if [ "$phase" = "Succeeded" ]; then
  pass "the harness reached its model through the gateway"
else
  fail "run ended '$phase': $(kubectl get agentrun "$NAME" -n "$NAMESPACE" -o jsonpath='{.status.error}')"
fi

spec="$(kubectl get job "$NAME" -n "$NAMESPACE" -o json 2>/dev/null || true)"
if [ -n "$spec" ] && echo "$spec" | jq -e --arg s "$NAME" '[.. | .secretKeyRef? // empty | select(.name == $s)] | length == 0' >/dev/null; then
  pass "no container references the real key's Secret"
else
  fail "the Job references the real key's Secret, or is gone"
fi

for _ in $(seq 1 30); do
  kubectl get secret "sympozium-run-model-${NAME}" -n "$NAMESPACE" >/dev/null 2>&1 || break
  sleep 1
done
if kubectl get secret "sympozium-run-model-${NAME}" -n "$NAMESPACE" >/dev/null 2>&1; then
  fail "the run token outlived the run"
else
  pass "the run token was deleted"
fi

exit "$FAILED"
