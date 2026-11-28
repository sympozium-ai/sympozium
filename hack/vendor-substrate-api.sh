#!/usr/bin/env bash
# Vendor Agent Substrate's public API at a release tag, or check the copy.
#   hack/vendor-substrate-api.sh <tag> <substrate checkout>   re-vendor
#   hack/vendor-substrate-api.sh --check                      verify against PIN
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/third_party/substrate"
files=(ateapi.proto ateapi.pb.go ateapi_grpc.pb.go)

if [[ "${1:-}" == "--check" ]]; then
  (cd "$dest/ateapi/v1" && grep -E '^[0-9a-f]{64}  ' "$dest/PIN" | sha256sum --check --quiet --strict) \
    || { echo "ERROR: third_party/substrate differs from PIN; re-vendor with hack/vendor-substrate-api.sh" >&2; exit 1; }
  exit 0
fi

tag="${1:?usage: $0 <tag> <substrate checkout> | --check}"
src="${2:?usage: $0 <tag> <substrate checkout> | --check}"
commit="$(git -C "$src" rev-parse "$tag^{commit}")"
mkdir -p "$dest/ateapi/v1"
for f in "${files[@]}"; do
  git -C "$src" show "$tag:api/ateapi/v1/$f" > "$dest/ateapi/v1/$f"
done
git -C "$src" show "$tag:LICENSE" > "$dest/LICENSE"
{
  echo "tag: $tag"
  echo "commit: $commit"
  (cd "$dest/ateapi/v1" && sha256sum "${files[@]}")
} > "$dest/PIN"
echo "vendored $tag ($commit)"
