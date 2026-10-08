#!/usr/bin/env bash
# Replace the next-release placeholder in the chart's NOTES.txt with the
# version in version.txt. A note that must name "the first release that
# contains this change" is written with the placeholder, because the author
# cannot know which release that will be. The release workflow runs this on
# main right after release-please has bumped version.txt, and commits the
# result, so the placeholder becomes that release's version once and stays
# that way.
#
#   hack/stamp-release-version.sh
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
placeholder="__NEXT_RELEASE_VERSION__"
notes="charts/sympozium/templates/NOTES.txt"
version="$(tr -d '[:space:]' <"$repo/version.txt")"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version.txt: \"$version\" is not MAJOR.MINOR.PATCH" >&2
  exit 1
fi
if ! grep -qF "$placeholder" "$repo/$notes"; then
  echo "No $placeholder in $notes"
  exit 0
fi
sed -i.bak "s/$placeholder/$version/g" "$repo/$notes"
rm -f "$repo/$notes.bak"
echo "Stamped $version into $notes"
