# Hesper for iPhone: concept

Status: concept, 31 Oct 2026; capture updated 2 Nov 2026; the terminal's
first version (watching) built 7 Nov 2026. Wireframes in
[`wireframes/`](wireframes/) (round 4 draws the writable terminal and iPad,
round 5 the mic circle that replaced the Search tab), critique between rounds
in [`critique.md`](critique.md), the writable terminal researched in
[`writable-terminal.md`](writable-terminal.md).

This concept starts from core Hesper as it is on `main`: hesperd on each
Mac, the relay, the wire protocol and the Mac app's features. It does not
build on the earlier iPhone prototype. Where the phone needs something core
Hesper does not have yet, the gap is listed in [Core work](#core-work).

## The idea

The phone is a remote for a fleet of agents that live on the owner's Macs.
It answers one question first: **does anything need me?** Then it lets the
owner start work from anywhere, follow it, steer it and judge the result.
Everything else stays on the Mac.

Three rules hold the design together:

1. **The Macs do the work, the phone decides.** No agent runs on the phone,
   no state is owned by it. The phone is a view and a set of verbs, like
   Hesper.app is a view of hesperd.
2. **Attention first, machines second.** The owner thinks in tasks and
   projects, not in Macs. The Mac is metadata on every row and a tab of its
   own for when it matters (asleep, offline, pairing).
3. **The real terminal, writable, without taking the Mac's size.** The
   agent view shows the agent's actual terminal (decided: live terminal
   first), and the owner can type into it as on the Mac (decided). It
   keeps the Mac's size unless the owner taps **Fit to iPhone**. Decisions
   and replies stay native controls under it, so the owner never has to
   aim at a TUI menu with a thumb.

## Core jobs

| # | Job | Where on the phone | Core method |
|---|---|---|---|
| 1 | See what needs me, across every Mac | Inbox tab, lock screen, Live Activity, widget | `agents.link` events (channel 0) |
| 2 | Answer or approve | Inbox row, agent dock, notification action | `agents.answer`, `agents.input` (digit for choices) |
| 3 | Start an agent, or keep a task for later, on any Mac and project | Mic circle next to the tabs, Siri / Action button | `agents.spawn`, `drafts.save`, `projects.recent`, `profiles.list` |
| 4 | Follow and steer | Agent view: live writable terminal plus dock | `agents.attach` `rw` (`owner: false`; `owner: true` after Fit to iPhone); keys as frame type 0 |
| 5 | Stop, resume, close | Agent menu, swipe on rows | `agents.stop`, `agents.resume`, `agents.close` |
| 6 | Judge the result | Review sheet from the Inbox | `review.list`, `review.diff`, `review.evidence`, `review.accept`, `review.sendBack` |
| 7 | Know the Macs are fine | Macs tab | inventory (`machines`, `machine.updated`), `snapshot` stats |

Move to another Mac is deliberately **later, through a Mac** (decided): the
phone will ask the source Mac's hesperd to run the move, because the move is
orchestrated by a controlling hesperd and has refusal branches the phone
should not reimplement.

## Information architecture

```
Home             scope bar (All · Needs you · Working) + menu (Agents,
│                Macs, Search, Settings); the priority decision; task grids
├── Agents       everything that runs, grouped by project, Mac as meta;
│                a search field (agents, projects, past sessions) and
│                Later (tasks kept for later, parked drafts on every Mac)
└── Macs         machines, their health, pairing
Console          Speak (push-to-talk) and Type at the bottom
Agent view       pushed from any row; live terminal (watching; typing
                 later) + dock;
                 portrait and landscape (iPad: the detail column)
Sheets           Capture · Review · Deny with a message · Pair a Mac
System surfaces  notifications · one fleet Live Activity · widgets · controls · App Intents
```

Why Home, a menu and a console (the Neon redesign, [`neon.md`](neon.md),
replaced the three tabs and the mic circle on 7 Nov):

- **Home** is the Mac's ⌘J and ⌘R in one place: the one decision that
  matters most on top, then what needs the owner, what is ready to review
  and what is working. The scope bar narrows it to Needs you or Working.
  Needs you can be empty, and empty is the goal ("Nothing needs you").
  Codex, Claude Code Remote Control and Cursor all lead with one run list
  that flags "needs input"
  ([Codex mobile](https://learn.chatgpt.com/docs/mobile.md),
  [Remote Control](https://code.claude.com/docs/en/remote-control),
  [Cursor](https://docs.cursor.com/background-agent/web-and-mobile)).
- **Agents** is the Wall without tiles: every agent, grouped like the
  Mac's automatic bands (by project; one level dropped when everything is in
  one project). Rows carry the agent's own activity line, not a terminal
  preview, so the list costs no terminal streams.
- **Macs** (in the menu) exists because machines fail in ways the owner must see (lid
  closed, offline, unpaired). A plain status line beats a dot: Tailscale
  found that an activity dot "caused more confusion"
  ([forum](https://forum.tailscale.com/t/online-status-in-app/2107)).
- **Search** is the field at the top of Agents (`.searchable`); the menu's
  Search opens Agents with it focused. On a phone the owner drops tasks
  far more often than they search, so capture gets the bottom of the
  screen, not search (decided, round 5).

**The console** at the bottom of Home holds Speak and Type. Speak listens
from the moment it is touched: a tap opens the capture sheet still
listening; holding is push-to-talk (the words show at the top of the
screen, letting go opens the sheet with them, sliding off cancels). Type
opens the sheet with the keyboard. It is the app's own bar, not a system
tab bar, because no system control carries push-to-talk. Pushed screens
such as the agent view have no console; iPad gets the same console.

## Key flows

### 1. Unblock from the lock screen (10 seconds)

1. An agent on mini asks to run `npm run db:migrate`. hesperd's state
   change reaches the relay; the relay sends a push with ids only.
2. The notification service extension opens the end-to-end channel, reads
   the agent and writes the real text: "migrations on mini wants to run
   npm run db:migrate".
3. Actions: **Allow**, **Deny**, **Reply**. Allow is first so Apple Watch
   double tap runs the safe, expected answer
   ([HIG notifications](https://developer.apple.com/design/human-interface-guidelines/notifications.md)).
   All three require authentication, because they run code on a Mac.
4. Before acting, the app checks the prompt is still the one shown (state
   and `stateSince`, later a prompt id). If not: "This was already answered
   on mini."
5. Risky commands (migrations, `rm`, pushes to `main`, anything the Mac
   flags) get **Open** only. Never a blind Allow for those.

### 2. Triage the Inbox (one minute)

Inbox shows Needs you (approval, question, error, oldest first like the
Mac's attention queue) and Ready to review. Each needs-you row has the
question in plain words and its answers inline. Answering removes the row
with a short undo window that only covers the local send, not the agent.
A question with choices shows its choices as buttons (up to 4 on the row,
more in the agent view). A question with no options says "Open to answer".

### 3. Drop a task from anywhere (20 seconds)

1. Hold **Speak** on Home's console and speak, then let go; or tap it and
   speak until Done. **Type** opens the sheet with the keyboard.
2. One sheet: the text first (live while listening, editable after), then
   one destination line
   "hesper on mini" (the last-used or most recent project from
   `projects.recent` across Macs), then the agent kind ("Claude"). Options
   (profile, worktree, branch) sit behind **Options**, collapsed.
3. **Start now** sends `agents.spawn`. The sheet closes into the agent view
   in `starting`. If the Mac is asleep or offline, the destination line
   says so before Start, and offers the other Macs that have the project.
4. **Keep for later** sends `drafts.save` with `parked: true` to that Mac:
   the task waits on its wall as a parked draft and under Later in Agents,
   with Start. "Kept on mini" shows for a few seconds with Undo. Kept tasks
   never count toward the badge.

Voice uses on-device `SpeechTranscriber`: volatile text dimmed, final text
solid, editable before sending
([WWDC25-277](https://developer.apple.com/videos/play/wwdc2025/277/)).
It listens in one language, which the owner picks in onboarding. Until
then it is the owner's first language the model knows: the keyboard's
dictation languages, then the preferred languages, not the phone's region
(English on a phone in Germany is `en-US`, a bare "de" is `de-DE`). There is
no language switch in capture and no choice of model. Mac, project and agent
names are passed to the model as words to expect. A task goes to the agent
in the language it was said in.

### 4. Follow and steer

The agent view is the agent's real terminal. Under it sits the dock:

- **needs you**: the decision as native buttons (Allow · Always · Deny…,
  or the numbered choices). Deny… opens a field for "tell Claude what to do
  instead".
- **working, done, idle**: one full-width **Send** that presses Enter;
  typing goes into the terminal. A tap on the terminal brings up the
  keyboard with a key bar (Esc, Tab, Ctrl, arrows); a second tap or a
  swipe down puts it away. Every key goes straight to the agent, as on
  the Mac, into Claude Code's own prompt. A fitted terminal shrinks to
  the rows above the keyboard.
- **exited**: Resume and Close.
- **error**: the error line, Open on Mac, Resume, Close.

There is no horizontal swiping between agents here: the terminal pans
sideways. After answering, the dock offers **Next: api-docs ›** when
another agent needs the owner.

### 5. Judge the result

Ready-to-review rows open the Review sheet (decided: triage, not line
review). It shows, in this order: the agent's last message, evidence (tests
and builds with fresh / stale / missing, from `review.evidence`), the files
with their size of change and risk flags, then the verdict.

- **Accept & commit** is the primary action only when evidence is fresh
  and no file is flagged (migrations, auth, schema, CI). Otherwise the
  primary action is **Review on Mac** and Accept sits in the menu, asking
  twice. This follows the Mac's rule that bulk accept is only for low-risk
  changes with fresh evidence.
- **Send back** takes a typed or spoken note and sends it with
  `review.sendBack`.
- A file opens a read-only unified diff (fetched in parts as the protocol
  allows). Reading a diff is possible, reviewing hunk by hunk is the Mac's.

### 6. Pair a Mac

Macs tab, **Add Mac**. The phone shows its code (`ABC-DEF`); the owner
approves on the Mac (`hesperctl approve CODE` or the Mac's notification),
comparing the code. Rights are chosen on the Mac. The phone never pairs
shells without Face ID and the strong key.

## Surfaces outside the app

**Notifications.** One per prompt, threaded per agent (`thread-id`), collapsed
per agent, `time-sensitive` only for approvals and questions; errors are
`active`; finished work is `passive` with a relevance score
([WWDC21-10091](https://developer.apple.com/videos/play/wwdc2021/10091/)).
Never re-notify for the same prompt; remove the delivered notification when
the prompt is answered anywhere. No push while the owner is active at the
Mac that raised it, like Claude Code's presence rule
([Remote Control](https://code.claude.com/docs/en/remote-control)).
Hidden previews read "Agent needs you".

**One fleet Live Activity** (decided). "5 working · 2 need you" with the
oldest waiting agent's name, plus one button, **Open next**. It leads with
progress and updates silently: the alert for a prompt is the
notification's job, not the activity's. The relay can
update it by push without seeing content, because the counts come from the
snapshot it already has; names are filled from the app's cache when it
runs. Rules from Apple: one activity that rotates rather than many,
priority-5 updates for progress and 10 only when something starts
waiting, `stale-date` so a lost relay shows stale instead of false
progress, end it 15 minutes after everything is idle, 8 hours at most
([HIG Live Activities](https://developer.apple.com/design/human-interface-guidelines/live-activities.md),
[ActivityKit push](https://developer.apple.com/documentation/activitykit/starting-and-updating-live-activities-with-activitykit-push-notifications.md)).
It starts when the app goes to the background with agents working, and
by push-to-start when the first agent needs the owner. It also shows in the
Mac menu bar and on the Watch for free
([WWDC25-278](https://developer.apple.com/videos/play/wwdc2025/278/)).

**Widgets.** Lock screen accessory (rectangular: "2 need you · 5 working";
circular: the needs-you count) and a small home widget listing up to three
waiting agents with Open. Widget push updates keep the count fresh
([WWDC25-278](https://developer.apple.com/videos/play/wwdc2025/278/)).
Buttons in widgets do not approve: they are inactive while locked anyway
([interactivity doc](https://developer.apple.com/documentation/widgetkit/adding-interactivity-to-widgets-and-live-activities.md)).

**Controls and intents.** A Control Center / Action button control
"New task" that opens straight into dictation; Keep for later works
without unlocking, Start now asks for Face ID. App Intents: "Start an
agent", "Show what needs me", "Stop agent". Any intent that approves asks
for confirmation (`requestConfirmation`,
[WWDC25-275](https://developer.apple.com/videos/play/wwdc2025/275/)).

## The terminal on a phone

**Built so far (8 Nov 2026): watching and scrolling.** The agent view
attaches read-write (`mode: rw`, right `type`) when the phone has that
right, read-only (`mode: ro`, right `observe`) otherwise, with no Face
ID, and draws the Mac's grid with no keyboard; a vertical drag scrolls
the agent as wheel input, with momentum. Replies, answers and Speak go
through the dock.
Readable and fit width, pinch, double tap, pan, "Live ↓" and landscape
work as below; the view follows the last row with text rather than the
cursor, which the renderer does not report. Typing, Fit to iPhone,
selection and links are still to come.

The terminal is writable, like the Mac's. Typing and owning the size are
separate in hesperd, so the phone can type without resizing anything
([research](writable-terminal.md)). It renders with libghostty through
libghostty-spm's `GhosttyTerminal` view (decided). That view brings the
iOS input layer: keyboard and IME, hardware keys, key bar, touch selection
and scrolling.

**Mirror** (default). A read-write attach without owner (`agents.attach`,
`mode: rw`, `owner: false`). The phone draws the PTY at exactly the Mac's
`cols × rows` and scales or pans the view, never the grid. A TUI then
never garbles, and the Mac's tile is untouched.

- **Readable** (default when the PTY is wider than about 90 columns): a
  fixed font of about 18 pt, a "120 cols →" label, pan for the rest. **Fit
  width** (default for narrower PTYs): the font scales so every column
  fits. Double tap toggles; pinch zooms. While typing, the view follows
  the cursor.
- **Landscape** (decided). The agent view rotates and shows more
  columns at the same font. The dock moves to the side. The rest
  of the app stays portrait on iPhone.
- **Bottom-anchored**: the newest line sits just above the dock or the
  keyboard.

**Fit to iPhone** (decided: an explicit tap each time, never automatic).
The phone becomes the size owner (`owner: true`) while the agent view is
open. The agent redraws at the phone's grid, about 46 × 19 at 11 pt in
portrait with the keyboard up. The Mac's tile becomes a view marked **On
iPhone**. The Mac takes the size back on its next keystroke or click in
that agent (the latest owner wins), and the phone drops back to Mirror. The
phone lets go when the view closes or the app leaves the front. Resizes
from the keyboard showing or hiding are debounced. This needs the core
work in items 9 and 10 below. With an older hesperd only Mirror is
offered.

- **Keyboard**: the software keyboard with autocorrect, smart quotes and
  inline predictions off. IME works (marked text). A key bar above it
  holds esc ⇥ ← ↑ ↓ → ⏎ ^C, a sticky ctrl and a paste control (no paste
  prompt). Hardware keyboards get Ctrl and Esc ahead of iOS's own
  shortcuts. Dictation and Scribble arrive as text. Keys follow the
  program's modes (application cursor, bracketed paste, kitty keys), as
  on the Mac.
- **Cost**: one signed `agents.attach` per opening, with right `type` and
  no Face ID. After that, keystrokes are sealed data frames, never signed
  one by one. Shell agents need right `shell`, so Face ID and the strong
  key once per attach (decided: the phone may type into shells).
- **Scrollback**: the read-write attach brings up to 10,000 lines. Then
  drag to scroll locally; "Live ↓" returns. Programs that capture the mouse
  get the scroll instead.
- **Selection**: long press with handles and the loupe, then copy.
- **Links**: a tap opens a URL after a confirmation, never by itself.
- **Latency**: about 45 ms to echo through the relay, under 1 ms on the
  direct path. No local echo prediction in v1: Claude Code draws its own
  input box, so a mosh-style prediction would be wrong there. Long text
  goes through the reply field and Speak.

Terminal apps on phones show the same lessons: zoom works; the missing
Esc/Ctrl/arrows and fragile connections are what hurts
([Blink](https://docs.blink.sh/),
[Termius](https://termius.com/blog/new-touch-terminal-on-ios)). The key
bar and the foreground-only attach answer both.

## iPad

In scope (decided). One app, adaptive:

- **Layout**: a split view with Inbox / Agents / Macs / New task in the sidebar and
  the agent view as the detail column. On a narrow window or in Slide
  Over it is the iPhone layout.
- **Windows**: each window is its own scene. In iPadOS windowing and Stage
  Manager, the owner can open agents side by side. With Fit to iPhone
  ("Fit to iPad"), a window resize resizes the PTY, as with a Mac window.
  There is at most one size owner per agent.
- **Hardware keyboard**: the main input on iPad. Cmd shortcuts for the
  app (⌘N new agent, ⌘F search, ⌘1–4 tabs). In the terminal everything else
  goes to the agent. Esc has its own key or Caps Lock → Escape in the
  system settings, because Cmd-. is unreliable. The key bar hides while a
  hardware keyboard is attached.
- **Pointer**: trackpad selection and scroll in the terminal, hover on
  rows.
- **External display**: the iPad's own window there, through iPadOS
  windowing. The iPhone mirrors only.

## Visual language

The Neon control console, the owner's Figma direction; tokens, type and
layout are in [neon.md](neon.md), which supersedes this section's
earlier Liquid Glass version:

- **Neon tokens**: dark background, cyan accent for action, magenta
  signal for "needs you"; every colour comes from `Theme`.
- **Square corners**: 2 pt on everything; the state marks stay 8 pt
  squares.
- **No glass**: surfaces are solid with 1 pt borders.
- **Type**: Space Grotesk Bold for headlines, JetBrains Mono for commands,
  counts and captions, SF Pro for the rest, all at Dynamic Type sizes.
- **Dark only** for now.

## What the phone leaves out

| Mac feature | On the phone | Why |
|---|---|---|
| Wall of live terminal tiles, bands, layouts | List with activity lines | Tiles at phone size are unreadable and each costs a stream |
| Desks | Out | Window arrangements have no meaning on a phone |
| Palette (⌘K) | Search in Agents | Search covers the finding; verbs live on rows |
| Projects and groups management (colours, rename, move, promote) | Out; projects are picked, not managed | Rare, careful work |
| Scratch lifecycle (keep, archive, promote) | Only "new scratch" when starting | Same |
| History: fork, continue as, archive, delete | Search and Resume only | Resume is the phone job |
| Review hunk by hunk, bulk accept | Triage only | Reading diffs is the Mac's job |
| Move between Macs | Later, through a Mac | Decided |
| Starting shell agents | Later, behind Face ID and the strong key | Highest risk right; typing into an existing shell is in (Face ID once per attach) |
| Attach files | Later (photo of a screen or sketch) | Nice, not core |
| Drafts | Parked drafts only: Keep for later makes one, Later lists them | Ideas dropped on the phone wait on the Mac's wall |
| Density, themes beyond Dusk / Daylight | Out | System settings cover it |

## Performance

**Startup.** First frame from cache, network after. The last inventory and
agent list are cached in the app group (encrypted, protection
`completeUntilFirstUserAuthentication` so the widget and extension can read
them). Target: first frame under 400 ms, live state under 1 s on the LAN
(direct path) and under 2 s through the relay. Rows show "as of 21:14"
until the link is live
([launch time](https://developer.apple.com/documentation/xcode/reducing-your-app-s-launch-time.md)).

**Streaming.** One `agents.link` per Mac with channel-0 events
(`list` then `changed` / `removed`). That is all the lists need. Exactly one
terminal attach at a time: the agent on screen (on iPad, one per window).
It is detached, and the size given back, when the view goes away and when
the app backgrounds. On return, the first frame is a full redraw, about
46 ms through the relay. Parsing a Claude Code flood (about 0.3 MB/s)
costs well under 1% of a core. Rows update at most at the display rate,
coalesced per agent.

The phone buffers at most sixteen terminal frames of up to 256 KiB each
while the renderer catches up. If it falls further behind, it closes only
that attach. The terminal shows Reconnecting, opens a new attach and takes
the Mac's full redraw; it never continues drawing a stream with missing
bytes. Other attaches and the Mac's link keep reading.

**Battery.** No background sockets: WebSockets do not run in the
background and the app is suspended shortly after leaving the foreground
([forum](https://developer.apple.com/forums/thread/716118)). While
backgrounded, everything comes from alert pushes and Live Activity pushes.
No background-fetch polling. The terminal renders only while visible, and
ProMotion runs high only while scrolling.

**Offline and flaky networks.**

- A Mac that drops keeps its agents for a minute (protocol rule), then
  shows "mini unreachable since 21:14" with its rows dimmed and verbs
  disabled, saying why.
- Mutations are never retried automatically; a timed-out answer shows
  "Sent, waiting for mini" until the next event confirms or denies it.
- An unsent capture text survives app kills.
- On foreground: reconnect, take a fresh inventory, rebuild. Direct path
  first on the same network, relay as fallback.

## Core work

What core Hesper (`main`) needs before this app can ship, in order:

1. **A mobile client library**: the controller side of the relay
   protocol (enrollment, device and strong keys, `devices.request`, signed
   requests, Noise channel, links) usable from Swift. *Built*:
   `HesperLink` in `apps/ios/Packages/HesperKit` (`LiveLink`,
   `LivePairing`) and read-only attaches (item 8); still to come: the
   direct path.
2. **Push from the relay**: APNs alerts on entering approval, question or
   error, ids only; the phone fills in text end to end. Plus a presence
   signal so a Mac in active use suppresses pushes.
3. **Live Activity and widget pushes** from the relay, counts only.
4. **A prompt id** on `attention`, so an answer can never land on a newer
   prompt than the one the owner saw.
5. **`sessions.search` and `sessions.resume` over the relay** for search
   and Resume (today History is local to each Mac).
6. **`agents.result` over the relay** (or the last message in
   `ReviewItem`) for the Review sheet and Done rows.
7. **`agents.move` driven by a Mac on the phone's behalf** (later).
8. **Attach channels in the mobile client library**: open an
   `agents.attach` on a link, then read and write its `D` frames and close
   it with `X`. No protocol change. *Built* for watching:
   `HostChannel.attach` opens a read-only attach and reads its `D` frames;
   writing them comes with typing.
9. **Who owns the size, in hesperd**: record whether the size owner is
   local or a remote device. Publish it as `sizeOwner` in the agent's info
   and events, and set `capabilities.sizeOwner` in the snapshot. Both are
   additive; update `relay/docs/protocol.md`. This is needed for Fit to
   iPhone.
10. **The Mac yields to a remote owner**: while `sizeOwner` names another
    device, `SizeOwnership` makes that agent's panes fit views marked "On
    iPhone"; a keystroke or click takes the size back.
11. **Drafts over the relay**: `drafts.list`, `drafts.save`,
    `drafts.remove` and the `drafts.changed` / `drafts.removed` events on a
    phone's link, for Keep for later and Later (today drafts are local to
    each Mac's hesperd). The phone names the project; hesperd resolves it
    to the project's folder on that Mac, as `hesperctl drafts save
    --project` does.

## Open decisions

- Inbox and Agents as two tabs, or one list sorted by attention? The
  wireframes argue for two; one tab is simpler if the owner rarely runs
  more than five agents.
- Should Allow from a notification require Face ID every time, or only for
  commands the Mac flags as risky?
- Which project does capture default to: last used on the phone, or the
  most recently active on any Mac?
- Does a kept task need a Mac and project, or can it stay unassigned until
  it is started?
- Does finished work notify at all (passive), or only show in the Inbox and
  Live Activity?
