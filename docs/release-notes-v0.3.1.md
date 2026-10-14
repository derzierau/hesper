# Hesper v0.3.1

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
v0.3.0, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Fixes

- **Claude Code's `/voice` works in Hesper.** Agents run under hesperd, so
  macOS asks Hesper for microphone access on their behalf. Hesper did not
  declare microphone use, so macOS ended any agent that opened the
  microphone instead of asking. Hesper now declares it, and the first
  `/voice` in an agent shows the macOS microphone prompt. Allow it, or turn
  it on later in System Settings ▸ Privacy & Security ▸ Microphone (listed
  as Hesper or hesperd). Voice needs a claude.ai login in Claude Code.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.3.1-arm64.zip` and
`Hesper-v0.3.1-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
