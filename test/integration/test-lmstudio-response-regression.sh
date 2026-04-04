#!/usr/bin/env bash
# Integration test: LM Studio response-propagation regression guard.
#
# Proves that `.status.result` is populated (non-empty) across three flows:
#   A) PersonaPack activation → stamped instance → dispatched AgentRun
#   B) Ad-hoc SympoziumInstance + AgentRun with tool-calling (k8s-ops skill)
#   C) Ad-hoc SympoziumInstance with multiple (3) sequential AgentRuns
#
# This guards against the regression where the tool-call circuit breaker
# returned an error with empty response even after LM Studio executed.
#
# Prerequisites:
#   - Kind cluster with Sympozium deployed (make install)
#   - LM Studio running on host at port 1234 with at least one model loaded
#   - host.docker.internal reachable from the kind cluster

set -uo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
SYSTEM_NS="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
TIMEOUT="${TEST_TIMEOUT:-420}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass()  { echo -e "${GREEN}✓ $*${NC}"; }
fail()  { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info()  { echo -e "${YELLOW}● $*${NC}"; }

EXIT_CODE=0
SUFFIX="$(date +%s)"

# ── Preflight ─────────────────────────────────────────────────────────────────

info "Regression test starting (namespace=${NAMESPACE}, suffix=${SUFFIX})"

# 1. LM Studio reachable + pick a small model.
lms_raw="$(kubectl run "lms-probe-${SUFFIX}" --rm -i --restart=Never \
  --image=curlimages/curl --quiet -- curl -s --connect-timeout 5 \
  http://host.docker.internal:1234/v1/models 2>/dev/null || true)"
LMS_MODEL="$(echo "$lms_raw" | python3 -c "
import sys, json
raw = sys.stdin.read()
try:
    start = raw.index('{')
    data, _ = json.JSONDecoder().raw_decode(raw, start)
except Exception:
    sys.exit(0)
models = [m['id'] for m in data.get('data', []) if 'embed' not in m['id'].lower()]
# Prefer qwen3.5-9b explicitly (user's regression target); else pick anything small.
for pref in ['qwen/qwen3.5-9b', 'qwen3.5-9b']:
    if pref in models:
        print(pref); sys.exit(0)
prefer_hints = ['9b', '8b', '7b', '4b', '3b', '1b', 'nano', 'lite', 'mini', 'small']
for hint in prefer_hints:
    for m in models:
        if hint in m.lower():
            print(m); sys.exit(0)
print(models[0] if models else '')
" 2>/dev/null || true)"
if [[ -z "$LMS_MODEL" ]]; then
  fail "LM Studio unreachable at host.docker.internal:1234 or no models loaded"
  exit 1
fi
pass "LM Studio reachable — using model '${LMS_MODEL}'"

# 2. NetworkPolicy must allow egress to port 1234.
if kubectl get networkpolicy sympozium-agent-allow-egress -n "$NAMESPACE" >/dev/null 2>&1; then
  if ! kubectl get networkpolicy sympozium-agent-allow-egress -n "$NAMESPACE" \
      -o jsonpath='{.spec.egress[*].ports[*].port}' 2>/dev/null | grep -q 1234; then
    kubectl patch networkpolicy sympozium-agent-allow-egress -n "$NAMESPACE" --type='json' \
      -p='[{"op":"add","path":"/spec/egress/-","value":{"ports":[{"port":1234,"protocol":"TCP"}]}}]' >/dev/null 2>&1
    pass "Patched NetworkPolicy to allow egress on port 1234"
  fi
fi

# 3. Confirm the freshly-loaded agent-runner image is present on the kind node.
if docker exec kind-control-plane crictl images 2>/dev/null | grep -q "sympozium/agent-runner"; then
  pass "agent-runner image present on kind node"
else
  info "agent-runner image check skipped (could not exec into kind-control-plane)"
fi

# ── Shared resources ──────────────────────────────────────────────────────────

SHARED_SECRET="lms-regression-key-${SUFFIX}"
kubectl create secret generic "$SHARED_SECRET" \
  --from-literal=API_KEY=lm-studio-no-key-needed \
  -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1

# track all resources we create for cleanup
RESOURCES_AGENTRUN=()
RESOURCES_INSTANCE=()
RESOURCES_PERSONAPACK=()
RESOURCES_SECRET=("$SHARED_SECRET")

cleanup() {
  set +u
  info "Cleaning up regression test resources..."
  for r in "${RESOURCES_AGENTRUN[@]}"; do
    [[ -n "$r" ]] && kubectl delete agentrun "$r" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  for r in "${RESOURCES_INSTANCE[@]}"; do
    [[ -n "$r" ]] && kubectl delete sympoziuminstance "$r" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  for r in "${RESOURCES_PERSONAPACK[@]}"; do
    [[ -n "$r" ]] && kubectl delete personapack "$r" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  for r in "${RESOURCES_SECRET[@]}"; do
    [[ -n "$r" ]] && kubectl delete secret "$r" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  set -u
}
trap cleanup EXIT

# Wait for an AgentRun to reach a terminal phase.
wait_for_run() {
  local run="$1" elapsed=0 phase=""
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    phase="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
      echo "$phase"
      return 0
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  echo "Timeout"
  return 1
}

# Assert the run succeeded AND status.result is non-empty.
assert_nonempty_result() {
  local run="$1" label="$2"
  local phase result err pod
  phase="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo '')"
  result="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo '')"
  err="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.error}' 2>/dev/null || echo '')"
  pod="$(kubectl get agentrun "$run" -n "$NAMESPACE" -o jsonpath='{.status.podName}' 2>/dev/null || echo '')"
  if [[ "$phase" != "Succeeded" ]]; then
    fail "${label}: phase='${phase}' (expected Succeeded)"
    [[ -n "$err" ]] && info "   status.error: ${err}"
    if [[ -n "$pod" ]]; then
      info "   --- last 20 lines of agent container logs (pod=${pod}) ---"
      kubectl logs "$pod" -n "$NAMESPACE" -c agent 2>/dev/null | tail -20 | sed 's/^/   /' || true
    fi
    return 1
  fi
  if [[ -z "$result" ]]; then
    fail "${label}: phase=Succeeded but status.result is EMPTY (regression!)"
    if [[ -n "$pod" ]]; then
      info "   --- last 20 lines of agent container logs (pod=${pod}) ---"
      kubectl logs "$pod" -n "$NAMESPACE" -c agent 2>/dev/null | tail -20 | sed 's/^/   /' || true
    fi
    return 1
  fi
  pass "${label}: Succeeded with ${#result}-char response (first 80 chars: $(echo "$result" | head -c 80 | tr '\n' ' '))"
  return 0
}

# ═══════════════════════════════════════════════════════════════════════════════
# SCENARIO B: Ad-hoc SympoziumInstance + AgentRun with execute_command tool
# ═══════════════════════════════════════════════════════════════════════════════

info ""
info "── Scenario B: ad-hoc instance with tool-calling command ─────────────"

B_INSTANCE="lms-regr-cmd-${SUFFIX}"
B_RUN="${B_INSTANCE}-run"
RESOURCES_INSTANCE+=("$B_INSTANCE")
RESOURCES_AGENTRUN+=("$B_RUN")

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ${B_INSTANCE}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: ${LMS_MODEL}
      baseURL: "http://host.docker.internal:1234/v1"
  authRefs:
    - provider: lm-studio
      secret: ${SHARED_SECRET}
  skills:
    - skillPackRef: k8s-ops
EOF

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${B_RUN}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${B_INSTANCE}
spec:
  instanceRef: ${B_INSTANCE}
  agentId: default
  sessionKey: "regr-B-${SUFFIX}"
  task: |
    Use the execute_command tool to run: echo REGRESSION_OK_B
    After you receive the command output, reply with a short summary
    confirming what the command printed. Do not call any other tools.
  model:
    provider: lm-studio
    model: ${LMS_MODEL}
    baseURL: "http://host.docker.internal:1234/v1"
    authSecretRef: ${SHARED_SECRET}
  skills:
    - skillPackRef: k8s-ops
  timeout: "6m"
EOF

pass "Scenario B: dispatched AgentRun ${B_RUN}"
info "Scenario B: waiting for completion (up to ${TIMEOUT}s)..."
B_PHASE="$(wait_for_run "$B_RUN")" || true
assert_nonempty_result "$B_RUN" "Scenario B"

# ═══════════════════════════════════════════════════════════════════════════════
# SCENARIO C: Multiple (3) sequential runs against the same ad-hoc instance
# ═══════════════════════════════════════════════════════════════════════════════

info ""
info "── Scenario C: multiple runs against one ad-hoc instance ──────────────"

C_INSTANCE="lms-regr-multi-${SUFFIX}"
RESOURCES_INSTANCE+=("$C_INSTANCE")

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ${C_INSTANCE}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: ${LMS_MODEL}
      baseURL: "http://host.docker.internal:1234/v1"
  authRefs:
    - provider: lm-studio
      secret: ${SHARED_SECRET}
EOF

C_RUNS=("${C_INSTANCE}-r1" "${C_INSTANCE}-r2" "${C_INSTANCE}-r3")
C_PROMPTS=(
  "Reply with exactly one sentence containing the phrase ALPHA_C1."
  "Reply with exactly one sentence containing the phrase BETA_C2."
  "Reply with exactly one sentence containing the phrase GAMMA_C3."
)

for i in 0 1 2; do
  RUN="${C_RUNS[$i]}"
  PROMPT="${C_PROMPTS[$i]}"
  RESOURCES_AGENTRUN+=("$RUN")
  cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${C_INSTANCE}
spec:
  instanceRef: ${C_INSTANCE}
  agentId: default
  sessionKey: "regr-C-${i}-${SUFFIX}"
  task: "${PROMPT}"
  model:
    provider: lm-studio
    model: ${LMS_MODEL}
    baseURL: "http://host.docker.internal:1234/v1"
    authSecretRef: ${SHARED_SECRET}
  timeout: "5m"
EOF
  pass "Scenario C: dispatched ${RUN}"
done

info "Scenario C: waiting for all 3 runs to complete..."
for RUN in "${C_RUNS[@]}"; do
  wait_for_run "$RUN" >/dev/null || true
  assert_nonempty_result "$RUN" "Scenario C / ${RUN##*-}"
done

# Additional correctness: each response should reference its specific marker.
for i in 0 1 2; do
  RUN="${C_RUNS[$i]}"
  case $i in
    0) MARKER="ALPHA_C1" ;;
    1) MARKER="BETA_C2" ;;
    2) MARKER="GAMMA_C3" ;;
  esac
  result="$(kubectl get agentrun "$RUN" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo '')"
  if echo "$result" | grep -q "$MARKER"; then
    pass "Scenario C / ${RUN##*-}: response contains marker ${MARKER}"
  else
    info "Scenario C / ${RUN##*-}: marker ${MARKER} not in response (model may have paraphrased; regression check already passed)"
  fi
done

# ═══════════════════════════════════════════════════════════════════════════════
# SCENARIO A: PersonaPack activation → stamped instance → AgentRun
# ═══════════════════════════════════════════════════════════════════════════════

info ""
info "── Scenario A: PersonaPack → stamped instance run ────────────────────"

A_PACK="lms-regr-pack-${SUFFIX}"
A_PERSONA="analyst"
A_INSTANCE="${A_PACK}-${A_PERSONA}"
A_RUN="${A_INSTANCE}-run-${SUFFIX}"
RESOURCES_PERSONAPACK+=("$A_PACK")
RESOURCES_INSTANCE+=("$A_INSTANCE")
RESOURCES_AGENTRUN+=("$A_RUN")

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: PersonaPack
metadata:
  name: ${A_PACK}
  namespace: ${NAMESPACE}
spec:
  enabled: true
  description: "LM Studio regression guard pack"
  category: test
  version: "0.0.1"
  baseURL: "http://host.docker.internal:1234/v1"
  authRefs:
    - provider: lm-studio
      secret: ${SHARED_SECRET}
  personas:
    - name: ${A_PERSONA}
      displayName: "Regression Analyst"
      systemPrompt: "You are a terse analyst. Respond with single sentences."
      model: ${LMS_MODEL}
EOF

pass "Scenario A: PersonaPack ${A_PACK} created (enabled=true)"
info "Scenario A: waiting for controller to stamp instance ${A_INSTANCE}..."

# Wait for the PersonaPack to stamp out the instance.
elapsed=0
while [[ "$elapsed" -lt 60 ]]; do
  if kubectl get sympoziuminstance "$A_INSTANCE" -n "$NAMESPACE" >/dev/null 2>&1; then
    pass "Scenario A: stamped instance ${A_INSTANCE} exists"
    break
  fi
  sleep 2
  elapsed=$((elapsed + 2))
done
if ! kubectl get sympoziuminstance "$A_INSTANCE" -n "$NAMESPACE" >/dev/null 2>&1; then
  fail "Scenario A: stamped instance never appeared (controller may not have reconciled the pack)"
else
  # Dispatch an AgentRun against the stamped instance.
  cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${A_RUN}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${A_INSTANCE}
spec:
  instanceRef: ${A_INSTANCE}
  agentId: default
  sessionKey: "regr-A-${SUFFIX}"
  task: "Say exactly: PERSONA_PACK_OK_A. Do not call any tools."
  model:
    provider: lm-studio
    model: ${LMS_MODEL}
    baseURL: "http://host.docker.internal:1234/v1"
    authSecretRef: ${SHARED_SECRET}
  timeout: "5m"
EOF

  pass "Scenario A: dispatched AgentRun ${A_RUN} against stamped instance"
  info "Scenario A: waiting for completion..."
  wait_for_run "$A_RUN" >/dev/null || true
  assert_nonempty_result "$A_RUN" "Scenario A"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════════════════════════════════════════

echo ""
if [[ "$EXIT_CODE" -eq 0 ]]; then
  pass "All three regression scenarios passed — LM Studio response propagation is intact"
else
  fail "One or more regression scenarios failed — see output above"
fi

# Dump a brief summary of the results for every run.
echo ""
info "Run summary:"
for RUN in "${RESOURCES_AGENTRUN[@]}"; do
  if kubectl get agentrun "$RUN" -n "$NAMESPACE" >/dev/null 2>&1; then
    p="$(kubectl get agentrun "$RUN" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo '?')"
    r="$(kubectl get agentrun "$RUN" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo '')"
    echo "  ${RUN}: phase=${p} result_len=${#r}"
  fi
done

exit "$EXIT_CODE"
