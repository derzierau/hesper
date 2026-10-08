# Hesper relay and hesperd

`hesperd` is Hesper's per-Mac daemon: it owns the agents in their PTYs
(their state, hooks, approvals, worktrees, persistence) and is the Mac's
only gateway to the other machines. `Hesper.app` talks only to the local
`hesperd`; an agent on another Mac looks exactly like a local one, its id
just carries that machine's short name (`M/a7f3k2`). The contract is
[docs/rebuild-contract.md](../docs/rebuild-contract.md).

Machines reach each other through `hesper-relay`, a small server you
deploy yourself ([Deploy your relay](docs/deployment.md)): every Mac
connects outbound to the same HTTPS origin (no Tailscale, no inbound
ports). On one network they talk directly ([direct path](docs/direct-path.md));
the relay stays the rendezvous and the fallback. The relay is optional:
without one, hesperd runs the local agents and both relay roles stay off.
Hesper has no built-in relay. The relay is trusted neither with
commands (hosts run only requests signed by a device they approved) nor
with content (an [end-to-end channel](docs/protocol.md#end-to-end-channel)
it forwards but cannot read).

## Programs and packages

| Component | Responsibility |
| --- | --- |
| `hesper-relay` | Enroll devices, authorize owners, track presence, route requests and streams |
| `hesperd` | The daemon (`serve`), `attach`, `hook`, `hooks`; host and controller roles |
| `hesperctl` | Agents on `hesperd` (`ls`, `new`, `attach`, …), sign-in, device approval, relay administration |
| `internal/agents`, `internal/ptyhost`, `pkg/wire` | The registry, the PTYs, the local socket protocol |
| `internal/gateway` | Assembles `hesperd serve`: registry, socket, host and controller roles |
| `internal/host` | Host role: authorization (part K), channel (N), direct path (D), agents methods, links, moves |
| `internal/remote` | Controller role: links to every other machine, merged registry, attach bridges, moves |
| `internal/handoff` | Moving an agent with its conversation and code (pack, probe, unpack) |
| `pkg/agentlink` | The link between two hesperd: agent events and attach channels on one stream |
| `internal/relay`, `internal/transport` | Routing, request lifecycle, HTTP/WebSocket and terminal stream bridging |
| `internal/auth`, `internal/identity`, `internal/storage` | GitHub sign-in, allowlist, enrollment, credentials |
| `pkg/protocol`, `pkg/client` | Relay wire contract, controller client (channel, direct path, transfers) |
| `pkg/devicekey`, `macos/hesper-keys` | Device keys: request signing and verification, Secure Enclave helper |
| `pkg/e2e`, `pkg/direct`, `pkg/transfer` | End-to-end channel, direct path, sealed file transfer |

See [architecture](docs/architecture.md) and the [relay protocol](docs/protocol.md).

## Build and test

Requires Go 1.25+ (and git for moves).

```sh
make build      # dist/hesper-relay, dist/hesperd, dist/hesperctl, dist/hesper-keys (macOS)
make test       # go test -race ./...
make check      # go vet ./...
make latency    # the latency harness: two hesperd through an in-process relay, 20 ms legs
```

Tests never start a real claude or codex (the test binaries are fake agent
programs, `internal/fakeagent`) and never touch the user's state,
LaunchAgents or the relay server: they run their own relays, hesperd
instances and state directories.

## One machine, two roles

`hesperd serve` reads two relay enrollments from its state directory
(`~/.local/state/hesper`):

- `host.credentials.json`: the **host role** serves this Mac's agents to
  devices approved here, through the relay or the direct path;
- `controller.credentials.json`: the **controller role** connects to every
  other machine and shows their agents through the local socket.

A role whose file is missing stays off. Sign in once per role:

```sh
./dist/hesperctl login --relay https://relay.example.com --name "Mac mini" --role host \
  --out ~/.local/state/hesper/host.credentials.json
./dist/hesperctl login --relay https://relay.example.com --name "Mac mini" --role controller \
  --out ~/.local/state/hesper/controller.credentials.json
```

### Which relay

`hesperctl login` and `pair` take the relay origin from, in this order:

1. `--relay https://…`;
2. the `HESPER_RELAY` environment variable;
3. `"relay"` in `~/.config/hesper/settings.json` (`$HESPER_CONFIG_DIR`),
   e.g. `{"relay": "https://relay.example.com"}`; `./install.sh --relay URL`
   writes it;
4. the relay recorded in this Mac's `host.credentials.json` or
   `controller.credentials.json` (every credentials file stores the relay
   that issued it).

With none of these they stop with `no relay configured: pass --relay
https://… or set relay in ~/.config/hesper/settings.json`. `hesperd serve`
does not use this setting: each role talks to the relay its credentials
file names, so an enrolled Mac keeps working when the setting changes.

Flags of `hesperd serve`: `--allow-shell` (devices with the shell right
may start shells here, Touch ID on theirs), `--direct auto|on|off`
(`auto`: listen unless the macOS firewall would ask), `--direct-port`,
`--require-device-keys`, `--keep-awake` (macOS, on by default: no idle
sleep while an agent works or waits), `--control-socket`. Short machine
names come from `~/.config/hesper/machines.json` (`{"machines": {"<relay
device id>": {"short": "M"}}}`), else the host's own name for itself.

The controller role holds the device's only relay connection; it serves
`controller.sock`, through which `hesperctl pair-host`, `machines`,
`request` and `watch` send their requests (signed in their own process).

### Device approval

Hosts accept requests only from controller devices approved on the host
itself ([protocol](docs/protocol.md#device-keys)). Each controller has two
P-256 keys: in the Secure Enclave through `hesper-keys` (installed to
`~/.local/lib/hesper/`), the second one behind Touch ID (starting a shell
on another machine); with `HESPER_KEYS_SOFTWARE=1` (Linux, CI) software
keys in the state directory.

```sh
# On the laptop, once per other machine: prints a code such as ABC-DEF
./dist/hesperctl pair-host --machine mini --rights observe,answer,type,transfer[,shell] [--wait 5m]
# On the mini: check the code, then approve
./dist/hesperctl approve ABC-DEF        # --deny, --rights, --name, --allow-software-shell
./dist/hesperctl devices-local          # approved and waiting devices; --revoke NAME
```

Rights: `observe` (list agents, links, read-only attach, plan/probe/export
of a move), `answer` (approvals), `type` (input, read-write attach),
`transfer` (spawn, stop, resume, remove, rename, clone, import a move),
`shell` (shells: starting one needs the strong key; every look at one needs
the right). Enforcement starts with the first approval. Keep the clocks in
sync (±60 s).

### Administration

```sh
./dist/hesperctl devices
./dist/hesperctl revoke --device DEVICE_ID
./dist/hesperctl machines [--save]
./dist/hesperctl trust [--reset MACHINE]
```

## Local walkthrough (development)

```sh
./dist/hesper-relay --data .state --dev-invitations &
for role in host controller; do
  ./dist/hesperctl invite --role $role --owner personal |
    ./dist/hesperctl pair --relay http://127.0.0.1:8787 --name "Dev $role" \
      --out /tmp/dev/$role.credentials.json
done
./dist/hesperd serve --state-dir /tmp/dev --config-dir /tmp/dev/config
HESPER_STATE_DIR=/tmp/dev ./dist/hesperctl ls
```

## Deploy the relay

The relay is self-hosted: a Docker container behind Caddy or an existing
nginx, with GitHub sign-in and an allowlist of stable GitHub account IDs.
[Deploy your relay](docs/deployment.md) covers a standalone server
(`RELAY_DOMAIN=relay.example.com docker compose up -d` in `deploy/`) and a
server that already runs nginx and certbot (`just deploy`, configured in the
Git-ignored `deploy/local.env`).
Part R needs no relay change: everything rides on requests and terminal
streams the hub already routes.

## Boundaries

- One relay process per state file; horizontal routing is not implemented.
- Delivery is not exactly once: a mutation that timed out may have run;
  hesperd never replays one.
- Machines must be awake and online to be reached; their agents survive a
  daemon restart (respawned with their sessions).
