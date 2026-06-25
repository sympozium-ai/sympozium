#!/usr/bin/env bash
# Integration test: Lifecycle hooks (preRun + postRun + RBAC + phase transitions).
#
# Proves:
#   1. PreRun init container executes before the agent (writes a file the agent reads)
#   2. PostRun Job is created and executes after agent completion
#   3. Workspace is shared between preRun -> agent -> postRun via PVC
#   4. RBAC rules are created and functional (postRun creates a ConfigMap via kubectl)
#   5. AGENT_EXIT_CODE and AGENT_RESULT env vars are populated in postRun
#   6. Phase transitions: Pending -> Running -> PostRunning -> Succeeded
#
# Requires: Kind cluster with Sympozium deployed, LM Studio accessible on node.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
TIMEOUT="${TEST_TIMEOUT:-240}"
INSTANCE_NAME="inttest-lifecycle-$(date +%s)"
RUN_NAME=""
PROOF_CM="lifecycle-proof-${INSTANCE_NAME}"

# LM Studio via node-probe proxy — no API key needed.
LM_STUDIO_BASE_URL="${LM_STUDIO_BASE_URL:-http://172.18.0.2:9473/proxy/lm-studio/v1}"
LM_STUDIO_MODEL="${LM_STUDIO_MODEL:-qwen/qwen3.5-9b}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS $*${NC}"; }
fail() { echo -e "${RED}FAIL $*${NC}"; FAILED=1; }
info() { echo -e "${YELLOW}---- $*${NC}"; }

FAILED=0
PHASES_SEEN=()

cleanup() {
  info "Cleaning up..."
  [[ -n "$RUN_NAME" ]] && kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap "$PROOF_CM" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  # Clean up Jobs and PVCs created by the controller.
  [[ -n "$RUN_NAME" ]] && kubectl delete job -n "$NAMESPACE" -l "sympozium.ai/agent-run=${RUN_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  [[ -n "$RUN_NAME" ]] && kubectl delete pvc "${RUN_NAME}-workspace" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  # Clean up lifecycle RBAC.
  [[ -n "$RUN_NAME" ]] && kubectl delete role "sympozium-lifecycle-${RUN_NAME}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  [[ -n "$RUN_NAME" ]] && kubectl delete rolebinding "sympozium-lifecycle-${RUN_NAME}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Preflight ────────────────────────────────────────────────────────────────

info "Creating test resources in namespace '$NAMESPACE'"
info "Using LM Studio model '${LM_STUDIO_MODEL}' at ${LM_STUDIO_BASE_URL}"

# ── Create Agent (LM Studio — no API key needed) ─────────────────

cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${INSTANCE_NAME}
spec:
  agents:
    default:
      model: ${LM_STUDIO_MODEL}
      baseURL: ${LM_STUDIO_BASE_URL}
EOF

# ── Create AgentRun with lifecycle hooks + RBAC ──────────────────────────────
#
# PreRun:  Writes a marker file to /workspace so we can prove it ran first.
# PostRun: Creates a ConfigMap using kubectl (needs RBAC), embedding the
#          AGENT_EXIT_CODE and AGENT_RESULT env vars as proof they were injected.

RUN_NAME="${INSTANCE_NAME}-run"

cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: primary
  sessionKey: lifecycle-test
  task: "Respond with exactly: hello lifecycle"
  model:
    provider: openai-compatible
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: ""
  timeout: "3m"
  lifecycle:
    rbac:
      - apiGroups: [""]
        resources: ["configmaps"]
        verbs: ["create", "get"]
    preRun:
      - name: write-marker
        image: busybox:1.36
        command: ["sh", "-c", "echo 'LIFECYCLE_PROOF_VALUE_42' > /workspace/pre-hook-marker.txt && echo 'preRun: wrote marker file'"]
    postRun:
      - name: create-proof-cm
        image: soldevelo/kubectl:1.36
        command:
          - sh
          - -c
          - |
            echo "postRun: AGENT_EXIT_CODE=\${AGENT_EXIT_CODE}"
            echo "postRun: AGENT_RESULT length=\${#AGENT_RESULT}"
            kubectl create configmap ${PROOF_CM} \
              --from-literal=exit-code="\${AGENT_EXIT_CODE}" \
              --from-literal=agent-result="\${AGENT_RESULT}" \
              --from-literal=marker="\$(cat /workspace/pre-hook-marker.txt 2>/dev/null || echo MISSING)" \
              -n ${NAMESPACE}
            echo "postRun: ConfigMap created successfully"
EOF

info "AgentRun '${RUN_NAME}' created — polling for phase transitions..."

# ── Poll for phase transitions ───────────────────────────────────────────────

elapsed=0
last_phase=""

while [[ $elapsed -lt $TIMEOUT ]]; do
  phase="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"

  # Record phase transitions.
  if [[ -n "$phase" && "$phase" != "$last_phase" ]]; then
    PHASES_SEEN+=("$phase")
    info "Phase: $phase (${elapsed}s)"
    last_phase="$phase"
  fi

  # Terminal states.
  if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
    break
  fi

  sleep 3
  elapsed=$((elapsed + 3))
done

if [[ $elapsed -ge $TIMEOUT ]]; then
  fail "Timed out after ${TIMEOUT}s (last phase: ${last_phase})"
  kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o yaml 2>/dev/null || true
  exit 1
fi

# ── Assert final phase ───────────────────────────────────────────────────────

final_phase="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
if [[ "$final_phase" == "Succeeded" ]]; then
  pass "AgentRun completed with phase: Succeeded"
else
  fail "AgentRun ended with phase: $final_phase (expected Succeeded)"
  kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.error}' 2>/dev/null && echo
  # Dump pod logs for debugging.
  pod_name="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.podName}' 2>/dev/null || echo "")"
  if [[ -n "$pod_name" ]]; then
    echo "  Agent container logs:"
    kubectl logs "$pod_name" -n "$NAMESPACE" -c agent --tail=30 2>/dev/null || true
  fi
fi

# ── Assert phase transitions included PostRunning ────────────────────────────

phases_str="${PHASES_SEEN[*]}"
if [[ "$phases_str" == *"PostRunning"* ]]; then
  pass "PostRunning phase was observed during execution"
else
  fail "PostRunning phase was NOT observed (saw: ${phases_str})"
fi

if [[ "$phases_str" == *"Running"* ]]; then
  pass "Running phase was observed"
else
  fail "Running phase was NOT observed (saw: ${phases_str})"
fi

if [[ "$phases_str" == *"Succeeded"* ]] || [[ "$phases_str" == *"Failed"* ]]; then
  pass "Terminal phase reached"
else
  fail "No terminal phase observed (saw: ${phases_str})"
fi

# ── Assert preRun executed (checked via postRun reading the marker later) ────
# The definitive proof that preRun wrote to workspace is in the postRun ConfigMap
# (see "workspace shared" assertion below). This section is a bonus check.

agent_result="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")"
if [[ -n "$agent_result" ]]; then
  pass "Agent produced a result (${#agent_result} bytes)"
else
  fail "Agent produced no result"
fi

# ── Assert postRun created the proof ConfigMap ───────────────────────────────

info "Checking for proof ConfigMap '${PROOF_CM}'..."

# Give the postRun Job a moment to finish if it hasn't already.
cm_elapsed=0
cm_found=false
while [[ $cm_elapsed -lt 30 ]]; do
  if kubectl get configmap "$PROOF_CM" -n "$NAMESPACE" >/dev/null 2>&1; then
    cm_found=true
    break
  fi
  sleep 2
  cm_elapsed=$((cm_elapsed + 2))
done

if $cm_found; then
  pass "PostRun hook created proof ConfigMap '${PROOF_CM}'"
else
  fail "Proof ConfigMap '${PROOF_CM}' was NOT created — postRun hook failed or RBAC blocked it"
  # Debug: check postRun Job status.
  postrun_job="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.postRunJobName}' 2>/dev/null || echo "")"
  if [[ -n "$postrun_job" ]]; then
    echo "  PostRun Job: $postrun_job"
    kubectl get job "$postrun_job" -n "$NAMESPACE" -o wide 2>/dev/null || true
    kubectl logs "job/${postrun_job}" -n "$NAMESPACE" --all-containers 2>/dev/null | tail -20 || true
  fi
fi

# ── Assert AGENT_EXIT_CODE was injected ──────────────────────────────────────

if $cm_found; then
  exit_code="$(kubectl get configmap "$PROOF_CM" -n "$NAMESPACE" -o jsonpath='{.data.exit-code}' 2>/dev/null || echo "")"
  if [[ "$exit_code" == "0" ]]; then
    pass "AGENT_EXIT_CODE=0 was injected into postRun container"
  else
    fail "AGENT_EXIT_CODE='${exit_code}' (expected '0')"
  fi
fi

# ── Assert AGENT_RESULT was injected ─────────────────────────────────────────

if $cm_found; then
  cm_result="$(kubectl get configmap "$PROOF_CM" -n "$NAMESPACE" -o jsonpath='{.data.agent-result}' 2>/dev/null || echo "")"
  if [[ -n "$cm_result" ]]; then
    pass "AGENT_RESULT was injected into postRun container (${#cm_result} bytes)"
  else
    fail "AGENT_RESULT was empty in postRun container"
  fi
fi

# ── Assert workspace was shared (marker file readable in postRun) ────────────

if $cm_found; then
  marker="$(kubectl get configmap "$PROOF_CM" -n "$NAMESPACE" -o jsonpath='{.data.marker}' 2>/dev/null || echo "")"
  if [[ "$marker" == *"LIFECYCLE_PROOF_VALUE_42"* ]]; then
    pass "Workspace PVC shared: postRun read marker written by preRun"
  else
    fail "Workspace NOT shared: postRun got marker='${marker}' (expected LIFECYCLE_PROOF_VALUE_42)"
  fi
fi

# ── Assert RBAC was created ──────────────────────────────────────────────────

role_name="sympozium-lifecycle-${RUN_NAME}"
if kubectl get role "$role_name" -n "$NAMESPACE" >/dev/null 2>&1; then
  pass "Lifecycle RBAC Role '${role_name}' was created"
  # Verify the rules include configmaps.
  verbs="$(kubectl get role "$role_name" -n "$NAMESPACE" -o jsonpath='{.rules[0].verbs}' 2>/dev/null || echo "")"
  if [[ "$verbs" == *"create"* ]]; then
    pass "RBAC Role includes 'create' verb for configmaps"
  else
    fail "RBAC Role verbs unexpected: ${verbs}"
  fi
else
  # Role may have been garbage-collected already if the AgentRun is terminal.
  info "Lifecycle RBAC Role not found (may have been GC'd) — postRun ConfigMap creation proves it worked"
fi

# ── Assert postRunJobName was set in status ──────────────────────────────────

postrun_job="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.postRunJobName}' 2>/dev/null || echo "")"
if [[ -n "$postrun_job" ]]; then
  pass "status.postRunJobName was set: ${postrun_job}"
else
  fail "status.postRunJobName was NOT set"
fi

# ── Assert preRun init container was present in pod ──────────────────────────

pod_name="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.podName}' 2>/dev/null || echo "")"
if [[ -n "$pod_name" ]]; then
  init_names="$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{range .spec.initContainers[*]}{.name}{"\n"}{end}' 2>/dev/null || echo "")"
  if [[ "$init_names" == *"pre-write-marker"* ]]; then
    pass "PreRun init container 'pre-write-marker' found in pod spec"
  else
    fail "PreRun init container 'pre-write-marker' NOT found in pod (got: ${init_names})"
  fi
else
  info "Pod already cleaned up — cannot verify init containers"
fi

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "Phase transitions observed: ${PHASES_SEEN[*]}"
echo ""

if [[ $FAILED -eq 0 ]]; then
  pass "All lifecycle hook assertions passed"
  exit 0
else
  fail "Some assertions failed"
  exit 1
fi
