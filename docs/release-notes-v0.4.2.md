# Hesper v0.4.2

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
v0.4.1, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **A host stays reachable after its replay cache grows.** hesperd's replay
  cache (`nonces.log`) was never rewritten, so after weeks of use it grew
  past 4 MiB. After the next restart hesperd refused every request from
  other Macs and the iPhone, with `replay cache: … is too large`. It now
  reads a large cache, keeps only the live entries and rewrites it, so a
  host in that state recovers once it runs v0.4.2.
- **Large history pages no longer drop the host's relay session.** A full
  `sessions.pull` page grew past the relay's 1 MiB message limit once
  encrypted. The relay closed the connection and the host reconnected
  every ~40 seconds. Pages are now smaller, and an answer that would still
  be too large is refused instead of being sent.
- **`hesperctl status` says why the relay session last dropped**, and how
  often in the last five minutes.
- **Agents whose task starts with a dash start.** A task like
  `--bring app fix the bug` was passed as the Remote Control name and
  Claude Code exited with `unknown option`. The name now drops leading
  dashes.
- **klauspost/compress is updated to v1.18.7**, a security release. The
  fixed code (`s2`) is not used by Hesper.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. This release needs no
relay update. See the [README](https://github.com/derzierau/hesper#install)
for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.4.2-arm64.zip` and
`Hesper-v0.4.2-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
