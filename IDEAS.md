# Bug reports and feature ideas

An editable inbox for reported bugs and proposed features in Hesper. Bugs record
unexpected behavior; feature ideas request new capabilities or interactions.
Reported bugs below have not yet been independently reproduced. Status tracks
progress separately from the type of request. Discuss larger changes in a
GitHub issue before implementation (see [CONTRIBUTING.md](CONTRIBUTING.md)).

## Bugs

### Keep the active column visible when starting a chat

- Type: bug
- Status: reported
- Problem: In columns view, opening a new window and starting a chat causes
  Hesper to jump back to the first column. The newly opened, active column
  scrolls out of view.
- Expected behavior / requested fix: Preserve the active column and its visibility when a new
  window opens and a chat starts, rather than resetting the view to the first
  column.
- Success criteria: With enough columns to require horizontal scrolling,
  navigate away from the first column, open a new window, and start a chat.
  The newly active column remains visible throughout; the view does not jump
  back to the first column.
- Issue / implementation PR: Not yet opened.

### Keep menu navigation expanded and fully visible

- Type: bug
- Status: reported
- Problem: The menu window sometimes becomes too wide, and its first option,
  "General", appears slid inward or partially hidden.
- Expected behavior / requested fix: Keep the menu navigation permanently expanded, with every
  option fully visible, instead of sliding or collapsing it. Keep the window
  width appropriate for the available screen space.
- Success criteria: Opening, resizing, and reopening the menu keeps "General"
  and all other navigation options fully visible and expanded. The window does
  not unexpectedly become too wide or clip navigation on smaller screens.
- Issue / implementation PR: Not yet opened.

### Show the full text of suggested chat answers

- Type: bug
- Status: reported
- Problem: When the chat suggests an answer, the first letter of the suggestion
  is cut off.
- Expected behavior / requested fix: Adjust the suggested-answer layout so the entire text is
  visible, including its first character.
- Success criteria: Display a suggested answer in chat and verify that its
  first letter is fully visible, with no clipping at the left edge.
- Issue / implementation PR: Not yet opened.

### Activate windows consistently with one click

- Type: bug
- Status: reported
- Actual behavior: Sometimes a window requires a double-click before it becomes
  active, instead of activating on the first click.
- Expected behavior: A single click consistently activates the intended window
  and gives it input focus.
- Verification: Switch between windows with one click. The clicked window
  should become active and ready for input without requiring a second click.
  The intermittent trigger has not yet been isolated.
- Issue / implementation PR: Not yet opened.

## Feature ideas

### Double-click again to return to the wall view

- Type: feature
- Status: idea
- Problem: Double-clicking an agent window opens its full-screen focus view,
  but double-clicking again does not return to the wall view. Returning currently
  requires ⌘Enter, which interrupts a mouse-based workflow.
- Proposed change: Make double-clicking toggle between the wall and the focused
  agent view, so a second double-click returns to the previous wall view.
- Success criteria: After entering focus view with a double-click, another
  double-click returns to the wall view. Keep the existing ⌘Enter shortcut.
- Issue / implementation PR: Not yet opened.

### Custom display names for connected machines

- Type: feature
- Status: idea
- Problem: Connected machines need recognizable, user-chosen names so it is
  easier to identify where agents are running.
- Proposed change: Allow users to assign and edit a custom display name for
  each connected machine.
- Success criteria: A connected machine can be renamed in Hesper, the custom
  name persists across reconnects and app restarts, and machine selectors and
  labels use it consistently. Renaming does not break the machine connection
  or change its underlying identity.
- Issue / implementation PR: Not yet opened.

### Keep group headers visible during horizontal scrolling

- Type: feature
- Status: idea
- Problem: In large groups or folders such as Scratch, horizontal scrolling can
  move the header out of view, making it difficult to tell which group is shown.
- Proposed change: Keep the group header anchored in place while its contents
  scroll horizontally, so the group name stays visible.
- Success criteria: In a group wider than the viewport, scroll horizontally
  through its contents. The group header remains visible and identifies the
  current group throughout.
- Issue / implementation PR: Not yet opened.

## Templates

### Bug report

```markdown
### Short title

- Type: bug
- Status: reported (reported / confirmed / in progress / fixed / closed)
- Actual behavior: What happens, and how often?
- Expected behavior: What should happen instead?
- Reproduction steps: List known steps; note when the trigger is unknown.
- Verification: How will we know the bug is fixed?
- Issue / implementation PR: Link when available.
```

### Feature idea

```markdown
### Short title

- Type: feature
- Status: idea (idea / planned / in progress / delivered / declined)
- Problem: Who is affected, and what is difficult today?
- Proposed change: What should Hesper do differently?
- Success criteria: What would make this complete?
- Issue / implementation PR: Link when available.
```

Update statuses and implementation links as work progresses. Keep closed bugs
and delivered or declined ideas with a short outcome so decisions are not lost.

Everything in this repository is public. Leave out credentials, private
information, and real session data. Report security issues through
[SECURITY.md](SECURITY.md).
