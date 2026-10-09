---
name: hesper
description: Drive Hesper (hesperd + hesperctl), which runs the user's Claude Code, Codex and shell agents on all their Macs. Use when the user mentions Hesper, hesperctl or hesperd; asks what their agents or sessions are doing or which ones need them (approvals, questions, errors); wants work fanned out to parallel agents or worktrees, delegated to another agent, or run on another Mac; wants to drive, answer, close or tidy agents; or wants to find, resume or hand over past Claude/Codex sessions (history).
---

# Hesper

Hesper runs the user's coding agents (Claude Code, Codex, plain shells) in
terminals owned by `hesperd`, a daemon on each of their Macs. Hesper.app shows
them as tiles on a wall; `hesperctl` does everything the app does, from a
shell. You use `hesperctl` through Bash.

**Source of truth:** `hesperctl reference` prints the whole CLI as Markdown
(concepts, every command, flags, JSON shapes, exit codes). Read it once
before anything non-trivial; `hesperctl help COMMAND` shows one command.
Do not guess flags.

## Concepts

- **Agent id** `machine/local`, e.g. `mini/a7f3k2`. Commands also take the
  local id (`a7f3k2`) or a unique name.
- **States:** `starting`, `working`, `approval` (waits for a permission
  decision), `question` (asks something), `done` (turn finished; `summary`
  has its last line), `idle` (waits for a prompt), `error`, `exited`.
- **Attention** (`attention` in approval/question/error): `{kind, title,
  detail, options}`. `hesperctl show ID --json` adds numbered `choices`.
- **Projects:** agents work in a project folder, optionally a git worktree
  (`worktree`, `branch`). **Machines:** the user's Macs (`hesperctl status`).
- **Parent/child:** `hesperctl new` run inside an agent makes a child of it
  (`parent`, `depth`). `hesperctl ls --tree`, `ls --children`.
- **Kinds:** `claude`, `codex`, `shell`. A shell agent runs TASK as a
  command once its prompt is ready. It is always `idle` unless started with
  `--track` (then `working` while a command runs, `idle` at its prompt):
  run commands in shells with `--track`; see
  [references/recipes.md](references/recipes.md#shell-agents).
- **Settled:** done, idle, exited, approval, question or error: the agent
  no longer works on its own. `wait --until settled` and `new --wait` stop
  there.

## Rules for every call

- Always pass `--json` and parse stdout (e.g. with `jq` or `python3 -c`).
  Errors go to stderr as `{"error":{"code","message"}}`.
- Exit codes: 0 ok, 1 error, 2 usage/ambiguous name, 3 not found,
  4 hesperd or machine unavailable, 5 forbidden, 6 timeout, 7 exists/live.
- Exit 4: hesperd is not running (or the Mac is offline). Tell the user;
  do not try to start or restart it yourself.
- Always give `wait` and `new --wait` a `--timeout`.

## Inside or outside Hesper

`hesperctl self --json` succeeds (exit 0) when you run inside a Hesper agent
(it prints your own agent); exit 3 means you run elsewhere, as the person.

Inside an agent hesperd enforces a policy (refusals exit 5, `forbidden`):
- you may steer (send, stop, close, kill, rename, move, background) only your
  **descendants**: agents you (or your children) started; never yourself,
  your parent or the user's other agents;
- you may answer approvals/questions only of your **own children** started
  with `--let-parent-answer`;
- at most 3 levels deep and 8 live children per agent;
- reading (ls, show, screen, result, events, wait, history) is always allowed;
- `history resume|fork|continue-as` started by you make your children too
  (same limits); `attach-file` only to your descendants;
- closing one of your children gives its children to you.

On exit 5, do not look for a way around it: report it to the user.

## Safety

- Never approve, `answer`, `choose` or `send` into an agent waiting for
  permission on the user's behalf unless the user asked you to (or the
  child is yours, started with `--let-parent-answer` for exactly that).
- Do not kill, stop, close or `tidy` agents you did not start unless the
  user asked. Prefer `close` (conversation kept in History) over `kill`.
- `history delete` deletes transcripts: only on explicit request.
- `review accept` commits and `review reject` reverts files in the user's
  repository: only on explicit request.
- Read an agent's screen before typing into it (it may be mid-question).

## Recipes

Details, variants and pitfalls: [references/recipes.md](references/recipes.md).

**What is happening / what needs me**
```sh
hesperctl ls --json | jq -r '.[] | "\(.id) \(.state) \(.name) \(.attention.title // .summary // "")"'
hesperctl ls --json | jq -r '.[] | select(.state=="approval" or .state=="question" or .state=="error") | .id'
hesperctl show ID --json            # attention + numbered choices
hesperctl screen ID --json | jq -r .text   # the terminal as plain text
hesperctl result ID --json          # last final message (message null: none yet)
```

**Fan out N subtasks in worktrees, wait, collect, close**
```sh
a=$(hesperctl new --json --project ~/src/app --worktree --name fix-a "Fix A; commit on the branch" | jq -r .id)
b=$(hesperctl new --json --project ~/src/app --worktree --name fix-b "Fix B; commit on the branch" | jq -r .id)
hesperctl wait "$a" "$b" --all --until settled --timeout 30m --json
hesperctl result "$a" --json; hesperctl result "$b" --json   # review, then:
hesperctl close "$a" "$b" --json      # returns once they are gone
```
Use `--until settled`, not `finished`: `finished` never returns while one
agent sits in `approval` or `error`.

**Delegate and wait in one step**
```sh
hesperctl new --json --let-parent-answer --wait --timeout 20m "Run the tests and fix failures"
```
`--project` defaults to the current directory (relative paths work).
Prints `{agent, result}`; exits 1 if the child ends in `error`, 6 on timeout.
If `result.state` is `approval`/`question`, it needs an answer (see below).

**Drive an agent**
```sh
hesperctl screen ID --json | jq -r .text            # look first
hesperctl send ID "now run the linter" && hesperctl wait ID --next --until settled --timeout 15m
hesperctl send ID --key esc                         # keys: esc, enter, up, down, tab, ctrl-c, …
hesperctl show ID --json | jq .choices; hesperctl choose ID 2
hesperctl answer ID deny --message "use a branch, not main"   # only when allowed (see Safety)
```

**Review finished work** (accept and reject change the user's repository:
only when asked)
```sh
hesperctl review ls --json      # settled agents with changes: risk, evidence (fresh/stale/missing)
hesperctl review show ID        # files in reading order, risk notes, commands it ran
hesperctl review diff ID --json # hunks with ids ("<file>:<hunk>") and the folder's tree
hesperctl review send-back ID --note "src/x.go:42 handle the error" --message "Then run the tests."
hesperctl review accept ID --hunk 0:0 --tree TREE -m "message"   # without --hunk: everything
```

**Find and resume past sessions**
```sh
hesperctl history search --json "login redirect" | jq -r '.items[] | "\(.id) \(.title)"'
hesperctl history show SESSION --json        # prompts, answer, todos, changed files
hesperctl history resume SESSION --json      # new agent; exit 7 if it runs already
hesperctl history continue-as SESSION --kind codex --json   # hand a Claude session to Codex
hesperctl history brief SESSION              # a brief to paste into another agent
```

**App layout:** if available (`hesperctl help open`), `open`, `wall` and
`desk` show agents and arrange the app's windows.
