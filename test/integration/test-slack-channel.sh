#!/usr/bin/env bash
# Integration test: verify the Slack channel pipeline works.
#
# This test has two modes:
#
# Mode 1 (no SLACK_BOT_TOKEN): Deployment pipeline only
#   - Creates a SympoziumInstance with a slack channel
#   - Verifies the controller creates a channel-slack Deployment
#   - Checks the Deployment has the correct image, labels, and config
#   - Verifies the pod is created (may restart without real tokens)
#
# Mode 2 (with SLACK_BOT_TOKEN + SLACK_APP_TOKEN + SLACK_CHANNEL_ID): Full E2E
#   - Everything in Mode 1, plus:
#   - Creates an AgentRun that uses send_channel_message to send a message
#   - Verifies the message appears in the agent result
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - channel-slack image available in the cluster
#   - (Mode 2) SLACK_BOT_TOKEN, SLACK_APP_TOKEN, and SLACK_CHANNEL_ID set
#
# Usage:
#   # Mode 1: deployment pipeline only
#   ./test/integration/test-slack-channel.sh
#
#   # Mode 2: full end-to-end with real Slack bot
#   SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-... SLACK_CHANNEL_ID=C0123456 \
#     ./test/integration/test-slack-channel.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-slack"
RUN_NAME="inttest-slack-msg"
SECRET_NAME="inttest-openai-key"
SLACK_SECRET_NAME="inttest-slack-secret"
MODEL="${TEST_MODEL:-gpt-4o-mini}"
TIMEOUT="${TEST_TIMEOUT:-120}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }
failures=0

FULL_MODE=false
if [[ -n "${SLACK_BOT_TOKEN:-}" && -n "${SLACK_APP_TOKEN:-}" && -n "${SLACK_CHANNEL_ID:-}" ]]; then
    FULL_MODE=true
fi

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    # Wait for the controller to clean up channel deployments (owned by SympoziumInstance)
    sleep 3
    kubectl delete deployment "$INSTANCE_NAME-channel-slack" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete secret "$SLACK_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}

# --- Pre-flight checks ---
if $FULL_MODE; then
    info "Running integration test: Slack channel (FULL — real bot)"
else
    info "Running integration test: Slack channel (deployment pipeline only)"
    info "Set SLACK_BOT_TOKEN + SLACK_APP_TOKEN + SLACK_CHANNEL_ID for full E2E test"
fi

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed."
    exit 1
fi

if ! kubectl get deployment sympozium-controller-manager -n sympozium-system >/dev/null 2>&1; then
    fail "Sympozium controller not running."
    exit 1
fi

# --- Ensure secrets exist ---
if $FULL_MODE; then
    # Slack secret with bot token and app token for Socket Mode
    kubectl delete secret "$SLACK_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    kubectl create secret generic "$SLACK_SECRET_NAME" \
        --from-literal=SLACK_BOT_TOKEN="$SLACK_BOT_TOKEN" \
        --from-literal=SLACK_APP_TOKEN="$SLACK_APP_TOKEN" \
        -n "$NAMESPACE" >/dev/null 2>&1
    info "Created Slack secret (bot token + app token for Socket Mode)"

    # OpenAI secret (for AgentRun)
    if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
        if [[ -z "${OPENAI_API_KEY:-}" ]]; then
            fail "No OPENAI_API_KEY set and secret '$SECRET_NAME' not found."
            exit 1
        fi
        kubectl create secret generic "$SECRET_NAME" \
            --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
            -n "$NAMESPACE"
    fi
fi

# --- Clean up previous runs ---
cleanup 2>/dev/null || true
sleep 2

# ============================================================
# Part 1: Channel Deployment Pipeline
# ============================================================
info "Creating SympoziumInstance with slack channel: $INSTANCE_NAME"

# Build the channel config — use real or dummy tokens
if $FULL_MODE; then
    CHANNEL_SECRET_REF="$SLACK_SECRET_NAME"
else
    # Create a dummy secret so the controller doesn't error
    kubectl create secret generic "$SLACK_SECRET_NAME" \
        --from-literal=SLACK_BOT_TOKEN="xoxb-dummy-token-for-pipeline-test" \
        -n "$NAMESPACE" >/dev/null 2>&1 || true
    CHANNEL_SECRET_REF="$SLACK_SECRET_NAME"
fi

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
  channels:
    - type: slack
      configRef:
        secret: ${CHANNEL_SECRET_REF}
EOF

# Wait for the channel Deployment to appear
info "Waiting for channel Deployment to be created..."
deploy_name="${INSTANCE_NAME}-channel-slack"
elapsed=0
deploy_found=false
while [[ $elapsed -lt 30 ]]; do
    if kubectl get deployment "$deploy_name" -n "$NAMESPACE" >/dev/null 2>&1; then
        deploy_found=true
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done

if $deploy_found; then
    pass "Channel Deployment created: $deploy_name"
else
    fail "Channel Deployment not created within 30s"
    failures=$((failures + 1))
fi

# Check the Deployment uses the right image
if $deploy_found; then
    deploy_image=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "")
    if echo "$deploy_image" | grep -q "channel-slack"; then
        pass "Deployment image is correct: $deploy_image"
    else
        fail "Unexpected image: $deploy_image"
        failures=$((failures + 1))
    fi
fi

# Check labels
if $deploy_found; then
    channel_label=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.metadata.labels.sympozium\.io/channel}' 2>/dev/null || echo "")
    instance_label=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.metadata.labels.sympozium\.io/instance}' 2>/dev/null || echo "")
    if [[ "$channel_label" == "slack" && "$instance_label" == "$INSTANCE_NAME" ]]; then
        pass "Deployment labels correct (channel=slack, instance=$INSTANCE_NAME)"
    else
        fail "Unexpected labels: channel=$channel_label, instance=$instance_label"
        failures=$((failures + 1))
    fi
fi

# Check envFrom injects the secret
if $deploy_found; then
    env_secret=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.spec.template.spec.containers[0].envFrom[0].secretRef.name}' 2>/dev/null || echo "")
    if [[ "$env_secret" == "$CHANNEL_SECRET_REF" ]]; then
        pass "Deployment injects secret via envFrom: $env_secret"
    else
        fail "Deployment envFrom secret mismatch: expected $CHANNEL_SECRET_REF, got $env_secret"
        failures=$((failures + 1))
    fi
fi

# Verify SympoziumInstance status shows the channel
channel_status=$(kubectl get sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" \
    -o jsonpath='{.status.channels[0].type}' 2>/dev/null || echo "")
if [[ "$channel_status" == "slack" ]]; then
    pass "SympoziumInstance status shows slack channel"
else
    info "SympoziumInstance channel status: $channel_status (may need reconcile)"
fi

# Wait for pod to start (or at least be created)
if $deploy_found; then
    info "Waiting for channel pod to start..."
    elapsed=0
    pod_started=false
    while [[ $elapsed -lt 60 ]]; do
        ready=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
            -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$ready" -ge 1 ]] 2>/dev/null; then
            pod_started=true
            break
        fi
        # Even without a real token, check if the pod at least started
        pod_phase=$(kubectl get pods -n "$NAMESPACE" \
            -l "sympozium.ai/channel=slack,sympozium.ai/instance=$INSTANCE_NAME" \
            -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "")
        if [[ "$pod_phase" == "Running" ]]; then
            pod_started=true
            break
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done

    if $pod_started; then
        pass "Channel pod is running"
    else
        if $FULL_MODE; then
            fail "Channel pod not running within 60s"
            failures=$((failures + 1))
        else
            info "Channel pod not running (expected with dummy token — SLACK_BOT_TOKEN exits immediately)"
            # Still check the pod was at least created
            pod_exists=$(kubectl get pods -n "$NAMESPACE" \
                -l "sympozium.ai/channel=slack,sympozium.ai/instance=$INSTANCE_NAME" \
                -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
            if [[ -n "$pod_exists" ]]; then
                pass "Channel pod was created: $pod_exists"
            else
                info "Channel pod not yet created (image pull may be slow)"
            fi
        fi
    fi
fi

# ============================================================
# Part 2: Full E2E — send a message via the agent
# ============================================================
if $FULL_MODE; then
    echo ""
    info "=== Part 2: Full E2E — agent sends Slack message ==="

    MARKER_TEXT="sympozium-slack-test-$(date +%s)"

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
  sessionKey: "inttest-slack-$(date +%s)"
  task: |
    Use the send_channel_message tool to send a message to the slack channel.
    The message text should be exactly: "${MARKER_TEXT}"
    The chatId should be: "${SLACK_CHANNEL_ID}"
    After sending, confirm what you sent.
  model:
    provider: openai
    model: ${MODEL}
    authSecretRef: ${SECRET_NAME}
  timeout: "3m"
EOF

    # Wait for completion
    info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
    elapsed=0
    phase=""
    pod=""
    while [[ $elapsed -lt $TIMEOUT ]]; do
        phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
            -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ -z "$pod" ]]; then
            pod=$(kubectl get pods -n "$NAMESPACE" \
                -l "sympozium.ai/agentrun=$RUN_NAME" \
                -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
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

    if [[ "$phase" == "Succeeded" ]]; then
        pass "AgentRun phase: Succeeded"
    elif [[ "$phase" == "Failed" ]]; then
        fail "AgentRun phase: Failed"
        failures=$((failures + 1))
    else
        fail "AgentRun did not complete within ${TIMEOUT}s"
        failures=$((failures + 1))
    fi

    # Check result references the message
    result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
        -o jsonpath='{.status.result}' 2>/dev/null || echo "")

    if echo "$result" | grep -qi "send_channel_message\|sent\|slack\|$MARKER_TEXT"; then
        pass "AgentRun result references Slack message send"
    else
        fail "AgentRun result does not reference message send"
        failures=$((failures + 1))
        info "Result (first 500 chars):"
        echo "$result" | head -c 500
        echo ""
    fi

    # Check channel pod logs for outbound delivery
    slack_pod=$(kubectl get pods -n "$NAMESPACE" \
        -l "sympozium.ai/channel=slack,sympozium.ai/instance=$INSTANCE_NAME" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [[ -n "$slack_pod" ]]; then
        slack_logs=$(kubectl logs "$slack_pod" -n "$NAMESPACE" 2>/dev/null | tail -30 || echo "")
        if echo "$slack_logs" | grep -qi "Socket Mode connected\|outbound\|postMessage"; then
            pass "Channel pod logs show Socket Mode connection or outbound activity"
        else
            info "Channel pod logs do not show explicit send (may use different logging)"
        fi
    fi
else
    echo ""
    info "Skipping Part 2 (full E2E) — set SLACK_BOT_TOKEN, SLACK_APP_TOKEN, and SLACK_CHANNEL_ID to enable"
fi

echo ""
# --- Summary ---
echo "=============================="
echo " Integration Test Summary"
echo "=============================="
echo " Test:      Slack Channel"
echo " Mode:      $(if $FULL_MODE; then echo "Full E2E (Socket Mode)"; else echo "Deployment Pipeline"; fi)"
echo " Instance:  $INSTANCE_NAME"
echo " Model:     $MODEL"
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
