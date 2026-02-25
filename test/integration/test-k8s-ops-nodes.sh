#!/usr/bin/env bash
# Integration test: verify the k8s-ops skill works end-to-end.
#
# What it does:
#   1. Creates a test SympoziumInstance + AgentRun with the k8s-ops skill
#   2. The AgentRun task tells the LLM to run "kubectl get nodes"
#   3. Waits for the AgentRun to complete
#   4. Checks the result contains node information (e.g. "Ready", "kind-control-plane")
#   5. Cleans up test resources
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - k8s-ops SkillPack CRD applied (kubectl get skillpack k8s-ops -n sympozium-system)
#   - An OpenAI API key in the environment (OPENAI_API_KEY) or an existing
#     secret named "inttest-openai-key" in the default namespace
#
# Usage:
#   ./test/integration/test-k8s-ops-nodes.sh
#   TEST_MODEL=gpt-5.2 ./test/integration/test-k8s-ops-nodes.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-k8s-ops"
RUN_NAME="inttest-k8s-ops-nodes"
SECRET_NAME="inttest-openai-key"
MODEL="${TEST_MODEL:-gpt-4o-mini}"
TIMEOUT="${TEST_TIMEOUT:-180}"  # longer timeout — skill sidecar image pull

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }
failures=0

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
}

# --- Pre-flight checks ---
info "Running integration test: k8s-ops skill (get nodes)"

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed. Is the cluster set up?"
    exit 1
fi

if ! kubectl get deployment sympozium-controller-manager -n sympozium-system >/dev/null 2>&1; then
    fail "Sympozium controller not running."
    exit 1
fi

if ! kubectl get skillpack k8s-ops -n sympozium-system >/dev/null 2>&1; then
    fail "k8s-ops SkillPack not found in sympozium-system."
    echo "  Apply it: kubectl apply -f config/skills/k8s-ops.yaml"
    exit 1
fi

# --- Ensure OpenAI secret exists ---
if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    if [[ -z "${OPENAI_API_KEY:-}" ]]; then
        fail "No OPENAI_API_KEY set and secret '$SECRET_NAME' not found."
        echo "  Either: export OPENAI_API_KEY=sk-..."
        echo "  Or:     kubectl create secret generic $SECRET_NAME --from-literal=OPENAI_API_KEY=sk-..."
        exit 1
    fi
    info "Creating secret $SECRET_NAME from OPENAI_API_KEY env var"
    kubectl create secret generic "$SECRET_NAME" \
        --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
        -n "$NAMESPACE"
fi

# Get the actual node name for validation
EXPECTED_NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -z "$EXPECTED_NODE" ]]; then
    fail "Could not determine cluster node name"
    exit 1
fi
info "Cluster node: $EXPECTED_NODE"

# --- Clean up any previous test run ---
cleanup 2>/dev/null || true
sleep 2

# --- Create test SympoziumInstance with k8s-ops skill ---
info "Creating SympoziumInstance: $INSTANCE_NAME (with k8s-ops skill)"
cat <<EOF | kubectl apply -f -
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ${INSTANCE_NAME}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: ${MODEL}
  authRefs:
    - secret: ${SECRET_NAME}
  skills:
    - skillPackRef: k8s-ops
EOF

# --- Create test AgentRun ---
info "Creating AgentRun: $RUN_NAME"
cat <<EOF | kubectl apply -f -
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${INSTANCE_NAME}
spec:
  instanceRef: ${INSTANCE_NAME}
  agentId: default
  sessionKey: "inttest-$(date +%s)"
  task: |
    Run the command "kubectl get nodes -o wide" using the execute_command tool.
    Report the full output exactly as returned by kubectl.
    Do not run any other commands.
  model:
    provider: openai
    model: ${MODEL}
    authSecretRef: ${SECRET_NAME}
  skills:
    - skillPackRef: k8s-ops
  timeout: "3m"
EOF

# --- Wait for completion ---
info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
elapsed=0
phase=""
pod=""
while [[ $elapsed -lt $TIMEOUT ]]; do
    phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    # Capture pod name early
    if [[ -z "$pod" ]]; then
        pod=$(kubectl get pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "$pod" ]]; then
            info "Pod found: $pod"
        fi
    fi
    if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
        break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
    if (( elapsed % 15 == 0 )); then
        info "  ...${elapsed}s elapsed (phase: ${phase:-Pending})"
    fi
done

if [[ "$phase" != "Succeeded" && "$phase" != "Failed" ]]; then
    fail "AgentRun did not complete within ${TIMEOUT}s (last phase: ${phase:-unknown})"
    info "Debug: kubectl describe agentrun $RUN_NAME -n $NAMESPACE"
    if [[ -n "$pod" ]]; then
        info "Pod events:"
        kubectl describe pod "$pod" -n "$NAMESPACE" 2>/dev/null | tail -20
    fi
    cleanup
    exit 1
fi

# --- Check result ---
echo ""
if [[ "$phase" == "Failed" ]]; then
    fail "AgentRun phase: Failed"
    result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")
    info "Result: $result"
    if [[ -n "$pod" ]]; then
        info "Agent logs:"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null | tail -30 || true
    fi
    cleanup
    exit 1
fi

pass "AgentRun phase: Succeeded"

# --- Validate the result ---
result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")

# Get pod logs if available
logs=""
if [[ -n "$pod" ]]; then
    logs=$(kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null || echo "")
fi

# Validation 1: Result contains the node name
if echo "$result" | grep -qi "$EXPECTED_NODE"; then
    pass "Result contains node name: $EXPECTED_NODE"
else
    fail "Result does not contain node name '$EXPECTED_NODE'"
    failures=$((failures + 1))
    info "Result (first 500 chars):"
    echo "$result" | head -c 500
    echo ""
fi

# Validation 2: Result contains "Ready" status (node is healthy)
if echo "$result" | grep -qi "Ready"; then
    pass "Result contains node Ready status"
else
    fail "Result does not mention Ready status"
    failures=$((failures + 1))
fi

# Validation 3: Evidence that execute_command was used (from logs or result)
# This is informational — if validations 1 and 2 passed, the tool clearly worked.
tool_evidence=false
if [[ -n "$logs" ]] && echo "$logs" | grep -qi "exec.*request\|execute_command\|kubectl get nodes"; then
    tool_evidence=true
fi
if echo "$result" | grep -qi "kubectl\|execute_command\|get nodes\|INTERNAL-IP\|EXTERNAL-IP\|OS-IMAGE"; then
    tool_evidence=true
fi
if $tool_evidence; then
    pass "Evidence of execute_command tool usage found"
else
    info "No direct tool invocation evidence in result/logs (node data validates it worked)"
fi

echo ""
# --- Summary ---
echo "=============================="
echo " Integration Test Summary"
echo "=============================="
echo " AgentRun:  $RUN_NAME"
echo " Phase:     $phase"
echo " Model:     $MODEL"
echo " Skill:     k8s-ops"
echo " Node:      $EXPECTED_NODE"
if [[ -n "$pod" ]]; then
    echo " Pod:       $pod"
fi
echo " Failures:  $failures"
echo "=============================="
echo ""

# --- Cleanup ---
cleanup

if [[ $failures -gt 0 ]]; then
    fail "Integration test finished with $failures failure(s)"
    exit 1
fi

pass "Integration test complete"
