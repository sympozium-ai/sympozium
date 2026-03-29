#!/bin/bash
# Move downloaded screenshots to docs/assets/screenshots/
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$SCRIPT_DIR/docs/assets/screenshots"
mkdir -p "$DEST"

for f in dashboard gateway mcp-servers instances persona-packs policies runs schedules skills; do
  src="$HOME/Downloads/$f.png"
  if [ -f "$src" ]; then
    mv "$src" "$DEST/$f.png"
    echo "Moved $f.png"
  else
    echo "Not found: $src"
  fi
done
echo "Done! Screenshots are in $DEST"
