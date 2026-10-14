# Hesper v0.3.2

Signed and notarized macOS release for Apple Silicon and Intel, requiring
macOS 14 or later. Install Claude Code or Codex separately. No Xcode, Go,
GitHub account, SSH access or GitHub token is needed for binary installation.

## Install with Homebrew

```sh
brew tap derzierau/hesper https://github.com/derzierau/hesper.git
brew install --cask derzierau/hesper/hesper
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
open /Applications/Hesper.app
```

Run `/hooks` once in Codex to enable its trusted hooks. To upgrade from
v0.3.1, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **One click puts you in a tile.** A click on a live tile on the wall makes
  it active straight away, so you can type, click and scroll in its
  terminal. It used to take two clicks, and a quick second click opened the
  full-size view instead. A tile waiting for an approval, or a question with
  answers, still selects on the first click so ⏎ / A / N answer it.
- **Nothing done right after the click is lost.** Clicks and scrolling in
  the moment before the tile's terminal takes over are kept and replayed
  into it.
- **Double-click always opens the full-size view** on a tile that wasn't
  active, and a click into Hesper while it is in the background acts at
  once.
- **⌃⌥ + arrow moves and types.** ⌃⌥ ← → ↑ ↓ go to the neighbouring tile and
  make it active, also from inside an active tile, where plain arrows belong
  to the agent. Plain arrows still only move the selection on the wall.

After a click the keyboard belongs to the agent; ⌘esc gives the wall its
arrow keys back.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.3.2-arm64.zip` and
`Hesper-v0.3.2-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
