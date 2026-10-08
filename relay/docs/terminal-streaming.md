# Terminal streams and remote attach

Between two hesperd, every terminal byte (and every agent event) travels on
one **link** per controller and host ([protocol](protocol.md#links)), a
terminal stream the relay bridges (or a direct connection on the
[direct path](direct-path.md)). The relay does not parse VT sequences or
terminal content and stores no terminal bytes; between hesperd it cannot
read them either (sealed).

## Open and authorization

The controller sends a signed `agents.link` (right `observe`) inside the
end-to-end channel. The host refuses it in plaintext. Through the relay
the host opens `GET /v1/terminal` with bearer auth, `X-Ghosty-Stream`
(random 256-bit ID), `X-Ghosty-Controller` (intended device), and
subprotocol `ghosty.terminal.v1`. The relay checks both device ownership
and role, caps total streams at 32/four per host, and responds
`{"type":"ready"}` after registration.

The request returns the ticket `{"id","e2e"}` (the stream's secret, inside
the channel). The controller opens the same endpoint with its own bearer
credential and `X-Ghosty-Stream`, receiving its own ready frame. Another
owner/device, reuse, and browser Origin are rejected. Tickets are carried
in headers, never URL query strings; pending registrations expire after 15
seconds. Setup is also bounded by the request's deadline.

Each attach on the link is a signed `agents.attach` (right `observe` for
mode `ro`, `type` for `rw`; shells need the `shell` right) naming the link
and a channel. A link is checked every 5 seconds against the host's
`controllers.json`: a revoked device loses it.

## Sealed streams

Both ends seal every frame with the ticket's secret (ChaCha20-Poly1305 per
direction, implicit counters; details in
[the protocol](protocol.md#end-to-end-channel)). The relay bridges these
binary frames unchanged and cannot read or inject keystrokes; an altered,
dropped or repeated frame ends the stream (and the link: its attaches
reattach on the next one).

## Direct streams

An `agents.link` that came on the [direct path](direct-path.md) returns a
ticket with `"direct": true`; the controller connects a second direct
connection (the same Noise handshake, naming the ticket) to the host and
the link runs there. TCP gives the backpressure; the host waits 15 seconds
for the connection. If it cannot be made, the controller opens the link
again through the relay. A direct link ends when its connection fails;
hesperd then opens one through the relay and its attaches reattach with a
fresh screen. When the direct path comes up while a link runs on the
relay, hesperd opens a direct link and moves the attaches to it.

## Frames and backpressure

Each binary message contains up to 32 KiB of raw bytes; larger writes are
split without interpreting their content. Link frames (`kind | channel |
length | payload`) are reassembled across messages. Each relay direction
holds at most one application frame. Socket backpressure propagates to the
sender; writes time out after 10 seconds.

On the host every attach is a `ptyhost` viewer: one that falls 4 MiB
behind gets SIZE and a fresh redraw instead of what it missed. On the
controller every attach has its own queue of whole frames toward the local
client; past 4 MiB (a client that does not read) the bridge drops it and
reattaches (SIZE, then the redraw), so one slow tile never holds up the
link or the agent. Input typed while an attach reattaches is dropped,
never replayed.

Relay pings active peers every 15 seconds with a 10-second timeout. Either
peer closing ends the stream. Credential expiry, revocation, allowlist
removal, host control disconnection and relay shutdown close streams.

## Latency

`make latency` (in `relay/`; `go test -run '^TestLatency$' -bench Latency
./internal/transport`) runs an in-process relay behind a proxy that delays
every message by half a leg's round trip (20 ms per leg by default, as
measured between the Macs and the relay) and two hesperd ("L", "M") with
device keys and the end-to-end channel. Through L's own socket, exactly as
the app attaches, it measures keystroke → echo into a fake shell on M, the
time to open a remote attach, a state change on M until L's
`agents.subscribe` shows it, a signed request and a ping, and the hosts'
time per stage (`internal/perf`). It runs once over the relay and once
with the direct path (loopback). Plain `go test` runs it without delay
with generous bounds.

Measured on an M1 Max, 20 ms legs (network floor 40 ms over the relay):

| | relay | direct path |
| --- | --- | --- |
| keystroke → echo p50 / p95 / p99 | 45.1 / 46.9 / 58.3 ms | 0.4 / 1.5 / 4.4 ms |
| open remote attach → first frame p50 | 45.7 ms | 0.7 ms |
| state change on M → L's subscription p50 / p95 | 22.1 / 23.2 ms | 0.2 / 0.2 ms |
| signed request (agents.list) p50 | 45.9 ms | 0.8 ms |
| ping p50 | 45.1 ms | 0.4 ms |

The host's own work per keystroke (decrypt, attach input, PTY write, PTY
read, seal) is well under a millisecond; a state change travels one leg
pair (host → relay → controller), not a request round trip, because the
link pushes it.
