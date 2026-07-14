#!/usr/bin/env bash
# tail-agents.sh — follow the output of every Sympozium agent run, live.
#
# Agent-run pods are created and deleted constantly (`cleanup: delete`), and
# their logs are NOT retained anywhere once the pod is gone — no otel backend,
# no DB. So the only way to debug a run is to be tailing it while it happens.
# This script attaches to every agent run currently in the namespace AND keeps
# watching, picking up new runs as the controller creates them.

set -uo pipefail

NAMESPACE="${NAMESPACE:-default}"
SELECTOR="${SELECTOR:-sympozium.ai/component=agent-run}"
TRIM_PREFIX="${TRIM_PREFIX:-}"
POLL="${POLL:-3}"
CONTAINER_RE='^agent$'
RAW=0

usage() {
    cat <<'EOF'
Usage: hack/tail-agents.sh [options]

Follows the logs of every Sympozium agent-run pod in a namespace, including
runs that start after the script does.

Options:
  -n, --namespace NS   namespace to watch (default: default)
  -l, --selector SEL   label selector for run pods
                       (default: sympozium.ai/component=agent-run)
  -s, --skills         also tail the skill-* sidecars
  -a, --all            tail every container, including ipc-bridge
  -p, --trim PREFIX    strip PREFIX from pod names in the log tag
  -r, --raw            don't unwrap the runner's JSON log lines
  -h, --help           show this help

Environment: NAMESPACE, SELECTOR, TRIM_PREFIX, POLL (seconds between rescans).

Examples:
  hack/tail-agents.sh                         # the `agent` container of every run
  hack/tail-agents.sh --skills                # + skill sidecars (exit codes!)
  NAMESPACE=agents hack/tail-agents.sh --all  # every container of every run

Why --skills is usually what you want: the `agent` container logs only the
tool-call *requests* (truncated), while a `skill-*` sidecar logs
`done [id]: exit=N` — the only place a command's exit code ever appears. The
[id] is nanoseconds-since-epoch, so it correlates the two streams.
EOF
    exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -n|--namespace) NAMESPACE="$2"; shift 2 ;;
        -l|--selector)  SELECTOR="$2"; shift 2 ;;
        -p|--trim)      TRIM_PREFIX="$2"; shift 2 ;;
        -s|--skills)    CONTAINER_RE='^(agent|skill-.*)$'; shift ;;
        -a|--all)       CONTAINER_RE='.'; shift ;;
        -r|--raw)       RAW=1; shift ;;
        -h|--help)      usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 1 ;;
    esac
done

command -v kubectl >/dev/null || { echo "kubectl not found" >&2; exit 1; }

# Kill every background tail when we leave, however we leave.
#
# Deliberately NOT `kill 0`: a script launched without job control shares its
# process group with the caller, so `kill 0` would take the caller's shell down
# with it. Kill our own children explicitly instead — and their children too,
# since each tail is a subshell running a `kubectl | jq | awk` pipeline that
# would otherwise survive as an orphan.
declare -a KIDS=()
SLEEP_PID=""
cleanup() {
    trap - EXIT INT TERM
    exec 2>/dev/null
    local p kids
    for p in "${KIDS[@]:-}"; do
        [[ -n "$p" ]] || continue
        # Note the order. Each tail is a subshell running `kubectl | jq | awk`.
        # Collect the pipeline's PIDs *before* killing anything, then kill the
        # subshell first: if we killed the pipeline first, the subshell would
        # still be alive to notice and print "Terminated: 15 kubectl logs ...".
        # Killing the subshell alone would instead orphan a live `kubectl -f`.
        kids=$(pgrep -P "$p" 2>/dev/null)
        kill "$p" 2>/dev/null
        [[ -n "$kids" ]] && kill $kids 2>/dev/null
    done
    # The poll-loop `sleep` is left to expire on its own (<= $POLL seconds).
    # Killing it makes bash announce "Terminated: 15 sleep" on the way out.
}
# A bare trap handler returns into the poll loop, so the signal traps must exit
# themselves. EXIT still cleans up for the normal/error paths.
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

declare -A SEEN
COLORS=(36 32 33 35 34 31 96 92 93 95 94 91)
idx=0

# Run-pod names carry the agent and schedule they came from, which is the same
# for every pod on screen. --trim/$TRIM_PREFIX drops that shared prefix from the
# log tag so the interesting part of the name stays visible.
short() { printf '%s' "${1#"$TRIM_PREFIX"}"; }

# Unwrap the agent-runner's JSON lines into "HH:MM:SS message"; pass anything
# else (plain sidecar output, the runner's own preamble) straight through.
prettify() {
    if (( RAW )) || ! command -v jq >/dev/null; then cat; return; fi
    while IFS= read -r line; do
        if [[ "$line" == \{* ]]; then
            out=$(jq -r 'select(.msg != null) | ((.time // "")[11:19]) + " " + .msg' <<<"$line" 2>/dev/null)
            [[ -n "$out" ]] && { printf '%s\n' "$out"; continue; }
        fi
        printf '%s\n' "$line"
    done
}

pod_exists() { kubectl get pod -n "$NAMESPACE" "$1" >/dev/null 2>&1; }

# Why isn't this container running yet? Surface ImagePullBackOff etc. rather
# than silently spinning — a stuck init container looks identical to a slow model.
waiting_reason() {
    kubectl get pod -n "$NAMESPACE" "$1" -o json 2>/dev/null | jq -r '
        [ (.status.initContainerStatuses // [])[], (.status.containerStatuses // [])[] ]
        | map(select(.state.waiting != null and .state.waiting.reason != "PodInitializing"))
        | if length > 0 then "\(.[0].name): \(.[0].state.waiting.reason)" else empty end'
}

# Block until the container has actually started, reporting any stall once.
await_start() {
    local pod="$1" cont="$2" tag="$3" last=""
    while pod_exists "$pod"; do
        state=$(kubectl get pod -n "$NAMESPACE" "$pod" \
            -o jsonpath="{.status.containerStatuses[?(@.name=='$cont')].state}" 2>/dev/null)
        [[ "$state" == *running* || "$state" == *terminated* ]] && return 0
        reason=$(waiting_reason "$pod")
        if [[ -n "$reason" && "$reason" != "$last" ]]; then
            printf '%s \033[33m[waiting] %s\033[0m\n' "$tag" "$reason"
            last="$reason"
        fi
        sleep "$POLL"
    done
    return 1
}

tail_container() {
    local pod="$1" cont="$2" color="$3"
    local tag
    tag=$(printf '\033[%sm%s/%s\033[0m' "$color" "$(short "$pod")" "$cont")

    await_start "$pod" "$cont" "$tag" || return 0
    printf '%s \033[90m--- streaming ---\033[0m\n' "$tag"

    kubectl logs -n "$NAMESPACE" "$pod" -c "$cont" -f --tail=-1 2>/dev/null \
        | prettify \
        | awk -v t="$tag" '{ print t " " $0; fflush() }'

    printf '%s \033[90m--- ended ---\033[0m\n' "$tag"
}

printf '\033[1mtailing agent runs\033[0m  ns=%s  selector=%s  containers=%s\n' \
    "$NAMESPACE" "$SELECTOR" "$CONTAINER_RE" >&2
printf 'watching for new runs every %ss — Ctrl-C to stop\n\n' "$POLL" >&2

while :; do
    pods=$(kubectl get pods -n "$NAMESPACE" -l "$SELECTOR" \
             -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)

    while IFS= read -r pod; do
        [[ -z "$pod" ]] && continue
        conts=$(kubectl get pod -n "$NAMESPACE" "$pod" \
                  -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null)
        while IFS= read -r cont; do
            [[ -z "$cont" ]] && continue
            [[ "$cont" =~ $CONTAINER_RE ]] || continue
            key="$pod/$cont"
            [[ -n "${SEEN[$key]:-}" ]] && continue
            SEEN[$key]=1
            color=${COLORS[$(( idx++ % ${#COLORS[@]} ))]}
            tail_container "$pod" "$cont" "$color" &
            KIDS+=("$!")
        done <<< "$conts"
    done <<< "$pods"

    # Backgrounded so a signal reaches the trap immediately, and so bash does
    # not announce "Terminated: 15 sleep" when it kills its own foreground child.
    sleep "$POLL" & SLEEP_PID=$!
    wait "$SLEEP_PID" 2>/dev/null
done
