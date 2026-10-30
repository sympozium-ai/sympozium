#!/usr/bin/env bash
# Gather the Celln KVM lane's evidence into one directory and tarball:
#   versions (host kernel, kind, kubectl, docker, Go, Calico, node image),
#   the Sympozium commit and the pinned Celln release, image digests,
#   the starter package identity, per-step timings and exit codes,
#   NetworkPolicy probe results, three-way acceptance timings, and the
#   journey logs.
#
#   hack/celln-lane/collect-evidence.sh <evidence-dir> [journey-work-dir...]
#
# <evidence-dir> already holds what the workflow wrote (steps.tsv, logs/*.log,
# netpol.jsonl, three-way.json). From each journey work directory only an
# allowlist is copied (logs, starter.json, the canary verdict); keys, CA
# material, kubeconfigs and the fleet output directory are never copied.
# Every copied text file is passed through a redaction filter for bearer
# tokens and API-key-shaped strings. Writes <evidence-dir>/SUMMARY.md and
# <evidence-dir>.tar.gz. Never fails the job: missing pieces are recorded.
set -uo pipefail
out="${1:?usage: $0 <evidence-dir> [journey-work-dir...]}"
shift
mkdir -p "$out/logs" "$out/journeys"
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
redact() { sed -E -e 's/(Bearer )[A-Za-z0-9._~+\/=-]+/\1[REDACTED]/g' \
	-e 's/(sk-[A-Za-z0-9_-]{4})[A-Za-z0-9_-]+/\1[REDACTED]/g' \
	-e 's/((api[_-]?key|token|password|secret)["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"']?)[^"'"'"'[:space:],}]{8,}/\1[REDACTED]/Ig'; }
for f in "$out"/logs/*.log; do [ -f "$f" ] && { redact <"$f" >"$f.tmp" && mv "$f.tmp" "$f"; }; done
for work in "$@"; do
	[ -d "$work" ] || continue
	dest="$out/journeys/$(basename "$work")"
	mkdir -p "$dest"
	for rel in install.log install-move.log install-rerun.log starter-build.log canary-deploy.log canary-verdict doctor.log starter/starter.json; do
		[ -f "$work/$rel" ] && redact <"$work/$rel" >"$dest/$(echo "$rel" | tr / _)"
	done
done
v() { "$@" 2>&1 | head -1 || true; }
{
	echo "# Celln KVM lane evidence"
	echo
	echo "| | |"
	echo "|---|---|"
	echo "| Repository | ${GITHUB_REPOSITORY:-local} |"
	echo "| Ref | ${GITHUB_REF_NAME:-$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null)} |"
	echo "| Commit | $(git -C "$repo" rev-parse HEAD 2>/dev/null) |"
	echo "| Run | ${GITHUB_SERVER_URL:-}/${GITHUB_REPOSITORY:-}/actions/runs/${GITHUB_RUN_ID:-local} (${GITHUB_EVENT_NAME:-manual}) |"
	echo "| Runner | ${RUNNER_NAME:-$(hostname)} |"
	echo "| Collected | $(date -u +%Y-%m-%dT%H:%M:%SZ) |"
	echo
	echo "## Versions"
	echo
	echo '```'
	echo "kernel:   $(uname -r)"
	echo "cpu:      $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
	echo "kind:     $(v kind version)"
	echo "kubectl:  $(v kubectl version --client)"
	echo "docker:   $(v docker version --format '{{.Server.Version}}')"
	echo "go:       $(v go version)"
	echo "node:     ${CELLN_LANE_NODE_IMAGE:-default (hack/celln-lane/kind-cluster.sh)}"
	echo "calico:   ${CELLN_LANE_CALICO_VERSION:-default (hack/celln-lane/kind-cluster.sh)}"
	echo "celln pin: $(tr -d '\n ' <"$repo/config/celln/release.json" 2>/dev/null)"
	echo '```'
	echo
	echo "## Image digests"
	echo
	echo '```'
	for img in ${CELLN_LANE_IMAGES:-}; do
		printf '%s %s\n' "$img" "$(docker image inspect -f '{{.Id}}' "$img" 2>/dev/null || echo missing)"
	done
	echo '```'
	echo
	echo "## Starter packages"
	echo
	for s in "$out"/journeys/*/starter_starter.json; do
		[ -f "$s" ] || continue
		echo "- $(basename "$(dirname "$s")"): \`$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("packageHash"), d.get("image"), d.get("publisher"))' "$s" 2>/dev/null)\`"
	done
	echo
	echo "## Steps"
	echo
	echo "| Step | Exit | Seconds |"
	echo "|---|---|---|"
	[ -f "$out/steps.tsv" ] && awk -F'\t' '{printf "| %s | %s | %s |\n", $1, $2, $3}' "$out/steps.tsv"
	echo
	echo "## NetworkPolicy probes"
	echo
	if [ -s "$out/netpol.jsonl" ]; then
		echo "| Policy | Probe | Want | Got |"
		echo "|---|---|---|---|"
		python3 -c '
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    print("| {policy} | {probe} ({from} -> {to}) | {want} | {got} |".format(**r))' "$out/netpol.jsonl"
	else
		echo "No probe results recorded."
	fi
	echo
	echo "## Three-way acceptance"
	echo
	if [ -s "$out/three-way.json" ]; then echo '```json'; cat "$out/three-way.json"; echo '```'; else echo "Not recorded."; fi
} >"$out/SUMMARY.md"
tar -czf "$out.tar.gz" -C "$(dirname "$out")" "$(basename "$out")"
echo "evidence: $out/SUMMARY.md and $out.tar.gz"
