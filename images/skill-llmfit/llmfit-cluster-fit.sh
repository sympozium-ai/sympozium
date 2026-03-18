#!/bin/bash
# llmfit-cluster-fit.sh
# Probe llmfit recommendations on each Kubernetes node and aggregate placement results.

set -euo pipefail

SERVICEACCOUNT_NS_FILE="/var/run/secrets/kubernetes.io/serviceaccount/namespace"
if [[ -n "${POD_NAMESPACE:-}" ]]; then
  NAMESPACE="${POD_NAMESPACE}"
elif [[ -r "$SERVICEACCOUNT_NS_FILE" ]]; then
  NAMESPACE="$(cat "$SERVICEACCOUNT_NS_FILE")"
else
  NAMESPACE="sympozium-system"
fi
MODEL_QUERY=""
LIMIT="5"
MIN_FIT="good"
USE_CASE=""
TIMEOUT_SECONDS="180"
PROBE_IMAGE="${LLMFIT_PROBE_IMAGE:-ghcr.io/sympozium-ai/sympozium/skill-llmfit:latest}"

usage() {
  cat <<'EOF'
Usage:
  llmfit-cluster-fit.sh --model <model-name> [options]

Options:
  --model <name>         Model name or substring to evaluate (required)
  --namespace <name>     Namespace for probe pods (default: POD_NAMESPACE or sympozium-system)
  --limit <n>            llmfit recommendation limit per node (default: 5)
  --min-fit <level>      perfect|good|marginal|too_tight (default: good)
  --use-case <name>      general|coding|reasoning|chat|multimodal|embedding
  --timeout <seconds>    Wait timeout per probe pod (default: 180)
  --probe-image <image>  Image to run on each node (default: ghcr.io/sympozium-ai/sympozium/skill-llmfit:latest)
  -h, --help             Show this help

Examples:
  llmfit-cluster-fit.sh --model "Qwen/Qwen2.5-Coder-14B-Instruct"
  llmfit-cluster-fit.sh --model "Qwen2.5" --use-case coding --min-fit good --limit 10
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)
      MODEL_QUERY="$2"
      shift 2
      ;;
    --namespace)
      NAMESPACE="$2"
      shift 2
      ;;
    --limit)
      LIMIT="$2"
      shift 2
      ;;
    --min-fit)
      MIN_FIT="$2"
      shift 2
      ;;
    --use-case)
      USE_CASE="$2"
      shift 2
      ;;
    --timeout)
      TIMEOUT_SECONDS="$2"
      shift 2
      ;;
    --probe-image)
      PROBE_IMAGE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$MODEL_QUERY" ]]; then
  echo "--model is required" >&2
  usage >&2
  exit 2
fi

case "$MIN_FIT" in
  perfect|good|marginal|too_tight) ;;
  *)
    echo "invalid --min-fit value: $MIN_FIT" >&2
    exit 2
    ;;
esac

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

nodes=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
if [[ -z "$nodes" ]]; then
  echo '{"error":"no Kubernetes nodes found"}'
  exit 1
fi

tmp_results=$(mktemp)
cleanup_list=()

cleanup() {
  for item in "${cleanup_list[@]:-}"; do
    ns="${item%%/*}"
    pod="${item##*/}"
    kubectl delete pod "$pod" -n "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
  rm -f "$tmp_results"
}
trap cleanup EXIT

now_epoch=$(date +%s)
model_yaml=$(printf '%s' "$MODEL_QUERY" | sed 's/\\/\\\\/g; s/"/\\"/g')
use_case_yaml=$(printf '%s' "$USE_CASE" | sed 's/\\/\\\\/g; s/"/\\"/g')

for node in $nodes; do
  sanitized_node=$(echo "$node" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-')
  suffix=$(printf '%s' "$node-$now_epoch-$RANDOM" | sha1sum | cut -c1-8)
  pod_name="llmfit-probe-${sanitized_node:0:30}-${suffix}"

  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: llmfit-probe
    sympozium.ai/managed-by: llmfit-skill
spec:
  restartPolicy: Never
  nodeName: ${node}
  tolerations:
    - operator: Exists
  containers:
    - name: probe
      image: ${PROBE_IMAGE}
      imagePullPolicy: IfNotPresent
      env:
        - name: MODEL_QUERY
          value: "${model_yaml}"
        - name: LLMFIT_LIMIT
          value: "${LIMIT}"
        - name: LLMFIT_MIN_FIT
          value: ${MIN_FIT}
        - name: LLMFIT_USE_CASE
          value: "${use_case_yaml}"
        - name: NODE_NAME
          value: ${node}
      command: ["/bin/sh", "-lc"]
      args: ["/usr/local/bin/llmfit-probe-json.sh"]
EOF

  cleanup_list+=("${NAMESPACE}/${pod_name}")

  if ! kubectl wait --for=condition=Ready "pod/${pod_name}" -n "$NAMESPACE" --timeout=60s >/dev/null 2>&1; then
    :
  fi

  wait_deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
  phase=""
  while true; do
    phase=$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
      break
    fi
    if [[ $(date +%s) -ge $wait_deadline ]]; then
      phase="Timeout"
      break
    fi
    sleep 2
  done

  if [[ "$phase" == "Succeeded" ]]; then
    if out=$(kubectl logs "$pod_name" -n "$NAMESPACE" 2>/dev/null); then
      if echo "$out" | jq -e . >/dev/null 2>&1; then
        echo "$out" >>"$tmp_results"
      else
        jq -n --arg node "$node" --arg err "probe output was not valid JSON" --arg raw "$out" \
          '{node:$node,error:$err,raw:$raw}' >>"$tmp_results"
      fi
    else
      jq -n --arg node "$node" --arg err "failed to read probe logs" '{node:$node,error:$err}' >>"$tmp_results"
    fi
  else
    reason=$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{.status.reason}' 2>/dev/null || true)
    message=$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{.status.message}' 2>/dev/null || true)
    jq -n --arg node "$node" --arg phase "$phase" --arg reason "$reason" --arg message "$message" \
      '{node:$node,error:("probe did not succeed (phase=" + $phase + ")"),reason:$reason,message:$message}' >>"$tmp_results"
  fi
done

jq -s --arg model "$MODEL_QUERY" --arg minFit "$MIN_FIT" --arg useCase "$USE_CASE" '
  def topScore($x): ($x.top[0].score // -1);
  def topFit($x): ($x.top[0].fit_level // "unknown");
  {
    requested_model: $model,
    min_fit: $minFit,
    use_case: (if $useCase == "" then null else $useCase end),
    node_count: length,
    ranked_nodes: (
      map(select(.error == null))
      | sort_by(-topScore(.))
      | map({
          node: .node,
          matched_count: (.matched_count // 0),
          best_fit: topFit(.),
          best_score: topScore(.),
          best_model: (.top[0].name // null),
          best_runtime: (.top[0].runtime // null),
          best_quant: (.top[0].best_quant // null),
          estimated_tps: (.top[0].estimated_tps // null)
        })
    ),
    node_results: .
  }
' "$tmp_results"
