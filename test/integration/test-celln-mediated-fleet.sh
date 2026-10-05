#!/usr/bin/env bash
# Mediated model access as the default, on more than one node. A bare
# `sympozium install` with a provider key in the environment must:
#   - run the model gateway (and its bundled PostgreSQL) without any flags,
#   - give the installer's key to one starter Agent, never to the nodes,
#   - refuse that key to any other Agent,
#   - route mediated conversations through the router to whichever node
#     prepared them, spreading them across nodes,
#   - end a conversation whose node is lost (AUTH_CONTEXT_LOST) while a
#     conversation on another node carries on,
#   - keep skills away from Secrets (admission policy present).
#
# Inputs: CELLN_BUNDLE (a Celln release bundle: bin/celln + share/celln) and
# a model: either DEEPSEEK_API_KEY (a real key; the starter Agent owns it and
# the conversations call DeepSeek through the gateway) or LLAMA_ORIGIN +
# LLAMA_MODEL (a keyless OpenAI-compatible server, e.g. llama-server, on a
# private address; the conversations reach it through the gateway on an
# explicitly approved keyless route, and key isolation is shown with a dummy
# key Secret, which is refused before any model call). Optional
# FLEET_CERT_MANAGER_MANIFEST, FLEET_CLUSTER, FLEET_WORK, KEEP_CLUSTER=1.
# Images: controller, apiserver, webhook, node-probe, model-gateway and
# celln-installer built with TAG (make docker-build-<name> TAG=...), and the
# Celln image tagged CELLN_IMAGE.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
CLUSTER="${FLEET_CLUSTER:-mediated}"
TAG="${SYMPOZIUM_IMAGE_TAG:-fleet-trial}"
CELLN_IMAGE="${CELLN_IMAGE:-celln:$TAG}"
REGISTRY_NAME="${FLEET_REGISTRY_NAME:-kind-registry}"
WORK="${FLEET_WORK:-$(mktemp -d /tmp/celln-mediated.XXXXXX)}"
: "${CELLN_BUNDLE:?a Celln bundle directory (bin/celln, share/celln) is required}"
if [ -n "${LLAMA_ORIGIN:-}" ]; then
	: "${LLAMA_MODEL:?LLAMA_MODEL names the model the server serves}"
	MODE=keyless
else
	: "${DEEPSEEK_API_KEY:?DEEPSEEK_API_KEY, or LLAMA_ORIGIN and LLAMA_MODEL, is required}"
	MODE=keyed
fi
log() { printf '\033[1;33m---- %s\033[0m\n' "$*"; }
pass() { printf '\033[0;32mPASS %s\033[0m\n' "$*"; }
fail() { printf '\033[0;31mFAIL %s\033[0m\n' "$*" >&2; exit 1; }
kc() { kubectl --context "kind-$CLUSTER" "$@"; }
wait_for() { # description seconds command...
	local what="$1" limit="$2" i=0
	shift 2
	until "$@" >/dev/null 2>&1; do
		[ "$i" -lt "$limit" ] || fail "$what did not happen within ${limit}s"
		sleep 5
		i=$((i + 5))
	done
}
mkdir -p "$WORK"
if [ "${KEEP_CLUSTER:-}" != 1 ]; then
	trap 'kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true' EXIT
fi

log "Cluster (one control plane, two KVM workers) and a local registry the nodes pull from"
if ! docker inspect "$REGISTRY_NAME" >/dev/null 2>&1; then
	docker run -d --restart=always -p 127.0.0.1:5001:5000 --name "$REGISTRY_NAME" registry:2 >/dev/null
fi
if ! kind get clusters | grep -qx "$CLUSTER"; then
	cat >"$WORK/kind.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
	kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 120s
fi
docker network connect kind "$REGISTRY_NAME" 2>/dev/null || true
registry="$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$REGISTRY_NAME"):5000"
for node in $(kind get nodes --name "$CLUSTER"); do
	docker exec "$node" mkdir -p /etc/containerd/certs.d/localhost:5001
	printf '[host."http://%s:5000"]\n' "$REGISTRY_NAME" | docker exec -i "$node" tee /etc/containerd/certs.d/localhost:5001/hosts.toml >/dev/null
done
kernel="/boot/vmlinuz-$(uname -r)"
[ -r "$kernel" ] || fail "readable host kernel required at $kernel"
for node in $(kind get nodes --name "$CLUSTER" | grep worker); do
	docker exec "$node" test -f "/boot/$(basename "$kernel")" || docker cp "$kernel" "$node:/boot/"
done
pass "cluster $CLUSTER with registry $registry"

log "Starter package and a CLI that pins it and the model gateway, as a release build would"
"$REPO/hack/build-celln-starter.sh" --bundle "$CELLN_BUNDLE" --kernel "$kernel" \
	--image "localhost:5001/celln/starter:mediated" --out "$WORK/starter" --push >"$WORK/starter-build.log" 2>&1 || { tail -5 "$WORK/starter-build.log"; fail "starter package"; }
digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"].split("@")[1])' "$WORK/starter/starter.json")"
package_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["packageHash"])' "$WORK/starter/starter.json")"
publisher="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["publisher"])' "$WORK/starter/starter.json")"
image="$registry/celln/starter@$digest"
docker tag "ghcr.io/sympozium-ai/sympozium/model-gateway:$TAG" "localhost:5001/sympozium/model-gateway:$TAG"
docker push -q "localhost:5001/sympozium/model-gateway:$TAG" >/dev/null
gateway="localhost:5001/sympozium/model-gateway@$(docker buildx imagetools inspect "localhost:5001/sympozium/model-gateway:$TAG" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')"
(cd "$REPO" && go build -ldflags "-X main.cellnStarterImage=$image -X main.cellnStarterHash=$package_hash -X main.cellnStarterPublisher=$publisher -X main.cellnStarterCellnVersion=journey -X main.modelGatewayImage=$gateway" -o "$WORK/sympozium" ./cmd/sympozium)
pass "sympozium built with package $package_hash and gateway $gateway"

log "Images"
for img in "$CELLN_IMAGE" "ghcr.io/sympozium-ai/sympozium/controller:$TAG" "ghcr.io/sympozium-ai/sympozium/apiserver:$TAG" "ghcr.io/sympozium-ai/sympozium/webhook:$TAG" "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" "ghcr.io/sympozium-ai/sympozium/node-probe:$TAG"; do
	kind load docker-image --name "$CLUSTER" "$img" >/dev/null
done
pass "images loaded"

install_env=() install_args=()
if [ "$MODE" = keyless ]; then
	install_env=(env -u DEEPSEEK_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY "SYMPOZIUM_CELLN_BACKEND=name=native,provider=llama-server,model=$LLAMA_MODEL,endpoint=$LLAMA_ORIGIN/v1/chat/completions,allow-insecure=true")
	install_args=(--celln-mediated-route "provider=llama-server,protocol=openai-chat,auth=none,allowInsecure=true,origin=$LLAMA_ORIGIN,models=$LLAMA_MODEL" --set "modelGateway.privateOrigins[0]=$LLAMA_ORIGIN")
fi
log "sympozium install — only a model backend in the environment ($MODE)"
if [ -n "${FLEET_CERT_MANAGER_MANIFEST:-}" ]; then
	kc apply -f "$FLEET_CERT_MANAGER_MANIFEST" >/dev/null
	kc -n cert-manager wait --for=condition=Available deploy --all --timeout=180s >/dev/null
fi
kind get kubeconfig --name "$CLUSTER" >"$WORK/kubeconfig"
KUBECONFIG="$WORK/kubeconfig" "${install_env[@]}" "$WORK/sympozium" install "${install_args[@]}" \
	--celln-fleet-output-dir "$WORK/fleet-out" --celln-fleet-wait 20m \
	--celln-router-image "$CELLN_IMAGE" \
	--celln-installer-image "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" \
	--set celln.fleet.package.insecureRegistry=true \
	--set "controller.image.tag=$TAG" --set "apiserver.image.tag=$TAG" --set "webhook.image.tag=$TAG" --set "nodeProbe.image.tag=$TAG" \
	</dev/null >"$WORK/install.log" 2>&1 || { tail -40 "$WORK/install.log"; fail "sympozium install"; }
grep -qi 'mediat' "$WORK/install.log" || fail "the install did not report mediated model access: $(tail -20 "$WORK/install.log")"
pass "installed (log: $WORK/install.log)"

log "The gateway, its database and the trust came up without any flags"
kc -n sympozium-system rollout status deploy/sympozium-model-gateway --timeout=300s >/dev/null || fail "model gateway not ready"
kc -n sympozium-system rollout status statefulset/sympozium-model-gateway-db --timeout=300s >/dev/null || fail "bundled database not ready"
for obj in secret/celln-mediation-controller secret/celln-mediation-gateway configmap/celln-mediation-trust; do
	kc -n sympozium-system get "$obj" >/dev/null || fail "$obj missing"
done
pass "gateway, bundled PostgreSQL and mediation trust are in place"

starter_ns=default
if [ "$MODE" = keyed ]; then
log "The installer's key belongs to the starter Agent, not the nodes"
owner="$(kc -n "$starter_ns" get secret starter-model-key -o jsonpath='{.metadata.annotations.sympozium\.ai/model-key-owner}')"
[ "$owner" = "Agent/starter" ] || fail "starter key owner is '$owner'"
kc -n "$starter_ns" get agent starter >/dev/null || fail "no starter Agent"
if kc -n celln-system get secret celln-fleet-model-credentials -o json | python3 -c '
import base64, json, os, sys
key = os.environ["DEEPSEEK_API_KEY"].encode()
data = json.load(sys.stdin).get("data", {})
sys.exit(0 if any(base64.b64decode(v).strip() == key for v in data.values()) else 1)'; then
	fail "the provider key was published to the fleet nodes"
fi
pass "starter-model-key is owned by Agent/starter and no node holds the key"
fi

log "API access"
api_token="$(kc -n sympozium-system get secret sympozium-ui-token -o jsonpath='{.data.token}' | base64 -d)"
api_auth=(-H "Authorization: Bearer $api_token")
kc -n sympozium-system rollout status deploy/sympozium-apiserver --timeout=300s >/dev/null || fail "apiserver rollout"
api_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
kubectl --context "kind-$CLUSTER" -n sympozium-system port-forward svc/sympozium-apiserver "$api_port:8080" >/dev/null 2>&1 &
api_pf=$!
if [ "${KEEP_CLUSTER:-}" != 1 ]; then
	trap 'kill "$api_pf" 2>/dev/null || true; kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true' EXIT
else
	trap 'kill "$api_pf" 2>/dev/null || true' EXIT
fi
wait_for "apiserver port-forward" 120 curl -sf "${api_auth[@]}" "http://127.0.0.1:$api_port/api/v1/agents?namespace=$starter_ns" -o /dev/null
api() { curl -sf "${api_auth[@]}" "$@"; }
CONVO_AGENT=starter KEY_SECRET=starter-model-key KEY_OWNER=Agent/starter
if [ "$MODE" = keyless ]; then
	log "A keyless Agent on the approved local route"
	api -X POST -H 'Content-Type: application/json' -d '{"profile":"celln-native-starter"}' "http://127.0.0.1:$api_port/api/v1/celln-platform/wrappers?namespace=$starter_ns" >/dev/null || fail "platform wrappers"
	runtime="$(kc -n "$starter_ns" get agentruntime -o json | python3 -c 'import json,sys; print(next(i["metadata"]["name"] for i in json.load(sys.stdin)["items"] if i["spec"].get("cellnProfileRef")))')"
	kc -n "$starter_ns" apply -f - >/dev/null <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: ModelConnection
metadata:
  name: qwen
spec:
  provider: llama-server
  protocol: openai-chat
  endpoint: $LLAMA_ORIGIN/v1/chat/completions
  allowInsecure: true
  models: ["$LLAMA_MODEL"]
---
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: qwen
spec:
  runtimeRef: $runtime
  agents:
    default:
      model: "$LLAMA_MODEL"
  execution:
    backend: celln
    modelConnectionRef: qwen
    model: "$LLAMA_MODEL"
    cellnSelection:
      runtimeRef: $runtime
      toolRefs: []
---
apiVersion: v1
kind: Secret
metadata:
  name: owner-a-key
  annotations:
    sympozium.ai/model-key-owner: Agent/owner-a
stringData:
  OPENAI_API_KEY: not-a-real-key
---
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: owner-a
spec:
  authRefs: [{provider: openai, secret: owner-a-key}]
  agents:
    default:
      model: gpt-test
EOF
	CONVO_AGENT=qwen KEY_SECRET=owner-a-key KEY_OWNER=Agent/owner-a
	pass "Agent qwen on runtime $runtime with a keyless connection to $LLAMA_ORIGIN"
fi
# The gateway reserves each model request's full output bound (2048 tokens by
# default) against the run's maxOutputTokens, so a conversation needs room
# for every turn's requests or its parent ends with its budget exhausted.
start_conversation() { # task [maxOutputTokens] [leaseSeconds] -> run name
	kc -n "$starter_ns" get agent "$CONVO_AGENT" -o json | python3 -c '
import json, sys
a = json.load(sys.stdin)["spec"]
e = a["execution"]
print(json.dumps({"agentRef": sys.argv[2], "task": sys.argv[1], "backend": "celln", "executionLifecycle": "enduring",
  "enduring": {"leaseSeconds": int(sys.argv[4]), "maxTurns": 8, "maxModelRequests": 24, "maxOutputTokens": int(sys.argv[3])},
  "model": e["model"], "modelConnectionRef": e["modelConnectionRef"],
  "cellnSelection": {"runtimeRef": e["cellnSelection"]["runtimeRef"], "toolRefs": []}}))' "$1" "$CONVO_AGENT" "${2:-24576}" "${3:-1800}" |
		api -X POST -H 'Content-Type: application/json' --data-binary @- "http://127.0.0.1:$api_port/api/v1/runs?namespace=$starter_ns" |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["name"])'
}
first_answer() { kc -n "$starter_ns" get agentrun "$1" -o jsonpath='{.status.result}'; }
send_turn() { # run turn-name message -> waits for the turn to finish
	local run=$1 name=$2 message=$3 uid
	uid="$(kc -n "$starter_ns" get agentrun "$run" -o jsonpath='{.metadata.uid}')"
	kc -n "$starter_ns" create -f - >/dev/null <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRunTurn
metadata:
  name: $name
  ownerReferences:
    - {apiVersion: sympozium.ai/v1alpha1, kind: AgentRun, name: $run, uid: "$uid", controller: true, blockOwnerDeletion: true}
spec:
  runName: $run
  runUID: "$uid"
  message: "$message"
EOF
}
turn_json() { kc -n "$starter_ns" get agentrunturn "$1" -o json; }

log "Two mediated conversations of the starter Agent, through the router"
run_a="$(start_conversation "Remember the word cobalt. Reply with only READY.")" || fail "API refused conversation A"
run_b="$(start_conversation "Remember the word saffron. Reply with only READY.")" || fail "API refused conversation B"
for run in "$run_a" "$run_b"; do
	wait_for "first answer of $run" 420 bash -c "[ -n \"\$(kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $run -o jsonpath='{.status.result}')\" ] || kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $run -o jsonpath='{.status.phase}' | grep -q Failed"
	[ "$(kc -n "$starter_ns" get agentrun "$run" -o jsonpath='{.status.phase}')" != Failed ] || fail "$run failed: $(kc -n "$starter_ns" get agentrun "$run" -o jsonpath='{.status.error}')"
done
pass "both conversations answered through the gateway: A '$(first_answer "$run_a" | head -c 60)', B '$(first_answer "$run_b" | head -c 60)'"

log "The conversations landed on different nodes"
node_of_run() { # run -> node holding its parent: the console's cells view, else dispatcher logs
	local incarnation pod node
	incarnation="$(kc -n "$starter_ns" get agentrun "$1" -o jsonpath='{.status.cellnScoped.parentIncarnation}')"
	node="$(api "http://127.0.0.1:$api_port/api/v1/celln-platform/cells" | python3 -c '
import json, sys
run, incarnation = sys.argv[1], sys.argv[2]
for n in json.load(sys.stdin):
    text = json.dumps({"cells": n.get("cells"), "parents": n.get("parents")})
    if f"\"{run}\"" in text or (incarnation and incarnation in text):
        print(n["node"]); break' "$1" "$incarnation" 2>/dev/null || true)"
	if [ -n "$node" ]; then
		echo "$node"
		return 0
	fi
	[ -n "$incarnation" ] || return 1
	for pod in $(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node -o name); do
		if kc -n celln-system logs "$pod" -c dispatcher --tail=-1 2>/dev/null | grep -q "${incarnation#blake3:}"; then
			kc -n celln-system get "$pod" -o jsonpath='{.spec.nodeName}'
			return 0
		fi
	done
	return 1
}
node_a="$(node_of_run "$run_a" || true)"
node_b="$(node_of_run "$run_b" || true)"
[ -n "$node_a" ] && [ -n "$node_b" ] || fail "could not locate the conversations' nodes (A='$node_a' B='$node_b'); dispatcher logs do not name the parents"
[ "$node_a" != "$node_b" ] || fail "both conversations landed on $node_a; capacity placement should spread them"
pass "conversation A on $node_a, B on $node_b"

log "Each conversation keeps its own context across turns"
send_turn "$run_a" "$run_a-recall" "Which word did I ask you to remember? Reply with only the word."
send_turn "$run_b" "$run_b-recall" "Which word did I ask you to remember? Reply with only the word."
for pair in "$run_a-recall:cobalt" "$run_b-recall:saffron"; do
	turn="${pair%%:*}" word="${pair##*:}"
	wait_for "turn $turn" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $turn -o json | grep -qiE '\"phase\": \"(Succeeded|Failed|Completed)\"|\"status\": \"True\"'"
	turn_json "$turn" | grep -qi "$word" || fail "$turn did not recall $word: $(turn_json "$turn" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin).get("status",{}))[:400])')"
done
pass "A recalled cobalt and B recalled saffron, each from its own node"

log "Another Agent cannot use $KEY_OWNER's key"
kc -n "$starter_ns" apply -f - >/dev/null <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: intruder
spec:
  authRefs: [{secret: $KEY_SECRET}]
  agents:
    default:
      model: deepseek-chat
EOF
intruder_err="$(kc -n "$starter_ns" create -f - 2>&1 <<EOF || true
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: intruder-run
spec:
  agentRef: intruder
  agentId: intruder
  sessionKey: intruder
  task: "say hi"
  cleanup: delete
  model:
    provider: deepseek
    model: deepseek-chat
    authSecretRef: $KEY_SECRET
EOF
)"
if echo "$intruder_err" | grep -q "belongs to $KEY_OWNER"; then
	pass "admission refused the intruder: ${intruder_err:0:160}"
else
	wait_for "intruder run to fail" 120 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrun intruder-run -o jsonpath='{.status.phase}' | grep -q Failed"
	kc -n "$starter_ns" get agentrun intruder-run -o jsonpath='{.status.error}' | grep -q "belongs to $KEY_OWNER" || fail "intruder run was not refused for the key: $(kc -n "$starter_ns" get agentrun intruder-run -o jsonpath='{.status.error}')"
	kc -n "$starter_ns" get jobs -l sympozium.ai/agent-run=intruder-run -o name | grep -q . && fail "a pod was started with another Agent's key"
	pass "the controller refused the intruder before starting a pod"
fi

log "Skills cannot reach Secrets"
kc get validatingadmissionpolicy sympozium-skill-secret-references >/dev/null || fail "skill Secret admission policy missing"
pass "skill Secret admission policy installed"

log "P3: a second namespace runs its own Agent independently"
tenant_b=tenant-b
kc create namespace "$tenant_b" >/dev/null 2>&1 || true
if [ "$MODE" = keyless ]; then
	api -X POST -H 'Content-Type: application/json' -d '{"profile":"celln-native-starter"}' "http://127.0.0.1:$api_port/api/v1/celln-platform/wrappers?namespace=$tenant_b" >/dev/null || fail "platform wrappers in $tenant_b"
	runtime_b="$(kc -n "$tenant_b" get agentruntime -o json | python3 -c 'import json,sys; print(next(i["metadata"]["name"] for i in json.load(sys.stdin)["items"] if i["spec"].get("cellnProfileRef")))')"
	kc -n "$starter_ns" get modelconnection qwen -o json | python3 -c 'import json,sys; o=json.load(sys.stdin); print(json.dumps({"apiVersion":o["apiVersion"],"kind":o["kind"],"metadata":{"name":"qwen"},"spec":o["spec"]}))' | kc -n "$tenant_b" apply -f - >/dev/null
	kc -n "$starter_ns" get agent qwen -o json | python3 -c '
import json,sys
o=json.load(sys.stdin); spec=o["spec"]; r=sys.argv[1]
spec["runtimeRef"]=r; spec["execution"]["cellnSelection"]["runtimeRef"]=r
print(json.dumps({"apiVersion":o["apiVersion"],"kind":o["kind"],"metadata":{"name":"qwen"},"spec":spec}))' "$runtime_b" | kc -n "$tenant_b" apply -f - >/dev/null
	saved_ns="$starter_ns"; starter_ns="$tenant_b"
	run_t="$(start_conversation "Remember the word indigo. Reply with only READY.")" || fail "API refused a conversation in $tenant_b"
	wait_for "first answer in $tenant_b" 420 bash -c "[ -n \"\$(kubectl --context kind-$CLUSTER -n $tenant_b get agentrun $run_t -o jsonpath='{.status.result}')\" ]"
	send_turn "$run_t" "$run_t-recall" "Which word did I ask you to remember? Reply with only the word."
	wait_for "turn $run_t-recall" 300 bash -c "kubectl --context kind-$CLUSTER -n $tenant_b get agentrunturn $run_t-recall -o json | grep -qiE '\"reason\": \"(Committed|ParentEnded)\"'"
	turn_json "$run_t-recall" | grep -qi indigo || fail "$tenant_b conversation did not recall indigo"
	starter_ns="$saved_ns"
	pass "$tenant_b ran its own conversation (recalled indigo) beside $starter_ns"
fi

log "P3: a controller restart mid-turn completes the turn once"
send_turn "$run_b" "$run_b-restart" "Which word did I ask you to remember? Reply with only the word."
kc -n sympozium-system rollout restart deploy/sympozium-controller-manager >/dev/null
kc -n sympozium-system rollout status deploy/sympozium-controller-manager --timeout=180s >/dev/null || fail "controller restart"
wait_for "turn $run_b-restart after a controller restart" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $run_b-restart -o json | grep -qiE '\"reason\": \"Committed\"'"
turn_json "$run_b-restart" | grep -qi saffron || fail "the turn across a controller restart did not recall saffron"
cells="$(api "http://127.0.0.1:$api_port/api/v1/celln-platform/cells" | python3 -c '
import json, sys
turn = sys.argv[1]
print(sum(1 for n in json.load(sys.stdin) for c in (n.get("cells") or []) if c.get("turn") == turn))' "$(kc -n "$starter_ns" get agentrunturn "$run_b-restart" -o jsonpath='{.metadata.uid}')" 2>/dev/null || echo "?")"
case "$cells" in 0|1|"?") ;; *) fail "the turn ran in $cells cells across the controller restart (duplicate work)";; esac
pass "the turn across a controller restart committed once (cells attributed: $cells)"

log "P3: a cancelled turn ends without an answer and the conversation goes on"
send_turn "$run_b" "$run_b-cancel" "Write a detailed 400 word essay about the history of sailing ships."
wait_for "turn $run_b-cancel to be dispatched" 120 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $run_b-cancel -o jsonpath='{.status.cellnScoped.startAttempted}' | grep -q true"
kc -n "$starter_ns" patch agentrunturn "$run_b-cancel" --type merge -p '{"spec":{"cancelRequested":true}}' >/dev/null
wait_for "turn $run_b-cancel to end" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $run_b-cancel -o jsonpath='{.status.conditions[?(@.type==\"CellnTurnComplete\")].status}' | grep -q True"
send_turn "$run_b" "$run_b-after-cancel" "Which word did I ask you to remember? Reply with only the word."
wait_for "turn $run_b-after-cancel" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $run_b-after-cancel -o json | grep -qiE '\"reason\": \"Committed\"'"
turn_json "$run_b-after-cancel" | grep -qi saffron || fail "the conversation did not go on after a cancelled turn"
pass "the cancelled turn ended ($(kc -n "$starter_ns" get agentrunturn "$run_b-cancel" -o jsonpath='{.status.conditions[?(@.type=="CellnTurnComplete")].reason}')) and B still recalled saffron"

log "P3: a conversation past its lease ends"
leased="$(start_conversation "Reply with only OK." 24576 60)" || fail "API refused the short-lease conversation"
wait_for "first answer of $leased" 300 bash -c "[ -n \"\$(kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $leased -o jsonpath='{.status.result}')\" ]"
sleep 75
send_turn "$leased" "$leased-late" "Reply with only OK."
wait_for "the expired conversation to end" 420 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $leased -o jsonpath='{.status.phase}' | grep -qE 'Failed|Succeeded' || kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $leased-late -o jsonpath='{.status.conditions[?(@.type==\"CellnTurnComplete\")].status}' | grep -q True"
if turn_json "$leased-late" | grep -qi '"answer": "OK'; then fail "a turn after the lease expired was answered"; fi
pass "the conversation past its lease ended: run $(kc -n "$starter_ns" get agentrun "$leased" -o jsonpath='{.status.phase} {.status.error}' | head -c 160)"

log "Losing conversation A's node ends A; B carries on"
pod_a="$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node --field-selector "spec.nodeName=$node_a" -o name)"
kc -n celln-system delete "$pod_a" --wait=true >/dev/null
send_turn "$run_a" "$run_a-after-loss" "Are you still there? Reply with only YES."
wait_for "conversation A to end after its node was lost" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $run_a -o jsonpath='{.status.phase}' | grep -q Failed"
kc -n "$starter_ns" get agentrun "$run_a" -o jsonpath='{.status.error}' | grep -q 'AUTH_CONTEXT_LOST' || fail "A did not end with AUTH_CONTEXT_LOST: $(kc -n "$starter_ns" get agentrun "$run_a" -o jsonpath='{.status.error}')"
send_turn "$run_b" "$run_b-after-loss" "Which word did I ask you to remember? Reply with only the word."
wait_for "turn $run_b-after-loss" 300 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrunturn $run_b-after-loss -o json | grep -qiE '\"phase\": \"(Succeeded|Failed|Completed)\"|\"status\": \"True\"'"
turn_json "$run_b-after-loss" | grep -qi saffron || fail "B did not carry on after A's node was lost"
pass "A ended with AUTH_CONTEXT_LOST; B on $node_b still recalled saffron"

log "A conversation that exhausts its budget ends instead of looking alive"
small="$(start_conversation "Reply with only OK." 6144)" || fail "API refused the small-budget conversation"
wait_for "first answer of $small" 420 bash -c "[ -n \"\$(kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $small -o jsonpath='{.status.result}')\" ]"
send_turn "$small" "$small-2" "Reply with only OK."
send_turn "$small" "$small-3" "Reply with only OK."
wait_for "the exhausted conversation to end" 420 bash -c "kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $small -o jsonpath='{.status.phase}' | grep -q Failed"
kc -n "$starter_ns" get agentrun "$small" -o jsonpath='{.status.error}' | grep -q 'AUTH_BUDGET_EXHAUSTED' || fail "$small did not end with AUTH_BUDGET_EXHAUSTED: $(kc -n "$starter_ns" get agentrun "$small" -o jsonpath='{.status.error}')"
pass "$small ended with AUTH_BUDGET_EXHAUSTED once its parent's budget ran out"

log "Deleting the lost conversation does not hang on its finalizer"
kc -n "$starter_ns" delete agentrun "$run_a" --wait=false >/dev/null
wait_for "lost conversation A to be deleted" 180 bash -c "! kubectl --context kind-$CLUSTER -n $starter_ns get agentrun $run_a"
pass "A deleted; cleanup treated the lost node's state as gone"
pass "mediated multi-node integration complete (work dir: $WORK)"
