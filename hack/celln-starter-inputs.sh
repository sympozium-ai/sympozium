#!/usr/bin/env bash
# Print the fingerprint of everything in this repository that decides what the
# default Celln starter package is: `sha256:<hex>`.
#
#   hack/celln-starter-inputs.sh [--tool-image NAME]... [--manifest]
#
# A release whose fingerprint equals the previous release's republishes that
# release's package identity instead of building a new one, so an upgrade does
# not replace the fleet's package (and end every live parent) for nothing.
#
# The fingerprint covers, in this order:
#   1. the pinned Celln release (config/celln/release.json: version, archiveSHA256),
#   2. hack/build-celln-starter.sh, which holds the packaging recipe and the
#      default tool images,
#   3. the --tool-image names the build is given, in the order given (none
#      means the build script's own default, which 2 already covers).
# It deliberately leaves out the guest kernel and the signing seed: both differ
# on every build without the package meaning anything different. Pass the same
# --tool-image arguments the build gets. --manifest prints what is hashed.
set -euo pipefail
tool_images=() manifest=0
while [ $# -gt 0 ]; do
	case "$1" in
	--tool-image) [ $# -ge 2 ] || { echo "--tool-image needs a name" >&2; exit 2; }; tool_images+=("$2"); shift 2 ;;
	--manifest) manifest=1; shift ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pin="$repo/config/celln/release.json"
recipe="$repo/hack/build-celln-starter.sh"
[ -r "$pin" ] || { echo "no Celln pin at $pin" >&2; exit 1; }
[ -r "$recipe" ] || { echo "no build script at $recipe" >&2; exit 1; }
read_pin() {
	python3 - "$pin" "$1" <<'PY'
import json, re, sys
value = json.load(open(sys.argv[1])).get(sys.argv[2])
if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9._-]+", value):
    sys.exit(f"{sys.argv[1]}: {sys.argv[2]} is missing or malformed")
print(value)
PY
}
version="$(read_pin version)"
archive="$(read_pin archiveSHA256)"
describe() {
	printf 'celln-starter-inputs/v1\n'
	printf 'celln.version=%s\n' "$version"
	printf 'celln.archiveSHA256=%s\n' "$archive"
	printf 'file hack/build-celln-starter.sh sha256:%s\n' "$(sha256sum <"$recipe" | cut -d' ' -f1)"
	if [ ${#tool_images[@]} -eq 0 ]; then
		printf 'tool-images default\n'
	else
		for name in "${tool_images[@]}"; do printf 'tool-image %s\n' "$name"; done
	fi
}
if [ "$manifest" = 1 ]; then
	describe
else
	printf 'sha256:%s\n' "$(describe | sha256sum | cut -d' ' -f1)"
fi
