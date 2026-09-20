#!/usr/bin/env bash
# Decide whether a release republishes the previous release's Celln starter
# package instead of building a new one.
#
#   hack/celln-starter-reuse.sh [--check-image] PREVIOUS_JSON FINGERPRINT
#
# PREVIOUS_JSON is the previous release's celln-starter.json (it need not
# exist), FINGERPRINT the output of hack/celln-starter-inputs.sh for this
# release. Exit 0 means reuse: the identity to publish is written to stdout,
# holding only the fields the release carries. Exit 1 means build, with the
# reason on stderr. --check-image also requires the previous image to still be
# pullable, asking the registry with `docker buildx imagetools inspect`.
#
# Reuse is only ever offered for a complete, well-formed identity whose
# recorded inputs equal FINGERPRINT; anything else builds.
set -euo pipefail
check_image=0
if [ "${1:-}" = "--check-image" ]; then check_image=1; shift; fi
[ $# -eq 2 ] || { echo "usage: $0 [--check-image] PREVIOUS_JSON FINGERPRINT" >&2; exit 2; }
previous="$1" fingerprint="$2"
[[ "$fingerprint" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "fingerprint '$fingerprint' is not sha256:<hex>" >&2; exit 2; }
build() { echo "building: $*" >&2; exit 1; }
[ -s "$previous" ] || build "no previous release published a celln-starter.json"
identity="$(python3 - "$previous" "$fingerprint" <<'PY'
import json, re, sys
path, fingerprint = sys.argv[1:]
def build(reason):
    print(f"building: {reason}", file=sys.stderr)
    sys.exit(1)
try:
    previous = json.load(open(path))
except ValueError as err:
    build(f"the previous celln-starter.json is not JSON ({err})")
if not isinstance(previous, dict):
    build("the previous celln-starter.json is not an object")
inputs = previous.get("inputs")
if not inputs:
    build("the previous celln-starter.json records no inputs")
if inputs != fingerprint:
    build(f"package inputs changed ({inputs} -> {fingerprint})")
shapes = {
    "image": r"[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}",
    "packageHash": r"blake3:[a-f0-9]{64}",
    "publisher": r"[^\s,=]+",
    "cellnVersion": r"[A-Za-z0-9._-]+",
}
for field, shape in shapes.items():
    value = previous.get(field)
    if not isinstance(value, str) or not re.fullmatch(shape, value):
        build(f"the previous celln-starter.json has no usable {field}")
print(json.dumps({field: previous[field] for field in [*shapes, "inputs"]}, indent=2))
PY
)" || exit 1
if [ "$check_image" = 1 ]; then
	image="$(printf '%s' "$identity" | python3 -c 'import json, sys; print(json.load(sys.stdin)["image"])')"
	docker buildx imagetools inspect "$image" >/dev/null 2>&1 || build "the previous package image $image is no longer pullable"
fi
printf '%s\n' "$identity"
