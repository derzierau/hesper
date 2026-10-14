# Hesper v0.3.3

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
v0.3.2, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## Changes

- **Project bands show up whenever agents run in more than one project.**
  With Automatic grouping, a wall used to group by project group as soon as
  one group existed, and put every project outside a group into a single
  "Other projects" band. When all agents landed there, or all in one group,
  the wall drew no bands at all and only the project tag on each tile was
  left. Automatic now goes down a level, from groups to projects to
  branches, until the agents split into two or more bands.
- **A project outside any group is a band of its own** next to the groups,
  with its own name, color, ＋ and ↗, instead of sharing "Other projects".
- **The Needs you and Working filters keep the bands.** The grouping is
  decided before the filter, so switching it narrows the cards in each band
  instead of regrouping the wall.
- The layout popover's Automatic row names the level in use ("now by
  project"). A grouping you chose yourself (By group, By project, None) is
  unchanged.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.3.3-arm64.zip` and
`Hesper-v0.3.3-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
