# Wireframe critique, round by round

Each round is kept as it was drawn. This file holds the critique that
produced the next round. Concept: [concept.md](concept.md).

## Round 1 → round 2

[Round 1](wireframes/round-1.html) drew the screens straight from the job
list. Problems, worst first:

1. **The Mac filter leads the home screen.** A segmented All · mini ·
   studio · air control makes the machine the first decision, but the owner
   thinks in tasks and projects. It also breaks at four or more Macs.
   → Drop it. The Mac becomes metadata on each row and a tab of its own.
2. **"Agents" mixes two jobs.** Waiting-on-me and everything-running share
   one scroll. With six working agents, a new question can sit below the
   fold. → Split: **Inbox** (needs you, ready to review; can be empty) and
   **Agents** (everything, grouped by project).
3. **The floating + fights iOS 26.** A FAB covers the last row and sits
   where the Liquid Glass tab bar and its accessory go. → New agent moves
   into the `tabViewBottomAccessory`, with a mic.
4. **New agent is a form with six decisions before the task.** The task is
   the point; destination and kind have good defaults (last-used project,
   its Mac, Claude). → Text first, one destination line, Options collapsed.
5. **The agent view can't be read.** A 120-column terminal drawn at phone
   width is 6 pt text. → Fit width with a readable minimum, then pan;
   double tap to toggle; pinch to zoom.
6. **Page dots on the agent view.** Swiping between agents fights panning
   the terminal sideways. → No paging. After an answer, "Next: api-docs ›".
7. **The answer box floats mid-screen** under the terminal, out of thumb
   reach, and the reply field sits below it as a second competing bar. →
   One dock at the bottom that changes with the state: decision, reply,
   resume.
8. **Allow is offered for everything inline.** "npm run db:migrate" got a
   one-tap Allow on the list and on the lock screen. → Risky commands get
   Open only; the Mac's risk rules decide.
9. **Live Activity shows agent names.** The relay only knows ids and states;
   names would have to come from the app, which is suspended. → Counts on
   the activity; names only when the app could fill them.
10. **Review offers Accept & commit as the default verb** regardless of
    evidence. → Primary action follows evidence and risk.
11. **Done agents and review look the same.** Both are "finished". → Done
    with changes = Ready to review (Inbox); done without changes stays in
    Agents only.
12. **Dots for state.** The Mac uses squares. → Squares everywhere.

What worked and stays: inline answers on rows, the Macs list with route and
latency, evidence on the review screen, the lock-screen pairing of a
notification with a fleet summary.

## Round 2 → round 3

[Round 2](wireframes/round-2.html) fixed the structure. Rendered at real
size, it shows new problems:

1. **The terminal is unreadable at fit width.** A 120-column PTY at phone
   width is about 5.5 pt. Fit width only works up to about 80 columns.
   → Default to **Readable** (about 9 pt, lines start at the left edge,
   pan for the rest) when the PTY is wider than 90 columns; Fit width when
   it is narrower; landscape shows the full width. A small "120 cols →"
   label says there is more.
2. **The terminal is top-anchored.** The view attach is anchored to the
   bottom, and so is the agent's work: the newest line belongs just above
   the dock. Round 2 left a black void under the content. → Bottom-anchor;
   older output scrolls up.
3. **Too much signal red.** Red marks plus red Allow buttons on the Inbox
   and the agent dock: "if two things glow, neither one stands out"
   (design system rule 2). → Signal stays on the state marks and the tab
   badge. Allow is the tinted primary in the accent (`working`).
4. **The TUI menu and the dock say the same thing.** Both show the edit
   prompt. That is acceptable, since the terminal is the ground truth, but
   the dock must quote the exact command or path so the two can be
   compared at a glance. → Dock shows tool, target and a "Risky" reason
   when there is one.
5. **The tab bar is not iOS 26.** Text-only tabs in a thin bar. → Floating
   glass capsule with symbols and labels, search as its own circle, the
   accessory above it, minimised on scroll.
6. **Memory bars on Macs decide nothing.** → Show what the owner acts on:
   needs-you count per Mac, asleep / unreachable since when, on battery.
7. **Missing states.** No empty Inbox, no offline Mac in a list, no
   "sent, waiting for mini", no Dynamic Island, no widget, no Daylight. →
   Round 3 draws them.
8. **Review does not say where Accept goes.** → "Commits to fix/login on
   mini" under the button; risky files flip the primary action to Review
   on Mac.
9. **New agent hides the mic.** It moved to the accessory but the sheet
   itself has none, so a typed start can't switch to voice. → Mic button in
   the sheet; "Options" names what it holds (worktree, profile).
10. **Instructional text in rows** ("swipe to resume"). → Swipe actions
    are discoverable by convention; the row says "Exited 5h ago".
11. **Lock screen doubles up.** The Live Activity's "3 need you" and three
    notifications are the same news. HIG: don't alert from a Live Activity
    for what a notification already says. → The activity updates silently
    and leads with progress ("5 working"); notifications carry the alert.

Kept: the Inbox / Agents split, inline answers, evidence before files in
Review, Macs with plain status lines, the accessory for New agent.

## Round 3: what is still open

[Round 3](wireframes/round-3.html) is the version the concept describes.
Looking at it rendered, these are left for a prototype on a device, not
for another paper round:

1. **Readable mode is at the edge.** 9 pt mono is readable on an iPhone
   held close, not at arm's length. Only a device test with real Claude and
   Codex output can settle the minimum size and whether Fit width or
   Readable is the better default.
2. **Short output leaves the top of the terminal empty.** With scrollback
   loaded it fills; on a fresh agent it looks broken. Fill with scrollback
   on attach (`SCROLL`), or start the view at the top until the screen is
   full.
3. **Lock screen counts appear twice** (widget and Live Activity). Pick
   one per moment: the widget when nothing runs, the Live Activity while
   agents work.
4. **New agent has dead space** between the destination rows and the
   keyboard. Recent tasks or "Continue fix-login" could live there, if
   that proves useful rather than noisy.
5. **"Review on Mac" hides in ⋯** when Accept is the primary action. Fine
   for low-risk work; the risky variant (primary = Review on Mac) is
   described but not drawn.
6. **Swipe actions** (Stop, Close, Resume) on Agents rows are described,
   not drawn.

## Round 3 → round 4

After round 3, the [writable-terminal research](writable-terminal.md)
changed the agent view: the phone types into the live terminal at the
Mac's size (Mirror), takes the size only on an explicit Fit to iPhone tap,
rotates the agent view, may type into shells behind Face ID once per
attach, and comes to iPad. [Round 4](wireframes/round-4.html) draws that,
and the round 3 leftovers that a paper round can still settle.

1. **Round 3 had no way into the terminal.** The dock's key strip sent keys
   through `agents.input`, which is fine for esc and ^C and wrong for
   anything longer. Now a tap on the terminal, or ⌨ in the dock, puts the
   focus in it; the key strip moves above the keyboard as a key bar with a
   sticky ctrl and paste, and leaves the dock.
2. **Fit to iPhone must be visible, not in ⋯.** It is the one action that
   changes something on the Mac, so it sits on the terminal next to the
   grid size ("Mac's size · 120 × 32"). When fitted, it turns into the
   accent and the way back ("Mac's size") sits in the same place.
3. **The Mac's side was missing.** Fit to iPhone resizes a tile on another
   screen. Round 4 draws that tile: the phone's grid centred, marked On
   iPhone, with how to take it back. And the phone's side when the Mac does
   take it back: one banner, then Mirror.
4. **Shells needed a gate.** Typing into a shell is a stronger right than
   answering Claude. A sheet asks once per attach and keeps "Just watch"
   as a full answer.
5. **Landscape with the keyboard up is cramped.** All 120 columns fit but
   about eight rows remain. That is acceptable for a short command; the
   caption says long text belongs in the reply field. Not worth a special
   layout.
6. **Empty terminal tops (round 3, item 2) are gone**: a read-write attach
   brings the scrollback, so the view is full from the first frame.
7. **Drawn from the round 3 list**: the risky review (Review on Mac leads,
   the reason under it), swipe actions on Agents rows, and one count per
   moment on the lock screen (the Live Activity while agents run, the
   widget otherwise).
8. **iPad** gets a sidebar with the tabs and the agents, and the agent view
   as the detail column at 120 columns. Windows show the one rule that is
   new on iPad: one size owner per agent.

## Round 4: what is still open

1. **Two ways to type.** The reply field and the terminal both take text.
   The rule (whole messages in the field, keys in the terminal) is clear on
   paper; a device test must show whether people pick the right one or
   find the field redundant once the terminal is writable.
2. **Fit to iPhone while the keyboard toggles.** The grid changes from about
   46 × 19 to 46 × 40 when the keyboard hides. Debounced, that is still a
   redraw on the Mac each time. Maybe the grid should keep the keyboard-up
   size for the whole attach.
3. **Who sees "On iPhone"?** If someone else sits at the Mac, a tile that
   suddenly shrinks is surprising. The banner on the tile may need the
   phone's device name and a time.
4. **iPad without a hardware keyboard** falls back to the iPhone key bar;
   not drawn.
5. Still open from round 3: Readable size on a device (item 1), New agent's
   dead space (item 4).
