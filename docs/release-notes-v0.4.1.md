# Hesper v0.4.1

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
v0.4.0, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **`hesperd --version` reports the release.** It printed `dev` in v0.4.0;
  the release build now passes its tag to hesperd, and the app takes its
  version from it too.
- **Setup keeps the daemon's flags.** `hesper-setup.sh` kept only
  `--allow-shell`; it now keeps every `hesperd serve` flag, including
  `--allow-forward PORTS`, and saves them in
  `~/.local/state/hesper/serve-flags`, because `brew upgrade` deletes the
  LaunchAgent plist. To change them, edit `ProgramArguments` in
  `~/Library/LaunchAgents/de.olezierau.hesperd.plist` and run setup again.
- **Setup restarts the daemon reliably.** It no longer starts the new
  daemon before launchd has removed the old one, which could fail with
  "Input/output error" and leave hesperd stopped.

**Upgrading from v0.4.0:** v0.4.0's uninstall step deletes the plist before
v0.4.1's setup can save the flags, so if hesperd ran with `--allow-shell` or
`--allow-forward`, add them to `ProgramArguments` once more after this
upgrade and run setup again. Later upgrades keep them.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.4.1-arm64.zip` and
`Hesper-v0.4.1-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
