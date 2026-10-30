#!/usr/bin/env bash
# A throwaway multi-node Kind cluster for the Celln KVM lane, isolated from
# anything else on the KVM host (on framework: a live kubeadm cluster, Agent
# Substrate and other Kind clusters), with an enforcing CNI (Calico) in place
# of kindnet, which ignores NetworkPolicy.
#
#   hack/celln-lane/kind-cluster.sh up   <name>   # network, cluster, Calico, caps, checks
#   hack/celln-lane/kind-cluster.sh down <name>   # delete cluster, registry, network (idempotent)
#
# Isolation:
#   - KUBECONFIG must be set to a file under CELLN_LANE_WORK (or RUNNER_TEMP)
#     that only Kind writes; the host's ~/.kube/config is never read or
#     written, and the script fails unless that file holds exactly one
#     context, kind-<name>.
#   - its own Docker network (KIND_EXPERIMENTAL_DOCKER_NETWORK, default
#     <name>) on CELLN_LANE_DOCKER_SUBNET, and pod/service subnets that do not
#     overlap the host's routes (checked against `ip -4 route` before
#     anything is created).
#   - only names starting with "celln-lane" are accepted, so it never deletes
#     another Kind cluster, network or registry.
#   - each node container is capped (docker update --cpus/--memory): Kind
#     has no resource settings of its own.
#
# The fleet journeys (test/integration/test-celln-fleet.sh and
# test-celln-oneliner.sh) reuse a cluster that already exists under their
# FLEET_CLUSTER name, so the lane brings this one up first and points them at
# it with the same KIND_EXPERIMENTAL_DOCKER_NETWORK, FLEET_REGISTRY_NAME and
# FLEET_REGISTRY_PORT.
#
# Optional inputs: CELLN_LANE_NODE_IMAGE, CELLN_LANE_CALICO_VERSION,
# CELLN_LANE_CALICO_SHA256, CELLN_LANE_DOCKER_SUBNET, CELLN_LANE_POD_SUBNET,
# CELLN_LANE_SERVICE_SUBNET, CELLN_LANE_WORKER_CPUS, CELLN_LANE_WORKER_MEMORY,
# CELLN_LANE_CP_CPUS, CELLN_LANE_CP_MEMORY, FLEET_REGISTRY_NAME, CELLN_LANE_WORK.
set -euo pipefail
action="${1:-}" name="${2:-}"
case "$action" in up | down) ;; *) echo "usage: $0 up|down <celln-lane-*>" >&2; exit 2 ;; esac
case "$name" in celln-lane*) ;; *) echo "refusing cluster name '$name': must start with celln-lane" >&2; exit 2 ;; esac
here="$(cd "$(dirname "$0")" && pwd)"
# kind v0.30.0's tested v1.34.0 node image, as in integration-kind.yaml.
node_image="${CELLN_LANE_NODE_IMAGE:-kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a}"
calico_version="${CELLN_LANE_CALICO_VERSION:-v3.33.0}"
calico_sha256="${CELLN_LANE_CALICO_SHA256:-2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5}"
network="${KIND_EXPERIMENTAL_DOCKER_NETWORK:-$name}"
docker_subnet="${CELLN_LANE_DOCKER_SUBNET:-172.29.0.0/24}"
pod_subnet="${CELLN_LANE_POD_SUBNET:-10.123.0.0/16}"
service_subnet="${CELLN_LANE_SERVICE_SUBNET:-10.124.0.0/16}"
registry_name="${FLEET_REGISTRY_NAME:-$name-registry}"
work="${CELLN_LANE_WORK:-${RUNNER_TEMP:-/tmp}/celln-lane}"
log() { printf '\033[1;33m---- %s\033[0m\n' "$*"; }
fail() { printf '\033[0;31mFAIL %s\033[0m\n' "$*" >&2; exit 1; }
case "$network" in celln-lane*) ;; *) fail "refusing Docker network '$network': must start with celln-lane" ;; esac
case "$registry_name" in celln-lane*) ;; *) fail "refusing registry '$registry_name': must start with celln-lane" ;; esac

# The kubeconfig must be the lane's own file, never the host's.
: "${KUBECONFIG:?set KUBECONFIG to a per-run file under the lane work directory}"
case "$KUBECONFIG" in *:*) fail "KUBECONFIG must be a single file, not a list" ;; esac
case "$(realpath -m "$KUBECONFIG")" in
"$(realpath -m "$work")"/* | "$(realpath -m "${RUNNER_TEMP:-/nonexistent}")"/*) ;;
*) fail "KUBECONFIG ($KUBECONFIG) is not under the lane work directory $work" ;;
esac
[ "$(realpath -m "$KUBECONFIG")" != "$(realpath -m "$HOME/.kube/config")" ] || fail "KUBECONFIG is the host's ~/.kube/config"
mkdir -p "$work" "$(dirname "$KUBECONFIG")"

if [ "$action" = down ]; then
	kind delete cluster --name "$name" >/dev/null 2>&1 || true
	docker rm -f "$registry_name" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	rm -f "$KUBECONFIG"
	echo "deleted kind cluster $name, registry $registry_name and network $network (where they existed)"
	exit 0
fi

[ -c /dev/kvm ] || fail "/dev/kvm is missing on this host"
[ -r /dev/kvm ] && [ -w /dev/kvm ] || fail "/dev/kvm is not read-writable by $(id -un) (add the user to the group owning /dev/kvm: $(stat -c %G /dev/kvm))"
kind get clusters 2>/dev/null | grep -qx "$name" && fail "cluster $name already exists; the lane wants a fresh one (run '$0 down $name')"
[ -s "$KUBECONFIG" ] && fail "$KUBECONFIG already has content; the lane wants an empty per-run kubeconfig"

log "Subnets: docker $docker_subnet, pods $pod_subnet, services $service_subnet must not overlap the host"
ip -4 route show | awk '{print $1}' | python3 -c '
import ipaddress, sys
want = {n: ipaddress.ip_network(n) for n in sys.argv[1:]}
for line in sys.stdin:
    dest = line.strip()
    if dest in ("default", "") or not dest[0].isdigit():
        continue
    route = ipaddress.ip_network(dest, strict=False)
    for name, net in want.items():
        if net.overlaps(route):
            sys.exit(f"{name} overlaps the host route {route}")
nets = list(want.values())
for i, a in enumerate(nets):
    for b in nets[i + 1:]:
        if a.overlaps(b):
            sys.exit(f"{a} overlaps {b}")
# The live kubeadm default service range is not a route on the host; keep clear of it too.
for net in nets:
    if net.overlaps(ipaddress.ip_network("10.96.0.0/12")):
        sys.exit(f"{net} overlaps the kubeadm default service range 10.96.0.0/12")
' "$docker_subnet" "$pod_subnet" "$service_subnet" || fail "subnet clash; set CELLN_LANE_*_SUBNET"
docker network inspect "$network" >/dev/null 2>&1 && fail "Docker network $network already exists (a previous run did not clean up?)"

log "Calico $calico_version manifest (sha256 checked before use)"
curl --fail --location --silent --show-error --retry 3 \
	"https://raw.githubusercontent.com/projectcalico/calico/${calico_version}/manifests/calico.yaml" -o "$work/calico.yaml"
printf '%s  %s\n' "$calico_sha256" "$work/calico.yaml" | sha256sum --check --quiet || fail "calico.yaml does not match the pinned sha256"
# Pin Calico's IPv4 pool to the pod subnet (the manifest ships it commented out).
sed -i -e 's|^\( *\)# - name: CALICO_IPV4POOL_CIDR$|\1- name: CALICO_IPV4POOL_CIDR|' \
	-e "s|^\\( *\\)#   value: \"192.168.0.0/16\"\$|\\1  value: \"$pod_subnet\"|" "$work/calico.yaml"
grep -q "value: \"$pod_subnet\"" "$work/calico.yaml" || fail "could not set CALICO_IPV4POOL_CIDR in the manifest"

log "Docker network $network ($docker_subnet), Kind cluster $name: one control plane, two workers, no default CNI"
docker network create --driver bridge --subnet "$docker_subnet" \
	--opt com.docker.network.bridge.enable_ip_masquerade=true \
	--label io.x-k8s.kind.network=1 --label sympozium.ai/celln-lane="$name" "$network" >/dev/null
sed -e "s|@NODE_IMAGE@|$node_image|g" -e "s|@POD_SUBNET@|$pod_subnet|g" -e "s|@SERVICE_SUBNET@|$service_subnet|g" \
	"$here/kind-config.yaml" >"$work/kind.yaml"
KIND_EXPERIMENTAL_DOCKER_NETWORK="$network" kind create cluster --name "$name" --config "$work/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 0s

contexts="$(kubectl config get-contexts -o name)"
[ "$contexts" = "kind-$name" ] || fail "the lane kubeconfig holds '$contexts', expected exactly kind-$name"
[ "$(kubectl config current-context)" = "kind-$name" ] || fail "current context is not kind-$name"
kc() { kubectl --context "kind-$name" "$@"; }

log "Resource caps on the node containers"
for node in $(kind get nodes --name "$name"); do
	case "$node" in
	*control-plane) docker update --cpus "${CELLN_LANE_CP_CPUS:-4}" --memory "${CELLN_LANE_CP_MEMORY:-6g}" --memory-swap "${CELLN_LANE_CP_MEMORY:-6g}" "$node" >/dev/null ;;
	*) docker update --cpus "${CELLN_LANE_WORKER_CPUS:-8}" --memory "${CELLN_LANE_WORKER_MEMORY:-12g}" --memory-swap "${CELLN_LANE_WORKER_MEMORY:-12g}" "$node" >/dev/null ;;
	esac
done

log "Calico"
kc apply -f "$work/calico.yaml" >/dev/null
kc -n kube-system rollout status daemonset/calico-node --timeout=300s
kc -n kube-system rollout status deployment/calico-kube-controllers --timeout=300s
kc wait --for=condition=Ready nodes --all --timeout=300s >/dev/null
kc -n kube-system rollout status deployment/coredns --timeout=300s >/dev/null

log "KVM inside the workers"
for node in $(kind get nodes --name "$name" | grep worker); do
	docker exec "$node" test -c /dev/kvm || fail "/dev/kvm is not visible inside $node"
done
echo "cluster $name ready on network $network with Calico $calico_version; /dev/kvm visible on every worker"
