#!/usr/bin/env bash
# API integration test: PersonaPack provisioning behavior.
# Validates that enabling a PersonaPack via API stamps out Instances/Schedules.

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
PACK_NAME=""
ORIGINAL_ENABLED="false"

cleanup() {
  info "Cleaning up PersonaPack API test resources..."
  if [[ -n "${PACK_NAME}" ]]; then
    api_request PATCH "/api/v1/personapacks/${PACK_NAME}" "{\"enabled\":${ORIGINAL_ENABLED}}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${PF_PID}" ]]; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
  fi
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
  if [[ -n "${APISERVER_TOKEN}" ]]; then return 0; fi

  local token
  token="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].value}' 2>/dev/null || true)"
  if [[ -n "$token" ]]; then APISERVER_TOKEN="$token"; return 0; fi

  local secret_name secret_key
  secret_name="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.name}' 2>/dev/null || true)"
  secret_key="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.key}' 2>/dev/null || true)"
  [[ -z "$secret_key" ]] && secret_key="token"
  if [[ -n "$secret_name" ]]; then
    token="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "$secret_name" -o jsonpath="{.data.${secret_key}}" 2>/dev/null | base64 -d 2>/dev/null || true)"
    [[ -n "$token" ]] && APISERVER_TOKEN="$token"
  fi
}

start_port_forward_if_needed() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then return 0; fi
  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then return 0; fi

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-personapack-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-personapack-portforward.log || true
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

  info "Running PersonaPack provisioning API test in namespace '${NAMESPACE}'"

  start_port_forward_if_needed
  resolve_apiserver_token

  api_request POST "/api/v1/personapacks/install-defaults" >/dev/null
  packs_json="$(api_request GET "/api/v1/personapacks")"

  PACK_NAME="$(printf "%s" "$packs_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["metadata"]["name"] if isinstance(d,list) and d else "")')"
  if [[ -z "${PACK_NAME}" ]]; then
    fail "No PersonaPacks found after install-defaults"
    exit 1
  fi

  ORIGINAL_ENABLED="$(api_request GET "/api/v1/personapacks/${PACK_NAME}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("true" if d.get("spec",{}).get("enabled", False) else "false")')"

  api_request PATCH "/api/v1/personapacks/${PACK_NAME}" "{\"enabled\":true}" >/dev/null
  pass "Enabled PersonaPack '${PACK_NAME}'"

  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    instances_json="$(api_request GET "/api/v1/instances")"
    schedules_json="$(api_request GET "/api/v1/schedules")"

    inst_count="$(printf "%s" "$instances_json" | python3 -c 'import json,sys; p=sys.argv[1]; d=json.load(sys.stdin); print(sum(1 for i in d if i.get("metadata",{}).get("labels",{}).get("sympozium.ai/persona-pack")==p))' "$PACK_NAME")"
    sched_count="$(printf "%s" "$schedules_json" | python3 -c 'import json,sys; p=sys.argv[1]; d=json.load(sys.stdin); print(sum(1 for i in d if i.get("metadata",{}).get("labels",{}).get("sympozium.ai/persona-pack")==p))' "$PACK_NAME")"

    if [[ "$inst_count" -gt 0 && "$sched_count" -gt 0 ]]; then
      pass "PersonaPack stamped resources (instances=${inst_count}, schedules=${sched_count})"
      break
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for PersonaPack '${PACK_NAME}' stamped resources"
    exit 1
  fi

  pass "PersonaPack provisioning API test passed"
}

main "$@"
