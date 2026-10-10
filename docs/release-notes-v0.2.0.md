# Hesper v0.2.0

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
v0.1.4, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

The cross-Mac features below need v0.2.0 on both Macs. Upgrade every Mac.

## New in v0.2.0

- **Checkpoint-based moves.** Continue on another Mac (⇧⌘M, ⌘K or the tile's
  menu) checkpoints the agent's worktree (staged, unstaged and untracked work,
  never your branches or index), sends only the changes, recreates the
  worktree on the same branch and resumes the same conversation. A preflight
  asks before moving a busy agent or one whose tools are missing on the
  target. ⌘Z moves it back; Fork keeps the original and starts a twin.
  Checkpoints are also kept when an agent closes, so History can restore them.
- **Fork a running session to another Mac.** `hesperctl history fork ID
  --machine mini` and the app's Fork no longer refuse a session that is still
  running.
- **Bring the folder along.** Start a task on a Mac that does not have the
  project, and Hesper clones it from its remote with your uncommitted changes,
  or copies it whole when there is no remote.
- **Scratch projects.** "New scratch" starts a task in a temporary Git project
  under `~/scratch`, which you can keep, archive, restore, delete or promote to
  a GitHub repository.
- **Review inbox.** ⌘R opens finished agent work from every Mac with a fast
  native diff, the commands the agent ran and whether they still apply, and
  which turn last wrote each file. Accept, reject or send it back with notes.
  `hesperctl review` and the MCP tools do the same.
- **Large transfers.** Moves, forks and brought folders no longer time out on
  slow links: transfers resume where they stopped, transcripts are compressed
  and progress is shown. One `maxTransferMB` setting (default 5 GB) replaces
  the earlier size caps.
- **Direct connections with the firewall on.** A signed release that macOS
  allows automatically is now recognized, so `--direct=auto` no longer falls
  back to the relay.

## Fixes

- hesperd no longer keeps serving a socket after it was closed during startup.
- The `golang.org/x/crypto` dependency is updated to v0.52.0. No vulnerable
  code was reachable in v0.1.4.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.2.0-arm64.zip` and
`Hesper-v0.2.0-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
