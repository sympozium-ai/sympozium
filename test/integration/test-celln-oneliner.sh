#!/usr/bin/env bash
# The one-liner: `sympozium install` with no Celln flags and no model in the
# environment deploys the fleet, the router and the model gateway for every
# namespace. This journey builds the default starter package the way the
# release does, pins it (and the gateway image) into a CLI build, installs
# with nothing but image overrides for the sideloaded builds, and checks that
# the install disclosed the fleet's privilege and asked for no key, the node
# probe labelled the KVM nodes, the catalogue is installed, a fresh namespace
# is offered the mediation-only profile and gets its runtime wrapper without
# any Agent or connection being made for it, and `sympozium doctor` passes
# with its "Mediated model access" check reporting the origin guardrail.
#
# No model is needed: an Agent brings its own ModelConnection and key; the
# fleet holds none (test-celln-fleet.sh runs the conversations).
#
# Inputs: CELLN_BUNDLE (a Celln release bundle: bin/celln + share/celln),
# optional FLEET_CERT_MANAGER_MANIFEST, FLEET_CLUSTER, FLEET_WORK,
# FLEET_REGISTRY_NAME, FLEET_REGISTRY_PORT (default 5001),
# KIND_EXPERIMENTAL_DOCKER_NETWORK (default kind), KEEP_CLUSTER=1. Images:
# controller, apiserver, webhook,
# node-probe, model-gateway and celln-installer built with TAG, and the Celln
# image tagged CELLN_IMAGE.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
CLUSTER="${FLEET_CLUSTER:-oneliner}"
TAG="${SYMPOZIUM_IMAGE_TAG:-fleet-trial}"
CELLN_IMAGE="${CELLN_IMAGE:-celln:$TAG}"
REGISTRY_NAME="${FLEET_REGISTRY_NAME:-kind-registry}"
# The registry's host port and the Docker network the Kind nodes share (KIND_EXPERIMENTAL_DOCKER_NETWORK).
REGISTRY_PORT="${FLEET_REGISTRY_PORT:-5001}"
KIND_NETWORK="${KIND_EXPERIMENTAL_DOCKER_NETWORK:-kind}"
WORK="${FLEET_WORK:-$(mktemp -d /tmp/celln-oneliner.XXXXXX)}"
PROFILE=celln-native-starter
POLICY=celln-fleet-starter
: "${CELLN_BUNDLE:?a Celln bundle directory (bin/celln, share/celln) is required}"
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
pf=""
cleanup() {
	[ -z "$pf" ] || kill "$pf" 2>/dev/null || true
	[ "${KEEP_CLUSTER:-}" = 1 ] || kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "Cluster and registry"
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

log "Default starter package, built and published the way the release does"
"$REPO/hack/build-celln-starter.sh" --bundle "$CELLN_BUNDLE" --kernel "$kernel" \
	--image "localhost:$REGISTRY_PORT/celln/starter:oneliner" --out "$WORK/starter" --push >"$WORK/starter-build.log" 2>&1 || { tail -5 "$WORK/starter-build.log"; fail "starter package"; }
digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"].split("@")[1])' "$WORK/starter/starter.json")"
package_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["packageHash"])' "$WORK/starter/starter.json")"
publisher="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["publisher"])' "$WORK/starter/starter.json")"
image="$registry/celln/starter@$digest"
pass "package $package_hash by $publisher at $image"

log "A CLI build that pins that package and the model gateway, as a release build would"
docker tag "ghcr.io/sympozium-ai/sympozium/model-gateway:$TAG" "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG"
docker push -q "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG" >/dev/null
gateway="localhost:$REGISTRY_PORT/sympozium/model-gateway@$(docker buildx imagetools inspect "localhost:$REGISTRY_PORT/sympozium/model-gateway:$TAG" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')"
(cd "$REPO" && go build -ldflags "-X main.cellnStarterImage=$image -X main.cellnStarterHash=$package_hash -X main.cellnStarterPublisher=$publisher -X main.cellnStarterCellnVersion=journey -X main.modelGatewayImage=$gateway" -o "$WORK/sympozium" ./cmd/sympozium)
pass "sympozium built with the pinned starter package and gateway $gateway"

log "Images"
for img in "$CELLN_IMAGE" "ghcr.io/sympozium-ai/sympozium/controller:$TAG" "ghcr.io/sympozium-ai/sympozium/apiserver:$TAG" "ghcr.io/sympozium-ai/sympozium/webhook:$TAG" "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" "ghcr.io/sympozium-ai/sympozium/node-probe:$TAG"; do
	kind load docker-image --name "$CLUSTER" "$img" >/dev/null
done
pass "images loaded"

log "sympozium install: no Celln flags, no model in the environment"
if [ -n "${FLEET_CERT_MANAGER_MANIFEST:-}" ]; then
	kc apply -f "$FLEET_CERT_MANAGER_MANIFEST" >/dev/null
	kc -n cert-manager wait --for=condition=Available deploy --all --timeout=180s >/dev/null
fi
# Only image tags for the sideloaded builds and the private test registry are
# passed; nothing about the package, the scope, the approval or a model. Any
# provider key in the environment is dropped so the install cannot lean on it.
kind get kubeconfig --name "$CLUSTER" >"$WORK/kubeconfig"
KUBECONFIG="$WORK/kubeconfig" env -u DEEPSEEK_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u SYMPOZIUM_CELLN_BACKEND "$WORK/sympozium" install \
	--celln-fleet-output-dir "$WORK/fleet-out" --celln-fleet-wait 20m \
	--celln-router-image "$CELLN_IMAGE" \
	--celln-installer-image "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" \
	--set celln.fleet.package.insecureRegistry=true \
	--set "controller.image.tag=$TAG" --set "apiserver.image.tag=$TAG" --set "webhook.image.tag=$TAG" --set "nodeProbe.image.tag=$TAG" \
	</dev/null >"$WORK/install.log" 2>&1 || { tail -30 "$WORK/install.log"; fail "sympozium install"; }
grep -q 'Using the default Celln starter package' "$WORK/install.log" || fail "the install did not pick the pinned package: $(head -20 "$WORK/install.log")"
grep -q 'two privileged DaemonSets' "$WORK/install.log" && grep -q 'Nodes hold no model key' "$WORK/install.log" || fail "the install did not disclose the fleet's privilege: $(head -20 "$WORK/install.log")"
grep -q 'Model gateway:' "$WORK/install.log" || fail "the install did not report the model gateway"
if grep -Eiq '_API_KEY|provider key|credential-file|celln-fleet-model|SYMPOZIUM_CELLN_BACKEND|starter Agent|model-credentials' "$WORK/install.log"; then
	fail "the install mentioned a provider key or a model input: $(grep -Ei '_API_KEY|provider key|credential-file|celln-fleet-model|SYMPOZIUM_CELLN_BACKEND|starter Agent|model-credentials' "$WORK/install.log" | head -3)"
fi
[ -f "$WORK/fleet-out/installation/installed.json" ] || fail "installer did not record the installation"
grep -q '"runSubmitted": *false' "$WORK/fleet-out/installation/installed.json" || fail "the install submitted a run: $(cat "$WORK/fleet-out/installation/installed.json")"
pass "installed with the pinned package; privilege disclosed; no key asked; no run submitted (log: $WORK/install.log)"

log "ergoz came with the install"
wait_for "ergoz collector" 240 bash -c "kubectl --context kind-$CLUSTER -n ergoz-system get deploy ergoz-collector -o jsonpath='{.status.readyReplicas}' | grep -qx 1"
kc -n ergoz-system get svc -l sympozium.ai/collector=energy -o name | grep -q . || fail "ergoz collector Service is not labelled for discovery"
pass "ergoz release deployed into ergoz-system with a discoverable collector"

log "Nodes joined by themselves; the gateway and its trust came up without any flags"
labelled="$(kc get nodes -l celln.dev/kvm=true -o name | wc -l)"
[ "$labelled" -eq 2 ] || fail "expected both workers labelled by the probe, got $labelled"
kc -n sympozium-system logs ds/sympozium-node-probe --tail=50 | grep -q 'labelled node for the Celln fleet' || fail "the probe did not report labelling"
[ "$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node --field-selector status.phase=Running -o name | wc -l)" -eq 2 ] || fail "two owners expected"
kc -n sympozium-system rollout status deploy/sympozium-model-gateway --timeout=300s >/dev/null || fail "model gateway not ready"
for obj in secret/celln-mediation-controller secret/celln-mediation-gateway configmap/celln-mediation-trust; do
	kc -n sympozium-system get "$obj" >/dev/null || fail "$obj missing"
done
pass "the node probe labelled both KVM workers, both owners are running, the gateway and its trust are in place"

log "The fleet holds no model"
[ -z "$(kc get secrets -A --field-selector metadata.name=celln-fleet-model-credentials -o name)" ] || fail "a fleet model credential Secret exists"
[ -z "$(kc get agents,modelconnections -A -o name)" ] || fail "the install created Agents or ModelConnections: $(kc get agents,modelconnections -A -o name | tr '\n' ' ')"
[ "$(kc get cellnruntimeprofile "$PROFILE" -o jsonpath='{.metadata.labels.celln\.sympozium\.ai/mediation-only}')" = true ] || fail "profile $PROFILE is not labelled celln.sympozium.ai/mediation-only=true"
[ -z "$(kc get cellnexecutionpolicy "$POLICY" -o jsonpath='{.spec.allowedOrigins}{.spec.deniedOrigins}')" ] || fail "policy $POLICY narrows origins out of the box"
pass "no credential Secret, no Agent or ModelConnection anywhere, $PROFILE mediation-only, $POLICY without an origin guardrail"

log "The catalogue and the mediation-only profile are offered to a fresh namespace"
tools="$(kc get clustercellntool -o name | wc -l)"
[ "$tools" -ge 8 ] || fail "expected at least the eight brokered tools, got $tools"
tenant="oneliner-a"
kc create namespace "$tenant" >/dev/null 2>&1 || true
api_token="$(kc -n sympozium-system get secret sympozium-ui-token -o jsonpath='{.data.token}' | base64 -d)"
auth=(-H "Authorization: Bearer $api_token")
# The final wiring upgrade rolls the API server; a port-forward opened into
# the old pod dies with it, so wait for the rollout first.
kc -n sympozium-system rollout status deploy/sympozium-apiserver --timeout=300s >/dev/null || fail "apiserver rollout"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
kubectl --context "kind-$CLUSTER" -n sympozium-system port-forward svc/sympozium-apiserver "$port:8080" >/dev/null 2>&1 &
pf=$!
wait_for "apiserver port-forward" 120 curl -sf "${auth[@]}" "http://127.0.0.1:$port/api/v1/celln-platform/profiles?namespace=$tenant" -o /dev/null
# This is exactly what the Agent wizard consults to enable the Celln tile.
profiles="$(curl -sf "${auth[@]}" "http://127.0.0.1:$port/api/v1/celln-platform/profiles?namespace=$tenant")"
echo "$profiles" | python3 -c '
import json, sys
ps = json.load(sys.stdin)
p = next((p for p in ps if p["name"] == sys.argv[1]), None)
assert p, f"profile {sys.argv[1]} not offered: {ps}"
assert p.get("mediationOnly") is True, "profile is not mediation-only"
assert not p.get("provider") and not p.get("model") and not p.get("agent"), "profile still carries a fleet model or shared Agent"
assert p["wrapper"] == "celln-native" and p.get("toolboxWrapper") == "celln-native.toolbox", p' "$PROFILE" || fail "profile listing for $tenant"
curl -sf "${auth[@]}" -X POST -H 'Content-Type: application/json' -d "{\"profile\":\"$PROFILE\",\"runtimeOnly\":true}" "http://127.0.0.1:$port/api/v1/celln-platform/wrappers?namespace=$tenant" | grep -q '"runtime":"celln-native"' || fail "the runtime wrapper was not created in $tenant"
kc -n "$tenant" get agentruntime celln-native >/dev/null || fail "AgentRuntime celln-native missing in $tenant"
[ -z "$(kc -n "$tenant" get agents,modelconnections -o name)" ] || fail "the wrapper brought model objects into $tenant: $(kc -n "$tenant" get agents,modelconnections -o name | tr '\n' ' ')"
curl -sf "${auth[@]}" "http://127.0.0.1:$port/api/v1/celln-platform/mediation?namespace=$tenant" | python3 -c '
import json, sys
m = json.load(sys.stdin)
assert m["enabled"] is True and m["routes"] == [] and m["pending"] == [], m
assert m["allowedOrigins"] == [] and m["deniedOrigins"] == [], m' || fail "mediation report for $tenant"
pass "$tenant is offered $PROFILE (mediation-only) and got only AgentRuntime celln-native; no Agent, no connection, no route"

log "sympozium doctor passes and reports the origin guardrail"
KUBECONFIG="$WORK/kubeconfig" "$WORK/sympozium" doctor --json >"$WORK/doctor.json" 2>"$WORK/doctor.err" || { cat "$WORK/doctor.err"; python3 -c 'import json,sys; [print(f["status"], f["check"]+":", f["summary"]) for f in json.load(open(sys.argv[1]))["findings"]]' "$WORK/doctor.json" 2>/dev/null; fail "doctor found problems"; }
python3 -c '
import json, sys
report = json.load(open(sys.argv[1]))
assert report["ok"], "doctor reported problems"
f = next(f for f in report["findings"] if f["check"] == "Mediated model access")
assert f["status"] == "PASS", f
want = "every Agent brings its own connection and key, pinned to that endpoint'"'"'s origin"
assert want in f["summary"], f["summary"]
guardrail = f"policy {sys.argv[2]} lets an Agent'"'"'s own key go to any origin the gateway reaches"
assert guardrail in f["summary"], f["summary"]
print(f["summary"])' "$WORK/doctor.json" "$POLICY" >"$WORK/doctor-mediation" || fail "doctor's mediated model access check: $(python3 -c 'import json,sys; print(next((f for f in json.load(open(sys.argv[1]))["findings"] if f["check"] == "Mediated model access"), {}))' "$WORK/doctor.json")"
pass "doctor: $(cat "$WORK/doctor-mediation")"
pass "celln one-liner integration complete (work dir: $WORK)"
