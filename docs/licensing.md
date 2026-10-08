# Licensing: libghostty and GNU libintl

Hesper is MIT-licensed; every third-party component and its license is in
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md). One of them is under
the LGPL: GNU gettext's runtime library, **libintl** (LGPL-2.1-or-later).
Ghostty bundles it on Apple platforms for message translation and links it
statically into libghostty, which Hesper.app links statically too. Hesper
uses none of Ghostty's translations.

## Which Hesper.app contains libintl

| Build | libghostty | libintl in Hesper.app |
|---|---|---|
| GitHub release (`.github/workflows/release.yml`) | built from source, `-Di18n=false`, libintl not linked | no |
| `install.sh` / `make -C app build` by default | prebuilt `GhosttyKit.xcframework` from [libghostty-spm](https://github.com/Lakr233/libghostty-spm), pinned in `app/Package.swift` / `app/Package.resolved` | yes |
| `make -C app build` after `scripts/build-ghosttykit.sh` | built from source, as in releases | no |

`make -C app build` (and so `install.sh`) links `app/Vendor/GhosttyKit.xcframework`
when it exists (git-ignored): the Makefile passes it to `app/Package.swift`
as `HESPER_GHOSTTYKIT`. Without it, the prebuilt one. Delete `app/Vendor/`
to go back. A plain `swift build` uses the prebuilt one unless you set
`HESPER_GHOSTTYKIT=Vendor/GhosttyKit.xcframework` yourself.

Check any build:

```sh
scripts/build-ghosttykit.sh --check app/build/Hesper.app/Contents/MacOS/Hesper
```

## Building libghostty without libintl

```sh
# Zig 0.16.0 (what libghostty-spm builds with), git, python3, Xcode 26
scripts/build-ghosttykit.sh      # ~10 min; writes app/Vendor/GhosttyKit.xcframework
make -C app build                # or ./install.sh
scripts/build-ghosttykit.sh --check app/build/Hesper.app/Contents/MacOS/Hesper
```

The script takes the libghostty-spm revision pinned in
`app/Package.resolved`, the Ghostty commit in that revision's `Ghostty.ref`
and libghostty-spm's patch stack, adds `scripts/ghosttykit/*.sh` (Ghostty
links libintl only when i18n is on) and builds the macOS arm64 + x86_64
framework with `-Di18n=false`. It fails if the result contains a libintl
symbol. With i18n off, Ghostty's `_()` returns the English message and
locale names are passed to `setlocale` as macOS reports them instead of
through gnulib's canonicalization. Hesper ships no Ghostty translations,
and its agents get their environment from `hesperd`, not from Hesper.app.

## Replacing libintl in a build that contains it (LGPL-2.1 §6)

A Hesper.app built with the prebuilt framework contains libintl. Everything
needed to rebuild it with a modified libintl and relink Hesper is public:

- Hesper: this repository, at the commit you built.
- libghostty-spm: the revision in `app/Package.resolved`
  (`7199bd7aa3c32d0a5edcab7fe87b3c1e76f76de9` for 2.2.2026100501), with its
  build scripts and patches.
- Ghostty: the commit in that revision's `Ghostty.ref`
  (`35a81a980bb9fce09a1ea762a68b55f8eb3477ed`).
- libintl: gettext 0.24, which Ghostty's `pkg/libintl/build.zig.zon` fetches
  from `https://deps.files.ghostty.org/gettext-0.24.tar.gz` and builds with
  `pkg/libintl/build.zig`.

To relink with your own libintl:

```sh
cd hesper                                   # your Hesper checkout
rev=$(python3 -c 'import json; print(next(p["state"]["revision"] for p in json.load(open("app/Package.resolved"))["pins"] if p["identity"] == "libghostty-spm"))')
mkdir -p build/relink && cd build/relink
curl -fsSL "https://github.com/Lakr233/libghostty-spm/archive/$rev.tar.gz" | tar xz
cd "libghostty-spm-$rev"
git clone https://github.com/ghostty-org/ghostty References/ghostty-upstream
git -C References/ghostty-upstream checkout "$(cat Ghostty.ref)"

# Use your gettext: in References/ghostty-upstream/pkg/libintl/build.zig.zon
# replace the gettext dependency's .url and .hash with
#     .path = "/absolute/path/to/your/gettext",
# (a gettext source tree; pkg/libintl/build.zig compiles gettext-runtime/intl).

./build.sh --platforms macos --skip-tests   # Zig 0.16.0
cd ../../..
scripts/build-ghosttykit.sh --install "build/relink/libghostty-spm-$rev/BinaryTarget/GhosttyKit.xcframework"
make -C app build                           # relinks Hesper.app with it
nm app/build/Hesper.app/Contents/MacOS/Hesper | grep libintl_   # your libintl
```

`--install` copies the framework to `app/Vendor/` (renaming its library so
SwiftPM cannot mix it up with the prebuilt one). `make -C app build` signs
ad hoc; `./install.sh --skip-build` installs that build, or run
`./install.sh` (it builds the same way). Hesper links libghostty only
through `app/Package.swift`, so any framework with the same
`include/ghostty.h` API links unchanged.
