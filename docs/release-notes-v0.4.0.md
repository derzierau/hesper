# Hesper v0.4.0

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
v0.3.4, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **Port forwarding between machines.** Open a port on another machine's
  loopback as `localhost` on this one, for example a dev server's
  `localhost:3000` in your browser: `hesperctl forwards add dev:3000`,
  `hesperctl forwards ls`, `hesperctl forwards rm dev:3000`. It runs over
  the existing end-to-end encrypted link, through the relay or directly on
  the same network; the relay does not change and cannot read it. The
  machine you forward to opts in with `hesperd serve --allow-forward PORTS`
  (`3000,5173`, a range or `all`), connects only to its own loopback, and
  serves only devices it approved with the `shell` right. Forwards survive
  a daemon restart. Agents may list forwards but not add them. Both
  machines need v0.4.0.
- **Settings fit their window.** Settings keeps one size on every pane,
  the sidebar shows the pane names, and rows that do not fit stack under
  their label. One font size setting drives the wall tiles; the focused
  agent and agent windows keep your Ghostty font size.
- **Questions pulse too.** Approvals and questions pulse, errors stay
  steady, and the menu-bar item blinks while something waits for you
  (not under Reduce Motion).
- **Double-click the focus header** to go back to the wall, as a
  double-click on a tile opens the focus view.
- **Lane titles stay in view.** In a wide group on the Columns wall, the
  group's title stays at the left edge while its cards scroll past.
- **Fixes.** Starting a new agent's chat no longer jumps the wall back to
  the first column, and the first letter of Claude Code's suggested reply
  no longer disappears in wall tiles.

Thanks to [@pippo-wtf](https://github.com/pippo-wtf) for the Settings,
wall and focus changes.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.4.0-arm64.zip` and
`Hesper-v0.4.0-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
