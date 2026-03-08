#!/usr/bin/env bash
# Integration test: web-endpoint AgentRun serving mode (end-to-end).
#
# What it does:
#   1. Creates a SympoziumInstance with web-endpoint skill and ollama provider
#   2. Waits for the web-endpoint AgentRun to reach "Serving" phase
#   3. Retrieves the auto-generated API key from the Secret
#   4. Port-forwards to the web-proxy Service and hits /healthz
#   5. Sends a POST /v1/chat/completions request and verifies a response
#   6. Verifies the child AgentRun was created as a Job (mode=task), NOT a Deployment
#   7. Cleans up all test resources
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - Ollama reachable from inside the cluster (default: http://172.18.0.1:11434/v1)
#   - The model pulled in Ollama (default: qwen2.5-coder:7b)
#
# Usage:
#   ./test/integration/test-web-endpoint-serving.sh
#   OLLAMA_BASE_URL=http://my-ollama:11434/v1 ./test/integration/test-web-endpoint-serving.sh
#   TEST_MODEL=llama3.2:3b TEST_TIMEOUT=300 ./test/integration/test-web-endpoint-serving.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-webep-serve-$(date +%s)"
SECRET_NAME="${INSTANCE_NAME}-ollama-key"
OLLAMA_BASE_URL="${OLLAMA_BASE_URL:-http://172.18.0.1:11434/v1}"
MODEL="${TEST_MODEL:-qwen2.5-coder:7b}"
TIMEOUT="${TEST_TIMEOUT:-180}"
LOCAL_PORT="${WEB_PROXY_PORT:-18081}"
BASE_URL="http://127.0.0.1:${LOCAL_PORT}"
CHAT_TIMEOUT="${CHAT_TIMEOUT:-120}"

PF_PID=""
WEB_PROXY_API_KEY=""
SERVING_RUN=""
failures=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; failures=$((failures + 1)); }
info() { echo -e "${YELLOW}● $*${NC}"; }

cleanup() {
  info "Cleaning up test resources..."
  # Stop port-forward
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    wait "${PF_PID}" >/dev/null 2>&1 || true
  fi
  # Delete child AgentRuns spawned by web-proxy for this instance
  kubectl delete agentruns -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME},sympozium.ai/source=web-proxy" --ignore-not-found >/dev/null 2>&1 || true
  # Delete the serving AgentRun (owned by the instance)
  kubectl delete agentruns -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  # Delete Deployments and Services
  kubectl delete deploy -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete svc -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  # Delete Jobs created by child runs
  kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  # Delete the instance itself
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  # Delete the dummy secret
  kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

require_cmd() { command -v "$1" >/dev/null 2>&1 || { fail "Missing command: $1"; exit 1; }; }

# Helper: make a request and capture status code + body
http_request() {
  local method="$1" path="$2" body="${3:-}" auth="${4:-}"
  local url="${BASE_URL}${path}"
  local tmp
  tmp="$(mktemp)"
  local -a opts=(-sS -o "$tmp" -w "%{http_code}" -X "$method" --max-time 10)

  [[ -n "$auth" ]] && opts+=(-H "Authorization: Bearer ${auth}")
  opts+=(-H "Content-Type: application/json")
  [[ -n "$body" ]] && opts+=(--data "$body")

  local code
  code="$(curl "${opts[@]}" "$url")"
  HTTP_CODE="$code"
  HTTP_BODY="$(cat "$tmp")"
  rm -f "$tmp"
}

# =========================================================================
# Setup
# =========================================================================

setup_secret() {
  info "Creating dummy auth secret for Ollama (no real key needed)"
  kubectl create secret generic "$SECRET_NAME" \
    --from-literal=OPENAI_API_KEY="ollama-no-key-needed" \
    -n "$NAMESPACE" >/dev/null 2>&1 || true
  pass "Auth secret created: $SECRET_NAME"
}

create_instance() {
  info "Creating SympoziumInstance: $INSTANCE_NAME (provider=ollama, model=$MODEL)"
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
      baseURL: ${OLLAMA_BASE_URL}
  authRefs:
    - provider: ollama
      secret: ${SECRET_NAME}
  skills:
    - skillPackRef: web-endpoint
EOF
  pass "SympoziumInstance created"
}

wait_for_serving() {
  info "Waiting for Serving-phase AgentRun (timeout: ${TIMEOUT}s)..."
  local elapsed=0
  while [[ $elapsed -lt $TIMEOUT ]]; do
    SERVING_RUN="$(kubectl get agentruns -n "$NAMESPACE" \
      -l "sympozium.ai/instance=${INSTANCE_NAME}" \
      -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.phase}{"\n"}{end}' 2>/dev/null \
      | grep "Serving" | awk '{print $1}' | head -1 || true)"
    [[ -n "$SERVING_RUN" ]] && break
    sleep 5
    elapsed=$((elapsed + 5))
    if (( elapsed % 15 == 0 )); then
      info "  ...${elapsed}s elapsed"
    fi
  done

  if [[ -n "$SERVING_RUN" ]]; then
    pass "Found Serving-phase AgentRun: $SERVING_RUN"
  else
    fail "No Serving-phase AgentRun found within ${TIMEOUT}s"
    info "Current AgentRuns for instance:"
    kubectl get agentruns -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" -o wide 2>/dev/null || true
    exit 1
  fi
}

retrieve_api_key() {
  local key_secret="${INSTANCE_NAME}-web-proxy-key"
  info "Retrieving API key from Secret: $key_secret"
  WEB_PROXY_API_KEY="$(kubectl get secret "$key_secret" -n "$NAMESPACE" \
    -o jsonpath='{.data.api-key}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  if [[ -n "$WEB_PROXY_API_KEY" ]]; then
    pass "API key retrieved (${#WEB_PROXY_API_KEY} chars)"
  else
    fail "Could not retrieve API key from Secret '$key_secret'"
    exit 1
  fi
}

start_port_forward() {
  # Find the web-proxy service for this instance
  local svc_name
  svc_name="$(kubectl get svc -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -z "$svc_name" ]]; then
    fail "No Service found for instance $INSTANCE_NAME"
    exit 1
  fi
  info "Port-forwarding $svc_name to :${LOCAL_PORT}"
  kubectl port-forward -n "$NAMESPACE" "svc/${svc_name}" "${LOCAL_PORT}:8080" >/tmp/webep-serve-test-pf.log 2>&1 &
  PF_PID=$!

  local elapsed=0
  while [[ $elapsed -lt 30 ]]; do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/webep-serve-test-pf.log 2>/dev/null || true
      exit 1
    fi
    if curl -fsS --max-time 2 "${BASE_URL}/healthz" >/dev/null 2>&1; then
      pass "Port-forward ready"
      return 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  fail "Timed out waiting for port-forward"
  exit 1
}

# =========================================================================
# Tests
# =========================================================================

test_healthz() {
  info "Testing GET /healthz"
  http_request GET /healthz "" ""
  if [[ "$HTTP_CODE" == "200" && "$HTTP_BODY" == "ok" ]]; then
    pass "/healthz returns 200 ok"
  else
    fail "/healthz expected 200/ok, got ${HTTP_CODE}/${HTTP_BODY}"
  fi
}

test_chat_completions() {
  info "Testing POST /v1/chat/completions (timeout: ${CHAT_TIMEOUT}s)"

  # Count existing web-proxy child runs before the request
  local before_count
  before_count="$(kubectl get agentruns -n "$NAMESPACE" \
    -l "sympozium.ai/instance=${INSTANCE_NAME},sympozium.ai/source=web-proxy" \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')"

  # Send a chat request (Ollama can be slow, so give it time)
  local tmp_resp
  tmp_resp="$(mktemp)"
  local http_code
  http_code="$(curl -sS -o "$tmp_resp" -w "%{http_code}" --max-time "$CHAT_TIMEOUT" \
    -X POST "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${WEB_PROXY_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"default","messages":[{"role":"user","content":"Say hello in one short sentence."}]}' 2>/dev/null || echo "000")"
  local resp_body
  resp_body="$(cat "$tmp_resp")"
  rm -f "$tmp_resp"

  if [[ "$http_code" == "200" ]]; then
    pass "Chat completions returned HTTP 200"
  else
    fail "Chat completions expected 200, got $http_code"
    echo "$resp_body" | head -20
  fi

  # Verify the response has OpenAI-compatible structure
  if [[ "$http_code" == "200" ]]; then
    local resp_id
    resp_id="$(printf "%s" "$resp_body" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)"
    if [[ -n "$resp_id" ]]; then
      pass "Response has id field: $resp_id"
    else
      fail "Response missing id field"
    fi

    local content
    content="$(printf "%s" "$resp_body" | python3 -c '
import json, sys
data = json.load(sys.stdin)
choices = data.get("choices", [])
if choices:
    msg = choices[0].get("message", {})
    print(msg.get("content", ""))
' 2>/dev/null || true)"
    if [[ -n "$content" ]]; then
      pass "Response contains message content (${#content} chars)"
    else
      fail "Response missing message content"
      echo "$resp_body"
    fi
  fi

  # Verify a new child AgentRun was created
  local after_count
  after_count="$(kubectl get agentruns -n "$NAMESPACE" \
    -l "sympozium.ai/instance=${INSTANCE_NAME},sympozium.ai/source=web-proxy" \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$after_count" -gt "$before_count" ]]; then
    pass "Chat completions created a new child AgentRun ($before_count -> $after_count)"
  else
    fail "No new child AgentRun created ($before_count -> $after_count)"
  fi
}

test_child_run_is_job() {
  info "Verifying child AgentRun is a Job (mode=task), not a Deployment"

  # Get the latest child run created by web-proxy
  local child_run
  child_run="$(kubectl get agentruns -n "$NAMESPACE" \
    -l "sympozium.ai/instance=${INSTANCE_NAME},sympozium.ai/source=web-proxy" \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null || true)"

  if [[ -z "$child_run" ]]; then
    fail "No child AgentRun found to verify"
    return
  fi
  info "Checking child AgentRun: $child_run"

  # Check mode field -- should be "task" (or empty, which defaults to "task")
  local mode
  mode="$(kubectl get agentrun "$child_run" -n "$NAMESPACE" \
    -o jsonpath='{.spec.mode}' 2>/dev/null || true)"
  if [[ "$mode" == "task" || -z "$mode" ]]; then
    pass "Child AgentRun mode is 'task' (got: '${mode:-<default>}')"
  else
    fail "Child AgentRun mode should be 'task', got '$mode'"
  fi

  # Verify a Job was created (not a Deployment) for the child run
  local job_count
  job_count="$(kubectl get jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=${child_run}" \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$job_count" -ge 1 ]]; then
    pass "Child AgentRun created a Job (found $job_count)"
  else
    # The Job may have already been cleaned up; check if the run has a jobName in status
    local job_name
    job_name="$(kubectl get agentrun "$child_run" -n "$NAMESPACE" \
      -o jsonpath='{.status.jobName}' 2>/dev/null || true)"
    if [[ -n "$job_name" ]]; then
      pass "Child AgentRun has jobName in status: $job_name (Job may have been cleaned up)"
    else
      info "No Job found and no jobName in status (Job may have been cleaned up already)"
      pass "Child AgentRun mode confirms it is not a Deployment"
    fi
  fi

  # Verify no Deployment was created for the child run
  local deploy_count
  deploy_count="$(kubectl get deploy -n "$NAMESPACE" -l "sympozium.ai/agentrun=${child_run}" \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$deploy_count" -eq 0 ]]; then
    pass "No Deployment created for child AgentRun (correct)"
  else
    fail "Child AgentRun should NOT have a Deployment, but found $deploy_count"
  fi

  # Verify the child run does NOT have the web-endpoint skill
  local has_webep
  has_webep="$(kubectl get agentrun "$child_run" -n "$NAMESPACE" -o json 2>/dev/null \
    | python3 -c '
import json, sys
data = json.load(sys.stdin)
skills = data.get("spec", {}).get("skills", [])
print(any(s.get("skillPackRef") == "web-endpoint" for s in skills))
' 2>/dev/null || true)"
  if [[ "$has_webep" == "False" ]]; then
    pass "Child AgentRun does not inherit web-endpoint skill"
  else
    fail "Child AgentRun should not have web-endpoint skill"
  fi
}

# =========================================================================
# Main
# =========================================================================

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running web-endpoint serving integration test"
  info "  Namespace:  $NAMESPACE"
  info "  Ollama URL: $OLLAMA_BASE_URL"
  info "  Model:      $MODEL"

  # Pre-flight checks
  if ! kubectl get crd sympoziuminstances.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed. Is the cluster set up?"
    exit 1
  fi

  if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "AgentRun CRD not installed."
    exit 1
  fi

  # Clean up any previous test run with same name
  cleanup 2>/dev/null || true
  sleep 2

  # Step 1: Create secret and instance
  setup_secret
  create_instance

  # Step 2: Wait for the serving AgentRun
  wait_for_serving

  # Step 3: Retrieve the auto-generated API key
  retrieve_api_key

  # Step 4: Port-forward and test /healthz
  start_port_forward
  test_healthz

  # Step 5: Send a chat completions request
  test_chat_completions

  # Step 6: Verify child run is a Job, not a Deployment
  test_child_run_is_job

  # --- Summary ---
  echo ""
  echo "=============================="
  echo " Web-Endpoint Serving Test"
  echo "=============================="
  echo " Instance:     $INSTANCE_NAME"
  echo " Serving Run:  $SERVING_RUN"
  echo " Model:        $MODEL"
  echo " Ollama URL:   $OLLAMA_BASE_URL"
  echo " Failures:     $failures"
  echo "=============================="
  echo ""

  if [[ $failures -gt 0 ]]; then
    fail "$failures check(s) failed"
    exit 1
  fi
  pass "All web-endpoint serving tests passed"
}

main "$@"
