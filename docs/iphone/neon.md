# Hesper for iPhone: the Neon control console

Design document for restyling and relaying out `apps/ios` after the owner's
Figma direction "Home — Neon control console" (file `DA5Go5PdDsVSDw5q771Jzp`,
page Screens, node `27:2456`; render: `neon-home.png` next to this file).
Decided with the owner on 7 Nov 2026. Supersedes the "Visual language"
section of `concept.md` and the tab-bar part of its information
architecture; everything else in `concept.md` still holds.

## Intent

The owner is a designer. The app's look is his, and this Figma frame is the
look he wants: a dark control console, cyan for action, magenta for
"needs you", square corners, a big headline for the one decision that
matters most, a bottom console with Speak and Type. The app keeps Ole's
flows, model and link, and gets this face.

## Decisions (the owner's)

1. **Full Neon layout.** Home is the Figma frame: scope bar on top, the
   priority decision card, the task grid, the Speak / Type console at the
   bottom. **No tab bar and no mic circle.**
2. **Dark only for now.** The app forces dark. A light variant may come
   later; nothing in this change prepares one beyond keeping tokens
   semantic.
3. **Bundle the fonts.** Space Grotesk (Bold) and JetBrains Mono
   (Regular), both SIL OFL 1.1, shipped in the app and listed in
   `THIRD_PARTY_NOTICES.md`.
4. Ole's app (`apps/ios`) is the codebase. The older `ios/` app is gone
   (tag `archive/hesper-remote-ios-0.2.3`).

## Goals

- Home matches the Figma frame: same hierarchy, colours, type, shapes,
  spacing, at 440 pt width, and holds up at other widths and Dynamic
  Type sizes.
- Every other screen (agent view, capture sheet, Agents, Macs, Settings,
  Add Mac, onboarding, toasts, talk panel) uses the Neon tokens, square
  corners and bordered controls, with its layout otherwise unchanged.
- No Liquid Glass anywhere; surfaces are solid.
- Ole's flows keep working: inline answers, push-to-talk, capture sheet
  with Start now / Keep for later, drafts, agent view dock, onboarding,
  live link.

## Non-goals

- A light appearance.
- The live terminal, drafts over the relay, the Review sheet (still
  Ole's open work).
- Widgets, the Live Activity, the notification service: no layout work.
  They import `Theme`, so they take the Neon colours with everyone else
  and must still build; that is all.
- The Mac app and HesperCore's `DesignTokens`: unchanged. The Neon palette
  is the phone's own; it lives in HesperKit (`NeonPalette`) and
  `HesperDesign.Theme` reads it.
- iPad layout work beyond what falls out of the grid (see Layout).

## Anti-goals

- Rounded corners, capsules or glass "because iOS": the owner wants the
  square console.
- Hard-coded colours or sizes in views: everything comes from `Theme` and
  `DS` (repo rule).
- Losing Ole's behaviour while restyling: a restyled control does what it
  did before.

## Tokens

Source: the Figma frame (hard-coded there; this table is the system).
`Theme.Token` in `HesperDesign/Theme.swift` is replaced by these; the old
Dusk/Daylight pairs go. Names stay semantic so a light set can be added
later.

| Token | Hex | Used for |
|---|---|---|
| background | #070A0D | screen, console, command blocks, menu button fill |
| surface | #0D1419 | cards, scope segments (unselected), Type button, No… button |
| surface2 | #101D24 | numbered option rows |
| line | #304650 | 1 pt borders, console top edge, command block left edge |
| text | #F0F6F7 | headlines, body, status bar |
| muted | #9BAFB8 | meta lines, option detail |
| accent | #64F5FF | cyan: selected segment, Yes, Speak, borders of scope pill, menu and Type, command text, "You", option numbers, chevron |
| onAccent | #070A0D | text and icons on accent fills |
| accentTint | #0D2B32 | "You" panel, open-decision button |
| signal | #FF55CE | magenta: needs-you caption, "!" marks, section count, No… text and border, destructive option |
| signalTint | #291529 | destructive option row fill |

State colours (my call, Neon defines only cyan and magenta): `working` =
accent, `signal`/`question`/`error` = signal, `done` and `dim` = muted.
`Theme.tone(_:)` maps the state-mark tones onto these. `horizon` (the
onboarding glow) = accent at low opacity.

Shapes: corner radius **2 pt** on everything (a new `DS.Radius.console =
2` in HesperDesign; `DS` exposes no radius today);
the only exceptions are the 6 pt circle of the state caption dot and the
8 pt square `StateMark`, which stays square. Borders are 1 pt `line`
unless the table says accent or signal. The priority card has a **3 pt
accent top edge** and 1 pt `line` sides and bottom.

Spacing: `DS.Spacing` as is (2/4/6/8/12/16/24); the grid gap is 10 and
the console gap is 10 (`DS.Spacing.grid = 10`).

Motion: unchanged (`DS.animation`).

## Type

Three faces. All sizes scale with Dynamic Type
(`Font.custom(_:size:relativeTo:)`).

| Role | Face | Size / line | Colour |
|---|---|---|---|
| Hero question | Space Grotesk Bold | 36 / 38, relative to `.largeTitle` | text |
| Section heading | Space Grotesk Bold | 19, `.title3` | text |
| Grid card title | Space Grotesk Bold | 15 / 18, `.subheadline`; leading "! " in signal when needs-you | text |
| Section count | JetBrains Mono | 20, `.title3` | signal (needs you), muted (other sections) |
| State caption | JetBrains Mono | 12, `.caption` | signal |
| Command / file | JetBrains Mono | 12 / 16 in the hero, 10 / 12 in grid cards, `.caption` / `.caption2` | accent |
| Option number | JetBrains Mono | 17, `.body` | accent; signal when destructive |
| "You" label | SF Pro Semibold | 13 hero, 11 grid | accent |
| Request text | SF Pro | 15 hero, 12 grid | text |
| Meta line | SF Pro | 11 / 14 | muted |
| Option label | SF Pro Medium | 15 / 18 | text; signal when destructive |
| Option detail | SF Pro | 11 | muted |
| Scope segment | SF Pro | 13; Semibold when selected | onAccent selected, text otherwise |
| Grid buttons | SF Pro | 14 | onAccent (Yes), signal (No…) |
| Speak | SF Pro Semibold | 22 | onAccent |
| Type | SF Pro Semibold | 17 | accent |
| Everything else | SF Pro at the system text styles Ole uses | | per token |

Font files: `apps/ios/App/Fonts/SpaceGrotesk-Bold.ttf` and
`JetBrainsMono-Regular.ttf`, registered with `UIAppFonts` in
`project.yml`. Sources: github.com/floriankarsten/space-grotesk (OFL 1.1)
and github.com/JetBrains/JetBrainsMono (OFL 1.1). Both get a row and the
licence text in `THIRD_PARTY_NOTICES.md` (JetBrains Mono already has one
for the Mac app; add the iOS app to it).

## Layout: Home

Replaces `InboxScreen` as the root of the `NavigationStack`. Top to bottom:

**Scope bar** (height 92 incl. padding; inset 16, gap 12):
- Scope pill: 44 pt tall, fills the width, `background` fill, 1 pt accent
  border. Three equal segments **All N · Needs you N · Working N**.
  Selected: accent fill, onAccent Semibold. Unselected: `surface` fill,
  1 pt `line` border, `text` Regular. Counts: All = all live agents,
  Needs you = `fleet.needsYou.count`, Working = `fleet.counts.working`.
- Menu button: 44 pt square, `background` fill, 1 pt accent border, menu
  icon (`line.3.horizontal`) in accent. Opens a `Menu`: **Agents, Macs,
  Search, Settings**. Agents, Macs and Settings push their existing
  screens; Search pushes Agents with the search field focused.
- The system status bar stays; its text is light.

**Content** scrolls under the console, inset 16, section gap 16, bottom
padding so the last card clears the console.

Scope **All**: priority card, "Needs you" grid, "Ready to review" grid,
"Working" grid. Scope **Needs you**: priority card and the "Needs you"
grid. Scope **Working**: the "Working" grid only. Empty scope: Ole's
`ContentUnavailableView` copy, restyled.

Which states go where (my call, following the Mac's tiers): needs you =
approval, question, error; working = working, starting; ready to review =
done **and idle** (the Mac's "Done · idle" tier), newest first. Exited
and unknown agents are not on Home; they stay in Agents, as today. "All N"
counts every agent in the fleet, exited ones included, so it matches the
Agents screen.

**Priority decision card** = `fleet.needsYou.first` (the attention queue:
approvals, then questions, then errors, oldest first; same order as the
Mac). `surface` fill, 3 pt accent top edge, 1 pt `line` sides, padding
12, gap 8:
1. Row: state caption (6 pt signal dot + "Needs you · 11m" mono 12 signal,
   age = `stateSince`) and, trailing, the **open button** (28 pt,
   `accentTint`, chevron.right accent) which pushes the agent view
   (`AgentRoute`). The whole card above the options is also tappable to
   push.
2. Question: `AgentCopy.headline(agent)` in the hero face.
3. Command block, only when the attention has a command or file to show
   (Ole's `AgentCopy` already extracts it for approvals): `background`
   fill, 2 pt `line` left edge, mono 12 accent, scrolls sideways.
4. "You" panel: `accentTint`, "You" + the agent's task (`agent.task`);
   omitted when there is no task.
5. Meta line: `name · project · mac · ⌥ branch`, breaking only after a
   separator (Ole's `MetaLine` grows into this).
6. Option rows (gap 4), each 44 pt min, `surface2` fill, 1 pt `line`
   border, number in mono accent, label Medium, optional detail:
   - Approval: `1 Yes…` → allow; `2 Yes, and don't ask again` / detail
     `asks once more` → always; `3 No, and tell Claude what to do instead`
     (destructive: `signalTint` fill, signal border, signal number and
     label) → opens Ole's deny-with-message path.
   - Question: the agent's choices, numbered, from `AgentCopy.choices`;
     no destructive row.
   - Error: one row `1 Resume` and a destructive `2 Close`.
   - **Risky approvals** (`RiskRule.isRisky`): the rows show, but `Yes`
     and `Yes, and don't ask again` first ask a confirmation dialog
     ("Changes the database or remote state." Allow / Cancel). The
     owner's decision (7 Nov); Ole's inline rows refused to answer these
     at all, and this replaces that rule on Home and in the grid.
   - Rows are disabled while `pending` or the Mac is unreachable, as
     today.

**Task grids.** A heading row (Space Grotesk 19 + mono count, signal
for Needs you, muted otherwise), then a 2-column `LazyVGrid`, gap 10, of
cards: `surface` fill, 1 pt `line` border, padding 8, gap 4. On regular
width (iPad) 3 columns; otherwise identical.

- **Needs you** grid = `fleet.needsYou.dropFirst()`. Card: title with a
  leading signal "! " (`AgentCopy.headline`), file/command block (mono 10
  accent) when present, "You" panel (task) when present, meta line, then
  the action row (32 pt, gap 6): approval → `Yes` (accent fill) and `No…`
  (surface, signal border); question → up to the first 2 choices as
  `surface`/accent-border buttons (more → "Open"); error → `Resume` and
  `Close`. Risky approvals: `Yes` asks the same confirmation. Tapping the
  card body pushes the agent view.
- **Ready to review** grid = `fleet.readyToReview`. Card: title = name,
  `summary` as the request text, meta; actions `Open` (accent) and
  `Close` (surface, line border → `.close`).
- **Working** grid = agents in `working`/`starting`, newest first. Card:
  title = name, headline line, meta; action `Stop` (surface, signal
  border → Ole's stop with confirm) and `Open`.

**Console** (bottom, `safeAreaInset`): `background` fill, 1 pt `line` top
edge, padding 16 horizontal, 12 top, home-indicator inset below; two
buttons 64 pt tall, gap 10:
- **Speak**: accent fill, mic icon 24 + "Speak" 22 Semibold onAccent,
  about 60 % of the width (the Figma has 252 of 408). It carries Ole's
  mic gesture unchanged: touch down starts listening; tap opens the
  capture sheet listening; hold shows `TalkPanel` and release opens the
  sheet with what was heard; sliding off cancels.
- **Type**: `surface` fill, 1 pt accent border, keyboard icon + "Type" 17
  Semibold accent; opens the capture sheet typing.

## Layout: everything else

Tokens, square corners and bordered controls replace glass and capsules;
layouts stay as Ole built them.

- **Agent view**: `background`; the dock becomes a Neon panel (`surface`,
  1 pt `line` top edge) whose approval/question controls are the same
  numbered option rows as the hero; message field = `surface` with `line`
  border, Send = accent square. Terminal placeholder unchanged.
- **Capture sheet**: sheet background `surface`, 1 pt accent top edge
  (the console's sheets carry the accent, cards carry `line`), grabber
  `line`; "Listening…" heading in Space Grotesk 20 with a signal dot and a
  mono timer; waveform bars in accent; transcript in SF Pro 17; hint in
  11 muted; `Where` row as a `surface2` option row with a chevron;
  `Keep for later` = `surface` + accent border, `Start now` = accent
  fill, both 58 pt.
- **TalkPanel**: `surface`, 1 pt accent border, radius 2.
- **KeptToast**: `surface`, 1 pt `line` border, Undo in accent.
- **Agents, Macs, Settings, Add Mac**: `List` stays; rows `surface` with
  a 1 pt `line` border between them (list row separators in `line`),
  section headers in Space Grotesk 19; primary buttons accent fill,
  secondary `surface` + accent border, destructive `surface` + signal
  border; searchable field tinted accent.
- **Onboarding**: same token swap; main buttons accent fill; the horizon
  glow becomes a low-opacity accent glow; HesperMark's full stop goes
  accent → signal as today.
- **Button styles**: `primaryAction()` = accent fill, onAccent label,
  radius 2; `secondaryAction()` = `surface` fill, 1 pt accent border,
  accent label; new `destructiveAction()` = `surface`, signal border,
  signal label. `.glassProminent`/`.glass`/`.glassEffect` disappear from
  HesperUI.

## Dark only

`project.yml` sets `UIUserInterfaceStyle: Dark` for the app target and
`RootView` applies `.preferredColorScheme(.dark)`. `Theme.Token` resolves
to one colour; the `UIColor` provider stays so a light set can return.

## iPad

No separate design. The same screens; the grids use 3 columns on regular
width; the `TabView` with "New task" goes, iPad also gets the console.

## Where logic goes (and tests)

HesperUI has no tests and `make -C apps/ios check` only compiles it, so
every piece of logic lands in HesperKit with Swift Testing tests:

- `Fleet.priority` (the hero) and `Fleet.needsYouRest`.
- `HomeScope` (all / needsYou / working) → which sections and which
  agents, and the three counts.
- `DecisionOptions.rows(for: agent)` → the numbered rows (label, detail,
  tone, command) for approval, question, error, risky.
- `NeonPalette` (the hex table above) lives in HesperKit or HesperCore
  so a test pins every value against this document.

## Docs and files

- `docs/iphone/concept.md`: replace "Visual language"; in the IA block,
  replace the tab bar and mic lines with the scope bar, the menu and the
  console. `apps/ios/README.md`: layout table (Fonts folder, Home).
- `THIRD_PARTY_NOTICES.md`: the two fonts.
- This document and `neon-home.png` stay as the reference.

## Verification

- `make -C apps/ios check` green (lint, HesperKit tests, simulator build).
- `make -C relay build && make -C app test-unit test-fake` green if
  `app/Package.swift` or HesperCore changes.
- Simulator screenshots (iPhone 17, dark) of Home in all three scopes,
  the capture sheet listening and typing, the agent view, Agents, Macs,
  Settings and the first onboarding screen, with Ole's fixtures
  (`FixtureLink`), placed next to the Figma render for the owner.
- Device build installed on the owner's iPhone 16 Pro (the one that is
  paired here) and launched.

## Left to the builder

- Exact Dynamic Type `relativeTo` choices beyond the table.
- How the hero's command block is derived for questions (none) and
  errors (the last line).
- Splitting into commits: fonts and tokens; Home; console and capture;
  menu and navigation; remaining screens; docs. Each builds and passes.
