#!/usr/bin/env bash
# The one-liner: `sympozium install` with a model backend in the environment
# and nothing else enables enduring Celln parents for every namespace. This
# journey builds the default starter package the way the release does, pins
# it into a CLI build, installs with no --celln-fleet-* flags, and checks that
# the node probe labelled the KVM nodes, the catalogue is installed, a fresh
# namespace is offered the Celln parent plane, and a run answers on it.
#
# Inputs: CELLN_BUNDLE (a Celln release bundle: bin/celln + share/celln),
# SYMPOZIUM_CELLN_BACKEND (the backend the install picks up, e.g.
# name=native,provider=llama-server,model=M,endpoint=http://H:8080/v1/chat/completions,allow-insecure=true),
# optional FLEET_CERT_MANAGER_MANIFEST, FLEET_CLUSTER, FLEET_WORK.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
CLUSTER="${FLEET_CLUSTER:-oneliner}"
TAG="${SYMPOZIUM_IMAGE_TAG:-fleet-trial}"
CELLN_IMAGE="${CELLN_IMAGE:-celln:$TAG}"
REGISTRY_NAME="${FLEET_REGISTRY_NAME:-kind-registry}"
WORK="${FLEET_WORK:-$(mktemp -d /tmp/celln-oneliner.XXXXXX)}"
: "${CELLN_BUNDLE:?a Celln bundle directory (bin/celln, share/celln) is required}"
: "${SYMPOZIUM_CELLN_BACKEND:?the backend spec the install should discover is required}"
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

log "Cluster and registry"
if ! docker inspect "$REGISTRY_NAME" >/dev/null 2>&1; then
	docker run -d --restart=always -p 127.0.0.1:5001:5000 --name "$REGISTRY_NAME" registry:2 >/dev/null
fi
if ! kind get clusters | grep -qx "$CLUSTER"; then
	printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n  - role: control-plane\n  - role: worker\n  - role: worker\n' >"$WORK/kind.yaml"
	kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 120s
fi
docker network connect kind "$REGISTRY_NAME" 2>/dev/null || true
registry="$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$REGISTRY_NAME"):5000"
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
	--image "localhost:5001/celln/starter:oneliner" --out "$WORK/starter" --push >"$WORK/starter-build.log" 2>&1 || { tail -5 "$WORK/starter-build.log"; fail "starter package"; }
digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"].split("@")[1])' "$WORK/starter/starter.json")"
package_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["packageHash"])' "$WORK/starter/starter.json")"
publisher="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["publisher"])' "$WORK/starter/starter.json")"
image="$registry/celln/starter@$digest"
pass "package $package_hash by $publisher at $image"

log "A CLI build that pins that package, as a release build would"
(cd "$REPO" && go build -ldflags "-X main.cellnStarterImage=$image -X main.cellnStarterHash=$package_hash -X main.cellnStarterPublisher=$publisher -X main.cellnStarterCellnVersion=journey" -o "$WORK/sympozium" ./cmd/sympozium)
pass "sympozium built with the pinned starter package"

log "Images"
for img in "$CELLN_IMAGE" "ghcr.io/sympozium-ai/sympozium/controller:$TAG" "ghcr.io/sympozium-ai/sympozium/apiserver:$TAG" "ghcr.io/sympozium-ai/sympozium/webhook:$TAG" "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" "ghcr.io/sympozium-ai/sympozium/node-probe:$TAG"; do
	kind load docker-image --name "$CLUSTER" "$img" >/dev/null
done
pass "images loaded"

log "sympozium install — no --celln-fleet flags; the backend comes from SYMPOZIUM_CELLN_BACKEND"
if [ -n "${FLEET_CERT_MANAGER_MANIFEST:-}" ]; then
	kc apply -f "$FLEET_CERT_MANAGER_MANIFEST" >/dev/null
	kc -n cert-manager wait --for=condition=Available deploy --all --timeout=180s >/dev/null
fi
# Only image tags for the sideloaded builds and the private test registry are
# passed; nothing about the package, the scope, the approval or the backend.
KUBECONFIG="$WORK/kubeconfig" true; kind get kubeconfig --name "$CLUSTER" >"$WORK/kubeconfig"
KUBECONFIG="$WORK/kubeconfig" SYMPOZIUM_CELLN_BACKEND="$SYMPOZIUM_CELLN_BACKEND" "$WORK/sympozium" install \
	--celln-fleet-output-dir "$WORK/fleet-out" --celln-fleet-wait 20m \
	--celln-router-image "$CELLN_IMAGE" \
	--celln-installer-image "ghcr.io/sympozium-ai/sympozium/celln-installer:$TAG" \
	--set celln.fleet.package.insecureRegistry=true \
	--set "controller.image.tag=$TAG" --set "apiserver.image.tag=$TAG" --set "webhook.image.tag=$TAG" --set "nodeProbe.image.tag=$TAG" \
	</dev/null >"$WORK/install.log" 2>&1 || { tail -30 "$WORK/install.log"; fail "sympozium install"; }
grep -q 'Using the default Celln starter package' "$WORK/install.log" || fail "the install did not pick the pinned package: $(head -20 "$WORK/install.log")"
grep -q 'from SYMPOZIUM_CELLN_BACKEND' "$WORK/install.log" || fail "the install did not take the backend from the environment"
pass "installed with the pinned package and the environment's backend"

log "Nodes joined by themselves"
labelled="$(kc get nodes -l celln.dev/kvm=true -o name | wc -l)"
[ "$labelled" -eq 2 ] || fail "expected both workers labelled by the probe, got $labelled"
kc -n sympozium-system logs ds/sympozium-node-probe --tail=50 | grep -q 'labelled node for the Celln fleet' || fail "the probe did not report labelling"
[ "$(kc -n celln-system get pods -l app.kubernetes.io/name=celln-node --field-selector status.phase=Running -o name | wc -l)" -eq 2 ] || fail "two owners expected"
pass "the node probe labelled both KVM workers and both owners are running"

log "The catalogue and the Celln parent plane are offered to a fresh namespace"
tools="$(kc get clustercellntool -o name | wc -l)"
[ "$tools" -ge 8 ] || fail "expected at least the eight brokered tools, got $tools"
tenant="oneliner-a"
kc create namespace "$tenant" >/dev/null 2>&1 || true
api_token="$(kc -n sympozium-system get secret sympozium-ui-token -o jsonpath='{.data.token}' 2>/dev/null | base64 -d || true)"
[ -n "$api_token" ] || api_token="$(kc -n sympozium-system get deploy sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].value}')"
auth=(-H "Authorization: Bearer $api_token")
# The final wiring upgrade rolls the API server; a port-forward opened into
# the old pod dies with it, so wait for the rollout first.
kc -n sympozium-system rollout status deploy/sympozium-apiserver --timeout=300s >/dev/null || fail "apiserver rollout"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
kubectl --context "kind-$CLUSTER" -n sympozium-system port-forward svc/sympozium-apiserver "$port:8080" >/dev/null 2>&1 &
pf=$!
trap 'kill "$pf" 2>/dev/null || true' EXIT
wait_for "apiserver port-forward" 120 curl -sf "${auth[@]}" "http://127.0.0.1:$port/api/v1/celln-platform/profiles?namespace=$tenant" -o /dev/null
# This is exactly what the Agent wizard consults to enable the Celln parent tile.
profiles="$(curl -sf "${auth[@]}" "http://127.0.0.1:$port/api/v1/celln-platform/profiles?namespace=$tenant")"
echo "$profiles" | grep -q '"name":"celln-native-starter"' || fail "the fresh namespace was not offered the starter profile: $profiles"
curl -sf "${auth[@]}" -X POST -H 'Content-Type: application/json' -d '{"profile":"celln-native-starter"}' "http://127.0.0.1:$port/api/v1/celln-platform/wrappers?namespace=$tenant" | grep -q '"connection":"celln-native"' || fail "wrappers were not created in $tenant"
pass "$tenant is offered celln-native-starter; the wizard's Celln parent tile is enabled there"

log "An Agent answers on the Celln parent plane through the API"
run_json="$WORK/fleet-out/installation/run.json"
[ -f "$run_json" ] || fail "installer did not materialize the sample run"
run="$(python3 -c '
import json, sys
r = json.load(open(sys.argv[1]))["spec"]
print(json.dumps({"agentRef": r["agentRef"], "task": "Where is Botswana? Reply with one short sentence; do not use tools.", "systemPrompt": r["systemPrompt"], "backend": "celln",
  "executionLifecycle": "one-shot", "model": r["model"]["model"], "modelConnectionRef": r["model"]["connectionRef"],
  "cellnSelection": {"runtimeRef": r["cellnSelection"]["runtimeRef"], "clusterToolRefs": r["cellnSelection"]["clusterToolRefs"], "toolRefs": []}}))' "$run_json" |
	curl -sf "${auth[@]}" -X POST -H 'Content-Type: application/json' --data-binary @- "http://127.0.0.1:$port/api/v1/runs?namespace=$tenant" |
	python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["name"])')" || fail "API refused a one-shot run in $tenant"
wait_for "one-shot $run to finish" 300 bash -c "kubectl --context kind-$CLUSTER -n $tenant get agentrun $run -o jsonpath='{.status.phase}' | grep -qE 'Succeeded|Failed'"
[ "$(kc -n "$tenant" get agentrun "$run" -o jsonpath='{.status.phase}')" = Succeeded ] || fail "one-shot $run failed: $(kc -n "$tenant" get agentrun "$run" -o jsonpath='{.status.error}')"
answer="$(kc -n "$tenant" get agentrun "$run" -o jsonpath='{.status.result}')"
pass "one-shot $run answered on the default fleet: ${answer:0:120}"
pass "celln one-liner integration complete (work dir: $WORK)"
