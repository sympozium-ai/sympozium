#!/usr/bin/env bash
# Build what the Celln fleet journeys need, from this checkout and the pinned
# Celln release (config/celln/release.json), the way the release does:
#   - the Celln bundle (bin/celln + share/celln), sha256-checked, into
#     $CELLN_LANE_WORK/bundle;
#   - the Celln image pulled by its pinned digest and tagged celln:$TAG;
#   - the controller, apiserver, webhook, node-probe, model-gateway and
#     celln-installer images built with TAG.
# Prints the images it produced (one per line) to $CELLN_LANE_WORK/images.
#
#   TAG=celln-lane-123 CELLN_LANE_WORK=/tmp/x hack/celln-lane/prepare.sh
set -euo pipefail
repo="$(cd "$(dirname "$0")/../.." && pwd)"
: "${TAG:?TAG names the image tag the journeys load}"
work="${CELLN_LANE_WORK:?CELLN_LANE_WORK is the lane work directory}"
registry="${REGISTRY:-ghcr.io/sympozium-ai/sympozium}"
pin="$repo/config/celln/release.json"
version="$(jq -er '.version | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))' "$pin")"
archive_sha="$(jq -er '.archiveSHA256 | select(test("^[a-f0-9]{64}$"))' "$pin")"
image_digest="$(jq -er '.imageDigest | select(test("^sha256:[a-f0-9]{64}$"))' "$pin")"
mkdir -p "$work/bundle"

echo "---- Celln $version bundle"
curl --fail --location --silent --show-error --retry 3 \
	"https://github.com/sympozium-ai/celln/releases/download/${version}/celln-x86_64-unknown-linux-musl.tar.gz" -o "$work/celln.tar.gz"
printf '%s  %s\n' "$archive_sha" "$work/celln.tar.gz" | sha256sum --check --quiet
tar -xzf "$work/celln.tar.gz" -C "$work/bundle"
[ -x "$work/bundle/bin/celln" ] || { echo "bundle has no bin/celln" >&2; exit 1; }

echo "---- Celln image $image_digest"
docker pull -q "ghcr.io/sympozium-ai/celln@$image_digest" >/dev/null
docker tag "ghcr.io/sympozium-ai/celln@$image_digest" "celln:$TAG"

echo "---- Sympozium images (TAG=$TAG)"
cd "$repo"
for name in controller apiserver webhook node-probe model-gateway; do
	make --no-print-directory "docker-build-$name" TAG="$TAG" REGISTRY="$registry"
done
make --no-print-directory docker-build-celln-installer TAG="$TAG" REGISTRY="$registry"
{
	echo "celln:$TAG"
	for name in controller apiserver webhook node-probe model-gateway celln-installer; do echo "$registry/$name:$TAG"; done
} >"$work/images"
echo "images:"
cat "$work/images"
