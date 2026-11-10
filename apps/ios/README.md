# Hesper for iPhone and iPad

A remote for the agents on your Macs: what needs you, start, follow, steer
and judge. The concept and wireframes are in
[`docs/iphone/`](../../docs/iphone/concept.md).

On first launch it shows onboarding
([`docs/iphone/onboarding.md`](../../docs/iphone/onboarding.md)) until a Mac
is paired or the owner looks around with the sample Macs. Sample Macs run
on fixtures (`FixtureLink`, `FixturePairing`); paired Macs through the
mobile link library (`HesperLink`), wired in `App/Services.swift`.

## Requirements

- Xcode 26 (iOS 26 SDK) and its Swift 6.2 toolchain.
- [XcodeGen](https://github.com/yonaskolb/XcodeGen) 2.46 or newer:
  `brew install xcodegen`.

## Build and test

Everything is headless; nothing opens a window or boots a simulator.

```sh
make -C apps/ios test      # logic tests (swift test, on the Mac)
make -C apps/ios build     # app + extensions for the simulator, unsigned
make -C apps/ios lint      # swift format lint
make -C apps/ios check     # all three, as CI (.github/workflows/ios.yml)
```

To run it, `make -C apps/ios project` and open `Hesper.xcodeproj`. The
project is generated from `project.yml` and not committed: change targets
and settings there, never in Xcode.

## Signing

Copy `Config/Local.xcconfig.example` to `Config/Local.xcconfig` (ignored by
git) and set your team ID and a bundle prefix you own. The app becomes
`<prefix>.ios`, the extensions `<prefix>.ios.widgets` and
`<prefix>.ios.notifications`, and the app group `group.<prefix>`. App
groups need a paid developer account.

## Layout

| Path | What |
|---|---|
| `project.yml` | Targets and settings (XcodeGen) |
| `Config/` | `Base.xcconfig` and its Debug/Release layers; your `Local.xcconfig` |
| `App/` | The app target: `HesperApp.swift`, icon, privacy manifest |
| `App/Fonts/` | Space Grotesk Bold and JetBrains Mono Regular (OFL) |
| `Widgets/` | Lock-screen widget and the one fleet Live Activity |
| `NotificationService/` | Writes a push's text before it shows |
| `Packages/HesperKit/` | Logic, no views; `swift test` runs it on a Mac |
| `Packages/HesperUI/` | The screens (iOS only) |
| `Packages/HesperUI/Sources/HesperFeatures/Home/` | Home: scope bar, priority card, task grids, the Speak / Type console |
| `Packages/HesperTerminal/` | The agent's terminal: libghostty for iOS, and its tested viewport logic |

`HesperKit`:

- `HesperFleet`: link events folded into one fleet across Macs, the Inbox
  order, project groups, counts, the copy of a row, and `FleetModel`, the
  app's observable model over a `LinkClient`. Onboarding and Add Mac on a
  `PairingService`: `OnboardingModel`, `AddMacModel`, `PairingPayload`.
- `HesperFixtures`: the sample Macs from the wireframes, `FixtureLink`, and
  `FixturePairing`, which plays every way pairing can end.
- `HesperSurfaces`: what the app shares with its extensions (app group
  counts, the Live Activity's attributes).
- `HesperLink`: the live link, the controller side of the relay protocol.
  Device and strong keys in the Secure Enclave (software on the simulator),
  the X25519 end-to-end key in the Keychain, request signing
  (`pkg/devicekey`'s golden vector), GitHub enrollment with PKCE and one
  renewal at a time, the relay socket, the Noise IK channel (BLAKE2s;
  vectors from `relay/pkg/e2e`), sealed links, read-only attaches on a
  link, `LiveLink: LinkClient` and `LivePairing: PairingService`. Its flows are tested against a fake relay
  and Mac.

`HesperUI`:

- `HesperDesign`: the Neon tokens (`docs/iphone/neon.md`), the square state
  mark, controls.
- `HesperFeatures`: Home, Agents (with search and Later), Macs (with Add
  Mac), Settings, the agent view with the live terminal, capture (the
  Speak/Type console, dictation and the capture sheet) and onboarding.

`HesperTerminal`:

- `TerminalModel`: no UIKit, so `swift test` runs it on a Mac. `Viewport`
  (readable and fit width, zoom and pan limits, which row to follow),
  `FollowRule`, `TerminalFeeder` and `Fling` (scroll momentum).
- `HesperTerminal`: `TerminalSurface`, libghostty-spm's `GhosttyTerminal`
  (iOS only) drawing a Mac's grid; a drag is handed to it as wheel input
  and what it writes back goes to the Mac on a read-write attach.

Wire models and design tokens come from the Mac app's `HesperCore`
(`app/Package.swift` exports it), so the phone and the Mac decode the same
contract.

## Checking a screen

A Debug build walks onboarding to one screen on the fixtures with
`-HesperOnboardingDemo <step>` (`hello`, `scan`, `address`, `signIn`,
`pickMac`, `approveOnMac`, `connected`, `language`, `samples`), and plays a
failure with `-HesperPairingFixture <name>` (`notAllowed`, `unreachable`,
`cancelled`, `pending`, `offline`, `denied`, `unsupported`, `keyChanged`).
For a headless simulator: `xcrun simctl launch <device> <bundle id>
-HesperOnboardingDemo connected`, then `xcrun simctl io <device> screenshot`.
`-HesperOnboardingDemo samples` lands on Home with the sample Macs; the
environment variable `HESPER_HOME_SCOPE` (`all`, `needsYou`, `working`;
pass it as `SIMCTL_CHILD_HESPER_HOME_SCOPE=working xcrun simctl launch …`)
opens Home on that scope. `HESPER_CAPTURE` (`listening`, `typing`) opens
the capture sheet at launch, `listening` with a sample transcript and no
microphone, since nobody can press Speak on a headless simulator.
`HESPER_OPEN` (`agent:<id>`, `agents`, `macs`, `settings`) pushes that
screen over Home at launch. `agent:studio/s1` opens the agent view with
the sample Mac's scripted terminal (every sample agent has one), so the
terminal can be checked without a Mac.

## Choices

- **XcodeGen, no checked-in project**: a short YAML file instead of a
  `project.pbxproj` that agents and reviewers can't merge.
- **Thin targets, logic in packages**: the app target is one file.
  Logic that can be tested without a simulator goes in `HesperKit`.
- **Swift 6 with approachable concurrency**: `NonisolatedNonsendingByDefault`
  and `InferIsolatedConformances`. UI code defaults to the main actor
  (`defaultIsolation(MainActor.self)`, `SWIFT_DEFAULT_ACTOR_ISOLATION`), and
  logic stays nonisolated.
- **Swift Testing**, `swift format` from the toolchain. One third-party
  package: libghostty-spm (with DisplayLink) for the terminal, at the
  Mac's pin. Its prebuilt framework includes LGPL libintl, so before an
  App Store release the iOS slice needs the source build the Mac uses.
- **Plain SwiftUI**: `@Observable` model in the environment, one
  `NavigationStack`, `#Preview` on fixtures. Home's Speak / Type console
  (`Console`) is the app's own bar, not a system tab bar: Speak carries a
  push-to-talk gesture no system control has.
- **On-device dictation**: `SpeechTranscriber` for capture; nothing leaves
  the phone but the text the owner sends.
- **No background sockets**: the model follows the Macs only while the app
  is active; pushes and Live Activity updates carry the rest.

## What comes next

In the order of the concept's "Core work":

Built: the live terminal, writable on a `rw` attach (below). Its
scrollback on a read-only attach needs hesperd #65, and the fit needs
hesperd #67 (`sizeOwner`, the capability, an optional `sizeOwnerHost`
when the agent's own Mac owns the size, an owner's name that may be
absent); without them the phone mirrors with no history on `ro`.

1. Terminal selection and links. The agent view shows the live
   terminal. On a Mac that publishes
   `capabilities.sizeOwner` it fits the Mac's terminal to the phone when
   it opens: an owner attach (`rw`) at the grid that
   fits the area above the dock at a true 18 pt, unscaled, no sideways
   pan, resized (RESIZE, 300 ms debounce) on rotation and dock changes,
   let go (`X`) when the screen closes or the app leaves the foreground.
   An older Mac, a `forbidden` reply, or the Mac taking the size back
   (its keystroke or click) gives the Mirror: the Mac's grid, scaled and
   panned, attached `rw` when the phone has the `type` right (`ro` after
   a `forbidden`). A drag scrolls the agent as wheel input, with
   momentum; the only bytes sent are what libghostty writes back for it.
   On a `rw` attach a tap on the terminal brings up the keyboard, with
   libghostty's key bar (Esc, Tab, sticky Ctrl, arrows); a second tap or
   a swipe down puts it away. Keys go to the agent; the dock's Send
   presses Enter. A read-only attach takes no keys.
   In the samples `studio` and `air` fit, `mini` mirrors
   (`HESPER_OPEN=agent:mini/m2`).
2. Push: the notification extension decrypts ids into text; actionable
   Allow / Deny / Reply.
3. Live Activity and widget pushes from the relay.
4. Speech for replies in the agent view, App Intents and a Control Center
   control that opens capture.
5. An Icon Composer icon (`AppIcon.icon`); the current icon is the Mac's
   icon, cropped.
6. String catalogs once the copy settles.
