# Hesper recipes (details)

Everything here is `hesperctl … --json`; `hesperctl reference` has the full
flag list and JSON shapes.

## Overview: what needs the user

```sh
hesperctl status --json                     # hesperd up? which Macs are online
hesperctl ls --json                         # every agent of every Mac
hesperctl ls --tree                         # children under their parents (table)
```

Group by state when reporting: first `approval` / `question` / `error`
(with `attention.title` and `attention.detail`), then `working`, then
`done` (with `summary`), then `idle` / `exited`. For one agent:

```sh
hesperctl show ID --json      # every field; choices:[{n, title, decision?, keys?}] when it waits
hesperctl screen ID --json    # {text, rows, cols, cursor}; --scrollback 200 for history above
hesperctl result ID --json    # {id, state, message, summary, at}; exit 1 when nothing yet
```

`screen --rows N` gives the screen's *last* N rows: on a mostly empty screen
those are blank. Prefer the whole screen and strip blank lines.

To follow changes live: `hesperctl events --kinds agents` (JSON lines; runs
until interrupted, so only with a time limit, e.g. `timeout 60 hesperctl events …`).

## Fan out work

1. Pick the project folder (`hesperctl projects recent --json` lists folders
   agents used; `--project` defaults to the current directory and must be
   absolute when given).
2. Start one agent per subtask, each in its own worktree so they do not
   collide:
   ```sh
   hesperctl new --json --project ~/src/app --worktree --name api-tests "…task…"
   hesperctl new --json --project ~/src/app --branch fix-login "…task…"   # --branch implies --worktree
   hesperctl new --json --kind codex --machine mini --project ~/src/app --worktree "…"
   ```
   Write self-contained tasks: the agent sees only its prompt. Say what to
   deliver ("commit on the branch", "end with a summary of what changed").
   Inside an agent: at most 8 live children; add `--let-parent-answer` only
   if you are expected to handle the child's permission prompts.
3. Wait for all of them to settle (finished or needing someone):
   ```sh
   hesperctl wait ID1 ID2 ID3 --all --until state=done,idle,exited,approval,question,error --timeout 45m --json
   ```
   `--any` returns as soon as one settles (then handle it and wait on the
   rest). Exit 6 = timeout (the message says who is still in which state);
   exit 3 = an agent went away (closed by someone).
4. Collect: `hesperctl result ID --json` per agent, plus the `worktree` and
   `branch` from `hesperctl show ID --json` to review the diff
   (`git -C WORKTREE diff main...`).
5. Close the ones you started and are done with: `hesperctl close ID… --json`
   (prints the session to resume each with). `close` answers before the
   agent is fully gone; it disappears from `ls` a moment later.
   `hesperctl tidy --dry-run --json` shows what `tidy` would close (every
   finished agent, also the user's own: only run it when asked).
   Alternative: `hesperctl background ID` hides an agent and closes it
   when it finishes.

## Delegate and wait (one child)

```sh
out=$(hesperctl new --json --project "$PWD" --let-parent-answer --wait --timeout 20m "…task…")
echo "$out" | jq -r .result.message
```

`--wait` returns when the child is done, idle, exited or needs someone
(approval, question, error). Without `--json` it prints only the result text,
not the id: use `--json` to keep the id (`.agent.id`). Then:

- `result.state == "approval"`: with `--let-parent-answer` (and the user's
  consent for this kind of action) `hesperctl answer ID allow|deny`, else tell
  the user; then wait again with `hesperctl wait ID --next --until state=…`.
- `result.state == "question"`: `hesperctl show ID --json` for the choices,
  `hesperctl choose ID N`.

## Drive an agent

Before typing, read the screen and the state: typing into an agent that
shows a question or a menu answers it.

```sh
hesperctl send ID "text"              # pasted, then Enter
hesperctl send ID --no-submit "text"  # pasted, no Enter
git diff | hesperctl send ID -        # TEXT from stdin
hesperctl send ID --key esc           # interrupt Claude/Codex
hesperctl send ID --key down --key enter
hesperctl send ID --raw y             # typed as is (no paste, no Enter)
hesperctl attach-file ID shot.png     # like dropping a file on it (pasted, no Enter)
```

After `send`, `wait ID --next --until …` waits for the turn the send starts
(`--next` ignores the state the agent is in when wait starts).

Decisions: `approve ID [--always]`, `deny ID [--message M]`,
`answer ID DECISION [--message M]` (DECISION from `attention.options`),
`choose ID N` (N from `show`'s `choices`). Others: `stop`, `resume`,
`rename`, `mv ID MACHINE` (move to another Mac, conversation included),
`kill` (last resort; stays listed as exited/killed).

## Shell agents

`--kind shell` starts a login shell. It does **not** run TASK (TASK only
names it), and its state stays `idle` while commands run, so `wait` cannot
tell when a command finished. Two patterns:

```sh
id=$(hesperctl new --json --kind shell --project "$PWD" --name build "build" | jq -r .id)
# a) one-shot: end the shell with the command, then wait for exited
hesperctl send "$id" 'make test; exit'
hesperctl wait "$id" --until exited --timeout 10m --json
hesperctl screen "$id" --json | jq -r .text    # still readable after exit
# b) long-lived: print a marker and poll the screen for it
hesperctl send "$id" 'make test; echo __DONE__$?'
```

A shell agent the user started (no parent) acts as the user: hesperctl run
inside it is not restricted by the agent policy.

## History (every Claude and Codex session of every Mac)

```sh
hesperctl history search --json "flaky test"          # words match prompts, answers, titles
hesperctl history search --json --project . --since 7d
hesperctl history search --json --live                # running now (live.agentId when in Hesper)
hesperctl history search --json --kind codex --machine mini --external
hesperctl history show SESSION --json                 # + changes: files, uncommitted, ahead/behind
hesperctl history brief SESSION                       # plain-text handover
hesperctl history resume SESSION [--machine M] --json # exit 7 (live) if it runs: use .agentId
hesperctl history fork SESSION --json                 # a copy; the original stays
hesperctl history continue-as SESSION --kind claude|codex --json
```

SESSION ids look like `mini:claude:5b0c1d2e-…`. Pages hold at most 50;
`--json` gives `cursor` for the next (`--cursor`). Archive with
`history archive`; `history delete` only on explicit request (30 s undo with
`history undelete`).

## Projects, groups, drafts

`projects ls|recent|update|rm|promote|clone`, `groups ls|save|rm`,
`drafts ls|save|rm` (a draft is a prepared agent the user starts from the
app: a good way to *suggest* work without starting it), `profiles` (how
agents start; `--profile` on `new`).
