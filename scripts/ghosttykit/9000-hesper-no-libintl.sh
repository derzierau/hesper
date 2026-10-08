#!/bin/bash
# Hesper's patch on top of libghostty-spm's stack (scripts/build-ghosttykit.sh
# copies it into Patches/ghostty/): link GNU libintl only when Ghostty is
# built with -Di18n=true. Upstream links it on every Apple target even when
# i18n is off, so the archive would still carry LGPL code nothing calls.
set -euo pipefail

SOURCE_DIR="${1:?Usage: $0 <ghostty-source-dir>}"
FILE="$SOURCE_DIR/src/build/SharedDeps.zig"
MARKER="HESPER_NO_LIBINTL_PATCH"

if grep -q "$MARKER" "$FILE"; then
    echo "[+] hesper no-libintl patch already applied"
    exit 0
fi

python3 -I - "$FILE" "$MARKER" <<'PY'
import sys
from pathlib import Path

path, marker = Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = '        if (b.lazyDependency("libintl", .{'
new = f'        // {marker}: libintl only with i18n.\n        if (self.config.i18n) if (b.lazyDependency("libintl", .{{'
if text.count(old) != 1:
    sys.exit(f"[-] {path}: expected one libintl dependency, found {text.count(old)}")
start = text.index(old)
# The block's closing brace (same indentation) ends the outer if: needs ";".
end = text.index("\n        }\n", start) + len("\n        }")
text = text[:start] + new + text[start + len(old):end] + ";" + text[end:]
path.write_text(text)
PY
echo "[+] applied hesper no-libintl patch"
