#!/bin/bash
# Builds GhosttyKit.xcframework (libghostty, macOS arm64 + x86_64) from source
# without GNU gettext's libintl, into app/Vendor/GhosttyKit.xcframework.
# `make -C app build` links that one instead of the prebuilt libghostty-spm
# framework whenever it exists (app/Makefile, app/Package.swift); release
# builds use it, so the Hesper.app we ship contains no LGPL code
# (docs/licensing.md).
#
#   scripts/build-ghosttykit.sh              build, verify, install
#   scripts/build-ghosttykit.sh --check F    verify a framework or a linked
#                                            binary (e.g. Hesper.app's) has
#                                            no libintl symbols
#   scripts/build-ghosttykit.sh --install F  install another GhosttyKit
#                                            .xcframework as app/Vendor's
#                                            (e.g. one with your own libintl)
#
# Uses the libghostty-spm revision pinned in app/Package.resolved (its build
# scripts and patch stack, and the Ghostty commit in its Ghostty.ref), plus
# scripts/ghosttykit/*.sh, with Ghostty's -Di18n=false. Needs Zig $ZIG_VERSION,
# git, python3 and Xcode; network for the sources and Zig packages.
# GHOSTTYKIT_WORK (default app/.build/ghosttykit) holds sources and caches.
set -euo pipefail

ZIG_VERSION=0.16.0 # libghostty-spm's CI (.github/workflows/build.yml)

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/app/Vendor/GhosttyKit.xcframework"

# Symbols only libintl defines or uses. Ghostty with i18n calls bindtextdomain
# and dgettext; libintl's own objects define libintl_* and _nl_*.
intl_pattern='(_|\b)(libintl_[A-Za-z_]+|bindtextdomain|bind_textdomain_codeset|textdomain|dgettext|dcgettext|dngettext|dcngettext|_nl_[A-Za-z_]+)\b'

check() {
    local target=$1 files=()
    if [ -d "$target" ]; then
        while IFS= read -r f; do files+=("$f"); done < <(find "$target" -name '*.a' -type f)
    else
        files=("$target")
    fi
    [ ${#files[@]} -gt 0 ] || { echo "[-] nothing to check in $target" >&2; return 1; }
    local f hits
    for f in "${files[@]}"; do
        hits=$(nm -A "$f" 2>/dev/null | grep -E "$intl_pattern" || true)
        if [ -n "$hits" ]; then
            echo "[-] $f contains libintl symbols:" >&2
            echo "$hits" | head -20 >&2
            return 1
        fi
        echo "[+] no libintl symbols: $f"
    done
}

# install <xcframework> <provenance text>: copy to app/Vendor. The library is
# renamed: SwiftPM copies every binary target's library into the build
# directory under its file name, used or not, so the prebuilt framework's
# libghostty.a would overwrite this one there.
install_framework() {
    local src=$1 provenance=$2
    [ -f "$src/Info.plist" ] || { echo "[-] $src is not an .xcframework" >&2; return 1; }
    rm -rf "$out" && mkdir -p "$(dirname "$out")"
    cp -R "$src" "$out"
    python3 -I - "$out" <<'PY'
import plistlib, sys
from pathlib import Path

fw = Path(sys.argv[1])
info = fw / "Info.plist"
plist = plistlib.loads(info.read_bytes())
for lib in plist["AvailableLibraries"]:
    if lib["LibraryPath"] == "libghostty.a":
        d = fw / lib["LibraryIdentifier"]
        (d / "libghostty.a").rename(d / "libghostty-vendor.a")
        lib["LibraryPath"] = "libghostty-vendor.a"
        if "BinaryPath" in lib:
            lib["BinaryPath"] = "libghostty-vendor.a"
info.write_bytes(plistlib.dumps(plist))
PY
    printf '%s\n' "$provenance" >"$(dirname "$out")/GhosttyKit.source"
    echo "[+] $out (make -C app build links it; remove app/Vendor to go back to the prebuilt framework)"
}

case "${1:-}" in
    --check)
        check "${2:?--check needs a framework or binary}"
        exit
        ;;
    --install)
        install_framework "${2:?--install needs an .xcframework}" "installed from $2"
        exit
        ;;
    "") ;;
    *)
        sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
        exit 2
        ;;
esac

command -v zig >/dev/null || { echo "[-] zig $ZIG_VERSION not found (https://ziglang.org/download/)" >&2; exit 1; }
[ "$(zig version)" = "$ZIG_VERSION" ] || { echo "[-] zig $(zig version) found, libghostty-spm builds with $ZIG_VERSION" >&2; exit 1; }

rev=$(python3 -I -c 'import json,sys
pins = json.load(open(sys.argv[1]))["pins"]
print(next(p["state"]["revision"] for p in pins if p["identity"] == "libghostty-spm"))' "$root/app/Package.resolved")

work=${GHOSTTYKIT_WORK:-$root/app/.build/ghosttykit}
spm="$work/libghostty-spm-$rev"
if [ ! -f "$spm/.root" ]; then
    echo "[*] libghostty-spm $rev"
    rm -rf "$spm" && mkdir -p "$spm"
    curl -fsSL "https://github.com/Lakr233/libghostty-spm/archive/$rev.tar.gz" | tar xz -C "$spm" --strip-components=1
fi
cp "$root"/scripts/ghosttykit/*.sh "$spm/Patches/ghostty/"
ghostty_ref=$(tr -d '[:space:]' <"$spm/Ghostty.ref")
echo "[*] Ghostty $ghostty_ref, -Di18n=false"

(cd "$spm" && ZIG_BUILD_EXTRA_ARGS="-Di18n=false ${ZIG_BUILD_EXTRA_ARGS:-}" \
    ./build.sh --ref "$ghostty_ref" --platforms macos --skip-tests)

check "$spm/BinaryTarget/GhosttyKit.xcframework"
install_framework "$spm/BinaryTarget/GhosttyKit.xcframework" "libghostty-spm $rev
ghostty $ghostty_ref
zig $ZIG_VERSION
-Di18n=false, scripts/ghosttykit/*.sh: no libintl"
