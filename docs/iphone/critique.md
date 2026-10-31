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
