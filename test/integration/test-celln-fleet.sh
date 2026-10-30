#!/usr/bin/env bash
# The model-free Celln fleet on multi-node Kind. A bare `sympozium install`
# (a build that pins a starter package) must, with no model input at all:
#   - say what privilege the fleet takes and never ask for or mention a
#     provider key; deploy two owners, the router, the model gateway and its
#     trust;
#   - leave no model anywhere: no fleet credential Secret, no managed Agent or
#     ModelConnection, one mediation-only profile (plus its toolbox) per
#     scope and a policy with no routes that admits every public origin;
#   - let every namespace bring its own ModelConnection and Agent: a keyless
#     llama-server tenant and a tenant whose two Agents hold keys to an
#     in-cluster HTTPS canary provider that records which key every call
#     carried, so each Agent's key is seen to arrive and no other;
#   - hold one key to one Agent (a second Agent naming the first's Secret is
#     refused), pin a key Secret to its first origin (re-posting the
#     connection at another origin is 409), and refuse a re-owned key at the
#     gateway without disturbing the other Agent;
#   - refuse to move the fleet to another package without
#     --celln-fleet-replace-package, and keep live conversations on a rerun;
#   - end only the conversation whose node is lost (AUTH_CONTEXT_LOST) while
#     the one on the other node carries on;
#   - keep skills away from Secrets (admission policy present).
#
# Inputs: CELLN_BUNDLE (a Celln release bundle: bin/celln + share/celln),
# LLAMA_ORIGIN + LLAMA_MODEL (a keyless OpenAI-compatible server, e.g.
# llama-server, on a private address the gateway is told to approve). No
# provider key is read; any DEEPSEEK/OPENAI/ANTHROPIC key in the environment is
# dropped before the install so the journey proves it is not needed. Optional
# FLEET_CERT_MANAGER_MANIFEST, FLEET_CLUSTER, FLEET_WORK, FLEET_REGISTRY_NAME,
# FLEET_REGISTRY_PORT (default 5001), KIND_EXPERIMENTAL_DOCKER_NETWORK (default kind),
# KEEP_CLUSTER=1. Images: controller, apiserver, webhook, node-probe,
# model-gateway and celln-installer built with TAG (make docker-build-<name>
# TAG=...), and the Celln image tagged CELLN_IMAGE.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
CLUSTER="${FLEET_CLUSTER:-fleet}"
TAG="${SYMPOZIUM_IMAGE_TAG:-fleet-trial}"
CELLN_IMAGE="${CELLN_IMAGE:-celln:$TAG}"
REGISTRY_NAME="${FLEET_REGISTRY_NAME:-kind-registry}"
# The registry's host port and the Docker network the Kind nodes share (KIND_EXPERIMENTAL_DOCKER_NETWORK).
REGISTRY_PORT="${FLEET_REGISTRY_PORT:-5001}"
KIND_NETWORK="${KIND_EXPERIMENTAL_DOCKER_NETWORK:-kind}"
WORK="${FLEET_WORK:-$(mktemp -d /tmp/celln-fleet.XXXXXX)}"
SCOPE=starter
PROFILE="celln-native-$SCOPE"
POLICY="celln-fleet-$SCOPE"
: "${CELLN_BUNDLE:?a Celln bundle directory (bin/celln, share/celln) is required}"
: "${LLAMA_ORIGIN:?LLAMA_ORIGIN (scheme://host[:port] of a keyless OpenAI-compatible server) is required}"
: "${LLAMA_MODEL:?LLAMA_MODEL names the model the server serves}"
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
api_pf=""
cleanup() {
	[ -z "$api_pf" ] || kill "$api_pf" 2>/dev/null || true
	[ "${KEEP_CLUSTER:-}" = 1 ] || kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "Cluster (one control plane, two KVM workers) and a local registry the nodes pull from"
if ! docker inspect "$REGISTRY_NAME" >/dev/null 2>&1; then
	docker run -d --restart=always -p "127.0.0.1:$REGISTRY_PORT:5000" --name "$REGISTRY_NAME" registry:2 >/dev/null
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
docker network connect "$KIND_NETWORK" "$REGISTRY_NAME" 2>/dev/null || true
registry="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$KIND_NETWORK\").IPAddress}}" "$REGISTRY_NAME"):5000"
for node in $(kind get nodes --name "$CLUSTER"); do
	docker exec "$node" mkdir -p "/etc/containerd/certs.d/localhost:$REGISTRY_PORT"
	printf '[host."http://%s:5000"]\n' "$REGISTRY_NAME" | docker exec -i "$node" tee "/etc/containerd/certs.d/localhost:$REGISTRY_PORT/hosts.toml" >/dev/null
done
# Kind nodes carry no kernel; the probe and the dispatcher both need one.
kernel="/boot/vmlinuz-$(uname -r)"
[ -r "$kernel" ] || fail "readable host kernel required at $kernel"
for node in $(kind get nodes --name "$CLUSTER" | grep worker); do
	docker exec "$node" test -f "/boot/$(basename "$kernel")" || docker cp "$kernel" "$node:/boot/"
done
# Deliberately no celln.dev/kvm label: the node probe must add it.
pass "cluster $CLUSTER with registry $registry; no node labelled by hand"

log "Starter package and a CLI that pins it and the model gateway, as a release build would"
"$REPO/hack/build-celln-starter.sh" --bundle "$CELLN_BUNDLE" --kernel "$kernel" \
	--image "localhost:$REGISTRY_PORT/celln/starter:$CLUSTER" --out "$WORK/starter" --push >"$WORK/starter-build.log" 2>&1 || { tail -5 "$WORK/starter-build.log"; fail "starter package"; }
digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"].split("@")[1])' "$WORK/starter/starter.json")"
package_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["packageHash"])' "$WORK/starter/starter.json")"
publisher="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["publisher"])' "$WORK/starter/starter.json")"
image="$registry/celln/starter@$digest"
docker tag "ghcr.io/sympozium-ai/sympozium/model-gateway:$TAG" "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG"
docker push -q "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG" >/dev/null
gateway="localhost:$REGISTRY_PORT/sympozium/model-gateway@$(docker buildx imagetools inspect "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')"
(cd "$REPO" && go build -ldflags "-X main.cellnStarterImage=$image -X main.cellnStarterHash=$package_hash -X main.cellnStarterPublisher=$publisher -X main.cellnStarterCellnVersion=journey -X main.modelGatewayImage=$gateway" -o "$WORK/sympozium" ./cmd/sympozium)
pass "sympozium built with package $package_hash by $publisher and gateway $gateway"

log "Images"
for img in "$CELLN_IMAGE" "ghcr.io/sympozium-ai/sympozium/controller:$TAG" "ghcr.io/sympozium-ai/sympozium/apiserver:$TAG" "ghcr.io/sympozium-ai/sympozium/webhook:$TAG" "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" "ghcr.io/sympozium-ai/sympozium/node-probe:$TAG"; do
	kind load docker-image --name "$CLUSTER" "$img" >/dev/null
done
pass "images loaded"

log "Key canary: an in-cluster HTTPS provider that records which key each call carries"
# The gateway sends a key only over HTTPS to an approved origin, so the canary
# has its own CA, which the gateway is told to trust (modelGateway.providerCA).
CANARY_NS=canary-provider CANARY_ORIGIN="https://canary.canary-provider.svc"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=journey canary CA" \
	-keyout "$WORK/canary-ca.key" -out "$WORK/canary-ca.crt" >/dev/null 2>&1 || fail "canary CA"
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=canary.$CANARY_NS.svc" \
	-keyout "$WORK/canary.key" -out "$WORK/canary.csr" >/dev/null 2>&1 || fail "canary key"
printf 'subjectAltName=DNS:canary.%s.svc,DNS:canary.%s.svc.cluster.local\nextendedKeyUsage=serverAuth\n' "$CANARY_NS" "$CANARY_NS" >"$WORK/canary.ext"
openssl x509 -req -in "$WORK/canary.csr" -CA "$WORK/canary-ca.crt" -CAkey "$WORK/canary-ca.key" -CAcreateserial -days 2 \
	-extfile "$WORK/canary.ext" -out "$WORK/canary.crt" >/dev/null 2>&1 || fail "canary certificate"
kc create namespace "$CANARY_NS" >/dev/null 2>&1 || true
kc -n "$CANARY_NS" create secret tls canary-tls --cert "$WORK/canary.crt" --key "$WORK/canary.key" --dry-run=client -o yaml | kc apply -f - >/dev/null
kc create namespace sympozium-system >/dev/null 2>&1 || true
kc -n sympozium-system create configmap canary-provider-ca --from-file=ca.crt="$WORK/canary-ca.crt" --dry-run=client -o yaml | kc apply -f - >/dev/null
kind get kubeconfig --name "$CLUSTER" >"$WORK/kubeconfig"
KUBECONFIG="$WORK/kubeconfig" TEST_NAMESPACE="$CANARY_NS" KIND_CLUSTER_NAME="$CLUSTER" FAKE_MODEL_NAME=canary FAKE_MODEL_TLS_SECRET=canary-tls \
	"$REPO/test/integration/deploy-fake-model.sh" >"$WORK/canary-deploy.log" 2>&1 || { tail -20 "$WORK/canary-deploy.log"; fail "canary provider"; }
pass "canary provider serving $CANARY_ORIGIN"

log "sympozium install: no model input at all"
if [ -n "${FLEET_CERT_MANAGER_MANIFEST:-}" ]; then
	kc apply -f "$FLEET_CERT_MANAGER_MANIFEST" >/dev/null
	kc -n cert-manager wait --for=condition=Available deploy --all --timeout=180s >/dev/null
fi
# Only image tags for the sideloaded builds, the private test registry and the
# two private provider origins the gateway may reach; nothing about a model,
# a backend, a route or a key. Any provider key in the environment is dropped.
sympozium_install() { # extra args... -> the same command, rerun to prove the package gate
	KUBECONFIG="$WORK/kubeconfig" env -u DEEPSEEK_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u SYMPOZIUM_CELLN_BACKEND \
		"$WORK/sympozium" install "$@" \
		--set "modelGateway.privateOrigins[0]=$LLAMA_ORIGIN" --set "modelGateway.privateOrigins[1]=$CANARY_ORIGIN" \
		--set modelGateway.providerCA.configMap=canary-provider-ca \
		--celln-fleet-output-dir "$WORK/fleet-out" --celln-fleet-wait 20m \
		--celln-router-image "$CELLN_IMAGE" \
		--celln-installer-image "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" \
		--set celln.fleet.package.insecureRegistry=true \
		--set "controller.image.tag=$TAG" --set "apiserver.image.tag=$TAG" --set "webhook.image.tag=$TAG" --set "nodeProbe.image.tag=$TAG" \
		</dev/null
}
sympozium_install >"$WORK/install.log" 2>&1 || { tail -40 "$WORK/install.log"; fail "sympozium install"; }
grep -q 'Using the default Celln starter package' "$WORK/install.log" || fail "the install did not pick the pinned package: $(head -20 "$WORK/install.log")"
# The privilege disclosure (fleetPrivilegeNotice) is printed before anything is deployed.
grep -q 'two privileged DaemonSets' "$WORK/install.log" && grep -q 'Nodes hold no model key' "$WORK/install.log" || fail "the install did not disclose the fleet's privilege: $(head -20 "$WORK/install.log")"
grep -q 'Model gateway:' "$WORK/install.log" || fail "the install did not report the model gateway"
# No provider key was asked for, read or mentioned.
if grep -Eiq '_API_KEY|provider key|credential-file|celln-fleet-model|SYMPOZIUM_CELLN_BACKEND|starter Agent|model-credentials' "$WORK/install.log"; then
	fail "the install mentioned a provider key or a model input: $(grep -Ei '_API_KEY|provider key|credential-file|celln-fleet-model|SYMPOZIUM_CELLN_BACKEND|starter Agent|model-credentials' "$WORK/install.log" | head -3)"
fi
pass "installed with the pinned package, the privilege disclosed and no model asked (log: $WORK/install.log)"

log "The gateway, its database and the trust came up without any flags; the nodes joined by themselves"
kc -n sympozium-system rollout status deploy/sympozium-model-gateway --timeout=300s >/dev/null || fail "model gateway not ready"
kc -n sympozium-system rollout status statefulset/sympozium-model-gateway-db --timeout=300s >/dev/null || fail "bundled database not ready"
for obj in secret/celln-mediation-controller secret/celln-mediation-gateway configmap/celln-mediation-trust; do
	kc -n sympozium-system get "$obj" >/dev/null || fail "$obj missing in sympozium-system"
done
for obj in secret/celln-mediation-node configmap/celln-mediation-trust configmap/celln-mediation; do
	kc -n celln-system get "$obj" >/dev/null || fail "$obj missing in celln-system"
done
[ "$(kc get nodes -l celln.dev/kvm=true -o name | wc -l)" -eq 2 ] || fail "expected both workers labelled by the probe"
owners="$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node --field-selector status.phase=Running -o name | wc -l)"
[ "$owners" -eq 2 ] || fail "expected 2 running owners, got $owners"
kc -n sympozium-system get deploy sympozium-controller-manager -o jsonpath='{.spec.template.spec.nodeSelector}' | grep -q hostname && fail "controller pinned to a node"
pass "gateway, bundled PostgreSQL and mediation trust in place; two owners prepared from one package; controller unpinned"

log "The fleet holds no model: no credential Secret, no managed Agent or connection, one mediation-only profile, a policy without routes"
[ -z "$(kc get secrets -A --field-selector metadata.name=celln-fleet-model-credentials -o name)" ] || fail "a fleet model credential Secret exists"
managed="$(kc get agents,modelconnections -A -o json | python3 -c '
import json, sys
for i in json.load(sys.stdin)["items"]:
    m = i["metadata"]; labels = m.get("labels") or {}
    if labels.get("sympozium.ai/managed-by") == "celln-platform" or m["name"].startswith("celln-") or m["name"] == "starter":
        print(i["kind"] + " " + m["namespace"] + "/" + m["name"])')"
[ -z "$managed" ] || fail "the install created a model object: $(echo "$managed" | tr '\n' ' ')"
[ "$(kc get cellnruntimeprofile "$PROFILE" -o jsonpath='{.metadata.labels.celln\.sympozium\.ai/mediation-only}')" = true ] || fail "profile $PROFILE is not labelled celln.sympozium.ai/mediation-only=true"
kc get cellnruntimeprofile "$PROFILE.toolbox" >/dev/null || fail "toolbox profile $PROFILE.toolbox missing"
[ "$(kc get cellnruntimeprofile -o name | wc -l)" -eq 2 ] || fail "expected exactly the profile and its toolbox, got: $(kc get cellnruntimeprofile -o name | tr '\n' ' ')"
[ -z "$(kc get cellnexecutionpolicy "$POLICY" -o jsonpath='{.spec.allowedOrigins}{.spec.deniedOrigins}')" ] || fail "policy $POLICY narrows origins out of the box: $(kc get cellnexecutionpolicy "$POLICY" -o jsonpath='{.spec.allowedOrigins} {.spec.deniedOrigins}')"
tools="$(kc get clustercellntool -o name | wc -l)"
[ "$tools" -ge 8 ] || fail "expected at least the eight brokered tools, got $tools"
pass "no fleet credential; no Agent or ModelConnection anywhere; $PROFILE (+ .toolbox) mediation-only; $POLICY has no origin guardrail; $tools tools"

log "API access"
api_token="$(kc -n sympozium-system get secret sympozium-ui-token -o jsonpath='{.data.token}' | base64 -d)"
api_auth=(-H "Authorization: Bearer $api_token")
kc -n sympozium-system rollout status deploy/sympozium-apiserver --timeout=300s >/dev/null || fail "apiserver rollout"
api_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
kubectl --context "kind-$CLUSTER" -n sympozium-system port-forward svc/sympozium-apiserver "$api_port:8080" >/dev/null 2>&1 &
api_pf=$!
API="http://127.0.0.1:$api_port"
tenant_local=fleet-local tenant_canary=fleet-canary denied=fleet-denied
for ns in "$tenant_local" "$tenant_canary" "$denied"; do kc create namespace "$ns" >/dev/null 2>&1 || true; done
kc label namespace "$denied" --overwrite celln.sympozium.ai/excluded=true >/dev/null
wait_for "apiserver port-forward" 120 curl -sf "${api_auth[@]}" "$API/api/v1/celln-platform/profiles?namespace=$tenant_local" -o /dev/null
api() { curl -sf "${api_auth[@]}" "$@"; }
api_json() { api -H 'Content-Type: application/json' "$@"; }
api_code() { curl -s -o "$WORK/api-last-body" -w '%{http_code}' "${api_auth[@]}" -H 'Content-Type: application/json' "$@"; }

log "A fresh namespace is offered the mediation-only profile; the excluded one is refused; the policy routes nothing"
profiles="$(api "$API/api/v1/celln-platform/profiles?namespace=$tenant_local")"
echo "$profiles" | python3 -c '
import json, sys
ps = json.load(sys.stdin)
p = next((p for p in ps if p["name"] == sys.argv[1]), None)
assert p, f"profile {sys.argv[1]} not offered: {ps}"
assert p.get("mediationOnly") is True, "profile is not mediation-only"
assert not p.get("provider") and not p.get("model") and not p.get("endpoint"), "profile still carries a fleet model"
assert not p.get("agent"), "profile still names a shared Agent"
assert p["wrapper"] == "celln-native", p["wrapper"]
assert p.get("toolboxProfile") == sys.argv[1] + ".toolbox", p.get("toolboxProfile")
assert p.get("toolboxWrapper") == "celln-native.toolbox", p.get("toolboxWrapper")
assert p["policy"] == sys.argv[2], p["policy"]' "$PROFILE" "$POLICY" || fail "profile listing for $tenant_local"
api "$API/api/v1/celln-platform/mediation?namespace=$tenant_local" | python3 -c '
import json, sys
m = json.load(sys.stdin)
assert m["enabled"] is True, m
assert m["allowedOrigins"] == [] and m["deniedOrigins"] == [], f"origin guardrail out of the box: {m}"
assert m["routes"] == [] and m["pending"] == [], f"routes or pending left: {m}"
assert sys.argv[1] in m["policies"], m["policies"]
assert sys.argv[2] in m["privateOrigins"] and sys.argv[3] in m["privateOrigins"], m["privateOrigins"]
print("mediation: enabled, no origin guardrail, no routes, private origins", m["privateOrigins"])' "$POLICY" "$LLAMA_ORIGIN" "$CANARY_ORIGIN" || fail "mediation report for $tenant_local"
[ "$(api "$API/api/v1/celln-platform/profiles?namespace=$denied")" = "[]" ] || fail "excluded namespace $denied was offered a profile"
[ "$(api_code -X POST -d "{\"profile\":\"$PROFILE\",\"runtimeOnly\":true}" "$API/api/v1/celln-platform/wrappers?namespace=$denied")" = 403 ] || fail "excluded namespace $denied was given a wrapper"
pass "$tenant_local is offered $PROFILE (mediation-only, no provider, no shared Agent); $denied gets nothing; mediation lists no routes"

# toolbox_selection namespace -> "wrapper<TAB>tool-refs JSON": the toolbox
# wrapper (an AgentRuntime, created runtime-only) and exactly the tools it
# lends. Nothing else is created in the namespace: the Agent's own connection
# is its model route.
toolbox_selection() {
	local ns=$1 profiles toolbox_profile
	profiles="$(api "$API/api/v1/celln-platform/profiles?namespace=$ns")"
	toolbox_profile="$(echo "$profiles" | python3 -c 'import json,sys; print(next(p["toolboxProfile"] for p in json.load(sys.stdin) if p.get("toolboxProfile")))')" || fail "no toolbox profile is offered in $ns: $profiles"
	api_json -X POST -d "{\"profile\":\"$toolbox_profile\",\"runtimeOnly\":true}" "$API/api/v1/celln-platform/wrappers?namespace=$ns" | python3 -c '
import json, sys
w = json.load(sys.stdin)
assert w["runtime"] == sys.argv[1], w
assert not w.get("agent") and not w.get("connection"), f"wrapper creation made a shared Agent or connection: {w}"' "celln-native.toolbox" || fail "toolbox wrapper in $ns"
	[ -z "$(kc -n "$ns" get agents,modelconnections -o name)" ] || fail "wrapper creation left model objects in $ns: $(kc -n "$ns" get agents,modelconnections -o name | tr '\n' ' ')"
	echo "$profiles" | python3 -c 'import json,sys; p=next(p for p in json.load(sys.stdin) if p.get("toolboxProfile")); print(p["toolboxWrapper"]+"\t"+json.dumps(p["toolboxTools"]))'
}
# create_agent namespace name connection model -> an Agent on the toolbox
# wrapper whose model route is its own connection (POST /api/v1/agents).
create_agent() {
	local ns=$1 name=$2 connection=$3 model=$4 provider=$5 wrapper tool_refs
	IFS=$'\t' read -r wrapper tool_refs < <(toolbox_selection "$ns")
	python3 -c '
import json, sys
ns, name, connection, model, provider, wrapper, tools = sys.argv[1:8]
print(json.dumps({"name": name, "provider": provider, "model": model, "runtimeRef": wrapper,
  "execution": {"backend": "celln", "executionLifecycle": "enduring", "modelConnectionRef": connection, "model": model,
    # The persona belongs to the Agent; every run below must carry it into its cell.
    "systemPrompt": "You are the fleet journey agent " + name + ". Answer with only what is asked.",
    "cellnSelection": {"runtimeRef": wrapper, "toolRefs": [], "clusterToolRefs": json.loads(tools)},
    "enduring": {"leaseSeconds": 1800, "maxTurns": 12, "maxModelRequests": 64, "maxOutputTokens": 98304}}}))' \
		"$ns" "$name" "$connection" "$model" "$provider" "$wrapper" "$tool_refs" |
		api_json -X POST --data-binary @- "$API/api/v1/agents?namespace=$ns" >/dev/null || fail "API refused Agent $name in $ns"
}
# The gateway reserves each model request's full output bound against the
# run's maxOutputTokens, and a turn with tools may make several requests, so a
# conversation needs room for every turn's requests or its parent ends with
# its budget exhausted.
start_conversation() { # namespace agent task [maxOutputTokens] [leaseSeconds] -> run name
	kc -n "$1" get agent "$2" -o json | python3 -c '
import json, sys
e = json.load(sys.stdin)["spec"]["execution"]
print(json.dumps({"agentRef": sys.argv[2], "task": sys.argv[1], "backend": "celln", "executionLifecycle": "enduring",
  "enduring": {"leaseSeconds": int(sys.argv[4]), "maxTurns": 12, "maxModelRequests": 64, "maxOutputTokens": int(sys.argv[3])},
  "model": e["model"], "modelConnectionRef": e["modelConnectionRef"],
  "cellnSelection": {"runtimeRef": e["cellnSelection"]["runtimeRef"], "clusterToolRefs": e["cellnSelection"].get("clusterToolRefs", []), "toolRefs": []}}))' "$3" "$2" "${4:-98304}" "${5:-1800}" |
		api_json -X POST --data-binary @- "$API/api/v1/runs?namespace=$1" |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["name"])'
}
first_answer() { kc -n "$1" get agentrun "$2" -o jsonpath='{.status.result}'; }
wait_first_answer() { # namespace run
	wait_for "first answer of $2" 420 bash -c "[ -n \"\$(kubectl --context kind-$CLUSTER -n $1 get agentrun $2 -o jsonpath='{.status.result}')\" ] || kubectl --context kind-$CLUSTER -n $1 get agentrun $2 -o jsonpath='{.status.phase}' | grep -q Failed"
	[ "$(kc -n "$1" get agentrun "$2" -o jsonpath='{.status.phase}')" != Failed ] || fail "$2 failed: $(kc -n "$1" get agentrun "$2" -o jsonpath='{.status.error}')"
}
send_turn() { # namespace run turn-name message
	local ns=$1 run=$2 name=$3 message=$4 uid
	uid="$(kc -n "$ns" get agentrun "$run" -o jsonpath='{.metadata.uid}')"
	kc -n "$ns" create -f - >/dev/null <<EOF
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
turn_json() { kc -n "$1" get agentrunturn "$2" -o json; }
wait_turn() { # namespace turn-name -> the turn committed (or its parent ended)
	wait_for "turn $2" 300 bash -c "kubectl --context kind-$CLUSTER -n $1 get agentrunturn $2 -o json | grep -qiE '\"reason\": \"(Committed|ParentEnded)\"'"
}
turn_recalls() { # namespace turn-name word -> the committed answer names the word
	turn_json "$1" "$2" | python3 -c 'import json,sys; t=json.load(sys.stdin); sys.exit(0 if sys.argv[1].lower() in json.dumps(t.get("status",{})).lower() else 1)' "$3"
}

log "Tenant $tenant_local: its own keyless connection to $LLAMA_ORIGIN, its own Agent, a conversation through the router"
python3 -c '
import json, sys
origin, model = sys.argv[1:3]
print(json.dumps({"name": "local", "spec": {"provider": "llama-server", "protocol": "openai-chat", "endpoint": origin + "/v1/chat/completions",
  "models": [model], "allowInsecure": True,
  # A local reasoning model at ~10 tokens/s must answer within a turn: no thinking, and a short output bound per request.
  "parameters": {"chat_template_kwargs": {"enable_thinking": False}}, "maxOutputTokens": 512}}))' "$LLAMA_ORIGIN" "$LLAMA_MODEL" |
	api_json -X POST --data-binary @- "$API/api/v1/model-connections?namespace=$tenant_local" >/dev/null || fail "API refused the keyless connection in $tenant_local"
[ -z "$(kc -n "$tenant_local" get modelconnection local -o jsonpath='{.spec.secretRef}')" ] || fail "the keyless connection got a Secret"
create_agent "$tenant_local" local-agent local "$LLAMA_MODEL" llama-server
run_local="$(start_conversation "$tenant_local" local-agent "Remember the word indigo. Reply with only READY.")" || fail "API refused a conversation in $tenant_local"
wait_first_answer "$tenant_local" "$run_local"
[ "$(kc -n "$tenant_local" get agentrun "$run_local" -o jsonpath='{.spec.systemPrompt}')" = "You are the fleet journey agent local-agent. Answer with only what is asked." ] || fail "$run_local does not carry its Agent persona"
[ "$(kc -n "$tenant_local" get agentrun "$run_local" -o jsonpath='{.status.cellnParent.binding.target}')" = "http://celln-router.celln-system.svc.cluster.local:8787" ] || fail "$run_local not issued through the router"
send_turn "$tenant_local" "$run_local" "$run_local-recall" "Which word did I ask you to remember? Reply with only the word."
wait_turn "$tenant_local" "$run_local-recall"
turn_recalls "$tenant_local" "$run_local-recall" indigo || fail "$tenant_local conversation did not recall indigo: $(turn_json "$tenant_local" "$run_local-recall" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin).get("status",{}))[:300])')"
pass "$run_local answered through the router on a keyless connection ('$(first_answer "$tenant_local" "$run_local" | head -c 60)') and recalled indigo"

log "Tenant $tenant_local: the same Agent as an OpenAI-compatible endpoint continues that conversation"
completion="$(api -H 'Content-Type: application/json' -H "X-Sympozium-Namespace: $tenant_local" -X POST "$API/api/v1/agents/local-agent/v1/chat/completions?timeout=5m" \
	-d '{"model":"local-agent","messages":[{"role":"system","content":"ignored: the Agent holds its own conversation"},{"role":"user","content":"Once more: which word did I ask you to remember? Reply with only the word."}]}')" || fail "chat completion against local-agent failed: $(cat "$WORK/api-last-body" 2>/dev/null | head -c 300)"
echo "$completion" | python3 -c '
import json, sys
c = json.load(sys.stdin)
assert c["object"] == "chat.completion" and c["model"] == "local-agent", c
assert c["choices"][0]["finish_reason"] == "stop", c
assert "indigo" in c["choices"][0]["message"]["content"].lower(), c["choices"][0]["message"]["content"]
assert c["sympozium"]["run"] == sys.argv[1] and c["sympozium"]["turn"] and not c["sympozium"]["started"], c["sympozium"]
' "$run_local" || fail "chat completion did not continue $run_local with indigo: $(echo "$completion" | head -c 400)"
[ "$(api "$API/api/v1/agents/local-agent/v1/models?namespace=$tenant_local" | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"][0]["id"])')" = local-agent ] || fail "the Agent is not listed as its endpoint's model"
pass "an OpenAI chat completion continued $run_local as a turn and recalled indigo"

log "Tenant $tenant_canary: two Agents, each with its own key to the canary, entered through the API and pinned to the canary's origin"
declare -A canary_fp=() canary_secret=()
for agent in canary-a canary-b; do
	key="sk-canary-$(openssl rand -hex 16)"
	canary_fp[$agent]="$(printf '%s' "$key" | sha256sum | cut -c1-16)"
	canary_secret[$agent]="$agent-canary-key" # <connection>-<provider>-key, written by the API
	python3 -c '
import json, sys
name, origin, key = sys.argv[1:4]
print(json.dumps({"name": name, "apiKey": key, "spec": {"provider": "canary", "protocol": "openai-chat", "endpoint": origin + "/v1/chat/completions", "models": ["canary-model"]}}))' "$agent" "$CANARY_ORIGIN" "$key" |
		api_json -X POST --data-binary @- "$API/api/v1/model-connections?namespace=$tenant_canary" >/dev/null || fail "API refused connection $agent"
	[ "$(kc -n "$tenant_canary" get modelconnection "$agent" -o jsonpath='{.spec.secretRef}')" = "${canary_secret[$agent]}" ] || fail "connection $agent does not name its Secret ${canary_secret[$agent]}"
	pinned="$(kc -n "$tenant_canary" get secret "${canary_secret[$agent]}" -o jsonpath='{.metadata.annotations.sympozium\.ai/model-key-origin}')"
	[ "$pinned" = "$CANARY_ORIGIN" ] || fail "${canary_secret[$agent]} is pinned to '$pinned', not $CANARY_ORIGIN"
	create_agent "$tenant_canary" "$agent" "$agent" canary-model canary
	[ "$(kc -n "$tenant_canary" get agent "$agent" -o jsonpath='{.spec.authRefs[0].secret}')" = "${canary_secret[$agent]}" ] || fail "Agent $agent does not grant its own Secret"
done
[ "${canary_fp[canary-a]}" != "${canary_fp[canary-b]}" ] || fail "canary keys collide"
pass "canary-a and canary-b each hold their own key Secret, pinned to $CANARY_ORIGIN on entry, and grant it in authRefs"

log "Key canary: the two Agents' concurrent conversations each reach the provider with their own key and no other"
canary_run_a="$(start_conversation "$tenant_canary" canary-a "CANARY:canary-a Remember this exact token for the next turn: cobalt-a1")" || fail "API refused canary-a"
canary_run_b="$(start_conversation "$tenant_canary" canary-b "CANARY:canary-b Remember this exact token for the next turn: saffron-b2")" || fail "API refused canary-b"
wait_first_answer "$tenant_canary" "$canary_run_a"
wait_first_answer "$tenant_canary" "$canary_run_b"
for agent in canary-a canary-b; do
	owner="$(kc -n "$tenant_canary" get secret "${canary_secret[$agent]}" -o jsonpath='{.metadata.annotations.sympozium\.ai/model-key-owner}')"
	[ "$owner" = "Agent/$agent" ] || fail "${canary_secret[$agent]} is owned by '$owner', not Agent/$agent"
done
send_turn "$tenant_canary" "$canary_run_a" "$canary_run_a-recall" "What exact token did I ask you to remember?"
send_turn "$tenant_canary" "$canary_run_b" "$canary_run_b-recall" "What exact token did I ask you to remember?"
for pair in "$canary_run_a-recall:cobalt-a1" "$canary_run_b-recall:saffron-b2"; do
	turn="${pair%%:*}" token="${pair##*:}"
	wait_turn "$tenant_canary" "$turn"
	turn_recalls "$tenant_canary" "$turn" "$token" || fail "$turn did not recall $token"
done
canary_records() { kubectl --context "kind-$CLUSTER" get --raw "/api/v1/namespaces/$CANARY_NS/services/https:canary:443/proxy/fixture/keys"; }
records="$(canary_records)" || fail "could not read the canary's key records"
echo "$records" | python3 -c '
import json, sys
records = json.load(sys.stdin)
want = {"canary-a": sys.argv[1], "canary-b": sys.argv[2]}
for agent, fp in want.items():
    mine = [r for r in records if r["tag"] == agent]
    wrong = [r for r in mine if r["key"] != fp]
    if len(mine) < 2:
        sys.exit(f"{agent}: only {len(mine)} provider call(s) recorded, want its first turn and its recall")
    if wrong:
        sys.exit(f"{agent}: {len(wrong)} of {len(mine)} calls carried a key other than its own: {wrong}")
    print(f"{agent}: {len(mine)} calls, every one with its own key {fp}")
keyless = [r for r in records if r["tag"] in want and not r["key"]]
if keyless:
    sys.exit(f"a call reached the provider without its Agent key: {keyless}")
untagged = [r for r in records if r["tag"] not in want and r["key"] in want.values()]
if untagged:
    sys.exit(f"an Agent key served an untagged conversation: {untagged}")' "${canary_fp[canary-a]}" "${canary_fp[canary-b]}" >"$WORK/canary-verdict" || fail "key canary: $(cat "$WORK/canary-verdict")"
pass "the provider saw only each Agent's own key: $(tr '\n' ';' <"$WORK/canary-verdict")"

log "Key canary: neither key exists outside its own Secret, and no pod spec carries one"
kc get secrets -A -o json | python3 -c '
import base64, hashlib, json, sys
fps = set(sys.argv[1:])
for i in json.load(sys.stdin)["items"]:
    for k, v in (i.get("data") or {}).items():
        raw = base64.b64decode(v).strip()
        if hashlib.sha256(raw).hexdigest()[:16] in fps:
            print(i["metadata"]["namespace"] + "/" + i["metadata"]["name"])' "${canary_fp[canary-a]}" "${canary_fp[canary-b]}" | sort >"$WORK/canary-holders"
[ "$(tr '\n' ' ' <"$WORK/canary-holders")" = "$tenant_canary/${canary_secret[canary-a]} $tenant_canary/${canary_secret[canary-b]} " ] || fail "a canary key is held elsewhere: $(cat "$WORK/canary-holders")"
for agent in canary-a canary-b; do
	key="$(kc -n "$tenant_canary" get secret "${canary_secret[$agent]}" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)"
	[ -n "$key" ] || fail "${canary_secret[$agent]} holds no OPENAI_API_KEY"
	if kc get pods -A -o json | grep -qF "$key"; then fail "$agent's key appears in a pod spec"; fi
done
pass "each key is only in its own Secret; no pod spec carries either"

log "Key canary: the cells view attributes each conversation's cells to its Agent"
cells_by_agent="$(api "$API/api/v1/celln-platform/cells" | python3 -c '
import json, sys
runs = {sys.argv[1]: "canary-a", sys.argv[2]: "canary-b"}
seen = {}
for n in json.load(sys.stdin):
    for c in n.get("cells") or []:
        r = c.get("run") or {}
        if r.get("name") in runs and r.get("agent") == runs[r["name"]]:
            seen[runs[r["name"]]] = seen.get(runs[r["name"]], 0) + 1
print(" ".join(f"{a}={seen.get(a, 0)}" for a in ("canary-a", "canary-b")))' "$canary_run_a" "$canary_run_b")"
case "$cells_by_agent" in *"=0"*) fail "cells not attributed to both conversations: $cells_by_agent" ;; esac
pass "cells attributed: $cells_by_agent"

log "One key, one Agent: a second Agent naming canary-a's Secret is refused"
code="$(api_code -X POST -d "{\"name\":\"intruder\",\"provider\":\"canary\",\"model\":\"canary-model\",\"secretName\":\"${canary_secret[canary-a]}\"}" "$API/api/v1/agents?namespace=$tenant_canary")"
[ "$code" = 409 ] && grep -q "belongs to Agent/canary-a" "$WORK/api-last-body" || fail "an Agent naming canary-a's Secret was not refused ($code): $(cat "$WORK/api-last-body")"
intruder_by_secret="$(cat "$WORK/api-last-body")"
code="$(api_code -X POST -d '{"name":"intruder","provider":"canary","model":"canary-model","execution":{"backend":"celln","modelConnectionRef":"canary-a","model":"canary-model"}}' "$API/api/v1/agents?namespace=$tenant_canary")"
[ "$code" = 409 ] && grep -q "belongs to Agent/canary-a" "$WORK/api-last-body" || fail "an Agent selecting canary-a's connection was not refused ($code): $(cat "$WORK/api-last-body")"
kc -n "$tenant_canary" get agent intruder >/dev/null 2>&1 && fail "the intruder Agent was created"
# The Kubernetes path says the same: an Agent may list the Secret, but no run of
# it gets the key (admission or the controller refuses before any pod or parent).
kc -n "$tenant_canary" apply -f - >/dev/null <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: intruder
spec:
  authRefs: [{provider: canary, secret: ${canary_secret[canary-a]}}]
  agents:
    default:
      model: canary-model
EOF
intruder_err="$(kc -n "$tenant_canary" create -f - 2>&1 <<EOF || true
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
    provider: canary
    model: canary-model
    authSecretRef: ${canary_secret[canary-a]}
EOF
)"
if echo "$intruder_err" | grep -q "belongs to Agent/canary-a"; then
	pass "the API refused the intruder twice (409: ${intruder_by_secret:0:120}) and admission refused its run"
else
	wait_for "intruder run to fail" 120 bash -c "kubectl --context kind-$CLUSTER -n $tenant_canary get agentrun intruder-run -o jsonpath='{.status.phase}' | grep -q Failed"
	kc -n "$tenant_canary" get agentrun intruder-run -o jsonpath='{.status.error}' | grep -q "belongs to Agent/canary-a" || fail "intruder run was not refused for the key: $(kc -n "$tenant_canary" get agentrun intruder-run -o jsonpath='{.status.error}')"
	kc -n "$tenant_canary" get jobs -l sympozium.ai/agent-run=intruder-run -o name | grep -q . && fail "a pod was started with another Agent's key"
	pass "the API refused the intruder twice (409: ${intruder_by_secret:0:120}) and the controller refused its run before starting anything"
fi
kc -n "$tenant_canary" delete agent intruder --ignore-not-found >/dev/null

log "A key is pinned to its first origin: re-posting canary-a's connection at another origin is refused"
code="$(api_code -X POST -d "{\"name\":\"canary-a\",\"spec\":{\"provider\":\"canary\",\"protocol\":\"openai-chat\",\"endpoint\":\"https://attacker.example/v1/chat/completions\",\"models\":[\"canary-model\"],\"secretRef\":\"${canary_secret[canary-a]}\"}}" "$API/api/v1/model-connections?namespace=$tenant_canary")"
[ "$code" = 409 ] && grep -q "is pinned to $CANARY_ORIGIN" "$WORK/api-last-body" || fail "moving canary-a's key to another origin was not refused ($code): $(cat "$WORK/api-last-body")"
[ "$(kc -n "$tenant_canary" get modelconnection canary-a -o jsonpath='{.spec.endpoint}')" = "$CANARY_ORIGIN/v1/chat/completions" ] || fail "the refused move changed the connection"
[ "$(kc -n "$tenant_canary" get secret "${canary_secret[canary-a]}" -o jsonpath='{.metadata.annotations.sympozium\.ai/model-key-origin}')" = "$CANARY_ORIGIN" ] || fail "the refused move re-pinned the key"
pass "409: $(head -c 160 "$WORK/api-last-body" | tr '\n' ' '); connection and pin unchanged"

log "Key canary: re-owning canary-a's key stops canary-a, names it in the refusal, and leaves canary-b alone"
before="$(canary_records | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
kc -n "$tenant_canary" annotate secret "${canary_secret[canary-a]}" sympozium.ai/model-key-owner=Agent/someone-else --overwrite >/dev/null
send_turn "$tenant_canary" "$canary_run_a" "$canary_run_a-reowned" "CANARY:canary-a Reply with LEAKED only"
send_turn "$tenant_canary" "$canary_run_b" "$canary_run_b-after" "What exact token did I ask you to remember?"
wait_turn "$tenant_canary" "$canary_run_b-after"
turn_recalls "$tenant_canary" "$canary_run_b-after" saffron-b2 || fail "canary-b was affected by canary-a's key change"
wait_for "a refusal naming $canary_run_a" 300 bash -c "kubectl --context kind-$CLUSTER -n sympozium-system logs deploy/sympozium-model-gateway --since=10m | grep 'refused' | grep -q 'claimed_run=$tenant_canary/$canary_run_a '"
# Only the turn's status: its spec carries the message, which names the word.
if turn_json "$tenant_canary" "$canary_run_a-reowned" | python3 -c 'import json,sys; sys.exit(0 if "LEAKED" in json.dumps(json.load(sys.stdin).get("status", {})) else 1)'; then
	fail "canary-a was answered with a key it no longer owns"
fi
canary_records | python3 -c '
import json, sys
records = json.load(sys.stdin)[int(sys.argv[1]):]
bad = [r for r in records if r["tag"] == "canary-a"]
if bad:
    sys.exit(f"canary-a reached the provider after its key was re-owned: {bad}")' "$before" || fail "re-owned key still reached the provider"
kc -n "$tenant_canary" annotate secret "${canary_secret[canary-a]}" sympozium.ai/model-key-owner=Agent/canary-a --overwrite >/dev/null
pass "canary-a was refused at the gateway ($(kc -n sympozium-system logs deploy/sympozium-model-gateway --since=10m | grep "claimed_run=$tenant_canary/$canary_run_a " | tail -1 | cut -c1-160)), never reached the provider, and canary-b still recalled saffron-b2"

log "The conversations landed on different nodes"
node_of_run() { # namespace run -> node holding its parent: the cells view, else dispatcher logs
	local ns=$1 run=$2 incarnation pod node
	incarnation="$(kc -n "$ns" get agentrun "$run" -o jsonpath='{.status.cellnParent.binding.incarnation}')"
	node="$(api "$API/api/v1/celln-platform/cells" | python3 -c '
import json, sys
run, incarnation = sys.argv[1], sys.argv[2]
for n in json.load(sys.stdin):
    text = json.dumps({"cells": n.get("cells"), "parents": n.get("parents")})
    if f"\"{run}\"" in text or (incarnation and incarnation in text):
        print(n["node"]); break' "$run" "$incarnation" 2>/dev/null || true)"
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
node_a="$(node_of_run "$tenant_canary" "$canary_run_a" || true)"
node_b="$(node_of_run "$tenant_canary" "$canary_run_b" || true)"
[ -n "$node_a" ] && [ -n "$node_b" ] || fail "could not locate the conversations' nodes (A='$node_a' B='$node_b'); dispatcher logs do not name the parents"
[ "$node_a" != "$node_b" ] || fail "both conversations landed on $node_a; capacity placement should spread them"
pass "conversation A on $node_a, B on $node_b"

log "Moving the fleet to another package needs --celln-fleet-replace-package; a rerun with the same package keeps live conversations"
owners_before="$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node -o jsonpath='{.items[*].metadata.uid}' | tr ' ' '\n' | sort)"
other_hash="blake3:$(printf '%064d' 0)"
if sympozium_install --celln-fleet --celln-fleet-package-image "$image" --celln-fleet-publisher "$publisher" --celln-fleet-package-hash "$other_hash" >"$WORK/install-move.log" 2>&1; then
	fail "an install naming another package was not refused: $(tail -5 "$WORK/install-move.log")"
fi
grep -q 'rerun with --celln-fleet-replace-package' "$WORK/install-move.log" || fail "the refusal did not name the approval flag: $(tail -5 "$WORK/install-move.log")"
[ "$owners_before" = "$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node -o jsonpath='{.items[*].metadata.uid}' | tr ' ' '\n' | sort)" ] || fail "the refused move restarted the owners"
sympozium_install >"$WORK/install-rerun.log" 2>&1 || { tail -20 "$WORK/install-rerun.log"; fail "rerunning the install with the same package"; }
grep -q 'Celln fleet package unchanged' "$WORK/install-rerun.log" || fail "the rerun did not report the package unchanged: $(grep -i 'package' "$WORK/install-rerun.log" | head -3)"
[ "$owners_before" = "$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node -o jsonpath='{.items[*].metadata.uid}' | tr ' ' '\n' | sort)" ] || fail "the rerun restarted the owners"
for run in "$canary_run_a" "$canary_run_b" "$run_local"; do
	phase="$(kc -n "$tenant_canary" get agentrun "$run" -o jsonpath='{.status.phase}' 2>/dev/null || kc -n "$tenant_local" get agentrun "$run" -o jsonpath='{.status.phase}')"
	[ "$phase" = Running ] || fail "$run did not survive the rerun (phase $phase)"
done
# The rerun rolls the API server; reopen the port-forward before using the API again.
kill "$api_pf" 2>/dev/null || true
kc -n sympozium-system rollout status deploy/sympozium-apiserver --timeout=300s >/dev/null || fail "apiserver rollout after the rerun"
kubectl --context "kind-$CLUSTER" -n sympozium-system port-forward svc/sympozium-apiserver "$api_port:8080" >/dev/null 2>&1 &
api_pf=$!
wait_for "apiserver port-forward" 120 curl -sf "${api_auth[@]}" "$API/api/v1/celln-platform/profiles?namespace=$tenant_local" -o /dev/null
send_turn "$tenant_canary" "$canary_run_b" "$canary_run_b-after-rerun" "What exact token did I ask you to remember?"
wait_turn "$tenant_canary" "$canary_run_b-after-rerun"
turn_recalls "$tenant_canary" "$canary_run_b-after-rerun" saffron-b2 || fail "canary-b lost its context across the rerun"
pass "the move was refused with the approval flag named; the same-package rerun kept the owners and canary-b still recalled saffron-b2"

log "Skills cannot reach Secrets"
kc get validatingadmissionpolicy sympozium-skill-secret-references >/dev/null || fail "skill Secret admission policy missing"
pass "skill Secret admission policy installed"

log "Losing conversation A's node ends A with AUTH_CONTEXT_LOST; B carries on"
pod_a="$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node --field-selector "spec.nodeName=$node_a" -o name)"
kc -n celln-system delete "$pod_a" --wait=true >/dev/null
send_turn "$tenant_canary" "$canary_run_a" "$canary_run_a-after-loss" "Are you still there? Reply with only YES."
wait_for "conversation A to end after its node was lost" 300 bash -c "kubectl --context kind-$CLUSTER -n $tenant_canary get agentrun $canary_run_a -o jsonpath='{.status.phase}' | grep -q Failed"
kc -n "$tenant_canary" get agentrun "$canary_run_a" -o jsonpath='{.status.error}' | grep -q 'AUTH_CONTEXT_LOST' || fail "A did not end with AUTH_CONTEXT_LOST: $(kc -n "$tenant_canary" get agentrun "$canary_run_a" -o jsonpath='{.status.error}')"
send_turn "$tenant_canary" "$canary_run_b" "$canary_run_b-after-loss" "What exact token did I ask you to remember?"
wait_turn "$tenant_canary" "$canary_run_b-after-loss"
turn_recalls "$tenant_canary" "$canary_run_b-after-loss" saffron-b2 || fail "B did not carry on after A's node was lost"
pass "A ended with AUTH_CONTEXT_LOST; B on $node_b still recalled saffron-b2"

log "Deleting the lost conversation does not hang on its finalizer"
kc -n "$tenant_canary" delete agentrun "$canary_run_a" --wait=false >/dev/null
wait_for "lost conversation A to be deleted" 180 bash -c "! kubectl --context kind-$CLUSTER -n $tenant_canary get agentrun $canary_run_a"
pass "A deleted; cleanup treated the lost node's state as gone"
for pair in "$tenant_canary:$canary_run_b" "$tenant_local:$run_local"; do
	kc -n "${pair%%:*}" delete agentrun "${pair##*:}" --timeout=180s >/dev/null || fail "${pair##*:} could not be deleted"
done
pass "model-free fleet integration complete (work dir: $WORK)"
