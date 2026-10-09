# Review: concept and phases 1–2

Approved by the user (all recommended decisions). Background research with sources: [research.md](research.md).

## Principle
Review is trust calibration, not reading diffs. Hesper's review shows where to look, what the agent claimed, what was proven, and what the reviewer actually saw — for every agent on every Mac. Research: file order changes detection (−64% odds for a defect type in the last file), complexity raises acceptance of incorrect (esp. AI-labelled) code, AI flags narrow reviewer attention, verification is the hidden cost. Market gaps: no cross-agent review inbox, no plan/claim → hunk linking.

## Decisions
1. A separate **⌘R review inbox** next to ⌘J (⌘J stays for approvals/questions; ⌘R for finished work).
2. Claims come from the agent itself, asked once at the end (phase 3, not now).
3. Bulk accept only for low-risk changes with fresh evidence and no unclaimed hunks, never schema/auth/migrations.
4. Build phases 1 and 2 first (no AI judgement needed).

## Phase 1 — native diff and inbox (app + daemon diff API)
- **hesperd `review.*` API** (relay/internal, wire types, hesperctl `review ls|show|accept|send-back`):
  - `review.list` → finished agents ready for review (state done/idle with changes in their worktree/cwd vs. their base: branch base or the checkpoint before the task), per Mac, merged across Macs like agents.list.
  - `review.diff {id, context?}` → files with hunks (unified data model: file path, status A/M/D/R, old/new line numbers, lines with kind, intra-line word ranges), computed on the agent's Mac with `git diff` (histogram, `--find-renames`, `-M`), word ranges refined only inside changed hunks with time/size limits; formatting-only hunks flagged; moved blocks flagged (`--color-moved` logic or own detection).
  - `review.accept {id, hunks?: [ids]|all, commit?: {message}}` → stage/commit accepted hunks in the agent's worktree (never touch the user's main checkout unless the agent ran there); `review.reject {id, hunks}` → revert those hunks in the worktree.
  - `review.sendBack {id, notes:[{file, line, side, text}]}` → one combined instruction typed into the agent (like agents.input), agent continues; remember the reviewed checkpoint so the next review can show only the delta (interdiff, phase 4 — store the marker now).
- **App review sheet** (⌘R, and "Ready to review · N files" line on finished tiles; ⏎ opens):
  - Left: inbox across Macs, ranked (risk placeholder = size + simple rules in phase 1; evidence freshness in phase 2), bulk "accept low-risk with fresh evidence" (⇧A) obeying decision 3.
  - Middle: one virtualized stream of all files (multibuffer), reading order by simple rules: risky/depended-on first, tests next to their code, formatting-only and generated files last and folded; never alphabetical.
  - Rendering: view-based NSTableView rows drawn with CoreText (not TextKit 2, not SwiftUI List for the body), code row heights separate from comment heights, comments anchored to file+line+side, diff computed off the main thread, plain text first then syntax colors. 120 fps target.
  - Keys: J/K hunk (auto-advance after a decision), N next file/concern, V mark seen, X reject hunk, C note, ⇧↩ send back notes, ⌘↩ accept & commit, ⌥↩ open conversation at that point (phase 2), Esc close.
  - Right panel: evidence (phase 2), risk notes, attention strip (seen/unseen hunks).
  - Toolbar pill "Review · n" (not Signal red — red stays for needs-you). Menu bar section and notification "X on mini is ready to review".
- Design system: docs/design-system.md (DS tokens, StateMark, Pill, Kbd, Row, Panel); Dusk + Daylight; other windows read shared state via `lists`; per-Mac capability flags with fallback on "Unknown method".

## Phase 2 — evidence and provenance
- **hesperd records per agent**: commands run in its terminal and by its tools (from hooks: PreToolUse/PostToolUse Bash commands, exit codes, timestamps) and edit times (PostToolUse Edit/Write, file paths). Expose `review.evidence {id}` → commands with exit code and time, classified (test / build / lint / run / other by command patterns), and freshness: **fresh** (ran after the last edit), **stale** (edits after it), **missing** (no test/build ran). Screenshots/images the agent produced in its folder during the task listed as attachments.
- **Provenance**: each hunk links to the turn and tool call that last wrote it (match edit events to file+line ranges); `review.provenance {id, file, line}` → session id + turn index + prompt excerpt; the app's ⌥↩ opens History at that point.
- App: evidence box (fresh ✓ / stale ! / missing ?), provenance line under hunks ("Why here? turn 2 · …"), inbox ranks by evidence freshness.

## Rules
- Work on a branch from `feat/review`; signed commits; merged into `main` by squash PR.
- Never touch the live install, LaunchAgents, the running hesperd or real agents' folders; tests use temp HOME/state and the fake daemon/agents.
- No UI test suites or app launches that open windows; use headless checks and offscreen renders (see app/Sources/Hesper/Perf/*Render.swift, `Hesper --render-…`) and look at the PNGs.
- Checks: `cd relay && go build ./... && go vet ./... && go test ./...`; `cd app && swift build --product Hesper && swift test && make build`.
