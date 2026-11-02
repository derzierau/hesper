# Hesper for iPhone and iPad

A remote for the agents on your Macs: what needs you, start, follow, steer
and judge. The concept and wireframes are in
[`docs/iphone/`](../../docs/iphone/concept.md).

This is the base project. It runs on sample Macs (`FixtureLink`) until the
mobile link library gives it a live connection; see "What comes next".

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
| `Widgets/` | Lock-screen widget and the one fleet Live Activity |
| `NotificationService/` | Writes a push's text before it shows |
| `Packages/HesperKit/` | Logic, no views; `swift test` runs it on a Mac |
| `Packages/HesperUI/` | The screens (iOS only) |

`HesperKit`:

- `HesperFleet`: link events folded into one fleet across Macs, the Inbox
  order, project groups, counts, the copy of a row, and `FleetModel`, the
  app's observable model over a `LinkClient`.
- `HesperFixtures`: the sample Macs from the wireframes and `FixtureLink`.
- `HesperSurfaces`: what the app shares with its extensions (app group
  counts, the Live Activity's attributes).

`HesperUI`:

- `HesperDesign`: the Mac's Dusk/Daylight tokens, the square state mark, controls.
- `HesperFeatures`: Inbox, Agents (with search and Later), Macs, the agent
  view, and capture: the mic circle, dictation and the capture sheet.

Wire models and design tokens come from the Mac app's `HesperCore`
(`app/Package.swift` exports it), so the phone and the Mac decode the same
contract.

## Choices

- **XcodeGen, no checked-in project**: a short YAML file instead of a
  `project.pbxproj` that agents and reviewers can't merge.
- **Thin targets, logic in packages**: the app target is one file.
  Logic that can be tested without a simulator goes in `HesperKit`.
- **Swift 6 with approachable concurrency**: `NonisolatedNonsendingByDefault`
  and `InferIsolatedConformances`. UI code defaults to the main actor
  (`defaultIsolation(MainActor.self)`, `SWIFT_DEFAULT_ACTOR_ISOLATION`), and
  logic stays nonisolated.
- **Swift Testing**, `swift format` from the toolchain, no third-party
  packages.
- **Plain SwiftUI**: `@Observable` model in the environment, one
  `NavigationStack` per tab, `#Preview` on fixtures. One exception: on
  iPhone the tab bar is drawn by the app (`CaptureBar`), because the system
  bar has no slot for the mic circle.
- **On-device dictation**: `SpeechTranscriber` for capture; nothing leaves
  the phone but the text the owner sends.
- **No background sockets**: the model follows the Macs only while the app
  is active; pushes and Live Activity updates carry the rest.

## What comes next

In the order of the concept's "Core work":

1. A live `LinkClient` from the mobile link library: pairing, the
   end-to-end channel, `agents.link` per Mac.
2. The terminal in the agent view: libghostty-spm's `GhosttyTerminal`
   on an `agents.attach`, Mirror first, then Fit to iPhone.
3. Push: the notification extension decrypts ids into text; actionable
   Allow / Deny / Reply.
4. Live Activity and widget pushes from the relay.
5. Speech for replies in the agent view, App Intents and a Control Center
   control that opens capture.
6. An Icon Composer icon (`AppIcon.icon`); the current icon is the Mac's
   icon, cropped.
7. String catalogs once the copy settles.
