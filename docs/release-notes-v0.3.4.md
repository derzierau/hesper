# Hesper v0.3.4

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
v0.3.3, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **Agents name their own tiles.** A Claude or Codex agent names its tile
  after what it works on, in 2 to 5 words and in your language, once it
  understands the task. It renames the tile when the work moves on, for
  example from investigating to fixing, and never for each step. Agents
  learn this from Hesper's session-start hook, so it works for every task,
  not only ones that mention Hesper. Sessions already running pick it up
  at their next start, `/clear` or compaction.
- **A name you give stays.** Rename a tile yourself and agents keep your
  name, also after a restart or a move to another Mac. The tile menu's
  **Let the Agent Name It** hands naming back.
- **`hesperctl rename self NAME`** renames the agent the command runs in;
  it is the only change an agent may make to itself. `self` works as the
  ID of any command. `hesperctl rename ID --auto` hands a name back to
  the agent.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.3.4-arm64.zip` and
`Hesper-v0.3.4-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
