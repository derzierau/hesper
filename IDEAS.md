# Feature ideas and requested changes

An editable inbox for proposed improvements to Hesper. Add ideas below, starting
with the problem and desired outcome. These are proposals, not delivery
commitments. Discuss larger changes in a GitHub issue before implementation
(see [CONTRIBUTING.md](CONTRIBUTING.md)).

## Double-click again to return to the wall view

- Status: idea
- Problem: Double-clicking an agent window opens its full-screen focus view,
  but double-clicking again does not return to the wall view. Returning currently
  requires ⌘Enter, which interrupts a mouse-based workflow.
- Proposed change: Make double-clicking toggle between the wall and the focused
  agent view, so a second double-click returns to the previous wall view.
- Success criteria: After entering focus view with a double-click, another
  double-click returns to the wall view. Keep the existing ⌘Enter shortcut.
- Issue / implementation PR: Not yet opened.

## Keep the active column visible when starting a chat

- Status: idea
- Problem: In columns view, opening a new window and starting a chat causes
  Hesper to jump back to the first column. The newly opened, active column
  scrolls out of view.
- Proposed change: Preserve the active column and its visibility when a new
  window opens and a chat starts, rather than resetting the view to the first
  column.
- Success criteria: With enough columns to require horizontal scrolling,
  navigate away from the first column, open a new window, and start a chat.
  The newly active column remains visible throughout; the view does not jump
  back to the first column.
- Issue / implementation PR: Not yet opened.

## Template for new ideas

Copy this template below for each idea:

```markdown
## Short title

- Status: idea (idea / planned / in progress / delivered / declined)
- Problem: Who is affected, and what is difficult today?
- Proposed change: What should Hesper do differently?
- Success criteria: What would make this complete?
- Issue / implementation PR: Link when available.
```

Update statuses and implementation links as work progresses. Keep delivered or
declined ideas with a short outcome so decisions are not lost.

Everything in this repository is public. Leave out credentials, private
information, and real session data. Report security issues through
[SECURITY.md](SECURITY.md).
