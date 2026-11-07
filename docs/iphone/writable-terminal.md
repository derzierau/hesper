# A writable terminal on the iPhone

Status, 7 Nov 2026: step 1 shipped as watching only (a read-only Mirror,
no typing yet); Take over is later. The prebuilt libghostty-spm framework
includes LGPL libintl, so before an App Store release the iOS slice needs
the source build the Mac uses.

Research, 2026-10-31. Question: can the iPhone app have a terminal you
type into, as on the Mac (live agent, full keys, selection, scrollback),
instead of the read-only terminal plus reply panel of the concept
(`docs/iphone/concept.md` on `ios/concept`)?

## Answer: yes, in two steps

1. **Mirror** (no protocol change). The phone attaches `rw` without
   `owner` and renders the PTY at the Mac's own grid. It types with the
   full key set and never resizes anything. This is what spec §5.4 already
   plans for M4, made complete.
2. **Take over** (small, additive hesperd and Mac changes). On an explicit
   tap the phone becomes the size owner while its terminal is open. The
   agent redraws at the phone's grid, so text is readable and the TUI is
   laid out for the phone. The Mac's tile turns into a view marked "On
   iPhone" until the phone lets go or the Mac takes the size back.

The reply panel stays: Speak & Type and the native decision buttons are
still the fastest way to answer. The terminal becomes writable
underneath, instead of view-only.

The read-only rule in the concept came from a real constraint ("the phone
never resizes the Mac's terminal"). Today it is enforced by giving up
input. That is not needed. Input and size ownership are separate in
hesperd, and the renderer already has a full iOS input layer.

---

## 1. What exists today (in this repo)

| Piece | State | Source |
|---|---|---|
| PTY owner | hesperd `ptyhost.Term`, raw bytes in, raw bytes out | `relay/internal/ptyhost/term.go:97,245-258` |
| Remote attach | `agents.attach {link, ch, mode, request}` over the link, `D`/`X` frames on channel > 0, right `type` for `rw` | `relay/docs/protocol.md:455,477-498`, `relay/pkg/devicekey/rights.go:96-107` |
| Remote `rw` without owner | allowed: `req.Owner = req.Owner && req.Mode == wire.ModeRW` | `relay/internal/host/terminal.go:446` |
| Size rule | one size; the latest `rw`+`owner:true` attach owns it; when it detaches the size stays and fits take over | `relay/internal/ptyhost/viewer.go:84-89,139-146`, `docs/rebuild-contract.md:159-190` |
| `rw` view (daemon-rendered at the viewer's grid) | refused: "a view attach must be read-only" | `relay/internal/ptyhost/viewer.go:72-75` |
| Reflow | none; `vt` clips on resize, programs redraw on SIGWINCH | `relay/internal/vt/redraw.go:40-44` |
| Queries (DA, DSR, colours) | answered by hesperd and stripped, so viewers never answer them | `docs/rebuild-contract.md:682-688` |
| HesperLink | `Request` and `OpenLink` only; "Data and close frames … are ignored" | `relay/pkg/hesperlink/link.go:65-84` |
| Phone terminal | none: polls `agents.screen` every 2 s for 40 rows of plain text | `ios/Packages/HesperPhone/Sources/HesperPhone/Agent/DecisionScreenModel.swift:18-21,84-97` |
| Phone input | `agents.input {id, text, paste?, submit?}`, one signed request each; Esc only | `ios/Packages/HesperRemote/Sources/HesperRemote/RemoteHesper.swift:203-214` |
| Renderer | libghostty chosen, SwiftTerm the fallback; only rendering was checked | `ios/docs/renderer-decision.md` |
| Device family | iPhone only, portrait only | `ios/project.yml:69,88`, `ios/App/Info.plist:40-42` |

How the Mac does it. Each tile is a libghostty surface on the exec backend
running `hesperd attach <id> --owner|--fit`. libghostty encodes keys into
its local PTY, and the bridge forwards them as DATA
(`app/Sources/Hesper/Engine/GhosttySurfaceView.swift:207-219`,
`relay/internal/attachtty/attachtty.go`). The Mac rule "never `rw` without
`owner`" (`docs/rebuild-contract.md:1515-1521`) exists because a surface
on the exec backend takes its grid from its frame. A raw stream at another
size then lands in the wrong cells. The phone has no exec backend: it
feeds bytes itself, so it can make its surface the PTY's exact grid (§4,
Mirror).

---

## 2. Rendering

### Options

| Engine | iOS input | Used by | Verdict |
|---|---|---|---|
| **libghostty via `Lakr233/libghostty-spm`** (pin `2.2.2026100501`, as on the Mac) | `GhosttyTerminal.UITerminalView`: `UITextInput` with marked text (IME), priority `UIKeyCommand`s for Ctrl and Esc, hardware keys, key bar, loupe selection, momentum scroll, pointer | Hesper.app (Mac); gterm (fork) | **Use it** |
| SwiftTerm v1.99.x | `UIKeyInput` + `UITextInput` extension, `TerminalAccessory`, optional Metal | Secure ShellFish, La Terminal | Fallback |
| hterm in WKWebView | Blink's native keyboard layer on top | Blink Shell, a-Shell | No: web view, GPL (Blink) |
| Upstream Ghostty | embedded apprt has `GHOSTTY_PLATFORM_IOS`, but no host-fed IO and no iOS input | none | Not alone |

Sources: [libghostty-spm](https://github.com/Lakr233/libghostty-spm)
(`Sources/GhosttyTerminal/Platform/UIKit/*`,
`Patches/ghostty/0002-host-managed-io.patch`),
[SwiftTerm](https://github.com/migueldeicaza/SwiftTerm),
[Blink](https://github.com/blinksh/blink),
[a-Shell](https://github.com/holzschu/a-shell),
[Ghostty embedded apprt](https://github.com/ghostty-org/ghostty/blob/main/src/apprt/embedded.zig),
[Ghostty termio](https://github.com/ghostty-org/ghostty/tree/main/src/termio)
(only `Exec.zig`),
[Secure ShellFish](https://secureshellfish.app/help/terminal),
[gterm](https://madeye.github.io/gterm/). Termius and Prompt 3 don't
document their engines.

### Spike: libghostty input on iOS (this session)

A throwaway probe on the raw C API with
`GHOSTTY_SURFACE_IO_BACKEND_HOST_MANAGED`, in a scratch folder outside the
repo. It ran once on the Mac (an NSView never placed in a window) and once
on an iPhone 17 Simulator (booted with `simctl`, Simulator.app not
opened). The input → `receive_buffer` bytes were **identical on both**:

```
'h','i'                       68 69
Return / Tab / Backspace      0d / 09 / 7f
Ctrl-C / Esc                  03 / 1b
Up                            1b 5b 41      \e[A
Up after \e[?1h (DECCKM)      1b 4f 41      \eOA
surface_text "echo hi\nls"    \e[200~echo hi\nls\e[201~   (after \e[?2004h)
typed 'h' after 2004h         68            (key path, not bracketed)
kitty \e[>1u: Esc / Ctrl-C    \e[27u / \e[99;5u
SGR mouse click / scroll      \e[<0;2;1M\e[<0;2;1m / \e[<64;2;1M x3
\e[c / \e[6n fed              \e[?62;22;52c / \e[1;1R
drag selection                has_selection = true, read_selection ok
```

The phone gets the same key encoder as the Mac. It follows DECCKM,
bracketed paste, the kitty keyboard protocol and SGR mouse from the modes
in the stream, and hesperd's first redraw restores those modes
(`docs/rebuild-contract.md:663-675`). The bytes that come out are exactly
what ptyhost expects: raw input for the PTY. Query replies from the phone
must be dropped (hesperd answers them already), so the phone forwards
only bytes produced by user input. `ghostty_surface_write_buffer_replay`
(patch 0011) covers the redraw.

Not proven: `UITerminalView` driven by a real keyboard, IME and gestures,
and anything on a device.

### Cost per byte and per frame

Measured with the probe: 32 MiB fed in 64 KiB chunks, median of 3, and a
DSR reply after each run to check that parsing finished.

| Stream | libghostty, Mac | libghostty, iOS Simulator | SwiftTerm 1.99.0, Mac (headless) |
|---|---|---|---|
| Plain ASCII | ~680 MB/s | ~790 MB/s | ~680 MB/s |
| Truecolour SGR on every cell | ~198 MB/s | ~197 MB/s | ~145 MB/s |
| TUI repaint, 40×60, ~10.7 KB/frame | ~190 MB/s (56 µs/frame) | ~178 MB/s (60 µs/frame) | ~103 MB/s (104 µs/frame) |

The Simulator runs on the Mac's CPU, so neither column measures an
iPhone. SwiftTerm v1.99.2 does not compile with Swift 6.2.4
(`PowerlineRenderer.swift:386`), so v1.99.0 was used. Draw cost in the
Simulator is inconclusive: `ghostty_surface_draw` returns in microseconds,
because the real work is in the layer's display path (patch 0005). On the
Mac, an offscreen draw took about 0.8 ms.

**Estimate for the stream.** In a flood, about 31 content frames per
second reach a tile (`docs/rebuild-contract.md:1080-1098`). Taking that as
31 full ~10 KB repaints gives about 0.3 MB/s. That is **0.2% of one Mac
core to parse**. An A18/A19 core is in the same class, so allow a few
times that on a phone, still under 1%. Drawing on demand (wakeup →
render, display link paused when idle, capped at 60 Hz) costs at most
about 31 draws a second during a flood and none when idle. The radio and
the display cost far more battery than the terminal does. **To measure on
a device:** Instruments Metal System Trace and the Energy gauge while a
Claude Code session floods.

---

## 3. Input: iOS sources → ptyhost bytes

All rows go through libghostty's encoder (`ghostty_surface_key` /
`ghostty_surface_text`) and out of `receive_buffer` into one `D` frame on
the attach channel. ptyhost writes them to the PTY unchanged
(`relay/internal/ptyhost/term.go:245-258`).

| Source | iOS API | In libghostty-spm | Bytes |
|---|---|---|---|
| Software keyboard | `UITextInput.insertText` / `deleteBackward` | text as a key with `keycode 0xFFFF`; lone Return becomes the Enter key; backspace is a key (`UITerminalView+UITextInput.swift:65-149`, `TerminalTextInputHandler@UIKit.swift:115-138`) | UTF-8, `\r`, `\x7f` |
| Autocorrect, smart quotes, inline predictions | `UITextInputTraits` | all off, `inlinePredictionType` included (`UITerminalView+UITextInput.swift:15-60`) | none added |
| IME (Japanese, Chinese, Korean) | marked text | `setMarkedText` → `ghostty_surface_preedit`; `firstRect` from `ghostty_surface_ime_point` | committed text only |
| Key bar | `inputAccessoryView` | Esc, sticky Ctrl/Alt/Cmd, Tab, arrows, symbols, Paste (`TerminalInputBarKey.swift:9-20`) | `\e`, `\x03`, `\e[A` / `\eOA` … |
| Hardware keyboard | `pressesBegan` + `UIKey.keyCode` (HID) → macOS kVK | own key repeat; IME takes keys first (`UITerminalView+Keyboard.swift:97-326`) | mode-aware |
| Ctrl-letter, Esc on hardware | `UIKeyCommand` + `wantsPriorityOverSystemBehavior` | otherwise UIKit's text bindings eat Ctrl chords, and Esc resigns the keyboard (`UITerminalView+KeyCommands.swift:31,61`) | `\x01`…`\x1a`, `\e` |
| Cmd shortcuts | reserved by iPadOS (Cmd-Tab, Cmd-Space, Globe) | not reachable | none |
| Paste | `UIPasteControl` in the key bar (no prompt); edit menu | `paste_from_clipboard` binding, Ghostty's paste protection | bracketed when 2004 is on |
| Dictation | `UITextInput` (`insertText`, `insertDictationResult`) | multi-line text goes the paste route (`UITerminalView+UITextInput.swift:79`) | bracketed |
| Apple Pencil Scribble | works on any `UITextInput` view | comes in as `insertText` | text |
| GameController `GCKeyboard` | raw HID key down/up | not used; not needed | none |

Hesper-specific choices:
- Theme the key bar with `PhoneTheme`. Keep the concept's row (esc ⇥ ↑ ↓
  ⏎ ^C), add ← →, sticky ctrl, and a paste control.
- The keyboard type stays `.default`, as in both libraries; `.asciiCapable`
  would block IMEs.
- On iPad, Esc via Cmd-. is unreliable. Document Caps Lock → Escape in
  Settings › Keyboard › Hardware Keyboard.

Sources: Apple
[UITextInput](https://developer.apple.com/documentation/uikit/uitextinput),
[UITextInputTraits](https://developer.apple.com/documentation/uikit/uitextinputtraits),
[UIKey](https://developer.apple.com/documentation/uikit/uikey),
[UIKeyCommand.wantsPriorityOverSystemBehavior](https://developer.apple.com/documentation/uikit/uikeycommand/wantsprioritityoversystembehavior),
[UIPasteControl](https://developer.apple.com/documentation/uikit/uipastecontrol),
[GCKeyboard](https://developer.apple.com/documentation/gamecontroller/gckeyboard),
[UIIndirectScribbleInteraction](https://developer.apple.com/documentation/uikit/uiindirectscribbleinteractiondelegate).

---

## 4. Size ownership

The PTY has one size. The question is who sets it while the phone types.

| Option | What the phone sees | Mac tile | hesperd / protocol | Verdict |
|---|---|---|---|---|
| A. View only (concept) | `ro` view at the phone's grid, bottom-anchored, clipped | untouched | nothing | today's concept; not writable |
| **B. Mirror**: `rw`, no owner, at the PTY's grid | the Mac's exact grid, fit to width or pan and zoom, follows the cursor | untouched | **nothing** | **step 1** |
| **C. Take over**: `rw` owner while open | the agent at the phone's own grid; Claude Code lays out for it | becomes a view marked "On iPhone" | additive: who owns, a capability, the Mac reacting | **step 2** |
| D. `rw` view (daemon renders at the phone's grid) | a clipped window: a 120-col Claude Code cut to 46 cols | untouched | lift the "view must be ro" rule | no: clipping garbles wide TUIs |
| E. Reflow on the phone | — | — | `vt` cannot reflow; TUIs redraw cursor-addressed regions | no |

### B. Mirror (step 1)

- Attach with `agents.attach {link, ch, mode:"rw", request:{attach, mode:"rw", owner:false, history:true}}`.
  You get the scrollback preamble and a redraw at the PTY's size
  (`relay/internal/ptyhost/viewer.go:92-97`), then raw DATA and a SIZE
  frame on every resize.
- The phone **sets the libghostty surface to exactly `cols × rows`
  cells**: `ghostty_surface_set_size` in pixels taken from the cell size
  (`GHOSTTY_ACTION_CELL_SIZE`). Then it scales or pans that view, never the
  grid. This avoids the Mac's garbling, which came from the grid following
  the frame. **Check on a device:** that libghostty doesn't round the grid
  away from the requested size.
- Readability, estimated from the probe's cell (13 pt font → about
  10 × 23 pt cell): 120 cols fit an iPhone portrait width at about 4 pt
  (unreadable) and landscape at about 9 pt. So Mirror means landscape, or
  pinch-zoom plus pan with the viewport following the cursor while you
  type. With the keyboard up there are about 9 visible rows in landscape.
  It is good for a quick command, an Esc or ^C, a TUI menu. It is poor
  for reading.
- Several writers may type into one PTY
  (`docs/rebuild-contract.md:1378-1379`): the Mac and the phone can both
  type, as two Mac windows can.

### C. Take over (step 2)

The phone becomes the owner while the terminal screen is open and the
user has asked for it. In portrait at 11 pt that is about 46 × 19 cols ×
rows with the keyboard up. Claude Code redraws on SIGWINCH. The Mac never
garbles: it shows the agent as a fit view at its own pane size, bottom
anchored, with an "On iPhone" badge.

What is missing today: the Mac doesn't know it lost ownership. Its bridge
just follows SIZE (`relay/internal/attachtty/attachtty.go:55-58,134-137`),
and its surface keeps its own grid, so it would garble. When the phone
leaves, the size stays (`relay/internal/ptyhost/viewer.go:142-146`). So:

1. **hesperd knows who owns.** `Term` records the owner's origin (`local`,
   or the controller id of a remote device) and publishes it in the
   agent's info as `sizeOwner` (absent when local or nobody). It sends an
   agent event when the owner changes. This is additive; older clients
   ignore the field.
2. **Capability.** The snapshot sets `capabilities.sizeOwner: true`
   (`relay/docs/protocol.md:82`). The phone offers Take over only when the
   host sets it. An older hesperd leads to Mirror only.
3. **The Mac reacts** (`SizeOwnership`, `HesperCore/SizeOwner.swift`).
   While `sizeOwner` is remote, every local pane of that agent is a `--fit`
   view, so none garbles. When it clears, the existing debounced owner swap
   gives the key window's pane the owner attach again, and the PTY returns
   to the Mac's size. Typing or clicking in the tile takes the size back at
   once (latest owner wins). The phone sees the change in the same field
   and drops back to Mirror.
4. **The phone lets go.** It closes the channel (`X`) when the terminal
   screen goes away or the scene resigns active, inside a
   `beginBackgroundTask`. As a backstop, hesperd already drops a viewer
   that takes nothing for 30 s (`relay/internal/ptyhost/viewer.go:19`).
   Optional: release ownership after a shorter stall (say 10 s) for remote
   owners only.
5. **Keyboard.** Size the grid to the area above the keyboard and key bar,
   and debounce resizes on keyboard show and hide (300 ms, as fits do), so
   a quick dismissal doesn't redraw twice.

Nothing changes in the wire frames, the relay or the e2e layer. The
change goes in `relay/internal/ptyhost` (owner origin),
`relay/internal/host` (passing the controller id into the attach), the
agent info and its event, `relay/docs/protocol.md`,
`docs/rebuild-contract.md` (size rule) and the Mac's `SizeOwnership`.

---

## 5. Feel

- **Latency.** Keystroke to echo over the relay is p50 45 ms and p99 58 ms
  with 20 ms per leg; over the direct path (LAN, Noise IK) it is p50
  0.4 ms (`relay/docs/terminal-streaming.md:87-95`). On cellular to the
  relay, expect about 60–150 ms (unmeasured, spec risk §13). Measure it in
  M1 as planned.
- **Local echo.** Not in v1. Mosh predicts only after confirmation inside
  an epoch, shows predictions when SRTT > 30 ms, and underlines them above
  80 ms ([paper](https://mosh.org/mosh-paper.pdf),
  [`terminaloverlay.h:215-225`](https://github.com/mobile-shell/mosh/blob/master/src/frontend/terminaloverlay.h)).
  Claude Code hides the cursor and draws its own input box, so a predicted
  character at the terminal cursor would be wrong in exactly the main use
  case. Revisit only if measured cellular RTT is consistently above
  100 ms. Then use an adaptive overlay that is off in alt-screen and when
  the cursor is hidden. Long text keeps going through Type and Speak,
  which don't wait for an echo.
- **Selection and copy.** Long-press, handles and the system loupe from
  `UITerminalView`, copying to `UIPasteboard`. When a program captures the
  mouse, a tap goes to the program (`UITerminalView+Interaction.swift:216`).
  Drop OSC 52 clipboard reads (`clipboard-read = deny`, as the probe
  config already does).
- **Scrollback.** The `rw` attach preamble carries up to 10,000 lines /
  4 MiB (`docs/rebuild-contract.md:1126-1133`). Then a local one-finger
  pan with momentum. Mouse-capturing programs get wheel events instead.
- **Links.** libghostty detects URLs. Open them with a tap and
  `UIApplication.open` after a confirmation, never by themselves (a
  terminal can print anything).
- **Gestures.** Pinch zooms only in Mirror. Paging between agents stays
  off the terminal (critique: it fights panning).
- **Landscape.** The terminal screen allows landscape; the rest of the app
  stays portrait. It is needed for Mirror and nice for Take over.
- **iPad and Stage Manager.** The app is iPhone-only today. On an iPad with
  a hardware keyboard, Take over behaves like a Mac window: each scene
  resize is a RESIZE from the owner. External displays show the iPad's own
  window ([iPadOS 16.2+](https://www.macrumors.com/2022/06/06/apple-debuts-ipad-windows-and-full-display-support/)).
  On iPhone an external display mirrors unless the app adds an
  external-display scene; not worth it in v1.
- **Privacy.** The terminal screen is marked sensitive, as the spec
  already requires for conversation views (blurred in the app switcher).

---

## 6. Platform limits

- **Background.** iOS suspends the app shortly after it leaves the front.
  A suspended app runs no code, and there are no long-lived background
  sockets ([Apple DTS](https://developer.apple.com/forums/thread/685525)).
  So the terminal is foreground-only, as the concept already says. Detach,
  and release ownership, when the scene resigns active. On return,
  reattach: the first frame is a full redraw (45.7 ms to the first frame
  over the relay), so resuming is cheap. Blink's location trick to stay
  alive conflicts with guideline 2.5.4. Don't use it. Push and the 2 s
  poll cover the background, as today.
- **Signing.** Per-request signing stays as it is
  (`relay/pkg/client/controller.go:203-227`). A writable terminal costs
  **one** signed `agents.attach`, with right `type`, the ordinary device
  key and no Face ID. Keystrokes are `D` frames inside the sealed stream
  (ChaCha20-Poly1305, `relay/docs/protocol.md:333-344`). Nothing is signed
  per keystroke. Shell agents also need `shell`, which means the strong
  key and Face ID once per attach. Don't type through `agents.input`: each
  call is a Secure Enclave signature plus an ordered request round trip.
  No per-signature timing for the Secure Enclave is published; measure it
  on a device if it matters.
- **App Review.**
  - [2.5.2](https://developer.apple.com/app-store/review/guidelines/#2.5.2)
    is not a problem: all code runs on the owner's Mac, as with an SSH
    client.
  - The risk is
    [4.2.7](https://developer.apple.com/app-store/review/guidelines/#4.2.7)
    (remote desktop clients). Mirroring specific software rather than the
    whole host is limited to a user-owned machine "on a local and
    LAN-based network". A reviewer could read a relay-streamed agent
    terminal that way. SSH and Mosh terminals that reach the internet
    (Blink, Termius, Prompt, Moshi) are on the store, so terminals have not
    been treated as 4.2.7 apps so far (unverified).
  - In the review notes, present Hesper as a terminal and controller for
    the user's own Mac, with everything executed on the host. The 2020
    iSH episode (removal threatened, then withdrawn) shows that review can
    turn on wording ([iSH](https://ish.app/app-store-removal)).
- **Battery.** Render on demand: tick on wakeup, pause the display link
  when idle (renderer decision notes). Set `isSurfaceVisible = false` off
  screen. Detach in the background. No published numbers exist; measure
  with the Energy gauge.

---

## 7. Architecture

```
Phone (iOS 26)                         hesperd (Mac)
┌──────────────────────────────┐       ┌──────────────────────────────┐
│ HesperTerminal (new package) │       │ host: agents.attach          │
│  UITerminalView              │  D/X  │   → ptyhost.Term.Attach      │
│  (GhosttyTerminal, host IO)  │◄─────►│     rw, owner:false (Mirror) │
│  key bar · selection · zoom  │ ch>0  │     rw, owner:true  (Take    │
│        ▲ write   │ receive   │       │       over; records origin)  │
│        │         ▼ _buffer   │       │ agent info: sizeOwner        │
│ HesperLink: AttachChannel    │  E    │ snapshot: capabilities.      │
│  (gomobile; new)             │◄──────│   sizeOwner                  │
│ link (relay WS / direct)     │ ch 0  └──────────────────────────────┘
└──────────────────────────────┘               ▲ local attach
                                     Hesper.app SizeOwnership: remote
                                     owner → panes are --fit views
```

- **HesperLink** (`relay/pkg/hesperlink`): `Link.Attach(ch, mode, request)
  → *Channel` with `Write([]byte)`, `Resize(cols, rows)` (a RESIZE frame
  inside `D`), `Close()`, and a callback for DATA, SIZE, EXIT and SCROLL.
  It parses the local attach protocol inside `D` frames
  (`relay/pkg/wire/frame.go`). One terminal at a time
  (spec §8.4).
- **HesperTerminal** (iOS package, spec §8.1): wraps `UITerminalView`
  over `InMemoryTerminalSession(write:resize:)`. It sends output into
  `write_buffer`, sends user bytes into `Channel.Write`, and drops query
  replies. It has two sizing modes (Mirror: grid = PTY; Take over: grid =
  view). Theme from `PhoneTheme.terminal`.
- **Agent screen.** The transcript and decisions stay as designed. A
  "Terminal" control opens the writable terminal full screen, keyboard
  up, Mirror by default, with "Fit to iPhone" for Take over when the host
  supports it.

---

## 8. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| libghostty-spm is one maintainer's patches (host IO, iOS Metal, input) | upstream drift; fixes depend on one person | same pin as the Mac; SwiftTerm behind the same `HesperTerminal` API; the probe as a regression check |
| SwiftTerm v1.99.2 doesn't build with Swift 6.2.4 | the fallback needs a pin | pin v1.99.0 if it is ever used |
| Grid not exactly the PTY's in Mirror (rounding) | garbled TUI, as on the Mac | check on a device first; snap the surface size to whole cells |
| Mac and phone fight over the size | the PTY ping-pongs | only an explicit tap takes over; the Mac's latest owner wins; debounce; the phone drops to Mirror when it loses |
| A stale remote owner (phone suspended) | Mac stuck at the phone's size for up to 30 s | release on resign-active; optional shorter stall timeout for remote owners |
| Cellular latency | sluggish typing | Speak & Type for long text; measure; adaptive prediction later |
| App Review 4.2.7 | rejection | review notes; the terminal is a secondary screen, not the product's core |
| No device data yet | performance, battery, IME unknown | device spike before M4 (see effort) |
| App size | +19 MB static library (about 7.5 MB in the probe's Release binary, before thinning) | already accepted for the renderer decision |

---

## 9. Effort (one engineer, rough)

| Work | Days |
|---|---|
| Device spike: `UITerminalView` on a phone, keyboard, IME, key bar, grid exactness, Instruments | 2–3 |
| HesperLink attach channel (Go, gomobile, tests, `protocol.md` client notes) | 2–3 |
| `HesperTerminal` package: session, themes, key bar, selection, scrollback, links, sensitive view | 6–8 |
| Mirror: exact grid, fit/pan/zoom, cursor follow, landscape for the terminal screen | 4–5 |
| **Step 1 total** | **≈ 3 weeks** |
| hesperd: owner origin, `sizeOwner` in agent info and events, capability, tests, docs | 3–4 |
| Mac: `SizeOwnership` yields to a remote owner, "On iPhone" badge, take-back | 3–4 |
| Phone: Take over, keyboard-aware grid, release on background, fallback to Mirror | 2–3 |
| **Step 2 total** | **≈ 2 weeks** |

---

## 10. Decisions (owner, 2026-10-31)

1. **Take over is an explicit tap** ("Fit to iPhone") each time, never
   automatic. The Mac takes the size back on its next keystroke.
2. **iPad is in scope**, with hardware keyboards and iPadOS windowing. The
   concept's iPad section covers it.
3. **Landscape** for the agent view (the terminal screen). The rest of the
   app stays portrait on iPhone.
4. **The phone may type into shells**: right `shell`, Face ID and the
   strong key once per attach. Starting shells from the phone stays later.
   This changes the spec's phone rights (§2).
5. **libghostty-spm goes into the iOS app**, `GhosttyTerminal` product
   included (with its `Lakr233/DisplayLink` dependency, MIT), at the Mac's
   pin. This answers the open questions in
   `ios/docs/renderer-decision.md`.

These are folded into [`concept.md`](concept.md) ("The
terminal on a phone", "iPad", "Core work" 8–10).

Spike code (not committed) lives in the session's scratch folder: a Mac
probe, an iOS Simulator probe (XcodeGen) and a SwiftTerm benchmark,
against libghostty-spm `2.2.2026100501`.
