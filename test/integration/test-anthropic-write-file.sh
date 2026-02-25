#!/usr/bin/env bash
# Integration test: verify the Anthropic provider + write_file tool works end-to-end.
#
# What it does:
#   1. Creates a test SympoziumInstance + AgentRun using provider=anthropic
#   2. The AgentRun task tells Claude to write a specific file using write_file
#   3. Waits for the AgentRun to complete (Succeeded or Failed)
#   4. Checks pod logs for evidence the tool was invoked
#   5. Checks the AgentRun status.result for the expected content
#   6. Cleans up test resources
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - An Anthropic API key in the environment (ANTHROPIC_API_KEY) or an existing
#     secret named "inttest-anthropic-key" in the default namespace
#
# Usage:
#   ANTHROPIC_API_KEY=sk-ant-... ./test/integration/test-anthropic-write-file.sh
#   TEST_MODEL=claude-sonnet-4-20250514 ./test/integration/test-anthropic-write-file.sh
#   TEST_TIMEOUT=180 ./test/integration/test-anthropic-write-file.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-anthropic-write"
RUN_NAME="inttest-anthropic-write-run"
SECRET_NAME="inttest-anthropic-key"
MODEL="${TEST_MODEL:-claude-sonnet-4-20250514}"
TIMEOUT="${TEST_TIMEOUT:-120}"             # seconds to wait for completion
MARKER_TEXT="sympozium-anthropic-ok"        # text the agent must write
TARGET_FILE="/workspace/anthropic-test.txt"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
}

# --- Pre-flight checks ---
info "Running integration test: Anthropic provider + write_file tool"

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed. Is the cluster set up?"
    exit 1
fi

if ! kubectl get deployment sympozium-controller-manager -n sympozium-system >/dev/null 2>&1; then
    fail "Sympozium controller not running."
    exit 1
fi

# --- Ensure Anthropic secret exists ---
if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
        fail "No ANTHROPIC_API_KEY set and secret '$SECRET_NAME' not found."
        echo "  Either: export ANTHROPIC_API_KEY=sk-ant-..."
        echo "  Or:     kubectl create secret generic $SECRET_NAME --from-literal=ANTHROPIC_API_KEY=sk-ant-..."
        exit 1
    fi
    info "Creating secret $SECRET_NAME from ANTHROPIC_API_KEY env var"
    kubectl create secret generic "$SECRET_NAME" \
        --from-literal=ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
        -n "$NAMESPACE"
fi

# --- Clean up any previous test run ---
cleanup 2>/dev/null || true
sleep 2

# --- Create test SympoziumInstance ---
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
  authRefs:
    - secret: ${SECRET_NAME}
EOF

# --- Create test AgentRun ---
info "Creating AgentRun: $RUN_NAME (provider: anthropic, model: $MODEL)"
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
  sessionKey: "inttest-anthropic-$(date +%s)"
  task: |
    Use the write_file tool to write the exact text "${MARKER_TEXT}" to the file ${TARGET_FILE}.
    Do not add any extra content, newlines, or formatting — just that exact string.
    After writing, confirm you wrote the file.
  model:
    provider: anthropic
    model: ${MODEL}
    authSecretRef: ${SECRET_NAME}
  timeout: "3m"
EOF

# --- Wait for completion ---
info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
elapsed=0
phase=""
pod=""
while [[ $elapsed -lt $TIMEOUT ]]; do
    phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    # Capture pod name as soon as it exists
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
        info "Debug: kubectl logs $pod -c agent -n $NAMESPACE"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null | tail -30 || true
    fi
    cleanup
    exit 1
fi

# --- Check result ---
echo ""
if [[ "$phase" == "Failed" ]]; then
    fail "AgentRun phase: Failed"
    kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status}' | python3 -m json.tool 2>/dev/null || true
    if [[ -n "$pod" ]]; then
        info "Agent logs:"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null | tail -30 || true
    fi
    cleanup
    exit 1
fi

pass "AgentRun phase: Succeeded"

# --- Verify the tool was used ---
result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")

failures=0
logs=""
if [[ -n "$pod" ]]; then
    logs=$(kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null || echo "")
fi

# Validation 1: Check logs confirm provider=anthropic was used
if echo "$logs" | grep -q "provider=anthropic"; then
    pass "Pod logs confirm provider=anthropic"
else
    if [[ -z "$pod" ]]; then
        info "Pod not found — cannot check logs (job cleaned up too fast)"
    else
        fail "Pod logs do not confirm provider=anthropic"
        failures=$((failures + 1))
    fi
fi

# Validation 2: Check logs for tool_use evidence (Anthropic format)
if echo "$logs" | grep -qi "tool_use\|tool call.*read_file\|tool call.*write_file\|Wrote file"; then
    pass "Pod logs confirm tool execution"
else
    if [[ -z "$pod" ]]; then
        info "Pod not found — cannot check tool logs"
    else
        fail "Pod logs do not show tool invocation"
        failures=$((failures + 1))
        if [[ -n "$logs" ]]; then
            info "Last 20 log lines:"
            echo "$logs" | tail -20
        fi
    fi
fi

# Validation 3: Check the result text mentions the file or the marker
if echo "$result" | grep -qi "$MARKER_TEXT\|anthropic-test\|write_file\|wrote"; then
    pass "AgentRun result references the file write"
else
    fail "AgentRun result does not reference the file write"
    failures=$((failures + 1))
    info "Result (first 500 chars):"
    echo "$result" | head -c 500
    echo ""
fi

# Validation 4: Try to read the file from the pod
if [[ -n "$pod" ]]; then
    pod_phase=$(kubectl get pod "$pod" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ "$pod_phase" == "Running" || "$pod_phase" == "Succeeded" ]]; then
        file_content=$(kubectl exec "$pod" -c agent -n "$NAMESPACE" -- cat "$TARGET_FILE" 2>/dev/null || \
                       kubectl exec "$pod" -c sandbox -n "$NAMESPACE" -- cat "$TARGET_FILE" 2>/dev/null || echo "")
        if [[ -n "$file_content" ]]; then
            if echo "$file_content" | grep -q "$MARKER_TEXT"; then
                pass "File content verified: contains '$MARKER_TEXT'"
            else
                fail "File exists but content doesn't match"
                failures=$((failures + 1))
                info "Got: $file_content"
            fi
        else
            info "Could not read file from pod (containers exited) — relying on log evidence"
        fi
    else
        info "Pod phase is '$pod_phase' — cannot exec to verify file content"
    fi
else
    info "Pod not found — cannot verify file content on disk"
fi

echo ""
# --- Summary ---
echo "=============================="
echo " Anthropic Integration Test"
echo "=============================="
echo " AgentRun:  $RUN_NAME"
echo " Phase:     $phase"
echo " Provider:  anthropic"
echo " Model:     $MODEL"
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

pass "Anthropic integration test complete"
