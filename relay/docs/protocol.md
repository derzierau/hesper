# Hesper protocol v2

Public origin: `https://relay.olezierau.de`. All JSON is UTF-8. Native clients use
bearer credentials in headers. Credentials and pairing secrets never belong in
URLs. HTTP clients must not follow enrollment redirects with secrets attached.

## GitHub enrollment

1. Device generates a random 32-byte base64url verifier, retains it locally, and
   computes `challenge = base64url(SHA256(verifier))` without padding.
2. `POST /v1/auth/enroll` with `name`, `role` (`host` or `controller`), `challenge`,
   and optional exact registered `callback`. Response (201): `id`, `code`,
   `verificationURL`, `expires`, `interval` (poll seconds).
3. Open `verificationURL` in the system authentication browser. GitHub callback
   is `/auth/github/callback`. The user checks the matching code and approves the
   named device. A registered app callback carries only enrollment ID and status.
4. Poll `POST /v1/auth/claim` with `id` and `verifier` no faster than the returned
   interval. HTTP 409 / `authorization_pending` means keep waiting. Other failures
   terminate the flow. Success returns credentials; claiming is single-use.

Credential fields: `relay`, `deviceId`, `role`, `token`, `refreshToken`, `expiresAt`,
`refreshExpiresAt` (timestamps are RFC3339). Persist in the Keychain in an app, in protected
files on CLI. Keep the trusted relay origin used to initiate enrollment.

`POST /v1/auth/refresh` with `refreshToken` rotates both secrets and returns the
same credential shape. Access expiry is 15 minutes, capped by the 30-day absolute
refresh-family expiry. Only one renewal may run per device. Persist the new pair
before connecting. A lost response or persistence failure requires a new login;
reusing a refresh token revokes the entire device and active connection.

Native JSON endpoints reject browser `Origin` headers. Browser approval uses a
separate cookie-bound CSRF token and escaped HTML. Authentication requests share
a global 240/minute budget; responses never cache. GitHub needs no repository scopes.
Production `/v1/pair` is disabled. Legacy invitations exist only in loopback dev mode.

`GET /healthz` returns `{"ok":true,"protocol":2}`. Administration remains private.
The HTTP `/v1` resource prefix is retained; WebSocket envelope negotiation is v2.
Old v1 WebSocket clients must upgrade and cannot silently receive incomplete state.

## WebSocket connection

Connect to `wss://relay.olezierau.de/v1/connect` with:

```text
Authorization: Bearer DEVICE_SECRET
Sec-WebSocket-Protocol: ghosty.v2
```

The server requires `ghosty.v2`, text JSON messages, and `v: 2` in each message.
Browser Origin-bearing requests are not currently accepted. The server initially
sends a `hello` message containing device ID and role in `result`.

A controller receives a full `machines` inventory on connection. It replaces all
previous inventory. Subsequent `machine.updated` messages replace only the named
machine; `removed: true` removes it. Request a full refresh:

```json
{"v":2,"type":"list","id":"client-request-id"}
```

The corresponding `machines` response echoes `id`. Unsolicited updates have no
request ID. A missing `machines` field represents an empty inventory.

```json
{
  "v": 2,
  "type": "machines",
  "machines": [{
    "id": "MACHINE_ID",
    "name": "My Mac",
    "online": true,
    "snapshot": {
      "runtimeId": "RUNTIME_ID",
      "short": "M",
      "terminals": [{
        "target":{"runtimeId":"RUNTIME_ID","terminalId":"a7f3k2"},
        "role":"claude","state":"approval",
        "attention":"approval","attentionSince":1791036424,
        "columns":120,"rows":40,"exited":false
      }],
      "capabilities":{"agents":true,"transfer":true,"spawn":true,"shell":false,"e2e":true,"direct":true,"ping":true},
      "transferKey":"BASE64_X25519_PUBLIC_KEY",
      "e2eKey":"BASE64_X25519_PUBLIC_KEY",
      "machine":{"agents":4,"memoryUsed":0.62,"battery":0.18,"onBattery":true,"lidClosed":false}
    }
  }]
}
```

A hesperd host (rebuild contract part R) publishes **no names, tasks,
attention texts, branches, paths or terminal output**: each agent is a
terminal whose `terminalId` is its local id, `role` its kind, `state` its
state, `attention` its attention kind (`approval`, `question`, `error`)
with `attentionSince` (the Unix second it entered that state), and its PTY
size. `short` is the host's own short name. Everything else about agents
travels only inside the [end-to-end channel](#end-to-end-channel), as
results of the [agents methods](#agents) and on [links](#links).

Example delta:

```json
{"v":2,"type":"machine.updated","machineId":"MACHINE_ID","machines":[{"id":"MACHINE_ID","name":"My Mac","online":false}]}
```

Revocation sends `machine.updated` with `machineId` and `removed: true`, omitting
`machines`. Deltas and list responses share an ordered queue. A slow consumer is
disconnected, never silently given a partial inventory; reconnect resets state.

Offline machines have `online: false` and no snapshot. An online machine can have
no snapshot while initializing. Adapter errors replace the snapshot with an
`error` object; do not display old state as current.

## Host operations

Controllers send:

```json
{
  "v":2,"type":"request","id":"UNIQUE_CLIENT_REQUEST_ID","machineId":"MACHINE_ID",
  "method":"ping",
  "params":{}
}
```

Every request except `snapshot`, `ping` and `devices.request` also carries `auth`,
the controller's device-key signature (see [Device keys](#device-keys)).
Between a controller and a host that both support it, every request but
`devices.request` travels sealed inside the
[end-to-end channel](#end-to-end-channel) as an `e2e` request; the table
below describes the inner requests.

```json
{"v":2,"type":"request","id":"…","machineId":"MACHINE_ID","method":"input","params":{…},
 "auth":{"device":"CONTROLLER_DEVICE_ID","ts":1791036424000,"nonce":"AAECAwQFBgcICQoLDA0ODw==","sig":"MEUCIQ…"}}
```

The relay verifies ownership, assigns its own correlation ID and `deadline`
(Unix milliseconds), and forwards to that host with `auth` unchanged and
`controllerId` set to the sending controller. `machineId` is not forwarded.
Hosts return a `result` message using the relay correlation ID. The relay restores
the original client request ID before returning the result to that controller.

| Method | Parameters | Result |
| --- | --- | --- |
| `snapshot` | `{}` (unsigned) | The relay-visible snapshot |
| `ping` | `{}` (unsigned, right `observe`) | `{}`: controllers time the round trip (`hello`'s `rttMs`) |
| `agents.*`, `projects.*`, `profiles.list`, `fs.stat` | see [Agents](#agents) | |
| `transfer`, `agents.import`, `job`, `agents.export`, `download` | see [Moves](#moves) | |
| `devices.request` | `name`, `key`, `strongKey`, `hardware`, `rights` (unsigned) | `{"status":"pending","code","expires","hostKey"}` or `{"status":"approved","name","rights","hostKey"}` |
| `e2e.hello`, `e2e` | see [End-to-end channel](#end-to-end-channel) (unsigned; they carry the others) | |
| `direct.offer` | `{}`, only inside the channel (unsigned) | see [Direct path](#direct-path) |

Unknown parameter fields are rejected. A hesperd host refuses every method
but `snapshot`, `ping`, `devices.request` and the handshake in plaintext
(`e2e_required`).

Successful response:

```json
{"v":2,"type":"result","id":"UNIQUE_CLIENT_REQUEST_ID","result":{}}
```

Failure:

```json
{"v":2,"type":"result","id":"UNIQUE_CLIENT_REQUEST_ID","error":{"code":"connection_lost","message":"Connection lost; outcome may be unknown. Refresh before retrying."}}
```

Codes include `unavailable`, `busy`, `invalid_request`, `unsupported`,
`expired`, `timeout`, `connection_lost`, `shutdown`, `forbidden` (the
host refused the request's device signature or rights; see
[Device keys](#device-keys)), the channel's `e2e_session`, `e2e_binding`,
`e2e_required`, `integrity` and `duplicate_request` and `key_changed` (see
[End-to-end channel](#end-to-end-channel)), hesperd's own codes for agents
(`not_found`, `invalid`, `exists`), and for moves `too_large`, `integrity`,
`handoff_failed` and the codes of `internal/handoff` (`branch_diverged`,
`dirty_target`, `missing_project`, …). Protocol violations may close the
connection. Treat all timeout/disconnection outcomes for mutations as
uncertain. Never automatically replay input, submit, or interrupt requests
after reconnecting.

## Device keys

Hosts do not take the relay's word for who sent a request. Each controller
device has two P-256 keys: the **device key** signs ordinary requests, the
**strong key** (Touch ID on a Mac) signs starting a shell on another
machine (`agents.spawn` of a shell). On macOS both live in the
Secure Enclave behind the `hesper-keys` helper; elsewhere they are software
keys (PEM PKCS#8, mode 0600) in `~/.local/state/hesper/device.key` and
`device-strong.key`. A host keeps the keys it approved in
`~/.local/state/hesper/controllers.json` (mode 0600) with the rights each may
use. Contract: [remote-shell-contract.md](../../docs/remote-shell-contract.md), Part K.

**Signature.** `auth.sig` is the standard-base64 DER ECDSA P-256 signature
of SHA-256 of this message (lines joined by `\n`, no trailing newline):

```text
ghosty-req-v1
<host machine ID (the request's machineId)>
<method>
<auth.ts, Unix milliseconds, decimal>
<auth.nonce: 16 random bytes, standard base64 with padding>
<SHA-256 of the params bytes, lowercase hex>
```

The params bytes are the request's `params` in normalized form: compact
JSON, with `<`, `>`, `&`, U+2028 and U+2029 escaped (`\u003c` etc.; what
Go's `encoding/json` writes for a raw message, so the relay's re-encoding
does not change it). Senders sign and send that form; hosts normalize what
they receive before hashing. Params must be valid UTF-8 and may not repeat
an object key (refused). `auth.device` is the controller's relay device ID
and must equal the `controllerId` the relay routes the request with.
`pkg/devicekey` has a golden vector (`devicekey_test.go`).

**Host checks**, in this order; any failure answers `forbidden` and
appends a line to `~/.local/state/hesper/audit.log`:

1. `controllers.json` is readable, private and valid (else everything but
   `snapshot` is refused).
2. The method is in the rights table; `auth` is present and well formed.
3. `auth.device` is approved and has the method's right.
4. `|host clock − ts| ≤ 60 s`.
5. The signature verifies with the device key, or the strong key
   (starting a shell accepts only the strong key).
6. The nonce was not seen from that device in the last 10 minutes (kept in
   `nonces.log`, so a restart does not reopen a replay window).

| Right | Methods |
| --- | --- |
| `observe` | `agents.list`, `agents.link`, `agents.attach` with mode `ro`, `agents.plan`, `agents.probe`, `agents.screen`, `agents.export`, `download`, `job`, `projects.recent`, `profiles.list`, `fs.stat` (and the unsigned `snapshot`, `ping`) |
| `answer` | `agents.answer` |
| `type` | `agents.input`, `agents.attach` with mode `rw` |
| `transfer` | `agents.spawn`, `agents.stop`, `agents.resume`, `agents.remove`, `agents.rename`, `agents.import`, `transfer`, `projects.clone` |
| `shell` | `agents.spawn` of a shell (kind `shell`, or no kind and a profile named like one; **strong key only**); every method and link event about a shell agent, in addition to its own right (and only on a host started with `--allow-shell`) |

`snapshot` is never signed and stays open to every controller the relay
routes; it carries ids, kinds and states, never names, tasks or terminal
output. A link (`agents.link`) is authorized once and checked every few
seconds against `controllers.json` (a revoked device loses it); each attach
channel on it is a signed `agents.attach` of its own. The bytes on a link
are not signed but sealed (end-to-end channel or direct path), so a relay
can neither read nor inject them.

**Enforcement.** A host enforces device keys once `controllers.json`
exists, which the first approval creates; revoking the last device does not
switch it off. Until then requests are allowed and audited as
`request.unauthenticated` ("unauthenticated request (enforcement off:
approve devices with hesperctl approve)", once a minute per device and
method), so existing setups keep working; shells are refused regardless.
`hesperd serve --require-device-keys` enforces from the start. Hosts must run
behind a relay that forwards `auth` (this version); an older relay drops it
and an enforcing host then refuses every request.

**`devices.request`** (unsigned) asks a host to approve the sender:
`name` (1–40 printable characters), `key` and `strongKey` (base64 DER
SubjectPublicKeyInfo, P-256, different), `hardware` (the sender's claim that
the keys are in a Secure Enclave), `rights` and optionally `e2eKey` (the
controller's end-to-end static key, base64 X25519) with `e2eBinding` (the
device key's signature of it, see below), which the host keeps with the
approval. It only adds a pending
request (`pending-approvals.json`, at most 5, 10 minutes, one per controller,
a controller at most every 5 s and 10 requests a minute in all: `busy`) and
shows a macOS notification on the host. Asking again with the same keys
returns the same pending request; a denied device is refused (`forbidden`)
for 10 minutes. The result carries `hostKey`, the host's X25519 transfer
key, and `code`, the first 30 bits of SHA-256(host key ‖ key DER ‖ strong
key DER) in base32 as `ABC-DEF`. The controller computes the code itself
from the keys it holds and the host key (pinned in `trusted-hosts.json` like
for transfers) and shows it; the user approves on the host only when both
codes match, so a relay that swaps any key is caught. Approval happens only
on the host: `hesperctl approve CODE|NAME` (or `--deny`),
`hesperctl devices-local [--revoke NAME]`. A device
without hardware keys is not granted `shell` unless `--allow-software-shell`.

## End-to-end channel

The relay forwards, it does not read: between a controller and a host the
requests, their results and terminal streams travel in a channel only the
two ends can open (contract: [remote-shell-contract.md](../../docs/remote-shell-contract.md),
Part N). The relay still sees who talks to whom, when, and how much, and
everything outside the channel (published snapshots, `devices.request`).

**Keys.** The host's static key is its X25519 transfer key (`transferKey`,
also published as `e2eKey` with `capabilities.e2e: true`); controllers pin it
in `~/.local/state/hesper/trusted-hosts.json` when they pair (`hesperctl
pair-host`, whose approval code covers it) or first meet the host, and refuse
any other key (`key_changed`) until `hesperctl trust --reset`. A controller's
static key is an X25519 key in `~/.local/state/hesper/e2e.key` (mode 0600,
created on first use, never leaves the machine), bound to its P-256 device
key by `binding`: the base64 DER ECDSA signature by the device key (not the
strong one) of SHA-256(`"ghosty-e2e-bind-v1\n" + base64(static key)`).

**Handshake.** `Noise_IK_25519_ChaChaPoly_BLAKE2s` with the prologue
`"ghosty-e2e-v1\0" + host machine ID + "\0" + controller device ID`:

```json
{"v":2,"type":"request","id":"…","machineId":"MACHINE_ID","method":"e2e.hello","params":{"v":1,"h":"BASE64_NOISE_MESSAGE_1"}}
{"v":2,"type":"result","id":"…","result":{"h":"BASE64_NOISE_MESSAGE_2"}}
```

Message 1 carries (encrypted) `{"v":1,"ts":UNIX_MS,"binding":"…"}`; message
2 `{"v":1,"session":"32 HEX","require":BOOL}`. The host accepts the
controller's static key only when `binding` verifies with that device's
approved `key` in `controllers.json` (`e2e_binding` otherwise); before any
device is approved (no enforcement) an unbound key is accepted. A device
that is not approved on an enforcing host is `forbidden`. A first message
older than ±2 minutes, or one seen before (same ephemeral key), is refused.
Every session and refusal is audited (`e2e.session`, `e2e.refused`).

**Requests.** Each request is one relay request:

```json
{"v":2,"type":"request","id":"…","machineId":"MACHINE_ID","method":"e2e","params":{"s":"SESSION","n":7,"c":"BASE64_CIPHERTEXT"}}
{"v":2,"type":"result","id":"…","result":{"n":3,"c":"BASE64_CIPHERTEXT"}}
```

`c` is ChaCha20-Poly1305 (the session's key for that direction from the
handshake, nonce `n`, associated data `"ghosty-e2e-v1\0" + session`) of
`{"id","method","params","auth"}` toward the host and `{"id","result","error"}`
back; `id` is random and repeated in the answer, so answers cannot be
swapped. The inner request is exactly a plaintext request, part K signature
included, and is checked the same way. Counters are explicit (the relay may
drop a request); each side accepts a counter once, within a window of 1024
behind the newest. Errors outside the channel: `e2e_session` (unknown or
expired session: nothing ran; the controller handshakes again and resends
the same inner request once), `integrity` (a frame that does not
authenticate or repeats a counter: nothing ran; audited), `duplicate_request`
(an inner `id` already received in the last 10 minutes). Sessions end after
30 minutes unused or 24 hours; controllers handshake again every hour (or
after 2^20 requests).

**Terminal streams.** An `agents.link` that came through the channel
returns `e2e` (32 random bytes, base64) in its ticket. Both ends derive
one key per direction with HKDF-SHA256 (secret, salt = stream ID, info
`"ghosty-stream-v1 host"` / `"ghosty-stream-v1 controller"`) and send every
frame as a binary message: ChaCha20-Poly1305 with an implicit counter from 0
per direction and associated data `"ghosty-stream-v1\0" + stream ID` over
one type byte (0 terminal bytes, 1 resize: columns and rows as big-endian
uint16) and the payload, at most 32 KiB − 17 bytes. A frame that does not
authenticate (altered, dropped, repeated, reordered) ends the stream; on a
sealed stream text messages are refused. The relay forwards these frames as
it forwards plaintext terminal bytes.

**Negotiation.** A controller uses the channel when the host advertises
`capabilities.e2e` or it used the channel with that host before
(`trusted-hosts.json` records `e2e`, so a relay that hides the capability
cannot downgrade it); hesperd sends every request with `RequireE2E`, so
nothing goes out in plaintext (`e2e_required`). A hesperd host refuses
plaintext for everything but `snapshot`, `ping`, `devices.request` and the
handshake (`e2e_required`, audited "plaintext refused").
Audit lines carry `"e2e": true|false` for requests, and `"route":
"direct"|"relay"` (see [Direct path](#direct-path)).

## Direct path

Between a controller and a host on the same network, requests and terminal
streams can bypass the relay ([design and security](direct-path.md)). The
relay is not involved and did not change: everything below travels inside
the end-to-end channel or on a TCP connection between the two machines.

**Advertising.** A host that listens sets `capabilities.direct: true` in its
snapshots (with `capabilities.e2e`); its addresses are never published.
`direct.offer` (params `{}`, unsigned like `e2e.hello`) is answered only
inside the relay's end-to-end channel, only on a session whose controller
key is bound to an approved device (else `forbidden`; in plaintext
`e2e_required`; never on the direct path itself):

```json
{"v":1,"addrs":["10.2.32.152:53122","[fe80::1c2a:5ff:fe3b:9a10]:53122"],"token":"BASE64_16_BYTES","expires":1791036424000}
```

Without a listener: `{"v":1,"addrs":[],"reason":"firewall"}`. The host
listens only on private addresses of its interfaces (10/8, 172.16/12,
192.168/16, 169.254/16, fc00::/7, fe80::/10, link-local without zone in
offers); controllers dial only such addresses. Each offer refreshes the
device's offer time; a token opens connections for 10 minutes.

**Connection.** TCP. The controller writes `GHOSTYD1`, then a two-byte
big-endian length and the first message of
`Noise_IK_25519_ChaChaPoly_BLAKE2s` (at most 1024 bytes), prologue
`"ghosty-direct-v1\0" + host machine ID`, responder static key = the
pinned host key. Its payload:

```json
{"v":1,"ts":UNIX_MS,"device":"CONTROLLER_DEVICE_ID","binding":"…","token":"BASE64"}
```

or, for a terminal stream, `"stream":"TICKET_ID"` instead of `token`
(exactly one of them; unknown fields refused). The host accepts only when
`device` is approved in `controllers.json`, `binding` verifies with its
device key for the controller's static key (unbound keys are refused here,
with or without enforcement), `ts` is within ±2 minutes, the ephemeral key
is new, and `token` is from a live offer to that device (or `stream` a
ticket the host issued to it on the direct path). Then it answers with the
second message (payload `{"v":1,"require":BOOL}`); on any refusal it closes
without answering.

**Records.** Afterwards each record is a two-byte length and a Noise
transport message (implicit counters per direction; a record that does not
authenticate ends the connection). Record plaintext: one flag byte (1:
more records follow) and a piece of a message; messages are one kind byte
and a body, at most 2 MiB:

| Kind | Direction | Body |
| --- | --- | --- |
| `q` | controller → host | `{"id","method","params","auth","deadline"}`: the inner request of the channel, part K signature included; `deadline` Unix ms (capped at 30 s) |
| `r` | host → controller | `{"id","result","error"}` |
| `p` / `o` | either | ping (at most 64 bytes) / pong with the same body |
| `d` | stream, both | terminal bytes (at most 32 KiB) |
| `z` | stream, controller → host | resize: columns, rows (uint16 BE) |

Requests on one connection run in order, serialized with the relay's.
Inner request IDs are shared with the channel (an ID seen on either path
is `duplicate_request`). `e2e.hello`, `e2e`, `direct.offer` and
`devices.request` are `invalid_request` here. `agents.link` returns a
ticket with `"direct":true`: the controller opens a second connection with
`stream` = the ticket's ID to the same address within 15 seconds; the
stream carries `d`/`z` messages and needs no secret of its own. Audit lines
carry `"route":"direct"` (the relay's: `"relay"`); handshakes are audited
as `direct.session` / `direct.refused` (at most once a minute per peer),
closures as `direct.closed`.

**Lifetime.** Controllers ping every 2 s and fall back to the relay when a
ping goes unanswered for 2 s or the connection fails; a request that may
have reached the host is resent through the relay only when idempotent
(`snapshot`, `ping`, `agents.list`, `agents.plan`, `agents.probe`,
`agents.screen`, `download`, `projects.recent`, `profiles.list`, `fs.stat`; identical inner request), anything else fails `connection_lost`. Controllers fetch a new
offer through the relay every 10 minutes; the host closes direct
connections of a device that has not done so for 30 minutes, or that it no
longer approves.

**Compatibility.** Older hosts do not advertise `direct`; older controllers
ignore it. Neither changes the relay protocol, and the relay needs no
update.

## Agents

A hesperd host (rebuild contract part R) serves its registry with the
local daemon's methods, signed and inside the channel. Params and results
are those of the local socket ([rebuild contract](../../docs/rebuild-contract.md#local-socket-and-wire-format));
ids may be full (`M/a7f3k2`, the host's own short name) or local
(`a7f3k2`); results carry the host's ids, which the controlling hesperd
renames to its own machine naming.

| Method | Params | Result |
| --- | --- | --- |
| `agents.list` | `{}` | `[Agent]` (shells only for callers with the `shell` right on a host with `--allow-shell`) |
| `agents.spawn` | `SpawnParams` (`machine` empty or the host's) | `Agent` |
| `agents.input`, `agents.answer` | as locally | `{}` |
| `agents.stop` | `{id, wait?}`: `wait` (a move) returns once the process ended | `{}` |
| `agents.resume`, `agents.rename` | as locally | `Agent` |
| `agents.remove` | `{id}` | `{}` |
| `agents.link` | `{}` | a terminal ticket: the [link](#links) |
| `agents.attach` | `{link, ch, mode, request}`: `request` is the local attach request (every option of it, passed through), `mode` repeats its mode for the rights table | `{}`; the attach runs on channel `ch` of the caller's link |
| `agents.plan` | `{id}` | `{project, home, commits}` (a move's source: commits the target may have) |
| `agents.probe` | `{path, home, commits}` | `{exists, has}` (a move's target) |
| `agents.screen` | `{id, rows?, scrollback?}` | `{text, rows, cols, cursor?, alt?}`: the terminal as plain text (right `observe`) |
| `projects.clone` | `{url}` | `{path}` |
| `projects.recent`, `profiles.list` | `{}` | as locally |
| `fs.stat` | `{path}` (clean absolute) | `{exists, isDir}` (a draft's folder on this machine; right `observe`) |

### Links

One link per controller and host: a terminal stream (`agents.link`
returns a ticket like the former `terminal.open`: through the relay a
`/v1/terminal` socket sealed with the ticket's `e2e` secret, on the direct
path a direct connection of its own). Its bytes are frames:

```text
kind (1 byte) | channel (uint32 BE) | length (uint32 BE) | payload (≤ 1 MiB)
```

| Kind | Channel | Direction | Payload |
| --- | --- | --- | --- |
| `E` | 0 | host → controller | JSON event: first `{"machine","list":[Agent…],"full":true}`, then `{"changed":Agent}` or `{"removed":"id"}` at every change |
| `D` | > 0 | both | an attach connection's bytes, exactly the local attach protocol after the request line: the host's reply line and frames, the controller's frames |
| `X` | > 0 | both | the attach on this channel ended |

The controller opens channel `ch` with a signed `agents.attach {link, ch,
mode, request}`; the host bridges it to the agent's PTY like a local
attach (redraw first, the size owner rule, viewers that fall behind get
SIZE and a fresh redraw, EXIT last). One link per pair keeps within the
relay's four streams per host however many tiles show remote agents.
A host keeps at most four links per device (the oldest goes) and 256
channels per link.

## Moves

`agents.move` on the controlling hesperd moves an agent with its
conversation and code between any two machines: it stops the agent on the
source (`agents.stop {wait: true}`), asks the source's plan and the
target's probe, has the source pack a bundle (`manifest.json`,
`transcript.jsonl`, `code.bundle`; see `internal/handoff`), carries it
(a remote source: `agents.export` + `download`; a remote target:
`transfer` + `agents.import`, polled with `job`), and removes the source's
agent once the target resumed it (on failure the source is resumed).

`machine` describes the host: `agents` (live agents), `memoryUsed` and
`battery` (0 to 1), `onBattery`, `lidClosed`. Values the host cannot read
are omitted. The host samples them at most every 10 seconds.

**Encryption.** The host keeps an X25519 key (`host.transfer.key` next to its
credentials, mode 0600) and publishes the public key as `transferKey`. A
sender creates one ephemeral key per upload. Each file gets an AES-256-GCM
key from HKDF-SHA256 over the shared secret, salt `epk || transferKey`, info
`"ghosty-transfer-v1\0" + upload + "\0" + name`. Chunk `i` uses the nonce
`0x00000000 || uint64_be(i)` and associated data
`"ghosty-transfer-v1\0" + upload + "\0" + name + "\0" + uint64_be(i) + last`
(one byte, 1 on the last chunk), so chunks cannot be reordered, moved between
files or truncated. The files travel inside the end-to-end channel as well.

**`transfer`** sends one sealed chunk. `upload` is 1 to 64 letters, digits,
`-` or `_`; `name` is `manifest.json`, `transcript.jsonl` or `code.bundle`;
`data` is the base64 chunk. Every chunk but the last holds exactly 512 KiB
of plaintext; `offset` is the plaintext offset, a multiple of 512 KiB, and
chunks arrive in order. Repeating an earlier offset discards what followed
it. The last chunk carries `sha256` (hex, of the whole plaintext) and `epk`
(base64 ephemeral public key); the host then decrypts, verifies and stores
the file. Limits: 512 MiB per upload (`too_large`), 8 uploads not yet
imported (`busy`). Uploads stay in the state directory's `uploads/<upload>/`
(0700) for 24 hours.

**`agents.import`** `{upload}` queues the import of a complete upload and
returns `{"job"}` at once; **`job`** `{job, cancel?}` reports `state`
(`queued`, `running`, `done`, `failed`), and when done the started `Agent`
as `result`; failures carry `error` (`code`, `message`). An imported agent
keeps its local id when it is free. A job that was running when the host
stopped is reported failed (`interrupted`), never rerun.

**`agents.export`** `{id, have?, key}` packs agent `id` (incremental from
`have`, commits the target has) for the requester's ephemeral X25519 key
and answers `{"download":"dl-…","state":"ready","id":"ho-…","files":[{"name",
"size","sha256"}]}`, or `{"download","state":"packing"}` when packing
outlasts the request (ask again with only `download`). **`download`**
`{download, name, offset}` returns chunk `offset / 512 KiB`: `data` (sealed)
and `last`. The key reverses the upload's roles: HKDF-SHA256 over the X25519
secret of the host's transfer key and the requester's ephemeral key, salt
`key || transferKey`, info `"ghosty-download-v1\0" + download + "\0" +
name`. Once every file's last chunk was served the export is deleted; a
host restart drops the others (their keys lived in memory).

## Host publication

Host-only message:

```json
{"v":2,"type":"snapshot","result":{"runtimeId":"...","short":"M","terminals":[],"capabilities":{"agents":true,"e2e":true}}}
```

Host publications are full snapshots, not deltas (the relay keeps the last
one for new controllers). hesperd publishes about 15 ms after an
agent changed (bursts coalesce; publications at least 50 ms apart) and at
its interval as a safety net; unchanged snapshots are not sent. Controllers
take agent changes from their [link](#links), not from snapshots.

## Client lifecycle

On foreground/resume, establish a new authenticated connection and refresh
inventory. Discard stale runtime targets and reconstruct the screen from the
latest state. A second connection with the same enrollment replaces the first.
Separate devices should have separate enrollments. The relay does not buffer
offline commands or terminal captures for later delivery.

Ping/pong detects broken connections. The Go controller SDK exposes inventory
updates and explicit RPC calls; it does not reconnect or retry mutations on the
client's behalf. An app should separately manage connection state and drafts,
keeping an unsent draft distinct from an ambiguously delivered command.

The CLI `watch` reconnects observation automatically; raw SDK callers own renewal
and reconnect policy. Host agents renew credentials on reconnect. Active sockets
close at access expiry, and removed users cannot renew or reconnect.


## Terminal streams

`agents.link` uses the relay's [terminal stream](terminal-streaming.md)
bridging: the host dials `/v1/terminal` with the ticket, the controller
too, and the relay forwards binary frames between them without reading
them (they are sealed).
