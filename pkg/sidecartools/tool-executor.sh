#!/bin/bash
# tool-executor.sh — Watches /ipc/tools/ for exec-request-*.json files,
# executes the requested commands, and writes exec-result-*.json responses.
# This script runs as the main process in skill sidecar containers.
#
# This is the canonical copy. Every built-in skill image runs it (github-gitops
# through a wrapper that loads credentials first), and the controller mounts it
# read-only at /sympozium/bin/tool-executor.sh in every skill sidecar, so a stock
# image can run it by setting it as the SkillPack's command.
#
# Requires bash, jq, base64, timeout (coreutils or busybox) and the basic file
# utilities (mktemp, basename, cat, tr, mv, rm, sleep).
#
# Environment:
#   SYMPOZIUM_SKILL_PACK           this sidecar's SkillPack name, for target routing
#   TOOL_EXECUTOR_DEFAULT_TIMEOUT  timeout for a request that sets none, in seconds (default 30)
#   TOOL_EXECUTOR_MAX_TIMEOUT      upper bound on a request's timeout, in seconds (default 120)
#   SYMPOZIUM_IPC_DIR              IPC volume root (default /ipc)

set -euo pipefail

IPC_DIR="${SYMPOZIUM_IPC_DIR:-/ipc}"
TOOLS_DIR="$IPC_DIR/tools"
POLL_INTERVAL=0.2  # seconds

# positive_int <value> <fallback> prints value if it is a positive integer.
positive_int() {
    if [[ "$1" =~ ^[0-9]+$ ]] && [[ "$1" -ge 1 ]]; then echo "$1"; else echo "$2"; fi
}
DEFAULT_TIMEOUT=$(positive_int "${TOOL_EXECUTOR_DEFAULT_TIMEOUT:-30}" 30)
MAX_TIMEOUT=$(positive_int "${TOOL_EXECUTOR_MAX_TIMEOUT:-120}" 120)

mkdir -p "$TOOLS_DIR"

echo "[tool-executor] started, watching $TOOLS_DIR for exec requests"

process_request() {
    local req_file="$1"
    local basename
    basename=$(basename "$req_file")

    # Extract the request ID from the filename: exec-request-{id}.json
    local id
    id="${basename#exec-request-}"
    id="${id%.json}"

    local result_file="$TOOLS_DIR/exec-result-${id}.json"

    # Parse the JSON request using jq.
    local command args workdir timeout_sec
    command=$(jq -r '.command // ""' "$req_file" 2>/dev/null) || return
    args=$(jq -r '(.args // []) | join(" ")' "$req_file" 2>/dev/null) || return
    workdir=$(jq -r '.workDir // "/workspace"' "$req_file" 2>/dev/null) || return
    timeout_sec=$(jq -r ".timeout // $DEFAULT_TIMEOUT" "$req_file" 2>/dev/null) || return

    # Sanitize timeout.
    if [[ "$timeout_sec" -lt 1 ]]; then timeout_sec=$DEFAULT_TIMEOUT; fi
    if [[ "$timeout_sec" -gt "$MAX_TIMEOUT" ]]; then timeout_sec=$MAX_TIMEOUT; fi

    # Execute the command, capturing stdout/stderr and exit code.
    local stdout="" stderr="" exit_code=0 timed_out="false"
    local tmp_stdout tmp_stderr
    tmp_stdout=$(mktemp)
    tmp_stderr=$(mktemp)

    cd "$workdir" 2>/dev/null || cd /

    local started=$SECONDS

    # Native sidecar tools (argv mode): when .argv is a non-empty array, execute
    # it directly as an argument vector with NO shell, so argument values cannot
    # inject shell syntax. Optional .stdin is piped to the process. Otherwise
    # fall back to the legacy shell command path used by execute_command.
    local argv_len
    argv_len=$(jq -r '(.argv // []) | length' "$req_file" 2>/dev/null || echo 0)

    if [[ "$argv_len" -gt 0 ]]; then
        local cmd_argv=()
        # Decode argv elements via base64 so a value containing newlines (or any
        # bytes) stays a single argv element. Plain `jq -r '.argv[]'` is newline-
        # joined and would split a multi-line value into several arguments.
        while IFS= read -r _b64; do cmd_argv+=("$(printf '%s' "$_b64" | base64 -d)"); done < <(jq -r '.argv[] | @base64' "$req_file")
        local has_stdin
        has_stdin=$(jq -r 'if (.stdin // "") == "" then "0" else "1" end' "$req_file" 2>/dev/null || echo 0)
        echo "[tool-executor] exec-argv [$id]: ${cmd_argv[*]} (timeout=${timeout_sec}s, workdir=${workdir})"
        if [[ "$has_stdin" == "1" ]]; then
            if jq -r '.stdin' "$req_file" | timeout "$timeout_sec" "${cmd_argv[@]}" >"$tmp_stdout" 2>"$tmp_stderr"; then
                exit_code=0
            else
                exit_code=$?
            fi
        else
            if timeout "$timeout_sec" "${cmd_argv[@]}" </dev/null >"$tmp_stdout" 2>"$tmp_stderr"; then
                exit_code=0
            else
                exit_code=$?
            fi
        fi
    else
        # Legacy shell mode: build the full command and run it under bash -c so
        # execute_command keeps pipes, redirects and globs.
        local full_cmd="$command"
        if [[ -n "$args" ]]; then
            full_cmd="$command $args"
        fi

        echo "[tool-executor] exec [$id]: $full_cmd (timeout=${timeout_sec}s, workdir=${workdir})"

        if timeout "$timeout_sec" bash -c "$full_cmd" >"$tmp_stdout" 2>"$tmp_stderr"; then
            exit_code=0
        else
            exit_code=$?
        fi
    fi

    # coreutils timeout exits 124 when it kills the command. busybox timeout (the
    # one in Alpine images) exits 143, the SIGTERM status, so count a 143 as a
    # timeout once the time limit has passed.
    if [[ $exit_code -eq 124 ]] || { [[ $exit_code -eq 143 ]] && (( SECONDS - started >= timeout_sec )); }; then
        timed_out="true"
    fi

    stdout=$(cat "$tmp_stdout")
    stderr=$(cat "$tmp_stderr")
    rm -f "$tmp_stdout" "$tmp_stderr"

    # Truncate output if too large (50KB limit per field).
    if [[ ${#stdout} -gt 51200 ]]; then
        stdout="${stdout:0:51200}...(truncated)"
    fi
    if [[ ${#stderr} -gt 51200 ]]; then
        stderr="${stderr:0:51200}...(truncated)"
    fi

    # Write the result JSON. Use jq to properly escape strings.
    # Write to a temp file first then atomically rename so the agent
    # never reads a partially-written result.
    local tmp_result="${result_file}.tmp"
    jq -n \
        --arg id "$id" \
        --argjson exitCode "$exit_code" \
        --arg stdout "$stdout" \
        --arg stderr "$stderr" \
        --argjson timedOut "$timed_out" \
        '{id: $id, exitCode: $exitCode, stdout: $stdout, stderr: $stderr, timedOut: $timedOut}' \
        > "$tmp_result"
    mv "$tmp_result" "$result_file"

    echo "[tool-executor] done [$id]: exit=$exit_code timed_out=$timed_out"
}

# Main loop: poll for new request files.
while true; do
    # Exit when the agent signals it is done.
    if [[ -f "$IPC_DIR/done" ]]; then
        echo "[tool-executor] agent done, exiting"
        exit 0
    fi

    for req_file in "$TOOLS_DIR"/exec-request-*.json; do
        # Skip if no matches (glob didn't expand).
        [[ -e "$req_file" ]] || continue

        # Check if this request has already been processed.
        local_basename=$(basename "$req_file")
        local_id="${local_basename#exec-request-}"
        local_id="${local_id%.json}"
        result_file="$TOOLS_DIR/exec-result-${local_id}.json"

        if [[ -e "$result_file" ]]; then
            # Already processed, skip.
            continue
        fi

        # Target-based routing: if the request specifies a target, only the
        # sidecar whose SYMPOZIUM_SKILL_PACK env matches may claim it. An
        # empty target preserves legacy behavior (any sidecar may claim).
        # Comparison is case-insensitive and whitespace-trimmed for safety.
        if [[ -n "${SYMPOZIUM_SKILL_PACK:-}" ]]; then
            req_target=$(jq -r '.target // ""' "$req_file" 2>/dev/null || echo "")
            req_target_norm=$(printf '%s' "$req_target" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
            self_norm=$(printf '%s' "$SYMPOZIUM_SKILL_PACK" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
            if [[ -n "$req_target_norm" && "$req_target_norm" != "$self_norm" ]]; then
                continue
            fi
        fi

        # Atomically claim this request to prevent duplicate processing.
        # mkdir is atomic on POSIX filesystems — only one process wins.
        claim_dir="$TOOLS_DIR/.claim-${local_id}"
        if ! mkdir "$claim_dir" 2>/dev/null; then
            # Another iteration already claimed it, skip.
            continue
        fi

        process_request "$req_file" &
    done

    sleep "$POLL_INTERVAL"
done
