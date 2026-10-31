# Signed releases and Homebrew

A stable tag (`vX.Y.Z`) builds native arm64 and x86_64 apps on GitHub's
macOS runners. Each app includes hesperd, hesperctl, hesper-keys, the agent
skill and a binary setup script. The workflow tests, signs with the hardened
runtime, notarizes, staples and checks Gatekeeper before publishing either
architecture. libghostty is built without libintl as described in licensing.md.

## Current distribution

Homebrew installs **v0.4.0**, the signed and notarized release for macOS 14+,
once its cask update is merged. Install Claude Code or Codex separately. Local
use needs no relay; multi-Mac use requires a self-hosted relay and device
enrollment.

v0.3.0 makes every action reachable by menu, pointer and shortcut from one
command table, adds a complete menu bar, a shortcuts sheet, rebinding and
accessibility support to what v0.2.0 shipped. v0.3.1 lets agents use the
microphone, so Claude Code's `/voice` works. v0.3.2 makes a wall tile
active with one click, and ⌃⌥ + arrow moves to a neighbor and types there.
v0.3.3 groups the wall by project or branch whenever its agents run in more
than one, and gives projects outside a group their own band. v0.3.4 lets
agents name their own tiles after what they work on, keeping a name you give.
v0.4.0 forwards ports from one machine's loopback to another's over the link.
The [v0.4.0 release notes](release-notes-v0.4.0.md) are the text to publish on
GitHub; editing the file later does not change notes already published.

## One-time credentials

Use personal Apple Developer team `JG9EEDCLZB` and bundle identifier
`de.olezierau.hesper.mac`. Create a **Developer ID Application** certificate,
not an Apple Development or Mac App Store certificate. Export its certificate
and private key together as a password-protected `.p12`.

Create a team App Store Connect API key with the Developer role for
notarization. Download its `.p8` once and keep it securely. Record its key ID
and issuer ID. Install these repository Actions secrets (never commit them):

| Secret | Value |
| --- | --- |
| `DEVELOPER_ID_P12` | Base64 of the `.p12` |
| `P12_PASSWORD` | Export password |
| `NOTARY_API_KEY` | Base64 of the `.p8` |
| `NOTARY_API_KEY_ID` | API key ID |
| `NOTARY_API_ISSUER` | Issuer ID |

Missing credentials fail the release rather than publishing an unsigned app.
The signing keychain and decoded files are removed after every build.
Signing secrets are used only in the release workflow, never pull-request CI.

## Release

Before publishing the next release:

- Require green app CI and relay CI on both macOS and Linux for the release
  commit. Investigate failures rather than treating macOS success as sufficient.
- Test a fresh Homebrew install and an upgrade on a disposable Mac or account,
  including the setup script, CLI paths, signatures and daemon restart.
- Start Claude and Codex sessions, handle a permission prompt, and test a move
  and Move Undo between two enrolled Macs. Check any newly released features.
- Prepare release notes with Homebrew as the primary installation method,
  prerequisites and the features actually included in the tag.
- Create a new stable `vX.Y.Z` tag only after those checks pass. Do not reuse
  v0.1.4 or any other published version.

The Linux failures of relay run [37921267092](https://github.com/derzierau/hesper/actions/runs/37921267092)
(2026-10-09) were flaky tests, fixed before v0.2.0: `TestHasTool` by #15
(hesperd stopped serving after an early close) and
`TestSessionsForkLargeOverSlowLink` by #14 (it now checks lines, not bytes).
The v0.3.1 release runs hit four more timing flakes, fixed before it
shipped: the fork test's call outlived its 60 s deadline on an Intel
runner (it now runs under the test world's 2 minutes),
`TestSlowViewerResyncs` timed the PTY flood rather than a hang,
`TestScreen` read the screen before the prompt was drawn, and
`TestReviewAcceptReject` exposed a real race: a scan running during an
accept stored what it read before it.

The release workflow runs tests again, publishes both notarized zip files and
SHA256SUMS, then commits `Casks/hesper.rb` with exact archive hashes to the
branch `brew/<tag>`. Changes reach main only through pull requests, so open
and merge the pull request from the link in the run's summary.
It can also be re-run through workflow_dispatch with an existing stable tag.
Do not move an already released tag. Re-running replaces assets for that tag;
release versions should normally be immutable, with fixes shipped as a new tag.
An older release cannot downgrade the cask.

## Install binaries

Recommended installation (public downloads; no GitHub login, SSH or token):

```sh
brew tap derzierau/hesper https://github.com/derzierau/hesper.git
brew install --cask derzierau/hesper/hesper
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
```

Use the app directory printed by Homebrew if you supplied `--appdir`.
The setup command installs the LaunchAgent, hooks and skill without modifying
the signed app. Enable Codex hooks once with `/hooks`. Run setup again after
`brew update` and `brew upgrade --cask derzierau/hesper/hesper` to restart
the daemon with the updated binary.
Homebrew uninstall unloads the daemon and keeps your agents and configuration.

The repository and release downloads are public. The cask uses HTTPS
release download URLs with exact SHA-256 hashes; no GitHub token is needed.
The tap is hosted in this source repository, so its full URL is required
when adding it. A separate `homebrew-hesper` repository would enable the
short `brew tap derzierau/hesper` command. Official `homebrew/cask` inclusion
requires its own submission and review.
