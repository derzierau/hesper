# Hesper rebuild: contract

The rebuild replaces tmux, the Python cockpit and the `bin/` scripts with two
programs. This file is the contract between them; each part's agents build
against it and update the "as built" notes at the end.

No backward compatibility with the tmux setup is required.

## Parts

| Part | What | Where |
|---|---|---|
| D | `hesperd`: per-Mac daemon. Owns agents in PTYs, their state, hooks, approvals, worktrees, persistence; the local socket; the `hesperctl` CLI | `relay/cmd/hesperd`, `relay/internal/agents`, `relay/internal/ptyhost`, `relay/pkg/wire`, `relay/cmd/hesperctl` |
| A | `Hesper.app`: native macOS app (Swift, AppKit + SwiftUI, libghostty). Only a view of the daemon | `app/` |
| R | Remote: local `hesperd` reaches other Macs' `hesperd` through the relay (parts K, N, D of `docs/remote-shell-contract.md`) | `relay/internal/gateway`, `relay/internal/host`, `relay/internal/remote`, `relay/internal/handoff`, `relay/pkg/agentlink`, `relay/internal/controlsock` |
| C | Cutover: install, hooks, LaunchAgent, signing, delete the tmux setup | `install.sh`, `scripts/`, removals (wave 2) |

Kept as is: the relay server, sign-in and renewal, device keys (K), the Noise
channel (N), the direct path (D), transfer/handoff crypto, `internal/vt`,
`internal/perf`, the latency harness.

## The one rule

The app never talks to the network and never decides an agent's state. It
talks only to the **local** `hesperd`. The local daemon is the gateway to
other machines (part R): a remote agent looks exactly like a local one, its id
just carries another machine.

## Agent model

```json
{
  "id": "L/a7f3k2",            // "<machine short>/<local id>"; local id: 6 chars [a-z0-9]
  "machine": "L",              // short name from machines config; "L" = this Mac in the examples
  "kind": "claude",            // claude | codex | shell
  "profile": "claude-auto-rc", // launch profile (below)
  "name": "push-provider-fcm", // short display name; from the task's first line unless given
  "task": "…full task text…",
  "project": "/Users/…/acme-apps",
  "worktree": "/Users/…/worktrees/…",   // optional; agent cwd = worktree or project
  "branch": "feature/push-provider-fcm", // optional
  "state": "working",          // see states
  "stateSince": "2026-10-06T19:02:11Z",
  "attention": {               // present only for approval | question | error
    "kind": "approval",
    "title": "Bash",
    "detail": "git push origin feature/push-provider-fcm",
    "options": ["allow", "always", "deny"]
  },
  "summary": "Opened PR #482 (draft)",   // last useful line for done/idle tiles; optional
  "activity": "Bash: git push origin …", // what it does right now (the running tool); optional
  "sessionId": "…",            // claude/codex session id once known (for resume)
  "size": {"cols": 120, "rows": 40},     // the PTY's one size
  "created": "…", "pid": 12345, "exit": null
}
```

States: `starting`, `working`, `approval`, `question`, `done`, `idle`,
`error`, `exited`. The daemon derives them from hooks and process events only
(never from screen scraping, except as a documented fallback for Codex
approvals if no hook exists).

## Launch profiles

`~/.config/hesper/profiles.json` (defaults built in, user file overrides):

```json
{
  "claude":            {"kind": "claude", "argv": ["claude"]},
  "claude-auto-rc":    {"kind": "claude", "argv": ["claude", "--permission-mode", "auto", "--remote-control", "{name}"]},
  "claude-unattended": {"kind": "claude", "argv": ["claude", "--dangerously-skip-permissions", "--remote-control", "{name}"]},
  "codex":             {"kind": "codex",  "argv": ["codex"]},
  "codex-unattended":  {"kind": "codex",  "argv": ["codex", "--dangerously-bypass-approvals-and-sandbox"]},
  "shell":             {"kind": "shell",  "argv": ["{loginShell}", "-l"]}
}
```

- The default per kind (`settings.json` `defaults.kinds`, built in:
  `claude` → `claude`, `codex` → `codex`, `shell` → `shell`) keeps the
  tool's permission prompts on. `claude-auto-rc` and the `-unattended`
  profiles are opt-in: named on a spawn, or made a default in
  `settings.json`. A kind default that names no profile falls back to the
  built-in named after the kind, then to the kind's first profile by name.
- Legacy names: `claude-bypass` resolves to `claude-unattended` and
  `codex-full` to `codex-unattended` (same command lines; built-ins before
  the public release), wherever a name is read (spawn, resume, move,
  project and kind defaults), unless `profiles.json` defines the old name.
  `profiles.list` reports defaults under the name they resolve to.

- The task is delivered as the agent's first prompt (claude: positional prompt
  argument; codex: positional prompt), exactly like `bin/ghosty-pane` does today
  (read it; the `--remote-control` name must always be passed so it never
  swallows the prompt).
- Resume: claude `--resume <sessionId>`, codex `resume <sessionId>` (fork:
  `codex fork`), as in `bin/ghosty-pane`.
- Environment: the daemon sets `HESPER_AGENT_ID`, `HESPER_SOCKET` for hooks,
  plus the user's login environment (PATH etc., resolved once from a login
  shell).
- Default profile per kind and per project: `~/.config/hesper/settings.json`.

## Local socket and wire format

`$HESPER_STATE_DIR/hesperd.sock` (default `~/.local/state/hesper/`), mode 0600,
directory 0700. Peer credentials checked: same uid only.

**Control connections** speak newline-delimited JSON-RPC 2.0. Methods:

| Method | Params | Result |
|---|---|---|
| `hello` | `{client, version}` | `{daemon, version, machine, machines:[{short,name,online,rttMs,route}]}` |
| `agents.list` | – | `[Agent]` |
| `agents.subscribe` | – | result `{}` then notifications `agents.changed {agent}` / `agents.removed {id}` (first an `agents.changed` for every agent) |
| `agents.spawn` | `{machine?, profile?, kind?, project, task, name?, worktree?: true\|path, branch?, letParentAnswer?}` | `Agent` (inside an agent: its child, see "As built — agent tree") |
| `agents.result` | `{id}` | `{id, state, message?, summary?, at?}` (added, see "As built — agent tree") |
| `agents.input` | `{id, text}` (bytes as UTF-8; `\r` submits) | `{}` |
| `agents.answer` | `{id, decision: allow\|always\|deny, message?}` | `{}`; maps to the agent kind's keys (port `APPROVAL_KEYS` from `bin/ghosty-cockpit`) |
| `agents.stop` | `{id}` | `{}` (SIGHUP, then SIGKILL after 5 s; registry keeps it as `exited`) |
| `agents.resume` | `{id}` | `Agent` (respawn with its session id) |
| `agents.remove` | `{id}` | `{}` (forget an exited agent) |
| `agents.rename` | `{id, name}` | `Agent` |
| `agents.move` | `{id, to}` | `Agent` (handoff with conversation; part R) |
| `agents.screen` | `{id, rows?, scrollback?}` | `{text, rows, cols, cursor?:{col,row}, alt?}`: the terminal as plain text (added, see "As built — agent lifecycle CLI") |
| `projects.recent` | – | `[{path, name, lastUsed}]` |
| `projects.clone` | `{url, machine?}` | `{path}` (git clone into the projects root `<repo>`; on `machine` through the gateway) |
| `profiles.list` | – | `{profiles, defaults}` |
| `fs.stat` | `{machine?, path}` | `{exists, isDir}` (added, see "Machine and folder agree") |
| `hook` | `{agent, source: claude\|codex, event, payload}` | `{}` (what hook processes send) |
| `drafts.list` / `drafts.save` / `drafts.remove` | – / `{draft}` / `{id}` | `[Draft]` / `Draft` / `{}` (added, see "As built — drafts, overlays, …") |
| `files.put` / `files.chunk` | `{agent \| machine?+draft, name, size, sha256}` / `{upload, offset, data, last}` | `{upload, chunk}` / `{received}`, last `{path, size}` (added, see "As built — drop to attach") |
| `sessions.search` / `show` / `resume` / `fork` / `brief` / `continueAs` / `archive` / `delete` / `stats` | see "As built — shared history (data)" | the shared history of every Mac's Claude and Codex sessions (added) |
| `app.register` / `app.state` / `app.open` / `app.wall.set` / `app.desk` | see "As built — app control" | Hesper.app's windows, walls and desks, forwarded to the app (added; local only) |
| `review.list` / `review.diff` | see "As built — review (daemon)" | agents ready for review on every Mac, and one agent's changes (added) |

Errors: JSON-RPC errors with `data.code` in `not_found`, `invalid`,
`exists`, `unavailable`, `forbidden`, `remote` and a human message.

**Attach connections** carry one agent's terminal. The client sends one JSON
line `{"attach":"L/a7f3k2","mode":"rw"|"ro","cols":N,"rows":M,"owner":bool}`,
the daemon answers one JSON line `{"ok":true,"cols":C,"rows":R}` (the PTY's
size) or an error, then both sides switch to binary frames:
`1 byte type | 4 byte big-endian length | payload`.

| Type | Direction | Payload |
|---|---|---|
| 0 DATA | both | raw terminal bytes (to the client: PTY output; to the daemon: input, refused when `ro`) |
| 1 RESIZE | client→daemon | `cols u16, rows u16`; applies only if this attach is the size owner |
| 2 SIZE | daemon→client | `cols u16, rows u16`: the PTY changed size |
| 3 EXIT | daemon→client | JSON `{code, signal}` |
| 4 SCROLL | both (view attaches) | client: JSON `{offset}`/`{delta}`; daemon: JSON `{offset, max, new}` (added, see "As built — drafts, overlays, …") |

On attach the daemon first sends a DATA frame that redraws the current screen
exactly (from its `internal/vt` screen copy: clear, every row with attributes,
cursor position and modes, alternate screen), then live output.

**Size rule.** An agent's PTY has exactly one size. The size owner is the most
recent `owner:true` rw attachment (the app's focused view); when it detaches,
the size stays (unless view attaches ask for a fit, below). Every other
viewer receives bytes for that grid: the app renders tiles by choosing the
font size that fits `cols × rows` into the tile (scaled live tiles), never by
resizing the PTY.

**View attaches** (added with the layout rework, see "As built — layout"): an
`ro` attach may ask for a window onto the screen instead of the screen:
`"view":{"rows":R,"cols":C,"anchor":"bottom"}` (`rows`/`cols` 0 or missing:
the request's `cols`/`rows`; `anchor` only `bottom`). The daemon renders,
from its screen copy, the R rows that end at the lower of the last non-blank
row and the cursor's row, each clipped to C columns, and afterwards only the
rows that changed (each render one `?2026` synchronized update, at most one
per 8 ms; the first after a quiet period at once). The viewer gets no SIZE
frames; its RESIZE sets the window (`cols, rows`), never the PTY. The reply
echoes `"view":{rows, cols, anchor}`. A `rw` view is refused (`invalid`).

**As built — fit** (additive): a view attach may add `"fit":{"cols":C,"rows":R}`,
the grid its tile would like the PTY to have (the app sends its tile's
cols × rows). While no `rw` owner attach exists, the PTY follows the largest
fit of all view attaches that declare one (the most cols and the most rows
among them, each side ≥ 80 × 24, ≤ 1000), debounced: the fits must settle for
300 ms, and the PTY is resized only when a side changes by ≥ 2. A RESIZE from
a view with a fit updates both its window and its fit. An owner attach wins
as before (fits change nothing while it is attached); when the owner
detaches, the PTY goes to the fit size (if any view has one). Views without
`fit` never size the PTY. A tall tile (a 40-row agent in a full-height
column) thus gets a tall screen instead of empty rows above the agent's
last rows. `hesperd attach <id> --fit` (implies `--view`) sends its
terminal's size as the fit; relayed remote attaches pass `fit` through
unchanged.

## `hesperd` subcommands (one binary)

- `hesperd serve`: the daemon (LaunchAgent).
- `hesperd attach <id> [--ro] [--owner] [--view] [--view-rows R] [--fit]`: bridges
  its own stdin/stdout tty to an attach connection, forwards SIGWINCH as
  RESIZE (only with `--owner` or `--view`), and follows SIZE (not with
  `--view`: a view keeps its terminal's size and shows the agent's last rows
  that fit it). **The app runs this as each libghostty surface's command**, so
  libghostty owns a normal PTY and never needs custom IO.
- `hesperd hook <claude|codex> <event>`: reads the hook JSON on stdin, sends
  `hook` to the daemon, exits 0 fast (≤ 50 ms) even if the daemon is down.

`hesperctl` (CLI for humans and scripts) is rebuilt on the same socket:
`hesperctl ls | new | send | approve | deny | stop | resume | attach | mv`,
plus the existing relay commands (login, pair-host, approve-device, devices,
trust).

**As built — CLI foundation**: commands live in a registry
(`relay/cmd/hesperctl/commands.go`; each file registers its own in
`init`). `hesperctl help [CMD]`, `CMD --help` and `hesperctl reference`
(Markdown for LLM agents, `--json` too) are generated from it. Every
command takes `--json`; errors then go to stderr as
`{"error":{"code","message"}}`. Exit codes: 0 ok, 1 error, 2 usage,
3 not_found, 4 unavailable (no hesperd; codes unavailable, offline,
remote), 5 forbidden, 6 timeout, 7 exists (exists, live). `hesperctl
self` is the agent it runs in (`HESPER_AGENT_ID`).

**As built — CLI: history, projects, groups, drafts, profiles, status**
(`history.go`, `projects.go`, `drafts.go`; CLI only, the daemon methods
as specified). The registry gained subcommands: a command named
`"history search"` runs as `hesperctl history search …`; `hesperctl
history`, `history --help` and `help history` list the parent's
subcommands (`--json`: their descriptions); help and reference show each
one like any command.

| Command | Method |
|---|---|
| `history search [QUERY…] [--project P] [--kind K] [--machine M] [--since D] [--live] [--external] [--archived] [--moved] [--limit N] [--cursor C]` | `sessions.search` (`--project`: id, unique name, or a folder: the deepest project containing it, else `scratch:<folder>`; `--since`: 90m, 24h, 7d, 2w, a date or RFC 3339; the next page's cursor on stderr, or in the JSON) |
| `history show ID` / `brief ID` / `stats` | `sessions.show` / `brief` / `stats` |
| `history archive ID [--undo]` | `sessions.archive {archived: !undo}` |
| `history delete ID` / `undelete ID` | `sessions.delete {undo?}` |
| `history resume ID [--machine M]` / `fork ID [--machine M]` | `sessions.resume` / `fork`: prints the agent id, the note on stderr; a running session exits 7 (code `live`, `agentId` in the JSON error) |
| `history continue-as ID --kind claude\|codex [--machine M]` | `sessions.continueAs` |
| `projects ls` / `recent` | `projects.list` / `projects.recent` |
| `projects update PROJECT [--name] [--color] [--kind] [--default-profile] [--default-machine]` | `projects.update`: only the flags given; the defaults merged with the current ones (the app's way: `defaults` is replaced whole) |
| `projects rm PROJECT` / `promote PATH [--machine] [--name] [--kind]` / `clone URL [--machine]` | `projects.remove` / `promote` / `clone` |
| `groups ls` / `rm GROUP` | `groups.list` / `groups.remove` |
| `groups save [GROUP] [--name] [--color] [--project P]… [--remove-project P]… [--order N]` | `groups.save`: without GROUP a new group after the others (order max + 1, as the app's New Group); with it the group changed |
| `drafts ls` / `rm ID` | `drafts.list` / `drafts.remove` |
| `drafts save [TASK…\|-] [--id D] [--project P] [--machine M] [--kind K] [--profile P] [--worktree] [--branch B]` | `drafts.save`: a project (id or name) sets `band` and the project's folder on the machine (its default machine, else the first with a folder) and its default profile, as the app's "New Agent in …"; a folder sets `project`; `--machine` sets `machineExplicit` (this Mac's name is stored as none); `--kind` picks the kind's default profile; `--id` changes only what is given |
| `profiles` | `profiles.list` |
| `status` | `hello`: daemon, version, machine, machines with online, RTT and route |

Projects are addressed by id, unique name (exact, else ignoring case; a
real project wins a tie with scratch folders) or folder; groups by id or
unique name; two of one name exit 2 (code `ambiguous`). Session ids are
the full `machine:kind:sessionId`. Tests (`history_test.go`,
`projects_test.go`, `drafts_test.go`) run hesperd as the gateway wires it
(registry, project store, shared history with internal/sessions'
synthetic transcripts; cat as claude and codex).

**As built — agent lifecycle CLI** (`relay/cmd/hesperctl/agents_more.go`):
`close`, `kill`, `background [--off]` take several ids (agents.close /
kill / background); `tidy` closes what ⇧⌘W closes (state done, idle or
exited; background agents close themselves and are left out; `--project`,
`--dry-run`). `answer ID DECISION [--message]` takes every agents.answer
decision (allow, always, deny, trust, exit, skip, update); `show ID` lists
an agent's numbered choices and `choose ID N` picks one as the app's ⌘J
inbox does: an approval's offered decisions, a question's decision
options (trust/exit, skip/update), else the numbered options in its
detail ("1. A 2. B", at most 9), whose number is typed (agents.input, no
paste, no Enter). `send` gains `--raw` (text unpasted, no Enter) and
repeatable `--key` (esc, enter, tab, shift-tab, arrows, home, end,
pageup, pagedown, backspace, delete, space, ctrl-A…Z, one character; one
agents.input each, 50 ms apart). `attach-file` does what a drop does:
this Mac's agents get the path, another machine's an upload (files.put /
files.chunk; `--upload` forces one), pasted the app's way (one paste per
PNG/JPEG, the other paths in one, backslash-escaped). `events` prints
agents.subscribe's notifications as `{method, params}` lines (`--agent`,
`--kinds`); `wait ID… --until settled|needs-you|done|idle|exited|working|finished|state=S`
follows them (`--any`/`--all`, `--next` ignores the state at start,
`--timeout` exits 6, an agent removed exits 3). `settled` is done, idle,
exited, approval, question or error (no longer working on its own),
what `new --wait` waits for. `close` and `tidy` return once the agents
are gone (agents.removed on a subscription opened first; at most 10 s,
then a note on stderr; `--no-wait` returns at once). `screen --rows N`
prints the last N rows with text: hesperctl asks for the whole screen
(and scrollback), drops the blank rows at its end, then keeps N (the
daemon's `rows` is the grid's last rows, blank below output at the top).
`new --project` (and `--worktree-path`) relative to the current directory
for this Mac (no `--machine`, or this Mac's), made absolute by hesperctl;
for another Mac sent as given. `new --wait` without `--json` prints the
id at once (first line), then the result. `result` with nothing yet
exits 0: no stdout (a note on stderr); `--json` `{id, state, message:
null, summary: null}` (both are always present, null when empty; also
in `new --wait --json`'s `result`).
**As built — shell agents** (`internal/agents/shell.go`): hesperd follows
a shell's terminal. Prompt ready: the shell wrote output and then nothing
for 400 ms while no job runs (the PTY's foreground process group,
TIOCGPGRP on the master, is the shell's), or 10 s passed. A TASK given to
`agents.spawn` for a shell (as the app's composer sends it) is typed
then (bracketed paste when the shell has it on) with Enter, for every
shell. Only the spawn types it:
a respawn or `agents.resume` starts the shell without it (not persisted;
a restart before the prompt was ready drops it). `agents.input` to a
shell waits (≤ 10 s) for its prompt, so text sent right after the start
comes after the prompt (every shell). **State, opt-in per spawn:**
`agents.spawn` `track` (bool; `Agent.track`, omitempty, persisted, kept
by resume, respawn and moves (handoff manifest `agent.track`), sent to
hosts like `letParentAnswer` (a host older than this rejects a spawn
that sets it); ignored for other kinds; hesperctl `new --kind shell
--track`; not in drafts or the app). Without `track` (the default) a
shell behaves as before: `idle` until it exits, no `activity`, a
background shell is not closed when a command ends; the watch stops
once the prompt is ready and the task typed. With `track`: `starting`
until the task is typed, then `working` while a job runs (foreground
group not the shell's; `activity` the job's command name, p_comm or
/proc/PID/comm), and from a command hesperd typed (the task, an
`agents.input` with `submit`) until the shell is back at its prompt with
no output for 400 ms (a builtin or a quick command counts as done then);
else `idle` (activity cleared). Polled every 150 ms. For a tracked
shell: a full-screen or long-running program (vim, a dev server) keeps
it `working` (the app's close confirmation for a running shell command
gets its activity); in the background it is closed when a command it ran
ends (working → idle). No exit status is known (no shell integration).
hesperctl `new --wait` waits for a tracked shell to settle and for an
untracked one to exit (or err): `TASK; exit`, or `--track`. `wait
--until settled` matches an untracked shell at once (always idle).
**agents.screen** `{id, rows?, scrollback?}` (rows: the screen's last
rows, 0 all; scrollback: that many scrollback lines first, ≤ 10000)
returns `{text, rows, cols, cursor?, alt?}` from the daemon's vt screen:
lines without escape codes or trailing blanks joined by `\n`; rows/cols
the screen's size; cursor (0-based, on the screen) when visible; alt
when the alternate screen is on (the scrollback is the main screen's).
Another machine's agent: forwarded like agents.input; the host method
needs the `observe` right and hides shells as agents.list does.

**As built — app control** (walls, desks, navigation from the CLI;
Settings stay app-only). Walls, desks, layouts and focus live only in
Hesper.app, so hesperd relays: the app's control connection calls
`app.register` (`{client}` → `{}`) after every connect; one app at a
time, the latest registration wins, it ends with its connection. Any
other local client's `app.state` / `app.open` / `app.wall.set` /
`app.desk` is sent to that app **as a JSON-RPC request on the app's
connection** (server → client; ids are strings `"app-N"`, so they never
collide with the app's numeric ids; the app answers with an ordinary
response line, which hesperd takes only from the connection it asked)
and the answer is relayed back unchanged (the app's `data.code`
included). No app registered → `unavailable`; the app's connection
closing mid-call → `unavailable`; no answer within 10 s → `data.code`
`timeout` (hesperctl exits 6). Local only: hesperd's host service has
no `app.*`. Any caller, agents included: the agent tree's policy covers
the methods that start or change agents, and `app.*` changes none (a
`caller` param is dropped before forwarding). Code: `relay/internal/agents/appbridge.go` (a two-line hook
in `server.go`'s read loop and dispatch); the app: `HesperCore/
AppControl.swift` (params, names; pure, unit-tested), `RPCConnection`
(`onRequest`), `DaemonClient.onAppRequest`, `Hesper/App/AppControl.swift`
(runs the same WindowManager / AppModel / DeskController actions as
menus, keys and clicks; changes persist through desks.json and
UserDefaults as usual). Automated app runs (`--selftest-out` & co.) and
`--no-app-control` don't register. Methods:

| Method | Params | Result |
|---|---|---|
| `app.state` | – | `{active, focused: {wall?, agent?, agentWindow?}, currentWall, walls: [{id, title, isMain, isHome, open, visible, scope, scopeName?, arrangement, grouping, minChars, density, collapsed, bandOrder, sidebar, ownWalls, mode: wall\|focus\|compose, focusedAgent?, selectedAgent?, agents: [id], bands: [{key, title, collapsed, agents, pointer}]}] (desk order, home first), agentWindows: [{agent, title, key}], desks: [Desk row], arrangements, groupings, ownWalls, densities, scopes}` |
| `app.open` | one of: `{agent, mode?: focus\|wall\|window\|tab}` (focus: like a notification click — its window, the frontmost wall showing it, else home; wall: selected there; window: ⇧-click; tab: ⌥⇧-click) · `{mode?: "wall", wall?, scope?, newWall?}` (a wall forward; with `scope`: the named wall takes it, or without `wall` a wall having it comes forward, else a new wall — the sidebar's click / ⌥-click; `newWall`: ⌥⌘N) · `{composer: {project?, task?, kind?, profile?, machine?, worktree?, branch?}, wall?}` (⌘N's draft on that wall, filled in, not started; `project` a folder or a project id / name; `kind` picks its default profile) · `{history: {query?} \| true \| "query"}` (⌘Y searching) · `{inbox: true}` (⌘J) | `{agent, mode}` · the wall (as in `app.state`) · `{draft, wall}` · `{wall, query}` · `{wall, needsYou}`; brings the app to the front |
| `app.wall.set` | `{wall?, arrangement?: shelf\|columns\|treemap\|mainStack\|grid, grouping?: auto\|none\|group\|project\|branch, density?: dense\|normal\|N (or minChars), collapse?: [band], expand?: [band], bandOrder?: [band], scope?, sidebar?: bool, ownWalls?: collapsed\|full\|hidden, home?: bool}` | the wall; a band is its key (`p:<project>`, `g:<group>`, …), title or project / group id, `all` in collapse / expand: every band; an unknown band changes nothing (`not_found`) |
| `app.desk` | `{action: list\|save\|switch\|rename\|remove, name?, newName?}` | the desks after it: `[{id, name, current, automatic, thisDisplays, displays, walls, agentWindows}]` (this setup's first); a desk is named by id or name; an automatic desk can't be removed (`invalid`) |

`wall` everywhere: a wall id, its 1-based position (1 = home), `current`
(the key window's wall, else the last in front; the default) or `home`.
Scopes as text (`ScopeSpec`): `all`, `overflow`, `needs-you`, `working`,
`project:<id|name>`, `group:<id|name>`, `filter:machine=M,kind=K,
state=needsYou|working|quiet|ended,project=P,group=G` (a key repeated:
OR; keys: AND); `app.state` prints the same form. Agents by full id,
local id or unique name (`not_found`; two matches: `invalid`).

hesperctl (`relay/cmd/hesperctl/app.go`, group "App", subcommands of
the registry; the parents are aliases: `open ID` = `open agent ID`,
`wall` = `wall show`, `desk` = `desk ls`): `open agent ID
[--window|--tab|--select]` (also `open ID`), `open wall [--wall W]
[--scope S] [--new]`, `open new [--project P] [--task T | TASK…] [--kind
K] [--profile P] [--machine M] [--worktree] [--branch B] [--wall W]`,
`open history [QUERY…]`, `open inbox`; `wall [show] [--wall W]` (a table
and the bands; `--json`: the whole `app.state`), `wall set [--wall W]
[--arrangement A] [--grouping G] [--density D] [--collapse BAND]…
[--expand BAND]… [--band-order B,…] [--scope S] [--sidebar on|off]
[--own-walls M] [--home]`; `desk [ls] | save NAME | switch NAME | rename
OLD NEW | rm NAME`. When hesperd answers `unavailable` (no app), it runs
`open -a Hesper` (else `open ~/Applications/Hesper.app`; `-g` for
`wall` / `desk` except `desk switch`) and retries every 250 ms for up to
10 s; `--no-launch` exits 4 at once. Tests: relay `TestAppControl*`
(forwarding, params and errors through, no app, disconnect mid-request,
timeout with a late answer dropped, latest registration wins), hesperctl
`TestAppCommands*` (each command's params, printing, usage exits, no app /
launch fails / never registers / registers late; tests never launch the
app), HesperCore `AppControlTests` (params, names, wall.set → view,
desks, the request round trip on a scripted socket). Not covered by a UI
run yet: the actions themselves in the running app.

## Persistence

`$HESPER_STATE_DIR/agents.json` (atomic writes) holds the registry. Agents
survive the app closing (the daemon owns the PTYs). When the daemon restarts
(update, reboot), every agent that was running is respawned with its session id
(resume) in the same cwd and shown as `starting`, then its real state.

## App (part A) behaviour

- **Wall**: every agent from every machine as a live tile (libghostty surface
  running `hesperd attach <id> --view`: the agent's last rows), arranged to
  fill the window (see "As built — layout"). Tile
  header: state dot, name, machine + kind mark. Tiles in `approval` / `question`
  / `error` get a ring and inline actions (Allow / Always / Deny for approvals).
- **Focus**: ⌘↩ opens a tile full window as `rw --owner` (resizes the PTY to the
  view); ⌘↩ again or Esc returns. ⌘[ / ⌘] step through agents in wall order.
- **Attention**: ⌘J jumps to the next agent needing you (approval, then
  question, then error; oldest first). Menu bar item with counts; a macOS
  notification on entering approval/question/error (not while focused on it).
- **New agent** ⌘N: a draft tile in the wall (was a sheet; see "As built —
  drafts, overlays, …").
- **⌘K**: palette over agents, projects, machines, actions. ⌘W stops the
  focused agent (confirm in-app). ⌘⇧M moves it to another machine.
- Tokyo Night palette; Ghostty's font settings (read the user's Ghostty config
  for font family/size where possible).
- Performance budget (from the proposal): local keystroke→screen < 5 ms;
  16 live tiles at 120 Hz without dropped frames on an M1 Max; state change →
  ring < 100 ms. Hidden or offscreen tiles must not render.

## Tests and safety (all parts)

- Tests never start real `claude`/`codex`: use fake agent programs (scripts
  that print TUI-like output, emit hook calls, prompt for approval) via test
  profiles. Never touch the live `~/.local/state`,
  LaunchAgents, the relay server or the Mac mini; tests use temp state dirs and
  sockets.
- Go: `cd relay && gofmt -l . && go vet ./... && go test -race ./... && make build`.
- App: `make -C app test build` (whatever part A defines), runnable headless.

## As built

(Each part adds its notes here when it lands.)

### Part D: `hesperd` (relay/cmd/hesperd, internal/agents, internal/ptyhost, pkg/wire)

Code: `pkg/wire` (types, frames, Go client), `internal/ptyhost` (PTY, screen
copy, attach), `internal/agents` (registry, hooks, spawn, persistence,
server, hook configuration), `internal/attachtty` (the tty bridge),
`cmd/hesperd`, `cmd/hesperctl/agents.go`. `internal/vt` gained mode
tracking (mouse, focus, kitty keyboard flags, cursor shape), `Resize` and
`Redraw`.

**Deviations and additions to the wire contract** (all additive unless noted):

- `agents.input` takes two optional booleans: `paste` (wrap `text` in
  bracketed paste when the agent has it on) and `submit` (send `\r` after
  it, 100 ms after a paste). Without them it writes `text` raw, as specified.
- `agents.spawn`: `branch` without `worktree` implies `worktree: true` (the
  daemon never switches the branch of the project itself). New worktrees go
  to `$HESPER_WORKTREE_ROOT` (default `~/worktrees`)`/<project key>/<slug>`
  (project key: path below `$HESPER_PROJECT_ROOT`, default `~/projects`,
  else the base name), branch `worktree/<slug>` unless given; `-2`, `-3` …
  when taken. A `worktree` path that exists must be a worktree of the same
  repository (`exists` otherwise); a missing one is created there. A shell
  ignores `task`. `name` defaults to the slug of the task's first line
  (`"Push provider FCM"` → `push-provider-fcm`), a shell's to `shell`.
- Claude agents get `--session-id <uuid>` on a fresh start, so `sessionId`
  is set from the spawn on. Resume (`agents.resume`, daemon restart): claude
  `--resume <id>`, codex `codex resume [profile options] <id>`, **only when
  the session exists**: a hook confirmed it (UserPromptSubmit, a tool event,
  PermissionRequest, Stop/notify, PreCompact, or SessionStart with source
  `resume`/`compact`/`clear`; a fresh SessionStart does not count, Claude
  writes the transcript with the first message; persisted as
  `sessionSeen`) or its transcript is on disk (Claude
  `<ClaudeHome>/projects/*/<id>.jsonl`, Codex
  `$CODEX_HOME/sessions/*/*/*/rollout-*-<id>.jsonl`). Otherwise the agent
  starts fresh (Claude with the same `--session-id`, Codex with a new
  session; `claude --resume` of a session that was never saved exits 1 at
  once: seen when the trust question exited Claude), and **its task goes
  along only if the agent never got past `starting`** (no
  UserPromptSubmit, tool, turn end, approval or idle seen; persisted as
  `engaged`). A task that already ran ("push the branch") never runs
  twice: an agent that got further starts fresh *without* a prompt, its
  `summary` set to "Previous conversation could not be resumed", and
  becomes `idle` the usual way (Claude's SessionStart, Codex's composer
  rule below). (Found in the second trial: a finished agent whose
  transcript the old environment bug had kept from being saved came back
  after a daemon restart with its task again and redid the work.) An
  agents.json written before the flag counts an agent as engaged when its
  saved state was working/approval/done (idle without a pending task) or
  it had a summary or a confirmed session. Moved agents are engaged. A
  task starting with `-` is passed after `--`.
- `agents.answer` also takes `decision: "trust" | "exit"` for a trust
  question the daemon found on the agent's screen (attention options
  `["trust", "exit"]`, see "First-run screens") and `"skip" | "update"`
  for Codex's update screen; other decisions are `invalid` there.
- Profiles take the placeholders `{name}` (the `--remote-control` name: the
  task's first line, controls blanked, ≤ 60 chars), `{loginShell}`,
  `{project}`, `{dir}`, `{id}`. A profile in `profiles.json` with an empty
  `argv` removes a built-in one.
- `profiles.list` → `{profiles: {name: {kind, argv}}, defaults: {kind,
  kinds: {claude, codex, shell → profile}, projects: {path → profile}}}`.
  `settings.json` has the same `defaults` object, plus `machine` (this Mac's
  short name), `size` (`{cols, rows}` of a new PTY, default 120×40),
  `trustProjects` (default `true`, see "Pre-trust"), `codexSessionHooks`
  (default `true`, see "Codex hooks per agent") and `codexUpdatePrompt`
  (`"ask"` default, or `"skip"`; see "First-run screens").
  Spawn picks: `profile` if given; else the project's profile when its kind
  matches `kind` (or no kind given); else `defaults.kinds[kind or
  defaults.kind]`.
- `hello.machines` lists this Mac first with `route: "local"`, `online:
  true`, `rttMs: 0`; `name` is the host name. The machine short name comes
  from `settings.json` `machine`, else `$HESPER_MACHINE`, else
  `machines.json`'s entry for this host's device id, else **`L`** (host
  names are often asset tags like `ABC123456`, and the short name is in
  every agent id; two unnamed Macs both say `L`, a controller numbers the
  clash, part R). Set `machine` in `settings.json` for a real name.
- Unknown method: JSON-RPC `-32601` with `data.code: "not_found"`; bad
  params `-32602` / `invalid`; everything else `-32000` with the codes
  listed. Every error has `data.code`.
- Remote: ids whose machine part is not this Mac's, `agents.spawn` with
  another `machine`, `agents.move` and attaching to such ids answer
  `unavailable` until part R provides an `agents.Remote` (interface in
  `internal/agents/remote.go`: machines, agents, watch, call, attach, move;
  the server already routes to it).
- `hesperctl`: `attach`, `send`, `stop`, `approve` are the agent commands.
  The old relay commands are `relay-attach`, `relay-send`, `relay-stop`,
  `approve-device`; the old names still run them when given their relay
  flags (`--machine`, `--credentials`, … / `--deny`, `--rights`, …), and
  `approve X` falls back to device approval when X is no agent. Also `rm`
  and `rename`. The daemon socket flag is `--daemon-socket` (the relay
  commands' `--socket` is the fleet socket). Agents can be named by full id,
  local id or unique name. `hesperctl attach` detaches on Ctrl-].

**States** (hooks and process events only, as specified):

- `starting` until the first hook (a fresh agent with a task stays
  `starting` through `SessionStart` until `UserPromptSubmit`, at most 5 s).
  Without installed hooks a Claude agent stays `starting`: part C must
  install them (`hesperd hooks install`); Codex agents bring their own
  ("Codex hooks per agent" below). Shell agents have no hooks: hesperd
  watches their terminal (see "As built — shell agents" under the agent
  lifecycle CLI). By default they are `idle` from the start until they
  exit; one spawned with `track` is `starting` until its task is typed,
  `working` while a command runs, else `idle`.
  **Codex without a prompt** (after `codex resume`, or a fresh Codex with
  no task): Codex 0.160 sends no hook until the next prompt (seen in the
  trial: resumed Codex agents sat in `starting` while Claude's became
  `idle` via SessionStart). Rule: a starting Codex agent that expects no
  prompt is `idle` once its process is up and either its SessionStart hook
  arrives (if Codex sends one) or its screen shows Codex's composer (a
  `› ` row and the `? for shortcuts` / `N% context left` footer, nothing
  with "esc to interrupt", no first-run screen) and no output came for 1 s
  (checked with the first-run screens, every 500 ms; `codex.go`). Checked
  with the real Codex 0.160.1 under a scratch hesperd: idle 1.5–2 s after
  spawn.
- `working`: UserPromptSubmit, PreToolUse, PostToolUse, PreCompact.
  `approval`: PermissionRequest (attention `{kind: "approval", title:
  <tool name>, detail: <command / file / pattern>, options: [allow, always,
  deny]}`) or Notification `permission_prompt`. `question`: AskUserQuestion
  (PreToolUse or PermissionRequest; detail = the first question) or
  Notification `elicitation_dialog`. `done`: Stop / Codex notify
  `agent-turn-complete`, with `summary` = the last non-empty line of the
  turn's final message (from the payload, else the transcript's tail).
  `idle`: SessionStart, Notification `idle_prompt` (not after done),
  `agents.answer deny` without message. `error`: StopFailure (attention
  `{kind: "error", title: "Agent error", detail}`), or the process ending on
  its own with a non-zero code or a signal (attention `{kind: "error",
  title: "Exited", detail: "exit status 2" / "SIGSEGV"}`; `exit` set). A
  process that fails within 3 s of its start also gets its last three
  non-blank screen lines: `"exit status 1: No conversation found with
  session ID: …"`.
  `exited`: the process ended with 0, or by `agents.stop` (`exit.signal`
  `"SIGHUP"`, or `"SIGKILL"` after the 5 s grace).
- An agent in `approval`/`question` leaves it only on the asked tool's
  PostToolUse, a new prompt, the turn's end, an error, or `agents.answer`
  (hooks are async, so other tools' events are ignored meanwhile).
  `agents.answer` sets `working` (allow, always, deny with message) or
  `idle` (deny); keys: claude `1` / the "don't ask again" choice / Esc,
  codex `y` / `a` / Esc; a deny message is pasted and submitted 300 ms
  after Esc. Claude 2.1.29x's Bash prompt ("Do you want to proceed? ❯ 1.
  Yes / 2. Yes, and don't ask again for git push commands in … / 3. No,
  and tell Claude what to do differently (esc)", recorded in
  `internal/fakeagent/testdata/claude-permission.txt`) has it at 2, but a
  prompt without it has "No" at 2: `always` types the number of the
  choice starting "Yes, and don't ask again" / "Yes, allow all" below the
  prompt's "Do you want to …" line, is `invalid` when the choices have
  none ("answer allow or deny"), and types `2` when no choices are drawn
  yet.
- Hooks find their agent by `HESPER_AGENT_ID` (kind must match; a hook of
  another session in the agent's environment, e.g. a nested `claude -p`, is
  ignored unless it is the agent's own `SessionStart` with source
  `clear`/`resume`/`compact`, which updates `sessionId`). Without it (Codex
  runs hooks in its own server): by `session_id`, then (Codex only) by
  `cwd` among running Codex agents (one without a session first). Codex
  tool/approval events of a turn that already ended are dropped.
- Codex approval fallback (`internal/agents/codex.go`): on a BEL outside an
  OSC string, and on output while no PermissionRequest hook has arrived for
  that agent, the daemon looks at the agent's own screen (at most every
  250 ms) for Codex's approval overlay ("Would you like to run the following
  command?" … "Yes, proceed"); found: `approval` (title `Command`/`Edit`,
  detail the command line); the overlay gone: `working`.
- **Pre-trust** (`internal/agents/trustwrite.go`; `settings.json`
  `trustProjects`, default `true`): on `agents.spawn` of a Claude or Codex
  agent, the folder the user chose is recorded as trusted where the agent
  records it when the user accepts its own question (as `ghosty-handoff
  trust` did): Claude `~/.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json`)
  `projects[<key>].hasTrustDialogAccepted = true` (a new entry gets Claude's
  own defaults); Codex `$CODEX_HOME/config.toml` `[projects."<key>"]
  trust_level = "trusted"` (an existing `trust_level`, e.g. `"untrusted"`,
  stays). `<key>`: the repository's main worktree root (Claude 2.1: "trusting
  it trusts that whole repository"), else the folder, symlinks resolved.
  Never the home folder or above; Claude's file only when it exists (Claude
  creates it on its first run). Writes never clobber: under Claude's own
  lock (proper-lockfile's `<file>.lock` directory, 3 s wait, 10 s stale),
  temp file + fsync, the target re-read just before the rename and the edit
  started over (≤ 8 times) when it changed; the result must parse to the old
  content plus the one change (Claude's JSON keeps key order, numbers and
  strings byte for byte, two-space indent; Codex's TOML is edited as text:
  one line into the project's table or a new table at the end; a file with
  `projects` as an inline table or dotted keys is not edited). A failure is
  logged and the agent asks instead. Resume and restart do not pre-trust.
- **First-run screens** (`internal/agents/firstrun.go`): while a Claude or
  Codex agent is `starting`, the daemon looks at its screen every 500 ms (no
  hook reports these). Claude's trust question ("Quick safety check: Is
  this a project you created or one you trust? … ❯ 1. Yes, I trust this
  folder / 2. No, exit", Claude 2.1.29x, recorded in
  `internal/fakeagent/testdata`) or Codex's ("Trust this folder?") becomes
  state `question` with attention `{kind: "question", title: "Trust
  folder", detail: "Trust ~/path?", options: ["trust", "exit"]}`;
  `agents.answer` `trust` types `1` (and Enter 400 ms later if the question
  is still there), `exit` stops the agent. Claude's theme screen, login
  ("Select login method", "Browser didn't open", "Paste code here"), the
  API-key question and Codex's sign-in become `question` with a title
  (`Claude setup`, `Log in`, `API key`) and a detail saying to open the agent,
  no options. When the screen is gone the agent is `starting` again until
  its hooks say more. When the folder is untrusted at spawn, the task is
  still typed in after the question (as before: not on the command line;
  bracketed paste + Enter once the screen is stable).
  **Codex's update screen** (seen in the trial on `codex resume`: "Update
  available · 0.160.0 → 0.160.1 · Release notes … › 1. Update now (runs
  `brew upgrade --cask codex`) 2. Skip 3. Skip until next version · enter
  continue · esc skip", recorded in `fakeagent/testdata/codex-update.txt`;
  Codex waits there) becomes `question` with attention `{title: "Codex
  update available (0.160.0 → 0.160.1)", detail: "Update Codex now (runs
  brew upgrade --cask codex) or skip it and go on.", options: ["skip",
  "update"]}`; `agents.answer` `skip` sends Esc (the screen's own "esc
  skip", whatever the cursor is on), `update` types `1` and Enter 400 ms
  later if the screen is still there (Codex then updates and exits). The
  "✨ Update available!" box Codex leaves in the history after a skip is
  not the screen. `settings.json` `codexUpdatePrompt`: `"ask"` (default)
  or `"skip"`: Codex agents get `-c check_for_update_on_startup=false`
  (Codex's own config key; verified with 0.160.1 in a scratch
  `CODEX_HOME` whose `version.json` named a newer release: the screen
  stays away), and should the screen still show, the daemon sends Esc
  itself. Claude's "Update installed · Restart to update" footer is
  informational and raises nothing.

**Attach, exactly:**

- First line from the client; the daemon answers `{"ok":true,"cols":C,
  "rows":R}` (after applying an owner's size) or `{"ok":false,"error":{
  "code","message"}}` and closes. Then frames. The first frame is always
  DATA with the redraw: `ESC[?2026h` … `ESC[?2026l` (left open if the agent
  is inside its own synchronized update), which resets the viewer (main
  screen, scroll region, modes, charsets, tab stops), paints the main
  screen, enters `?1049h` and paints the alternate screen when it is on,
  restores both saved cursors, scroll region, cursor keys/keypad/bracketed
  paste, mouse tracking and encoding, focus reports, kitty keyboard flags
  (stack included), cursor shape and visibility, pen, charsets and the
  cursor (a pending wrap included). No scrollback is sent.
- A viewer that falls 4 MiB behind gets, in place of what it missed, a SIZE
  frame and a fresh redraw (then live output). A viewer that takes nothing
  for 30 s is dropped.
- `ro`: DATA from the client is discarded. RESIZE applies only from the
  current owner (most recent `rw` + `owner:true`); an `ro` attach never owns.
  When the owner detaches the size stays and nobody owns it.
- The daemon is the agent's terminal: it answers DA1/DA2/DA3, DSR 5/6
  (CPR), XTVERSION (`hesperd`), DECRQM, kitty `CSI ? u`, `CSI 14/16/18 t`,
  OSC 4/10/11/12 color queries (Tokyo Night: fg `#c0caf5`, bg `#1a1b26`),
  XTGETTCAP/DECRQSS (not supported), color scheme (dark), and drops OSC 52
  clipboard reads. These queries are removed from the output viewers get,
  so a viewer (libghostty) never answers them. Everything else, BEL and
  titles included, passes unchanged.
- EXIT `{"code":N|null,"signal":"SIGHUP"|absent}` is the last frame; the
  daemon then half-closes. Attaching to an exited agent gives its last
  screen (blank after a daemon restart) and EXIT. `agents.resume` starts a
  new process: viewers attach again (the app re-attaches when the agent's
  `exit` becomes null).
- `hesperd attach <id> [--ro] [--owner]` exits 0 on EXIT or a signal, 1 if
  the attach fails or the connection drops. Without `--owner` it follows
  SIZE by setting its own terminal's window size (TIOCSWINSZ) to the PTY's;
  the app should take the grid from `agent.size` / SIZE for font scaling.
  A terminal emulator that sizes its grid from its window (Ghostty, the
  app's surfaces) does not follow that, so the app never uses a raw attach
  without `--owner`: non-owners are `--fit` views (see "Size rule with
  several windows").
  With `--owner` it sends its size at attach and on SIGWINCH.

**Agents' environment:** the login environment (`$SHELL -l -c 'printf
<marker>; /usr/bin/env -0'`, resolved once at daemon start: two tries of
10 s each, the shell in its own process group killed whole at the timeout,
1 s `WaitDelay` so a profile's background process holding the output does
not hold the daemon; only what follows the marker counts, NUL-separated, so
values with spaces pass unchanged; a non-zero exit with the environment
printed still counts; no output or no PATH is a failure; the daemon's own
environment when both tries fail). Its **PATH is always completed**: the
login shell's entries, then the daemon's own PATH (the LaunchAgent's), then
`~/.local/bin /opt/homebrew/bin /opt/homebrew/sbin /usr/local/bin /usr/bin
/bin /usr/sbin /sbin`, each once, empty and relative entries dropped; only
`:` separates entries (the mini's login PATH starts with `/Applications/
Android Studio.app/Contents/jbr/Contents/Home/bin`). The daemon logs once
at INFO where the environment came from (login shell and how long it
took, or the fallback with each try's error) and the PATH. **The agent's
command is found on that PATH** (`ptyhost.LookPath` over the agent's env,
never `exec.Command`'s lookup on the daemon's PATH); not found is
`"claude" not found on the agent's PATH (<PATH>)`. A start whose command is
missing waits up to 15 s (`CommandWait`) for it to appear, without the
lock (spawn, resume): Claude Code's npm auto-updater (`npm install
--global @anthropic-ai/claude-code`, run again and again by long-running
older Claude processes) removes `/opt/homebrew/bin/claude` for ~5 s each
time. (Found on the Mac mini: `hesperctl resume` at 11:12:36 hit such a
reinstall, 11:12:33–38, and failed with exec's `"claude": executable file
not found in $PATH`.) The login shell starts from a **clean** environment, not the
daemon's: only `HOME USER LOGNAME SHELL TMPDIR PATH LANG LC_* XDG_*
SSH_AUTH_SOCK __CF_USER_TEXT_ENCODING CLAUDE_CONFIG_DIR CODEX_HOME` and
hesperd's own configuration (`HESPER_STATE_DIR HESPER_CONFIG_DIR
HESPER_PROJECT_ROOT HESPER_WORKTREE_ROOT HESPER_MACHINE`) pass, so what the
shell's profile sets is kept (the user's own `CLAUDE_CODE_*` settings
included) and what the daemon inherited is not. Then, at spawn, these never
reach an agent: other terminals' (`TERM TERM_PROGRAM* TERM_SESSION_ID
COLUMNS LINES WINDOWID STY LC_TERMINAL*`, `TMUX*`, `GHOSTTY_*`, `ITERM_*`,
`KITTY_*`, `WEZTERM_*`, `ALACRITTY_*`, `VSCODE_*`), shell bookkeeping
(`PWD OLDPWD SHLVL _`), Claude Code's markers for its child processes
(`CLAUDECODE CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION
CLAUDE_CODE_SESSION_ATTENDED CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_SSE_PORT
CLAUDE_CODE_CHROME_MCP_ORG_DENIED CLAUDE_PID CLAUDE_EFFORT
CLAUDE_PROJECT_DIR CLAUDE_ENV_FILE AI_AGENT TRACEPARENT TRACESTATE`,
`GIT_EDITOR=true`), and Codex's runtime (`CODEX_*` except `CODEX_HOME
CODEX_API_KEY CODEX_ACCESS_TOKEN CODEX_CA_CERTIFICATE CODEX_SQLITE_HOME
CODEX_GITHUB_PERSONAL_ACCESS_TOKEN CODEX_CONNECTORS_TOKEN`). (Found in a
trial: a hesperd started from inside Claude Code passed
`CLAUDE_CODE_CHILD_SESSION` on, and the agent's Claude turned transcripts
off — "Transcript saving is off — inherited CLAUDE_CODE_CHILD_SESSION
marker" — which breaks resume and moves.) Set for every agent:
`TERM=xterm-256color`, `COLORTERM=truecolor`, `TERM_PROGRAM=hesperd`,
`HESPER_AGENT_ID` (full id), `HESPER_SOCKET`. Each agent is its own session
and process group; stop sends SIGHUP to the group.

**Hooks** (`hesperd hooks print`, `hesperd hooks install [--dry-run]
[--bin P] [--claude-settings P] [--codex-home D]`; install replaces
its own earlier entries, keeps everything else, backs files up as
`*.hesperd-backup`, writes through symlinks): Claude `"<bin>" hook claude
<Event>` for SessionStart, UserPromptSubmit, PreToolUse (async, `*`),
PostToolUse (async, `*`), PermissionRequest (`*`), Notification (`*`),
PreCompact (auto, manual), Stop, StopFailure, SubagentStop, SessionEnd.
Codex hooks.json `"<bin>" hook codex <Event>` (timeout 5, async except
SessionEnd, whose timeout is 3: Codex clamps it) for SessionStart,
UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest, Stop,
SessionEnd; config.toml `notify = ["<bin>", "hook", "codex", "notify"]`. An
existing notify program of someone else is **chained**: `notify = ["<bin>",
"hook", "codex", "notify", "--then", <its argv…>]`; `hesperd hook codex
notify --then P… <json>` delivers the event, then starts `P… <json>`
detached (also when the daemon is down). A rerun keeps the chain (a new
`--bin` included); a notify that is not a one-line array of strings is left with
a warning. `hesperd hook` reads stdin (or the JSON argument notify passes),
drops tool output from payloads over 1 MiB, waits at most 250 ms for the
daemon and always exits 0 (≈ 10 ms with the daemon down).

**Persistence:** `$HESPER_STATE_DIR/agents.json` (`{version: 1, agents:
[Agent + local, running, pendingTask, endedTurn, sessionSeen, engaged, respawn]}`,
0600, temp file + fsync + rename, ≤ 50 ms after a change) and
`projects.json` (recent projects, 30). `hesperd serve` on SIGTERM hangs up
every agent and keeps them `running`; on start those are respawned in the
same cwd as `starting`: with resume when their session exists (rule
above), else fresh — with their task only when they never got past
`starting`, else without it and with the "could not be resumed" note
(shells always fresh). A daemon stop never makes an agent `exited`: its
process's end while the daemon stops (Claude ends with status 0 on the
hangup) keeps it `running` in the file. **A respawn or resume that fails is
`error` "Not resumed" with the reason** (a profile that is gone, the
command not on the PATH), never `exited`; a respawn at start that failed is
saved with `respawn: true` and tried again at the next start (and, when
only its command was missing, as soon as it is back within `CommandWait`);
`agents.stop` on it clears that. **A respawned or resumed claude/codex that
ends within 3 s, even with status 0, is `error` "Not resumed"** ("ended at
once after coming back (exit status 0): <its last screen lines>"), unless
the user stopped it or answered a first-run screen (a Codex update ends
it). `agents.resume` brings any of them back. (Found on the Mac mini: a
done agent respawned with `--resume` after the 11:10 update restart, its
remote-control bridge came up, and Claude ended with status 0 two seconds
later; the daemon recorded a quiet `exited`.) The socket directory is made 0700 (part C: this is the live
`~/.local/state/hesper`).

**Codex hooks per agent** (`internal/agents/codexargs.go`; Codex
0.160.0, checked against the real binary's `codex app-server` `hooks/list`
and `config/read` with a scratch `CODEX_HOME`, no model call): Codex has
hooks (`features.hooks`, stable, on), read from `~/.codex/hooks.json`,
`~/.codex/config.toml` (`[[hooks.<Event>]]` tables), the project's
`.codex/` and — despite the docs saying otherwise — **`-c` overrides**
(source `sessionFlags`, key `/<session-flags>/config.toml:<event>:0:0`). A
hook runs only when trusted: Codex compares `hooks.state."<key>".trusted_hash`
with `sha256:` + SHA-256 of the canonical JSON (sorted keys, compact) of
`{"event_name": <snake_case event>, "hooks": [<handler with async, command,
timeout, type>], "matcher"?}` (`/hooks` writes that trust into
config.toml; `--dangerously-bypass-hook-trust` would skip the check for
every enabled hook, the user's untrusted ones included, and is not used).
`-c` layers merge with the user's: the user's hooks and their trust stay.
So with `settings.json` `codexSessionHooks` (default `true`) every Codex
agent is started with `--no-daemon -c features.hooks=true -c
hooks.<Event>=[…] -c hooks.state={…trusted hashes…} -c notify=["<hesperd>",
"hook", "codex", "notify"(, "--then", <the user's notify>)]` after the
profile's options (for `codex resume` before the session id): hesperd's
hooks, trusted for exactly these commands, without touching `~/.codex`.
`--no-daemon` gives the agent its own in-process app server, so the
overrides are certainly this session's, hooks run in the agent's
environment (`HESPER_AGENT_ID` names it), and the user's shared Codex
server and its sessions are left alone — a side-by-side trial does not
disturb a live setup. Skipped: the hooks when the Codex home's hooks.json
already has hesperd's (`hesperd hooks install`; they would run twice);
the notify when config.toml's is hesperd's already. The user's own hooks
(trusted ones in hooks.json) still run in such agents too, and the user's
own notify is chained whole, a wrapper with its own `--previous-notify`
(Codex Computer Use's `SkyComputerUseClient turn-ended`) included;
`hesperd hooks install` does the same.
**The trial's "⚠ 2 warnings"** (reproduced with Codex 0.160.1 in a scratch
`CODEX_HOME` shaped like the user's — hooks.json with the tmux setup's
hooks, trusted in `[hooks.state]`, the chained notify, the user's
`[features]`/`[tui]` — launched in a PTY without a prompt, F2 to read
them; and in the real `~/.codex/logs_2.sqlite`, read only): (1) "clamping
SessionEnd hook timeout to 3s in ~/.codex/hooks.json" — the tmux setup's
leftover `ghosty-agent-event` SessionEnd hook has `timeout: 5` (Codex
hashes the clamped handler: its trusted hash uses 3); (2) "MCP client for
`vibes` failed to start" — the user's `vibes` MCP server's OAuth refresh
fails ("reauthorization required"). Neither comes from hesperd's `-c`
options (with them alone a scratch home shows no warning; they leave the
user's hooks and their trust intact, no "hooks need review"). (1) goes
away with `hesperd hooks install` (it replaces those entries; hesperd's
SessionEnd timeout is 3), (2) by logging in to it again (`codex mcp login vibes`). `hesperd hooks
codex-args [--bin P]` prints the options (one per line), e.g. to start a
Codex by hand: `codex $(hesperd hooks codex-args)`. The bin is the running
hesperd (`Options.HookBin`). Not verified without a model call: a full
interactive Codex turn with these options (the TUI with `--no-daemon`
loads the same config layers as the app server checked here).

**Measured** (M-series Mac, `go test -bench`): attach redraw of a full
200×60 screen, every cell colored: 0.44 ms (vt `BenchmarkRedraw200x60`);
keystroke through an attach connection to the PTY and the echo back:
35 µs (`ptyhost` `BenchmarkKeystrokeRoundTrip`); output fan-out to 16
viewers: 10 µs per 600-byte chunk, no allocation per viewer; hook call:
78 µs; 30 idle agents: 0.15 ms daemon CPU in 2 s (`TestIdleAgentsCostNoCPU`).

**Not done here:** the daemon keeps no scrollback; no `codex fork`; no
notifications (the app's). (Activity, `projects.clone` and moves: part R.)

### Part A: `Hesper.app` (app/)

Code: `app/Sources/HesperCore` (pure Swift: agent model, `DaemonClient`,
`AgentRegistry`, attention order, wall grid, font fitting, key routing, the
read-only Ghostty font reader), `app/Sources/Hesper` (AppKit + SwiftUI;
`Engine/` is the only code that imports libghostty), `app/Tests`,
`app/Tools/fake-hesperd` (Go, **test-only** stand-in daemon + fake TUIs).

**Engine: libghostty from the GhosttyKit XCFramework.** `app/Package.swift`
pins `Lakr233/libghostty-spm` `exact: 2.2.2026100501` (Ghostty
`35a81a980bb9`, reports `1.3.2-HEAD+35a81a980`), checksum-verified binary
target, no Zig. We use only its `GhosttyKit` product (the C API) and wrap it
ourselves (`GhosttyRuntime`, `GhosttySurfaceView`, ~600 lines modelled on
Ghostty.app's SurfaceView) behind `TerminalSurface`, so swapping the engine
touches `Engine/` and `TerminalEngine.makeSurface()` only. Building
libghostty from source would need a pinned Zig plus Xcode 26 metallib
workarounds for no gain at this pin; revisit only if the prebuilt lags a fix
we need. The package's own Swift wrapper (`GhosttyTerminal`) is not used: it
draws every surface from the main thread on a display link, whereas
libghostty on macOS renders on its own thread into an `IOSurfaceLayer` it
installs on our view. SwiftTerm was not needed.

libghostty findings that shaped the code:

- `ghostty_surface_config_s.command` always runs through a login shell
  (`/usr/bin/login -flp $USER bash -c 'exec -l …'`), and `command` forces
  `wait-after-command`. So each tile is two helper processes (login +
  `hesperd attach`), and "Last login" flashes until the attach redraw clears
  it (silent with `~/.hushlogin`). There is no `direct:` form in the C API.
- Changing a live surface's font (`set_font_size` binding action) or the app
  config (`ghostty_app_update_config`) with ~16 surfaces of different font
  sizes crashes inside libghostty (SIGSEGV at 0x4614, font grid path,
  reproducible). The app therefore never changes a live surface's font or
  config: a tile whose fitted font changes is re-attached at the new size
  (debounced 150 ms; the daemon's redraw makes this exact), and focus
  surfaces live in a **second libghostty app** created with
  `window-vsync = false` (tiles stay vsync-paced, the focus view renders as
  soon as output arrives).
- The initial `CELL_SIZE` action fires inside `ghostty_surface_new`; the
  view must already be the surface's owner. Frames are presented by a block
  libghostty dispatches to the main queue, so the main thread is on every
  tile's present path. Wakeups are coalesced into one `ghostty_app_tick`;
  actions from other threads hop to main; all app-level actions report
  handled. `keybind = clear` (only ⌘C/⌘V kept): every other key goes to
  the agent or the app.

**How the contract is used**

- Tiles: `hesperd attach <id> --view` (see "As built — layout"): the font
  comes from the card's width, the rows from its height, the daemon renders
  the agent's last rows into exactly that grid. A changed `agent.size` only
  re-lays out (a new font re-attaches). Hidden tiles (focus mode, occluded or
  minimized window) get `ghostty_surface_set_occlusion(false)` and render
  nothing. Tiles re-attach when the attach process ends while the agent
  still runs (≤ 5 tries / 10 s), after the control connection comes back,
  and when `exit` becomes null (resume). An agent is live iff
  `exit == null && state != exited`.
- Focus: `hesperd attach <id> --owner` (rw), the user's Ghostty font size;
  its grid becomes the PTY's size, tiles refit when `agents.changed` brings
  the new size.
- Control: one connection, `hello` → `agents.subscribe` → `agents.list`
  (drops agents not in the list and not seen since the subscribe), then
  notifications; reconnects with 0.1 → 2 s backoff. The socket is
  `--socket` / `$HESPER_SOCKET` / `--state-dir` / `$HESPER_STATE_DIR`, the
  binary `--hesperd` / `$HESPERD_BIN` / next to the app executable /
  `~/.local/bin` / PATH; attach processes get `HESPER_SOCKET` (and
  `HESPER_STATE_DIR`) so they reach the same daemon.
- Part D deviations handled: `exit` object, `error` with `exit` (Resume
  offered), `profiles.list` `defaults {kind, kinds, projects}`, attach errors
  as objects, `-32601`/`not_found`, `agents.input` `paste`/`submit`.

**UI as built:** wall (arrangements below), card header (state dot,
name, project · branch, elapsed time while working, machine · kind chip;
remote chips in purple), ring for approval / question / error with a soft
glow, inline Allow / Always / Deny… (also ⏎ / A / N on the selected tile) in
the card's own attention band, summary line for done/idle, exited band with
Resume. Focus with the same header and band, ⌥⌘← ⌥⌘→ (⌘[ ⌘]) stepping in wall order.
⌘J attention queue. Status (counts, machines with RTT, clickable attention
chips → ⌘J, working count), the layout switch, New Agent and ⌘K search in
the window's unified toolbar. Menu bar item (counts, the queue, New Agent). Notifications on
entering approval/question/error unless the app is active and focused on
that agent (click opens it). ⌘N sheet (multi-line task, name, machine,
profile with the daemon's project/kind default, recent projects + Choose
Folder + Clone URL, worktree + branch, ⌘↩). ⌘K palette over agents,
actions (open, rename, stop/resume/remove, move), projects, machines. ⌘W
stop with in-app confirm (remove for ended agents). ⌘⇧M machine picker;
`unavailable` shows as a toast. Closing the window only hides it; restarting
the app re-attaches everything (16 tiles live in ~0.4–0.6 s with calm
agents).

**Deviations / needs:**

- Esc is never taken from the agent (Claude and Codex use it to interrupt;
  Claude's double-Esc rewinds). Focus is left with ⌘↩ or ⌘Esc.
- **Need (part D):** cloning has no method. The sheet calls
  `projects.clone {url, machine?}` → `{path}` and, on `-32601`, tells the
  user to clone and choose the folder. Proposed: hesperd clones into
  `$HESPER_PROJECT_ROOT/<repo>` (or a given `into`), returns the path and
  adds it to recent projects.
- **Need (part C):** bundle `hesperd` as `Hesper.app/Contents/MacOS/hesperd`
  (the app looks there first) and sign both with the Developer ID;
  `app/Makefile` takes `SIGN_IDENTITY` (ad-hoc `-` today). Notifications need
  the signed bundle id `de.olezierau.hesper.mac`.
- Nice to have (part D): an `activity` field (current tool) for tile
  headers.

#### As built — layout

**Bottom-anchored tiles.** Scaling a whole 200×60 screen into a card made
text unreadable, and a card sized to the agent's aspect left a band of empty
card below the terminal (measured on a 2000×1290 pt window with 6 agents:
296 px = 148 pt of every card's terminal area, 25 %, showed nothing). Tiles
now show the agent's *last* rows: the font is the largest quarter point at
which the agent's whole `cols` fit the card's width (9 pt ≤ font ≤ the
user's Ghostty font; wider screens are cut on the right at 9 pt), the rows
are as many whole cells as fit the card's height, and the terminal area is
exactly `rows × cell height` (engine pixels, measured per font and cached),
bottom-anchored above the attention band. The surface's background is the
card body's, so the sub-cell remainder is invisible. Measured after
(LayoutProbe, every arrangement, 1–16 agents, 1000×700 to 2000×1290):
terminal area − engine grid = **0 px** on every tile; card body − terminal
< 1 cell.

**Daemon view** (wire: "View attaches" above; `internal/ptyhost/view.go`,
`hesperd attach --view [--view-rows R]`): read-only viewers outside the
output fan-out; per render one `vt.RowANSI` per window row, diffed against
the last render (35 µs for a full colored 120×40 window,
`BenchmarkViewWindow`). The bridge sends its terminal's size as RESIZE on
SIGWINCH, so a card that only gets taller or shorter needs no re-attach.
`fake-hesperd` (test-only) imitates it by rewriting its fake TUI's CUP rows.

**Arrangements** (`HesperCore/GridLayout.swift`, `WallLayout.make`; toolbar
switch, ⌥⌘1–5, ⌥⌘L cycles, View menu, Settings ⌘,; remembered in
UserDefaults `wallArrangement`):

- **E Grid + Shelf** (default): done/idle/exited agents become compact
  shelf cards (header + summary, 74 pt, ≥ 220 pt wide, more shelf rows when
  needed; their attach is closed); active agents fill the rest with A: the
  fewest rows such that every card is ≥ `minChars` × cell width at 9 pt + 2
  insets wide, a short last row stretched to the full width. Cards move only
  when an agent comes, goes or moves to/from the shelf — never on approval
  or question. All quiet: the plain fill grid.
- **B Columns**: one row of full-height cards, `max(min width, width / n)`
  wide; scrolls sideways, edge fades show cards off screen, the selection
  scrolls into view.
- **C Treemap**: squarified, approval/question/error 4, working 2, quiet 1;
  gaps inside the rects, outer margin exact.
- **D Main + Stack**: the ⌘J target (else the selection) full height at
  55 % (≥ min width), the rest in equal rows on the right, another stack
  column whenever rows would drop below 8 rows at 9 pt.
- **Grid** (⌥⌘5, `HesperCore/GridArrangement.swift`): every card exactly the
  same size (quiet agents too, no shelf, no weights) on one lattice of `C`
  columns over the full width. Bands are boxes on the lattice in band order:
  a band of `k ≤ C` takes `k` cells of a band row and the next band follows
  on that row while it fits; a band of `k > C` is full width in rows of `C`
  (last row left-aligned). Collapsed bands: their heading, one cell wide, in
  a strip on top. `C` maximizes the cell area among cells ≥ the minimum
  card and 1.2–3:1 wide; else the fitting `C` nearest that aspect; else the
  most columns of minimum width and the wall scrolls down. The previous `C`
  stays while it is ≥ 90 % as good (`WallLayout.gridColumns`, hysteresis).

Each wall has its own arrangement (windows.json, desks); the top bar's
switcher, its popover (opens on the current row), the View menu, ⌥⌘1–5 and
Settings › Wall (the frontmost wall, `FrontWall`) all read and write that
one value.

`minChars` is 80 (Settings: 60 dense, 100; View › Dense Cards). Cards never
get shorter than 8 rows: the wall scrolls down instead. Frame changes
animate (0.25 s, none with Reduce Motion); the terminal keeps its final
size during the animation and a font change re-attaches once, debounced
300 ms (libghostty still never changes a live surface's font).

**Measured after** (`make perf`, 16 flood tiles, grid + shelf): main thread 120.0 fps, 0 dropped, worst 8.4 ms; tiles 116 presents/s median; keystroke → grid p50 1.9 ms (p90 3.6), → presented frame p50 3.1 ms (p90 5.8); state → ring p50 4.7 ms; app CPU 1.8 cores (was 7.1: tiles draw only the rows they show); footprint 347 MB.

**Spacing scale** (`Theme.swift` `Metrics`): margin 20, gap 14, card radius
12, header 32, terminal inset 9, bands 46 (one row) / 80 (two rows), 1 px
border + soft shadow, rings 2 px (3 selected) with a glow; Tokyo Night.

**Build, test, measure** (Xcode 26.3 toolchain via `DEVELOPER_DIR`, no
Xcode UI; SwiftPM + Make — XcodeGen would only add a generated project):

- `make -C app build` → `app/build/Hesper.app` (Info.plist, generated
  placeholder icon, ad-hoc codesign, verified) and the test-only
  `app/.build/fake-hesperd`.
- `make -C app test`: 39 unit tests (JSON-RPC client against a scripted
  socket server incl. reconnect/timeouts/error codes, attention order, wall
  layout for every arrangement (fill to the margin, minimum card, shelf,
  scrolling, treemap weights, main slot, terminal = whole rows with < 1 cell
  of slack, font fit), cell table, key routing, registry/reconcile, model decoding, Ghostty
  config reader, environment) + `go vet` of the fake + the UI smoke test.
- `make -C app test-ui`: launches the real app against the fake daemon in a
  temp dir and drives it through the user's key paths (28 checks: tiles
  render, terminal area = engine grid (±1 px), ring, ⏎ allow, ⌘J order, ⌘↩
  focus owning the PTY size, typing, tiles stop rendering in focus, ⌘] step,
  refit after resize, done summary, done → shelf without a terminal, ⌥⌘2 /
  ⌥⌘L / ⌥⌘1 layouts with exact tiles, ⌘⇧M unavailable, ⌘W confirm → exited → resume re-attach, spawn,
  ⌘K). `make -C app test-ui-real` runs the same against the real
  `relay/dist/hesperd` (temp state/config dirs, `profiles.json` mapping
  every profile to the fake TUI, states driven by `hesperd hook claude …`
  processes): 28/28.
- `make -C app layout [AGENTS=6 WINDOW_SIZE=1600x1000 ARRANGEMENT=shelf
  SHOW=focus|palette|new|confirm|empty SHOT=out.png]`: the app dumps every
  card's frame, terminal area and engine grid (`--layout-out`, LayoutProbe)
  and the script screenshots the window (`screencapture -l`); `make
  layout-matrix OUT=dir` runs arrangements × counts × sizes
  (`Tools/layout-summary.py` prints a dump). The fake daemon's
  `--demo-states` gives agents a mix of states, `--sizes 120x40,200x60`
  per-agent PTY sizes.
- `make -C app perf [PERF_TUI=flood|agent PERF_AGENTS=16 PERF_FPS=120
  PERF_SECONDS=10]`: 16 agents on the fake daemon, display-link frame
  pacing with os_signposts (`vsync`), per-tile presented frames, CPU,
  `phys_footprint`, keystroke latency in focus (real key events; to the
  echo in libghostty's grid, and to the next presented frame), state change
  → tile, and the RSS of all helper processes. JSON in
  `app/build/perf-last.json`.

**Phase 0 measurements** (M1 Max, 120 Hz built-in display, release build;
note the machine was shared with other agents' builds, load average 13–33):

| | 16 calm agents (`agent` TUI) | 16 floods (every cell, every frame) |
|---|---|---|
| main-thread frames | 120.0 fps, **0 dropped** in 15 s, worst 8.4 ms | 117.7 fps, 37 dropped (≈ 2 %), worst 22.5 ms |
| frames reaching tiles | 11/s each (all content) | 31 content-fps per tile (producer-bound), 39 presents/s |
| app CPU | 14 % | 7.1 cores (libghostty renderers re-shaping 16 full screens) |
| keystroke → grid (p50/p90) | 0.8 / 4.4 ms | 2.0 / 3.0 ms |
| keystroke → presented frame (p50/p90) | **2.7 / 5.5 ms** | 3.1 / 5.9 ms |
| state change → tile (p50/p99) | 2.3 / 19 ms | 22 / 58 ms |
| app footprint | 370 MB | 510 MB |
| app + 32 helpers (login, attach) RSS | 515 MB | 605 MB |

Keystroke times include the full path through `hesperd attach`, the daemon
and the agent's PTY; "presented" is libghostty handing the frame to the
layer, the compositor's next refresh comes on top. Verdict: **go** — the
calm wall holds 120 Hz with no drops, typing is ≈ 3 ms to the frame, the
ring follows a state change in a few ms. Only the synthetic flood (far beyond
what agents print) drops ~2 % of frames, with all cores busy. With vsync on
for the focus view (one app) keystroke → frame was 11 ms p50, hence the
second libghostty app.

#### As built — drafts, overlays, typing on the wall, scrollback

Design: the "Starting an agent" design notes, plus the trial's feedback (one tile font, a calm footer, typing
in tiles, scrolling back).

**Wire additions** (all additive; older clients ignore them):

- `drafts.list` → `[Draft]` (oldest first); `drafts.save {draft}` →
  `Draft` (creates or replaces; empty `id` gets one; `created` kept,
  `updated` set); `drafts.remove {id}` → `{}` (`not_found`). `Draft`:
  `{id: "d-…" (1–40 of [a-z0-9-]), text, machine?, machineExplicit?: bool, project?, profile?,
  worktree?: bool, branch?, attachments?: [path], after?: tile id ("^":
  the front), parked?: bool, created, updated}` (`pkg/wire/drafts.go`).
  Kept by the local daemon in `$HESPER_STATE_DIR/drafts.json` (0600,
  atomic; a broken file moves to `drafts.json.broken`), ≤ 200 drafts, text
  ≤ 256 KiB; the daemon never reads the text. `machine` is where the agent
  will run (the app shows the draft among that machine's tiles); a draft
  itself never travels. On `agents.subscribe` connections:
  `drafts.changed {draft}` (first one per draft) and `drafts.removed {id}`.
  Code: `relay/internal/agents/drafts.go` (server.go only routes).
- Attach frame **4 SCROLL** (view attaches): client→daemon JSON `{offset}`
  (lines above the live bottom; 0: live) or `{delta}` (> 0: back);
  daemon→client JSON `{offset, max, new}` whenever one changes while the
  window is up (and once on returning to live). While up, output that
  scrolls off adds to the offset, so the window stays on the same lines;
  `new` counts them. The alternate screen has no scrollback (`max` 0).
- Scrollback: `internal/vt` keeps the lines that leave the top of the
  main screen (a scroll whose region starts at row 0) as RowANSI strings,
  ≤ 10 000 lines / 4 MiB per agent; never on the alternate screen; ED 3
  clears it; ED 2 does not add to it; a reset keeps it. **Read-write
  attaches** get it before the redraw (printed from the top of a cleared
  screen, then one newline per line still on it), so the focus view's and
  the active tile's own terminal scroll back natively.
- `hesperd attach --view|--fit`: its terminal's input (otherwise dropped)
  may carry `S+N;` / `S-N;` / `S=N;` → SCROLL; the daemon's state comes
  back as the terminal's title `hesper-scroll {json}` (a view never shows
  the agent's title). The Part R bridge forwards SCROLL both ways unchanged
  (`TestRemoteViewScrollsThroughTheBridge`).
- `agents.answer` `trust`/`exit` (from the rebuild fixes) are shown as
  Trust (⏎) / Exit on the tile's band; first-run questions as "Open".

**New agent = a draft tile.** ⌘N inserts a draft right of the selected
tile (`after`), focused, blue ring, "New agent · draft · saved". The
editor is an NSTextView (every editing key, undo, IME) with inline
tokens: `@machine`, `#project` (recent projects, then folders under
`$HESPER_PROJECT_ROOT`/`~/projects`; a git URL offers "Clone … and start"
via `projects.clone`), `/profile` (a name, or a kind: `/codex` = the
kind's default), `~branch` (worktree on that branch). A token counts
only at a word start and only when it resolves (prose like `#123`,
`src/x`, `~/p` stays prose); resolved tokens draw as colored chips and
leave the task text (projects become their name). Completion popovers at
the caret for each kind (↑↓ ⏎/⇥ esc). The chip row shows the resolved
machine (round trip, offline warned), profile, project, worktree; a chip
opens its list (typing filters) and rewrites the token if there is one.
Defaults: no project (see "New drafts have no project" below), this Mac,
the project's last profile once chosen (else Settings › Profiles, else
hesperd's); worktree off with a
branch suggested from the first line (`Fix the flaky badge test` →
`fix/flaky-badge-test`), ⌘⇧W toggles. ⌘↩ starts: the spawn's agent takes
the draft's place (`placements`, remembered in UserDefaults; the agent's
tile is created in the draft tile's frame — no movement), the first line
names it, the draft leaves hesperd. ⌥↩ starts and opens the next draft
right of it. Esc parks the draft (quiet: the shelf in grid + shelf); an
empty draft is discarded. ⌘W discards with undo. Saving is continuous
(300 ms after typing stops, at least every 2 s; quit flushes ≤ 2 s);
local edits win over late echoes until saved. Files and screenshots
dropped or pasted on a draft insert their paths; images without a file
are saved as PNG in `~/Library/Application Support/Hesper/Attachments/
<draft id>/` (not in the project: nothing lands in a repository). Since
"drop to attach" (below) drafts read drops the same way as terminals
(file promises, HEIC → PNG, `<timestamp>-<name>.png`, backslash-escaped
paths), and a draft started on another machine uploads the files its
task names first (`files.put {machine, draft}`) and starts with their
paths there.

**New drafts have no project** (live-use fix; supersedes "⌘N's draft
takes the project of the selected card's band" below). ⌘N, the
toolbar's New Agent, the menu bar's "New Agent on the Wall", the
palette's "New agent" and quick launch open a draft with NO project:
the project chip says "Choose folder" (amber), Start is disabled, ⌘↩
opens the folder chip's list instead of starting (quick launch: the #
list). Machine is this Mac (an explicit machine from the palette's
machines stays), profile from Settings — never the selected card's or
its band's. On a wall that draws bands the draft sits in a **"New"
band** at the top (key `new`, neutral #565f89, no ＋ project / ↗,
subtitle "no project yet · choose a folder"; it never counts towards
"two or more bands", is always first and ignores the wall's own band
order); on a plain wall (one project, grouping None, D) it sits in the
wall order right of the selected card as before. Only explicit project
actions preset a project and open the draft in that project's band: a
band's ＋, the sidebar's new "New Agent in <project>" (first item of a
project row's menu), ⌘N while a band HEADER is focused (a header click
— which still collapses/expands — focuses it with a blue ring; any card
selection clears it; ⌘N re-expands the band), a project in the palette,
⌥↩'s next draft. Their machine and folder come as a pair
(`DraftSeed.place`): an explicit machine, else the project's default
machine, else this Mac, else the first machine with a folder — and the
folder on THAT machine.

*The "no directory" warning* was hesperd's spawn error (`no directory
<path>`, `agents.Registry.Spawn`): the draft inherited the selected
card's machine (or the band project's default machine, or another Mac's
folder via `Project.path(on:)`'s any-machine fallback) while the #
list, the chip and recent projects offer this Mac's folders, so a
changed folder was started on the other machine (or this Mac got the
other Mac's path); a chip choice also rewrote `#<basename>`, which
could resolve to the first entry of that name instead of the chosen
folder. Now: changing the folder (#token, chip, the new "Choose
folder…" picker, a typed path in the chip — `~/x` or `/x`, existing or
not —, recent, clone) sets the draft's `project` silently and, unless
the user chose the machine (chip, palette, @token), the machine goes
back to this Mac (`DraftSeed.folderChanged`); a #name token naming the
draft's chosen folder resolves to that folder. A folder on this Mac
that doesn't exist shows "Folder doesn't exist — Create" in the chip row
(Create: mkdir -p now; otherwise on start); no modal, no toast. On start
the agent moves once (the wall's frame animation) into its project's
band. Drafts keep `project` (and `machine`) empty in hesperd until
chosen; drafts restored after a restart land in the New area (the band a
draft opened in is per session). A new draft is scrolled into view once
laid out. Code: `HesperCore/NewDrafts.swift`, `ViewResolver.newKey`,
`AppModel+Drafts.newDraft`, `ComposerModel.setFolder/missingFolder`.
Tests: HesperCore `NewDrafts` (9: New band first/neutral/never in a
project band, plain wall with one project, band drafts, place, folder
change + pinned machine, token names the chosen folder, missing folder
only locally, typed paths, no project saved); UI: test-ui (⌘N draft
without project, saved without, ⌘↩ opens the folder list, chosen
folder + missing-folder note, created on start in place; restart: the
kept draft back without project in the New area, folder chosen, starts
in place), test-ui-projects (⌘N in the New band at the top, ⌘↩ without
folder, folder change silent, start moves into its band, toolbar,
header-focus ⌘N, sidebar "New Agent in"). Screenshots
(`app/build/shots/new-drafts/`): new-draft, band-plus-draft,
folder-change, draft-missing-folder. Totals after this change: unit 163,
test-ui 73 + 6, test-ui-real 70 + 6, test-ui-windows 33 + 7,
test-ui-projects 42, test-ui-desks 41 + 3.

**Machine and folder agree** (live-use fix after "New drafts have no
project": a draft showed `acme-api ~/projects` and
"no directory /Users/…/acme-api" — the folder was on
the laptop, the draft targeted the mini, whose chip had scrolled off the
row's left edge, showing only "41 ms · relay"). Causes: (1) drafts kept a
`machine` without knowing whether the user chose it, so the "folder
decides the machine unless chosen" rule (a per-session flag) didn't
apply to restored or older drafts; (2) a `#token` updated the project
only in some edits (not on restore, not when the lists loaded later),
so the stored project could disagree with the token; (3) nothing
checked the folder on the target machine before the spawn; (4) the chip
row was one fixed-size line that overflowed centered. Now:
- **Wire (additive):** `Draft.machineExplicit?: bool` — set only when
  the user picks the machine (machine chip, the palette's machines, an
  `@machine` token, "Use mini's copy"); "Run on laptop" and removing the
  @token clear it. Migration: absent = not explicit (the machine
  follows the folder). `fs.stat {machine?, path}` → `{exists, isDir}` on
  the local socket (clean absolute path, else `invalid`); another
  machine's through the gateway like `agents.*` (host method, right
  `observe`, idempotent on the direct path; unknown/offline machine:
  `unavailable`). Code: `internal/agents/fsstat.go`, `internal/host/
  agents.go`, `pkg/wire/fs.go`, `pkg/devicekey/rights.go`.
- **Tokens decide** (`DraftSync.sync`, on every edit and for every
  draft from hesperd — list, changed, and again when the # lists or the
  catalog load): the last resolved `#token` is the project (a token
  naming the chosen folder keeps it), an edit that removes the last one
  clears it ("Choose folder"); an unresolved token leaves a restored
  draft alone. An `@token` is an explicit machine.
- **The machine follows the folder** unless explicit
  (`DraftSync.followFolder`): kept where the catalog has exactly that
  folder on it (a band's ＋ on the mini); else this Mac when the folder is
  here (or there is no folder); a folder only one other machine has
  (catalog) takes the draft there. Normalized drafts are saved back
  (the user's old drafts are fixed on install, nothing edits them by
  hand).
- **Checked before start and live:** the chip row asks the target
  machine (`fs.stat`; a daemon without it: the catalog's paths) whenever
  machine or folder change; ⌘↩ (and quick launch) asks again and starts
  nothing when it's missing. Note in the chip row: "This folder is on
  laptop, not on mini —" **Run on laptop** (one click: this Mac,
  machineExplicit cleared, an @token removed) and **Use mini's copy**
  when the project has a path there (`DraftSync.copy`); neither here nor
  there: "Folder isn't on mini". hesperd's raw `no directory <path>`
  is never shown: a spawn error with it maps to the same note (or, on
  this Mac, "Folder doesn't exist — Create").
- **Chip row:** the machine chip is first and says the machine's name
  ("mini · 41 ms · relay", "laptop · this Mac"; a host name falls back to
  the short name); the row wraps (`ChipFlow`, up to three lines, the
  tile measures it) instead of overflowing — nothing scrolls off.
Code: `HesperCore/NewDrafts.swift` (`DraftSync`, `FolderNote`),
`ComposerModel.checkTarget/folderNote/runHere/useTargetCopy/spawnFailed`,
`AppModel+Drafts.syncDraft/normalizeDrafts/folderExists`,
`UI/Composer.swift` (`ChipFlow`). Fake hesperd: `fs.stat`; its mini
lacks folders named `laptop-only` (spawn there: `no directory`).
`run-with-fake.sh selftest` sets `HESPER_PROJECT_ROOT` to the run's dir.
Tests: HesperCore `DraftSyncs` (9: the three live drafts — old mini
draft → laptop, explicit mini kept, token/project mismatch fixed on
restore —, last token wins/removal clears, @token explicit until
removed, the note and its copy, "no directory" mapping, machine chip
label); Go `TestFSStatLocal`, `TestDraftMachineExplicitKept`,
`TestRemoteFSStat` (observer through the in-process relay, path never
in plaintext; unapproved refused); UI test-ui (+8: the three drafts saved
as an older app left them, token removal, ⌘↩ with the folder not on the
mini starts nothing without raw error, narrow tile wraps with the machine
chip visible, Run on laptop; restore: a token naming a folder the last
process didn't list fixes the project), test-ui-real (+3 + 1). Shots:
draft-folder-elsewhere, draft-chips-narrow. Totals: unit 172, test-ui
81 + 7, test-ui-real 73 + 7, test-ui-windows 33 + 7, test-ui-projects 42,
test-ui-desks 41 + 3.

**Quick launch:** ⌃⌥Space (Settings › Quick Launch, recorder; Carbon
`RegisterEventHotKey`, no accessibility permission; never registered by
automated runs or `--no-hotkey`) or the menu bar item opens the same
composer in a non-activating floating panel over any app (completion
inline in the panel; chips insert their sigil). ⌘↩ starts (the agent
joins the wall at its natural place), clears the text, keeps the
choices, and posts a notification that selects it on the wall; esc
closes and keeps the text.

**One overlay family** (`UI/Overlays.swift`): raised #1f2335, 1 px
#3b4261 border, 12–14 pt radius, soft shadow, open 120 ms (scale 0.98 →
1 + fade), close 80 ms, Reduce Motion: fades. Popovers attach to their
thing with an arrow (`PopoverPlacement`: below, else above, sideways
while the arrow still reaches; never over a card that needs the user when
another spot is free). Keys everywhere (`OverlayKeys`): ↑↓ select, ⏎ do,
⌘⏎ the alternative, ⇥ act on, esc close, typing filters; each overlay's
footer lists its keys. State survives close (palette query, deny text,
composer). Inventory: ⌘K palette (the only centered one; light scrim with
holes so attention rings stay visible; agents, drafts, actions — an agent
named in the query brings its actions — projects, machines, layouts,
settings; ⏎ go/do, ⌘⏎ open full size, ⇥ that agent's actions); ⌘⇧M move
popover from the tile (machines with route and round trip, offline
dimmed, "with conversation + uncommitted work"; the tile says "moving to
mini…" in place; undo moves it back); rename popover; the toolbar's
machines pill (online, route, RTT, agents, pairing hint), attention pill
(the ⌘J queue: ⏎ go, ⌘⏎ allow) and layout button (four layouts + min
width; ⌥⌘1–4 stay). Deny… expands the band into a one-line message field
in place. ⌘W stops at once / removes at once (committed after 6 s) with
an undo toast (⌘Z, its button); stop undoes by resuming; only removing
an agent whose local worktree has uncommitted changes (`git status
--porcelain`) asks, inline on the tile (⏎ remove anyway, esc keep).
Settings ⌘, is a window (General: layout, tile font, min width; Quick
Launch; Notifications; Profiles per kind). Focus ⌘↩ zooms the card to the
window and back (200 ms; Reduce Motion: none).

**One tile font; the PTY follows the tile.** Every tile uses the same
font (Settings › Tile font size, default the user's Ghostty font, 9–16
pt) and shows as many cols × rows as fill its terminal area; tiles attach
with `--fit`, so with no owner the PTY takes the largest tile's grid (≥
80 × 24, hesperd's rule). A focused agent's PTY returns to its tile's
grid when focus closes (checked end to end). Min card width = N columns at
the tile font. The per-tile font search is gone (a font change still
re-attaches, now only when the setting changes).

**A calm footer.** Every live card (and the focus view) reserves one
footer line: activity while working (`agent.activity`), the summary when
done/idle, the approval/question/error band. Calm changes show after 400
ms of quiet (at most 1 s), attention states and leaving them at once,
with a 150 ms crossfade. A two-row band (narrow approval) covers the
terminal's bottom rows instead of shrinking it — calmer than reserving
two rows on every card (`build/shots/flap-*.png`: 29 changes in 3 s, 1
terminal frame). The shelf/treemap follow the shown (debounced) state.

**Typing on the wall (the active tile).** A click on the selected tile,
⏎ on it, typing a character on it, or ⌘J landing on it makes it active
(a plain click only selects: see "As built — keyboard navigation"): cyan ring, "typing · ⌘esc
leaves"; every key goes to the agent (Esc, ⏎, arrows, ⌃-keys, IME,
⌘C/⌘V, selection) except the app's ⌘ shortcuts (⌘N ⌘K ⌘J ⌘W ⌘↩ ⌘[ ⌘] ⌥⌘← ⌥⌘→ ⌘,
⌘⇧M ⌘Z ⌥⌘…). ⌘esc (Claude/Codex never see ⌘esc), a click on the wall or
another tile leaves (back to selected); one per window; double click still opens focus. On
a selected but not active tile ⏎/A/N still answer approvals. The active
tile's surface is a read-write **owner** attach at its own grid in the
low-latency engine app (owner, not a raw rw at the fit size: the fit is ≥
80 × 24 and a raw stream of a taller PTY cannot render in a shorter
surface); the new surface starts underneath the view and replaces it once
it has drawn (≤ 800 ms), and leaving swaps back the same way: no gap, no
layout change. Writers follow the daemon's rules (the latest owner sizes
the PTY; several rw attaches may type). Measured: keystroke → frame p50
2.4 ms (grid 0.4 ms) in an active tile; 16 flood tiles with one active:
120 fps, 0 dropped.

**Scrolling back.** The wheel/trackpad over a read-only tile moves its
window into the scrollback (precise deltas / cell height, fractions
accumulate, momentum included; at the live bottom or sideways the wall
scrolls). A scrolled tile keeps its lines while the agent goes on; a pill
"↓ N new lines ↑offset" takes it back (click, End, or scrolling down).
Focus and the active tile scroll natively.

**Tests:** HesperCore 67 (token parsing/resolution/completion, branch
suggestion, draft JSON, coalesced saving, local-wins, wall order anchors,
undo stack, overlay keys and list, popover placement, compose and
active-tile routes, uniform tile font). UI smoke (`make -C app test-ui`:
50 + 4 after an app restart; `test-ui-real`: 48 + 4 after an app and
daemon restart): draft → ⌘↩ keeps the tile's slot and frame, esc keeps a
draft on the shelf, empty + esc discards, the kept draft is back after
the restart and starts in place, ⌘W stops at once and ⌘Z resumes, move
popover → moving… → ⌘Z back (fake mini), trust ⏎, state changes never
move the terminal, active tile (swap without gap, typing, ⌘esc, rapid
switching leaves one rw surface, a remote agent's tile), after focus the
PTY returns to the tile's grid, one font on every tile, scrolling a tile
(real daemon: stays put under output, pill counts, End). Go:
`internal/agents` drafts (save/list/remove, restart, subscribe, broken
file), `internal/vt` history, `internal/ptyhost` scroll window / stable
offset / rw preamble / alt screen, `internal/attachtty` scroll parser,
`internal/transport` remote scroll through the bridge.
`make -C app layout SHOW=draft|draft-typing|palette|move|attention|
machines|layout|undo|deny|quick|settings|flap|after-focus|active|scroll
[DAEMON=real]` screenshots each (quick/settings: the panel/window too).

#### As built — windows (multi-window iteration 1)

Code: the window
layer in `app/Sources/Hesper/Windows/` (WindowManager, AgentWindow,
WallWindow, WindowRestorer, WindowParts: ↗ marker, scope pill,
SizeOwnership) and the pure rules in `HesperCore/WindowModel.swift`
(scopes and their distribution, wall capacity, routing, one window per
agent, restoration format, frame clamping).

**One model per window, one connection.** Every window (each wall, each
agent window) has its own `AppModel` (mode, selection, active tile,
layout, card width, overlays). The main model owns the one `DaemonClient`
(one control connection, one `agents.subscribe`); the others are created
with `AppModel(env:sharing:)`, share that client and get every daemon
event after the main model applied it (`onEvent` → `ingest`). Tiles are
one read-only view attach per agent per wall window (nothing is shared
below that: each window needs its own surface).

**Clicks and keys.** A plain click selects the tile, a second click
activates it (typing into it; "As built — keyboard navigation"),
⌘↩ / double-click open the focus view in the same window (as before);
only **⇧-click or ⌘-click** opens the agent in its own window (⌘-click is
not reserved for multi-select), ⌥⌘↩ on the selected tile and the tile's
context menu (⌃-click / right-click: Open in New Window, Open in New Tab,
Move to… (the move popover on the tile), Stop / Resume / Remove (the
undoable ones), Rename… (the rename popover)) do the same. ⌥⇧-click opens
it as a tab of the frontmost agent window. An agent that already has a
window gets that window brought forward, never a second one. ⌥⌘N opens a
wall window (⌘N stays "new agent"); ⌃⌘S (Window menu) opens the wall's
scope popover.

**Agent window.** The content is the app's RootView held in focus mode on
the window's model (its wall scope is empty, so no tiles behind it): the
same focus view, terminal, attention band, popovers, palette, deny field
and undo toasts as ⌘↩. Title = the agent's name (also the tab title),
subtitle `machine · kind` plus "needs approval" / "asks a question" /
"error" / "exited"; the title bar is tinted rose / amber / red while the
agent needs you. Keys as in focus, except ⌘W closes the window (the
agent keeps running; Agents ▸ Stop Agent… still stops it), ⌥⌘← ⌥⌘→
(⌘[ ⌘]) step to the previous / next agent within the window (tabs, or
retargeting: "As built — keyboard navigation"), ⌘↩ / ⌘Esc show the agent in the frontmost
wall that shows it, ⌘N starts a new agent on the frontmost wall, ⌘K
picking another agent opens that agent's window. `tabbingIdentifier
"hesper.agent"`, `tabbingMode .automatic`: Window ▸ Merge All Windows,
Move Tab to New Window and dragging tabs out are AppKit's; full screen
(`.fullScreenPrimary`), the Window menu lists agent windows by name, ⌘`
cycles. Wall windows don't tab. The tile of an agent with a window shows a
↗ badge on its top-right corner. Removing the agent closes its window.

**Size rule with several windows** (`SizeOwnership`, pure rule
`HesperCore/SizeOwner.swift`): among the read-write candidates for an
agent (an agent window, a wall's focus view, the active tile) only the one
in the app's **key window** attaches as owner, and at most one per agent
(a focus view before a tile); every other terminal of that agent is a
**view** (`attach --fit`), and with no owner the daemon follows the views'
fit (largest pane). Never rw without `owner`: that attach gets the PTY's
raw stream at the PTY's grid, but libghostty sizes a surface's grid from
its frame (the bridge's TIOCSWINSZ on its pty changes nothing there), so in
a pane of another size the agent's TUI wrapped and its cursor-addressed
redraws landed in the wrong cells (garbled panes in windows that weren't
key). A view is rendered by the daemon from its screen copy at the pane's
own grid: clipped, never reflowed. A terminal asks SizeOwnership whenever
it makes a surface, so a new one in a window that isn't key never attaches
as a second owner; the trade-off: a pane in a window that isn't key can't
type until its window is key and the owner swap is done (≤ 250 ms + the
swap). Applied 250 ms after key changes
settle, through AgentTerminal's seamless swap (the new attach draws under
the old surface, then replaces it), so flipping between windows doesn't
ping-pong the PTY (test: 8 key flips in 400 ms → one size change, two
re-attaches). Deviation: the app going to the background keeps the owner
(otherwise every app switch would resize every open agent twice); only
another Hesper window becoming key, or the owner closing, releases it.

**Walls and scopes.** Each wall has its own layout, card width and
selection, and a scope (toolbar pill "All · 12" / "All · 8 of 12" /
"Overflow · 4" / "Filter · 3"; its popover is the overlay system's
`PopoverKind.scope` attached to the pill: All, Overflow, Filter and the
filter's Project / Machine / Kind / State rows, ⏎ chooses, ⌘⏎ keeps it
open):

- All: everyone, in this wall's layout (a mirror).
- Overflow: the overflow chain is the first wall (when its scope is All)
  followed by every Overflow wall in window order. Every wall of the chain
  but the last takes as many agents as fit without scrolling at its
  minimum card size (`WallCapacity`: whole minimum cards in the visible
  area; columns: one row); the last takes the rest. Agents in approval /
  question / error always go to the head of the chain, even past its
  capacity, pushing working agents on. Order inside a wall is the wall
  order. Without Overflow walls the first wall shows everyone.
- Filter: project, machine, kind, state (needs you / working / done-idle /
  exited); dimensions AND, values OR.

A new wall starts as Overflow when the first wall can't fit everyone at
its minimum card width, else All. Drafts show in the window they were
made in (superseded "Drafts stay on the first wall": see "As built —
new agents per window"). Capacity
follows window resizes and layout / card-width changes (debounced 150 ms).

**Routing** (`WindowRouting`): ⌘J, notification clicks and the menu bar
item go to the agent's own window if open, else the frontmost wall
showing it (⌘J activates its tile there; a notification opens its focus
view), else the main wall (focus view). Rings show on every tile of the
agent in every wall; agent windows tint their title bar. The Dock badge
(number needing you) and the menu bar counts are app-wide. Menus act on
the key window's model (layouts on the frontmost wall).

**Restoration** (`WindowRestorer`, explicit file, not NSWindowRestoration):
`--window-state PATH`, else, only when talking to the live daemon (no
`--socket` / `--state-dir` / `$HESPER_SOCKET`, not automated),
`~/Library/Application Support/Hesper/windows.json`; written 0.5 s after
any window change and on quit. Every wall (id, scope, layout, card width,
frame, display id, full screen) reopens at launch; agent windows (agent,
frame, display, tab group and selected tab, full screen) once the daemon
listed its agents: removed agents' windows don't come back, and until the
list arrives the saved ones are kept in the file. Frames are clamped to
the screens there are now (the saved display if attached, else the one the
frame overlaps most, else the main one; shrunk to the visible area and
moved fully onto it). Spaces can't be restored with public API.
(Superseded by desks: the file is desks.json, one arrangement per
display setup; windows.json is migrated — "As built — desks".)

**Integration points in shared files** (all marked "window layer"):
`AppModel` (`init(env:sharing:)`, `onEvent`/`ingest`, `wallScope`,
`showsDrafts`, `scopeContent`, `routeAttention`, `tileClick`, `tileMenu`,
`hasAgentWindow`, `wallItems` filtered by scope over `unscopedWallItems`,
⌘J consults `routeAttention`, mirrors don't refresh lists),
`AppModel+Overlays` (`PopoverKind.scope`), `Overlays` (its width),
`MainWindow` (not `final`; `.scope` anchors on the toolbar), `Toolbar`
(`extraItemIDs`, `makeExtraItem`, `extraAnchor`), `WallView`/`TileView`
(`tileClick` first in `mouseDown`, `menu(for:)`, the ↗ marker),
`FocusView` (`ownsSizeDefault`; "n of m" hidden when there is no wall
order), `AgentTerminal` (`ownsSize`, used for rw attaches; the swap keeps
a focus terminal's input), `StatusItem` (`openAgent`, `lookingAt`),
`AppDelegate` (creates the WindowManager, menus act on the active model,
restoration at launch and quit), `AppEnvironment` (new valued flags).

**Tests.** HesperCore (`WindowModelTests`): overflow distribution incl.
attention past capacity, mirrors and filters outside the chain, chain
without an All head, combined filters, default scope, capacity, routing
order, one window per agent, restoration round trip / bad versions /
removed agents, frame clamping (display gone, unknown display, off
screen, tiny). UI (`make -C app test-ui-windows`, part of `make test`;
`Tools/run-windows.sh selftest`, fake daemon, temp state; `SHOTS=dir`
for screenshots): plain click / ⌘↩ / double-click open no window, ⇧-click
opens one and a second ⇧-click brings the same one forward, ⌘-click opens,
⌥⇧-click tabs onto the frontmost agent window, typing and size ownership in
the agent window, title / subtitle / ↗ marker, no ping-pong, key agent
window beats another window's active tile and the tile owns again when its
wall is key, ⌥⌘N → Overflow with the right split, the second wall renders,
one control connection, an agent needing you moves to the first wall with
its ring, ⌘J to the agent's own window with the tinted title bar, ⌃⌘S
popover anchored on the pill, closing the window leaves the agent running,
state saved; then a relaunch on the same daemon and file: agent windows
and the tab group back, a removed agent's window not, the second wall with
its scope, frames on screen, terminals attached (28 + 7 checks).

**Measured** (`make -C app perf-walls`: 16 calm agents on 1 wall, then the
same on 3 All walls side by side on the 120 Hz built-in display, M1 Max
shared with other builds, load average ≈ 30): 1 wall: 119.9 fps, 1
dropped frame in 10 s, CPU 15 %, 283 MB; 3 walls × 16 = 48 live tiles:
119.6 fps, 4 dropped (one 40 ms hitch), every tile 11 content-fps (all
the fake agents print), CPU 44 %, 778 MB, 48 login + 48 attach helpers,
one control connection, all 32 new tiles live 2.0 s after opening the
walls.

#### As built — keyboard navigation

Request: "use the arrow keys to move through the agents on the wall, and
left and right in single view to move to the next". Code: the pure rules
in `HesperCore/WallNavigation.swift` (`WallMove`, `WallNavigation`,
`TerminalKeys`) and `KeyRouter`; `AppModel` (`moveSelection`, `step`,
`typeIntoSelected`, `sendInput`, `mainPick`), `WallView`/`TileView`
(clicks, keyDown, footer hints), `AgentWindow.step` +
`WindowManager.step` (`AgentWindowBook.stepTarget` / `retarget`), View
menu in `AppDelegate`.

**Two levels of focus on a wall tile.**

- **Selected**: highlight ring (blue, 2 pt; a tile that needs you keeps
  its rose / amber / red ring at 3 pt), no cursor (read-only tiles live
  in the tile engine app, whose config has `cursor-opacity = 0`; the
  active tile and focus views are in the read-write app and keep theirs),
  read-only view attach; keys go to the wall. Its footer shows
  `←→↑↓ move · ⏎ type · ⇧-click window` (narrow: `←→↑↓ · ⏎ type`; shelf
  card: `⏎ open`); the band's buttons show their keys (⏎ A N, ⏎ for a
  question's first answer) only on the selected, not active tile, and
  never in focus (there the keys are the agent's).
- **Active** (typing, existing): cyan ring 2.5 pt, header chip
  "typing · ⌘esc leaves", the cursor, read-write owner attach; every key
  but the app's ⌘ chords goes to the agent.
- Plain click → selected. A click on the selected tile, ⏎, or typing any
  printable character (⌥-characters too: ⌥L is @ on German layouts) →
  active; the typed text goes to the agent at once through
  `agents.input`, and until the read-write terminal has swapped in
  (≤ 800 ms) the wall forwards every further key (text, ⏎, ⌫, esc, ⇥,
  arrows, ⌃-letters) the same way, in order (one call at a time,
  coalesced), so "just start typing" loses nothing. ⌘esc → back to
  selected (the wall has the keys again). ⇧/⌘-click (own window),
  ⌥⇧-click (tab), ⌃-click (menu), double-click (focus), ⌘J (activates)
  are unchanged. A shelf card has no terminal: activating it opens the
  focus view. A draft: click selects, a click on the selected draft, ⏎ or
  typing opens its editor (typed text goes into it).
- **Precedence on the selected tile**: draft → edit; approval → ⏎ Allow,
  A Always, N Deny…; a question with answers (trust, update) → ⏎ the first
  answer; otherwise ⏎ / a character → active. Other characters on an
  approval tile type into it (Claude's own 1/2/3 still work).

**Moving the selection** (selected, not active): ← → ↑ ↓ go to the
spatial neighbor, Home / End to the first / last card, Tab / ⇧Tab the
next / previous in wall order. Rule (every arrangement, cards and drafts
alike, `WallNavigation`): candidates lie wholly on that side (1 pt
tolerance); the nearest by `gap along + 2 × gap across` (0 across when
the cards share a row / column band) wins, ties by the distance of the
centres across — so a partial (stretched) last row, a shelf card under two
grid cards, the stack next to main + stack's main card, and treemap rects
all go to the card most in line. Wrap: ← / → with nothing on that side
continue in reading order (top edge, then left edge: the end of a row goes
to the next row's first card, like text), stopping at the first / last
card; ↑ / ↓ with nothing there stay. Grid → shelf and back is just "down"
/ "up". Columns scroll the selection into view (as before). Moving never
attaches read-write and never resizes: main + stack's main card follows
clicks, activation and focus (`mainPick`), not the arrows (test: sizes
and frames identical before and after).

**Single view and agent windows.** Plain ← → (and every other plain key)
stay the terminal's: Claude and Codex need them. ⌥⌘← / ⌥⌘→ = previous /
next agent (like a browser's tabs), ⌘[ ⌘] kept as aliases; in the wall's
focus view in wall order (wrapping); on the wall they move the selection
in wall order (and keep typing when a tile was active: the next one
becomes active). In an **agent window** they stay within that window:
tabbed → the previous / next tab (wrapping, AppKit's; each tab keeps its
agent); untabbed → the window is retargeted to the previous / next agent
in wall order that has **no window of its own** (one window per agent:
the book moves the window to the new agent; title, tint, ↗ markers,
restoration and size ownership follow; a beep when every other agent has
a window). Retargeting was chosen over "do nothing" for an untabbed window
because the request is to move to the next agent in the single view; the
skip keeps ⌥⌘→ from ever closing or duplicating another window.

**Discoverability.** Tile footer hints (above), focus / agent window
header `⌥⌘← →  next agent · ⌘↩ wall`, View ▸ Previous Agent ⌥⌘← / Next
Agent ⌥⌘→ (⌘[ ⌘] as hidden aliases; moved from the Agents menu; in an
agent window they act on its tabs / agent).

**Tests.** HesperCore (`WallNavigationTests`, `CoreTests`): grid rows
wrap like text, partial row, shelf ↔ grid, main + stack, unplaced cards,
every arrangement × sizes × counts (targets on the right side or the
reading-order wrap; every card reachable), shelf reading order with →,
scrolled columns, treemap adjacency, real main + stack; window step
skips agents with windows and wraps, retarget refuses a taken agent; key
table (selection, Home/End, typing incl. ⌥-characters and drafts,
approval precedence, ⌘esc on the wall, ⌥⌘← → everywhere, plain ← → in
focus pass through, terminal key bytes). UI (`make -C app test-ui`, 62 +
4; `test-ui-real` 60 + 4): click selects (no rw), → ← ↓ ↑ Home End move
by the layout, nothing rw and no size or frame change, typing "zeta" on a
selected tile activates it and every character arrives, ⌘esc back to
selected, a click on the selected tile activates, ⏎ on an approval tile
allows without activating, plain → in focus reaches the agent (the fake
TUI shows → as ›) while ⌥⌘→ / ⌥⌘← step. Windows (`test-ui-windows`, 32 +
7): ⌥⌘→ ⌥⌘← select tabs in a tab group; in an untabbed window ⌥⌘→ shows
the next agent without a window (title, terminal, ↗ marker moved) and
⌥⌘← brings it back. Screenshots: `make -C app test-ui SHOTS=dir`
(`tile-selected.png`, `tile-active.png`), `make -C app layout
SHOW=selected|selected-approval FAKE_ARGS=--demo-states SHOT=…`.
`make perf` after (16 flood tiles, 60 Hz external display this run):
0 dropped, keystroke → frame p50 3.5 ms (focus) / 2.1 ms (active tile),
state → ring p50 6.5 ms, 323 MB — unchanged.

#### As built — drop to attach

Request: "We are unable to drag and drop files into the terminals. Make
sure we can add attachments to Claude and Codex by dragging and dropping
them into the pane." Code: `HesperCore/Attachments.swift` (pure rules:
`DropClassifier`, `ShellEscape`, `DropPlan`, `AttachmentNaming`,
`AttachmentUpload`; `DaemonClient.filesPut/filesChunk`),
`UI/TerminalDrop.swift` (`TerminalDrop`, `DropOverlay`, `DropReader`,
`AttachmentStore`), `App/AppModel+Attachments.swift` (`deliverDrop`,
uploads, drafts), `TileView`/`FocusView` (drop targets),
`GhosttySurfaceView` (forwards drag events to the first ancestor that
takes drops, so the drop works whichever view AppKit picks).

**Where.** Every terminal surface: wall tiles in every state (selected,
active, read-only, shelf cards), the focus view, agent windows (their
focus view), in every arrangement and wall window; draft tiles as
before (now the same reading). A drop on the wall's background does
nothing (decided against creating a draft: a missed target should not
make things). Exited agents refuse ("<name> has exited").

**Payloads** (`DropClassifier`, first match): file URLs (Finder; several
files; folders) → their paths, as they are (local agents: no copy);
file promises (screenshot thumbnail, Photos, Mail; `NSFilePromiseReceiver`)
→ received, moved to the attachments folder; image bytes (browsers,
Preview: PNG/TIFF/HEIC/JPEG data, else `NSImage`) → saved as PNG; a web
URL or text → pasted as text. Unsupported types: no drop, a red ring
and "Only files, images and text can be dropped". Saved payloads go to
`~/Library/Application Support/Hesper/Attachments/<agent id, "/"→"-">/
<yyyyMMdd-HHmmss>-<name>.<ext>` (png for images; `-2`, `-3` when taken;
automated runs: the temp state dir), never into a repository. Image
files the agents can't read (HEIC, HEIF, TIFF, GIF, WebP, BMP) get a PNG
copy there and that path is pasted.

**Delivery.** The agent first gets the keyboard: on the wall its tile
becomes active (a shelf card opens the focus view), the window comes
forward and the app activates; the focus view already showing it keeps
it. Then the paths go in through `agents.input {paste: true, submit:
false}` (hesperd wraps them in bracketed paste) — never submitted, in
order with typed keys (the app's one input queue). Found with the real
CLIs (Claude Code 2.1.292, Codex 0.160.1, in `~/scratch/hesper-drop-test`
under a private tmux server, nothing submitted; Codex trusted via `-c`):

| Pasted (bracketed) | Claude 2.1.292 | Codex 0.160.1 |
|---|---|---|
| PNG / JPEG path, plain, backslash-escaped, '…' or "…" | `[Image #N]` | `[Image #N]` |
| two image paths in one paste | `[Image #5] [Image #6]` | raw text, no images |
| two images, two pastes | — | `[Image #1] [Image #2]` |
| text file / folder (escaped) | the path as text | the path as text |
| `@` + path | plain text, no mention | plain text, no mention |
| HEIC path | raw path | raw path |
| image + text file in one paste | `[Image #9]/Users/…` (space lost) | all raw text |
| PNG path typed (not bracketed) | raw path | `[Image #1]` |

So (`DropPlan`, the same for both kinds): one paste per image (PNG/JPEG,
images first), then the other paths in one paste, then dropped text;
every paste after the first starts with a space (both CLIs add one after
`[Image #N]` and swallow a trailing one), none ends with one; 150 ms
between pastes. Paths are backslash-escaped like Ghostty's own drop
(`\ ` and `\`()[]{}<>"'!#$&;|*?` tab and backslash); unicode stays as
it is; a path with a newline or another control character is ANSI-C
quoted (`$'a\nb'`). `@path` was not used (no effect in either).

**Remote agents.** A local path means nothing on the mini, so files go
there first: `files.put` / `files.chunk` (wire below), 512 KiB chunks,
one call at a time, then the path *there* is pasted. ≤ 50 MB per file
and no folders (refused with a toast; the rest still goes). The drop
label says "uploads to mini"; from 1 MB the tile (or focus view) shows a
footer pill "uploading to mini… 3.1 MB of 12.0 MB" until the paste.

**Wire: `files.put` / `files.chunk`** (Part D/R; `internal/agents/
files.go`, `internal/host/files.go`, `internal/remote/files.go`):
`files.put {agent | machine?+draft, name, size, sha256}` → `{upload:
"at-<16 hex>", chunk: 524288}`, then `files.chunk {upload, offset, data
(base64), last}` in order. Every chunk but the last is exactly 512 KiB;
a wrong offset is `invalid` ("expected offset N"); a non-last chunk
answers `{received}`, the last `{path, size}` after checking the size and
SHA-256 ("checksum mismatch": discarded). Limits: 50 MB per file ("<name>
is larger than 50 MB"), 8 uploads in progress per daemon
(`unavailable`), dropped after 10 min without a chunk. Names are
sanitized (base name, no control characters, a leading dot becomes `_`,
1–120 bytes). Files land in `$HESPER_STATE_DIR/attachments/<local agent
id | draft id>/<name>` (0700/0600, temp file then a hard link to a free
name `name-2.ext` …) — default the state dir, so nothing lands in a
repository; settings.json `"attachments": "project"` stores an agent's
files in `<worktree | project>/.hesper/attachments/` instead (drafts
always in the state dir). For an agent or draft on another machine the
local daemon forwards: it picks the upload id, seals each chunk with
`pkg/transfer` for the host's published transfer key (bound to upload
id, file name, chunk index and last flag), and sends the host's
`files.put {agent | draft, upload, name, size, sha256, epk}` /
`files.chunk` as signed requests over the end-to-end channel; the path
returned is the host's. Rights: both need `type` (as `agents.input`;
shells need the shell right and are otherwise invisible). Each upload
writes one audit line when it ends (device, agent, size, ok; never the
name or content); chunks are not audited one by one. Without a transfer
key the host answers `unavailable`. The relay sees neither content,
names nor the methods.

**Tests.** HesperCore (`AttachmentTests`): escaping (spaces, shell
characters, quotes, backslash, unicode, newline/tab/control → `$'…'`),
paste plan (images one per paste and first, separators, conversion
list), classification (files > promises > image bytes > URL > text,
unsupported), names (timestamp, sanitizing, unique, folders), chunking
and limits. UI (`make -C app test-ui`, 68 + 4; `test-ui-real`, 65 + 4;
drops through `FakeDrag`, an `NSDraggingInfo` delivered to the view the
hit test picks, walking up to the first registered view as AppKit does):
an unsupported payload is refused with its message; a file with spaces
and parentheses dropped on a read-only tile highlights "Drop to attach to
<name>", makes the tile active and arrives as one bracketed paste with
the escaped path (the fake TUI turns on bracketed paste and shows a
paste as `⟦…⟧`); PNG bytes dropped on the focus view (through the
surface's forwarding) are saved as `<timestamp>-image.png` in the agent's
folder and that path pasted; a file dropped on the fake mini's agent is
uploaded (`files.put`, the fake stores it under `remote-M/`) and the
remote path pasted. Windows (`test-ui-windows`, 33 + 7): a file dropped
on an agent window arrives as a paste. Go: `internal/agents`
`files_test.go` (chunks, cap, sha, order, names, unique, draft, project
setting, limit, expiry), `internal/host` `files_test.go` (sealed round
trip + audit without name/content, shells, observer `forbidden`),
`internal/transport` `remote_files_test.go` (L → M through the
in-process relay: identical bytes under M's `attachments/<id>/`, draft
form, checksum mismatch, > 50 MB, observer `forbidden`, the tap never
sees the marker, the names or a `files.` method).

**Not done:** dragging out of a terminal; folders to remote agents (no
archive); real (WindowServer) drags are not automated — `FakeDrag`
reproduces AppKit's destination choice.

#### As built — projects (views; workspace model step 2)

Design: the workspace model design notes (iteration 2
incl. "Reality check"); data: "As built — projects (data)" below. Code:
pure rules in `HesperCore/Projects.swift` (wire types, `ProjectCatalog`),
`ViewModel.swift` (grouping, bands, hysteresis, band navigation,
project-first stepping, sidebar, home wall), `BandLayout.swift` (grouped
layouts), `WindowModel.swift` (scopes, Overflow by group, saved view);
AppKit in `App/AppModel+Projects.swift`, `UI/Bands.swift` (band frames,
headers, needs-you strip), `UI/ProjectSidebar.swift`, `WallView`
(bands, strip), `WindowManager` (distribution, home wall, sidebar
actions), `AgentWindow` (project title, tint, tabs), `WindowRestorer`.

**A wall is a view: scope + grouping + layout.** The scope moved into
the wall's `AppModel` (`WallEntry.scope` passes through). Scopes: All,
**Group** (its projects, packages included), **Project** (it and its
packages), Overflow, Filter (Project / **Group** / Machine / Kind /
State; `WallFilter.projects` holds project ids, older files' folders
still match, `groups` is new and optional in the file). **Grouping**
(`Grouping`: auto, none, group, project, branch; layout popover section
"Grouping") resolves one level below the scope: All / Overflow / Filter
→ groups (projects when there are no groups), Group → projects,
Project → package / branch / worktree / "main" (the project folder).
Band keys: `g:<group>` / `g:~other` (projects in no group) /
`g:~scratch` / `g:~none`; `p:<project>` (packages fold into their
repository); `b:pkg:<id>`, `b:br:<branch>`, `b:wt:<dir>`, `b:main`. A
project in several groups bands with its first group (by order). Band
order: groups by `order`, then other, scratch, none; projects by their
group's order, scratch last; then the wall's own order (header drag)
first. **Bands are drawn only when there are two or more** (or one
continued from another wall): a wall with one project is exactly the
plain wall of before (so every earlier UI test, and the real daemon's
one-project test run, lay out unchanged).

**Which project a card is in** (`ProjectCatalog`): an agent's
`projectId` when the catalog knows it, or a `scratch:<folder>` id
(hesperd sends no notifications for those); otherwise its folder's
project on its machine (longest path match; a package beats its repo),
else `scratch:<folder>` — or `path:<folder>` with a daemon without
projects (`projects.list` → -32601: every folder is a synthesized
project, nothing editable; that is how the app runs against an older
hesperd). An agent naming an unknown `projectId` makes the main model
call `projects.list` + `groups.list` (debounced 400 ms; the result is
fed through the same event path, so every window's model gets it).
Colors: the daemon's `color` as given (`colorSet` decoded); without one,
the Tokyo Night palette by FNV of the id. `identity` objects are decoded
(remote, else package, else local).

**Layouts group in their own way** (`WallLayout.make(_:bands:…)`):

- **E grid + shelf**: full-width **bands** (header 34 pt, the band's own
  fill grid, its own shelf strip at the band's end, 8 pt padding, 16 pt
  between bands, tinted frame from the project/group color). **Boxes**
  side by side (rows of boxes, a short last row stretched) only when every
  box gets at least two minimum cards of width: `boxesPerRow = ⌊(W + 16)
  / (2·minCard + gap + 16 + 16)⌋ ≥ 2` (a 14″ laptop: bands; 2560 pt at
  60 characters, or ≥ ~2700 pt at 80: two boxes). Vertical space goes by
  **grid rows**: each band gets ⌈weight / columns⌉ rows, every row on the
  wall the same height (≥ the minimum card; the wall scrolls past that).
  **Weight = the band's active agents with hysteresis**
  (`BandAllocator`): grows at once, shrinks only when it lost two or
  more active agents since, or none are left — so one agent flipping
  working ↔ done keeps its band's height (the band's cards stack into its
  rows) and the other bands' terminals don't resize. Attention never
  changes a weight (test: an approval moves nothing).
- **B columns**: one **lane** per band (header over its columns, 16 pt
  between lanes), the wall scrolls sideways as one (deviation from "per
  lane": nested scroll views per lane would fight the wall's own
  scrolling and the edge fades; one sideways scroll keeps today's B).
- **C treemap**: **nested** — bands by the sum of their agents' weights
  (4/2/1 as before), then the agents inside each band's rect below its
  header.
- **D main + stack**: no frames; cards are ordered by band, the main slot
  follows ⌘J across projects as before; tile headers are tinted with the
  project color (a 3 pt bar + 16 % header tint).

Every tile header (all layouts) shows a project color swatch and the
project's name instead of the folder name. Uniform tile font and `fit`
are unchanged; bands only decide where cards go.

**Band headers** (`BandHeaderView`): chevron, color swatch, name ("tools
(cont.)" on a continuation wall), branch/package/projects subtitle,
counts ("7 agents · 5 active · 1 needs you"), ＋ (a draft in this band,
in its project) and ↗ (a new wall scoped to the group / project; hidden
for other/scratch). A **click collapses** the band to one header line
(its cards hidden, not rendered, kept attached so nothing resizes);
**⌥-click** collapses every other band. A collapsed header carries the
attention ring (rose border + glow) and a "N needs you" badge. **Header
drag reorders** the bands: the header follows the mouse, the band under
it lights up, the drop sets the wall's band order. Chosen: **per wall in
windows.json** (not `groups.save`): grouping by project or branch has no
group to store an order in, two walls may want different orders, and a
group's `order` is shared with the other Mac. Selecting / activating a
card in a collapsed band (⌘J, a needs-you card) expands it.

**Overflow by group** (`ScopeMath.distribute(_:walls:place:unitOrder:)`):
the chain (first wall when All, then every Overflow wall) is filled by
**unit** = All's band (group, else project), in band order: a wall takes
whole units while they fit its capacity; a unit that doesn't fit the room
left goes to the next wall; a unit alone larger than a wall starts on a
fresh wall, fills it, and continues on the next as "<name> (cont.)"
(`Distribution.continued`); the last wall takes the rest; attention still
goes to the head. Without project data every agent is its own unit (the
old behaviour). Capacity is still counted in minimum cards (band headers
not counted).

**Project sidebar** (⌘0 per wall, View/Window ▸ Project Sidebar; Show
Wall moved to ⇧⌘0): All, Groups (group → its projects, packages with
agents under their repo), Other projects, scratch; counts of agents
everywhere and a rose dot where something needs you; the current scope
highlighted. Click: this wall's scope. ⌥-click: a new wall scoped to it.
Drag a row onto another wall window: that wall takes the scope; dropped
anywhere else (another display, the desktop): a new wall centred there
(`NSDraggingSource` end point; within the source window: nothing).
⌘-click multi-selects projects. Context menu: Show on This Wall, Open as
New Wall, Rename… (`projects.update {id, name}`), Color ▸
(`projects.update {id, color}`), Default Profile ▸ / Default Machine ▸
(`projects.update {id, defaults}`), Promote to Project… (scratch:
`projects.promote {machine, path, name}`), Add to Group ▸, New Group
(from Selection)… (`groups.save {group}`, empty id), Remove from <group>,
Remove Project (`projects.remove`); groups: Rename, Color, Remove
(`groups.remove`). Synthesized projects (no daemon data) only scope.

**Home wall** (the first wall): a "needs you elsewhere" strip at the top
with a compact card (state, name, project, the attention's title/detail,
Allow for approvals) for every agent in approval / question / error that
is not visible on that wall (scoped out or in a collapsed band), in ⌘J
order; a click routes like ⌘J (its window, the frontmost wall showing it,
else the home wall's focus view). **The strip's row is reserved whenever
the home wall can hide agents** (a scope other than All, or collapsed
bands) and shows "Nothing elsewhere needs you" when empty, so a card
appearing never resizes a terminal. It is a second, read-only view (no
attach).

**Drafts and keys.** ⌘N's draft has no project and opens in the "New"
band (see "New drafts have no project" above); ＋ (and ⌘N on a focused
header, the sidebar's "New Agent in …") puts a draft with that band's
project and defaults at the end of that band. The
draft stays in the band it opened in while written (a changed `#project`
does not move it); on start the agent joins its own project's band once
(the wall's frame animation). Arrows cross bands spatially as before
(collapsed cards have no frame, so they are never targets); **⌥↑ / ⌥↓**
jump to the first card of the previous / next open band; Tab follows
band order and skips collapsed bands. **⌥⌘← / ⌥⌘→** in a single view
(focus, and untabbed agent windows) step through the same project's
agents first, then the next project's (`ProjectStep`).

**Agent windows**: titled "project · agent", title bar tinted with the
project color (attention colors win), `tabbingIdentifier
"hesper.agent.<projectId>"` (the repository's for packages), so Merge All
Windows gathers one project.

**Persistence** (`windows.json` `SavedWall`, all optional): `grouping`
(omitted for auto), `collapsed` (band keys), `bandOrder`, `sidebar`.
Older files load unchanged.

**Wire use and assumptions** (built against the data agent's
`pkg/wire/projects.go`; the fake was shaped like it): `projects.list` and
`groups.list` after `drafts.list` on every (re)connect (-32601: no
projects); notifications `projects.changed {project}`, `projects.removed
{id}`, `groups.changed {group}`, `groups.removed {id}` in any order with
agent/draft notifications; `projects.update {id, name|color|defaults}`,
`projects.promote {machine, path, name}`, `projects.remove {id}`,
`groups.save {group}` (empty id: new; `order` = last + 1 for a new
group), `groups.remove {id}`. No extra connection or subscription: one
control connection, the catalog lives in the registry every window model
shares.

**fake-hesperd** (test-only, `Tools/fake-hesperd/projects.go`): the same
methods and notifications, `projectId` on agents (scratch ids outside
every project, listed, never notified), `--projects demo` (groups "acme
apps" (acme-apps with package ios-app, design-system) and "tools"
(hesper), a folder project with no agents, a scratch folder;
seeded agents spread over them, one on the fake mini).

**Tests.** HesperCore `ProjectsViewTests` (19): wire decoding (hesperd's
shape, lenient), project lookup (id, folder, package, other Mac, scratch,
no daemon data), grouping resolution per scope, bands by group / project /
branch-package-worktree with order and the wall's own order, one band =
plain wall, (cont.) titles, Group / Project / Filter scopes incl. old
filter files and the saved view round trip, Overflow keeping groups
whole, splitting a group bigger than a wall, attention staying home,
hysteresis, attention moving nothing, one agent finishing keeping other
bands' card sizes, bands vs boxes (laptop vs 3440 pt), collapsed bands
(E, B, C), lanes, nested treemap areas, D without frames, navigation
across bands (spatial, ⌥↑↓ skipping collapsed), project-first stepping,
sidebar sections/counts/dots/packages, home-wall set and strip rule.
UI `make -C app test-ui-projects` (`Tools/run-projects.sh`, fake daemon
`--projects demo`, 34 checks, `SHOTS=dir`): bands render (titles,
headers, cards inside), an approval moves nothing, ⌥↓ ⌥↑ and Tab across
bands, a header click collapses (cards hidden and not rendering), Tab
skips it, its badge + the home strip, ⌥-click collapses the others, ＋
makes a draft in the band's project, a changed #project keeps the draft
in place and the started agent moves to its band, ⌘0 sidebar and its
rows, a click scopes the wall (bands by branch/package), a scoped-out
approval appears as a home card without resizing anything and the card
jumps to it, ⌥-click opens a group wall, Overflow keeps groups whole, B
lanes, C nested, D no frames, grouping None, a wide wall draws boxes, the
agent window's project title and tab id, header drag reorders, a sidebar
row dropped outside opens a wall, windows.json keeps the view. `make
test` (unit 132, test-ui 68 + 4, test-ui-windows 33 + 7, test-ui-projects
34) and `make test-ui-real` (65 + 4, against the merged real hesperd
with projects) pass. Screenshots: `app/build/shots/projects/`
(laptop-bands, wide-boxes, collapsed-bands, collapsed-others, sidebar,
home-needs-you, lanes, nested, main-stack).

**Measured** (`make perf`, 16 flood tiles, this run's display was the
60 Hz external one): with `FAKE_ARGS="--projects demo"` (bands): 60.0
fps, 0 dropped, worst 16.7 ms, content 119 fps per tile, keystroke →
grid p50 2.5 ms, state → ring p50 12 ms, CPU 15 %, 122 MB; the same
without projects (plain wall) on the same machine state: 59.9 fps, 0
dropped, p50 2.5 ms, ring 10 ms, CPU 14 %, 157 MB — no regression.
Resolving bands is per sync (≤ 50 cards, no I/O).

**Not done here:** per-lane sideways scrolling in B, capacity counting
band headers, a collapsed band's agents releasing their attaches. Desks:
"As built — desks" below.

#### As built — desks (workspace model step 3)

Design: the workspace model design notes ("Desk",
scenarios "Laptop only" / "Office" / "Deep work"). Code: pure rules in
`HesperCore/Desks.swift` (`DisplayInfo`, `DisplaySetup`, `Desk`,
`DeskBook`, `HomeRule`, `DeskFold`) and `WindowModel.swift`
(`OwnWallMode`, `WallPointer`, `OwnWalls`, pointers in
`ScopeMath.distribute`), `ViewModel.swift` (pointer bands);
AppKit in `Windows/DeskController.swift` (displays, file, switching,
picker, menus), `WindowRestorer` (snapshot / apply a desk),
`WindowManager` (home order, visibility, pointers), `Bands.swift`
(pointer header), `SettingsView` (Desks tab). No relay change.

**A desk** is the whole arrangement: every wall (id, scope, grouping,
layout, min width, collapsed bands, band order, sidebar, own-walls
setting, frame, display, full screen) in desk order and every agent
window (agent, frame, display, tab group and selected tab, full
screen), saved per **display setup**.

**Display setup signature** (`DisplaySetup`). Each display gets a
stable key: `v<vendor>-m<model>-s<serial>` from CGDisplayVendorNumber /
ModelNumber / SerialNumber when it reports a serial, else
`v<vendor>-m<model>-<localized name>-<W>x<H>mm` (CGDisplayScreenSize,
physical, so resolution changes don't matter); identical displays
without serial get `#2`, `#3`… left to right. The signature is the
sorted keys plus the arrangement: where every other display sits
relative to the first (by key) — right / left / above / below by the
larger offset of the centres. `id = "ds-" + FNV-1a 64` of it. So it
ignores list order, CGDirectDisplayIDs, global coordinates (which move
with the main display) and resolution, survives reconnects, and changes
on plug / unplug and when a display is moved to another side in
Arrange. Default names: "Laptop only"; else the displays left to right
with the built-in one as "Laptop" ("Laptop + Studio Display"); lid
closed: the display's name. Renamable (picker, Settings ▸ Desks).

**File** `desks.json` (`--window-state PATH` is the desks file now;
otherwise, only against the live daemon,
`~/Library/Application Support/Hesper/desks.json`), written atomically
0.5 s after any window change and on quit:
`{version: 2, setups: [{id, signature, screens: [{key, name, builtin}],
current, lastUsed}], desks: [{id, name, setup, automatic, updated,
windows: {version: 1, walls: [SavedWall], agentWindows:
[SavedAgentWindow]}}]}`. `SavedWall.screen` / `SavedAgentWindow.screen`
hold the display's stable key; `SavedWall.ownWalls` is new (omitted =
collapsed). **Migration:** a version-1 windows.json (at the given path,
or `windows.json` next to the live desks.json when there is none)
becomes the current setup's automatic desk; its CGDirectDisplayIDs are
mapped to the stable keys of the displays attached now (unknown ones
stay; clamping falls back to overlap). windows.json is left in place.

**One desk per setup automatically** (`automatic`, named after the
setup), plus named desks (Window ▸ Save Desk As…, the picker's "Save
Desk As…": a name field popover; the same name replaces). Each setup
has a *current* desk; changes go into it. Window ▸ Desks ▸ <name> and
the **⌃⌘D picker** (overlay system `PopoverKind.desks`, under the
toolbar of the frontmost wall: this setup's desks checked, desks of
other displays (picking one copies it into this setup), Save Desk As…,
Rename) switch between them. Removing a named desk (Settings) falls
back to the automatic one; automatic desks stay.

**Automatic switching** (Settings ▸ Desks ▸ "Switch desks automatically
when displays change", default on): `didChangeScreenParameters` → 1 s
debounce (restarted by every change) → the new signature; same id:
nothing. Never while a mouse button is down or a window is in live
resize (re-checked every 0.5 s). Old desk: saves are refused as soon as
the displays differ from the desk's setup, so the frames macOS moves
windows to on unplug never reach it — the old desk keeps the last state
saved while its displays were attached. New desk: its current desk, else
**fold to defaults** (`DeskFold`): one main wall (its layout, card
width, grouping, sidebar; scope All, nothing collapsed) filling the
main display's visible frame, agent windows as tabs of one 980×680
window centred there (selected tab kept) or closed (Settings ▸ Desks ▸
"Displays without a desk: agent windows"). Applying a desk
(`WindowRestorer.apply`): walls not in it close, walls with the same id
are reconfigured and moved (the main wall always stays), missing ones
open; walls are put in desk order; agent windows not in it close,
existing ones are moved and regrouped (leave tab groups that differ,
rejoin by host), missing ones open (once the daemon listed the agents).
Moves are animated (NSAnimationContext, 0.3 s; none with Reduce Motion).
Switching off: the windows stay and become the new setup's desk. At
launch: the current setup's desk, else the most recent desk of another
setup folded.

**Frames, full screen, Spaces.** Frames are clamped to the visible
frames of the displays there are now (saved display if attached, else
most overlap, else main; `FrameClamp`). Full screen is restored by
toggling after 0.5 s; a full-screen window that the desk wants windowed
leaves full screen and is moved after 0.9 s. **Spaces can't be
restored**: no public API places a window on a Space or tells which
Space it is on; windows come back on the current Space (and a
full-screen window gets its own Space).

**Home wall per desk** (`HomeRule`): the first wall of the desk is
home — the needs-you strip, the head of the overflow chain, drafts,
the target of routing's "main wall". Window ▸ Make Home Wall and the
scope popover's "Make Home Wall" row move the active wall first; saved
as the wall order in the desk. The app's main window (the primary
model) can be any wall of the order.

**Pointers to own walls.** A wall whose scope is a project or a group
is that project's / group's **own wall**. While it is really visible —
open (ordered in), not minimized, on an attached display (`isVisible`,
`isMiniaturized`, `screen` and the desk's displays) — every wider wall
(All, Overflow, Filter: project and group walls; a group wall: project
walls; never a wall with the same scope, never a project wall) shows it
as **one collapsed pointer line** in band style: ▸ swatch, name, `in
“Hesper — acme-apps” window ↗`, "4 agents · 3 working", the rose ring
and "N needs you" badge. Its agents' tiles are removed from that wall
(no read-only attaches there); the line sits where its band was (after
the band its agents would be in, or in that band's place). A click
brings the own wall forward (`makeKeyAndOrderFront`; Spaces as macOS
does). Minimizing, hiding or closing the own wall, or unplugging its
display (desk fold), brings the full band back; the change re-lays out
once (minimize / deminiaturize / screen change / ordering in or out
trigger a 50 ms coalesced resync), attention never does (pointer agents
have no tiles; bands keep their hysteresis). **Deviation:** occlusion
is not counted as "not visible" — overlapping windows on one display
would otherwise re-lay out on every click — and Spaces aren't
detectable, so an own wall on another Space counts as visible.
Attention: the ring stays on the pointer; the home strip covers those
agents (they aren't visible on the home wall); ⌘J still routes to the
frontmost wall showing the agent (the own wall). Overflow: pointer
agents leave the chain before distribution (the pointer sits on the
chain's head) and take no capacity; attention in them is not forced
home. Per-wall setting in the layout popover, section "Own walls":
Collapsed pointer (default) / Full band (tiles here too, as before) /
Hidden; saved per wall in the desk. With grouping None there are no
bands: Collapsed behaves like Hidden. Single-agent windows (⇧/⌘-click)
are not own walls: their tiles stay, with the ↗ marker.

**Tests.** HesperCore `DesksTests`: signature order-independent,
survives reconnect (new display ids, moved origin, other resolution),
changes on plug / side / another unit, identical displays numbered,
identity fallback, default names; desk book (one automatic desk per
setup, save-as, select, rename, remove, a desk of another setup copied,
round trip, bad files); migration (desk, display ids → keys); fold
(one All wall on the main display, tabs / close, one window no group,
clamping); home rule; pointers (visible project wall → pointer and no
tiles, minimized / closed → band back, setting values and file, rules,
no capacity in the chain, pointer band placement, strip reserved).
`ProjectsViewTests`' scope test now sets `ownWalls: .full` where a
project wall would otherwise be a pointer. UI `make -C app
test-ui-desks` (part of `make test`; `Tools/run-desks.sh`, fake daemon
`--projects demo`, display sets injected with `--fake-screens` /
`DeskController.inject`, carved out of the real main display so windows
stay visible; `SHOTS=dir`; 41 + 3 checks): windows.json migrated into
"Laptop only"; a project wall → pointer, no tiles there, title and
counts, click brings it forward, approval ring + home card, minimize →
band back, restore → pointer, setting full / hidden / collapsed, agent
window not a pointer, close → band back; plugging "office" folds to
defaults with the agent window kept; arranging office (group wall on
the Studio Display made home, agent window, collapsed band, sidebar);
unplug → laptop desk exactly; replug (displays listed in reverse) →
office desk exactly (frames, scopes, view, home, agent windows); same
displays in another order: no switch; no switch while busy, then the
switch; ⌃⌘D picker, Save Desk As "Deep work", picking desks, Window ▸
Desks; switching off keeps the windows; desks.json has 2 setups, 3
desks; relaunch on the office displays restores Deep work with the
group wall as home, all windows on attached displays. `make test`
(unit 154, test-ui 68 + 4, test-ui-windows 33 + 7, test-ui-projects 34,
test-ui-desks 41 + 3) and `make test-ui-real` (65 + 4) pass.
Screenshots: `app/build/shots/desks/` (pointer, desk-office,
desk-laptop: maps of the fake displays with each Hesper window captured
alone; desk-picker).

#### As built — new agents per window

Concept: Hesper "New agent anywhere" (8 Oct 2026; the user took every
recommended call). Root cause fixed: drafts were drawn only by
`walls.first` (`showsDrafts`), so ⌘N in Wall 2 or a project window made
a draft no visible window drew, and a project window could never create
anything. Code: pure rules in `HesperCore/DraftHome.swift` (`DraftHome`,
`DraftPreset`, `DraftLock`, `StartLanding`); `Draft.wall` /
`Draft.projectLocked` (`Drafts.swift`; wire `draft.wall`,
`draft.projectLocked`, hesperd stores the whole draft);
`AppModel+Drafts.newDraft` (presets, quick launch from the focus view),
`AppModel.scopedWallItems` (`draftsShown` replaces `showsDrafts`; history
ghost cards keep the home-wall rule via `showsGhosts`),
`WindowManager` (open walls, acting main, release on close, "Started in
… ↗"), `DraftChips.lockedPill`, `QuickLaunchController.show(project:
machine:)`, toast actions (`AppModel.ToastAction`, `Overlays`).

**A draft belongs to its window.** Every draft made in a wall window
saves that window's wall id (windows.json: `main`, `wall-…`) in
`draft.wall`; that window shows it, whatever its scope or order. The
main wall (the primary model's) also shows drafts without a wall (older
drafts, the single window) and drafts whose window isn't open. "Open":
every extra wall in the window list, and the main wall unless its window
was closed (ordered out; minimized or a hidden app still counts). With
the main window closed, the first open All wall stands in for it, else
the first open wall (`DraftHome.actingMain`). Invariant (unit-tested):
every draft is visible in exactly one open wall window. Agent windows
show no drafts.

**The window presets what it is about** (`DraftPreset.make`, for ⌘N, the
toolbar's ＋ New and the palette's "New agent"; explicit project actions
— a band's ＋, the sidebar's "New Agent in …", a palette project — keep
their project):

| Window | ⌘N / toolbar ＋ | band ＋ | after start |
|---|---|---|---|
| Main wall (All), Wall 2… (All, Overflow) | draft in this window's New area, no project | that project's band | joins its band where its project shows |
| Project window | draft here, project **locked** to the window's (folder and machine via `DraftSeed.place`) | same (a branch / package band of it: locked too) | stays; another folder: toast |
| Group window | draft here in the group's most recently used project (`lastUsed`); the folder chip lists only the group's projects (a typed path or a #token still reaches any folder) | that project | stays (in the group) |
| Machine-filtered wall | the filter's first machine, explicit; no project | that project on that machine | stays if on that machine |
| Agent window / focus view | quick launch, preset to the agent's project folder and machine (a #token in its text is dropped) | — | as quick launch |

Quick launch from the hotkey, menu bar and Dock is unchanged (no preset).

**The lock.** A project window's draft carries `projectLocked`; the
project pill (after the chips) shows the project's mark, its name and a
lock (`draft.chip.projectLock`). A click unlocks it and opens the folder
list; a folder (#token, chip, typed path, palette) that belongs to
another project, or to none, unlocks it by itself (`DraftLock.check` in
`editDraftContent`; a package of the project keeps it). The lock never
blocks anything: it says "this starts in this window's project".

**Started elsewhere.** After ⌘↩, when the window the draft was in
doesn't show the new agent (`ScopeMath.distribute` with the agent;
pointer lines to a project's own window count as elsewhere), that window
shows a toast "Started <name> in <project>" with a "Show ↗" button (6 s):
it brings forward the open wall that shows the agent and selects it,
else takes it where ⌘J would (the main wall). `StartLanding.elsewhere`.

**Owner window closed.** Closing a wall window parks its open draft (an
empty one goes), takes its unsaved edits along, clears `wall` on every
draft it owned and saves them through the main model: they appear on
the main wall — its New area, or their project's band (`band` is kept).
Closing the main window only hides it: its drafts show on the acting
main wall until it is back. "Make Home Wall" no longer moves drafts.

Tests: HesperCore `DraftHomeTests` (9: exactly-once across only main
open, main closed with Wall 2 + a project window, several walls, only a
project window, main scoped to a project; own window regardless of
scope; acting main; release on close keeps the band; encoding; presets
per scope; lock by band lineage; unlock by folder; started-elsewhere
target); relay `TestDraftsSaveListRemoveAndRestart` (band, wall,
projectLocked survive a restart); test-ui-projects (⌘N in a group
window: there, a group project, not on main; closing it moves the draft
to main with its text and band; ⌘N in a project window: there, locked,
not on main; screenshot project-window-draft). Offscreen render:
`Hesper --render-draft DIR` (draft-locked/unlocked in Dusk and Daylight,
narrow). Not run here (headless rule): the UI suites.

### Part C: cutover (install.sh, packaging/, scripts/test-install.sh, CI)

**`install.sh`** (POSIX sh; `--dry-run` prints every action and changes
nothing; every step checks first, so a second run only reinstalls the
build): `make -C relay build`, `make -C app build` (`--skip-build` uses the
outputs as they are) → copies `hesperd`, `hesperctl`, `hesper-keys` into
`app/build/Hesper.app/Contents/MacOS` and `hesperd`'s SHA-256 into
`Contents/Resources/hesperd.sha256` → signs (below) → installs to
`$HESPER_APP_DIR` (default `~/Applications/Hesper.app`; an existing bundle
with another `CFBundleIdentifier` than `de.olezierau.hesper.mac` is never
replaced) → links `~/.local/bin/hesperd`, `~/.local/bin/hesperctl` and
`~/.local/lib/hesper/hesper-keys` (where `devicekey.FindHelper` looks first)
into the bundle → state dir 0700 → hooks → LaunchAgent → firewall →
optional login item. (On a Mac that ran Ghosty, `migrate_from_ghosty` runs
after signing and before the app is installed; see "Wire names kept from
Ghosty".)

- **LaunchAgent** `de.olezierau.hesperd`
  (`~/Library/LaunchAgents/de.olezierau.hesperd.plist`): `<app>/Contents/
  MacOS/hesperd serve --state-dir <state>` plus `relay_plist_args` (part R:
  `--config-dir <config>`, and `--allow-shell` with `./install.sh
  --allow-shell`; a rerun without `--allow-shell`/`--no-allow-shell` keeps
  what the installed plist has),
  RunAtLoad, KeepAlive, `ProcessType Interactive` (agents build and test:
  no background throttling), ThrottleInterval 10, stdout and stderr to
  `<state>/hesperd.log`. Rewritten only when the plist differs (then
  bootout + bootstrap); otherwise `kickstart -k` only when the recorded
  `hesperd` checksum changed, so a reinstall of the same daemon never
  restarts it (a restart resumes agents, see Persistence).
- **Hooks:** `hesperd hooks install --bin ~/.local/bin/hesperd` (the stable
  link, not the bundle path) for `~/.claude/settings.json` and
  `${CODEX_HOME:-~/.codex}`. The installer plans with `--dry-run` first and
  copies each file that will change into its own timestamped backup
  (`~/.ghosty-config-backups/<time>/`), in addition to hesperd's single
  `*.hesperd-backup`.
- **Signing:** `SIGN_IDENTITY` set → `codesign --options runtime
  --timestamp` for each tool (hesperd with `packaging/hesperd.entitlements`:
  `com.apple.security.automation.apple-events`, since hesperd is the
  responsible process of agents' osascript), then the bundle; else ad-hoc.
  `NOTARY_PROFILE` (needs `SIGN_IDENTITY`) → `notarytool submit
  --keychain-profile … --wait` and `stapler staple`.
- **Firewall:** the old `allow_through_firewall` logic for
  `<app>/Contents/MacOS/hesperd` (sudo only when not yet allowed; `sudo -n`
  without a terminal; prints the commands if it cannot).
- **Legacy cleanup: removed.** The installer once also cleaned up the tmux
  setup (its host/fleet LaunchAgents, `bin/ghosty-*`, tmux and Ghostty
  spaces links, the SwiftBar plugin; `--keep-legacy` skipped it) and linked
  a Ghostty config from the repository (`--no-ghostty-config`). Both steps,
  their flags and the repository's `ghostty/` folder are gone; only the
  Ghosty → Hesper migration remains. Hesper.app still reads its font
  settings from the user's own Ghostty config. Relay credentials in the
  state dir are reused, never moved.
- **Stubs for tests:** `LAUNCHCTL`, `CODESIGN`, `XCRUN`, `SUDO`,
  `OSASCRIPT`, `DEFAULTS`, `PGREP`, `SOCKETFILTERFW`; `--print-launch-agent`
  prints the plist.

**Relay (filled in after part R):** `hesperd serve` finds both enrollments
in the state directory by default, so the LaunchAgent passes no credential
flags. The "State and relay sign-in" step reuses `host.credentials.json`
and `controller.credentials.json` there and prints `hesperctl login --role host|controller … --out
<state>/<role>.credentials.json` only for a missing role, then the
`launchctl kickstart -k` that makes hesperd pick it up. The relay is
self-hosted and has no built-in default: `--relay URL` writes `"relay"` into
the config directory's `settings.json` (backed up first, other keys kept,
dry run only says so), where `hesperctl login` finds it (after `--relay` and
`$HESPER_RELAY`, before the relay an existing enrollment records). With no
relay configured and no enrollment, the step prints no login command but how
to deploy one (`relay/docs/deployment.md`) and set it, and so does "Next".
An existing enrollment keeps the relay its credentials file names.
`machines.json` (config
directory) is never changed when it exists (it reports the short name it
gives this Mac's host device id, or the entry to add); when it does not,
`--machine SHORT` writes `{"machines": {"<host device id>": {"short":
"SHORT"}}}` (needs the host enrollment). Device approval hints only for
what is missing: no `controllers.json` → `pair-host --machine <this>` on
the other Mac and `approve CODE` here; no `trusted-hosts.json` (with a
controller enrollment) → the reverse. The firewall step allows
`<app>/Contents/MacOS/hesperd` and names a leftover ghostyd
(`…/Ghosty.app/Contents/MacOS/ghostyd`) firewall entry with its
`socketfilterfw --remove` command.

**Removed:** `tmux/`, `ghostty/spaces/`, `menubar/`, `shell/`, all other
`bin/ghosty-*` (cockpit, draft, space, …), `scripts/install-agent-hooks.py`,
`scripts/test-{cockpit,draft,handoff,performance}.py`,
`scripts/{benchmark,diagnose}-performance.py`, `docs/{draft,strip,new-task}-ux.html`,
`docs/{draft,strip}-contract.md`, `docs/performance.md`. After part R also `bin/`
(`ghosty-handoff`, `ghosty-pane`, `ghosty-add-agent`, `ghosty-agents`,
`ghosty-agent-event`, `ghosty-session-gc`, `README.md`; handoff is
`internal/handoff` now), `docs/handoff-architecture.md` (bundle formats:
`internal/handoff`, relay/docs/protocol.md "Moves") and
`docs/remote-shell-plan.html`; the relay justfile's tmux test targets
became `just test-remote`. Later also `ios/` (the iPhone client, which
still spoke the tmux host's protocol), the repository's `ghostty/` config,
install.sh's tmux-setup cleanup and the relay's APNs push.

**CI:** `.github/workflows/app.yml` (macOS: relay build, app unit tests,
fake daemon, app build, the UI smoke test as non-blocking, and
`scripts/test-install.sh`); `relay.yml` unchanged apart from paths and a
gofmt check; `release.yml` (tag `v*`: build, sign with `DEVELOPER_ID_P12` /
`P12_PASSWORD` in a temporary keychain, notarize with the App Store Connect
API key secrets `NOTARY_API_KEY` / `NOTARY_API_KEY_ID` /
`NOTARY_API_ISSUER`, staple, zip + SHA256SUMS on a GitHub release); it only
prints a notice while the secrets are missing.

### Part R: hesperd is the gateway (relay/internal/gateway, host, remote, handoff, pkg/agentlink)

Code: `internal/gateway` (assembles `hesperd serve`), `internal/host` (host
role), `internal/remote` (controller role, `agents.Remote`),
`internal/handoff` (moves), `pkg/agentlink` (links), `internal/controlsock`
(the control socket for hesperctl's relay commands), `internal/agents`
(`activity`, `projects.clone`, `Pack`/`Import`/`Plan`/`Probe`/`StopWait`).
Wire details: `relay/docs/protocol.md` (Agents, Links, Moves);
design: `relay/docs/architecture.md` (hesperd across machines),
`relay/docs/terminal-streaming.md`, `relay/docs/direct-path.md`.

**Roles.** `hesperd serve` runs both relay roles with their own
enrollments from the state directory: `host.credentials.json` (serve this
Mac's agents to devices approved here) and `controller.credentials.json`
(reach every other machine). A role whose file is missing stays off; a
role whose credential is rejected stops and logs the login command, the
other role and the agents keep running. New flags: `--host-credentials`,
`--controller-credentials`, `--control-socket` (default
`controller.sock`, `off`), `--allow-shell`, `--require-device-keys`,
`--direct auto|on|off`, `--direct-port`, `--keep-awake`,
`--keep-awake-min-battery`. Device keys, `e2e.key`, `trusted-hosts.json`,
`controllers.json`, the audit log, nonces and move uploads live in the
state directory. **Removed:** `ghosty-host`, `hesperctl fleet` (and
`fleet.json`, the cockpit refresh exec), `internal/tmux` (pane views, tmux
shells), `internal/helper`, the session provider interfaces, and
hesperctl's relay-attach, shell(s), inbox/ack/respond, relay-send/job/
relay-stop, handoff, task and bring-back. hesperctl keeps login, pair,
invite/devices/revoke, machines, watch/request, trust, pair-host,
approve(-device), devices-local and the agent commands.

**What the relay sees** (host publication, unsigned `snapshot`): per agent
its local id (`terminalId`), kind (`role`), state, attention kind and
`attentionSince`, PTY size, `exited`; the host's `short`, capabilities
(`agents`, `e2e`, `direct`, `transfer`, `spawn`, `shell`, `ping`), keys,
machine stats. No names, tasks, attention titles/details,
summaries, activity, paths, branches or terminal bytes: those travel only
sealed (request results, link events, attach channels). A hesperd host
refuses everything but `snapshot`, `ping`, `devices.request` and the
handshake in plaintext; the controller sends everything with
`RequireE2E`.

**Host methods** (signed, part K rights): `agents.list`, `agents.spawn`
(a shell: kind `shell` announced in the params, right `shell`, strong key,
host `--allow-shell`), `agents.input`, `agents.answer`, `agents.stop
{id, wait?}`, `agents.resume`, `agents.remove`, `agents.rename`,
`agents.link`, `agents.attach`, `agents.plan`, `agents.probe`,
`agents.export`, `download`, `transfer`, `agents.import`, `job`,
`projects.clone`, `projects.recent`, `profiles.list`, `fs.stat`, `files.put`, `files.chunk`. Rights: observe
(list, link, ro attach, plan, probe, export, download, job, recent,
profiles, fs.stat), answer, type (input, rw attach, files.put/chunk), transfer (spawn, stop, resume,
remove, rename, import, transfer, clone), shell (any shell agent, plus
the strong key to start one). Shell agents are invisible to callers
without the shell right (lists and link events).

**Links and attach.** One link per (controller device, host): an
`agents.link` ticket opened like the former `terminal.open` (sealed relay
stream, or a direct stream), multiplexed (`kind | channel | len |
payload`): channel 0 carries the host's agent events (full list, then
each change/removal, pushed as they happen), channels > 0 one attach each,
opened by a signed `agents.attach {link, ch, mode, request}` where
`request` is the local `AttachRequest` passed through unchanged (so
additive options such as `view` behave the same remotely). The host runs
`ptyhost`'s own attach on it, so redraw, the size owner rule, slow-viewer
resync and EXIT are identical. One stream per pair keeps within the
relay's four streams per host for any number of tiles.

The controller's bridge (`hesperd attach M/x` → L's socket) forwards the
client's frames unchanged and the host's reply line and frames as whole
frames. It reattaches (a SIZE frame, then the fresh redraw; an owner
reclaims its last size) when the client falls 4 MiB behind, when the host
ends a channel without EXIT, when the link breaks (waiting up to 30 s for
the next link, then the attach ends) and when the link moves to the direct
path. Input typed during a reattach is dropped. Attach refusals keep the
daemon's error codes (`forbidden` for missing rights).

**Merged registry.** `agents.list`/`agents.subscribe` include every
reachable machine's agents with ids `<short>/<local id>` and `machine` =
that short name (machines.json by relay device id, else the host's own
`short`, else its relay name lowercased to the first space/dot; a clash
with another machine gets a digit). Every agents method for such an id
(and `agents.spawn`/`projects.clone` with `machine`) goes to that machine
as one signed request; results come back renamed; errors keep `not_found`,
`invalid`, `exists`, `unavailable`, `forbidden`, others are `remote`. An
unreachable machine's agents stay listed for 60 s, then `agents.removed`.

**hello.** `machines` lists this Mac (`route: "local"`), then every other
machine of the owner: `online` (relay presence while connected), `route`
(`direct`/`relay` while online, `""` offline), `rttMs` (median of the last
five pings on that route, 0 until measured).

**agents.move `{id, to}`**, between any two machines (this one included):
stop the agent on the source and wait for its exit; source's plan (project,
base and main-branch commits) and target's probe (which it has); the
source packs (`internal/handoff`: the handoff commit of staged and
unstaged work incl. untracked files, a Git bundle of the branch and that
commit, incremental from what the target has, plus the Claude session file
or Codex rollout); a remote source exports it sealed for this Mac
(`agents.export`/`download`), a remote target receives it as a sealed
upload (`transfer`, `agents.import`, polled with `job`). The target checks
out the branch at the base in the same path mapped to its home (worktree
included; a missing repository is created from a full bundle), restores
the uncommitted work as uncommitted changes, places the conversation (every
`cwd` mapped to its home) and resumes the agent with its session, keeping
its local id when free (`L/a7f3k2` → `M/a7f3k2`). Then the source's agent
is removed. Any failure after the stop resumes the source's agent; the
error keeps handoff codes (`branch_diverged`, `dirty_target`,
`missing_project`, `conflicts`, …). Shells do not move.
(Superseded in part by "As built — move work" below: no stop first, a
preflight, checkpoints, worktrees, clone from the remote, close with
reason `moved`, fork.)

**Additions for part A** (local and remote): `activity` on Agent (claude
and codex PreToolUse: `"<tool>: <detail>"`, ≤ 80 chars; cleared at
UserPromptSubmit, Stop/notify, StopFailure, SessionStart, SessionEnd and
process exit). `projects.clone {url, machine?}` → `{path}`: `git clone`
into the projects root (`$HESPER_PROJECT_ROOT`, `~/projects`)`/<repo>`
(`.git` stripped); https/ssh/git/file URLs, `git@host:path` and absolute
paths; an option-looking URL is `invalid`; the same clone already there is
returned; anything else there is `exists`; the path joins
`projects.recent`; 10 minute limit.

**Relay deploy:** none needed. Links, attaches, events and moves ride on
requests, the channel and terminal streams the deployed hub already
routes; the snapshot is any JSON under 256 KiB.

**Tests** (`relay/internal/transport/remote*_test.go`, two hesperd against
an in-process relay behind a recording tap, software keys, fake agents):
remote spawn → approval (detail) → answer → done, input, rename, stop,
resume, error codes, and no task/name/prompt/screen text at the relay;
attach ro/rw (size reply, owner resize, ro cannot type or resize, EXIT);
slow viewer resynced; events latency; approval gates (unapproved device:
no link, no agents, `forbidden`; observer cannot answer/type/spawn/stop or
attach rw; no shell without the shell right; no shell without
`--allow-shell`; shells invisible without the right); direct path used
and its loss falling back with the attach carrying on; move L→M→L with
conversation and uncommitted work; reconnect after every relay socket
dropped. Plus `internal/handoff` (full and incremental bundles, worktrees,
transcript path mapping, Codex rollouts, manifest checks), `internal/host`
(snapshot without task text, rights, plaintext refusal, imports/exports),
`pkg/agentlink`, `cmd/hesperctl` (device approval end to end, control
socket forwarding the caller's signature).

**Measured** (`make latency`, M1 Max, relay legs of 20 ms round trip,
network floor 40 ms): keystroke → echo through a remote attach p50 45.1 /
p95 46.9 / p99 58.3 ms over the relay, 0.4 / 1.5 / 4.4 ms on the direct
path; opening a remote attach 45.7 ms (relay) / 0.7 ms (direct); a state
change on M to L's subscription p50 22.1 ms, p95 23.2 ms (target was
< 100 ms + RTT); a signed request 45.9 ms; ping 45.1 ms.

**Not done here:** predictive local echo; `codex fork`. The install side
(LaunchAgent for `hesperd serve`, firewall allowance for the hesperd
binary) is part C above.

### As built — projects (data; workspace model step 1)

Design: the workspace model design notes (iteration 2,
"Reality check"). Code: `relay/internal/projects` (store, identity,
detection, replication), `relay/pkg/wire/projects.go` (wire types), hooks
marked "projects step 1" in `internal/agents` (`projecthook.go`; Spawn,
Resume, restore and Import set `projectId`; `server.go` routes;
`persist.go` recent), `internal/host/projects.go`,
`internal/remote/projects.go`, `internal/gateway/projects.go`,
`pkg/agentlink` (`Event.projects`), `pkg/devicekey` (rights).

**Model.** A project is a folder with an identity. Kinds: `repo` (a git
repository), `package` (a monorepo package, or a promoted folder inside a
repository), `folder` (plain), `reference` (read-mostly; a kind the user
sets) and the virtual `scratch`. A group is a named, ordered set of
projects (one level; a project may be in several). Projects and groups are
shared by the owner's Macs; paths are per machine.

**Identity and ids** (`identity.go`). Repository: its remote (origin, else
upstream, else the first by name) normalized: scheme, user, password and
port dropped, host lower-cased, leading/trailing slashes and `.git`
dropped, `ssh.github.com`/`www.github.com` → `github.com`, the path
lower-cased on github.com, gitlab.com and bitbucket.org (they ignore case;
kept elsewhere). `git@GitHub.com:Owner/Repo.git`,
`https://u:t@github.com/owner/repo/`, `ssh://git@github.com:22/owner/repo`
→ `github.com/owner/repo`; a local path or `file://` remote →
`file:<path>`. Package: repository identity + path relative to the
repository (`identity.package`). A repository without remote and a plain
folder: a generated `identity.local` (`l-` + 16 hex). **`id` = `"p-"` +
the first 16 hex characters of the SHA-256 of the identity key**
(`git:<remote>[#<package>]` or `local:<local>[#<package>]`), so every Mac
computes the same id for the same repository: a remote agent's `projectId`
equals a local agent's in the same repository. Scratch:
`"scratch:<folder>"` (the agent's folder as given). Color: `#rrggbb` set
by `projects.update`, else `Palette[fnv32a(id) % 10]` (Tokyo Night:
`#7aa2f7 #bb9af7 #73daca #ff9e64 #7dcfff #9d7cd8 #2ac3de #e0af68 #9ece6a
#b4f9f8`). Default names: the last part of the remote URL as configured
(case kept, no `.git`), a package's manifest name, a folder's base name.

**Which project an agent is in** (`Store.Resolve`: on spawn, resume,
restore after a restart and move import; again for every local agent
whenever projects change): the deepest project containing the agent's
folder. A worktree counts as its repository's main checkout (`git
rev-parse --path-format=absolute --show-toplevel --git-common-dir`: the
main worktree is the common dir's parent when that is a `.git`;
submodules and bare repositories keep their own top), so an agent in a
worktree of `app` (or in a package folder inside it) joins `app` (or that
package). Candidates: every project with a path on this Mac containing
the folder (in the main checkout's coordinates, or literally), plus the
repository and package found by identity (a second clone of the same
repository on one Mac joins it too; the first clone keeps the recorded
path while it exists). No candidate: `scratch:<folder>`.
**Auto-registration:** the folder's repository, and the detected package
the folder is in, become projects on first sight, except repositories at
the home folder or above (a dotfiles repository in `~` would swallow
everything) and removed ones (agents starting there do not bring them
back; `projects.promote` does). Plain folders never register by
themselves: they are scratch until promoted. Detection is cached: two git
calls per new folder (re-checked after 2 min), packages per checkout keyed
by the manifests' size and mtime (seven `stat`s); `projects.list`,
`projects.recent`, `Touch` and snapshots never run git. A folder that does
not exist (yet) is looked at from its nearest existing parent.

**Monorepo packages** (`packages.go`; detected, but a package becomes a
project only once an agent works in it; repositories list theirs as
`detectedPackages`): pnpm (`pnpm-workspace.yaml` `packages`), npm/yarn
(`package.json` `workspaces`, a list or `{packages}`), Turborepo
(`turbo.json` next to npm/pnpm workspaces: tool `turbo`), Lerna
(`lerna.json` `packages`, default `packages/*`), Nx (`nx.json`: every
`project.json` ≤ 4 deep outside node_modules, dot folders, dist, build,
target; name from project.json, else package.json, else the folder), Go
(`go.work` `use`, single and block; name: the module path's last part),
Cargo (`[workspace] members` minus `exclude`). Globs: `* ? [..]` per
segment, `**` (≤ 6 deep), `!` excludes; JS and Cargo matches need their
manifest; never node_modules or dot folders; ≤ 2000 per repository.

**Wire** (local socket; all additive; types in `pkg/wire/projects.go`):

| Method | Params | Result |
|---|---|---|
| `projects.list` | – | `[ProjectInfo]` |
| `projects.update` | `{id, name?, color?, kind?, defaults?}` | `ProjectInfo` |
| `projects.promote` | `{machine?, path, name?, kind?}` | `ProjectInfo` |
| `projects.remove` | `{id}` | `{}` |
| `groups.list` | – | `[Group]` |
| `groups.save` | `{group}` | `Group` |
| `groups.remove` | `{id}` | `{}` |

- `Agent.projectId` (string; omitted only when hesperd runs without a
  project store, i.e. never in `hesperd serve`).
- `ProjectInfo = {id, name, color, colorSet?, kind:
  "repo"|"package"|"folder"|"reference"|"scratch", identity: {remote?,
  package?, local?}, parentId?, paths: {<machine short>: path}, groups:
  [groupId] (sorted), defaults: {profile?, machine?}, detectedPackages?:
  [{path, name, tool}], lastUsed}`. **Refinements over the plan:**
  `colorSet: true` when the user set the color (else `color` is the
  automatic one); `detectedPackages[].tool` (pnpm, npm, turbo, lerna, nx,
  go, cargo); `lastUsed` is `0001-01-01T00:00:00Z` for a project no agent
  started in yet; `parentId` is a package's (or promoted inner folder's)
  repository. `paths` keys: this Mac's short name; another Mac's by the
  name this Mac gives it (as in agent ids: machines.json, …), else the
  name it gives itself, else its node id. `detectedPackages` only for
  repositories with a folder on this Mac whose manifests declare packages
  (paths relative to the repository).
- `projects.list`: every project, most recently used first (then name),
  then the scratch projects of the agents there are (local and remote; one
  per `scratch:<folder>`, `paths` = the machines of those agents,
  `lastUsed` = the newest agent's `created`, `groups` empty).
  Repositories are re-checked for packages here.
- `projects.update`: the fields given change; `color: ""` goes back to
  the automatic color; `kind` one of repo, package, folder, reference;
  `defaults` replaced whole; `name` 1–200 characters on one line. Unknown
  or removed id `not_found`; a `scratch:` id or bad values `invalid`.
- `projects.promote`: a repository's top (or any of its worktrees' tops)
  → its repository project; a folder inside a repository → a project with
  the repository's identity + that relative path (kind `package` when it
  is a detected package, else `folder`; `parentId` the repository); any
  other folder → `folder` with a new local identity (the same project
  again when the folder already is one). `name`/`kind` set when given.
  Always revives a removed project. `machine` other than this Mac: run
  there (host method `projects.promote`, its state merged here before the
  answer). Missing folder `not_found`, relative path `invalid`.
- `projects.remove`: a tombstone (it reaches the other Macs). Agents fall
  back to the next project containing their folder (packages inside a
  removed repository stay), else scratch, with `agents.changed`.
- `groups.list` by `order`, then name; `Group = {id, name, projectIds,
  order, color?}` (`projectIds` without removed projects). `groups.save`
  creates or replaces (empty `id`: `g-` + 12 hex; ids are `g-` + 1–40 of
  `[a-z0-9-]`); every project id must be a live project (scratch and
  unknown ids `invalid`/`not_found`), duplicates dropped, `color`
  `#rrggbb` or empty. `groups.remove` `not_found` for unknown ids.
- **Notifications** on `agents.subscribe` connections, sent by their own
  goroutine (they interleave with agents/drafts notifications): first
  `projects.changed {project}` for every project, then `groups.changed
  {group}` for every group, then each change: `projects.changed`,
  `projects.removed {id}`, `groups.changed`, `groups.removed {id}`,
  coalesced per id. A group change also re-sends the projects whose
  `groups` changed; newly detected packages re-send their repository.
  Scratch projects have no notifications (they come and go with agents).
  **Rule for the app: an agent whose `projectId` it does not know (a
  scratch folder, or a project whose `projects.changed` is still on its
  way) → call `projects.list` (debounced).** `agents.changed` is sent when
  an agent's `projectId` changes.
- Errors carry `data.code` like every method (bad params are
  `-32000`/`invalid` here, not `-32602`).
- `projects.recent` → `[{path, name, lastUsed, projectId?}]`: the
  projects with a folder on this Mac first (`projectId` set, most recently
  used first, packages included), then other folders agents started in
  (≤ 30).

**Persistence:** `$HESPER_STATE_DIR/projects.json` (0600, temp file +
fsync + rename, ≤ 50 ms after a change): `{version: 2, node, clock,
projects: [Record], groups: [GroupRecord], nodes, aliases, recent}`. The
previous file (a list of recent folders) is read as `recent`; a broken
file moves to `projects.json.broken`; tombstones older than 180 days are
dropped on load.

**Sync between Macs** (`crdt.go`, `internal/host/projects.go`,
`internal/remote/projects.go`). Every daemon has a node id (`n-` + 16 hex,
kept in projects.json) and a hybrid logical clock. Every field of a
project (`name`, `color`, `kind`, `defaults`, `deleted`, and each node's
`path`) and of a group (`name`, `projectIds`, `order`, `color`,
`deleted`) is a last-writer-wins register `{v, s: {t: ms, c, n: node}}`;
equal stamps compare the values, so any order of merges converges.
Identity and `parentId` never change; `lastUsed` takes the maximum.
Removal is the `deleted` register (a tombstone): an older copy never
revives it, a later promote does. Projects merge by id (= identity hash:
same remote, same project; paths unioned per node); a record whose id is
not its identity's hash is dropped. Transport, over part R's links and
end-to-end channel only: the host sends its whole state on every link
(`agentlink.Event{projects: State}` on channel 0, sealed like the agent
events) when the link opens and ≤ 100 ms after each change; the
controller sends its state with the signed host method `projects.sync
{state}` → the host's state when a link opens and ≤ 150 ms after each
change; both merge (the controller notes which machine a node is, for the
`paths` names). Merging is idempotent, so an exchange settles after one
round; hosts without projects answer `unsupported` (ignored). Rights:
`projects.sync` without `state` `observe`, with one `transfer`;
`projects.promote` `transfer`. **The relay sees none of it:** the
published snapshot carries no project ids, names, paths or remotes, and
methods and events travel only sealed.

**Tests:** `internal/projects`: remote normalization (ssh/https/user/
port/host case/.git/file), equal ids across spellings; worktree → its
repository, no git calls once cached (`Resolve`/`Touch`/`List` × 20);
repository without remote; packages for pnpm (globs, `**`, `!`, no
manifest), npm, yarn, turbo, lerna, nx (dist skipped), go.work, Cargo
(multi-line members, comments, exclude) and none; an agent in a package
joins it (also in a worktree), packages listed as detected; deepest
project, scratch, promote (folder, reference, inside a repository, twice,
revive), remove fallbacks (deeper package stays, repository → scratch, no
revival by agents), a home repository is not registered; update, groups,
persistence across a reopen (node id, recent from the old file, 0600);
merge: one repository from two folders/spellings → one id and both
paths, LWW on concurrent renames, independent fields, groups, tombstone
vs an older copy, revive by promote, convergence, order independence with
equal stamps, forged ids dropped; events (initial burst, change, group,
removal, re-resolution). `internal/transport`
`TestProjectsSharedBetweenMacs` (two hesperd through the in-process
relay): a local agent and a remote agent (spawned on M through L) in
clones of the same repository (ssh vs https remote, different folders)
get the same `projectId`; both Macs list one project with both paths; a
rename on L reaches M, a group made on M reaches L, L's subscription sees
`projects.changed`/`groups.changed`; promote on M from L; remove on L →
gone on M, M's agent falls back to scratch (listed on L with M's path);
`projects.recent` project first; projects.json written on both; the tap
never saw the repository, folder, project or group names, the project or
group ids, or a `projects.` method. `pkg/devicekey`: the rights.

**Not done here:** views, desks, the sidebar (steps 2–3, the app); a Mac
reinstalled gets a new node id (its old node's paths stay until the
project is promoted there again); no notifications for scratch.

### As built — shared history (data)

Design: the shared history design notes (version 2: one
shared history across all Macs). Decisions: index everything, full-text
search over prompts and answers, sessions of the desktop apps and IDE
extensions included (marked external), transcripts of the last 14 days
mirrored to every Mac (`historyMirrorDays`), older ones on demand. Code:
`relay/internal/sessions` (parse, scan, db, search, sync, mirror, live,
actions, git, throttle), `relay/pkg/wire/sessions.go` (wire types), hooks
marked "shared history": `internal/agents/sessionhook.go` (the
`Sessions` interface, `SpawnSession`, `watchSessions`; `server.go`
routes `sessions.*`; `registry.go` `forkFrom`), `internal/host/sessions.go`
(host methods, link hints; `service.go` routes them and lets
`agents.export` take a `session:` id), `internal/remote/sessions.go`
(link-up / hint callbacks, `Linked`, `TransferKey`, `Fetch`),
`internal/gateway/history.go` (wiring; `gateway.go` opens it before the
registry, `Config.History`/`NoHistory`), `internal/projects/sessionhook.go`
(`PathOn`), `pkg/agentlink` (`Event.sessions`), `pkg/devicekey`
(rights), `pkg/wire` (`ErrorData.agentId`, `Error.AgentID`, `CodeLive`),
`internal/fakeagent` (`fork`).

**Formats handled** (learned from the real files read-only; the test
fixtures are synthetic):

- Claude Code: `<ClaudeHome>/projects/<folder>/<sessionId>.jsonl`
  (`$CLAUDE_CONFIG_DIR` or `~/.claude`; only the folder's own files, not
  `subagents/`). Lines by `type`: `user` (a prompt when not `isMeta`,
  not `isCompactSummary`, not a sidechain, its content a string or text
  blocks, no `tool_result`, not starting with `<` — commands, hook and
  task notifications, bash input/output — nor `Caveat:` / `[Request
  interrupted`), `assistant` (text blocks are answers; `thinking` is
  not kept; `TodoWrite` input `todos` and `TaskCreate`/`TaskUpdate` are
  the todos; `message.usage` input + cache creation + output, counted
  once per `message.id` — Claude writes one line per block), `custom-title`
  (/rename), `agent-name`, `ai-title`, `summary`, `relocated`
  (`relocatedCwd`); `cwd`, `gitBranch`, `entrypoint` (`cli`,
  `claude-desktop`, `sdk-cli` …) and `timestamp` from the lines. Lines
  starting with a type that carries nothing for the index
  (file-history, queue-operation, mode, permission-mode, atis-latch,
  frame-link, bridge-session, artifact-*, last-prompt, cost-state,
  pr-link, worktree-state) are skipped without decoding. Title:
  custom title, else agent (Remote Control) name, else Claude's own
  title, else a summary, else the first prompt's first line.
- Codex: `<CodexHome>/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl` and
  `.jsonl.zst` (zstd, pure Go `github.com/klauspost/compress/zstd`,
  streamed), plus `<CodexHome>/archived_sessions/` (Codex's archive:
  indexed as archived). The line's `type` and `payload.type` are read
  from its first 320 bytes (Codex writes `timestamp, [ordinal,] type,
  payload`; other key orders are decoded); only `session_meta` (id, cwd,
  originator → origin: `codex-tui`, `Codex Desktop`, `codex_exec`,
  `codex-chrome-extension-sidepanel` …, `git.branch`), `response_item`
  `message` (user `input_text` without the injected context — text
  starting with `<` or `# AGENTS.md` —, assistant `output_text`),
  `function_call` `update_plan` (the plan: todos), `event_msg`
  `token_count` (`total_token_usage`: input − cached + output) and
  `thread_name_updated` are decoded; everything else (tool calls and
  their output, reasoning, `item_completed`, …: most of the 9 GB) is
  skipped after reading its first bytes. Titles: the thread name from
  `<CodexHome>/session_index.jsonl` (newest `updated_at` per id), else the
  first prompt.
- Searchable text: user prompts and assistant answers only (never tool
  output, thinking, hook output): each prompt ≤ 1000 characters, each
  answer ≤ 600, per session ≤ 8 KiB of prompts and ≤ 12 KiB of answers
  (the first prompt kept, the oldest others dropped); `firstPrompt`,
  `lastUser`, `lastAssistant` ≤ 2000 characters; titles ≤ 200; ≤ 50
  todos.

**Index** (`$HESPER_STATE_DIR/history.db`, 0600): SQLite through
`modernc.org/sqlite` (no cgo), WAL, `synchronous=NORMAL`. One writer
goroutine owns the write connection and commits everything queued within
20 ms in one transaction (a savepoint per operation); readers use a pool
of four read-only connections with cached prepared statements, so
searches never wait for the scanner. Tables: `sessions` (one row per
entry: the fields below, every last-writer-wins stamp, a local sequence
number), `fts` (FTS5 over `title, prompts, answers`, `unicode61
remove_diacritics 2`, external content = `sessions`: the text is stored
once), `files` (checkpoints: path, size, mtime, byte offset after the
last complete line, the parse state), `peers` (replication cursors),
`mirror`, `owned` (sessions hesperd started), `kv` (node id, epoch).
Search: filters on indexed columns, `rowid IN (SELECT rowid FROM fts
WHERE fts MATCH ?)` for text (every word, the last as a prefix:
`"flaky" "bad"*`; anything typed is quoted, so no FTS syntax errors),
newest activity first, keyset pagination; the page's rowids are found
first (sorting only `last, rowid`), then their columns without the
text; snippets are made in Go from the page's text (FTS5's `snippet()`
re-tokenized 50 × 20 KiB: 15 ms).

**Scanner.** Incremental: a transcript whose size and mtime match its
checkpoint is not opened; an appended one is read from its offset (a
last line without newline waits); a shorter one (rewritten) is read
again; a `.zst` one is read whole when it changed. The first full index
goes newest first (the last 14 days are searchable first) with two
workers; later passes look at Claude's project folders and Codex's last
three day folders every 2 s and walk everything every 60 s (a full pass
with nothing new: stats only). Vanished transcripts drop their
checkpoint; their entry becomes a tombstone unless the same session was
found elsewhere (Codex's archive moves it). **Throttle** (`throttle.go`;
each worker on its own locked thread): transcripts written within 14
days (read first) at normal priority with a duty cycle — after every
file and every 4 MiB the worker rests as long as it worked, ≤ half a
core each — so recent search works within seconds; everything older,
and everything while one of hesperd's agents is `working`/`starting`, in
macOS's background band (`setpriority(PRIO_DARWIN_THREAD,
PRIO_DARWIN_BG)`: lowest CPU priority, throttled disk I/O; nice 19 on
Linux), resting three times as long as it worked while agents work; the
second worker steps aside then. Closing stops a read within 4 MiB. The
mirror always runs in the background band.

**Project mapping** as agents: `projects.Store.Resolve(cwd)` (deepest
project, worktrees to their repository, identity by git remote;
repositories register like an agent's would), cached per folder,
re-resolved when the projects change.

**Live** (`live: {agentId?, external}`): hesperd's agent with that
`sessionId` runs (`agentId`; an ended hesperd agent means not live);
else Claude's `<ClaudeHome>/sessions/<pid>.json` names it and that pid
lives, or Codex holds `<CodexHome>/thread-writer-locks/<id>.lock` (a
shared non-blocking `flock`, released at once; asked only for sessions
written within a day), or the transcript was written within 2 minutes
and the session is not hesperd's (`external: true`). Re-checked every
pass for live or recently active sessions and on agent changes; the home
replicates it, so another Mac knows. **External** = not started by
hesperd (no agent of this daemon ever had the session id; resuming one
adopts it).

**Wire** (local socket; all methods local, other machines reached through
the gateway). Types in `pkg/wire/sessions.go`:

| Method | Params | Result |
|---|---|---|
| `sessions.search` | `{query?, projectId?, kinds?, machines?, since?, live?, external?, archived?, limit?, cursor?, moved?}` | `{items: [Session], cursor?}` |
| `sessions.show` | `{id}` | `Session` + `changes?: {files: [{path, added, removed}], uncommitted, ahead?, behind?, worktreeExists}` |
| `sessions.resume` | `{id, machine?}` | `Agent` + `note?` |
| `sessions.fork` | `{id, machine?}` | `Agent` + `note?` |
| `sessions.brief` | `{id}` | `{text}` |
| `sessions.continueAs` | `{id, kind, machine?}` | `Agent` |
| `sessions.archive` | `{id, archived}` | `Session` |
| `sessions.delete` | `{id, undo?}` | `{}` |
| `sessions.stats` | – | `{count, byKind, byMachine, indexBytes, mirrorBytes, indexing: {done, total}}` |

`Session = {id: "<machine>:<kind>:<sessionId>", kind, sessionId,
machine (home/owner, this Mac's name for it), cwd, projectId, branch,
title, firstPrompt, lastUser, lastAssistant, todos: [{text, done}],
turns, tokens?, startedAt, lastActivity (RFC 3339), live?: {agentId?,
external}, external, archived, mirrored: [machine] (the home first),
snippet?}`. **Additive refinements:** `origin` (entrypoint / originator),
`removedAt` (the hesperd agent running it was removed; ghost cards),
`movedTo` (continued on that machine; this entry is read-only history),
`bytes` (transcript size on the home); `search.moved` (default false:
moved entries are left out); `search.limit` default and maximum 50;
`search.archived` omitted/false lists the others, true only archived;
deleted sessions are never listed; `delete.undo` takes a delete back
within 30 s ("too late" `invalid` after the file is gone); the resume
result's `note` says what did not come along (also the agent's
`summary`); snippets mark matches with `[` `]`; turns = real prompts;
tokens = input (without cache reads) + output. Errors: `not_found`,
`invalid`, `unavailable` and **`live`**: `sessions.resume` (and
`sessions.delete`) of a running session answers code `live` with
`data.agentId` (in this Mac's naming) when hesperd runs it — the app
opens that agent instead; a fork is allowed while it runs.

Notifications on `agents.subscribe` connections (own goroutine,
interleaved): `sessions.indexing {done, total}` first and while a pass
with work runs (≤ 2/s), `sessions.changed {session}` coalesced per
session and sent ≤ 250 ms after a change — for sessions active within 3
days, and every archive / delete / move / removal (the first full index
of old transcripts announces nothing: search instead), and additive
`sessions.removed {id}` for a delete.

**Resume, fork, move, continue.**

- `sessions.resume {id}` (no machine): on the home, in its `cwd`
  (`claude --resume <id>`, `codex resume … <id>`, the task never sent);
  a missing worktree is recreated from its branch (`git worktree add`,
  the local branch, else `origin/<branch>` fetched) in the project's
  repository on that Mac (`projects` `PathOn`). From another Mac it is
  forwarded to the home (host method `sessions.resume`), the agent comes
  back in this Mac's naming (`M/x7…`).
- `sessions.resume {id, machine}` with another machine than the home:
  the session **moves**. On the target: with the home reachable, the
  home packs it like `agents.move` (`sessions.plan` for the commits, the
  target probes where the project is here — its project's folder, else
  the path mapped —; without it and with a remote, the bundle is
  incremental from the remote's head and the target clones;
  `agents.export {id: "session:<key>", compress: true}` sealed download:
  branch, base, uncommitted work incl. untracked files, the transcript
  as `transcript.jsonl.zst` — archived and `.zst` rollouts read as they
  are) and the target unpacks it (`PlaceSession`: the project cloned
  from its remote when missing, else made from a full bundle,
  uncommitted changes restored, transcript placed with every `cwd` mapped
  to this home) and resumes; with the home away, the transcript comes
  from this Mac's mirror and the folder is the session's folder mapped
  to this home, else a worktree of the project here on its branch as
  pushed — the result's `note` says so. Then the old entry gets
  `movedTo` (replicated); resuming it goes to the new home's entry
  (which this Mac indexes from the placed transcript and owns). Live
  sessions never move (`live`). A home that is reachable but refuses
  (conflicts, live, too large) is the answer: only an unreachable home,
  or a transfer that stopped moving, falls back to the mirror (the
  note says which). When the mirror cannot serve either (no copy, no
  folder), the error names both reasons: "could not get the session
  from laptop: <why>; this Mac's copy: <why>" (also in hesperd's log).
- Between Macs a resume or fork runs as the target's own operation
  (it outlasts the relay's 20 s per request): the caller sends
  `{key, poll: true}`, the target answers the result or `{pending,
  steps, progress}`, and the caller asks `{op, seen}` until the result.
  It tells its subscribers `agents.moving {id: <session id>, session:
  true, to, fork, step, percent?, bytes?, total?}` (checkpoint,
  transfer, worktree, resume, done or failed); hesperctl prints them on
  stderr. A transfer fails only when no chunk got through for a minute
  (chunks lost on the way are asked again from the same offset); the
  whole is bounded at 2 hours.
- `sessions.fork`: `claude --resume <id> --fork-session --session-id
  <new>`, `codex fork [options] <id>`, on the home or brought to
  `machine` the same way (the original stays where it is).
- `sessions.brief` (no model call): task (first prompt), the prompts in
  between (≤ 12, "decisions and changes of direction"), last request,
  last answer (last state), todos, changed files (git, on the home),
  where the transcript is. `sessions.continueAs {id, kind}` starts that
  kind in the session's folder (mapped; else the project's folder) with
  the brief as its first prompt, on the home or `machine`.
- `sessions.archive`: a replicated field. `sessions.delete`: a tombstone
  now (every Mac hides it); the home removes the transcript 30 s later
  if not undone (Claude: the file; Codex: moved to
  `<CodexHome>/archived_sessions/`, Codex's own archive).
- Ghost cards: when an agent with a session is removed, its entry gets
  `removedAt` (and is not live).

**Shared history between Macs** (`sync.go`). Every Mac indexes its own
transcripts; entries are replicated by **pull**: each Mac is a controller
of every other (part R), so pulling both ways is enough. An entry's
fields are last-writer-wins registers with hybrid logical clock stamps
(as projects): `meta` (everything read from the transcript, incl. the
searchable text and live; written only by the home), `archived`,
`deleted` (tombstone), `movedTo`, `removedAt`, and one `mirrors` mark per
node; equal stamps compare values, so merges converge in any order.
`sessions.pull {since, epoch, from}` (host method, signed, right
`observe`) answers the entries changed after `since` (a per-database
sequence number; a new database = new epoch = from 0), skipping entries
whose every stamp is the requester's (no echo), ≤ 400 entries and ≤ 600
KiB of zstd-compressed JSON per answer, `{node, short, epoch, next, more,
data}`. The puller asks again while `more`, one request at a time per
Mac, pausing 20 ms + 1 s per 8 MiB received (rate limit), and stores the
cursor (`peers`), so an interrupted sync resumes. Pulls start when a
link opens and when the host's **hint** arrives on its link
(`agentlink.Event{sessions: {node, seq}}`, sealed like agent events,
sent on open and ≤ 250 ms after changes). Requests travel signed inside
the end-to-end channel, not on the link's attach channels. Each Mac
names other nodes by the machine it pulled them from (else their own
short name). Search, show and brief work offline on every Mac.

**Transcript mirror** (`mirror.go`). After a pull, each Mac brings its
copies of that machine's sessions active within `historyMirrorDays`
(settings.json, default 14) up to date, newest first:
`$HESPER_STATE_DIR/history/mirror/<machine>/<kind>/<id>.jsonl.zst`, a
sequence of zstd frames, one per chunk. `sessions.transcript {key,
offset, epk, download}` (host, `observe`) answers ≤ 2 MiB of the
transcript from `offset`, zstd-compressed and sealed with `pkg/transfer`
(`NewHostDownload`: only the requester's ephemeral key opens it; the host
key is the pinned transfer key) — resumable from the size it has,
nothing fetched twice, a transcript whose version (size + mtime) did not
change is not asked for, one that shrank starts over. A complete copy
sets this node's `mirrors` mark (so every Mac shows `mirrored`). The
mirror runs in the background band and waits while agents work. Copies
of sessions quiet for longer than the window + 2 days, deleted or moved
are dropped. `sessions.stats.mirrorBytes` is their total.

**Host methods and rights** (`pkg/devicekey`): `sessions.pull`,
`sessions.transcript`, `sessions.plan`, `sessions.changes` observe;
`sessions.resume`, `sessions.fork`, `sessions.continueAs` (with `{key,
kind?}`: start it on this Mac) transfer; `agents.export` with a
`session:<key>` id observe (as for agents). Entries are named by their
key `<node>:<kind>:<sessionId>` between Macs. **The relay sees none of
it**: the published snapshot is unchanged; methods, entries, hints and
transcript chunks travel only sealed (the transport test's tap never
sees a title, prompt, answer, folder, file name, session id or a
`sessions.` method).

**Tests.** `internal/sessions` (synthetic fixtures `testdata/claude.jsonl`,
`codex.jsonl` in the real shapes): both parsers (titles by precedence,
prompts vs meta/commands/tool results/sidechains, tokens once per
message, TodoWrite, TaskCreate/TaskUpdate, update_plan, nothing of tool
output/thinking/hook output searchable), `.zst` + thread names, the
checkpoint (a partial last line waits; an append reads exactly the new
bytes; a restart reads nothing; a rewrite starts over), FTS (snippet,
prefix, title, tool output not found, kinds, machines, since, external,
live, pages, FTS syntax as text), project mapping, live (Claude pid
file, Codex flock, recent write), resume on the home (`--resume`, no
prompt), never resume live (`live` + agentId, also delete), owned → not
external, fork (claude `--fork-session --session-id`, `codex fork`),
brief, continueAs, removedAt, a missing worktree recreated from its
branch + `show.changes`, archive/delete/undo (Claude file removed, Codex
rollout moved to its archive), LWW convergence and tombstones, bad ids,
the budgets (`TestSearchBudget`, `TestIncrementalBudget`).
`internal/transport` `TestSessionsSharedHistory` (two hesperd through the
in-process relay): every entry on both Macs, search on L for M's text,
the mirror (recent yes, 30 days old no; M sees L's mirror), archive
L → M, resume on the home from L, `live` with the agent id afterwards,
a Codex session moved to L with its uncommitted (untracked) file and
the conversation (L indexes and owns it, M's entry `movedTo` L on both
Macs, resuming the old entry hits the live new one), delete → M removes
the transcript, offline search after M stops, resume on L from the
mirror with M away (note, transcript placed with mapped cwd), the tap
sees nothing. `TestSessionsReplicateThousands`: 2,000 sessions.

**Measured** (M1 Max, shared with other agents' builds: load average 55–90
during these runs; `TestRealHistoryBenchmark` with `HESPER_HISTORY_BENCH=1`
indexes the real `~/.claude` and `~/.codex` read-only into a temp index;
keystrokes = echo through a PTY under `ptyhost` in the same process):

| | |
|---|---|
| Real data | 1,594 transcripts, 9.36 GB (1,537 Codex rollouts, 57 Claude sessions); 155 of the last 14 days |
| First full index, unthrottled (normal priority, warm page cache, load ≈ 60) | **16.4 s** (580 MB/s; the last 14 days after 3.2 s); keystroke p50 0.10 ms, but p99 4.1 ms vs 0.27 ms idle — hence the throttle |
| First full index as shipped (load ≈ 90) | the last 14 days searchable after **46 s**; the older history then fills in in the background band at what the kernel leaves over (0.4–5 MB/s at load 55–90; the whole 9 GB when the Mac is quieter); keystroke p99 **3.73 ms during vs 3.59 ms before** (unchanged at that load), 4.26 ms while in the background band (noise of the load) |
| Everything in the background band (as while agents work, load ≈ 56) | 0.4 MB/s; keystroke p99 1.69 ms during vs 3.53 ms before (unchanged) |
| Full pass with nothing new | 15 ms (1,594 stats) |
| Incremental: append to a 12 MB transcript | p50 **0.43 ms**, p99 1.2 ms, reading only the new bytes |
| Index size | 30.0 MB for the 1,594 real sessions (**18.8 MB per 1,000**); 12.4–14.4 MB per 1,000 for synthetic ones at the text caps |
| Search over 2,100 sessions (real + synthetic replicated), in the daemon | p50 **1.5 ms**, p99 **4.8 ms** (budget 5 / 20 ms); 2,000 synthetic sessions where every query word is in every session (the worst case): p50 2.9 ms, p99 8.3 ms |
| `sessions.show` without git | p50 **32 µs**, p99 140 µs; with the cached git state p50 73 µs, p99 227 µs |
| Replication of 2,000 entries M → L (in-process relay) | **1.2 s** after M indexed them (1.0 s); 24.7 MB index on L |

Budgets checked in every run (logged only under the race detector):
`TestSearchBudget` (2,000 sessions: p50 ≤ 5 ms, p99 ≤ 20 ms; show ≤ 2 ms),
`TestIncrementalBudget` (≤ 10 ms).

**Not done here:** the app (see "As built — shared history (app)");
"Summarize" with a model; Claude's subagent transcripts are not indexed;
`movedTo` makes the home's copy read-only history but its file stays;
a session continued on two Macs at once while they could not see each
other (offline) is two sessions afterwards (LWW keeps one `movedTo`).


### As built — shared history (app)

Design: the shared history design notes (version 2).
Data: "As built — shared history (data)" above. Code: pure rules in
`HesperCore/Sessions.swift` (wire types decoded by hand from `JSONValue`,
lenient; `HistoryQuery` → `sessions.search` params, chips, `HistoryList`
paging/merging, `UpdateCoalescer`, `GhostCards`), `SessionFormat.swift`
(row and card text, `[match]` highlights, `HistoryKeys`),
`HistorySidebar.swift`, `SessionsAPI.swift` (`DaemonClient` methods); the
app in `Sources/Hesper/History/` (`HistoryHub`, `HistoryPanel`,
`SessionCardPanel` + `SessionCard` (SwiftUI: the brief editor and the
tile's first screen), `HistoryActions`, `GhostCard`, `AppModel+History`,
`HistorySelfTest`, `HistoryPerf`).

**Status: built and unit-tested headless only — the UI suites have NOT
been run against this code yet** (the Mac was in use; no app window was
opened). Verified: `swift build -c release`, `swift test` (HesperCore,
193 tests incl. 22 `SessionTests`), fake-hesperd `go vet` / build, relay
`go vet` / `go test -race ./...`. The UI checks and the performance
table below come from the interrupted earlier iteration and must be
re-measured. **To run before shipping**, in this order: `make -C app
test-ui-history` (fake daemon, then `--no-sessions`), `make -C app
perf-history` (the budgets below, both daemons), `make -C app perf`
(the wall unchanged, also with `--sessions 3000`), `make -C app
test-ui-real` (incl. `run-history.sh real-test` against the real hesperd
over synthetic transcripts), and the regression suites the shared files
touch: `make -C app test-ui test-ui-windows test-ui-projects
test-ui-desks` (or all of them: `make -C app test`).

**History is a panel over the wall, not a wall mode.** ⌘Y (Agents ▸
History, the project sidebar's "Show History (⌘Y)" per project, ⇥ on a
⌘K History row) opens it on the window's content area: the overlay
family's look (#1f2335, #3b4261 border, 14 pt radius, shadow, scrim 38 %,
open 120 ms scale 0.98 → 1 + fade, close 80 ms, Reduce Motion: none) and
keys. Chosen over a dedicated wall mode because it is calmer: nothing on
the wall changes — no mode switch, no tile hidden or re-attached, no
relayout, selection and active tile kept — and esc returns at once to
exactly the wall that was there (a mode would stop and restart every
tile's rendering both ways). The tiles keep rendering underneath (wall
numbers below); toasts and undo stay above the panel; a click on the
scrim closes; state (query, filters, list, selection) survives closing.

Layout: left the project sidebar (the same `SidebarRowContent` rows as
⌘0) with session counts: All sessions, groups → projects (a package's
sessions count for its repository), other projects, and "Scratch" /
"Elsewhere (folder gone)" rows when the daemon counts them; top the
search field (live FTS as you type, 60 ms debounce) and chips (this
project — the selected card's or the wall's —, all projects, Claude,
Codex, one per Mac, time any/24 h/7 days/30 days, live in Hesper,
external, archived, moved to another Mac); the list (kind badge, title,
Mac · branch · turns · tokens, the FTS snippet with its `[matches]`
highlighted or the last exchange, when and state: ended / ● live in
Hesper / ● running outside / external / moved to M / mirrored on N
Macs); right the "where you left off" card; the footer: count, the first
index's progress (`sessions.indexing`, also from `stats.indexing`) and
the keys.

**The card** renders at once from the row's fields (title, Kind on Mac ·
folder, you asked, it answered, open todos, branch) and fills the git
line from `sessions.show` 120 ms after the selection settles (changed
files +/−, uncommitted, ahead/behind, folder on M / folder gone) into a
slot reserved for it: nothing moves. Actions with keys: **⏎** Resume (on
its Mac; "Open (live)" when hesperd runs it → that agent's tile or
window; "Open on M" for a moved entry, which hesperd resumes on the new
home; running outside Hesper → a toast), **⌥⏎** Resume here ("continues
on laptop, moves ownership; uncommitted work comes along if mini is
reachable"; `sessions.resume {id, machine: this Mac}`), **F** Fork,
**C** Continue in Claude/Codex (the card turns into the brief from
`sessions.brief`, editable, the other kind preselected; ⌘⏎ starts:
unedited → `sessions.continueAs {id, kind}`; edited → `agents.spawn`
of that kind in the session's folder on its Mac with the edited text,
since continueAs takes no text; esc back), **A** Archive / Unarchive,
**⌫** Delete (gone from the list at once, the undo toast; ⌘Z or Undo →
`sessions.delete {id, undo: true}`, then `sessions.show` puts it back in
its place), **⌘C** copy the session id (the CLI's). A result's `note`
shows as a toast. A resumed / forked agent joins its project's band
(band and draft rules unchanged), is selected, and its tile shows the
card as its first screen (`SessionCardOverlay`) until the CLI has drawn
two lines (at least 0.8 s, at most 12 s, typing into it hides it).

**Keys** (`HistoryKeys`, the overlay family's): ↑↓ select (also from the
search field), ⌘↑ ⌘↓ / Home End first / last, page up/down, ⏎ resume,
⌥⏎ here, ⇥ the list takes the keys (then F C A ⌫ act; typing any other
character goes back to the search with it), ⇧⇥ / ⌘F the search, ⌘C copy
id (the field's own copy when text is selected), esc / ⌘Y close; ⌘Z, ⌘K,
⌘W… stay the window's. A toast never takes the keyboard from the panel.

**Ghost cards.** A removed agent (⌘W on an ended one) becomes a dashed
card in its place on its band's shelf at once (its undo window: ⏎ =
undo the removal), then the daemon's entry with `removedAt` keeps it
there for 24 h; newest 3 per project; Grid + Shelf only (the other
layouts stay strictly agents); scoped like its project. ⏎ / double-click
Resume (`sessions.resume`), ⌫ / Forget hides it (remembered per Mac);
Settings › General › "Ghost cards for removed agents" turns them off.
The ghost swaps with the agent in the same slot, so removing one moves
nothing else. Sources: `sessions.search {since: −24 h}` pages filtered by
`removedAt`, then `sessions.changed`.

**⌘K.** A "History" section (after Projects) with up to 8 sessions
matching the query by title and full text (`sessions.search {query,
limit: 8}`, 60 ms debounce, off the main thread, never in the palette's
body; rows are not filtered again by the palette's fuzzy match): ⏎
resumes (live: opens), ⌘⏎ / ⇥ shows its card in History.

**Degrades.** `sessions.stats` at every connect decides: -32601 → no
History (menu item hidden, ⌘Y beeps, no ⌘K section, no ghost cards, no
"Show History"); unknown while disconnected.

**Wire use and assumptions** (built against `pkg/wire/sessions.go`;
the refinements `origin`, `removedAt`, `movedTo`, `bytes` decoded —
`origin` other than the plain CLI/TUI shows in the card's subtitle,
"Claude on mini (desktop app)", `bytes` is kept but not shown; empty
`projectId` / `branch` read as none; `stats.indexing` `{0, 0}` = idle):
`sessions.search` (limit default 50, clamped to 1…50 by the app, keyset
cursor, `moved` only with the chip;
`projectId` exact; the fake also answers the sentinels `~scratch` /
`~elsewhere`), `sessions.show` (Session + `changes`), `sessions.stats`
(`count`; `byProject`, `scratch`, `elsewhere` used when present),
`sessions.resume/fork` (Agent + `note`, shown as a toast; error `live` +
`data.agentId` → open that agent, no `agentId` → "running outside
Hesper" toast: `SessionStart.live(from:)`; `RPCError.data` keeps the
whole `error.data`),
`sessions.brief`, `sessions.continueAs`, `sessions.archive`,
`sessions.delete {id, undo?}` (`live` → toast, row back, and its agent
opens when `data.agentId` names one),
notifications `sessions.changed {session}` / `sessions.removed {id}` /
`sessions.indexing {done,total}` decoded on the connection's reader
thread (`DaemonClient.onOtherNotification`) and handed to the main
thread at most 4 times a second (`EventBuffer` + `UpdateCoalescer`),
merged into the list in one pass (`HistoryList.merge`: in place; a
session not shown joins only when newer than the top row, so a busy
daemon never grows or reorders the list under the reader). **Needs
(part D):** per-project counts in `sessions.stats` (`byProject`, plus
scratch / folder-gone counts and a search filter for them) — without
them the sidebar lists projects without numbers and hides Scratch /
Elsewhere; `sessions.continueAs` taking an edited `brief`.

**Performance** (the whole path off the main thread: the call, decoding
by hand, row text with cached formatters; the list a virtualized
`NSTableView` with fixed 74 pt rows whose cells draw their strings
directly (no subviews, no SwiftUI), paging 60 rows ahead of the scroll
position; the card plain AppKit labels at fixed slots — the SwiftUI
brief editor exists only while continuing, since a hidden hosting view
still re-rendered on every selection; sidebar rows re-render only when
their selection changes). `make -C app perf-history` (M1 Max, 120 Hz,
shared machine at load 15–65):

| | fake daemon, 2,840 sessions, 16 agents | real hesperd, 2,000 synthetic transcripts |
|---|---|---|
| ⌘Y → first rows drawn: cold / warm (cached rows) / warm, fresh page | 70 / 3.7 / 37 ms | 58 / 7.3 / 41 ms |
| daemon answer → rows on screen (apply a page) | p50 0.83 ms, max 0.98 | p50 0.89 ms, max 1.08 |
| last keystroke → rows (incl. the 60 ms debounce) | p50 79 ms | p50 79 ms |
| one keystroke in the field | p50 1.7 ms | p50 1.1 ms |
| selection change / card update + layout (our code, worst) | 0.25 / 1.98 ms | 4.1 / 9.0 ms (first card) |
| scrolling 2 rows a frame for 8 s (to row 2,000, paging) | 119.7 fps, 3 dropped | 119.9 fps, 0 dropped |
| 400 sessions events/s for 5 s | ≤ 4 UI updates/s, main-thread passes p99 2.3 ms | (no flood on the real daemon) |
| ⌘K History results after the debounce | p50 4.7 ms | p50 4.4 ms |
| main-thread passes while typing / ⌘K typing (with / without History) | p99 14 / 16 vs 18 ms | p99 14 / 14 vs 21 ms |

Main-thread passes on this shared machine (wall alone p99 0.5–1.5 ms)
include AppKit's text system and display commits; History's own work
per update stays ≤ 2 ms (≤ 9 ms for the very first card). **The wall is
unchanged** (`make perf`, 16 flood tiles, `--sessions 3000`):
History closed 120.0 fps, 0 dropped, tiles 117 fps, keystroke → frame
p50 2.96 ms; History open with 200 `sessions.changed` + indexing events
a second streaming the whole run: 0 dropped (60 Hz display: 60.0 fps,
tiles 59.9 fps, two runs), keystroke → grid p50 2.4 ms, state → ring
p50 13 ms, no extra attach. (Before `HistoryList.merge` every event of a
session not shown was inserted with a full reindex: 160–210 dropped
frames under the same stream — the reason for the merge rule.)

**Integration points in shared files** (marked "shared history"):
`AppModel` (`WallItem.ghost`, `scopedWallItems`, `unscopedWallItems` →
`withGhosts`, a selected ghost keeps the selection, `.history`),
`AppModel+Projects` (ghost project / view item), `AppModel+Overlays`
(`UndoAction.restoreSession`, the palette's History section, its
filtering and order, ⇥), `KeyRouter` (⌘Y), `MainWindow` (the panel's
keys first; `RootView` attaches it under the overlays, `historyFrame`,
toasts don't take its keyboard), `WallView` (`ghostTiles`, ghost input /
placement / selection / keys, the resume card on a new tile),
`ProjectSidebar` (Show History), `AppSettings` / `SettingsView` (ghost
cards), `AppDelegate` (menu item, harnesses), `AppEnvironment` (flags),
`DaemonClient` (`onOtherNotification`), `RPCConnection` (`RPCError.data`),
`Harness` (`--perf-history`), `run-with-fake.sh` (`PERF_HISTORY`).

**fake-hesperd** (test-only, `Tools/fake-hesperd/sessions.go`): every
method above, `--sessions N` (demo projects, both kinds, both Macs,
external, archived, mirrored, moved, live per seeded agent),
`--indexing-ms`, `--no-sessions` (-32601), the real shapes (`projectId`
/ `branch` always sent, `origin`, `bytes`, `stats` with `count`,
`indexBytes`, `mirrorBytes` and `indexing` always present — plus
`byProject` / `scratch` / `elsewhere`, which the real daemon does not
send yet: the "Needs" above), removed agents leave a
`removedAt` session, resume to another Mac returns a `note`, snippets
`[ ]`, `fake.sessionCalls` (the call log), `fake.sessionsFlood`.

**Tests.** HesperCore `SessionTests` (22): decoding (wire shape, the
real daemon's shape with `origin` / `removedAt` / `movedTo` / `bytes`
and its stats, lenient, show/stats incl. `count`), `[match]` highlight
parsing (several, non-ASCII), cursor paging (limit clamp, pages without
duplicates, delete + undo back in place), ghost expiry at 24 h, `live`
error routing (agent / outside / other errors), search params (limit 50, sentinels,
since, moved), chips, paging and `merge`, query matching, the
coalescer (≤ 5 flushes for 1,000 events in a second), row and snippet
formatting (`[ ]`, `<b>`), `when`, the card (here / there / live /
moved / folder gone, the ⌥⏎ note), ghost expiry and per-project cap,
pending ghosts, key routing, the sidebar, `RPCError.data`. UI `make -C
app test-ui-history` (fake daemon, 54 + 6 checks; **not run yet**, see
Status): ⌘Y with rows, focus, the
first index's progress, this project, the wall untouched; sidebar counts
and Scratch / Elsewhere / project filters; every chip; cursor paging;
debounced FTS with highlights (≤ 3 searches for a word); the card at
once and its git line later; ⌘C (private pasteboard); F → `sessions.fork`;
⌥⏎ → `sessions.resume {machine: L}` with its note and the card as the
tile's first screen until the CLI drew; ⏎ on a live session opens its
agent (no call); C → `sessions.brief`, ⌘⏎ → `sessions.continueAs`, an
edited brief → a Codex agent with that text; A → `sessions.archive`;
⌫ → `sessions.delete` + toast, ⌘Z → `{undo: true}` and back; ⏎ →
`sessions.resume`; ghost card in the removed agent's shelf slot, kept
after the undo window, ⏎ resumes, ⌫ forgets, the setting hides them;
⌘K History rows, ⇥ shows the card, ⏎ resumes; then against
`--no-sessions`: menu hidden, ⌘Y and ⌘K without History, no ghost.
`make -C app test-ui-real` now also runs `run-history.sh real-test`
(the real hesperd over 2,000 synthetic transcripts from
`Tools/history-fixtures.py` in temp `CLAUDE_CONFIG_DIR` / `CODEX_HOME`,
every profile the fake TUI; 12 checks: index, pages of 50, FTS
highlights, card + `sessions.show`, ⏎ resumes through hesperd with the
session's id and the tile card, live → opens, archive, delete + undo,
⌘K) after the 73 + 7 selftest-real checks. Screenshots (`SHOTS=dir`):
history-panel, history-search, history-card, history-continue,
history-undo, resume-card, ghost-cards, palette-history, real-search,
real-card.

**Not done here:** "Summarize" with a model; per-project counts and
Scratch / Elsewhere on the real daemon (need above); ghost cards in the
other layouts; a moved session's "open on M" relies on hesperd routing
the resume to the new home.

### As built — closing agents (daemon: internal/agents/close.go, pkg/wire)

An agent is on a wall, in the background (running, hidden from every
wall), or closed (gone from the registry; its session stays in the
shared history and comes back with `sessions.resume`). The daemon side:

| Method | Params | Result / effect |
|---|---|---|
| `agents.close` | `{id}` | `{session?}` (the agent's session id when it has one). Ended agent: removed before the answer. Running agent: answered at once, then the tool's interrupt (Claude Code / Codex: Esc, only while working, in an approval or a question; shell: ^C), at least 200 ms and at most 5 s (`Options.CloseWait`) for it to leave `working`, then the hangup `agents.stop` sends (SIGHUP, SIGKILL after the stop grace); once the process ended it leaves the registry. Both: `agents.removed {id, reason: "closed"}`. A second close while one runs answers the same. |
| `agents.kill` | `{id}` | `{}`. SIGTERM to the process group, SIGKILL after 3 s (`Options.KillGrace`), also while a stop is under way. The agent stays: `state: "exited"`, `ended: "killed"`, exit signal SIGTERM. An ended agent: nothing (a "Not resumed" one stays down). `ended` clears when it runs again (resume). |
| `agents.background` | `{id, background}` | `{}`. Sets `Agent.background`; `agents.changed` follows. Never touches the process. |

- `Agent` gains `background` (bool, omitempty; persisted in agents.json
  as part of the agent, so it survives daemon restarts; in
  `agents.list` and `agents.changed`) and `ended` (omitempty:
  `"killed"`).
- `agents.removed` gains `reason` (omitempty): `"closed"`,
  `"finished-in-background"`, `"removed"` (agents.remove, locally and
  for remote agents). Removals the daemon makes on its own (a remote
  machine dropped after its grace, a machine renamed) carry none.
- **Finished in the background:** a background agent whose turn ends
  (from `working`, `approval` or `question` to `done` or `idle`) is
  hung up and leaves with reason `"finished-in-background"`; so does one
  whose process ends with status 0 or by a stop. Not: a start that comes
  up idle (resume, the respawn after a daemon restart — the background
  agent stays), a killed one (stays, killed), one that failed (stays as
  `error` for the app to surface), approvals and questions (normal
  attention).
- **Offline:** `agents.close`, `agents.kill` and `agents.background` for
  an agent of another Mac go to its daemon like the other agents.*
  calls (gateway → host `internal/host/agents.go`, device right
  `transfer`). When that Mac cannot be reached (no remote connection,
  machine offline, link lost: "unavailable"), they fail with JSON-RPC
  error **-32010** (`wire.RPCOffline`), `data.code: "offline"`, message
  `machine offline: …`. Other methods keep -32000 / `unavailable`.
  The host's removal reason travels in the link's event
  (`agentlink.Event.reason`) to the controller's subscribers.
- **Resume after close:** nothing extra is kept: the session (machine,
  cwd, kind, session id) is in the shared history; the agent's removal
  sets the session's `removedAt` (existing) — that is the "closed at"
  (no separate `closedAt`). `sessions.resume` of it starts a new agent
  with `--resume <session>` (test `TestResumeAfterClose`).
- An agent being closed when the daemon stops (`closing` in
  agents.json) is not restored on the next start.
- `agents.stop` and `agents.remove` are unchanged (remove now also says
  `reason: "removed"`).

Tests: internal/agents/close_test.go (close running — Esc reaches the
tool, removed "closed", gone from agents.json; close of a shell; close
of an ended agent; kill keeps it exited/killed and resume clears it;
background in agents.changed/list, persisted across a daemon restart,
process untouched; background turn end / exit → "finished-in-background",
killed and failed background agents stay; -32010 `offline` for another
machine; remove reason; a closing agent not restored),
internal/sessions `TestResumeAfterClose`, internal/transport
`TestRemoteCloseKillBackground` (background, kill, close of running and
ended agents on M through L, reasons on L's subscription, offline after
M stops).

**Deviations from the shared contract:** close ends a running agent
with the hangup `agents.stop` uses (SIGHUP, then SIGKILL after the stop
grace), not SIGTERM — SIGHUP is how Claude Code saves and exits cleanly;
`agents.kill` uses SIGTERM → SIGKILL 3 s as specified. `agents.close`
answers before a running agent has ended (the removal notification says
when it is gone), so a slow tool never holds a remote host's serialized
operations. A failed (`error`) background agent is not closed.

### As built — agent tree (internal/agents/tree.go, pkg/wire, hesperctl tree.go)

Agents start agents: `hesperctl new` run inside an agent makes a child.
hesperd records the tree and lets an agent steer only what it started,
so an agent can orchestrate others without being able to approve its
own actions or touch the person's other agents.

**Wire.** `Agent` gains `parent` (the starting agent's full id; empty
for an agent a person started), `depth` (0 for a person's agent, parent's
+ 1) and `letParentAnswer` (bool). All omitempty, in `agents.list` and
`agents.changed`, persisted in agents.json as part of the agent (so they
survive daemon restarts and respawns), carried by moves (handoff
manifest `agent.parent/depth/letParentAnswer`; after `agents.move` the
controller re-points its local children at the moved agent's new id).
`agents.spawn` gains `letParentAnswer` (ignored without a parent) and
`caller`. Every `agents.*` call may carry `caller` (the calling agent's
id; `wire.Client.Caller` adds it, hesperctl sets it from
`HESPER_AGENT_ID`, completed with `HESPER_MACHINE`). hesperd drops
`caller` before forwarding to another machine (hosts decode strictly);
for a remote spawn it sends `parent` and `depth` instead, which a host
takes from an approved controller, and which hesperd ignores from its
local socket. New method `agents.result {id}` →
`{id, state, message, summary, at}`: the final message of the agent's
last turn (Claude's Stop `last_assistant_message`, else the transcript's
last assistant text; Codex notify `last-assistant-message`; kept up to
64 KiB, persisted as `lastMessage` in agents.json), and its summary.
Forwarded to hosts (device right `observe`); a host without it answers
with the summary from the controller's list.

**Who calls.** The daemon takes the socket peer's PID (macOS
`LOCAL_PEERPID`, Linux `SO_PEERCRED`) and walks its parents
(`kern.proc.pid` sysctl, `/proc/PID/stat`); the nearest running agent
process in that chain is the caller (verified). Without one it takes
`caller` from the params (advisory). Both missing: a person (the app,
hesperctl in an ordinary terminal). A verified caller beats a claimed
one (a mismatch is audited as `agent.caller-mismatch`). A caller agent
hesperd does not know is refused (`forbidden`). A person's own shell
agent (kind shell, no parent) acts as the person; a shell an agent
started is that agent's child. **This boundary stops accidents and
prompt-injection chains, not a hostile process of the same user**: such
a process can leave the agent's process tree (double fork, reparented to
launchd) and unset `HESPER_AGENT_ID`, and is then a person.

**Policy** (caller an agent; a person may do everything as before):

| Method | Allowed when |
|---|---|
| read-only (`agents.list`, `agents.result`, `agents.subscribe`, ro attach, `hello`, …) | always |
| `agents.spawn` | the child's depth (caller's + 1) ≤ `maxAgentDepth` (default 3) and the caller has < `maxAgentChildren` (default 8) live children (not exited; spawns under way count) |
| `agents.answer`; `agents.input` to an agent in `approval` or `question` | the target is the caller's **own child** (not a grandchild, not itself) started with `letParentAnswer` |
| `agents.input` (otherwise), `stop`, `resume`, `remove`, `rename`, `move`, `close`, `kill`, `background`; rw attach (verified callers only) | the target is a strict **descendant** of the caller (never itself, never its parent or another agent's) |

Refusals are JSON-RPC errors with `data.code: "forbidden"` (hesperctl
exit 5) and a message naming the rule. Limits come from settings.json in
the config directory (`maxAgentDepth`, `maxAgentChildren`; 0 means agents
start none; read at daemon start). For remote targets the controller
decides from its view of the other machine's agents before forwarding.

**Closing a parent** (agents.close, a background agent that finished):
its children on this machine get its parent (the grandparent still
steers them; a closed root's children become roots), depths are
recomputed for the whole subtree, `letParentAnswer` is cleared (the new
parent never chose to answer for them); `agents.changed` for each,
persisted. `agents.remove` does not re-parent (a move removes the agent
it moved; `Reparent` re-points the children at the moved one), and a
child on another machine keeps the closed parent (it is then a root in
`ls --tree`, steerable by a person only).

**Session starts and uploads.** `sessions.resume`, `sessions.fork` and
`sessions.continueAs` called by an agent start its child like
`agents.spawn`: the same limits (a slot reserved while it starts),
parent the caller, depth + 1, `letParentAnswer` off, audited
(`agent.request` with the new agent as target, `agent.refused`). hesperd
adds `parent`/`depth` to the params it hands the shared history (and
drops any a client sent); the history passes them to `SpawnSession` /
`Spawn`, or to the host that starts it (`hostParams.parent/depth`, taken
from an approved controller; an older host ignores them and the agent
has no parent). `files.put` with an `agent` from an agent caller needs a
strict descendant; `files.chunk` of an upload for an agent too (hesperd
remembers each upload's agent from `files.put`, an hour at most).
Uploads for a machine or a draft are not restricted. `wire.Client`
sends `caller` on these methods too (`wire.CarriesCaller`).

**Audit.** Every call of an agent to `agents.spawn` or a method above
appends a JSON line to `audit.log` in the state directory (the host's
file and format, plus `agent`, `target`, `route: "local"`): event
`agent.request` (ok and the error) or `agent.refused` (the policy's
reason). A person's local calls are not audited.

**CLI.** `new --let-parent-answer`; `new --wait [--timeout D]` waits
(subscription) until the child has settled (done, idle, exited, approval,
question, error: `wait --until settled`), then prints its result (or
what it waits for); without `--json` the id first (at once), then the
result; `--json` prints `{agent, result}`; error exits 1, timeout 6.
`ls --tree` indents children under parents; `ls --children [ID]` lists
ID's children (default: the agent hesperctl runs in). `result ID [--json]`
prints the last final message, else the summary (nothing yet: exit 0,
empty stdout, `message: null` in JSON). **Which daemon hears the
caller:** hesperctl sends `caller` (HESPER_AGENT_ID) only to the agent's
own hesperd: the socket it talks to is `HESPER_SOCKET` (hesperd sets it
in every agent's environment; without it, the default socket), compared
after cleaning and resolving links. Pointed at another hesperd
(`--daemon-socket` elsewhere) it sends none and is a person there (that
daemon does not know the agent and would refuse it, exit 5).

Tests: internal/agents/tree_test.go (answer/steer matrix: person, parent,
non-parent, self, grandparent, with and without letParentAnswer, input
in approval, unknown caller, a person's shell and an agent's shell;
limits; persistence of the tree and the result across a restart;
reparent; verified caller beats a lying claim, rw attach; the policy as
a table), cmd/hesperctl/tree_test.go (`new --let-parent-answer --wait`,
`result`, `ls --children`, `ls --tree`, approve/stop exit codes,
timeout), internal/transport `TestRemoteAgentTree` (a child on M of an
agent on L: parent recorded, answer policy and rename enforced on L,
result from M). Polish: internal/agents/tree_more_test.go (session starts
as children within the limits, a person's without a parent whatever the
params say, audit; uploads only to descendants, chunks of a person's
upload refused; closing a parent re-parents and re-depths the subtree,
persisted), internal/agents/shell_test.go (a real `/bin/sh -i`: task
typed after the prompt; tracked: starting → working (activity sleep) →
idle, send makes it working until done, a builtin too, a background one
closes when its command ends; untracked: idle throughout, no activity,
not closed in the background; input before a late prompt waits; a resumed shell does not retype its task),
cmd/hesperctl/polish_test.go (relative `--project`, local and another
machine; `result` with nothing yet; caller only to the own daemon;
`new --kind shell --track --wait` returns after the command, without
`--track` after the exit, or times out), and in
agents_more_test.go / tree_test.go: `close`/`tidy` return with the agents
gone, `--no-wait`, `screen --rows`, `lastRows`, `wait --until settled`,
`new --wait` text output.

### As built — MCP (hesperctl mcp: relay/cmd/hesperctl/mcp.go)

`hesperctl mcp [--daemon-socket S]` is a Model Context Protocol server on
stdio, so Claude Code and Codex use Hesper as tools. Hand-rolled (no SDK,
no new dependency): newline-delimited JSON-RPC 2.0 on stdin/stdout, logs
on stderr only, the server exits when stdin ends (after the calls under
way). **Protocol**: the initialize handshake ("legacy" era), revisions
2025-11-25 (newest), 2025-06-18, 2025-03-26, 2024-11-05: the client's
version if served, else the newest. A dual-era client's `server/discover`
probe (revision 2026-07-28) gets -32601, on which the spec has it fall
back to initialize. Methods: `initialize`, `ping`, `tools/list`,
`tools/call`, `resources/list`, `resources/templates/list` (empty),
`resources/read`, `resources/subscribe`/`unsubscribe`, `prompts/list`
(empty); notifications `initialized`, `cancelled`. No batches.

**Tools come from the command registry**, never a list: one per command
not marked `NoMCP`, so commands added later (open, wall, desk, …) appear
by themselves. Name: `hesper_` + the name in snake case, one-word commands
of group Agents with `agents_` (`hesper_agents_new`, `hesper_agents_ls`,
`hesper_history_search`, `hesper_projects_update`). Description: summary,
help, usage, `--json` output, examples. `inputSchema` (JSON Schema object,
`additionalProperties: false`): every flag but `--json` and
`--daemon-socket` by its value's type (bool → boolean, int → integer,
duration → string "90s/5m", string → string, repeatable list flags →
array of strings), with usage and default; positional arguments from
`Usage` (upper-case words; `[X]` optional, `X…` an array, `A|B` →
`a_or_b`, a name a flag has gets `_arg`; `TASK…|-` drops stdin); a Usage
that cannot be read gets an `args` array instead. Registry fields
(Command): `ReadOnly` → `readOnlyHint` (ls, self, show, screen, wait,
result, status, profiles, drafts/projects/groups ls, projects recent,
history search/show/brief/stats, devices, reference), `Destructive` →
`destructiveHint` (stop, kill, close, rm, tidy, drafts/projects/groups
rm, history delete, revoke; others false), `NoMCP` (attach, events,
watch, login, pair, help, mcp). `openWorldHint` false.

**A call** runs the command as a child process of the server (hesperctl
itself: `NAME --json --flag=value… [--] ARGS…`, stdin empty), not in the
server's process: calls run concurrently, a cancelled call can be
interrupted, and commands write to stdout as they always did (no writer
refactor of every command). Exit 0: the stdout JSON as one text content
(`{"ok":true}` when the command prints nothing). Non-zero:
`isError: true` with the command's `--json` error plus `exitCode`:
`{"error":{"code","message"[,"agentId"]},"exitCode":N}` (exit codes as
"CLI foundation"). Bad parameters (unknown, wrong type, missing
required) are `isError` with code usage, exit 2; an unknown tool is
JSON-RPC -32602. Waiting: a command with a duration `--timeout` (wait;
new with `wait: true`) always gets one: the parameter, at most 10 m, by
default 10 m. Every call is bounded at 11 m; output at 4 MiB.
`notifications/cancelled` cancels the call: the child gets SIGINT (SIGKILL
3 s later) and the request no response.

**Resources**: `hesper://agents` (ls --json), `hesper://needs-you` (the
agents in approval or question, as `wait --until needs-you`),
`hesper://reference` (the Markdown reference, generated in process).
Subscribing to agents or needs-you subscribes once to hesperd
(agents.subscribe); an `agents.*` notification sends
`notifications/resources/updated` for each subscribed URI (at most every
250 ms).

**Caller**: the server inherits its parent's environment and passes it to
every call, so HESPER_AGENT_ID and HESPER_SOCKET (or `--daemon-socket`,
set as HESPER_SOCKET) reach the command; and since the call's process is
the server's child, which is the agent's descendant (Claude Code or Codex
starts MCP servers as children), hesperd's process-tree walk finds the
agent and the call is verified as the agent's: the agent tree policy
applies, an agent starting an agent through MCP makes its child.

**install.sh --mcp** (opt-in, function `mcp_step`): `claude mcp add
--scope user hesper -- ~/.local/bin/hesperctl mcp` (an existing user-scope
hesper running another command is removed first; `~/.claude.json` backed
up) and `[mcp_servers.hesper] command = "~/.local/bin/hesperctl" (full
path), args = ["mcp"]` in `$CODEX_HOME/config.toml` (default ~/.codex;
an old table replaced, the file backed up). Already registered: nothing
changes. `--dry-run` says what it would do; `CLAUDE` replaces the claude
binary (tests).

Tests (`mcp_test.go`, over pipes against the test daemon; the calls run
the test binary as hesperctl via HESPERCTL_TEST_MAIN): handshake and
version negotiation, tools/list against the registry (NoMCP left out,
schemas checked, annotations), a synthetic command's schema and argv,
calls (ls, new with a shell agent, send, screen, show not found → exit 3,
wait timeout → exit 6, approve, close), unreachable hesperd (exit 4),
parameter errors, resources (needs-you after a PermissionRequest),
subscription notifications, cancelling a wait, and an agent whose process
runs `hesperctl mcp` with HESPER_AGENT_ID unset: the agent it starts is
its child (verified caller).

### As built — move work (checkpoints, agents.move; daemon)

Code: `internal/handoff/checkpoint.go` (checkpoints, restore, prune),
`internal/agents/checkpoint.go` (when they are taken, pruning,
`agents.checkpoint`, `checkpoints.restore`'s worktree),
`internal/agents/processes.go`, `internal/agents/move.go` (plan, probe,
pack, import, handover note), `internal/remote/move.go` (the
orchestration), `internal/sessions/checkpoints.go` (history),
`pkg/wire/move.go`, `cmd/hesperctl` (`move`, `checkpoint`). Tests:
`internal/handoff/checkpoint_test.go`, `internal/agents/checkpoint_test.go`,
`internal/sessions/checkpoints_test.go`, `internal/transport/remote_move_test.go`
(`TestRemoteMoveWork`, `TestRemoteMovePreflightAndFork`), hesperctl
`TestAgentCommands`.

**Checkpoint.** A Claude or Codex agent's Git folder (a repository with a
commit) as two commits on no branch, the shape `git stash create` has for
the index: `base (HEAD) <- staged (the index's tree) <- files (every file:
tracked, untracked; ignored ones left out, as git add -A)`, built through a
copy of the index (`GIT_INDEX_FILE`), `commit-tree --no-gpg-sign`, at
`refs/hesper/checkpoints/<local id>`; the one it replaces moves to
`…/<local id>-prev`. The user's index, branches, stash and files are never
touched (tested by hashing the index file and comparing refs, HEAD and
status). Unchanged (same files tree, staged tree and base): not taken
again, the existing one is reported. A folder outside Git, or a repository
without a commit: none (`checkpoint` stays absent; agents.checkpoint
answers `{checkpoint: null}`). Shells are never checkpointed (a shell may
sit in the home folder): `agents.checkpoint` of one is `invalid`.
- Taken: when the agent enters `done` (debounced: at most one per 60 s per
  agent, `CheckpointEvery`; a later turn end inside the window is
  checkpointed when it closes), on `agents.close` (once the agent left
  `working`, right before its process is ended; an ended agent: just after
  its removal), on `agents.move` (the source's pack), and on
  `agents.checkpoint {id}` → `{checkpoint}`. One at a time per daemon.
- `Agent.checkpoint {ref, commit, at, changed, branch?}` (omitempty; in
  agents.list / agents.changed; persisted in agents.json). `changed` is the
  number of files that differ from HEAD (staged, unstaged, untracked).
- History: `Session.checkpoint` (same object) for this Mac's sessions
  whose hesperd agent had one, also after the agent is gone. Kept in the
  history index's new `checkpoints` table (kind, sid → JSON), local only
  (the refs live in this Mac's repositories), not replicated;
  sessions.changed follows an update.
- Pruned: refs older than 14 days (`CheckpointKeep`, by committer date) on
  daemon start and daily, in every repository listed in
  `$STATE/checkpoints.json` (`{"repos": {path: last}}`, written when a
  checkpoint is made); a repository left without any leaves the list.

**`checkpoints.restore {session, ref, commit, machine?}` → `{path,
branch}`** (added for the app's "Restore checkpoint"): on the session's
home (another Mac's session goes there as the host method
`checkpoints.restore {key, ref, commit}`, right `transfer`; `machine`, when
given, must be that home), checks that `commit` is the checkpoint `ref`
(or `ref-prev`) names in the repository of the session's folder (its
project's folder there when the folder is gone), then makes a new worktree
at `<worktree root>/<project key>/<branch slug>-restored[-N]`: on the
checkpoint's branch when that branch is free and at the checkpoint's base
(or missing: then made there), else on a new branch
`<branch>-restored[-N]` from the base; the checkpoint's work restored as
uncommitted changes (staged ones staged, untracked ones untracked). Not
found: `not_found` (e.g. pruned).

**agents.move `{id, to, fork?, interrupt?, leaveProcesses?}`** →
`{agent: "<new id>", …the new Agent's fields}` (the Agent fields stay at
the top level: older clients decode the result as an Agent). The daemon
that gets the call orchestrates (the gateway's Fleet, as before: any two
machines, this one included); the agent's own Mac does its part through
host methods. Steps:
1. Preflight (the agent untouched on failure, error `data.code`):
   `offline` (-32010) when either machine cannot be reached; `busy` when
   the agent is `starting` or `working` — with `interrupt: true` it gets
   Esc and up to 15 s to settle (done/idle/question/approval/error/exited
   are settled), else still `busy`; `processes` with `data.processes:
   [{pid, command}]` unless `leaveProcesses: true` — the processes running
   under the command shells the tool started (Claude Code's Bash tool,
   Codex's exec: `sh`/`bash`/`zsh`/… children of the tool and what runs
   under them; the tool's other children such as MCP servers and
   `hesperd hook` calls are not counted; through a login-shell wrapper);
   `tool-missing` when the target's profile command for the kind is not on
   its agents' PATH; `no-remote` when the target does not have the project
   and it has no Git remote. A shell does not move (`invalid`).
2. Checkpoint: the source takes a fresh checkpoint; it is the bundle's
   handoff commit (same shape as before, so older daemons read it).
3. Transfer: as before (incremental from the target's commits; when the
   target will clone, from the source's last known remote head), at most
   5 GB (`too-large`; settings.json `maxTransferMB`, default 5120), over the E2E transfer
   channel. The controller adds `move: {from, to, fork, note}` to the
   manifest (older targets ignore it).
4. Target: the project is the project's folder there (shared projects,
   `PathOn`), else the source's path mapped to this home; missing (or an
   empty folder): `git clone` of the remote into it (a taken path:
   `projects.clone` into the projects root). The agent lands where its
   branch is checked out already (that checkout must be clean, as before),
   else in the source's worktree path mapped to this home, else — the
   source ran in its main checkout — a new worktree
   `<worktree root>/<project key>/<branch slug>`; never over a checkout
   with local changes (`dirty_target`). The uncommitted work is restored
   as before. The conversation is placed for the new folder (its `cwd`s
   mapped to it). The folder is pretrusted as a spawn's is (setting
   `trustProjects`). The agent resumes (`--resume` / `codex resume`) with
   its kind, profile, name, task, tree place; its local id kept when free.
   Then the handover note is typed in once its screen settles: "You were
   moved from <src> to <dst>. Your worktree is now <path> on branch <b>,
   with the same uncommitted changes. Processes you started on <src> did
   not move." (fork: "forked …", plus "The original agent keeps running
   on <src>."; outside Git: "Your folder is now <path>.") Only when the
   conversation was resumed; not yet configurable.
5. The source is closed (agents.close semantics: interrupt if needed,
   checkpoint, hangup) with reason `moved`: `agents.removed {id, reason:
   "moved", data: {to: "<new id>"}}` (through links too:
   `agentlink.Event.to`; a host without it is closed plainly). `fork:
   true` keeps it. Its children follow the new id (Reparent), not with fork.

Events: `agents.moving {id, step, to, percent?, agent?, fork?, error?}` on
the orchestrating daemon's subscriptions: `checkpoint`, `transfer`
(`percent` 0–100: download and/or upload progress), `worktree`, `resume`
(`agent`: the new id), then `done` (`agent`) or `failed` (`error {code,
message}`). Undo is a move back.

hesperctl: `move ID --to MACHINE [--fork] [--interrupt]
[--leave-processes] [--json]` (alias `mv`, `move ID MACHINE` still works;
prints the new id, the processes on stderr), `checkpoint ID [--json]`;
exit 7 for `busy` and `processes`. `hesperctl reference` and `events` help
list the new codes and `agents.moving`.

**Deviations from the shared contract:** the move runs on the daemon
that gets agents.move (it reaches both Macs), not necessarily the agent's
Mac — same result, and the progress events reach the caller's
subscribers. The agent is no longer stopped before the move: it must be
settled, so its conversation is complete, and nothing needs undoing on
failure. The checkpoint commit has the staged tree in between (base <-
staged <- files) so the index state is restored too; `-prev` is kept. The
bundle carries the checkpoint as the existing handoff ref
(`refs/ghosty/handoff/<id>`, wire name kept) with the branch, not the
checkpoint ref. `agents.move`'s result keeps the Agent fields next to
`agent`. `agents.moving` adds `done`/`failed` steps and `agent`, `fork`,
`error`. Moves of history sessions (`sessions.resume` to another Mac)
keep creating a missing repository from a full bundle; `agents.move`
answers `no-remote` there instead (as specified). The per-project carry
list is not built yet.

**Needs a live two-Mac check:** clone from a real (ssh/https) remote with
credentials on the target; trust prompts of the real Claude Code / Codex
in the new worktree and the timing of the handover note on their real
screens; `processes` detection with real Claude Code Bash tool shells and
MCP servers; transfer progress over the relay for large bundles.

### As built — scratch projects (daemon)

Code: `internal/projects/scratch.go` (model, creation, adoption,
lifecycle, archive/restore/delete/promote, moves, history), `crdt.go`
(`Record.created`, `Record.scratch`), `internal/agents` (`registry.go`
spawn, `projecthook.go`, `move.go`), `internal/remote/move.go`,
`internal/handoff` (`Plan.scratch`, `ProjectInfo.scratch`,
`WriteManifest`), `internal/sessions/service.go` (`folderRemoved`),
`internal/host/projects.go` and `pkg/devicekey/rights.go` (host
methods), `pkg/wire/projects.go`, `cmd/hesperctl/scratch.go`,
`cmd/hesperd` (`$HESPER_SCRATCH_ROOT`). Tests:
`internal/projects/scratch_test.go`, `internal/sessions`
`TestFolderRemoved`, `internal/transport/remote_scratch_test.go`
(`TestRemoteMoveScratch`), hesperctl `TestScratchCommands`. Every test
uses temporary homes, never `~/scratch` or `~/projects`.

**Model.** A catalog project of kind `scratch` (a generated
`identity.local`, so a `p-…` id like any project): `ProjectInfo` gains
`created` (omitzero) and `scratch: {state: "active"|"resting"|"archived",
keep, archivedAt?, home: "<machine short>", git}` (only for kind
scratch). Replicated as `Record.scratch {home (node), keep, archivedAt
(Unix ms, 0: not), git, adoptedAt}`, each field a last-writer-wins
register like the others, and `Record.created` (the oldest wins).
`state` is derived: `archived` when `archivedAt` is set, else `active`
when a live agent (not exited) has its project id or (this Mac's) runs
inside its folder, else `resting`; it follows the agents on
projects.list, every 30 s and on an agent's start. The per-folder
`"scratch:<folder>"` ids stay for agents outside every project.
- The scratch root is `~/scratch` (`$HESPER_SCRATCH_ROOT`; tests: the
  gateway's `Registry.Home/scratch`, or `projects.Options.ScratchRoot`);
  without one, scratch projects are off (`unavailable`).

**Creation.** `projects.scratch {name?, task?, machine?}` →
`{project, path}`: the name given, else the first words of the task's
first line slugged (`[a-z0-9]+`, whole words up to 40 characters; name
= the slug's words, "csv cleanup"); folder
`<root>/<yyyy-mm-dd>-<slug>`, `-2`, `-3`… when that folder, the archive's
folder of that name or a catalog entry has it; `git init -b main` and
an empty first commit "Start scratch: <name>" (not signed, hooks
skipped: hesperd's own commit; `user.name/email` Hesper /
hesper@localhost only when git has none); the catalog entry (home: this
Mac, created/lastUsed now). Another `machine`: done there (host method
`projects.scratch`, its state merged here). `agents.spawn {scratch:
true}` without a project makes one from the task (else the agent's
name) and spawns into it; `scratch` is ignored when `project` is given.
Several agents may run in one scratch.

**Adoption** (on start and with the daily lifecycle): every
`<root>/<yyyy-mm-dd>[-rest]` folder (not hidden) becomes a scratch
project of this Mac (name: the rest with `-` → space, else the date;
created: the date): a new entry, or the project the folder already is
(repo or folder kind, no remote) turned kind scratch with its id kept.
A folder with content and no `.git` stays as it is (`git: false`, no
checkpoints); an empty one gets its repository. Clones (a Git remote)
and removed projects (`projects.remove`) are left alone. Projects of
kind scratch without a lifecycle get one.

**Lifecycle** (this Mac's scratches only, i.e. `home` is this Mac; on
start and every 24 h): `keep` skips; live agents bump `lastUsed`;
resting since `max(lastUsed, created, adoptedAt)` ≥ `archiveAfterDays`
(default 14) → archived: the folder moves to `<root>/.archive/<folder>`
(`-2`… if taken), `archivedAt` set, the project's path follows; archived
≥ `deleteAfterDays` (default 30) → deleted: the folder removed
(`RemoveAll`, only ever a direct child of the root or its archive), the
catalog entry tombstoned. Archived projects are left out of
projects.list (`{archived: true}` includes them) and projects.recent,
but stay in the `projects.changed` notifications (the app filters on
`scratch.state`); sessions in an archived scratch keep its id (its
original folder still resolves to it).
- `projects.scratchKeep {id, keep}` → ProjectInfo (any Mac; replicated).
- `projects.scratchArchive {id}` → ProjectInfo; `projects.scratchRestore
  {id}` → ProjectInfo (back to `<root>/<folder>`, resting, `lastUsed`
  now); `projects.scratchDelete {id}` → `{}`. Archive, delete and
  promote are refused with `busy` while agents run in it. They run on
  the scratch's home: another Mac forwards them there (host methods of
  the same names, right `transfer`) and merges the answer's state.
- `projects.promote {id, name?, createRepo?: "github"}` (the existing
  method; `path` is optional now): on the home, `busy` while agents run
  in it; the folder moves to `<projects root>/<slug of name>` (`exists`
  when taken), kind `repo` (`folder` for a `git: false` scratch), the
  same id (sessions and agents stay linked), name set, `lastUsed` now.
  `createRepo: "github"` runs `gh repo create <slug> --private --source .
  --push` there; without gh (`unavailable`) nothing moves; a failing gh
  answers `remote` ("promoted to …, but gh repo create failed"). A
  promoted scratch that got a remote keeps its local id (repository
  resolution falls back to it).
- Settings: settings.json `"scratch": {"archiveAfterDays",
  "deleteAfterDays"}`, read on start; `projects.scratchSettings
  {archiveAfterDays?, deleteAfterDays?}` → the current values (1–3650;
  `{}` only reads), written back into settings.json with its other keys
  kept. The app's names for the same: `settings.get {keys?:
  ["scratch.archiveAfterDays", "scratch.deleteAfterDays"]}` → `{values:
  {key: n}}` (no keys: both; unknown keys left out) and `settings.set
  {values: {key: n}}` → `{values}` (another key, or a value outside
  1–3650: `invalid`). Per Mac (each runs its own scratches' lifecycle).
- `projects.scratchKeep` (and every scratch method) of an unknown id is
  `not_found`; of a project that is not a scratch `invalid`.

**Moves.** `agents.move` of an agent in a scratch project: the source's
plan says `scratch: true`, so a target without the repository is not
`no-remote`; the bundle is full; the manifest's `project.scratch
{local, name, created}` lets the target make the folder at its own
`<scratch root>/<same folder name>` (`-2`… when taken by something
else) from the bundle (no worktree: the agent runs in the scratch
folder), record it as the same project (made from the manifest when the
state did not arrive yet) and, for a move (not a fork), make itself the
scratch's home. The source's folder stays on the source (as with any
move); it no longer archives or deletes it.

**History.** `Session.folderRemoved` (omitempty): the session ran in a
scratch project whose folder hesperd deleted (by its project id while
the tombstone lasts, 180 days, or its folder).

**hesperctl:** `new --scratch TASK` (not with --project, --worktree,
--branch), `scratch ls [--all]`, `scratch new NAME… [--machine M]`
(prints the folder), `scratch keep SCRATCH [--off]`, `scratch archive`,
`scratch restore`, `scratch promote SCRATCH [--name N] [--github]`,
`scratch rm` (SCRATCH: id, unique name, folder name or folder); in the
reference and MCP tools.

**Deviations from the shared contract (additive):** `projects.scratch`
answers `{project, path}`; `projects.scratchDelete`,
`projects.scratchSettings`, `settings.get` and `settings.set` are new
methods (the app's Delete and the Settings fields); `projects.list` takes `{archived}`; `adoptedAt` makes
adopted folders rest from their adoption, so the first start does not
archive every old folder at once; clones in the scratch root are not
adopted; a scratch without Git promotes to kind `folder`; the per-folder
`scratch:<folder>` projects remain for agents outside every project.

### As built — bring the folder (daemon)

Code: `pkg/wire/bring.go` (`Bring`, `Bringing`, steps,
`agents.bringing`), `pkg/wire/wire.go` (`SpawnParams.bring`,
`SpawnParams.draft`, `Error.path`), `internal/handoff/bring.go` (plan,
destination, pack, unpack, tar), `internal/agents/bring.go` (registry
side, `Server.bring`), `internal/remote/bring.go` (`Fleet.Bring`
orchestration), `internal/host/agents.go` (`bring.plan`,
`bring.probe`), `internal/host/service.go` and `internal/gateway`
(`agents.export "folder:…"`, `agents.import` of a bring bundle),
`internal/host/handoff.go` / `pkg/client/handoff.go` (`folder.tar` on
the transfer channel), `pkg/devicekey/rights.go`,
`cmd/hesperctl/agents.go` (`new --bring [--clean]`). Tests:
`internal/handoff/bring_test.go` (tar excludes, symlinks, cap, escapes,
destinations, a repository without remote), `internal/transport/
remote_bring_test.go` (two machines: a repository with a remote —
clone, branch, staged/unstaged/untracked work, progress with the draft,
catalog path, relay sees nothing, then `exists`; a repository without
a remote, clean; `exists` with nothing written; a hidden folder
refused; a scratch project; a plain folder from M to L with excludes
and the cap). All in temporary homes.

**agents.spawn** takes `bring: {from?: "<machine short>" (default: this
Mac), path?: "<absolute path on from>" (default: params.project),
changes?: "with" (default) | "clean", draft?}` and a top-level `draft?`
(the app's draft id; either is echoed). The spawn's `machine` is the
target (default: this Mac). from = target: a plain spawn of `path`.
Otherwise the daemon the call reaches orchestrates (any two machines,
itself included) and the reply is the agent, as for any spawn. `draft`
and `bring` never reach a host (a host refuses `bring`).

**Preflight** (nothing written on failure): both machines linked
(`offline`, -32010; a host without transfer: `unavailable`); the
source's `bring.plan {path}` → `{path, home, name, git?, remote?,
remoteHead?, branch?, projectId?, scratch?}` — `path` must be an
existing folder below the source's home, not the home, with no hidden
folder below the home on the way (`invalid`; `~/.ssh` never travels);
a Git checkout is taken at its top. The target's `bring.probe {plan,
kind?, profile?}` → `{path, exists, tool?}`: where the folder goes
there —
- a scratch project: `<target's scratch root>/<same folder name>`;
- a repository with a remote: the same path under the target's home
  when the source has it under `~/projects` or `~/scratch`, else
  `<target's projects root>/<name>`;
- anything else: the same path under the target's home.
Something there (anything, even empty) → `exists` with
`data.path` = that path ("Use mini's copy"). The spawn's tool missing →
`tool-missing`.

**Steps** (`agents.bringing {id: "br-…", draft?, step, percent?, to,
from, path?, agent?, error?}` to every subscriber that takes moves;
`draft` on every note when given):
1. `checkpoint`: the source packs. A Git folder (with a commit): with
   changes a checkpoint at `refs/hesper/checkpoints/bring` (the
   agents' routine; pruned with the others) is the handoff commit,
   `clean` carries HEAD only; the bundle has the branch and is
   incremental from the remote's head the source last saw when the
   repository has a remote (the target clones), else full. Over the
   transfer cap (settings.json `maxTransferMB`, default 5 GB):
   `too-large`. Any other folder (or a repository
   without a commit): `folder.tar` without `node_modules`, `.build`,
   `DerivedData`, `target`, `dist`, `.venv`, `__pycache__` at any depth,
   symlinks kept only when they point inside the folder (made
   relative), sockets/devices skipped, unreadable files skipped; over
   the transfer cap of files: `too-large`. A remote source packs on
   `agents.export {id: "folder:with:<path>" | "folder:clean:<path>"}`
   (right `transfer`), downloaded sealed for this Mac.
2. `transfer` (percent 0–100; halves when both ends are other Macs).
3. `unpack`: the target makes the folder next to its place
   (`.<name>.bring-*`) and renames it in only while the place is still
   free (`exists` otherwise; never overwritten): a clone of the remote
   on the source's branch (`checkout -B` at the source's HEAD, upstream
   set when `origin/<branch>` exists) with the bundle's commits; a
   repository made from the full bundle (origin set when the source had
   one); or the untarred folder (only folders, files and symlinks inside
   it; nothing written through a symlink). With changes the staged,
   unstaged and untracked (not ignored) files are restored as on the
   source. The catalog: a scratch project is recorded as the same
   project with this Mac's path (its home stays the source); anything
   else through the project resolution (`Paths[<target>]`; the same id
   for a repository, a new folder project otherwise).
4. `spawn` (`path`: the folder there): `agents.spawn` there with
   `project` = that folder (the params' kind, profile, worktree, branch,
   name, task and tree fields kept); then `done` (`agent`, `path`) or
   `failed` (`error {code, message}`) at any point.

**Host methods** (rights): `bring.plan` (transfer), `bring.probe`
(observe), `agents.export` with a `folder:` id (transfer), uploads of
`folder.tar`, `agents.import` of a manifest with `bring: {kind: "git" |
"folder", changes}` (no agent; the job's result is `{path,
projectId?}`). A host without them: `unavailable` ("update hesperd
there").

**hesperctl:** `new --machine M --bring [--clean] [--project DIR] TASK`
brings DIR (default: the current folder) from this Mac; `--bring` needs
another machine, no `--scratch`; `--clean` only with `--bring`; waits up
to 11 minutes. `events` shows `agents.bringing`. In the reference and
the MCP tools (flags of `new`).

**Deviations from the shared contract (additive):** the top-level
`draft` and `bring.draft` (both accepted); `agents.bringing` also has
`id`, `from`, `path`, `agent` and `error`, and ends with `done` /
`failed` like `agents.moving`; `exists` carries `data.path`; a
non-Git folder's pack step is still called `checkpoint`; only folders
below the home (not hidden) are brought; a repository with a remote
brings only its current branch (as a move); the bring checkpoint ref is
`refs/hesper/checkpoints/bring` (one per repository, replaced by the
next bring); a non-scratch project's copy is recorded through the
regular project resolution, not as a marked replica.

### As built — review (daemon)

Phases 1–2 of [the review concept](review/concept.md), the daemon side.
Code: `pkg/wire/review.go` (types), `internal/review` (Git through a copy
of the index, diff parsing, word ranges, the rules: generated,
formatting-only, moved, risk, reading order; diffs in parts),
`internal/agents/review.go` (registry and socket side),
`internal/host/review.go` (host methods), `pkg/devicekey/rights.go`.
Tests: `internal/review/diff_test.go`, `internal/agents/review_test.go`,
`internal/transport/remote_review_test.go`. All in temporary homes and
repositories with the fake agent.

**Review base.** `Agent.reviewBase` (persisted): the commit HEAD pointed
to when a Claude or Codex agent started in a Git folder (spawn, session
resume or fork); a moved agent keeps the source's (`AgentInfo.reviewBase`
in the move manifest) when the bundle brought that commit, else the
commit it moved at. An agent without one (started before this, or its
base gone) reviews from its branch point: the merge base of HEAD and the
main worktree's branch, else HEAD. Shells are never reviewed.

**Ready for review:** a Claude or Codex agent in `done`, `idle` or
`exited` whose folder (its worktree, else its project; a project below
a repository's top limits the diff to that folder) differs from its
base. The folder is its files now, untracked ones included, ignored ones
not: a tree written through a copy of the index (`git add -A` into a
temporary index file), so the user's index is never touched.

| Method | Params | Result |
|---|---|---|
| `review.list` | `{}` | `[ReviewItem]`: this Mac's, then every connected Mac's (each asked in parallel, 30 s; a Mac without `review.*` lists none), most recently settled first |
| `review.diff` | `{id, context?}` (default 3, at most 1000) | `{base, head: "worktree", tree, files: [ReviewFile]}` |

`ReviewItem = {id, machine, name, kind, project, branch?, worktree?,
state, files, added, removed, risk, riskNotes: [string], evidence,
readyAt, reviewedAt?, base}`; `readyAt` is when the agent settled
(`stateSince`).

`ReviewFile = {path, oldPath?, status: "A"|"M"|"D"|"R", binary?,
formattingOnly?, generated?, tooLarge?, order, risk, added, removed,
hunks: [Hunk]}`; `Hunk = {id: "<file index>:<hunk index>", oldStart,
oldLines, newStart, newLines, formattingOnly?, moved?, lines: [{kind:
" "|"+"|"-", text, old?, new?, words?: [[start, end]], noNewline?}]}`.
Files come in reading order (`order` is the index); hunk ids are
positions in that list, so they hold for one `tree` and one `context`.

- Diff: `git diff --histogram --find-renames` of the base against that
  tree, whatever the user's diff configuration (no color, no external
  diff or textconv, fixed prefixes, not relative); a type change is
  `M`; binary files have no hunks; a file with more than 20,000 diff
  lines is `tooLarge` without hunks.
- Word ranges: inside a hunk each run of removed lines is paired line
  by line with the run of added lines after it; pairs that share enough
  words get the differing ranges, `[start, end)` in **UTF-16 code
  units** of `text` (CoreText's). Bounded: 400 words a line, 20,000
  pairs and 300 ms a diff, then none.
- `formattingOnly`: a hunk whose changed lines are equal without
  whitespace; a file (M or R) whose every hunk is.
- `moved`: a hunk whose every changed line belongs to a block (at
  least 3 non-blank lines) removed in one hunk and added, equal but for
  indentation, in another (any file).
- `generated`: lock files (`go.sum`, `package-lock.json`, `yarn.lock`,
  `Cargo.lock`, `Package.resolved`, …), `vendor/`, `node_modules/`,
  `dist/`, `build/`, `*.pb.go`, `*.min.js`, `*.snap`, …,
  `linguist-generated` in `.gitattributes`, or "Code generated … DO NOT
  EDIT" / `@generated` in the new file's first 10 lines.
- Risk of a file: generated or formatting-only `low`; auth, secrets,
  tokens, crypto, certificates, permissions, `.env` (by path words),
  migrations and schemas (`*.sql`, `*.prisma`), deployment and CI
  `high`; deletions, build and dependency manifests (`go.mod`,
  `package.json`, `Package.swift`, `Makefile`, …) and changes over 300
  lines `medium`; else `low`. Of a change (`review.list`): the riskiest
  file; at least `medium` over 800 changed lines or over 100 changed
  lines of code without a test changed. `riskNotes` say why (`touches
  auth or secrets: <path>`, `migration or schema: <path>`, `deployment
  or CI: <path>`, `deletes <path>`, `dependencies or build: <path>`,
  `large change: …`, `no tests changed for N lines of code`).
- Reading order: high-risk files; then what others depend on (build
  and dependency manifests, configuration, `.proto`, `.graphql`,
  `.d.ts`); code, each test right after its subject (`foo_test.go`,
  `foo.test.ts`, `test_foo.py`, `FooTests.swift` after `foo`, same folder
  first); tests without a changed subject; docs; formatting-only files;
  generated files last. Within a group: riskier, then larger, the path
  only breaks ties.

**Across Macs.** `review.diff` of another Mac's agent goes to its
daemon (host methods `review.list` and `review.diff`, right `observe`).
A diff can be larger than one relay message: the controller asks for
it in parts (`review.diff {id, context?, part: n, tree?}` →
`{tree, parts, part, data}`: the diff's JSON gzipped, base64, cut into
384 KiB pieces; parts after the first name the first part's `tree`, and
a folder that changed in between is refused) and hands the app the
whole diff. The host keeps the last 4 encoded diffs for 2 minutes.

**Notification:** `review.changed {id}` on `agents.subscribe` when an
agent of this Mac settles with changes in its folder, and when a
connected Mac's agent settles (a hint: the app fetches `review.list`
again).

## Wire names kept from Ghosty

Hesper was called Ghosty. The rename covers the binaries (`hesperd`,
`hesperctl`, `hesper-keys`, `hesper-relay`), `Hesper.app`
(`de.olezierau.hesper.mac`), the
LaunchAgent `de.olezierau.hesperd`, `~/.local/state/hesper`,
`~/.config/hesper`, `~/.local/lib/hesper`, `hesperd.sock`, `hesperd.log`
and the `HESPER_*` environment. These names are signed, hashed, framed or
sent between hesperd and the relay, so they keep the old spelling
until the next relay deploy (each has a `wire name:` comment where it is
defined); renaming one needs every Mac and the relay updated together:

| Name | Where | What |
|---|---|---|
| `ghosty-req-v1` | relay/pkg/devicekey | label of every device-key signed request |
| `ghosty-e2e-v1` | relay/pkg/e2e/e2e.go | Noise prologue and per-session AD |
| `ghosty-e2e-bind-v1` | relay/pkg/e2e/e2e.go | binding digest a device key signs |
| `ghosty-stream-v1` (`… host`, `… controller`) | relay/pkg/e2e/stream.go | terminal stream HKDF info and AD |
| `ghosty-direct-v1` | relay/pkg/direct | Noise prologue of the direct path |
| `GHOSTYD1` | relay/pkg/direct | magic bytes of a direct connection |
| `ghosty-transfer-v1`, `ghosty-download-v1` | relay/pkg/transfer | file transfer key labels |
| `ghosty.v2` | relay/pkg/protocol | WebSocket subprotocol |
| `ghosty.terminal.v1` | relay/pkg/protocol | terminal WebSocket subprotocol |
| `X-Ghosty-Stream`, `X-Ghosty-Controller` | relay transport, pkg/client | terminal stream HTTP headers |
| `refs/ghosty/handoff/<id>` | relay/internal/handoff | ref in a handoff bundle between Macs |
| `ghosty_auth` | relay/internal/auth | sign-in cookie (relay) |
| `ghosty refresh successor v1` | relay/internal/storage | key of stored credential successors (relay) |
| `ghosty golden device key` | relay/pkg/devicekey (test) | seed of the golden test vector |

Deploy names, also kept until then: relay/deploy (`/opt/ghosty-relay`, compose project and service
`ghosty-relay`, `/etc/ghosty/auth.json`, `GHOSTY_IMAGE`, the image's
`ghosty` user and its binaries `ghosty-relay` and `ghostyctl`, which
relay/scripts/deploy.py and relay/Dockerfile build from `cmd/hesper-relay`
and `cmd/hesperctl`); the GitHub OAuth app "Ghosty Relay". The repository
is now `derzierau/hesper` and the Go module `github.com/derzierau/hesper/relay`
(formerly `ghosty-config`); install.sh's backups stay in
`~/.ghosty-config-backups`. Legacy names install.sh
and hesperd still recognize to clean up: ghostyd's hook entries and Codex
`hooks.json` description, which `hesperd hooks install` replaces. (The tmux
setup's names are no longer recognized: its cleanup was removed.)

**Migration (install.sh `migrate_from_ghosty`, after the build and before
the app is installed):** quits `Ghosty.app` (`de.olezierau.ghosty.mac`),
boots out `de.olezierau.ghostyd` and moves its plist to the backup; moves
`~/.local/state/ghosty` → `~/.local/state/hesper` (leaving a link at the
old path), renames `ghostyd.log` → `hesperd.log`, drops `ghostyd.sock`;
moves `~/Library/Application Support/Ghosty` → `…/Hesper` (link left too);
rewrites the old absolute paths (plain or with JSON-escaped slashes) in the
state directory's `*.json` / `*.jsonl`; moves `~/.config/ghosty` and
`~/.local/lib/ghosty` (dropping its `ghosty-keys` link); removes the
`~/.local/bin` links `ghostyd`, `ghostyctl`, `ghosty-keys` and
`~/Applications/Ghosty.app`; copies the app's defaults domain to
`de.olezierau.hesper.mac`; keeps `--allow-shell` from the old plist. A
directory whose new name exists already is left alone with a warning.
Every step checks first (a second run or a Mac without Ghosty does
nothing); `install.sh --migrate-only` runs just this step
(scripts/test-install.sh section 5).
