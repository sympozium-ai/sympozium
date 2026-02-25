#!/usr/bin/env bash
# Integration test: verify the write_file tool works end-to-end.
#
# What it does:
#   1. Creates a test ClawInstance + AgentRun in the cluster
#   2. The AgentRun task tells the LLM to write a specific file using write_file
#   3. Waits for the AgentRun to complete (Succeeded or Failed)
#   4. Checks pod logs for evidence the tool was invoked
#   5. Checks the AgentRun status.result for the expected content
#   6. Cleans up test resources
#
# Prerequisites:
#   - Kind cluster running with KubeClaw installed
#   - An OpenAI API key in the environment (OPENAI_API_KEY) or an existing
#     secret named "inttest-openai-key" in the default namespace
#
# Usage:
#   ./test/integration/test-write-file.sh
#   OPENAI_API_KEY=sk-... ./test/integration/test-write-file.sh
#   TEST_TIMEOUT=180 ./test/integration/test-write-file.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-write-file"
RUN_NAME="inttest-write-file-run"
SECRET_NAME="inttest-openai-key"
MODEL="${TEST_MODEL:-gpt-4o-mini}"
TIMEOUT="${TEST_TIMEOUT:-120}"             # seconds to wait for completion
MARKER_TEXT="kubeclaw-integration-ok"      # text the agent must write
TARGET_FILE="/workspace/test-output.txt"

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
    kubectl delete clawinstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    # Delete jobs/pods owned by the AgentRun
    kubectl delete jobs -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    # Don't delete the secret — the user may have created it manually
}

# --- Pre-flight checks ---
info "Running integration test: write_file tool"

if ! kubectl get crd agentruns.kubeclaw.io >/dev/null 2>&1; then
    fail "KubeClaw CRDs not installed. Is the cluster set up?"
    exit 1
fi

if ! kubectl get deployment kubeclaw-controller-manager -n kubeclaw-system >/dev/null 2>&1; then
    fail "KubeClaw controller not running."
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

# --- Clean up any previous test run ---
cleanup 2>/dev/null || true
sleep 2

# --- Create test ClawInstance ---
info "Creating ClawInstance: $INSTANCE_NAME"
cat <<EOF | kubectl apply -f -
apiVersion: kubeclaw.io/v1alpha1
kind: ClawInstance
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
info "Creating AgentRun: $RUN_NAME"
cat <<EOF | kubectl apply -f -
apiVersion: kubeclaw.io/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
  namespace: ${NAMESPACE}
  labels:
    kubeclaw.io/instance: ${INSTANCE_NAME}
spec:
  instanceRef: ${INSTANCE_NAME}
  agentId: default
  sessionKey: "inttest-$(date +%s)"
  task: |
    Use the write_file tool to write the exact text "${MARKER_TEXT}" to the file ${TARGET_FILE}.
    Do not add any extra content, newlines, or formatting — just that exact string.
    After writing, confirm you wrote the file.
  model:
    provider: openai
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
    # Capture pod name as soon as it exists (before it gets cleaned up)
    if [[ -z "$pod" ]]; then
        pod=$(kubectl get pods -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "$pod" ]]; then
            info "Pod found: $pod"
        fi
    fi
    if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
        break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
    # Show progress every 15s
    if (( elapsed % 15 == 0 )); then
        info "  ...${elapsed}s elapsed (phase: ${phase:-Pending})"
    fi
done

if [[ "$phase" != "Succeeded" && "$phase" != "Failed" ]]; then
    fail "AgentRun did not complete within ${TIMEOUT}s (last phase: ${phase:-unknown})"
    info "Debug: kubectl describe agentrun $RUN_NAME -n $NAMESPACE"
    cleanup
    exit 1
fi

# --- Check result ---
echo ""
if [[ "$phase" == "Failed" ]]; then
    fail "AgentRun phase: Failed"
    kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status}' | python3 -m json.tool 2>/dev/null || true
    cleanup
    exit 1
fi

pass "AgentRun phase: Succeeded"

# --- Verify the tool was used ---
# Check the result field for write_file evidence
result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")

# Check pod logs for the tool execution log line (pod name captured during polling)
failures=0
logs=""
if [[ -n "$pod" ]]; then
    logs=$(kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null || echo "")
fi

# Validation 1: Check logs for "Wrote file" evidence
if echo "$logs" | grep -qi "Wrote file.*test-output"; then
    pass "Pod logs confirm write_file tool was called"
else
    if [[ -z "$pod" ]]; then
        info "Pod not found — cannot check logs (job cleaned up too fast)"
    else
        fail "Pod logs do not show write_file tool invocation"
        failures=$((failures + 1))
        if [[ -n "$logs" ]]; then
            info "Last 20 log lines:"
            echo "$logs" | tail -20
        fi
    fi
fi

# Validation 2: Check the result text mentions the file or the marker
if echo "$result" | grep -qi "$MARKER_TEXT\|test-output\|write_file\|wrote"; then
    pass "AgentRun result references the file write"
else
    fail "AgentRun result does not reference the file write"
    failures=$((failures + 1))
    info "Result (first 500 chars):"
    echo "$result" | head -c 500
    echo ""
fi

# Validation 3: Try to read the file from the pod (if still running)
if [[ -n "$pod" ]]; then
    pod_phase=$(kubectl get pod "$pod" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ "$pod_phase" == "Running" || "$pod_phase" == "Succeeded" ]]; then
        # Try to cat the file from any container that has /workspace
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
        info "Pod phase is '$pod_phase' — cannot exec to verify file content, relying on log evidence"
    fi
else
    info "Pod not found — cannot verify file content on disk"
fi

echo ""
# --- Summary ---
echo "=============================="
echo " Integration Test Summary"
echo "=============================="
echo " AgentRun:  $RUN_NAME"
echo " Phase:     $phase"
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

pass "Integration test complete"
