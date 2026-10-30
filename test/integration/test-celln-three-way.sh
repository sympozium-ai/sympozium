#!/usr/bin/env bash
# The three-way acceptance shape on an installed Celln fleet: two Agents'
# conversations plus a one-shot run, all asked the same question at the same
# time, each must answer it. Run against a cluster where test-celln-fleet.sh
# installed the fleet and was kept (KEEP_CLUSTER=1).
#
# The model is the Agents' own ModelConnection, never the fleet's (the fleet
# holds none):
#   - default: a keyless OpenAI-compatible server (LLAMA_ORIGIN + LLAMA_MODEL,
#     the private origin test-celln-fleet.sh approved at install);
#   - or, when ACCEPT_API_KEY is set: a keyed provider at ACCEPT_ENDPOINT
#     (an HTTPS chat-completions URL), ACCEPT_MODEL, ACCEPT_PROVIDER. The key
#     is read from the environment only, posted once to the API on stdin and
#     never printed; the API stores it in the connection's own Secret.
#
# Inputs: FLEET_CLUSTER, the model inputs above, optional ACCEPT_QUESTION +
# ACCEPT_ANSWER (a case-insensitive word every answer must contain),
# ACCEPT_EVIDENCE (a JSON file the timings are written to), ACCEPT_TIMEOUT
# (seconds, default 600).
set -euo pipefail
CLUSTER="${FLEET_CLUSTER:?FLEET_CLUSTER names the Kind cluster}"
NS=celln-three-way
QUESTION="${ACCEPT_QUESTION:-What is the capital of France? Reply with only the city name.}"
ANSWER="${ACCEPT_ANSWER:-paris}"
EVIDENCE="${ACCEPT_EVIDENCE:-/dev/null}"
LIMIT="${ACCEPT_TIMEOUT:-600}"
log() { printf '\033[1;33m---- %s\033[0m\n' "$*"; }
pass() { printf '\033[0;32mPASS %s\033[0m\n' "$*"; }
fail() { printf '\033[0;31mFAIL %s\033[0m\n' "$*" >&2; exit 1; }
kc() { kubectl --context "kind-$CLUSTER" "$@"; }
if [ -n "${ACCEPT_API_KEY:-}" ]; then
	: "${ACCEPT_ENDPOINT:?ACCEPT_ENDPOINT is required with ACCEPT_API_KEY}"
	: "${ACCEPT_MODEL:?ACCEPT_MODEL is required with ACCEPT_API_KEY}"
	case "$ACCEPT_ENDPOINT" in https://*) ;; *) fail "ACCEPT_ENDPOINT must be https://" ;; esac
	provider="${ACCEPT_PROVIDER:-openai}" model="$ACCEPT_MODEL" mode=keyed
else
	: "${LLAMA_ORIGIN:?LLAMA_ORIGIN (or ACCEPT_API_KEY + ACCEPT_ENDPOINT) is required}"
	: "${LLAMA_MODEL:?LLAMA_MODEL is required}"
	provider=llama-server model="$LLAMA_MODEL" mode=keyless
fi
pf=""
cleanup() {
	[ -z "$pf" ] || kill "$pf" 2>/dev/null || true
	kc delete namespace "$NS" --wait=false --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "API access"
api_token="$(kc -n sympozium-system get secret sympozium-ui-token -o jsonpath='{.data.token}' | base64 -d)"
api_auth=(-H "Authorization: Bearer $api_token")
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
kc -n sympozium-system port-forward svc/sympozium-apiserver "$port:8080" >/dev/null 2>&1 &
pf=$!
API="http://127.0.0.1:$port"
kc create namespace "$NS" >/dev/null 2>&1 || true
i=0
until curl -sf "${api_auth[@]}" "$API/api/v1/celln-platform/profiles?namespace=$NS" -o /dev/null; do
	[ "$i" -lt 120 ] || fail "apiserver port-forward"
	sleep 2
	i=$((i + 2))
done
api_json() { curl -sf "${api_auth[@]}" -H 'Content-Type: application/json' "$@"; }

log "One connection ($mode, $provider), the toolbox wrapper, two Agents"
if [ "$mode" = keyed ]; then
	python3 -c '
import json, os
print(json.dumps({"name": "accept", "apiKey": os.environ["ACCEPT_API_KEY"], "spec": {"provider": os.environ.get("ACCEPT_PROVIDER", "openai"),
  "protocol": "openai-chat", "endpoint": os.environ["ACCEPT_ENDPOINT"], "models": [os.environ["ACCEPT_MODEL"]], "maxOutputTokens": 512}}))' |
		api_json -X POST --data-binary @- "$API/api/v1/model-connections?namespace=$NS" >/dev/null || fail "API refused the keyed connection"
else
	python3 -c '
import json, sys
origin, model = sys.argv[1:3]
print(json.dumps({"name": "accept", "spec": {"provider": "llama-server", "protocol": "openai-chat", "endpoint": origin + "/v1/chat/completions",
  "models": [model], "allowInsecure": True, "parameters": {"chat_template_kwargs": {"enable_thinking": False}}, "maxOutputTokens": 512}}))' "$LLAMA_ORIGIN" "$LLAMA_MODEL" |
		api_json -X POST --data-binary @- "$API/api/v1/model-connections?namespace=$NS" >/dev/null || fail "API refused the keyless connection"
fi
profiles="$(curl -sf "${api_auth[@]}" "$API/api/v1/celln-platform/profiles?namespace=$NS")"
toolbox_profile="$(echo "$profiles" | python3 -c 'import json,sys; print(next(p["toolboxProfile"] for p in json.load(sys.stdin) if p.get("toolboxProfile")))')" || fail "no toolbox profile offered in $NS"
api_json -X POST -d "{\"profile\":\"$toolbox_profile\",\"runtimeOnly\":true}" "$API/api/v1/celln-platform/wrappers?namespace=$NS" >/dev/null || fail "toolbox wrapper"
IFS=$'\t' read -r wrapper tools < <(echo "$profiles" | python3 -c 'import json,sys; p=next(p for p in json.load(sys.stdin) if p.get("toolboxProfile")); print(p["toolboxWrapper"]+"\t"+json.dumps(p["toolboxTools"]))')
for agent in alpha beta; do
	python3 -c '
import json, sys
name, model, provider, wrapper, tools = sys.argv[1:6]
print(json.dumps({"name": name, "provider": provider, "model": model, "runtimeRef": wrapper,
  "execution": {"backend": "celln", "executionLifecycle": "enduring", "modelConnectionRef": "accept", "model": model,
    "systemPrompt": "You are acceptance agent " + name + ". Answer with only what is asked.",
    "cellnSelection": {"runtimeRef": wrapper, "toolRefs": [], "clusterToolRefs": json.loads(tools)},
    "enduring": {"leaseSeconds": 1800, "maxTurns": 12, "maxModelRequests": 64, "maxOutputTokens": 98304}}}))' \
		"$agent" "$model" "$provider" "$wrapper" "$tools" |
		api_json -X POST --data-binary @- "$API/api/v1/agents?namespace=$NS" >/dev/null || fail "API refused Agent $agent"
done
pass "connection accept, wrapper $wrapper, Agents alpha and beta"

# submit agent lifecycle -> run name
submit() {
	kc -n "$NS" get agent "$1" -o json | python3 -c '
import json, sys
e = json.load(sys.stdin)["spec"]["execution"]
agent, lifecycle, task = sys.argv[1:4]
run = {"agentRef": agent, "task": task, "backend": "celln", "executionLifecycle": lifecycle,
  "model": e["model"], "modelConnectionRef": e["modelConnectionRef"],
  "cellnSelection": {"runtimeRef": e["cellnSelection"]["runtimeRef"], "clusterToolRefs": e["cellnSelection"].get("clusterToolRefs", []), "toolRefs": []}}
if lifecycle == "enduring":
    run["enduring"] = {"leaseSeconds": 1800, "maxTurns": 12, "maxModelRequests": 64, "maxOutputTokens": 98304}
print(json.dumps(run))' "$1" "$2" "$QUESTION" |
		api_json -X POST --data-binary @- "$API/api/v1/runs?namespace=$NS" |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["name"])'
}

log "Three runs, one question, submitted together: alpha (conversation), beta (conversation), alpha (one-shot)"
start="$(date +%s.%N)"
run_a="$(submit alpha enduring)" || fail "API refused alpha's conversation"
run_b="$(submit beta enduring)" || fail "API refused beta's conversation"
run_o="$(submit alpha one-shot)" || fail "API refused the one-shot run"
declare -A done_at=()
deadline=$(( $(date +%s) + LIMIT ))
while [ ${#done_at[@]} -lt 3 ]; do
	[ "$(date +%s)" -lt "$deadline" ] || fail "not every run answered within ${LIMIT}s (answered: ${!done_at[*]})"
	for run in "$run_a" "$run_b" "$run_o"; do
		[ -z "${done_at[$run]:-}" ] || continue
		phase="$(kc -n "$NS" get agentrun "$run" -o jsonpath='{.status.phase}')"
		[ "$phase" != Failed ] || fail "$run failed: $(kc -n "$NS" get agentrun "$run" -o jsonpath='{.status.error}')"
		[ -z "$(kc -n "$NS" get agentrun "$run" -o jsonpath='{.status.result}')" ] || done_at[$run]="$(date +%s.%N)"
	done
	sleep 2
done
for run in "$run_a" "$run_b" "$run_o"; do
	result="$(kc -n "$NS" get agentrun "$run" -o jsonpath='{.status.result}')"
	printf '%s' "$result" | grep -qi -- "$ANSWER" || fail "$run did not answer '$ANSWER': ${result:0:200}"
done
python3 -c '
import json, sys
start, mode, provider, model = float(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
runs = []
for item in sys.argv[5:]:
    kind, run, at = item.split("=")
    runs.append({"shape": kind, "run": run, "seconds": round(float(at) - start, 1)})
print(json.dumps({"mode": mode, "provider": provider, "model": model, "runs": runs}, indent=2))' \
	"$start" "$mode" "$provider" "$model" \
	"alpha-conversation=$run_a=${done_at[$run_a]}" "beta-conversation=$run_b=${done_at[$run_b]}" "alpha-one-shot=$run_o=${done_at[$run_o]}" |
	tee "$EVIDENCE"
pass "all three runs answered '$ANSWER' concurrently"
