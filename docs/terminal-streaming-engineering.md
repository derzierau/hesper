# Keeping remote agent terminals responsive

Hesper is an MIT-licensed macOS workspace for Claude Code, Codex and shell
agents. Its interesting systems problem is not generating code: it is keeping
several interactive terminals usable when their processes live on different
Macs and one viewer or network connection stops keeping up.

This note describes the architecture and a reproducible transport measurement
on main at `aa76851f35dc326900f73e2630e29b4dc18a888e`. It is not a benchmark of
the v0.1.4 Homebrew binary or a comparison with other terminal applications.

## The window does not own the process

The native Swift/AppKit/SwiftUI app embeds libghostty for terminal rendering.
It talks only to the local Go daemon, `hesperd`, over a Unix socket. The daemon
owns the PTYs and agent state and acts as the gateway to other Macs. Closing
the UI and terminating the daemon are therefore different operations. This
does not mean an operating-system process survives a reboot.

```text
Hesper.app -> local Unix socket -> local hesperd
                                      |
                               direct or relay link
                                      |
                                 remote hesperd -> PTY -> agent
```

One multiplexed link per controller/host pair carries agent events and terminal
attaches. The relay routes sealed frames; it does not parse terminal escape
sequences. When a direct connection is available, attaches can move to that
path; the relay remains rendezvous and fallback infrastructure.

## A slow tile must not block every agent

Binary messages carry at most 32 KiB of raw terminal data. Each relay direction
holds at most one application frame, allowing socket backpressure to propagate
instead of accumulating an unbounded queue.

Individual attaches have a different recovery boundary. A host viewer that
falls 4 MiB behind receives terminal size and a fresh redraw. On the controller,
an attach whose local client exceeds its 4 MiB queue is dropped and reattached.
The result favors a current screen over replaying every obsolete intermediate
screen. These thresholds bound the attach queue, not total application memory.

Input typed during reattachment is dropped rather than replayed. That is a
deliberate tradeoff: a delayed keystroke can become a command in a different
prompt. Request delivery is not exactly once either: a connection can fail
after input reaches the agent but before the response returns. Unknown outcomes
must remain visible rather than being hidden behind automatic mutation retries.

## Measuring the transport path

Run from `relay/` at the commit above:

```sh
go test ./internal/transport -run '^TestLatency$' -bench Latency -count 1
```

The existing harness creates an in-process relay, two daemon roles, encrypted
channels and a fake shell that echoes input. It measures through the local
daemon socket, including the remote attach bridge. Each controller/relay and
host/relay leg has an injected 20 ms round trip, giving a 40 ms relay-path
network floor. The direct-path comparison is loopback, not a physical LAN.
Device signing uses software keys, not Secure Enclave hardware.

One run on 2026-10-10: Apple M1 Max, macOS 26.7, Go 1.25.5 darwin/arm64.
The machine was in ordinary interactive use, not an isolated benchmark host.

| Measurement | Samples per route | Relay p50 / p95 / p99 (ms) | Direct loopback p50 / p95 / p99 (ms) |
| --- | ---: | ---: | ---: |
| Keystroke to echoed bytes | 300 | 48.4 / 59.9 / 65.4 | 0.8 / 3.5 / 7.9 |
| Open attach to first frame | 5 | 46.5 / 66.0 / 66.0 | 0.7 / 1.1 / 1.1 |
| State change to subscription | 40 | 22.7 / 27.6 / 35.2 | 0.2 / 0.3 / 0.6 |
| Signed agents.list request | 40 | 48.9 / 65.9 / 72.4 | 1.7 / 6.1 / 10.5 |
| Encrypted ping | 40 | 50.3 / 62.4 / 63.3 | 0.6 / 2.3 / 8.4 |

[Raw output](measurements/2026-10-10-terminal-latency.txt) includes host-stage
timings and the passing test result. Small sample counts, especially five
attach opens, make the upper percentiles exploratory. The measurements exclude
app rendering, display refresh, human typing, real model responses, real WAN
loss and hardware-key authorization. They establish neither UI latency nor a
speed advantage over another product.

The lower state-event latency is consistent with the design: events are pushed
from host through relay to controller, while a keystroke echo traverses that
route in both directions. Further work should measure rendering latency,
multiple simultaneously noisy terminals, memory under slow consumers, physical
LAN/WAN reconnects and repeated runs on idle hardware.

## Questions worth discussing

- Is redrawing a lagging interactive viewer preferable to retaining a longer
  byte backlog, and where should that boundary sit?
- How should a terminal UI communicate input dropped during reconnect?
- Which measurements best expose interference between quiet and noisy tiles?

## Trying the released project

[Hesper](https://github.com/derzierau/hesper) v0.1.4 is available through
Homebrew for macOS 14+, Intel and Apple silicon. Install Claude Code/Codex
separately. Local use requires no relay; multiple Macs currently require a
self-hosted relay and device setup. Native review/diffs, checkpoint-based moves
and live-agent forks on main are not features of that Brew release.

See the [architecture](../relay/docs/architecture.md),
[streaming protocol](../relay/docs/terminal-streaming.md) and
[latency harness](../relay/internal/transport/latency_test.go) for implementation
details.
