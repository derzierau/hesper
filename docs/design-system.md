# Hesper design system

Phase 0 of the UI redesign: one `DS` namespace and six primitives that every
view builds on. Colors stay in `Theme` (tokens with Dusk and Daylight
values). Everything else (spacing, radii, type, motion, elevation, density)
comes from `DS`.

| What | Where |
|---|---|
| Pure values (unit tested) | `app/Sources/HesperCore/DesignTokens.swift`: `DesignTokens`, `StateMarkKind` |
| AppKit/SwiftUI side | `app/Sources/Hesper/UI/DesignSystem.swift`: `DS` |
| Primitives | `app/Sources/Hesper/UI/DS/`: StateMark, Pill, Kbd, IconButton, Panel, Row |
| Gallery (DEBUG builds only) | `UI/DS/DesignGallery.swift`, opened from **Debug → Design System Gallery** |
| Tests | `app/Tests/HesperCoreTests/DesignTokensTests.swift`, `WallLayoutTests.swift` (terminal edge to edge) |

## Rules

1. **No literal numbers in views.** Spacing, radii, font sizes and durations
   come from `DS`. If a value you need is missing, add it to
   `DesignTokens`/`DS` with a name. Don't write the number inline.
2. **Signal is only for "needs you".** Signal red means an approval is
   waiting. Questions use `question`, errors use `error`, and selection or
   accents use `working`. If two things glow, neither one stands out.
3. **Glass only for floating layers.** The toolbar, popovers, the palette
   and sheets use `Panel` / `DS.Elevation.floating`. Tiles and terminals
   stay solid (`flat`).
4. **Squares, not dots.** Every agent state is drawn with `StateMark`.
   Never draw a `Circle()` for state.
5. **Chrome ≤ 28 pt** (`DS.chromeMaxHeight`). Tile headers follow density
   (`DS.tileHeader`).
6. **Weights 400/500/600 only** (`DS.Weight.regular/.medium/.semibold`).
   Don't use `.bold`.
7. **Continuous corners.** Use `DS.Radius.shape(_)` in SwiftUI and
   `DS.Radius.apply(_, to:)` in AppKit, so every layer gets
   `cornerCurve = .continuous`.
8. **Motion is calm and optional.** Use `DS.animation`, `DS.withMotion`,
   `DS.animate` or `DS.caAnimation`. They return no animation under Reduce
   Motion. No springs, no bounces.
9. **Terminals own their background.** Anything behind or around a
   terminal is painted `DS.terminalBackground` (libghostty's theme). Never
   use a chrome token there. This holds in both Dusk and Daylight.
10. **Color tokens** live in `Theme.Token`: `background · surface · tile ·
    line · text · text2 · dim · dim2 · signal · working · question · done ·
    error · horizon`. Layers resolve them with `.cg(in: view)` and
    re-apply them in `viewDidChangeEffectiveAppearance`.

## DS API

```swift
// Spacing (CGFloat): 2 · 4 · 6 · 8 · 12 · 16 · 24
DS.Spacing.xxs, .xs, .s, .m, .l, .xl, .xxl

// Radius (CGFloat): kbd 4 · control 7 · tile 10 · panel 14
DS.Radius.kbd, .control, .tile, .panel
DS.Radius.shape(_ r: CGFloat) -> RoundedRectangle            // style: .continuous
DS.Radius.apply(_ r: CGFloat, to layer: CALayer?, masks: Bool = false)

// Type: Geist; meta is Geist Mono
DS.TextStyle: .meta 11 (mono) · .chrome 12 · .body 13 · .panelTitle 15 · .sheetTitle 20 · .display 28
DS.Weight: .regular (400) · .medium (500) · .semibold (600)
DS.font(_ style, _ weight = .regular) -> Font
DS.nsFont(_ style, _ weight = .regular) -> NSFont
DS.monoFont(_ style, _ weight) -> Font / DS.nsMonoFont(_:_:) -> NSFont   // Geist Mono at a step's size
Font.ds(.chrome, .medium), NSFont.ds(.body)                  // shorthands

// Motion: quick 0.12 · move 0.18 · settle 0.22 s; one curve cubic(0.2, 0, 0, 1)
DS.Motion.quick/.move/.settle  (.duration, .duration(reduceMotion:))
DS.timingFunction: CAMediaTimingFunction
DS.animation(_ m, reduceMotion: Bool = DS.reduceMotion) -> Animation?      // nil = don't animate
DS.withMotion(_ m, reduceMotion:) { … }                                    // withAnimation wrapper
DS.animate(_ m, reduceMotion:, { view.animator().frame = f }, completion:) // NSAnimationContext
DS.caAnimation(_ m, keyPath:, reduceMotion:) -> CABasicAnimation?          // nil under Reduce Motion
DS.reduceMotion, DS.reduceTransparency: Bool                               // the system settings

// Elevation
DS.Elevation.flat / .raised / .floating
  .shadow: Shadow?              // floating only: opacity 0.28, radius 18, y 6
  .usesMaterial: Bool
  .applyShadow(to: CALayer?, radius:)

// Chrome and density
DS.chromeMaxHeight            // 28
DS.bandGap                    // 16
DS.density: DS.Density        // @MainActor; AppSettings.density keeps it in sync (persisted)
DS.densityDidChange           // Notification posted when it changes
DS.tileHeader                 // 26 Comfortable / 22 Compact
DS.tileGutter                 // 8 / 6
DS.Density.comfortable/.compact (.tileHeader, .tileGutter, .bandGap, .label)

// Terminal
DS.terminalBackground: NSColor  // TokyoNight background, the same in both schemes
```

Pure equivalents for HesperCore and tests: `DesignTokens.Spacing/Radius/
TextStyle/Weight/Chrome/Density/Motion`, `DesignTokens.pulsePeriod` (2 s).

### State → mark

`StateMarkKind(_ state: AgentState)`:

| AgentState | Kind | Drawn as |
|---|---|---|
| starting | `.starting` | outlined `working` |
| working | `.working` | filled `working` |
| approval | `.needsYou` | filled `signal`, slow 2 s opacity pulse (none under Reduce Motion) |
| question | `.question` | filled `question` |
| done | `.done` | filled `done` |
| idle, unknown | `.idle` | outlined `dim` (1.5 pt) |
| error | `.error` | filled `error` |
| exited | `.exited` | outlined `dim` with a diagonal slash |

The mark is 7 pt (`StateMarkKind.side`). Its `.fill` is `.filled`,
`.outlined` or `.slashed`, and its `.tone` maps to a token through
`Theme.token(_:)`. `.pulses(reduceMotion:)` is true only for `.needsYou`.

## Primitives

Each primitive has one spec and two thin implementations: SwiftUI for
overlays and AppKit for the wall. Both read the same `DS` values.

### StateMark

```swift
// SwiftUI
StateMark(state: agent.state)
StateMark(.needsYou)
StateMark(color: Theme.color(hex))      // a plain filled square (non-state: machine online, project)

// AppKit
let m = StateMarkView(.working)         // intrinsic size 7 × 7; layer-drawn, follows appearance
m.kind = StateMarkKind(agent.state)     // pulses for .needsYou while in a window
m.plainColor = Theme.ns(.done)          // optional: plain square

// Menus
item.image = StateMark.image(StateMarkKind(agent.state))
```

### Pill

Radius `control`, height 22. Variants: `.segment(selected:)`, `.status`
(tinted with its mark's tone at 14%) and `.count` (Mono).

```swift
Pill("All", variant: .segment(selected: true), count: 7)
Pill("2 need you", mark: .needsYou)
Pill("", variant: .count, count: 12)
Pill("Search", variant: .segment(selected: false), kbd: "⌘K")

let p = PillView("Working", variant: .status, mark: .working)   // AppKit
p.count = 3
```

### Kbd

Geist Mono 10.5 medium, a 1 px `line` border, radius `kbd`, dim text.

```swift
Kbd("⌘K")
let k = KbdView("⌘J")   // AppKit, intrinsic size
```

### IconButton

An SF Symbol in a 24 pt hit area, with a `line` fill on hover and a tooltip
of the form "Help (shortcut)".

```swift
IconButton("magnifyingglass", help: "Search", shortcut: "⌘K") { model.openPalette() }

let b = IconButtonView(symbol: "clock", help: "History", shortcut: "⌘Y", target: self, action: #selector(history))
```

### Panel

The floating glass container: material and blur, radius `panel`
(continuous), a 1 px `line` hairline and one soft shadow. Under Reduce
Transparency it falls back to solid `surface`.

```swift
Panel { VStack { … } }                 // padding DS.Spacing.l by default
someView.dsPanel()                     // the same look on any view

let panel = PanelView(frame: r)        // AppKit
panel.contentView.addSubview(list)     // content is clipped to the rounded rect
```

### Row

A leading mark, a title (body 13), an optional subtitle (chrome 12, dim),
trailing meta (Mono 11, dim) and an optional Kbd. Hover uses `line` at 60%
and selected uses `working` at 20%. Radius `control`. The height is 28,
or 40 with a subtitle (`Row.height(subtitle:)`).

```swift
Row(title: "migrations", subtitle: "Allow edit to 0042_users.sql?", meta: "mini · 2m",
    mark: .needsYou, kbd: "⏎", selected: i == selection)

// Sidebar extras (SwiftUI): a 6 pt color square in the mark's place, a
// trailing mark before the meta, nesting inside the hover/selected fill
Row(title: "api", meta: "3", swatch: Theme.color(hex), trailingMark: .needsYou, indent: DS.Spacing.l)

let r = RowView(title: "api", meta: "3", mark: .working)   // AppKit
r.isSelected = true
```

## Terminals edge to edge

`WallSpec.inset` is 0 (it is still a parameter). The terminal spans the
tile body across the full width, from under the header to the footer band.
`WallTile.body` is that whole area. `WallTile.terminal` is the
whole-cell grid (`cols × rows`, the size asked of the PTY with `attach
--fit`), bottom-anchored inside the body. The less-than-a-cell remainder
sits inside the body and is painted in `DS.terminalBackground`: TileView
adds a terminal backdrop behind `AgentTerminal.host`, the host's layer uses
the same color, and libghostty uses `window-padding-color = background`.
FocusView (and with it every AgentWindow) has `inset` 0. The surface fills
the area and libghostty sizes the PTY from it.

## What phase 0 did not change (for the phase owners)

- **Tile geometry**: `Metrics.wall` keeps header 32, gap 14, margin 20 and
  `Metrics.radius` 12. The tile phase moves these to `DS.tileHeader`,
  `DS.tileGutter` and `DS.Radius.tile`, and wires `DS.densityDidChange` to a
  relayout. Density has no UI yet (Settings → Appearance belongs to the
  settings phase), but `AppSettings.density` is persisted.
- **Font sizes outside the scale**: about 80 sites use 8.5, 9.5, 10, 10.5,
  11 (sans), 14 or 22. Mapping them needs a design decision, mostly
  between `chrome 12` and `meta 11 Mono`. Find them with
  `git grep -nE "\.geist(Mono)?\((ofSize: )?[0-9.]+" app/Sources/Hesper`.
- **Radii left as literals**: tile and band cards at 12 (`Metrics.radius`,
  Bands), the project swatch at 2 and 3, and the wall's scroll pill at 11.
- **Spacing outside the scale** (3, 5, 7, 9, 10, 14, 20 …) is still literal
  in the components that use it.
