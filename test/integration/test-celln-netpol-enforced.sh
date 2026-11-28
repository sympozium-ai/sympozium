#!/usr/bin/env bash
# The fleet's NetworkPolicies are enforced, not merely present. Run against a
# cluster where test-celln-fleet.sh installed the fleet (KEEP_CLUSTER=1) on an
# enforcing CNI (hack/celln-lane/kind-cluster.sh).
#
# For each policy, a positive control proves the target is listening and
# reachable from the peer the policy admits; the negative probes, from peers
# the policy does not admit, must then fail to connect. On a CNI that ignores
# NetworkPolicy (kindnet) the negative probes connect and this script fails.
#
#   celln-router-ingress (celln-system): admits only the controller and the
#     apiserver pods *in the control-plane namespace* on 8788.
#     + sympozium-system pod labelled app.kubernetes.io/component=apiserver
#     - the same labels in a tenant namespace (a copied label is not enough)
#     - an unlabelled pod in sympozium-system
#   celln-node-ingress (celln-system): admits only the router on 8787.
#     + celln-system pod labelled app.kubernetes.io/name=celln-router
#     - a tenant pod
#
# Inputs: FLEET_CLUSTER (Kind cluster name), optional NETPOL_EVIDENCE (a file
# the results are appended to as JSON lines), NETPOL_PROBE_IMAGE.
set -euo pipefail
CLUSTER="${FLEET_CLUSTER:?FLEET_CLUSTER names the Kind cluster}"
SYSTEM_NS="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
CELLN_NS=celln-system
TENANT_NS=celln-netpol-probe
IMAGE="${NETPOL_PROBE_IMAGE:-busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec}"
EVIDENCE="${NETPOL_EVIDENCE:-/dev/null}"
log() { printf '\033[1;33m---- %s\033[0m\n' "$*"; }
pass() { printf '\033[0;32mPASS %s\033[0m\n' "$*"; }
fail() { printf '\033[0;31mFAIL %s\033[0m\n' "$*" >&2; exit 1; }
kc() { kubectl --context "kind-$CLUSTER" "$@"; }
probes=()
cleanup() {
	for p in "${probes[@]}"; do kc delete pod --wait=false --ignore-not-found -n "${p%%/*}" "${p##*/}" >/dev/null 2>&1 || true; done
	kc delete namespace "$TENANT_NS" --wait=false --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# probe <namespace> <name> <labels-json> <host> <port> -> exit 0 if a TCP
# connection was made within 5 s. A restricted, non-root pod so it is
# admitted under any Pod Security level.
probe() {
	local ns=$1 name=$2 labels=$3 host=$4 port=$5 phase i=0
	probes+=("$ns/$name")
	kc -n "$ns" delete pod "$name" --ignore-not-found --wait=true >/dev/null
	kc -n "$ns" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $name
  labels: $labels
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    runAsGroup: 65534
    seccompProfile: {type: RuntimeDefault}
  containers:
    - name: probe
      image: $IMAGE
      command: ["nc", "-z", "-w", "5", "$host", "$port"]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: {drop: [ALL]}
YAML
	until phase="$(kc -n "$ns" get pod "$name" -o jsonpath='{.status.phase}')" && { [ "$phase" = Succeeded ] || [ "$phase" = Failed ]; }; do
		[ "$i" -lt 120 ] || fail "probe $ns/$name did not finish (phase '$phase')"
		sleep 2
		i=$((i + 2))
	done
	[ "$phase" = Succeeded ]
}
# expect <allow|deny> <policy> <description> <probe args...>
expect() {
	local want=$1 policy=$2 what=$3 got
	shift 3
	if probe "$@"; then got=allow; else got=deny; fi
	printf '{"policy":"%s","probe":"%s","from":"%s/%s","to":"%s:%s","want":"%s","got":"%s"}\n' \
		"$policy" "$what" "$1" "$2" "$4" "$5" "$want" "$got" >>"$EVIDENCE"
	[ "$got" = "$want" ] || fail "$policy: $what: wanted $want, got $got"
	pass "$policy: $what -> $got"
}

log "Preconditions: the CNI enforces policy and the fleet's policies exist"
kc -n kube-system get daemonset calico-node >/dev/null 2>&1 || kc -n kube-system get daemonset cilium >/dev/null 2>&1 ||
	fail "no enforcing CNI (calico-node or cilium) in kube-system"
for np in celln-router-ingress celln-node-ingress; do
	kc -n "$CELLN_NS" get networkpolicy "$np" >/dev/null || fail "NetworkPolicy $CELLN_NS/$np missing"
done
router="celln-router.$CELLN_NS.svc.cluster.local"
node_ip="$(kc -n "$CELLN_NS" get pods -l app.kubernetes.io/name=celln-node --field-selector status.phase=Running -o jsonpath='{.items[0].status.podIP}')"
[ -n "$node_ip" ] || fail "no running celln-node pod"
[ "$(kc -n "$CELLN_NS" get pods -l app.kubernetes.io/name=celln-node -o jsonpath='{.items[0].spec.hostNetwork}')" != true ] ||
	fail "celln-node runs on the host network, where NetworkPolicy does not apply"
kc create namespace "$TENANT_NS" >/dev/null 2>&1 || true
pass "enforcing CNI present; router service $router; owner pod $node_ip"

log "celln-router-ingress"
expect allow celln-router-ingress "apiserver-labelled pod in $SYSTEM_NS reaches the router" \
	"$SYSTEM_NS" netpol-apiserver-ok '{"app.kubernetes.io/component": "apiserver"}' "$router" 8787
expect deny celln-router-ingress "the same labels in a tenant namespace are refused" \
	"$TENANT_NS" netpol-copied-label '{"app.kubernetes.io/component": "apiserver", "control-plane": "controller-manager"}' "$router" 8787
expect deny celln-router-ingress "an unlabelled pod in $SYSTEM_NS is refused" \
	"$SYSTEM_NS" netpol-unlabelled '{"app": "netpol-probe"}' "$router" 8787

log "celln-node-ingress"
expect allow celln-node-ingress "router-labelled pod in $CELLN_NS reaches an owner" \
	"$CELLN_NS" netpol-router-ok '{"app.kubernetes.io/name": "celln-router"}' "$node_ip" 8787
expect deny celln-node-ingress "a tenant pod cannot reach an owner directly" \
	"$TENANT_NS" netpol-tenant-owner '{"app": "netpol-probe"}' "$node_ip" 8787
pass "the fleet's NetworkPolicies are enforced"
