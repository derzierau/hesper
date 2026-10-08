# Direct path

When a controller and a host are on the same network (the laptop and the
Mac mini in one office), every keystroke and request used to go laptop →
the relay → mini and back: about 40 ms of internet round trip
where the two machines are 4–10 ms apart. The direct path lets them talk
directly, with the relay as the place where they find each other and the
fallback when they cannot.

What goes direct: every request between two hesperd (agents methods,
moves, pings) and the link between them (agent events and every attach;
hesperd moves a link that started on the relay to the direct path as soon
as the path is up). What stays on the relay: presence, the published
snapshots, device approval (`devices.request`), and the end-to-end
channel through which the direct path is set up and refreshed.

Code: `pkg/direct` (wire format, address rules), `internal/host/direct.go`
(listener), `pkg/client/direct.go` (controller), `internal/gateway` and
`cmd/hesperd` (`serve --direct`, `--direct-port`), `internal/remote`
(links, routes). Wire details: [protocol](protocol.md#direct-path).

## How a controller finds and uses it

1. The host listens on TCP on each **private address** of its own
   interfaces (RFC 1918 IPv4, IPv4 link-local, IPv6 unique local, IPv6
   link-local; never a wildcard and never a public address; tunnels and
   point-to-point interfaces such as VPNs, AWDL are skipped), on one random
   port (or `--direct-port`). It follows interface changes every 10 s and
   advertises only `capabilities.direct: true` in its snapshot. No address
   is ever published.
2. A controller with the end-to-end channel (part N) to that host asks
   `direct.offer` **inside the channel**. The host answers only an approved
   device whose channel key is bound to its device key: its addresses
   (`ip:port`) and a random 16-byte token, valid 10 minutes.
3. The controller dials every eligible candidate (private addresses only,
   whatever the offer says; link-local IPv6 once per own interface), the
   next 50 ms after the previous, and keeps the first that completes the
   handshake as the **pinned** host, all within 300 ms. Otherwise the relay
   keeps doing everything and the controller tries again in 15 s, doubling
   up to 5 minutes, and at once when its own addresses change (a new
   network).
4. On the connection the controller sends the same inner requests it would
   seal for the relay's channel (`{id, method, params, auth}`, part K
   signature included) and the host answers `{id, result, error}`. An
   `agents.link` that came this way returns a ticket with `direct: true`;
   the controller opens the stream as a second direct connection to the
   same address, naming the ticket.
5. Pings every 2 s (2 s timeout) watch the connection. When it fails, the
   route is the relay again at once, and the path is probed again 2 s later.
   Every 10 minutes the controller fetches a new offer through the relay.

hesperd's controller role does this for every other machine. `hello`
gives each other online machine `route: "direct"` or `"relay"` and
`rttMs`, the median round trip of the last pings on that route;
`hesperctl machines` has a ROUTE column; `hesperctl trust` a DIRECT column
(when this Mac last reached the host directly). Host audit lines carry
`"route": "direct"|"relay"`.

## Requests in flight when the connection fails

* Not yet written: sent through the relay, as if nothing happened.
* Possibly delivered, idempotent (`snapshot`, `ping`, `agents.list`,
  `agents.plan`, `agents.probe`, `download`, `projects.recent`,
  `profiles.list`): resent through the relay as the **identical**
  inner request (same inner ID, same signature). Host and relay share the
  memory of inner IDs, so if the host did run it, the resend is refused as
  `duplicate_request` (part N's protection); part K's nonce cache would
  refuse it too.
* Possibly delivered, anything else (input, keys, answers, transfers,
  shells…): `connection_lost`, outcome unknown, never replayed, exactly as
  when the relay connection drops mid-request. Callers refresh first.

## Security

The direct path changes the route, not who may do what.

* **Same cryptography as part N.** `Noise_IK_25519_ChaChaPoly_BLAKE2s`
  with the host's static key as pinned in `trusted-hosts.json` (the key
  part K's approval code covers) and the controller's static key, which the
  host accepts only with a valid binding signature by the device key it
  approved. The prologue `ghosty-direct-v1\0<machine ID>` keeps relay
  handshakes and direct ones apart. Each connection has fresh ephemeral
  keys (forward secrecy); records use implicit counters, so any altered,
  dropped, repeated or reordered record ends the connection.
* **Stricter admission than the relay's channel.** No unbound keys, even
  on a host that does not enforce device keys yet (the direct path needs an
  approved device); a token from an offer the controller fetched through
  the relay within 10 minutes (or, for a stream, a ticket the host issued to
  that device); timestamp within ±2 minutes; ephemeral keys remembered
  (replays refused).
* **Same authorization per request.** Inner requests run through the same
  `perform`: part K signature, rights, ±60 s, nonce cache, strong key
  (Touch ID) for starting a shell, the plaintext refusal satisfied (the
  path is end to end), the same audit with `route: "direct"`. Requests of
  both paths are serialized as before.
* **Strangers on the network learn nothing.** Before the handshake verifies
  the host parses only an 8-byte magic, a 2-byte length (at most 1024) and
  one Noise message; without the host's public key (relay-visible, not
  LAN-visible) a stranger cannot even produce a message the host can
  decrypt. Every refusal closes the connection without a byte. Limits: 16
  handshakes at once, 3 s each, 20 connections per 10 s per peer address, a
  peer with 5 failures in a minute ignored for a minute, 64 connections in
  all and 16 per device, 2 MiB per message, 16 queued requests per
  connection. Refusals are audited (`direct.refused`) at most once a minute
  per peer. The worst a stranger can do is keep the listener busy, which
  costs the relay route nothing.
* **The relay cannot redirect anyone.** Offers travel sealed in the
  channel. Even a forged address would only reach a machine that cannot
  complete the handshake as the pinned key: the controller falls back to
  the relay and sends nothing (no request is written before the host proved
  its key).
* **Relay revocation still matters.** A host closes direct connections of a
  device it no longer approves (checked every 5 s) and of a device that has
  not fetched an offer through the relay for 30 minutes, so a device the
  relay no longer serves loses the direct path within 30 minutes.

What a LAN observer sees: that two machines talk over TCP, when and how
much (as the relay operator sees for the relay route). The relay now sees
less: no request traffic, only the offer every 10 minutes, presence and
snapshots.

## macOS firewall

The direct path needs an **incoming** connection on the host only (the
Mac mini); controllers dial out, which the firewall never asks about.

macOS's application firewall is off by default. When it is on, an unsigned
(ad-hoc signed) program that accepts connections makes macOS show "Do you
want the application hesperd to accept incoming network connections?" at
the Mac, and a rebuilt binary (new code signature) can ask again. A
LaunchAgent must not raise such dialogs, so `hesperd serve --direct=auto` (the
default) checks with the read-only queries of
`/usr/libexec/ApplicationFirewall/socketfilterfw` first:

| Firewall | hesperd |
| --- | --- |
| off (`--getglobalstate`: disabled) | listens |
| on, hesperd listed as allowed (`--listapps`) | listens |
| on, hesperd not listed, downloaded signed software automatically allowed, and its Developer ID signature verifies | listens without changing firewall settings or requiring admin rights |
| on, unsigned/ad-hoc signed hesperd not listed, automatic allowance disabled, explicitly blocked, or "block all" | does not listen; logs the fix once; controllers silently use the relay |

Signed release installs need no administrator firewall change when
“Automatically allow downloaded signed software” is enabled. The daemon
verifies the executable's signature against Apple's Developer ID Application
certificate requirement; an ad-hoc signature does not qualify. Explicit
per-app blocks and “Block all incoming connections” still take precedence.

If automatic allowance does not apply, an administrator can explicitly allow
the daemon (repeat after an update of an ad-hoc signed build):

```sh
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add ~/Applications/Hesper.app/Contents/MacOS/hesperd
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp ~/Applications/Hesper.app/Contents/MacOS/hesperd
# then restart hesperd: launchctl kickstart -k gui/$(id -u)/de.olezierau.hesperd
```

`--direct=on` listens regardless (accepting that macOS may ask once at the
Mac); `--direct=off` turns the path off. When a listener is up but blocked
anyway (another firewall, a network that isolates clients), dialing fails
within 300 ms and the relay is used without any visible error.

## Not done (possible later)

* Internet-direct (NAT hole punching, IPv6 global addresses) and Tailscale
  style CGNAT addresses (100.64/10) are not candidates.
* Reverse direction (controller listens) for hosts behind a firewall.
* Snapshots and presence over the direct path (they stay on the relay).
