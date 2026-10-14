#!/bin/sh
# Renders brand/og.html to public/og.png (1200×630) with headless Chrome.
set -e
cd "$(dirname "$0")/.."
chrome="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
[ -x "$chrome" ] || { echo "Google Chrome not found at $chrome" >&2; exit 1; }
"$chrome" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
  --window-size=1200,630 --screenshot="$PWD/public/og.png" "file://$PWD/brand/og.html" 2>/dev/null
echo "wrote public/og.png"
