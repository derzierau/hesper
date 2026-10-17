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
