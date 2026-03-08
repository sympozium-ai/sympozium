#!/usr/bin/env bash
# Integration test: verify Sympozium works end-to-end with a local Ollama instance.
#
# What it does:
#   1. Checks that Ollama is reachable from inside the Kind cluster
#   2. Creates a dummy API key secret (Ollama does not require auth)
#   3. Creates a SympoziumInstance configured for Ollama
#   4. Creates an AgentRun with a simple arithmetic task
#   5. Waits for the AgentRun to reach Succeeded phase
#   6. Verifies .status.result is populated
#   7. Verifies .status.tokenUsage is populated
#   8. Cleans up all test resources
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - Ollama running on the host, accessible from Kind at 172.18.0.1:11434
#
# Configuration (env vars):
#   OLLAMA_BASE_URL   Base URL for Ollama from inside Kind (default: http://172.18.0.1:11434/v1)
#   OLLAMA_MODEL      Model to use (default: qwen2.5-coder:7b)
#   TEST_NAMESPACE    Kubernetes namespace (default: default)
#   TEST_TIMEOUT      Seconds to wait for completion (default: 180)
#
# Usage:
#   ./test/integration/test-ollama-local.sh
#   OLLAMA_MODEL=llama3:8b ./test/integration/test-ollama-local.sh
#   OLLAMA_BASE_URL=http://10.0.0.5:11434/v1 ./test/integration/test-ollama-local.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-ollama"
RUN_NAME="inttest-ollama-run"
SECRET_NAME="inttest-ollama-key"
OLLAMA_BASE_URL="${OLLAMA_BASE_URL:-http://172.18.0.1:11434/v1}"
MODEL="${OLLAMA_MODEL:-qwen2.5-coder:7b}"
TIMEOUT="${TEST_TIMEOUT:-180}"

# Derive the raw Ollama host URL (without /v1) for connectivity checks
OLLAMA_HOST_URL="${OLLAMA_BASE_URL%/v1}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    # Delete jobs/pods owned by the AgentRun
    kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# --- Pre-flight checks ---
info "Running integration test: Ollama local (model: ${MODEL})"

if ! command -v kubectl >/dev/null 2>&1; then
    fail "Required command not found: kubectl"
    exit 1
fi

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed. Is the cluster set up?"
    exit 1
fi

if ! kubectl get deployment sympozium-controller-manager -n sympozium-system >/dev/null 2>&1; then
    fail "Sympozium controller not running."
    exit 1
fi

# --- Step 1: Check Ollama connectivity from inside the cluster ---
info "Checking Ollama connectivity from inside Kind cluster..."
OLLAMA_CHECK=$(kubectl run inttest-ollama-check --rm -i --restart=Never \
    --image=curlimages/curl:latest \
    -- curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "${OLLAMA_HOST_URL}/api/tags" 2>/dev/null || echo "000")

if [[ "$OLLAMA_CHECK" == "200" ]]; then
    pass "Ollama is reachable from inside the cluster at ${OLLAMA_HOST_URL}"
else
    fail "Ollama is NOT reachable from inside the cluster (HTTP ${OLLAMA_CHECK})"
    echo "  Expected Ollama at: ${OLLAMA_HOST_URL}/api/tags"
    echo "  Make sure Ollama is running and listening on 0.0.0.0:11434"
    echo "  For Kind, the host is typically reachable at 172.18.0.1"
    exit 1
fi

# --- Step 2: Clean up any previous test run ---
cleanup 2>/dev/null || true
sleep 2

# --- Step 3: Create dummy secret for Ollama auth ---
info "Creating dummy API key secret: $SECRET_NAME"
kubectl create secret generic "$SECRET_NAME" \
    --from-literal=OPENAI_API_KEY="ollama-no-key-required" \
    -n "$NAMESPACE" >/dev/null 2>&1 || true

# --- Step 4: Create SympoziumInstance for Ollama ---
info "Creating SympoziumInstance: $INSTANCE_NAME"
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
      baseURL: "${OLLAMA_BASE_URL}"
  authRefs:
    - provider: ollama
      secret: ${SECRET_NAME}
EOF

# --- Step 5: Create AgentRun ---
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
  sessionKey: "inttest-ollama-$(date +%s)"
  task: "What is 2+2? Reply with just the number."
  model:
    provider: ollama
    model: ${MODEL}
    baseURL: "${OLLAMA_BASE_URL}"
    authSecretRef: ${SECRET_NAME}
  timeout: "3m"
EOF

# --- Step 6: Wait for completion ---
info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
elapsed=0
phase=""
pod=""
while [[ $elapsed -lt $TIMEOUT ]]; do
    phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    # Capture pod name as soon as it appears
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
    kubectl describe agentrun "$RUN_NAME" -n "$NAMESPACE" 2>/dev/null || true
    if [[ -n "$pod" ]]; then
        info "Pod logs:"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" --tail=30 2>/dev/null || true
    fi
    exit 1
fi

echo ""

# --- Step 7: Check phase ---
if [[ "$phase" == "Failed" ]]; then
    fail "AgentRun phase: Failed"
    kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status}' | python3 -m json.tool 2>/dev/null || true
    exit 1
fi

pass "AgentRun phase: Succeeded"

# --- Step 8: Verify .status.result is populated ---
failures=0

result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")
if [[ -n "$result" ]]; then
    pass "status.result is populated"
    info "Result: $(echo "$result" | head -c 200)"
else
    fail "status.result is empty"
    failures=$((failures + 1))
fi

# --- Step 9: Verify .status.tokenUsage is populated ---
input_tokens=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.tokenUsage.inputTokens}' 2>/dev/null || echo "")
output_tokens=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.tokenUsage.outputTokens}' 2>/dev/null || echo "")
total_tokens=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.tokenUsage.totalTokens}' 2>/dev/null || echo "")
duration_ms=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.tokenUsage.durationMs}' 2>/dev/null || echo "")

if [[ -n "$input_tokens" && "$input_tokens" -gt 0 ]]; then
    pass "tokenUsage.inputTokens is populated: ${input_tokens}"
else
    fail "tokenUsage.inputTokens is missing or zero"
    failures=$((failures + 1))
fi

if [[ -n "$output_tokens" && "$output_tokens" -gt 0 ]]; then
    pass "tokenUsage.outputTokens is populated: ${output_tokens}"
else
    fail "tokenUsage.outputTokens is missing or zero"
    failures=$((failures + 1))
fi

if [[ -n "$total_tokens" && "$total_tokens" -gt 0 ]]; then
    pass "tokenUsage.totalTokens is populated: ${total_tokens}"
else
    fail "tokenUsage.totalTokens is missing or zero"
    failures=$((failures + 1))
fi

if [[ -n "$duration_ms" && "$duration_ms" -gt 0 ]]; then
    pass "tokenUsage.durationMs is populated: ${duration_ms}ms"
else
    fail "tokenUsage.durationMs is missing or zero"
    failures=$((failures + 1))
fi

# --- Summary ---
echo ""
echo "=============================="
echo " Integration Test Summary"
echo "=============================="
echo " AgentRun:  $RUN_NAME"
echo " Phase:     $phase"
echo " Provider:  ollama"
echo " Model:     $MODEL"
echo " Base URL:  $OLLAMA_BASE_URL"
if [[ -n "$pod" ]]; then
    echo " Pod:       $pod"
fi
echo " Failures:  $failures"
echo "=============================="
echo ""

if [[ $failures -gt 0 ]]; then
    fail "Integration test finished with $failures failure(s)"
    exit 1
fi

pass "Integration test complete"
