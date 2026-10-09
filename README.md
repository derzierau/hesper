<p align="center">
  <img src="app/Resources/brand/icon-1024.png" width="128" alt="Hesper icon: h. on dusk indigo">
</p>

<h1 align="center">hesper.</h1>

<p align="center"><b>Every Mac you own, running your agents. One fast window to steer them.</b></p>

<p align="center">
  <a href="https://github.com/derzierau/hesper/actions/workflows/app.yml"><img src="https://github.com/derzierau/hesper/actions/workflows/app.yml/badge.svg" alt="app"></a>
  <a href="https://github.com/derzierau/hesper/actions/workflows/relay.yml"><img src="https://github.com/derzierau/hesper/actions/workflows/relay.yml/badge.svg" alt="relay"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-8F9CFF" alt="MIT"></a>
  <img src="https://img.shields.io/badge/macOS-14%2B-171A2E" alt="macOS 14+">
</p>

Hesper turns all your Macs into one pool of compute for Claude Code and Codex
agents. Start an agent on the laptop, the Mac mini under the desk or the
Studio in the office, from any of them, and hand it to another Mac whenever
you like. Every agent shows up as a live terminal tile on one wall, wherever
it runs. The ones waiting for you line up in a single queue, one key away.

Every layer is trimmed for speed: a native AppKit app on libghostty, a small
Go daemon per Mac, and a budget for every hop. Typing into an agent reaches
the screen in about 3 ms, the wall holds 120 Hz with 16 live agents, and 30
idle agents cost the daemon practically nothing.

Hesper is named after Hesperos, the evening star: the first light after
sunset. Your agents run quietly across your Macs; Hesper lights up the one
that needs you.

> Hesper is built on [libghostty](https://ghostty.org). It is an independent
> project and not affiliated with Ghostty.

## Release availability

**Homebrew currently installs [v0.1.4](https://github.com/derzierau/hesper/releases/tag/v0.1.4),
a signed and notarized release for macOS 14+.** This README also describes
unreleased work on `main`; building from source is required for those features.

| Feature | v0.1.4 / Homebrew | `main` (unreleased) |
| --- | --- | --- |
| Live agent wall, attention controls, remote launches | Available | Available |
| Basic moves between Macs and Move Undo | Available | Available |
| History transfer/fork and Claude ↔ Codex handoff | Available | Available |
| Checkpoint-based moves and improved move preflight | — | Available |
| Fork a running agent onto another Mac | — | Available |
| Bring the project folder when starting on another Mac | — | Available |
| Scratch projects | — | Available |
| Native Review inbox and diffs | — | Available |

One Mac works without a relay. **Multiple Macs currently require a
self-hosted relay and device setup**; Hesper does not provide a hosted cloud
service. See [More than one Mac](#more-than-one-mac).

## Every Mac is compute

One Mac runs out of cores, memory and battery long before you run out of
tasks. Most people who run many agents have a second or third Mac sitting
idle. Hesper uses all of them.

**Start anywhere, run anywhere.** Pick the machine when you start an agent
(`@mini` in the composer, `--machine mini` on the command line), or give a
project a default machine. A remote agent looks and behaves exactly like a
local one: the machine is a badge on the tile, not a place you go to.

**Move work to where there is room.** When the laptop runs hot or its
battery runs low, hand the busy agents to a Mac with room to spare (see
[Pick up anywhere](#pick-up-anywhere)).

**Agents that use the whole pool.** `hesperctl`, the Hesper skill and the
MCP server let Claude and Codex start their own helpers on any of your Macs,
wait for them and collect their results. Each agent controls only the agents
it started, and only within depth and count limits.

**Your machines, end to end encrypted.** Macs on the same network talk
directly; otherwise through a relay you run that forwards traffic it cannot
read. Device keys live in the Secure Enclave, every device is approved per
Mac, and opening a shell on another Mac needs Touch ID. Macs with agents at
work stay awake (on battery only above 20 %).

## Pick up anywhere

Most agent tools tie a session to the machine it started on. In Hesper, work
moves between your Macs as easily as it moves between windows.

**Hand off a running agent.** Continue on mini (⇧⌘M, ⌘K, or the tile's menu)
moves an agent to another Mac. Basic moves and Move Undo ship in v0.1.4.
On `main` (unreleased), Hesper checkpoints its worktree
(staged, unstaged and untracked work, never your own branches or index),
sends only the changes over your encrypted link, recreates the worktree on
the same branch there, and resumes the same Claude or Codex conversation.
One ⌘Z moves it back. Fork on mini keeps the original and starts a twin.

**Start anywhere, even without the folder (`main`, unreleased).** Start a
task for a Mac that doesn't have the project yet, and Hesper brings it along:
cloned from its remote with your uncommitted changes, or copied whole when there is no remote
(scratch folders and plain folders included, build output left behind).

**One history across all your Macs.** ⌘Y searches every Claude and Codex
session on every Mac. Resume one where it ran, continue it on another Mac,
fork it, or hand a Claude session to Codex. These history actions ship in
v0.1.4. On `main` (unreleased), when an agent closes, its last
checkpoint stays with its session, so you can restore the work later.

**Safe by design.** Every piece of work lives in exactly one place at a time,
and moving it is an explicit step you can undo. Nothing syncs your folders
behind your back, so two agents never fight over the same files.

## Built for speed

Hesper is meant to sit open all day next to dozens of busy agents, so every
part is measured against a budget and kept lean:

| | Measured |
|---|---|
| Keystroke → frame on screen, focused agent | **2.7 ms** p50, 5.5 ms p90 |
| Wall with 16 live agents | **120 fps, 0 dropped frames** (worst frame 8.4 ms) |
| Agent state change → tile | 2.3 ms p50 |
| Keystroke round trip through the daemon | 35 µs |
| Full-screen redraw on attach (200×60, every cell colored) | 0.44 ms |
| 30 idle agents | 0.15 ms of daemon CPU in 2 s |
| Hook call from Claude or Codex | 78 µs; never blocks the agent |
| Keystroke → echo, agent on another Mac (same network) | **0.4 ms** p50 |
| Keystroke → echo, agent on another Mac (relay, 40 ms network floor) | 45 ms p50 |
| State change on another Mac → your wall | 22 ms p50 |

How it stays fast:

- **Native, no web view.** AppKit and libghostty's GPU renderer, Swift 6 on
  the app side, Go on the daemon side. The real `claude` and `codex` TUIs run
  unchanged, so there is no chat UI to keep in sync.
- **Draw only what is seen.** Tiles are vsync-paced and draw only the rows
  they show; hidden and offscreen tiles render nothing. The focused agent
  skips vsync for the lowest typing latency.
- **The daemon is the terminal.** `hesperd` keeps one VT emulator per agent,
  so attaching, switching or reconnecting starts with an exact redraw instead
  of replaying output. Slow viewers get a fresh redraw instead of a growing
  buffer.
- **Pushed, not polled.** Claude's and Codex's own hooks tell the daemon
  when an agent works, waits or finishes, and every change is pushed to the
  app and to other Macs.
- **Direct when possible.** Macs on the same network skip the relay; a link
  moves to the direct path as soon as it is up.
- **Background work stays in the background.** Session history is indexed
  into SQLite FTS5 at background priority, and nothing runs for an agent that
  is quiet.

## One wall, one queue

**One queue, one key.** ⌘J jumps to the next agent that needs you: approvals
first, then questions, then errors. ⏎, A or N allows, always-allows or denies
right on the tile. Tiles never jump around to get your attention.

**Agents belong to the Mac, not the window.** `hesperd` owns every agent's
terminal. Close the app, restart it or reboot: agents come back with their
session. You get tmux-style persistence without a terminal inside a terminal.

**Fast to start.** ⌘N opens a draft tile in the wall. Type the task, add
`@machine`, `#project`, `/profile` or `~branch`, and ⌘↩ starts it; `~branch`
gives the agent its own git worktree.

## How it works

```
 ┌──────────────────────────── your Mac ─────────────────────────────┐
 │                                                                   │
 │  Hesper.app (AppKit + libghostty)        hesperctl (CLI)          │
 │        │  JSON-RPC over a 0600 local socket   │                   │
 │        ▼                                      ▼                   │
 │  hesperd (LaunchAgent) ── owns PTYs ── claude / codex / shell     │
 │        ▲                                      │                   │
 │        └──────────── hooks: state changes ────┘                   │
 └────────┬──────────────────────────────────────────────────────────┘
          │ outbound only, Noise IK end to end   ┌─── another Mac ───┐
          ├──────────► hesper-relay ────────────►│      hesperd      │
          └────────── direct TCP on the same LAN ►│                   │
                                                 └───────────────────┘
```

| Component | What it is |
|---|---|
| **`hesperd`** | Go daemon, one per Mac, run as a LaunchAgent. Owns every agent's PTY, tracks state from hooks, persists and resumes agents, creates worktrees, indexes session history, and is the Mac's only network gateway. |
| **`Hesper.app`** | Swift 6 AppKit/SwiftUI app. Renders each agent with libghostty and talks only to the local `hesperd`. |
| **`hesperctl`** | The same control surface from a terminal, plus relay sign-in and device pairing. |
| **`hesper-keys`** | A small Swift helper holding device keys in the Secure Enclave. |
| **`hesper-relay`** | Go rendezvous server (Docker). GitHub sign-in, presence and routing. Trusted with neither content nor commands. |

The full design, wire contract and "as built" notes are in
[docs/rebuild-contract.md](docs/rebuild-contract.md); the security design is
in [docs/remote-shell-contract.md](docs/remote-shell-contract.md) and
[relay/docs/](relay/docs/).

### Technical notes

- **libghostty, embedded directly.** A pinned, checksum-verified
  `GhosttyKit.xcframework` (no Zig toolchain needed). Only
  `app/Sources/Hesper/Engine/` touches it. Each tile's command is
  `hesperd attach <id>`, so libghostty drives an ordinary PTY. Tiles stay
  vsync-paced; the focused agent renders without vsync for lower keystroke
  latency; hidden tiles render nothing.
- **The daemon is the terminal.** `hesperd` keeps its own VT emulator per
  agent, and every attach starts with an exact redraw of the screen. The
  emulator is tested against tmux as ground truth: the same bytes go into
  both and the screens must match. Slow viewers are resynced with a fresh
  redraw instead of buffering without bound.
- **State from hooks, not screen scraping.** The installer merges hooks into
  Claude Code's settings and keeps everyone else's. Codex agents get
  per-agent hook overrides plus a precomputed trust hash, so `~/.codex` is
  never modified. `hesperd hook` returns within 250 ms and always exits 0, so
  a hook can never stall an agent.
- **Resume you can trust.** Agents are persisted atomically and resumed with
  `claude --resume` or `codex resume` only when the session verifiably exists.
  The original task is re-sent only if the agent never started, so "push the
  branch" never runs twice.
- **A relay that cannot read or forge.** Requests are signed with P-256
  device keys (method rights, clock window, persisted nonce cache) and
  carried in a `Noise_IK_25519_ChaChaPoly_BLAKE2s` channel with pinned host
  keys. Terminal streams get per-direction keys; a tampered, dropped or
  reordered frame ends the stream. Pairing codes are compared on both screens,
  so a relay that swaps keys is caught.
- **Checkpoint-based moves (`main`, unreleased).**
  `hesperctl move ID --to mini` checkpoints the agent's folder, packs the transcript and an incremental git bundle
  (the checkpoint carries the uncommitted and untracked work, never ignored
  files), sends it sealed, and resumes the agent on the target in a worktree
  on the same branch (cloning the project from its remote when the target
  lacks it), with a handover note; `--fork` keeps the original. A move that
  fails leaves the agent where it was. Checkpoints are also taken when a
  turn ends and at close (`hesperctl checkpoint ID`), kept 14 days.
- **Shared history.** Claude and Codex transcripts from every Mac are indexed
  into SQLite FTS5 (pure Go, no cgo) at background priority. Project data
  shared between Macs is a CRDT, so every Mac converges.
- **Tested end to end.** 275 Go test functions, 193 Swift Testing tests for
  the UI-free `HesperCore` module, about 300 in-app UI self-test checks run
  against a fake daemon and the real one, an in-app frame probe and perf
  harness, and an installer test suite that runs every scenario in a
  temporary `HOME` with stubs that refuse any system change.

## Install

Requires macOS 14 or later. Signed releases include the app, daemon,
`hesperctl` and the Secure Enclave helper; Xcode and Go are only needed for
source builds. Install Claude Code or Codex separately to run those agents.

The repository and signed release downloads are public. No GitHub account
or token is needed to install.

### Homebrew

With Homebrew installed:

```sh
brew tap derzierau/hesper https://github.com/derzierau/hesper.git
brew install --cask derzierau/hesper/hesper
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
open /Applications/Hesper.app
```

Homebrew selects the native Apple Silicon or Intel archive and installs the
CLI tools on your PATH. Use the app path printed by Homebrew if you choose
another `--appdir`. To update:

```sh
brew update
brew upgrade --cask derzierau/hesper/hesper
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
```

Run setup after each update to restart the daemon with the new binary.

### Homebrew tap and official catalog

The current tap lives in this repository, so `brew tap` above includes its
HTTPS URL. A separate public `derzierau/homebrew-hesper` repository would
allow the shorter `brew tap derzierau/hesper` command; see
[Homebrew's tap documentation](https://docs.brew.sh/Taps.html).

Hesper is not yet in Homebrew's official catalog. Installation without
adding a tap requires a separate submission and acceptance into
`homebrew/cask` under its
[package acceptance policy](https://docs.brew.sh/Package-Acceptance-Policy).

### Download a signed release

Open [GitHub Releases](https://github.com/derzierau/hesper/releases/latest)
and download the archive for your Mac:

| Mac | Release archive |
| --- | --- |
| Apple Silicon (M-series) | `Hesper-vX.Y.Z-arm64.zip` |
| Intel | `Hesper-vX.Y.Z-x86_64.zip` |

Each archive contains `Hesper.app`, including `hesperd`, `hesperctl` and
`hesper-keys` in `Contents/MacOS`. Both architectures are Developer ID-signed,
notarized and stapled. `SHA256SUMS` contains the archive checksums.

Extract the zip, move `Hesper.app` to `/Applications`, then run:

```sh
/bin/sh /Applications/Hesper.app/Contents/Resources/hesper-setup.sh
open /Applications/Hesper.app
```

The CLI is also available directly at
`/Applications/Hesper.app/Contents/MacOS/hesperctl`.

After either binary installation, open Codex and run `/hooks` once to enable
its trusted hooks. Setup installs the LaunchAgent, agent hooks and Hesper skill.

### Build from source

You need Xcode 26 (used from the command line) and Go 1.25 or later
(`brew install go`).

```sh
git clone https://github.com/derzierau/hesper ~/projects/hesper
cd ~/projects/hesper
./install.sh --dry-run   # print every action, change nothing
./install.sh
```

The installer:

1. builds the relay tools and the app;
2. puts `hesperd`, `hesperctl` and `hesper-keys` inside `Hesper.app` and
   installs it to `~/Applications`;
3. links `hesperd` and `hesperctl` into `~/.local/bin`;
4. runs `hesperd serve` as a LaunchAgent;
5. merges Hesper's hooks into `~/.claude/settings.json` and Codex (your other
   hooks are kept);
6. links the Hesper skill for Claude Code and Codex (see below);
7. lets `hesperd` through the macOS firewall.

Run it again after every pull; it only changes what differs, and anything it
replaces is backed up first. See `./install.sh --help` for `--login-item`,
`--skip-build`, `--no-firewall` and the rest.

Codex only runs hooks it trusts: after installing, open Codex and run
`/hooks` once.

Maintainer instructions for signing credentials, release automation and the
Homebrew tap are in [docs/releases.md](docs/releases.md).

**Signing.** Without configuration the app is signed ad-hoc, which works
locally. `SIGN_IDENTITY="Developer ID Application: …" ./install.sh` signs
with the hardened runtime, and `NOTARY_PROFILE=<profile>` also notarizes.
macOS notifications need a Developer ID-signed build.

New agents start with each tool's permission prompts on (`claude`,
`codex`); Hesper queues the prompts for you (⌘J). To run agents unattended,
pick a profile per agent, per project or per kind:

| Profile | Command |
|---|---|
| `claude` (default) | `claude` |
| `codex` (default) | `codex` |
| `claude-auto-rc` | `claude --permission-mode auto --remote-control …` |
| `claude-unattended` | `claude --dangerously-skip-permissions --remote-control …` |
| `codex-unattended` | `codex --dangerously-bypass-approvals-and-sandbox` |
| `shell` (default) | your login shell |

> [!WARNING]
> The `-unattended` profiles turn the permission prompts off (and, for
> Codex, the sandbox): an agent can then run any command and change any file
> your user can, on every Mac you give it. Use them only for projects and
> tasks you would let run without watching. Make one a default with
> `~/.config/hesper/settings.json`, e.g.
> `{"defaults": {"kinds": {"codex": "codex-unattended"}}}`; your own
> profiles go in `~/.config/hesper/profiles.json`.

## Using it

| Key | |
|---|---|
| ⌘N | New agent: task, machine, profile (Claude, Codex or a shell), project, worktree and branch. ⌘↩ starts it. |
| ⌘↩ | Focus the selected tile and type into it. Again, or ⌘Esc, returns to the wall. |
| ⌘[ ⌘] | Previous / next agent |
| ⌘J | Next agent that needs you (approvals, then questions, then errors) |
| ⏎ / A / N | Allow / always allow / deny the selected tile's approval |
| ⌘K | Palette: agents, projects, machines, actions |
| ⌘Y | History: every session on every Mac |
| ⌘R | Review: finished work from every Mac, its diff, tests and where each change came from |
| ⌘W | Stop the agent (asks first); for an ended one, remove it |
| ⌘⇧M | Move the agent to another Mac |

Esc always goes to the agent, since Claude and Codex use it. The menu bar item
shows the counts and the queue, and a notification arrives when an agent
starts waiting for you, or when its finished work is ready to review.

`hesperctl` does the same from a terminal:

```sh
hesperctl ls                                  # every agent, with its state
hesperctl new --project ~/projects/app "Fix the login redirect"
hesperctl new --machine mini --worktree --project ~/projects/app "Add the export"
hesperctl attach fix-the-login-redirect       # Ctrl-] detaches
hesperctl send ID "yes, go ahead" ; hesperctl approve ID ; hesperctl deny ID
hesperctl stop ID ; hesperctl resume ID ; hesperctl rm ID ; hesperctl move ID --to mini
```

Agents are addressed by id (`L/a7f3k2`), local id or unique name.

`hesperctl mcp` serves the same commands as MCP tools on stdio, so Claude
Code and Codex can start, watch and answer agents themselves
(`hesper_agents_new`, `hesper_agents_wait`, `hesper_history_search`, …,
plus the resources `hesper://agents`, `hesper://needs-you` and
`hesper://reference`). `./install.sh --mcp` registers it with both
(`claude mcp add --scope user hesper -- ~/.local/bin/hesperctl mcp`, and
`[mcp_servers.hesper]` in `~/.codex/config.toml`). Inside a Hesper agent the
agent-tree rules apply to its calls: what it starts are its children.

### Hesper for agents (skill)

`skills/hesper` is an [Agent Skill](https://agentskills.io) that teaches
Claude Code and Codex to use `hesperctl`: see what every agent is doing and
which ones need you, fan work out to parallel agents in worktrees and collect
their results, delegate and wait (`hesperctl new --wait`), drive or answer an
agent, and find and resume past sessions. It keeps to Hesper's agent policy
(an agent steers only the agents it started) and never approves anything on
your behalf unless you ask. The installer links it into `~/.claude/skills/hesper`
and `~/.agents/skills/hesper` (Codex); `--no-skills` leaves it out. Then just
ask, e.g. "what are my agents doing?" or "split this into three agents in
worktrees and report back". `hesperctl reference` is the full CLI it reads.

## More than one Mac

Every Mac runs its own `hesperd`. One Mac needs nothing more. To use agents
across Macs you need a relay: a small Go server you deploy yourself (Docker,
behind Caddy or nginx; there is no built-in or shared one). The Macs sign in
to it and find each other through it; on the same network they then talk
directly, otherwise through the relay.
[relay/docs/deployment.md](relay/docs/deployment.md) covers deploying it;
then point each Mac at it and sign in:

```sh
./install.sh --relay https://relay.example.com   # your relay's address
hesperctl login --role host --name "Mac mini" --out ~/.local/state/hesper/host.credentials.json
hesperctl login --role controller --name "Mac mini" --out ~/.local/state/hesper/controller.credentials.json
```

[relay/README.md](relay/README.md) covers the sign-in, where the relay
address comes from, and approving devices with `hesperctl pair-host` and
`hesperctl approve`.

Opening a shell on another Mac is off by default. Install with
`--allow-shell` on the Mac that should accept it; the request then needs
Touch ID on the Mac that sends it.

## Where things live

| Path | |
|---|---|
| `/Applications/Hesper.app` (Homebrew), `~/Applications/Hesper.app` (source install) | The app, with `hesperd`, `hesperctl` and `hesper-keys` in `Contents/MacOS` |
| `~/.local/state/hesper/` | `hesperd.sock`, `agents.json`, `projects.json`, logs, relay credentials and device keys |
| `~/.config/hesper/` | `profiles.json` (launch profiles), `settings.json`, `machines.json` |
| `~/Library/LaunchAgents/de.olezierau.hesperd.plist` | The daemon |
| `~/worktrees/<project>/<task>` | Agent worktrees (`HESPER_WORKTREE_ROOT`) |

Set `HESPER_STATE_DIR` to use another state directory.

## Development

```sh
cd relay && go vet ./... && go test -race ./... && make build   # hesperd, hesperctl, relay, keys
make -C app test-unit build                                       # HesperCore tests, then Hesper.app
make -C app run                                                   # the app against a fake daemon
make -C app test                                                  # plus UI self-tests (needs a GPU)
scripts/test-install.sh                                           # installer, dry runs in a temp HOME
```

| Path | |
|---|---|
| `app/` | Hesper.app. `Sources/HesperCore` is UI-free and unit-tested; `Sources/Hesper/Engine` is the libghostty boundary; `Tools/fake-hesperd` is a test daemon with fake agents. |
| `relay/` | `hesperd`, `hesperctl`, `hesper-relay` and `hesper-keys`, with their docs in `relay/docs/` |
| `docs/` | The design and wire contract |
| `install.sh` | Installer, updater and migrator |

Contributions are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). Report
security issues privately as described in [SECURITY.md](SECURITY.md).

## Status

Hesper is young and used daily by its author. Signed v0.1.4 releases are
available through Homebrew and GitHub Releases. Native Review and diffs are
implemented on `main`, but are not included in v0.1.4. See
[Release availability](#release-availability) before choosing a build or
sharing screenshots of development features.

Supported agents are Claude Code, Codex and a plain shell.

### Upgrading

**Default profiles now keep permission prompts on.** Earlier versions started
new Claude agents with `claude-auto-rc` (auto mode) and new Codex agents with
`codex-full` (approvals and sandbox off) unless you configured otherwise.
Now the defaults are plain `claude` and `codex`. Nothing in your
`~/.config/hesper` is rewritten:

- profiles and defaults you set in `profiles.json` / `settings.json` keep
  working as before;
- the old names `claude-bypass` and `codex-full` still work wherever they
  are saved (running agents, project and per-folder defaults, drafts,
  scripts) and mean `claude-unattended` and `codex-unattended`;
- if you relied on the old built-in defaults, new agents now ask before
  acting. To keep the old behaviour, put this in
  `~/.config/hesper/settings.json` and restart `hesperd`
  (`launchctl kickstart -k gui/$(id -u)/de.olezierau.hesperd`):

  ```json
  {"defaults": {"kinds": {"claude": "claude-auto-rc", "codex": "codex-unattended"}}}
  ```

## Coming from Ghosty

Hesper was formerly called Ghosty. On a Mac that ran it, `install.sh` stops
the old app and LaunchAgent, moves state and config to their `hesper` names,
rewrites saved paths and replaces the hooks. Agents, history, device keys and
relay enrollments carry over, and every agent resumes under `hesperd`. A few
wire names keep the old spelling for compatibility.

## License

[MIT](LICENSE). Geist and Geist Mono are bundled under the
[SIL Open Font License](app/Resources/Fonts/OFL.txt). Third-party licenses
are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). A build from
source with the default prebuilt libghostty includes GNU libintl (LGPL-2.1);
release builds do not. [docs/licensing.md](docs/licensing.md) explains both
and how to build either way.
