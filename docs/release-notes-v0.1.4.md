# Hesper v0.1.4

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

Run `/hooks` once in Codex to enable its trusted hooks. To upgrade, run
`brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then the setup
command again. Use the app path printed by Homebrew if using `--appdir`.

## Included in v0.1.4

- Native agent wall and attention controls for Claude Code, Codex and shells.
- Basic moves between Macs and Move Undo.
- History transfer/fork and Claude ↔ Codex handoff.
- A fix for a scan/resume race that could classify a Hesper-owned history
  session as external.

Checkpoint-based moves, live-agent forks, improved preflight, folder transfer
on launch, scratch projects and native Review/diffs are development features
on `main`; they are not included in this release.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.1.4-arm64.zip` and
`Hesper-v0.1.4-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
