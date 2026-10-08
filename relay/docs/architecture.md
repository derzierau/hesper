# Architecture and operating model

## Decisions

Two Go programs carry the system: `hesper-relay` owns connectivity and
device enrollment; `hesperd`, one per Mac, owns the agents and is the Mac's
only gateway to the other machines (rebuild contract part R): as a relay
**host** it serves its agents to devices approved on it, as a relay
**controller** it shows the other machines' agents through its local
socket, so `Hesper.app` never talks to the network. `hesperctl` is the CLI
for both.

Keep session semantics out of routing. `relay.Hub` takes a nonblocking `Peer`
interface and enrolled identities. It routes opaque method payloads, binds each
response to the exact host connection that received the request, and enforces
owner isolation. Its unit tests use in-memory peers. HTTP/WebSocket details live
in the transport adapter.

On a Mac, `internal/agents` is the registry (PTYs, states from hooks,
persistence), `internal/host` the host role (authorization, the channel,
the direct path, links, moves) and `internal/remote` the controller role
(links to the other machines, the merged registry, attach bridges, moves);
`internal/gateway` assembles them into `hesperd serve`.

Use bbolt for transactional enrollment and device credentials. It is suited to a
single relay with a small identity directory. The repository interface permits a
later storage adapter; it does not imply that routing can already span replicas.
Multiple relay replicas would additionally need a connection directory and
inter-node routing. We do not introduce those components prematurely.

## Identity and trust

GitHub authenticates the human; the relay's numeric-ID allowlist authorizes them.
The stable owner key is `github:<id>`, independent of username changes. A device
starts an enrollment with an S256 challenge. A browser-bound, single-use OAuth
state plus PKCE verifies identity; a separate CSRF-protected approval confirms the
device name, role, and matching code. The device claims credentials with its
retained verifier. GitHub tokens are used once to fetch identity and discarded.

Access credentials last 15 minutes; rotating refresh credentials share a 30-day
absolute expiry. Refresh reuse durably revokes the device. Access and refresh
credentials are stored hashed. Existing sockets close at access expiry. An
allowlist reload disconnects removed users immediately. The host and CLI serialize
file renewal with a process lock and atomically replace mode-0600 credentials.
An ambiguous refresh requires sign-in again rather than retrying a consumed token.
Legacy invitations are permitted only in explicit loopback development mode.

A controller can address only active hosts in the same owner namespace. A host
cannot issue controller requests or impersonate another host's replies. Local
host input permission is a further boundary: a read-only host rejects mutations
even from its owner's authenticated and approved controller.

Local administration is served on a Unix socket with mode `0600`. No admin
routes exist on the public listener. Revocation is committed before the live
connection is removed. Enrollment and revocation are synchronized so an in-flight
pairing cannot restore a revoked identity in memory.

**Commands: the relay is not trusted.** Hosts decide themselves which
controller may do what. Every request but `snapshot` is signed by the
controller's device key (P-256, in the Secure Enclave on a Mac, Touch ID for
starting a shell on another machine) over the host's machine ID, the method, the exact params, a
timestamp and a nonce; the host checks it against its own allowlist
(`controllers.json`, approved locally after comparing a code derived from
the host's and the device's keys), the method's right, a ±60 s window and a
persisted 10-minute nonce cache. A relay, its database, backups or a stolen
relay credential can therefore not type into an agent, answer a prompt,
start, stop or move agents, or open a shell; it can drop, delay or reorder
requests (availability is not protected) and every refusal is audited on
the host. The relay's GitHub login and device enrollment still decide who
may connect at all; device approval on each host is the second, independent
boundary. See [Device keys](protocol.md#device-keys).

The host's runner authorizes each request exactly once (a nonce is only
good once) and passes the resulting caller (device, name, rights, strong
signature) to the operation in its context; shells (`hesperd serve
--allow-shell`, off by default) read that caller for starting one (strong
key), for any method or link event about one, and refuse everything
without it. Shells are impossible until a device is approved
with the `shell` right.

**Content: end to end between controller and host.** TLS ends at the
reverse proxy, so everything the relay forwards in plaintext it can read.
Part N (`pkg/e2e`, [End-to-end channel](protocol.md#end-to-end-channel))
therefore puts requests, results and terminal streams in a channel only the
two ends can open: Noise IK (`Noise_IK_25519_ChaChaPoly_BLAKE2s`,
github.com/flynn/noise) between the host's static key (its transfer key,
pinned by controllers and covered by part K's approval code) and the
controller's static X25519 key, which the host accepts only with a
signature by the device key it approved. Inside travel the part K-signed
requests (params, agent lists and events, attach bytes, move bundles) and their results; terminal streams get a
fresh secret through the channel and seal every frame. The relay forwards
the channel as ordinary requests (`e2e.hello`, `e2e`) and binary terminal
frames: **the hub did not change** and routes them as any other method.
A relay that alters a frame gets it refused (nothing runs, audited) or ends
the stream; replays are refused by counters, inner request IDs and part K's
nonces. Each connection handshakes anew and controllers rekey hourly, so a
session's keys are forgotten (forward secrecy against a later theft of the
static keys).

What the relay still sees: who talks to whom, when and how much; the
snapshots hesperd hosts publish for every controller (agent ids, kinds,
states, attention kinds, PTY sizes, machine stats; no names, tasks,
attention texts, paths or terminal output); `devices.request` (public
keys, names). Everything
else (agent lists and events, every method's params and results, attach
bytes, move bundles) travels in the channel, and a hesperd host refuses
plaintext for all of it. A relay can still drop or delay traffic
(availability is not protected).

**Route: direct when possible.** Between machines on one network the
controller reaches the host directly ([direct path](direct-path.md)): the
host listens on its private addresses only and hands them out inside the
channel (`direct.offer`) to approved devices, the connection is the same
Noise IK handshake over TCP (pinned host key, controller key bound to the
approved device key), and inner requests are authorized and audited exactly
as through the relay. The relay remains the rendezvous, carries presence,
snapshots, and is the fallback at any moment. Requests that may
have reached the host when the direct connection failed are resent through
the relay only when idempotent (as the identical inner request).

## Connection and execution semantics

Each enrolled identity has at most one current connection. Reconnecting replaces
the old connection. Removal uses connection identity, so a late close from the
old socket cannot remove its replacement. Presence and snapshots are ephemeral;
the credential directory survives relay restarts.

Requests are correlated using fresh relay-generated IDs. Host responses are
accepted only from the exact connection assigned to that request. A request has
a deadline; the host refuses work that expires before execution. The host
serializes operations so two controllers cannot interleave paste and submit.

Delivery is **not exactly once**. A connection can fail after the agent receives input
and before the response arrives. Neither relay nor SDK retries RPC requests.
Errors explicitly preserve the possibility of an unknown outcome. The host
reconnects with capped exponential backoff and jitter; it republishes state but
does not replay old requests. A controller should refresh on foreground/resume.

Backpressure is bounded: WebSocket messages are limited to 1 MiB, each transport
has 16 queued outgoing messages, a controller can have 16 pending requests, and
the hub permits 256 pending requests and 256 connected devices. Host metadata
snapshots are capped at 256 KiB. A slow consumer is disconnected and must refresh.
This initial implementation is intended for a person's own machines, not large tenants
whose aggregate inventories exceed one message.

Both ends use WebSocket ping/pong while continuing to read. HTTP requests have
body/header limits and timeouts. Authentication has a global attempt budget (240 HTTP requests/minute), avoiding
trust in proxy-supplied client IP headers. A deployment with broader enrollment
needs an edge rate limit and tenant-level resource quotas.

## hesperd across machines (part R)

The controller role keeps **one link per other machine**
([links](protocol.md#links)): a sealed terminal stream (relay, or the
direct path when it is up; a link on the relay moves to the direct path as
soon as that comes up) that carries the host's agent events and every
attach to its agents, multiplexed. One stream keeps within the relay's four
streams per host whatever the number of remote tiles, and an attach opens
with one signed request. Events are pushed at the change (about one network
leg to the app's subscription); requests (input, answers, spawn, …) are one
signed request each.

Attaching to a remote agent through the local socket is bridged byte for
byte: the remote host runs the same `ptyhost` attach (redraw, the size
owner rule, slow viewers resynced), so `hesperd attach M/x` behaves like
`hesperd attach L/x`. The bridge reattaches with a fresh screen (SIZE, then
the redraw) when the local client reads too slowly, when the host ended the
channel, when the link broke (waiting up to 30 s for the next one) and when
the link moved to the direct path; input typed meanwhile is dropped, never
replayed. A machine that cannot be reached keeps its agents listed for a
minute.

Moves (`agents.move`) stop the agent on the source, carry its conversation
file and its code (Git bundle with the uncommitted work as a handoff
commit, incremental from what the target has) sealed with the transfer
crypto, import it on the target (resume with the session) and remove the
source's; any failure after the stop resumes the source.

## Metadata performance

Wire v2 sends `machine.updated` deltas for host state and presence, including a
removal marker on revocation. Initial connections and explicit list requests get
full inventories. Ordered WebSocket delivery preserves consistency; reconnect
always starts from a fresh inventory. The Go SDK aggregates deltas for consumers.

Owner-indexed lookups avoid scans over every device. The hub queues messages under
its lock; JSON serialization runs once per recipient in the transport writer,
outside that lock. Queues remain bounded at 16 messages; slow consumers disconnect
and resnapshot. Metadata coalescing and shared fan-out encoding are not implemented.

A hesperd host publishes about 15 ms after an agent changed (bursts
coalesce, at least 50 ms apart; its registry's subscription is the
trigger), with a 30-second safety net; presence (5 s cache) changes
trigger a publication too.

On the controller, hesperd takes agent changes from its links, not from
published snapshots: a state change on the host reaches the app's
subscription in about one network leg (`make latency`: 22 ms p50 with
20 ms relay legs).
