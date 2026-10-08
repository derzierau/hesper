# Signed releases and Homebrew

A stable tag (`vX.Y.Z`) builds native arm64 and x86_64 apps on GitHub's
macOS runners. Each app includes hesperd, hesperctl, hesper-keys, the agent
skill and a binary setup script. The workflow tests, signs with the hardened
runtime, notarizes, staples and checks Gatekeeper before publishing either
architecture. libghostty is built without libintl as described in licensing.md.

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

After build/test CI passes on main:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs tests again, publishes both notarized zip files and
SHA256SUMS, then commits `Casks/hesper.rb` to main with exact archive hashes.
It can also be re-run through workflow_dispatch with an existing stable tag.
Do not move an already released tag. Re-running replaces assets for that tag;
release versions should normally be immutable, with fixes shipped as a new tag.
An older release cannot downgrade the cask.

## Install binaries

After the first successful signed release:

```sh
brew tap derzierau/hesper https://github.com/derzierau/hesper
brew install --cask derzierau/hesper/hesper
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
```

Use the app directory printed by Homebrew if you supplied `--appdir`.
The setup command installs the LaunchAgent, hooks and skill without modifying
the signed app. Enable Codex hooks once with `/hooks`. Run setup again after
`brew upgrade --cask hesper` to restart the daemon with the updated binary.
Homebrew uninstall unloads the daemon and keeps your agents and configuration.

The repository currently is private. The tap and release downloads therefore
require access; public distribution requires making them publicly accessible.
The custom tap needs no separate cross-repository token. No cask is available
until a signed release has succeeded.
