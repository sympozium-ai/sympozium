#!/usr/bin/env bash
# Build, sign and publish the default Celln starter package that
# `sympozium install` pins. Used by the release workflow and the one-liner
# journey so both produce the same shape.
#
#   hack/build-celln-starter.sh --bundle DIR --kernel FILE --image REPO:TAG --out DIR
#       [--tool-image NAME]... [--signing-seed FILE] [--push] [--root DIR]
#
# Writes DIR/package (the package), DIR/inspect.json (celln starter-inspect)
# and DIR/starter.json: {"image": "REPO@sha256:…", "packageHash": "blake3:…",
# "publisher": "…", "cellnVersion": "…", "inputs": "sha256:…"} — the exact
# identity the installer embeds, plus the fingerprint of the repository inputs
# that decided it (hack/celln-starter-inputs.sh). A release whose inputs match
# the previous release's republishes that identity instead of running this
# (hack/celln-starter-reuse.sh), because every new package costs a fleet its
# live parents; anything this script starts reading from the repository as
# package content belongs in that fingerprint. The signing seed is ephemeral unless one is supplied: the package is
# trusted by its publisher key and hash, which the installer approves
# explicitly, not by who holds the seed afterwards.
set -euo pipefail
bundle="" kernel="" image="" out="" seed="" push=0 root=""
tool_images=()
while [ $# -gt 0 ]; do
	case "$1" in
	--bundle) bundle="$2"; shift 2 ;;
	--kernel) kernel="$2"; shift 2 ;;
	--image) image="$2"; shift 2 ;;
	--out) out="$2"; shift 2 ;;
	--tool-image) tool_images+=("$2"); shift 2 ;;
	--signing-seed) seed="$2"; shift 2 ;;
	--root) root="$2"; shift 2 ;;
	--push) push=1; shift ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ -n "$bundle" ] && [ -n "$kernel" ] && [ -n "$image" ] && [ -n "$out" ] || { echo "usage: $0 --bundle DIR --kernel FILE --image REPO:TAG --out DIR [--tool-image NAME]... [--push]" >&2; exit 2; }
# Fingerprinted as given: the default below is covered by this file's own hash.
inputs_args=()
for name in "${tool_images[@]}"; do inputs_args+=(--tool-image "$name"); done
inputs="$("$(dirname "${BASH_SOURCE[0]}")/celln-starter-inputs.sh" "${inputs_args[@]}")"
[ ${#tool_images[@]} -gt 0 ] || tool_images=(busybox jq)
# celln refuses relative packaging paths; callers may pass either.
mkdir -p "$out"
bundle="$(readlink -f "$bundle")" kernel="$(readlink -f "$kernel")" out="$(readlink -f "$out")"
[ -z "$seed" ] || seed="$(readlink -f "$seed")"
[ -z "$root" ] || root="$(readlink -f "$root")"
celln="$bundle/bin/celln"
[ -x "$celln" ] || { echo "no celln CLI at $celln" >&2; exit 1; }
[ -r "$kernel" ] || { echo "kernel $kernel is not readable" >&2; exit 1; }
for tool in skopeo debugfs mke2fs gcc cpio docker; do
	command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 1; }
done
[ -n "$root" ] || root="$out/celln-root"
if [ -z "$seed" ]; then
	seed="$out/publisher.seed"
	head -c 32 /dev/urandom >"$seed"
	chmod 600 "$seed"
fi
args=()
for name in "${tool_images[@]}"; do args+=(--tool-image "$name"); done
rm -rf "$out/package"
"$celln" --root "$root" starter-package --runtime-dir "$bundle/share/celln" --guest-dir "$bundle/share/celln/pilot" \
	--kernel "$kernel" --signing-key "$seed" --output "$out/package" "${args[@]}"
"$celln" starter-inspect "$out/package" >"$out/inspect.json"
# The pinned release version when the caller knows it (CI), else the CLI's own.
version="${CELLN_VERSION:-$("$celln" --version | awk '{print "v"$2}')}"
rm -rf "$out/image" && mkdir -p "$out/image" && cp -r "$out/package" "$out/image/package"
printf 'FROM scratch\nCOPY package /package\n' >"$out/image/Dockerfile"
docker build -q -t "$image" "$out/image" >/dev/null
digest=""
if [ "$push" = 1 ]; then
	docker push -q "$image" >/dev/null
	digest="$(docker inspect --format '{{index .RepoDigests 0}}' "$image" | sed 's/.*@//')"
fi
python3 - "$out/inspect.json" "$image" "$digest" "$version" "$inputs" >"$out/starter.json" <<'PY'
import json, sys
inspect, image, digest, version, inputs = sys.argv[1:]
report = json.load(open(inspect))
def publishers(o):
    if isinstance(o, dict):
        for k, v in o.items():
            if k == "publisher" and isinstance(v, str):
                yield v
            yield from publishers(v)
    elif isinstance(o, list):
        for i in o:
            yield from publishers(i)
keys = sorted(set(publishers(report)))
assert len(keys) == 1, f"one publisher expected, found {keys}"
repo = image.rsplit(":", 1)[0] if "@" not in image else image.split("@", 1)[0]
print(json.dumps({"image": f"{repo}@{digest}" if digest else "", "packageHash": report["packageHash"], "publisher": keys[0], "cellnVersion": version, "inputs": inputs}, indent=2))
PY
cat "$out/starter.json"
