# Remote shell: contracts between the parts

> Written for the tmux setup. Since the rebuild (docs/rebuild-contract.md)
> parts K, N and D serve hesperd's host and controller roles (part R). Part
> SH (tmux remote shells) and the cockpit, tmux, fleet and menu-bar surfaces
> are gone (part C removed them; part SH's text is in Git history): a remote
> shell is an `agents.spawn` of kind `shell` on another machine (shell
> right, Touch ID, the host's `--allow-shell`).

Approved plan: docs/remote-shell-plan.html (removed with the tmux setup;
in Git history). Decisions: Touch ID for every new
shell (and for the first answer/typing per agent after the controller slept),
no per-shell approval on the host, a full user shell, steps 1–2 before the
shell is enabled for real use.

Threat model: the relay (server, database, backups, operator) is untrusted
for content and for commands. Hosts trust only device keys they approved
locally.

## Part K (step 1): device keys, host allowlist, signed requests

- Controller device key: P-256 in the Secure Enclave on macOS via a small
  Swift helper `hesper-keys` (built by `relay/Makefile`, installed to
  ~/.local/lib/hesper/), storing the SE key blob (dataRepresentation, only
  usable on this Mac) at ~/.local/state/hesper/device.key (0600). Two keys:
  `device` (no biometry, signs normal mutating requests) and `device-strong`
  (access control: biometryCurrentSet or devicePasscode fallback; signs
  shell.open and "first input after sleep"). Fallback when no Secure Enclave
  (Linux, CI): software P-256 keys in files with 0600, marked
  `"hardware": false` and only granted rights that don't include shell unless
  the host owner explicitly allows it.
  Helper CLI: `hesper-keys public [--strong]` (prints base64 SPKI),
  `hesper-keys sign [--strong] [--reason TEXT] < digest` (prints base64 DER
  signature; --reason is the Touch ID prompt text).
- Host allowlist: ~/.local/state/hesper/controllers.json (0600):
  `{"version":1,"controllers":[{"device":"<relay device id>","name":"laptop",
  "key":"<b64 SPKI>","strongKey":"<b64 SPKI>","hardware":true,
  "rights":["observe","answer","type","transfer","shell"],"approved":unix,
  "approvedBy":"local"}]}`.
- Approval: a controller asks a host to be approved with
  `request method=devices.request params={name,key,strongKey,hardware,rights}`;
  the host shows a local prompt (Hesper popup on the attached client, else a
  macOS notification + `hesperctl approve` CLI on the host) with a 6-char code
  derived from both keys (also printed on the controller); only a local "y"
  writes controllers.json. `hesperctl devices-local [--revoke NAME]` on the
  host lists/revokes. Migration: `install.sh` prints how to approve the
  existing laptop/mini pairs once.
- Signed requests: every request except `snapshot` carries
  `auth: {device, ts (unix ms), nonce (16 random bytes b64), sig}` where sig
  is over SHA-256 of `ghosty-req-v1\n<host machine id>\n<method>\n<ts>\n<nonce>\n<sha256(params JSON as sent)>`,
  with the strong key for methods/rights that require it. Host verifies:
  allowlisted key, right for the method, |now−ts| ≤ 60 s, nonce unseen in the
  last 10 minutes (persist across restarts), signature. Refusals return
  `forbidden` and are written to ~/.local/state/hesper/audit.log (JSON lines).
- Host API for other parts (Go, internal/host): `Authorize(ctx, msg) (Caller,
  error)` where `Caller{Device, Name, Rights, Strong bool, Hardware bool}`;
  methods declare the right they need in one table:
  observe: snapshot, capture, job, probe, export, download;
  answer: ack, respond; type: input, key; transfer: transfer, spawn, stop;
  shell: shell.open, shell.close (strong signature required).
- Audit: `Audit(event string, fields map[string]any)` appends JSON lines
  `{at, event, device, name, method, ok, detail}`.

### Part K as built

Code: `relay/macos/hesper-keys/main.swift` (helper), `relay/pkg/devicekey`
(signing, verification, rights table, approval code, software keys),
`relay/internal/host/{authorize,devices}.go` (host), `relay/pkg/client`
(signing in `Controller.Request`), `relay/cmd/hesperctl/devices.go`
(`pair-host`, `approve`, `devices-local`). Wire format:
`relay/docs/protocol.md#device-keys`.

- **Helper.** `hesper-keys info` (JSON `hardware`, `secureEnclave`,
  `biometry`, `software`, `device`, `deviceStrong`), `public [--strong]`,
  `sign [--strong] [--reason TEXT]`; the digest on stdin is exactly 32 raw
  bytes or 64 hex digits plus at most one newline, anything else is refused.
  Keys are created on first use (`link(2)`, never replaced), files must be
  0600 and owned by the user. `device` uses `privateKeyUsage` with
  `AfterFirstUnlockThisDeviceOnly` (usable by background commands);
  `device-strong` uses `privateKeyUsage` + `biometryCurrentSet` (re-enrolling
  fingers makes it unusable: pair again) or `userPresence` without biometry,
  `WhenUnlockedThisDeviceOnly`. `HESPER_KEYS_SOFTWARE=1`: PEM PKCS#8 files
  with the same names, no user presence. `HESPER_STATE_DIR` moves the files.
  Lookup by Go: `$HESPER_KEYS`, `~/.local/lib/hesper/hesper-keys`, next to
  the executable; without a helper on a Mac requests go unsigned.
- **Signed message**: `ghosty-req-v1\n<host machine id>\n<method>\n<ts>\n<nonce>\n<hex sha256(params)>`,
  the signature over SHA-256 of it. "params JSON as sent" is pinned down as
  the normalized form (compact, `<>&` U+2028/9 escaped, i.e. what Go writes
  for a raw message; the relay's re-encoding is the identity on it); hosts
  normalize before hashing, and refuse invalid UTF-8 and repeated keys.
  `auth.device` must equal the relay's `controllerId`. Any request may be
  signed with the strong key (`Caller.Strong` reports it); `shell.*` must be.
  Golden vector in `pkg/devicekey/devicekey_test.go`.
- **Rights table additions**: `setPersistence` and writable `terminal.open`
  need `type`, `terminal.open` with `readOnly: true` needs `observe`; methods
  not in the table are refused while enforcing. `snapshot` is not signed.
- **Enforcement** (deviation, fail closed): on as soon as `controllers.json`
  *exists* (created by the first approval), not "has a controller": revoking
  the last device keeps the host closed. Before that: allowed and audited as
  `request.unauthenticated` (once a minute per device and method); `shell.*`
  always refused. `hesperd serve --require-device-keys`, `--state-dir`.
  A damaged, world-readable or ambiguous `controllers.json` refuses all but
  `snapshot`. Hosts must run behind a relay that forwards `auth` (the hub
  now copies it; deploy the relay before the first approval).
- **Approval**: `devices.request {name,key,strongKey,hardware,rights}`
  (`strongKey` required) → `{status:"pending",code,expires,hostKey}` or
  `{status:"approved",name,rights,hostKey}`; code = base32(SHA-256(host
  X25519 transfer key ‖ key DER ‖ strong key DER))[:6] shown `ABC-DEF`. The
  controller pins `hostKey` in `trusted-hosts.json` (same pin as
  transfers) and prints the code it computes itself; it refuses when the
  host's code differs. Max 5 pending, 10 min; per controller one request per
  5 s, 10 a minute overall; re-asking with the same keys returns the same
  request without a new notification; `--deny` blocks that device and key
  for 10 min. Pending file `pending-approvals.json` (0600, `{version,
  pending:[{device,name,key,strongKey,hardware,rights,code,requested,
  expires}], denied}`); controllers and pending changes hold an flock on
  `devices.lock`. Surfaces: macOS notification (osascript, texts as argv),
  CLI `hesperctl approve [--deny] [--rights R,…] [--name N]
  [--allow-software-shell] NAME|CODE`, `hesperctl devices-local [--revoke
  NAME] [--json]`, `hesperctl pair-host --machine M [--rights …] [--name N]
  [--wait D] [--json]`. Default requested rights: observe, answer, type,
  transfer.
- **Control socket**: the control socket's `request` op carries `auth`. The
  calling command signs in its own process (`Controller.Request`; strong key
  for `shell.*`, so Touch ID prompts for the command the user ran) and the
  daemon forwards `params` and `auth` unchanged (`Controller.Forward`). It
  holds no signer for a caller and never signs for one; an unsigned caller
  gets `forbidden` from an enforcing host.
- **Replay cache**: `nonces.log` (0600) lines `<expiry ms> <device>
  <nonce>`, appended (no fsync) before the request runs, reloaded on start,
  compacted; at most 200 000 live nonces (then refused). Clocks must agree
  within 60 s; a Touch ID prompt answered after more than 60 s fails as
  expired.
- **API for N** (`internal/host`): `(*Authorizer).Authorize(ctx, msg)
  (Caller, error)`; `Caller{Device, Name, Rights, Strong, Hardware,
  Verified}` with `Has(right)`; Runner authorizes every request and puts the
  caller into the operation's context: `host.CallerFrom(ctx)`;
  `(*Authorizer).Audit(event, fields)` / `Store.Audit`;
  `(*Authorizer).Enforcing()`; `Store.LoadControllers()` (re-read per
  request, so a revocation applies to the next request).
  Controller side (`pkg/client`): `Controller.SetSigner`,
  `client.WithSigning(ctx, devicekey.Options{Strong, Reason})` (for "first
  input after sleep" and the shell's Touch ID text), `Controller.Forward`.
  `devicekey.MethodRight`, `NeedsStrong`, `SignRequest`, `Verify`,
  `ApprovalCode`.
- **Not done here**: "first answer/typing per agent after the controller
  slept" with the strong key is left to the controller UI (it can pass
  `WithSigning(ctx, {Strong: true})`; hosts accept and report it). Hardware
  is the controller's claim (no attestation). The snapshot carries no
  terminal output; its task, activity and attention texts stay readable by
  the relay until part N.

## Part N (step 2): end-to-end channel (after K)

- Noise IK (github.com/flynn/noise) between controller and host, carried in
  an opaque relay envelope type `e2e` (relay forwards, no server change
  needed if it forwards unknown request methods/results as today; otherwise
  add a generic forward). Static keys: X25519 per device, bound to the
  device's P-256 identity by a signature stored in controllers.json at
  approval time; host static key pinned by controllers (extends today's
  transfer-key pinning). Inside: all RPCs (params, results incl. capture
  text and snapshots' terminal fields) and terminal stream bytes.

### N as built

Code: `relay/pkg/e2e` (handshake, sessions with replay windows, sealed
terminal streams, the controller's static key and its binding, the shared
`trusted-hosts.json` store), `relay/pkg/client/e2e.go` (controller side in
`Controller.Forward`, `EnableE2E`, `WithE2EReport`, `RequireE2E`),
`relay/pkg/client/terminal.go` (sealed streams), `relay/internal/host/e2e.go`
(host side, `--require-e2e`), `relay/internal/host/runner.go` (dispatch),
`relay/cmd/hesperctl` (channel on every own connection). Wire format:
`relay/docs/protocol.md#end-to-end-channel`. Tests: `pkg/e2e/e2e_test.go`,
`internal/host/e2e_test.go`, `internal/transport/e2e_test.go` (in-process
relay behind a recording, tampering proxy: no plaintext at the relay,
flipped bits refused, pin mismatch, no downgrade, `--require-e2e`) and the
handoff tests (all through the channel now).

- **No relay change.** The channel rides on ordinary requests: `e2e.hello`
  (handshake) and `e2e` (one sealed inner request each, result sealed), so
  the hub routes it like any method; sealed stream frames are binary
  messages the terminal bridge already forwards (resize travels sealed in a
  binary frame, so the bridge never sees a text message on such a stream).
  No server deploy is needed for N.
- **Keys.** Host static key = its transfer key (`host.transfer.key`):
  controllers pinned it already (handoffs, `pair-host`) and part K's
  approval code covers it, so existing pairings are verified pins without
  re-pairing; published as `e2eKey` with `capabilities.e2e`. Deviation from
  "separate key": one X25519 key serves ECIES-style transfer sealing (HKDF
  with its own labels) and the Noise DH; both only ever hash DH outputs into
  distinct domains, and a separate key would have needed a new, unverified
  pin for every existing pairing. Controller static key: `e2e.key` in the
  device-key state directory (JSON `{version, private, binding}`, 0600,
  created with `link(2)` like the device keys). Binding: the device key
  (never the strong key: no Touch ID) signs SHA-256(`ghosty-e2e-bind-v1\n` +
  base64(static)); `pair-host` sends `e2eKey`/`e2eBinding` with
  `devices.request` and the host stores `e2eKey` in `controllers.json`
  (informational). Every handshake checks the binding against the approved
  device key; a re-created static key with a valid binding is accepted
  (audited `e2e.key-changed`): the device key is the root of trust. A stale
  binding (`e2e_binding`) makes the controller re-sign once and retry.
  Before any approval (no enforcement) unbound keys are accepted (the
  controller still pinned the host, so the relay cannot read).
- **Pinning.** `trusted-hosts.json` (now `pkg/e2e.Trust`, updated under an
  flock): a host key that differs from the pin (advertised or proven in the
  handshake) is `key_changed`, a hard refusal with the `trust --reset`
  advice. Unpinned hosts are pinned on first use from the snapshot (as
  transfers always did; pair-host's code check catches a swap). `e2e`
  (Unix seconds) marks the first channel with a host: from then on no
  plaintext with it, whatever the snapshot says (downgrade protection).
- **Prologue** `ghosty-e2e-v1\0<host machine ID>\0<controller device ID>`:
  a relay cannot splice a handshake to another host or controller. First
  message payload `{v, ts, binding}` (±2 min, ephemeral keys remembered:
  replays refused); second `{v, session, require}`.
- **Transport.** Explicit counters (the relay may answer `busy` without
  forwarding, results arrive out of order), a 1024-counter sliding window
  per direction, AD `ghosty-e2e-v1\0<session>`; inner `{id, method, params,
  auth}` → `{id, result, error}`. Part K's signature, rights, time window
  and nonce cache apply to the inner request unchanged (defense in depth;
  shell still needs Touch ID). Host keeps per controller at most 16
  sessions (LRU), 256 in all, 30 min idle, 24 h max; controllers rehandshake
  hourly or after 2^20 frames (rekey with fresh ephemerals). An unknown
  session (`e2e_session`, e.g. host restart) is retried once through a new
  session with the identical inner request; inner IDs are remembered 10
  minutes per controller, so a relay that fakes `e2e_session` after the
  request ran gets `duplicate_request`, never a second execution.
  Tampered frames: `integrity`, nothing ran, audited `e2e.refused`; the
  session stays usable.
- **Streams.** Per-stream 32-byte secret from the host inside the channel
  (`terminal.open` result `e2e`); HKDF-SHA256 per direction, implicit
  counters, type byte (data / resize), frames ≤ 32 KiB − 17. Any bad frame
  ends the stream.
- **Policy.** Controller: channel when advertised or used before; `shell.*`
  never in plaintext; `RequireE2E(ctx)` refuses plaintext; through the
  control socket the command passes `requireE2e` and gets `e2e` back in the
  control response. Host: `--require-e2e` (default = `--allow-shell`)
  refuses plaintext for every method needing a right other than `observe`
  and for anything on a shell (`e2e_required`, audited). Older controllers
  keep observing; a host without the flag still serves them fully.
- **Audit.** Lines gained `e2e` (bool) for requests, refusals and shell
  events; `e2e.session` per handshake (device, name, bound or not),
  `e2e.refused` (bad handshake, replay, tampering, duplicate),
  `e2e.key-changed`.
- **UI.** `hesperctl trust` E2E column, `devices-local` E2E KEY column.
- **Still visible to the relay** (honest list): routing metadata, sizes and
  timing; published snapshots (names, task/activity/attention texts,
  branches, machine stats); `devices.request`; anything a peer without the
  channel sends.

## Part D: direct path (after N)

Goal: no relay round trip for requests and terminal streams between
machines on one network; the relay stays rendezvous and fallback.
Design, security argument and firewall notes:
`relay/docs/direct-path.md`; wire format:
`relay/docs/protocol.md#direct-path`.

### D as built

Code: `relay/pkg/direct` (handshake, records, address rules),
`relay/internal/host/direct.go` (listener, offers, admission, sweep),
`relay/internal/host/runner.go` (`perform`: one serialized path for relay
and direct requests; `direct.offer`), `relay/internal/host/firewall.go`,
`relay/pkg/client/direct.go` (probing, routing, failover),
`relay/cmd/hesperctl` (control socket, machines, trust, attach).
Tests: `pkg/direct/direct_test.go` (address filtering, handshake, pin
mismatch, impostor, tampered record, size limits, strict Hello),
`internal/host/direct_test.go` (authorization per request, refusals for
unknown/unbound devices, tokens, streams, strangers and throttling,
revocation and offer freshness, firewall verdicts),
`internal/transport/direct_test.go` (in-process relay behind the tap:
requests, strong shell, transfer with no request at the relay; listener
killed mid-session and back; in-flight requests through a lossy proxy),
`cmd/hesperctl/direct_test.go` (`machines`, a command's request and stream
on the direct path).

- **No relay change**, no new relay message: the offer is an inner request
  of the channel, the rest is between the two machines. No server deploy.
- **Keys and admission**: the channel's keys (host transfer key pinned by
  the controller; controller static key bound to the approved device key).
  Stricter than the channel: unbound keys are refused even before
  enforcement, a token from a recent offer (proof the controller still
  reaches the host through the relay) or a stream ticket is required.
- **Authorization**: unchanged; inner requests carry part K signatures and
  run through the same `perform` (rights, nonces, Touch ID for shells,
  `--require-e2e` satisfied, audit `route: "direct"`); inner IDs shared with
  the channel.
- **Failover**: unwritten → relay; written + idempotent → identical inner
  request through the relay (host dedup); written + mutation →
  `connection_lost`, never replayed. Pings 2 s/2 s, re-probe 2 s after a
  loss, 15 s doubling to 5 min after failed probes, at once on a change of
  local addresses.
- **Revocation**: a host closes direct connections of devices it no longer
  approves (5 s) or that fetched no offer through the relay for 30 minutes.
- **Firewall**: `--direct=auto` listens only when the macOS application
  firewall is off or explicitly allows hesperd (`socketfilterfw`
  read-only queries); otherwise it logs the `socketfilterfw --add /
  --unblockapp` fix and controllers use the relay. `--direct=on|off`,
  `--direct-port`.
- **UI**: `hesperctl machines` ROUTE, `hesperctl trust` DIRECT (last
  direct connection), `ControlResponse.route`/`direct`, control op
  `routes`.
