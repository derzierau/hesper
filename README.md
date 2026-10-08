<p align="center">
  <img src="app/Resources/brand/icon-1024.png" width="128" alt="Hesper icon: h. on dusk indigo">
</p>

<h1 align="center">hesper.</h1>

<p align="center"><b>Every agent on every Mac, in one calm window.</b></p>

<p align="center">
  <a href="https://github.com/derzierau/hesper/actions/workflows/app.yml"><img src="https://github.com/derzierau/hesper/actions/workflows/app.yml/badge.svg" alt="app"></a>
  <a href="https://github.com/derzierau/hesper/actions/workflows/relay.yml"><img src="https://github.com/derzierau/hesper/actions/workflows/relay.yml/badge.svg" alt="relay"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-8F9CFF" alt="MIT"></a>
  <img src="https://img.shields.io/badge/macOS-14%2B-171A2E" alt="macOS 14+">
</p>

Hesper is a native macOS app for people who run many Claude Code and Codex
agents at once, often on more than one Mac. Every agent is a live terminal
tile on one wall. The ones waiting for you (an approval, a question, an
error) line up in a single queue, one key away. Agents outlive the window,
survive reboots, and can move to another Mac with their conversation and
their uncommitted work.

Hesper is named after Hesperos, the evening star: the first light after
sunset. Your agents run quietly across your Macs; Hesper lights up the one
that needs you.

> Hesper is built on [libghostty](https://ghostty.org). It is an independent
> project and not affiliated with Ghostty.

## Why Hesper

When agents do the typing, your attention is the scarce resource. Running six
or sixteen agents in tmux panes, terminal tabs or separate windows means
cycling through all of them to find the one that is stuck on a prompt, and
an agent you start on one Mac stays on that Mac.

**One wall for every agent, on every Mac.** Each agent is a live,
GPU-rendered terminal tile, grouped in bands by project. A remote agent looks
exactly like a local one: the machine is a badge on the agent, not a place
you go to.

**One queue, one key.** Hesper knows each agent's state from Claude Code's
and Codex's own hooks, not from guessing at the screen. ⌘J jumps to the next
agent that needs you: approvals first, then questions, then errors. ⏎, A or
N allows, always-allows or denies right on the tile. Tiles never jump around
to get your attention.

**The real agents, natively.** Hesper runs the actual `claude` and `codex`
TUIs, exactly as their makers designed them, in libghostty surfaces inside an
AppKit app. There is no web view and no replacement chat UI, so every CLI
feature works on day one.

**Agents belong to the Mac, not the window.** A small daemon, `hesperd`, owns
every agent's terminal. Close the app, restart it or reboot: agents come back
with their session. You get tmux-style persistence without a terminal inside
a terminal.

**Your machines, end to end encrypted.** Macs reach each other directly on
the same network, or through a relay you run that forwards traffic it cannot
read. Device keys live in the Secure Enclave, every device is approved per
Mac, and opening a shell on another Mac needs Touch ID.

**Conversations that move.** ⌘Y searches every Claude and Codex session on
all your Macs. Resume one, fork it, continue on another Mac, or hand it from
Claude to Codex with a brief. ⌘⇧M moves a running agent to another Mac,
uncommitted changes included.

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
- **Moving agents between Macs.** `hesperctl mv` packs the transcript and an
  incremental git bundle (with a handoff commit for uncommitted work), sends
  it sealed, and resumes on the target. Any failure resumes the agent where
  it was.
- **Shared history.** Claude and Codex transcripts from every Mac are indexed
  into SQLite FTS5 (pure Go, no cgo) at background priority. Project data
  shared between Macs is a CRDT, so every Mac converges.
- **Tested end to end.** 275 Go test functions, 193 Swift Testing tests for
  the UI-free `HesperCore` module, about 300 in-app UI self-test checks run
  against a fake daemon and the real one, an in-app frame probe and perf
  harness, and an installer test suite that runs every scenario in a
  temporary `HOME` with stubs that refuse any system change.

## Install

Hesper builds from source. You need macOS 14 or later, Xcode 26 (used from
the command line) and Go 1.25 or later (`brew install go`).

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

**Signing.** Without configuration the app is signed ad-hoc, which works
locally. `SIGN_IDENTITY="Developer ID Application: …" ./install.sh` signs
with the hardened runtime, and `NOTARY_PROFILE=<profile>` also notarizes.
macOS notifications need a Developer ID-signed build.

> [!WARNING]
> The built-in launch profiles start agents with their permission prompts
> turned off (`claude --dangerously-skip-permissions`, `codex
> --dangerously-bypass-approvals-and-sandbox`), because Hesper is designed to
> run many agents unattended. Review and edit them in
> `~/.config/hesper/profiles.json` before you start your first agent.

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
| ⌘W | Stop the agent (asks first); for an ended one, remove it |
| ⌘⇧M | Move the agent to another Mac |

Esc always goes to the agent, since Claude and Codex use it. The menu bar item
shows the counts and the queue, and a notification arrives when an agent
starts waiting for you.

`hesperctl` does the same from a terminal:

```sh
hesperctl ls                                  # every agent, with its state
hesperctl new --project ~/projects/app "Fix the login redirect"
hesperctl attach fix-the-login-redirect       # Ctrl-] detaches
hesperctl send ID "yes, go ahead" ; hesperctl approve ID ; hesperctl deny ID
hesperctl stop ID ; hesperctl resume ID ; hesperctl rm ID ; hesperctl mv ID mini
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

Every Mac runs its own `hesperd`. On the same network they connect directly;
otherwise through a relay. The relay is a small Go server you deploy yourself
(Docker, behind Caddy or nginx); [relay/README.md](relay/README.md) covers
deploying it, signing in each Mac, and approving devices with
`hesperctl pair-host` and `hesperctl approve`.

Opening a shell on another Mac is off by default. Install with
`--allow-shell` on the Mac that should accept it; the request then needs
Touch ID on the Mac that sends it.

## Where things live

| Path | |
|---|---|
| `~/Applications/Hesper.app` | The app, with `hesperd`, `hesperctl` and `hesper-keys` in `Contents/MacOS` |
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

Hesper is young and used daily by its author. Not there yet:

- no built-in diff or pull-request review of an agent's work;
- no signed binary release; you build from source;
- agents are Claude Code, Codex or a plain shell.

## Coming from Ghosty

Hesper was formerly called Ghosty. On a Mac that ran it, `install.sh` stops
the old app and LaunchAgent, moves state and config to their `hesper` names,
rewrites saved paths and replaces the hooks. Agents, history, device keys and
relay enrollments carry over, and every agent resumes under `hesperd`. A few
wire names keep the old spelling for compatibility.

## License

[MIT](LICENSE). Geist and Geist Mono are bundled under the
[SIL Open Font License](app/Resources/Fonts/OFL.txt). Third-party licenses
are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
