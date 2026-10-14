# Hesper v0.3.0

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
v0.2.0, run `brew update`, `brew upgrade --cask derzierau/hesper/hesper`, then
the setup command again so the daemon restarts on the new binary. Use the app
path printed by Homebrew if using `--appdir`.

## New in v0.3.0

- **Every action works with the mouse.** Each control on a tile, strip,
  sheet, popover and band is a button that carries its key cap: Allow ⏎,
  Always A and Deny N on an approval, Resume on an ended agent, Fork and
  Continue on another Mac in History, Seen, Reject and Note on a Review hunk.
  History, Review and the quick-launch panel have a close button, the focus
  view has a back button, rename and deny fields have Save and Cancel, and
  narrow tiles fold their secondary verbs into a ⋯ menu instead of hiding
  them. Right-click works on active terminals, draft tiles, ghost cards,
  band headers and the rows of History and Review.
- **A complete menu bar.** File, Edit, View, Window and Help with the
  standard macOS items, a Dock menu, and menu items that disable when they
  do not apply. Edit ▸ Undo names what it undoes.
- **Help ▸ Keyboard Shortcuts (⌘/)** lists every binding, grouped by where it
  is live, with search. Settings ▸ Shortcuts shows the same list.
- **Rebind shortcuts.** Settings ▸ Shortcuts records a new chord per
  command, refuses conflicts and macOS-reserved chords, and warns when the
  quick-launch hotkey collides with macOS input-source switching.
- **One command table.** Menus, the ⌘K palette, key routing, Settings and
  the shortcuts sheet are generated from one registry, with tests that keep
  them aligned: no chord means two things in one surface, and every chord
  has a menu item.
- **Accessibility.** Rows, cards and tiles have button roles and press
  actions for VoiceOver, the tile menu is exposed as custom actions, and
  Full Keyboard Access can reach the toolbar, sidebar and sheets.
- **hesperctl** gains `open review [--agent ID]` and `open palette [--query
  TEXT]`, also as MCP tools.

## Changed bindings

- ⌘[ and ⌘] no longer step agents; use ⌥⌘← and ⌥⌘→ (Tab and ⇧Tab on the
  wall). They clashed with the layout chords on German keyboards.
- Worktree toggle in a draft is ⌥⌘B (was ⇧⌘W, which is Tidy Up everywhere).
- Desk Picker is ⇧⌘D (was ⌃⌘D, the macOS Look Up chord).
- ⌘W in an agent's own window closes the window and keeps the agent running;
  ⌥⌘W sends it to background. ⌘W in Settings closes Settings.
- ⌥⌘↑ and ⌥⌘↓ jump between bands (⌥↑ ⌥↓ still work on the wall).
- In History, ⌘K opens the palette (Tab switches scope), M continues on the
  other Mac, J and K move, and ⌘⏎ resumes full size. ⌥⏎ forks, as the card
  now says.
- In the ⌘J inbox, ⌘⏎ opens the agent (the ⌘O alias is gone) and typing
  filters the list.

## Fixes

- Keys are matched by key code, so ⌥⌘5 on a German layout picks the Grid
  layout and ⌫ reaches a popover's filter.
- Esc in a draft closes the completion list before it parks the draft.
- A close or move strip no longer takes ⏎ and Esc from a focused terminal.
- ⌘W over History, Review or a popover no longer closes the agent beneath.
- ⌘W in Settings or First Run no longer closes an agent on the main wall.
- Rebinding reaches the palette and popover chords and no longer leaks a
  History observer.

Local use needs no relay. Multiple Macs require a self-hosted relay and device
setup; Hesper does not provide a hosted cloud service. See the
[README](https://github.com/derzierau/hesper#install) for setup and prerequisites.

## Alternative downloads

The release assets `Hesper-v0.3.0-arm64.zip` and
`Hesper-v0.3.0-x86_64.zip` contain Hesper.app, hesperd, hesperctl and hesper-keys.
Both architectures are Developer ID-signed, notarized and stapled. Verify the
archive against `SHA256SUMS`, extract it into `/Applications`, then run the
same setup command above.
