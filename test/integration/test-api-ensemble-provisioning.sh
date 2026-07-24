#!/usr/bin/env bash
# API integration test: Ensemble provisioning behavior.
# Validates that enabling a Ensemble via API stamps out Instances/Schedules.
# Uses a self-contained temporary Ensemble to avoid stale cluster state.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
PACK_NAME="inttest-provision-$(date +%s)"

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    for _ in {1..5}; do
      if ! kill -0 "${PF_PID}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    if kill -0 "${PF_PID}" >/dev/null 2>&1; then
      kill -9 "${PF_PID}" >/dev/null 2>&1 || true
    fi
    wait "${PF_PID}" >/dev/null 2>&1 || true
  fi

  if command -v pkill >/dev/null 2>&1; then
    pkill -f "kubectl port-forward -n ${APISERVER_NAMESPACE} svc/sympozium-apiserver ${PORT_FORWARD_LOCAL_PORT}:8080" >/dev/null 2>&1 || true
  fi

  PF_PID=""
}

cleanup() {
  info "Cleaning up Ensemble API test resources..."
  api_request DELETE "/api/v1/ensembles/${PACK_NAME}" >/dev/null 2>&1 || true
  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  stop_port_forward
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { fail "Required command not found: $1"; exit 1; }
}

url_with_namespace() {
  local path="$1"
  if [[ "$path" == *"?"* ]]; then
    echo "${APISERVER_URL}${path}&namespace=${NAMESPACE}"
  else
    echo "${APISERVER_URL}${path}?namespace=${NAMESPACE}"
  fi
}

api_request() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local url="$(url_with_namespace "$path")"
  local tmp="$(mktemp)"
  local -a headers
  headers=(-H "Content-Type: application/json")
  if [[ -n "${APISERVER_TOKEN}" ]]; then
    headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
  fi

  local code
  if [[ -n "$body" ]]; then
    code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" --data "$body" "$url")"
  else
    code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" "$url")"
  fi

  local resp="$(cat "$tmp")"
  rm -f "$tmp"

  if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
    fail "API ${method} ${path} failed (HTTP ${code})"
    echo "$resp"
    return 1
  fi
  printf "%s" "$resp"
}

resolve_apiserver_token() {
  if [[ -n "${APISERVER_TOKEN}" ]]; then
    return 0
  fi

  local token

  # 1. Literal env value — set when apiserver.webUI.token is pinned in values.
  token="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].value}' 2>/dev/null || true)"
  if [[ -n "$token" ]]; then
    APISERVER_TOKEN="$token"
    return 0
  fi

  # 2. Volume-mounted Secret — production chart with no webUI.token. The
  #    apiserver hot-reloads by re-reading this file on every request, so
  #    a Secret rotation propagates without a pod restart.
  local secret_name
  secret_name="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.volumes[?(@.name=="sympozium-ui-token")].secret.secretName}' 2>/dev/null || true)"
  if [[ -n "$secret_name" ]]; then
    token="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "$secret_name" \
      -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true)"
    if [[ -n "$token" ]]; then
      APISERVER_TOKEN="$token"
      return 0
    fi
  fi

  # 3. Legacy chart (env.valueFrom.secretKeyRef) — kept for deployments
  #    that have not yet been upgraded to the volume mount.
  local legacy_secret_name legacy_secret_key
  legacy_secret_name="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.name}' 2>/dev/null || true)"
  legacy_secret_key="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.key}' 2>/dev/null || true)"
  if [[ -z "$legacy_secret_key" ]]; then legacy_secret_key="token"; fi
  if [[ -n "$legacy_secret_name" ]]; then
    token="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "$legacy_secret_name" \
      -o jsonpath="{.data.${legacy_secret_key}}" 2>/dev/null | base64 -d 2>/dev/null || true)"
    if [[ -n "$token" ]]; then
      APISERVER_TOKEN="$token"
      return 0
    fi
  fi

  # Token may be disabled in some local setups.
  APISERVER_TOKEN=""
}

start_port_forward_if_needed() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then return 0; fi
  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then return 0; fi

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-ensemble-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-ensemble-portforward.log || true
      exit 1
    fi
    if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
      pass "Port-forward ready"
      return 0
    fi
    sleep 1
  done

  fail "Timed out waiting for API server via port-forward"
  exit 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running Ensemble provisioning API test in namespace '${NAMESPACE}'"

  start_port_forward_if_needed
  resolve_apiserver_token

  # Create a dedicated temporary Ensemble with two personas (both with schedules).
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  description: "Integration test provisioning pack"
  category: "integration"
  version: "1.0.0"
  enabled: false
  agentConfigs:
    - name: planner
      displayName: "Test Planner"
      systemPrompt: "You are a planner for integration testing."
      skills:
        - code-review
      schedule:
        type: scheduled
        cron: "*/10 * * * *"
        task: "plan integration work"
    - name: executor
      displayName: "Test Executor"
      systemPrompt: "You are an executor for integration testing."
      skills:
        - code-review
      schedule:
        type: sweep
        interval: "15m"
        task: "execute integration work"
EOF
  pass "Created temporary Ensemble '${PACK_NAME}'"

  # Enable the pack via API.
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" "{\"enabled\":true}" >/dev/null
  pass "Enabled Ensemble '${PACK_NAME}'"

  # Wait for stamped instances and schedules to appear.
  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    instances_json="$(api_request GET "/api/v1/agents")"
    schedules_json="$(api_request GET "/api/v1/schedules")"

    inst_count="$(printf "%s" "$instances_json" | python3 -c 'import json,sys; p=sys.argv[1]; d=json.load(sys.stdin); print(sum(1 for i in d if i.get("metadata",{}).get("labels",{}).get("sympozium.ai/ensemble")==p))' "$PACK_NAME")"
    sched_count="$(printf "%s" "$schedules_json" | python3 -c 'import json,sys; p=sys.argv[1]; d=json.load(sys.stdin); print(sum(1 for i in d if i.get("metadata",{}).get("labels",{}).get("sympozium.ai/ensemble")==p))' "$PACK_NAME")"

    if [[ "$inst_count" -ge 2 && "$sched_count" -ge 2 ]]; then
      pass "Ensemble stamped resources (instances=${inst_count}, schedules=${sched_count})"
      break
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for Ensemble '${PACK_NAME}' stamped resources"
    exit 1
  fi

  pass "Ensemble provisioning API test passed"
}

main "$@"
